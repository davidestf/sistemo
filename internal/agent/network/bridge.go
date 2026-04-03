package network

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"

	"go.uber.org/zap"
)

const (
	BridgeName = "sistemo0"

	// DefaultBridgeSubnet is used when the user doesn't configure bridge_subnet.
	// 10.200.0.0/16 avoids conflicts with common networks:
	// - Home routers: 192.168.0.0/16, 10.0.0.0/24
	// - Docker: 172.17.0.0/16
	// - VPNs (Tailscale/WireGuard): 100.64.0.0/10, 10.100.0.0/16
	// - Kubernetes: 10.96.0.0/12, 10.244.0.0/16
	// - AWS VPC: 10.0.0.0/16
	// Override in ~/.sistemo/config.yml with bridge_subnet: "10.50.0.0/16"
	DefaultBridgeSubnet = "10.200.0.0/16"
)

// Parsed bridge config — set by ParseBridgeSubnet, read by all network functions.
var (
	BridgeIP      = "10.200.0.1"
	BridgeCIDR    = "10.200.0.0/16"
	BridgeNetmask = "16"
	bridgeIPNet   *net.IPNet
)

// Package-level firewall instance, lazily initialized.
var (
	fw     Firewall
	fwOnce sync.Once
	fwErr  error
)

// GetFirewall returns the package-level firewall instance, initializing it on first call.
func GetFirewall(logger *zap.Logger) (Firewall, error) {
	fwOnce.Do(func() {
		fw, fwErr = NewNftFirewall(logger)
	})
	return fw, fwErr
}

// ParseBridgeSubnet parses a CIDR like "10.200.0.0/16" and sets the bridge globals.
// Gateway IP is .1 in the subnet. Must be called before EnsureBridge.
func ParseBridgeSubnet(cidr string) error {
	if cidr == "" {
		cidr = DefaultBridgeSubnet
	}
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid bridge_subnet %q: %w", cidr, err)
	}
	// Gateway = network address + 1 (using 32-bit math for correctness with any CIDR)
	base := ipNet.IP.To4()
	baseU32 := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	gwU32 := baseU32 + 1
	gw := net.IP{byte(gwU32 >> 24), byte(gwU32 >> 16), byte(gwU32 >> 8), byte(gwU32)}

	ones, _ := ipNet.Mask.Size()
	BridgeIP = gw.String()
	BridgeCIDR = ipNet.String() // canonical form, e.g. "10.200.0.0/16"
	BridgeNetmask = fmt.Sprintf("%d", ones)
	bridgeIPNet = ipNet
	return nil
}

// EnsureBridge creates the sistemo0 bridge if it doesn't exist, assigns an IP,
// and sets up NAT for outbound internet access from VMs.
func EnsureBridge(_ string, logger *zap.Logger) error {
	// Check required binaries
	for _, bin := range []string{"nft", "ip", "sysctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found — install it (e.g. apt install nftables iproute2 procps)", bin)
		}
	}

	// Initialize firewall
	firewall, err := GetFirewall(logger)
	if err != nil {
		return err
	}

	// Create bridge if not exists
	if rc, _, _ := run("ip", "link", "show", BridgeName); rc != 0 {
		if rc, out, _ := run("ip", "link", "add", BridgeName, "type", "bridge"); rc != 0 {
			return fmt.Errorf("create bridge %s: %s", BridgeName, out)
		}
		logger.Info("created bridge", zap.String("bridge", BridgeName))
	}

	// Assign IP if not already set
	rc, out, _ := run("ip", "addr", "show", BridgeName)
	if rc == 0 && !strings.Contains(out, BridgeIP+"/"+BridgeNetmask) {
		if rc, out, _ := run("ip", "addr", "add", BridgeIP+"/"+BridgeNetmask, "dev", BridgeName); rc != 0 {
			if !strings.Contains(out, "File exists") {
				return fmt.Errorf("assign IP to bridge: %s", out)
			}
		}
	}

	// Bring up
	if rc, out, _ := run("ip", "link", "set", BridgeName, "up"); rc != 0 {
		return fmt.Errorf("bring up bridge: %s", out)
	}

	// Enable ip_forward
	if rc, out, _ := run("sysctl", "-w", "net.ipv4.ip_forward=1"); rc != 0 {
		return fmt.Errorf("enable ip_forward: %s", out)
	}

	// Allow localhost DNAT to work. Only set route_localnet on the bridge interface
	// — setting it on "all" can hijack SSH/SCP connections to remote hosts.
	if rc, out, _ := run("sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.route_localnet=1", BridgeName)); rc != 0 {
		return fmt.Errorf("enable route_localnet on %s: %s", BridgeName, out)
	}

	// MASQUERADE: VMs → internet + localhost → VM
	if err := firewall.EnsureMasquerade(BridgeCIDR, BridgeName); err != nil {
		return fmt.Errorf("bridge masquerade: %w", err)
	}

	// FORWARD: bridge traffic rules
	if err := firewall.EnsureBridgeRules(BridgeName); err != nil {
		return fmt.Errorf("bridge forward rules: %w", err)
	}

	// Compat: insert accept rules into system filter tables (Debian/Ubuntu have
	// a default `table inet filter` with forward chain policy drop).
	if err := firewall.EnsureSystemForward(BridgeName); err != nil {
		logger.Warn("could not insert system forward rules (VMs may lack internet)", zap.Error(err))
	}

	logger.Info("bridge ready",
		zap.String("bridge", BridgeName),
		zap.String("subnet", BridgeCIDR),
		zap.String("gateway", BridgeIP))
	return nil
}

