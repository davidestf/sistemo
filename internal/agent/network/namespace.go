// Package network handles network namespace creation and management.
//
// Architecture: each VM runs inside a network namespace for isolation. A shared
// Linux bridge (sistemo0, 10.200.0.0/16) connects all VMs. Each VM gets a unique
// IP from the bridge subnet. Inside each namespace, a small bridge (nb-*) connects
// the veth-in (tunnel to host bridge) with the TAP device (Firecracker's NIC).
//
//	Host:
//	  sistemo0 (10.200.0.1/16)
//	    ├── vo-<hash> ←veth→ vi-<hash> ─┬─ nb-<hash> ─ tap-<hash> ─ VM (10.200.0.X)
//	    ├── vo-<hash> ←veth→ vi-<hash> ─┬─ nb-<hash> ─ tap-<hash> ─ VM (10.200.0.Y)
//	    ...
package network

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	gonet "net"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// VMNetwork holds the network configuration for a VM.
type VMNetwork struct {
	NamespaceName string
	VethOut       string // host side, attached to host bridge
	VethIn        string // namespace side, attached to namespace bridge
	NsBridge      string // bridge inside namespace connecting veth-in and TAP
	TapName       string // TAP device for Firecracker
	VMIP          string // unique IP from bridge subnet (e.g. 10.200.0.5)
	HostBridge    string // host bridge to attach to (sistemo0 or br-<name>)
	BlockSMTP     bool   // block outbound SMTP (ports 25, 465, 587)
	Logger        *zap.Logger
}

func (n *VMNetwork) logger() *zap.Logger {
	if n.Logger != nil {
		return n.Logger
	}
	return zap.NewNop()
}

