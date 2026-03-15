// Package network handles network namespace creation and management.
package network

import (
	"fmt"
	"hash/fnv"
	"os/exec"
	"strings"
	"sync"

	"go.uber.org/zap"
)

const (
	GatewayIP = "10.0.0.1"
	VMIP      = "10.0.0.2"
	Netmask   = "24"
)

// VMNetwork holds the network configuration for a VM.
type VMNetwork struct {
	NamespaceName string
	VethOut       string // host side
	VethIn        string // namespace side
	TapName       string
	GatewayIP     string
	VMIP          string
	VethHostIP    string // unique per namespace: 10.200.X.1
	VethNsIP      string // unique per namespace: 10.200.X.2
	Logger        *zap.Logger
}

func (n *VMNetwork) logger() *zap.Logger {
	if n.Logger != nil {
		return n.Logger
	}
	return zap.NewNop()
}

func getShortID(vmID string) string {
	h := fnv.New32a()
	h.Write([]byte(vmID))
	return fmt.Sprintf("%08x", h.Sum32())
}

// getUniqueSubnet returns (third_octet, fourth_base) for a /30 subnet in 10.200.0.0/16.
// This yields ~16K unique subnets (256 * 64) instead of 254.
func getUniqueSubnet(vmID string) (int, int) {
	h := fnv.New32a()
	h.Write([]byte(vmID))
	index := int(h.Sum32() % 16384)
	third := index / 64
	fourthBase := (index % 64) * 4
	return third, fourthBase
}

func run(name string, args ...string) (int, string, string) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return exitCode, string(out), ""
}

func runInNamespace(nsName string, name string, args ...string) (int, string, string) {
	fullArgs := append([]string{"netns", "exec", nsName, name}, args...)
	return run("ip", fullArgs...)
}

// NewVMNetwork creates a VMNetwork config for the given VM ID.
func NewVMNetwork(vmID string, logger *zap.Logger) *VMNetwork {
	shortID := getShortID(vmID)
	third, fourthBase := getUniqueSubnet(vmID)
	return &VMNetwork{
		NamespaceName: "ns-" + shortID,
		VethOut:       "vo-" + shortID,
		VethIn:        "vi-" + shortID,
		TapName:       "tap-" + shortID,
		GatewayIP:     GatewayIP,
		VMIP:          VMIP,
		VethHostIP:    fmt.Sprintf("10.200.%d.%d", third, fourthBase+1),
		VethNsIP:      fmt.Sprintf("10.200.%d.%d", third, fourthBase+2),
		Logger:        logger,
	}
}

// NewVMNetworkWithTap creates a VMNetwork with a specific TAP name (for snapshot restore).
func NewVMNetworkWithTap(vmID string, tapName string, logger *zap.Logger) *VMNetwork {
	shortID := getShortID(vmID)
	third, fourthBase := getUniqueSubnet(vmID)
	return &VMNetwork{
		NamespaceName: "ns-" + shortID,
		VethOut:       "vo-" + shortID,
		VethIn:        "vi-" + shortID,
		TapName:       tapName,
		GatewayIP:     GatewayIP,
		VMIP:          VMIP,
		VethHostIP:    fmt.Sprintf("10.200.%d.%d", third, fourthBase+1),
		VethNsIP:      fmt.Sprintf("10.200.%d.%d", third, fourthBase+2),
		Logger:        logger,
	}
}

// removeNamespaceAndVeth removes an existing namespace and its host veth so Create() can run on a clean slate.
// Use when the namespace or veth already exist from a previous run (e.g. VM was stopped but namespace left behind).
func (n *VMNetwork) removeNamespaceAndVeth() {
	run("ip", "netns", "delete", n.NamespaceName)
	run("ip", "link", "delete", n.VethOut)
}