// CreateNamedBridge creates an isolated bridge for a named network.
// Each named bridge gets its own subnet, IP forwarding, and MASQUERADE rules.
func CreateNamedBridge(bridgeName, cidr string, logger *zap.Logger) error {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid subnet %q: %w", cidr, err)
	}

	firewall, err := GetFirewall(logger)
	if err != nil {
		return err
	}

	// Gateway = network address + 1 (using 32-bit math for correctness with any CIDR)
	base := ipNet.IP.To4()
	baseU32 := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	gwU32 := baseU32 + 1
	gw := net.IP{byte(gwU32 >> 24), byte(gwU32 >> 16), byte(gwU32 >> 8), byte(gwU32)}
	ones, _ := ipNet.Mask.Size()
	gwCIDR := fmt.Sprintf("%s/%d", gw.String(), ones)

	// Create bridge
	if rc, _, _ := run("ip", "link", "show", bridgeName); rc != 0 {
		if rc, out, _ := run("ip", "link", "add", bridgeName, "type", "bridge"); rc != 0 {
			return fmt.Errorf("create bridge %s: %s", bridgeName, out)
		}
	}

	// Assign IP
	rc, out, _ := run("ip", "addr", "show", bridgeName)
	if rc == 0 && !strings.Contains(out, gwCIDR) {
		if rc, out, _ := run("ip", "addr", "add", gwCIDR, "dev", bridgeName); rc != 0 {
			if !strings.Contains(out, "File exists") {
				return fmt.Errorf("assign IP to bridge %s: %s", bridgeName, out)
			}
		}
	}

	// Bring up
	if rc, out, _ := run("ip", "link", "set", bridgeName, "up"); rc != 0 {
		return fmt.Errorf("bring up bridge %s: %s", bridgeName, out)
	}

	// Enable route_localnet so localhost DNAT works for port forwarding.
	run("sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.route_localnet=1", bridgeName))

	// MASQUERADE for outbound + localhost
	if err := firewall.EnsureMasquerade(cidr, bridgeName); err != nil {
		return fmt.Errorf("named bridge masquerade: %w", err)
	}

	// FORWARD rules for this bridge
	if err := firewall.EnsureBridgeRules(bridgeName); err != nil {
		return fmt.Errorf("named bridge forward rules: %w", err)
	}

	// Compat: system filter table forward rules
	firewall.EnsureSystemForward(bridgeName)

	// ISOLATION: block traffic between this bridge and all other sistemo-managed bridges.
	if err := firewall.EnsureIsolation(bridgeName, BridgeName); err != nil {
		return fmt.Errorf("bridge isolation: %w", err)
	}
	for _, other := range ListNamedBridges() {
		if other != bridgeName {
			if err := firewall.EnsureIsolation(bridgeName, other); err != nil {
				return fmt.Errorf("bridge isolation %s<->%s: %w", bridgeName, other, err)
			}
		}
	}

	logger.Info("named bridge ready",
		zap.String("bridge", bridgeName),
		zap.String("subnet", cidr),
		zap.String("gateway", gw.String()))
	return nil
}

// DeleteNamedBridge removes a named bridge and its rules.
func DeleteNamedBridge(bridgeName string, logger *zap.Logger) {
	firewall, err := GetFirewall(logger)
	if err != nil {
		logger.Warn("cannot get firewall for cleanup", zap.Error(err))
		return
	}

	// Remove isolation rules first
	if err := firewall.RemoveIsolation(bridgeName); err != nil {
		logger.Warn("remove isolation rules", zap.Error(err))
	}

	// Get subnet from bridge IP to remove MASQUERADE rule.
	rc, out, _ := run("ip", "addr", "show", bridgeName)
	if rc == 0 {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "inet ") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					_, ipNet, err := net.ParseCIDR(fields[1])
					if err != nil {
						continue
					}
					networkCIDR := ipNet.String()
					if err := firewall.RemoveMasquerade(networkCIDR, bridgeName); err != nil {
						logger.Warn("remove masquerade", zap.Error(err))
					}
				}
			}
		}
	}

	if err := firewall.RemoveBridgeRules(bridgeName); err != nil {
		logger.Warn("remove bridge rules", zap.Error(err))
	}

	run("ip", "link", "set", bridgeName, "down")
	run("ip", "link", "delete", bridgeName)
	logger.Info("named bridge removed", zap.String("bridge", bridgeName))
}

// ListNamedBridges returns all br-* bridge names currently on the system.
func ListNamedBridges() []string {
	var bridges []string
	rc, out, _ := run("ip", "-o", "link", "show", "type", "bridge")
	if rc != 0 {
		return bridges
	}
	// Format: "3: br-backend: <FLAGS> ..."
	// The bridge name is always the second field, with a trailing colon.
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimRight(fields[1], ":")
		if strings.HasPrefix(name, "br-") {
			bridges = append(bridges, name)
		}
	}
	return bridges
}

// CleanupBridge removes the sistemo0 bridge and all sistemo nftables rules.
func CleanupBridge(hostInterface string, logger *zap.Logger) {
	firewall, err := GetFirewall(logger)
	if err == nil {
		if cleanErr := firewall.Cleanup(); cleanErr != nil {
			logger.Warn("nft cleanup", zap.Error(cleanErr))
		}
	}
	run("ip", "link", "set", BridgeName, "down")
	run("ip", "link", "delete", BridgeName)
	logger.Info("bridge removed", zap.String("bridge", BridgeName))
}