// getShortID returns a collision-resistant short ID for interface/namespace naming.
// Uses SHA-256 truncated to 8 hex chars (32 bits of a cryptographic hash).
// Linux interface names are max 15 chars: "vo-" + 8 = 11, "tap-" + 8 = 12. Safe.
func getShortID(vmID string) string {
	h := sha256.Sum256([]byte(vmID))
	return hex.EncodeToString(h[:])[:8]
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

// NewVMNetwork creates a VMNetwork for the given VM ID and its allocated IP.
// hostBridge is the host-side bridge to attach to. Empty string defaults to sistemo0.
func NewVMNetwork(vmID, vmIP string, logger *zap.Logger, hostBridge ...string) *VMNetwork {
	shortID := getShortID(vmID)
	bridge := BridgeName
	if len(hostBridge) > 0 && hostBridge[0] != "" {
		bridge = hostBridge[0]
	}
	return &VMNetwork{
		NamespaceName: "ns-" + shortID,
		VethOut:       "vo-" + shortID,
		VethIn:        "vi-" + shortID,
		NsBridge:      "nb-" + shortID,
		TapName:       "tap-" + shortID,
		VMIP:          vmIP,
		HostBridge:    bridge,
		Logger:        logger,
	}
}

// removeNamespaceAndVeth removes an existing namespace and its host veth.
func (n *VMNetwork) removeNamespaceAndVeth() {
	run("ip", "netns", "delete", n.NamespaceName)
	run("ip", "link", "delete", n.VethOut)
}

// Create sets up the full network namespace with veth pair, namespace bridge, and TAP device.
// The veth-out is attached to the sistemo0 host bridge. Inside the namespace, a bridge (nb-*)
// connects veth-in and the TAP device so the VM's traffic flows to the host bridge.
func (n *VMNetwork) Create() error {
	log := n.logger()

	// --- Create namespace ---
	if rc, out, _ := run("ip", "netns", "add", n.NamespaceName); rc != 0 {
		if strings.Contains(out, "File exists") {
			log.Info("removing existing namespace for clean start", zap.String("namespace", n.NamespaceName))
			n.removeNamespaceAndVeth()
			if rc2, out2, _ := run("ip", "netns", "add", n.NamespaceName); rc2 != 0 {
				return fmt.Errorf("failed to create namespace %s after cleanup: %s", n.NamespaceName, out2)
			}
		} else {
			return fmt.Errorf("failed to create namespace %s: %s", n.NamespaceName, out)
		}
	}
	log.Info("created namespace",
		zap.String("namespace", n.NamespaceName),
		zap.String("vm_ip", n.VMIP))

	// --- Create veth pair ---
	if rc, out, _ := run("ip", "link", "add", n.VethOut, "type", "veth", "peer", "name", n.VethIn); rc != 0 {
		if strings.Contains(out, "File exists") {
			log.Info("removing existing veth for clean start", zap.String("veth_out", n.VethOut))
			n.removeNamespaceAndVeth()
			if rc3, out3, _ := run("ip", "netns", "add", n.NamespaceName); rc3 != 0 {
				return fmt.Errorf("failed to re-create namespace %s after veth cleanup: %s", n.NamespaceName, out3)
			}
			if rc2, out2, _ := run("ip", "link", "add", n.VethOut, "type", "veth", "peer", "name", n.VethIn); rc2 != 0 {
				n.Cleanup("")
				return fmt.Errorf("failed to create veth pair after cleanup: %s", out2)
			}
		} else {
			n.Cleanup("")
			return fmt.Errorf("failed to create veth pair: %s", out)
		}
	}

	// --- Host side: attach veth-out to host bridge, bring up ---
	if rc, out, _ := run("ip", "link", "set", n.VethOut, "master", n.HostBridge); rc != 0 {
		n.Cleanup("")
		return fmt.Errorf("failed to attach veth to bridge: %s", out)
	}
	if rc, out, _ := run("ip", "link", "set", n.VethOut, "up"); rc != 0 {
		n.Cleanup("")
		return fmt.Errorf("failed to bring up veth-out: %s", out)
	}

	// --- Move veth-in into namespace ---
	if rc, out, _ := run("ip", "link", "set", n.VethIn, "netns", n.NamespaceName); rc != 0 {
		n.Cleanup("")
		return fmt.Errorf("failed to move veth to namespace: %s", out)
	}

	// --- Namespace side: create bridge, TAP, wire them together ---
	// Bring up loopback
	runInNamespace(n.NamespaceName, "ip", "link", "set", "lo", "up")

	// Create namespace-local bridge
	if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "link", "add", n.NsBridge, "type", "bridge"); rc != 0 {
		if !strings.Contains(out, "File exists") {
			n.Cleanup("")
			return fmt.Errorf("failed to create namespace bridge: %s", out)
		}
	}
	if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "link", "set", n.NsBridge, "up"); rc != 0 {
		n.Cleanup("")
		return fmt.Errorf("failed to bring up namespace bridge: %s", out)
	}

	// Attach veth-in to namespace bridge
	if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "link", "set", n.VethIn, "master", n.NsBridge); rc != 0 {
		n.Cleanup("")
		return fmt.Errorf("failed to attach veth-in to ns bridge: %s", out)
	}
	if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "link", "set", n.VethIn, "up"); rc != 0 {
		n.Cleanup("")
		return fmt.Errorf("failed to bring up veth-in: %s", out)
	}

	// Create TAP device and attach to namespace bridge
	if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "tuntap", "add", n.TapName, "mode", "tap"); rc != 0 {
		if !strings.Contains(out, "File exists") {
			n.Cleanup("")
			return fmt.Errorf("failed to create TAP device: %s", out)
		}
	}
	if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "link", "set", n.TapName, "master", n.NsBridge); rc != 0 {
		n.Cleanup("")
		return fmt.Errorf("failed to attach TAP to ns bridge: %s", out)
	}
	if rc, out, _ := runInNamespace(n.NamespaceName, "ip", "link", "set", n.TapName, "up"); rc != 0 {
		n.Cleanup("")
		return fmt.Errorf("failed to bring up TAP: %s", out)
	}

	// Block outbound SMTP (spam prevention) — rules auto-destroyed with namespace
	if n.BlockSMTP {
		for _, port := range []string{"25", "465", "587"} {
			runInNamespace(n.NamespaceName, "iptables", "-I", "FORWARD", "1",
				"-p", "tcp", "--dport", port, "-j", "DROP")
		}
	}

	log.Info("network setup complete",
		zap.String("namespace", n.NamespaceName),
		zap.String("bridge", n.HostBridge),
		zap.String("vm_ip", n.VMIP))
	return nil
}