// Create sets up the full network namespace with veth pair and TAP device.
// If the namespace or veth already exist (e.g. after a previous stop), they are removed and recreated.
func (n *VMNetwork) Create() error {
	if rc, out, _ := run("ip", "netns", "add", n.NamespaceName); rc != 0 {
		if strings.Contains(out, "File exists") {
			n.logger().Info("removing existing namespace for clean start", zap.String("namespace", n.NamespaceName))
			n.removeNamespaceAndVeth()
			if rc2, out2, _ := run("ip", "netns", "add", n.NamespaceName); rc2 != 0 {
				return fmt.Errorf("failed to create namespace %s after cleanup: %s", n.NamespaceName, out2)
			}
		} else {
			return fmt.Errorf("failed to create namespace %s: %s", n.NamespaceName, out)
		}
	}
	n.logger().Info("created namespace",
		zap.String("namespace", n.NamespaceName),
		zap.String("veth_host_ip", n.VethHostIP),
		zap.String("veth_ns_ip", n.VethNsIP))

	if rc, out, _ := run("ip", "link", "add", n.VethOut, "type", "veth", "peer", "name", n.VethIn); rc != 0 {
		if strings.Contains(out, "File exists") {
			n.logger().Info("removing existing veth for clean start", zap.String("veth_out", n.VethOut))
			n.removeNamespaceAndVeth()
			run("ip", "netns", "add", n.NamespaceName)
			if rc2, out2, _ := run("ip", "link", "add", n.VethOut, "type", "veth", "peer", "name", n.VethIn); rc2 != 0 {
				n.Cleanup("")
				return fmt.Errorf("failed to create veth pair after cleanup: %s", out2)
			}
		} else {
			n.Cleanup("")
			return fmt.Errorf("failed to create veth pair: %s", out)
		}
	}

	if rc, out, _ := run("ip", "link", "set", n.VethIn, "netns", n.NamespaceName); rc != 0 {
		n.Cleanup("")
		return fmt.Errorf("failed to move veth to namespace: %s", out)
	}

	// Host-side and namespace-side config are independent after veth move — run in parallel
	var wg sync.WaitGroup
	var hostErr, nsErr error

	wg.Add(2)

	// Host-side: configure veth-out
	go func() {
		defer wg.Done()
		if rc, out, _ := run("ip", "addr", "add", n.VethHostIP+"/30", "dev", n.VethOut); rc != 0 {
			if !strings.Contains(out, "File exists") {
				hostErr = fmt.Errorf("failed to configure veth-out IP: %s", out)
				return
			}
		}
		if rc, out, _ := run("ip", "link", "set", n.VethOut, "up"); rc != 0 {
			hostErr = fmt.Errorf("failed to bring up veth-out: %s", out)
		}
	}()

	// Namespace-side: configure veth-in, loopback, TAP, sysctl, route, iptables
	go func() {
		defer wg.Done()
		if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "addr", "add", n.VethNsIP+"/30", "dev", n.VethIn); rc != 0 {
			if !strings.Contains(out, "File exists") {
				nsErr = fmt.Errorf("failed to configure veth-in IP: %s", out)
				return
			}
		}
		if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "link", "set", n.VethIn, "up"); rc != 0 {
			nsErr = fmt.Errorf("failed to bring up veth-in: %s", out)
			return
		}
		runInNamespace(n.NamespaceName, "ip", "link", "set", "lo", "up")

		if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "tuntap", "add", n.TapName, "mode", "tap"); rc != 0 {
			if !strings.Contains(out, "File exists") {
				nsErr = fmt.Errorf("failed to create TAP device: %s", out)
				return
			}
		}

		if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "addr", "add", n.GatewayIP+"/"+Netmask, "dev", n.TapName); rc != 0 {
			if !strings.Contains(out, "File exists") {
				nsErr = fmt.Errorf("failed to configure TAP IP: %s", out)
				return
			}
		}

		if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "link", "set", n.TapName, "up"); rc != 0 {
			nsErr = fmt.Errorf("failed to bring up TAP device: %s", out)
			return
		}
		runInNamespace(n.NamespaceName, "sysctl", "-w", "net.ipv4.ip_forward=1")
		runInNamespace(n.NamespaceName, "ip", "route", "add", "default", "via", n.VethHostIP, "dev", n.VethIn)
		runInNamespace(n.NamespaceName, "iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "10.0.0.0/24", "-o", n.VethIn, "-j", "MASQUERADE")

		// Block outbound SMTP (spam prevention) — rules auto-destroyed with namespace
		// Use -I (insert at position 1) so DROP rules take priority over any ACCEPT rules
		for _, port := range []string{"25", "465", "587"} {
			runInNamespace(n.NamespaceName, "iptables", "-I", "FORWARD", "1",
				"-p", "tcp", "--dport", port, "-j", "DROP")
		}
	}()

	wg.Wait()

	if hostErr != nil {
		n.Cleanup("") // no iptables yet (SetupNAT not called)
		return hostErr
	}
	if nsErr != nil {
		n.Cleanup("") // no iptables yet
		return nsErr
	}

	n.logger().Info("network setup complete",
		zap.String("gateway", n.GatewayIP),
		zap.String("vm_ip", n.VMIP))
	return nil
}

