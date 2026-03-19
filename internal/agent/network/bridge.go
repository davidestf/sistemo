package network

import (
	"fmt"
	"net"
	"strings"

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
func EnsureBridge(hostInterface string, logger *zap.Logger) error {
	if hostInterface == "" {
		hostInterface = "eth0"
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

	// MASQUERADE: VMs → internet. Use "! -o sistemo0" so the rule survives
	// WiFi reconnects and interface name changes.
	rc, _, _ = run("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", BridgeCIDR, "!", "-o", BridgeName, "-j", "MASQUERADE")
	if rc != 0 {
		if rc, out, _ := run("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", BridgeCIDR, "!", "-o", BridgeName, "-j", "MASQUERADE"); rc != 0 {
			return fmt.Errorf("bridge MASQUERADE rule: %s", out)
		}
	}
	// MASQUERADE: localhost → VM. Rewrite source from 127.0.0.1 to bridge IP.
	rc, _, _ = run("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", "127.0.0.0/8", "-o", BridgeName, "-j", "MASQUERADE")
	if rc != 0 {
		run("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "127.0.0.0/8", "-o", BridgeName, "-j", "MASQUERADE")
	}

	// FORWARD: allow bridge traffic out and return traffic back
	rc, _, _ = run("iptables", "-C", "FORWARD", "-i", BridgeName, "!", "-o", BridgeName, "-j", "ACCEPT")
	if rc != 0 {
		run("iptables", "-I", "FORWARD", "1", "-i", BridgeName, "!", "-o", BridgeName, "-j", "ACCEPT")
		run("iptables", "-I", "FORWARD", "1", "!", "-i", BridgeName, "-o", BridgeName, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}

	// FORWARD: allow VM-to-VM traffic on the bridge
	rc, _, _ = run("iptables", "-C", "FORWARD", "-i", BridgeName, "-o", BridgeName, "-j", "ACCEPT")
	if rc != 0 {
		run("iptables", "-I", "FORWARD", "1", "-i", BridgeName, "-o", BridgeName, "-j", "ACCEPT")
	}

	// Ensure isolation chain exists and is at position 1 in FORWARD.
	// Named bridges add their isolation rules here; the default bridge just ensures the chain.
	ensureIsolationChain()

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

	// MASQUERADE for outbound
	rc, _, _ = run("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", cidr, "!", "-o", bridgeName, "-j", "MASQUERADE")
	if rc != 0 {
		run("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", cidr, "!", "-o", bridgeName, "-j", "MASQUERADE")
	}

	// FORWARD: allow same-bridge traffic (VM-to-VM within this network)
	rc, _, _ = run("iptables", "-C", "FORWARD", "-i", bridgeName, "-o", bridgeName, "-j", "ACCEPT")
	if rc != 0 {
		run("iptables", "-I", "FORWARD", "1", "-i", bridgeName, "-o", bridgeName, "-j", "ACCEPT")
	}

	// FORWARD: allow outbound to internet and return traffic (but NOT to other bridges — isolation rules below handle that)
	rc, _, _ = run("iptables", "-C", "FORWARD", "-i", bridgeName, "!", "-o", bridgeName, "-j", "ACCEPT")
	if rc != 0 {
		run("iptables", "-I", "FORWARD", "1", "-i", bridgeName, "!", "-o", bridgeName, "-j", "ACCEPT")
		run("iptables", "-I", "FORWARD", "1", "!", "-i", bridgeName, "-o", bridgeName, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}

	// ISOLATION: block traffic between this bridge and all other sistemo-managed bridges.
	// This must be BEFORE the ACCEPT rules (higher priority = lower rule number).
	ensureIsolationChain()

	// Block traffic from this bridge to the default bridge and vice versa
	addIsolationRule(bridgeName, BridgeName)

	// Block traffic from this bridge to all other named bridges
	for _, other := range ListNamedBridges() {
		if other != bridgeName {
			addIsolationRule(bridgeName, other)
		}
	}

	logger.Info("named bridge ready",
		zap.String("bridge", bridgeName),
		zap.String("subnet", cidr),
		zap.String("gateway", gw.String()))
	return nil
}

// ensureIsolationChain creates the SISTEMO-ISOLATION chain and ensures it's at position 1 in FORWARD.
// This chain contains DROP rules between bridges to enforce network isolation.
func ensureIsolationChain() {
	// Create chain (ignore error if already exists)
	run("iptables", "-N", "SISTEMO-ISOLATION")

	// Check if the chain jump already exists in FORWARD
	rc, _, _ := run("iptables", "-C", "FORWARD", "-j", "SISTEMO-ISOLATION")
	if rc != 0 {
		// Not present at all — insert at position 1
		run("iptables", "-I", "FORWARD", "1", "-j", "SISTEMO-ISOLATION")
		return
	}

	// Already present — check if it's at position 1 by reading rule 1
	rc, out, _ := run("iptables", "-L", "FORWARD", "1", "-n")
	if rc == 0 && strings.Contains(out, "SISTEMO-ISOLATION") {
		return // already at position 1
	}

	// Move to position 1
	run("iptables", "-D", "FORWARD", "-j", "SISTEMO-ISOLATION")
	run("iptables", "-I", "FORWARD", "1", "-j", "SISTEMO-ISOLATION")
}

// addIsolationRule adds bidirectional DROP rules between two bridges.
func addIsolationRule(bridgeA, bridgeB string) {
	// A → B
	rc, _, _ := run("iptables", "-C", "SISTEMO-ISOLATION", "-i", bridgeA, "-o", bridgeB, "-j", "DROP")
	if rc != 0 {
		run("iptables", "-A", "SISTEMO-ISOLATION", "-i", bridgeA, "-o", bridgeB, "-j", "DROP")
	}
	// B → A
	rc, _, _ = run("iptables", "-C", "SISTEMO-ISOLATION", "-i", bridgeB, "-o", bridgeA, "-j", "DROP")
	if rc != 0 {
		run("iptables", "-A", "SISTEMO-ISOLATION", "-i", bridgeB, "-o", bridgeA, "-j", "DROP")
	}
}

// removeIsolationRules removes all isolation rules involving a bridge.
func removeIsolationRules(bridgeName string) {
	// Remove all rules in SISTEMO-ISOLATION that mention this bridge
	// Try removing rules involving this bridge with sistemo0 and all named bridges
	run("iptables", "-D", "SISTEMO-ISOLATION", "-i", bridgeName, "-o", BridgeName, "-j", "DROP")
	run("iptables", "-D", "SISTEMO-ISOLATION", "-i", BridgeName, "-o", bridgeName, "-j", "DROP")
	for _, other := range ListNamedBridges() {
		if other != bridgeName {
			run("iptables", "-D", "SISTEMO-ISOLATION", "-i", bridgeName, "-o", other, "-j", "DROP")
			run("iptables", "-D", "SISTEMO-ISOLATION", "-i", other, "-o", bridgeName, "-j", "DROP")
		}
	}
}

// DeleteNamedBridge removes a named bridge and its rules.
func DeleteNamedBridge(bridgeName string, logger *zap.Logger) {
	// Remove isolation rules first (before bridge disappears from ListNamedBridges)
	removeIsolationRules(bridgeName)

	// Get subnet from bridge IP to remove MASQUERADE rule.
	// ip addr show returns "10.201.0.1/24" (gateway+prefix), but the MASQUERADE rule
	// was created with the network CIDR "10.201.0.0/24". Convert to network form.
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
					networkCIDR := ipNet.String() // canonical network form
					run("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", networkCIDR, "!", "-o", bridgeName, "-j", "MASQUERADE")
				}
			}
		}
	}
	run("iptables", "-D", "FORWARD", "-i", bridgeName, "!", "-o", bridgeName, "-j", "ACCEPT")
	run("iptables", "-D", "FORWARD", "!", "-i", bridgeName, "-o", bridgeName, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	run("iptables", "-D", "FORWARD", "-i", bridgeName, "-o", bridgeName, "-j", "ACCEPT")
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

// CleanupBridge removes the sistemo0 bridge and its NAT rules.
func CleanupBridge(hostInterface string, logger *zap.Logger) {
	run("iptables", "-D", "FORWARD", "-i", BridgeName, "!", "-o", BridgeName, "-j", "ACCEPT")
	run("iptables", "-D", "FORWARD", "!", "-i", BridgeName, "-o", BridgeName, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	run("iptables", "-D", "FORWARD", "-i", BridgeName, "-o", BridgeName, "-j", "ACCEPT")
	run("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", BridgeCIDR, "!", "-o", BridgeName, "-j", "MASQUERADE")
	run("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", "127.0.0.0/8", "-o", BridgeName, "-j", "MASQUERADE")
	run("ip", "link", "set", BridgeName, "down")
	run("ip", "link", "delete", BridgeName)
	logger.Info("bridge removed", zap.String("bridge", BridgeName))
}
