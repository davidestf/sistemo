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
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid bridge_subnet %q: %w", cidr, err)
	}
	// Gateway = network address + 1
	gw := make(net.IP, len(ip.To4()))
	copy(gw, ipNet.IP.To4())
	gw[3] = 1

	ones, _ := ipNet.Mask.Size()
	BridgeIP = gw.String()
	BridgeCIDR = cidr
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
	run("sysctl", "-w", "net.ipv4.ip_forward=1")

	// Allow localhost DNAT to work. Only set route_localnet on the bridge interface
	// — setting it on "all" can hijack SSH/SCP connections to remote hosts.
	run("sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.route_localnet=1", BridgeName))

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

	logger.Info("bridge ready",
		zap.String("bridge", BridgeName),
		zap.String("subnet", BridgeCIDR),
		zap.String("gateway", BridgeIP))
	return nil
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