// SetupNAT configures NAT for outbound traffic from the namespace.
func (n *VMNetwork) SetupNAT(hostInterface string) error {
	run("sysctl", "-w", "net.ipv4.ip_forward=1")
	rc, _, _ := run("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", "10.200.0.0/16", "-o", hostInterface, "-j", "MASQUERADE")
	if rc != 0 {
		if rc2, out, _ := run("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "10.200.0.0/16", "-o", hostInterface, "-j", "MASQUERADE"); rc2 != 0 {
			return fmt.Errorf("failed to add NAT rule: %s", out)
		}
	}
	// Use -I (insert at top) so VM rules take priority over Docker's FORWARD DROP policy.
	// Use -C (check) first to prevent rule accumulation on repeated calls.
	rc, _, _ = run("iptables", "-C", "FORWARD", "-i", n.VethOut, "-o", hostInterface, "-j", "ACCEPT")
	if rc != 0 {
		run("iptables", "-I", "FORWARD", "1", "-i", n.VethOut, "-o", hostInterface, "-j", "ACCEPT")
		run("iptables", "-I", "FORWARD", "1", "-i", hostInterface, "-o", n.VethOut, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}
	return nil
}

// Cleanup removes the namespace and all associated network devices.
// hostInterface is the host's default route interface (e.g. eth0, wlp1s0) used when adding FORWARD rules; use it so cleanup removes the correct rules.
func (n *VMNetwork) Cleanup(hostInterface string) error {
	n.logger().Info("cleaning up namespace", zap.String("namespace", n.NamespaceName))
	vethOut := n.VethOut
	if vethOut == "" && strings.HasPrefix(n.NamespaceName, "ns-") {
		vethOut = "vo-" + strings.TrimPrefix(n.NamespaceName, "ns-")
	}
	if hostInterface == "" {
		hostInterface = "eth0"
	}
	// Clean up host FORWARD rules for this veth (-D silently fails if rule doesn't exist)
	if vethOut != "" {
		run("iptables", "-D", "FORWARD", "-i", vethOut, "-o", hostInterface, "-j", "ACCEPT")
		run("iptables", "-D", "FORWARD", "-i", hostInterface, "-o", vethOut, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}
	run("ip", "netns", "delete", n.NamespaceName)
	if vethOut != "" {
		run("ip", "link", "delete", vethOut)
	}
	return nil
}

// GetBootArgs returns the kernel boot args for static IP configuration.
func GetBootArgs() string {
	return fmt.Sprintf("ip=%s::%s:255.255.255.0::eth0:off", VMIP, GatewayIP)
}

// CleanupAllNamespaces removes all ns-* namespaces and their veth/iptables (for startup cleanup).
// preserve is an optional set of namespace names to keep (e.g. namespaces of VMs rehydrated from disk).
func CleanupAllNamespaces(hostInterface string, logger *zap.Logger, preserve map[string]struct{}) {
	if hostInterface == "" {
		hostInterface = "eth0"
	}
	rc, out, _ := run("ip", "netns", "list")
	if rc != 0 {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) > 0 && strings.HasPrefix(parts[0], "ns-") {
			nsName := parts[0]
			if preserve != nil {
				if _, keep := preserve[nsName]; keep {
					continue
				}
			}
			logger.Info("cleaning up stale namespace", zap.String("namespace", nsName))
			ns := &VMNetwork{NamespaceName: nsName, Logger: logger}
			ns.Cleanup(hostInterface)
		}
	}
}