// Cleanup removes the namespace and detaches the veth from the host bridge.
// Deleting the namespace auto-destroys: veth-in, namespace bridge, TAP, all namespace iptables.
func (n *VMNetwork) Cleanup(_ string) error {
	log := n.logger()
	log.Info("cleaning up namespace", zap.String("namespace", n.NamespaceName))

	vethOut := n.VethOut
	if vethOut == "" && strings.HasPrefix(n.NamespaceName, "ns-") {
		vethOut = "vo-" + strings.TrimPrefix(n.NamespaceName, "ns-")
	}

	// Delete namespace (auto-destroys veth-in, ns bridge, TAP, internal iptables)
	if rc, out, _ := run("ip", "netns", "delete", n.NamespaceName); rc != 0 {
		// "No such file" is expected when namespace was already cleaned (e.g. stop then delete)
		if !strings.Contains(out, "No such file") {
			log.Warn("failed to delete namespace", zap.String("ns", n.NamespaceName), zap.String("output", out))
		}
	}
	// Delete host-side veth (may already be gone when namespace was deleted)
	if vethOut != "" {
		run("ip", "link", "delete", vethOut)
	}
	return nil
}

// ExposePort adds DNAT rules to forward hostPort on the host to vmPort on this VM.
// Idempotent: if the rules already exist (e.g. after daemon restart), they are not duplicated.
func (n *VMNetwork) ExposePort(_ string, hostPort, vmPort int, protocol string) error {
	if hostPort < 1 || hostPort > 65535 || vmPort < 1 || vmPort > 65535 {
		return fmt.Errorf("port numbers must be 1-65535")
	}
	if protocol != "tcp" && protocol != "udp" {
		return fmt.Errorf("protocol must be tcp or udp")
	}
	if gonet.ParseIP(n.VMIP) == nil {
		return fmt.Errorf("invalid VM IP %q", n.VMIP)
	}
	hp := fmt.Sprintf("%d", hostPort)
	vp := fmt.Sprintf("%d", vmPort)
	dest := fmt.Sprintf("%s:%d", n.VMIP, vmPort)

	// DNAT: external traffic — check first, add only if missing.
	prArgs := []string{"!", "-i", n.HostBridge, "-p", protocol, "--dport", hp, "-j", "DNAT", "--to-destination", dest}
	addedPR := false
	if rc, _, _ := run("iptables", append([]string{"-t", "nat", "-C", "PREROUTING"}, prArgs...)...); rc != 0 {
		if rc, out, _ := run("iptables", append([]string{"-t", "nat", "-A", "PREROUTING"}, prArgs...)...); rc != 0 {
			return fmt.Errorf("PREROUTING DNAT failed: %s", out)
		}
		addedPR = true
	}

	// DNAT: localhost traffic — check first, add only if missing.
	outArgs := []string{"-p", protocol, "--dport", hp, "-d", "127.0.0.1", "-j", "DNAT", "--to-destination", dest}
	addedOUT := false
	if rc, _, _ := run("iptables", append([]string{"-t", "nat", "-C", "OUTPUT"}, outArgs...)...); rc != 0 {
		if rc, out, _ := run("iptables", append([]string{"-t", "nat", "-A", "OUTPUT"}, outArgs...)...); rc != 0 {
			// Rollback PREROUTING rule if we added it
			if addedPR {
				run("iptables", append([]string{"-t", "nat", "-D", "PREROUTING"}, prArgs...)...)
			}
			return fmt.Errorf("OUTPUT DNAT failed: %s", out)
		}
		addedOUT = true
	}

	// FORWARD: allow traffic to this VM — check first, add only if missing.
	fwdArgs := []string{"-d", n.VMIP, "-p", protocol, "--dport", vp, "-j", "ACCEPT"}
	if rc, _, _ := run("iptables", append([]string{"-C", "FORWARD"}, fwdArgs...)...); rc != 0 {
		if rc, out, _ := run("iptables", append([]string{"-I", "FORWARD", "1"}, fwdArgs...)...); rc != 0 {
			// Rollback previously added rules
			if addedPR {
				run("iptables", append([]string{"-t", "nat", "-D", "PREROUTING"}, prArgs...)...)
			}
			if addedOUT {
				run("iptables", append([]string{"-t", "nat", "-D", "OUTPUT"}, outArgs...)...)
			}
			return fmt.Errorf("FORWARD rule failed: %s", out)
		}
	}
	return nil
}

