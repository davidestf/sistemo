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
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// VMNetwork holds the network configuration for a VM.
type VMNetwork struct {
	NamespaceName string
	VethOut       string // host side, attached to sistemo0 bridge
	VethIn        string // namespace side, attached to namespace bridge
	NsBridge      string // bridge inside namespace connecting veth-in and TAP
	TapName       string // TAP device for Firecracker
	VMIP          string // unique IP from bridge subnet (e.g. 10.200.0.5)
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
func NewVMNetwork(vmID, vmIP string, logger *zap.Logger) *VMNetwork {
	shortID := getShortID(vmID)
	return &VMNetwork{
		NamespaceName: "ns-" + shortID,
		VethOut:       "vo-" + shortID,
		VethIn:        "vi-" + shortID,
		NsBridge:      "nb-" + shortID,
		TapName:       "tap-" + shortID,
		VMIP:          vmIP,
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

	// --- Host side: attach veth-out to sistemo0 bridge, bring up ---
	if rc, out, _ := run("ip", "link", "set", n.VethOut, "master", BridgeName); rc != 0 {
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
	for _, port := range []string{"25", "465", "587"} {
		runInNamespace(n.NamespaceName, "iptables", "-I", "FORWARD", "1",
			"-p", "tcp", "--dport", port, "-j", "DROP")
	}

	log.Info("network setup complete",
		zap.String("namespace", n.NamespaceName),
		zap.String("bridge", BridgeName),
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
		log.Warn("failed to delete namespace", zap.String("ns", n.NamespaceName), zap.String("output", out))
	}
	// Delete host-side veth (may already be gone when namespace was deleted)
	if vethOut != "" {
		run("ip", "link", "delete", vethOut)
	}
	return nil
}

// ExposePort adds DNAT rules to forward hostPort on the host to vmPort on this VM.
// Since each VM has a unique IP on the bridge subnet, this is a simple DNAT — no
// fwmark or policy routing needed.
func (n *VMNetwork) ExposePort(_ string, hostPort, vmPort int, protocol string) error {
	if hostPort < 1 || hostPort > 65535 || vmPort < 1 || vmPort > 65535 {
		return fmt.Errorf("port numbers must be 1-65535")
	}
	if protocol != "tcp" && protocol != "udp" {
		return fmt.Errorf("protocol must be tcp or udp")
	}
	hp := fmt.Sprintf("%d", hostPort)
	vp := fmt.Sprintf("%d", vmPort)
	dest := fmt.Sprintf("%s:%d", n.VMIP, vmPort)

	// DNAT: external traffic (any interface except the bridge itself)
	if rc, out, _ := run("iptables", "-t", "nat", "-A", "PREROUTING",
		"!", "-i", BridgeName, "-p", protocol, "--dport", hp,
		"-j", "DNAT", "--to-destination", dest); rc != 0 {
		return fmt.Errorf("PREROUTING DNAT failed: %s", out)
	}
	// DNAT: localhost traffic (curl localhost:80 from host)
	if rc, out, _ := run("iptables", "-t", "nat", "-A", "OUTPUT",
		"-p", protocol, "--dport", hp, "-d", "127.0.0.1",
		"-j", "DNAT", "--to-destination", dest); rc != 0 {
		run("iptables", "-t", "nat", "-D", "PREROUTING",
			"!", "-i", BridgeName, "-p", protocol, "--dport", hp,
			"-j", "DNAT", "--to-destination", dest)
		return fmt.Errorf("OUTPUT DNAT failed: %s", out)
	}
	// FORWARD: allow traffic to this VM (scoped by destination IP)
	if rc, out, _ := run("iptables", "-I", "FORWARD", "1",
		"-d", n.VMIP, "-p", protocol, "--dport", vp,
		"-j", "ACCEPT"); rc != 0 {
		run("iptables", "-t", "nat", "-D", "PREROUTING",
			"!", "-i", BridgeName, "-p", protocol, "--dport", hp,
			"-j", "DNAT", "--to-destination", dest)
		run("iptables", "-t", "nat", "-D", "OUTPUT",
			"-p", protocol, "--dport", hp, "-d", "127.0.0.1",
			"-j", "DNAT", "--to-destination", dest)
		return fmt.Errorf("FORWARD rule failed: %s", out)
	}
	return nil
}

// UnexposePort removes the DNAT rules for the given hostPort.
func (n *VMNetwork) UnexposePort(_ string, hostPort, vmPort int, protocol string) error {
	hp := fmt.Sprintf("%d", hostPort)
	vp := fmt.Sprintf("%d", vmPort)
	dest := fmt.Sprintf("%s:%d", n.VMIP, vmPort)

	run("iptables", "-t", "nat", "-D", "PREROUTING",
		"!", "-i", BridgeName, "-p", protocol, "--dport", hp,
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

// GetBootArgs returns the kernel boot args for static IP configuration.
// The VM configures its eth0 with the unique bridge IP and uses sistemo0 as gateway.
func GetBootArgs(vmIP string) string {
	return fmt.Sprintf("ip=%s::%s:255.255.0.0::eth0:off", vmIP, BridgeIP)
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