// UnexposePort removes the DNAT rules for the given hostPort.
func (n *VMNetwork) UnexposePort(_ string, hostPort, vmPort int, protocol string) error {
	hp := fmt.Sprintf("%d", hostPort)
	vp := fmt.Sprintf("%d", vmPort)
	dest := fmt.Sprintf("%s:%d", n.VMIP, vmPort)

	run("iptables", "-t", "nat", "-D", "PREROUTING",
		"!", "-i", n.HostBridge, "-p", protocol, "--dport", hp,
		"-j", "DNAT", "--to-destination", dest)
	run("iptables", "-t", "nat", "-D", "OUTPUT",
		"-p", protocol, "--dport", hp, "-d", "127.0.0.1",
		"-j", "DNAT", "--to-destination", dest)
	run("iptables", "-D", "FORWARD",
		"-d", n.VMIP, "-p", protocol, "--dport", vp,
		"-j", "ACCEPT")
	return nil
}

// CleanupPortRules removes all DNAT rules for the given port rules.
func (n *VMNetwork) CleanupPortRules(_ string, rules []PortRule) {
	for _, r := range rules {
		n.UnexposePort("", r.HostPort, r.VMPort, r.Protocol)
	}
}

// FlushDNATRulesForPort removes ALL DNAT rules targeting a specific host port,
// regardless of destination IP. This handles stale rules from old VMs whose IP
// changed after reboot or redeployment. Called on daemon startup before restoring
// port rules to ensure a clean iptables state.
//
// Uses iptables -D with line numbers from --line-numbers to delete by rule number
// (highest first to avoid index shifts).
func FlushDNATRulesForPort(hostPort int, protocol string) {
	hp := fmt.Sprintf("%d", hostPort)

	// Flush from PREROUTING and OUTPUT chains
	for _, chain := range []string{"PREROUTING", "OUTPUT"} {
		for attempt := 0; attempt < 50; attempt++ {
			// List rules with line numbers to find matches
			_, out, _ := run("iptables", "-t", "nat", "-L", chain, "-n", "--line-numbers")
			found := false
			for _, line := range strings.Split(out, "\n") {
				// Match lines containing our port and DNAT
				if !strings.Contains(line, "dpt:"+hp) || !strings.Contains(line, "DNAT") {
					continue
				}
				// Extract line number (first field)
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				lineNum := fields[0]
				if lineNum == "num" {
					continue // header row
				}
				// Delete by line number
				run("iptables", "-t", "nat", "-D", chain, lineNum)
				found = true
				break // restart scan since line numbers shifted
			}
			if !found {
				break
			}
		}
	}
}

// GetBootArgs returns the kernel boot args for static IP configuration.
// The VM configures its eth0 with the unique bridge IP and uses the bridge as gateway.
func GetBootArgs(vmIP string) string {
	// Use the actual netmask from the parsed bridge subnet
	mask := gonet.CIDRMask(16, 32) // default /16
	if bridgeIPNet != nil {
		mask = bridgeIPNet.Mask
	}
	netmask := fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
	return fmt.Sprintf("ip=%s::%s:%s::eth0:off", vmIP, BridgeIP, netmask)
}

// GetBootArgsForSubnet returns boot args for a VM on a named network with a specific subnet.
// The caller must ensure cidr is valid (validated at network creation time).
func GetBootArgsForSubnet(vmIP, cidr string) string {
	_, ipNet, err := gonet.ParseCIDR(cidr)
	if err != nil {
		// This should never happen — subnet is validated at network creation.
		// Fall back to default rather than crash, but log a warning.
		return GetBootArgs(vmIP)
	}
	// Gateway = network address + 1 (using 32-bit math for correctness with any prefix length)
	base := ipNet.IP.To4()
	baseU32 := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	gwU32 := baseU32 + 1
	gw := gonet.IP{byte(gwU32 >> 24), byte(gwU32 >> 16), byte(gwU32 >> 8), byte(gwU32)}
	ones, _ := ipNet.Mask.Size()
	// Convert prefix length to netmask
	mask := gonet.CIDRMask(ones, 32)
	netmask := fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
	return fmt.Sprintf("ip=%s::%s:%s::eth0:off", vmIP, gw.String(), netmask)
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
