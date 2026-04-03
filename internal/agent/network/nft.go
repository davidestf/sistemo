package network

import (
	"encoding/json"
	"fmt"
	gonet "net"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// safeBridgeName matches valid Linux interface names (alphanumeric, hyphens, max 15 chars).
var safeBridgeName = regexp.MustCompile(`^[a-zA-Z0-9][-a-zA-Z0-9]{0,14}$`)

// validateNftInputs validates all parameters before they're interpolated into nft rule strings.
// This is defense-in-depth — callers also validate, but this prevents command injection
// if a new caller is added without proper validation.
func validateNftInputs(ip, protocol, bridge string, ports ...int) error {
	if ip != "" && gonet.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP address %q", ip)
	}
	if protocol != "" && protocol != "tcp" && protocol != "udp" {
		return fmt.Errorf("protocol must be tcp or udp, got %q", protocol)
	}
	if bridge != "" && !safeBridgeName.MatchString(bridge) {
		return fmt.Errorf("invalid bridge name %q", bridge)
	}
	for _, p := range ports {
		if p != 0 && (p < 1 || p > 65535) {
			return fmt.Errorf("port %d out of range 1-65535", p)
		}
	}
	return nil
}

// NftFirewall manages firewall rules using native nftables.
// All rules live under "table inet sistemo" with comment-based tagging.
type NftFirewall struct {
	mu     sync.Mutex
	logger *zap.Logger
}

// NewNftFirewall creates a new nftables firewall manager and ensures
// the base table and chains exist.
func NewNftFirewall(logger *zap.Logger) (*NftFirewall, error) {
	if _, err := exec.LookPath("nft"); err != nil {
		return nil, fmt.Errorf("nft not found — install nftables (e.g. apt install nftables)")
	}

	fw := &NftFirewall{logger: logger}
	if err := fw.ensureTable(); err != nil {
		return nil, fmt.Errorf("nftables init: %w", err)
	}
	return fw, nil
}

// ensureTable creates the sistemo table and chains if they don't exist.
// nft merges into existing tables, so this is idempotent.
func (fw *NftFirewall) ensureTable() error {
	// Use nft -f - to atomically create the table structure.
	// "add" is idempotent — it doesn't error if the object already exists.
	ruleset := `
add table inet sistemo
add chain inet sistemo sistemo-prerouting { type nat hook prerouting priority dstnat; policy accept; }
add chain inet sistemo sistemo-output { type nat hook output priority dstnat; policy accept; }
add chain inet sistemo sistemo-postrouting { type nat hook postrouting priority srcnat; policy accept; }
add chain inet sistemo sistemo-forward { type filter hook forward priority filter; policy accept; }
add chain inet sistemo sistemo-isolation
`
	return fw.nftApply(ruleset)
}

func (fw *NftFirewall) EnsureMasquerade(subnet, bridge string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	tag := "sistemo:" + bridge + ":masq"
	tagLo := "sistemo:" + bridge + ":masq-lo"

	// Check if rules already exist
	if fw.hasRuleWithComment("sistemo-postrouting", tag) {
		return nil
	}

	rules := fmt.Sprintf(`
add rule inet sistemo sistemo-postrouting ip saddr %s oifname != "%s" masquerade comment "%s"
add rule inet sistemo sistemo-postrouting ip saddr 127.0.0.0/8 oifname "%s" masquerade comment "%s"
`, subnet, bridge, tag, bridge, tagLo)

	return fw.nftApply(rules)
}

func (fw *NftFirewall) RemoveMasquerade(subnet, bridge string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	tag := "sistemo:" + bridge + ":masq"
	tagLo := "sistemo:" + bridge + ":masq-lo"

	fw.deleteRulesWithComment("sistemo-postrouting", tag)
	fw.deleteRulesWithComment("sistemo-postrouting", tagLo)
	return nil
}

func (fw *NftFirewall) EnsureBridgeRules(bridge string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	tag := "sistemo:" + bridge + ":fwd"

	if fw.hasRuleWithComment("sistemo-forward", tag) {
		return nil
	}

	// Three FORWARD rules:
	// 1. Intra-bridge (VM-to-VM)
	// 2. Outbound (bridge → other interfaces)
	// 3. Return traffic (established/related back to bridge)
	rules := fmt.Sprintf(`
add rule inet sistemo sistemo-forward iifname "%s" oifname "%s" accept comment "%s"
add rule inet sistemo sistemo-forward iifname "%s" oifname != "%s" accept comment "%s"
add rule inet sistemo sistemo-forward iifname != "%s" oifname "%s" ct state established,related accept comment "%s"
`, bridge, bridge, tag, bridge, bridge, tag, bridge, bridge, tag)

	return fw.nftApply(rules)
}

func (fw *NftFirewall) RemoveBridgeRules(bridge string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	fw.deleteRulesWithComment("sistemo-forward", "sistemo:"+bridge+":fwd")
	return nil
}

func (fw *NftFirewall) AddDNAT(hostPort, machinePort int, machineIP, protocol, bridge string) error {
	// Validate all inputs before interpolating into nft rule strings.
	if err := validateNftInputs(machineIP, protocol, bridge, hostPort, machinePort); err != nil {
		return fmt.Errorf("AddDNAT validation: %w", err)
	}

	fw.mu.Lock()
	defer fw.mu.Unlock()

	tag := fmt.Sprintf("sistemo:%s:dnat:%d:%s", bridge, hostPort, protocol)

	if fw.hasRuleWithComment("sistemo-prerouting", tag) {
		return nil // already exists (idempotent)
	}

	dest := fmt.Sprintf("%s:%d", machineIP, machinePort)

	// Atomic: all three rules or none.
	// "dnat ip to" is required in inet family tables to disambiguate from ip6.
	// Localhost DNAT goes in OUTPUT chain (local traffic doesn't hit PREROUTING).
	rules := fmt.Sprintf(`
add rule inet sistemo sistemo-prerouting iifname != "%s" %s dport %d dnat ip to %s comment "%s"
add rule inet sistemo sistemo-output ip daddr 127.0.0.1 %s dport %d dnat ip to %s comment "%s"
add rule inet sistemo sistemo-forward ip daddr %s %s dport %d accept comment "%s"
`, bridge, protocol, hostPort, dest, tag,
		protocol, hostPort, dest, tag,
		machineIP, protocol, machinePort, tag)

	return fw.nftApply(rules)
}

func (fw *NftFirewall) RemoveDNAT(hostPort, machinePort int, machineIP, protocol string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	// Find rules by matching the port and IP in any bridge's DNAT tag.
	// We search all DNAT comments for this host port.
	suffix := fmt.Sprintf(":dnat:%d:%s", hostPort, protocol)

	// Remove from prerouting, output (localhost DNAT), and forward
	fw.deleteRulesMatchingCommentSuffix("sistemo-prerouting", suffix)
	fw.deleteRulesMatchingCommentSuffix("sistemo-output", suffix)
	fw.deleteRulesMatchingCommentSuffix("sistemo-forward", suffix)

	return nil
}

func (fw *NftFirewall) FlushDNATForPort(hostPort int, protocol string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	suffix := fmt.Sprintf(":dnat:%d:%s", hostPort, protocol)

	fw.deleteRulesMatchingCommentSuffix("sistemo-prerouting", suffix)
	fw.deleteRulesMatchingCommentSuffix("sistemo-output", suffix)
	fw.deleteRulesMatchingCommentSuffix("sistemo-forward", suffix)
	return nil
}

func (fw *NftFirewall) AddForward(machineIP string, port int, protocol string) error {
	if err := validateNftInputs(machineIP, protocol, "", port, 0); err != nil {
		return fmt.Errorf("AddForward validation: %w", err)
	}

	fw.mu.Lock()
	defer fw.mu.Unlock()

	tag := fmt.Sprintf("sistemo:fwd:%s:%d:%s", machineIP, port, protocol)

	if fw.hasRuleWithComment("sistemo-forward", tag) {
		return nil
	}

	rule := fmt.Sprintf(`add rule inet sistemo sistemo-forward ip daddr %s %s dport %d accept comment "%s"`,
		machineIP, protocol, port, tag)

	return fw.nftApply(rule)
}

func (fw *NftFirewall) RemoveForward(machineIP string, port int, protocol string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	tag := fmt.Sprintf("sistemo:fwd:%s:%d:%s", machineIP, port, protocol)
	fw.deleteRulesWithComment("sistemo-forward", tag)
	return nil
}

func (fw *NftFirewall) EnsureIsolation(bridgeA, bridgeB string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	tagAB := fmt.Sprintf("sistemo:isolate:%s:%s", bridgeA, bridgeB)
	tagBA := fmt.Sprintf("sistemo:isolate:%s:%s", bridgeB, bridgeA)

	if fw.hasRuleWithComment("sistemo-isolation", tagAB) {
		return nil
	}

	// Bidirectional DROP + jump from forward chain to isolation chain.
	// First ensure the jump exists.
	jumpTag := "sistemo:isolation-jump"
	var rules string
	if !fw.hasRuleWithComment("sistemo-forward", jumpTag) {
		// Insert isolation jump at the beginning of sistemo-forward.
		// nft "insert" places it at position 0 (highest priority).
		rules += fmt.Sprintf("insert rule inet sistemo sistemo-forward jump sistemo-isolation comment \"%s\"\n", jumpTag)
	}

	rules += fmt.Sprintf(`add rule inet sistemo sistemo-isolation iifname "%s" oifname "%s" drop comment "%s"
add rule inet sistemo sistemo-isolation iifname "%s" oifname "%s" drop comment "%s"
`, bridgeA, bridgeB, tagAB, bridgeB, bridgeA, tagBA)

	return fw.nftApply(rules)
}

func (fw *NftFirewall) RemoveIsolation(bridge string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	// Remove all isolation rules mentioning this bridge (as either source or destination).
	fw.deleteRulesContainingBridge("sistemo-isolation", bridge)
	return nil
}

func (fw *NftFirewall) BlockSMTPInNamespace(namespace string) error {
	// Create a minimal nftables ruleset inside the network namespace.
	// The namespace is destroyed when the VM is cleaned up, which auto-removes these rules.
	ruleset := `
add table inet sistemo-smtp
add chain inet sistemo-smtp block-smtp { type filter hook forward priority filter; policy accept; }
add rule inet sistemo-smtp block-smtp tcp dport 25 drop
add rule inet sistemo-smtp block-smtp tcp dport 465 drop
add rule inet sistemo-smtp block-smtp tcp dport 587 drop
`
	cmd := exec.Command("ip", "netns", "exec", namespace, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("block SMTP in namespace %s: %s", namespace, string(out))
	}
	return nil
}

func (fw *NftFirewall) Cleanup() error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	// Single atomic operation removes everything.
	cmd := exec.Command("nft", "delete", "table", "inet", "sistemo")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// "No such file or directory" means table doesn't exist — not an error.
		if strings.Contains(string(out), "No such file") {
			return nil
		}
		return fmt.Errorf("nft cleanup: %s", string(out))
	}
	return nil
}

// --- Internal helpers ---

// nftApply executes an nft ruleset atomically via stdin.
func (fw *NftFirewall) nftApply(ruleset string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft apply failed: %s (rules: %s)", strings.TrimSpace(string(out)), strings.TrimSpace(ruleset))
	}
	return nil
}

// nftJSON runs "nft -j list chain inet sistemo <chain>" and returns parsed JSON.
func (fw *NftFirewall) nftJSON(chain string) ([]nftRule, error) {
	cmd := exec.Command("nft", "-j", "list", "chain", "inet", "sistemo", chain)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Chain might not exist yet
		if strings.Contains(string(out), "No such file") {
			return nil, nil
		}
		return nil, fmt.Errorf("nft list chain %s: %s", chain, string(out))
	}
	return parseNftJSON(out)
}

// hasRuleWithComment checks if any rule in the chain has the exact comment.
func (fw *NftFirewall) hasRuleWithComment(chain, comment string) bool {
	rules, err := fw.nftJSON(chain)
	if err != nil {
		return false
	}
	for _, r := range rules {
		if r.Comment == comment {
			return true
		}
	}
	return false
}

// deleteRulesWithComment deletes all rules in a chain with the exact comment.
func (fw *NftFirewall) deleteRulesWithComment(chain, comment string) {
	rules, err := fw.nftJSON(chain)
	if err != nil {
		return
	}
	for _, r := range rules {
		if r.Comment == comment {
			fw.deleteRuleByHandle(chain, r.Handle)
		}
	}
}

// deleteRulesMatchingCommentSuffix deletes rules whose comment ends with suffix.
func (fw *NftFirewall) deleteRulesMatchingCommentSuffix(chain, suffix string) {
	rules, err := fw.nftJSON(chain)
	if err != nil {
		return
	}
	for _, r := range rules {
		if strings.HasSuffix(r.Comment, suffix) {
			fw.deleteRuleByHandle(chain, r.Handle)
		}
	}
}

// deleteRulesContainingBridge deletes rules whose comment contains the bridge name
// in an isolation context (sistemo:isolate:*).
func (fw *NftFirewall) deleteRulesContainingBridge(chain, bridge string) {
	rules, err := fw.nftJSON(chain)
	if err != nil {
		return
	}
	prefix := "sistemo:isolate:"
	for _, r := range rules {
		if !strings.HasPrefix(r.Comment, prefix) {
			continue
		}
		// Comment format: "sistemo:isolate:bridgeA:bridgeB"
		parts := strings.SplitN(r.Comment[len(prefix):], ":", 2)
		if len(parts) == 2 && (parts[0] == bridge || parts[1] == bridge) {
			fw.deleteRuleByHandle(chain, r.Handle)
		}
	}
}

// deleteRuleByHandle deletes a single rule by its nftables handle.
func (fw *NftFirewall) deleteRuleByHandle(chain string, handle int) {
	cmd := exec.Command("nft", "delete", "rule", "inet", "sistemo", chain, "handle", fmt.Sprintf("%d", handle))
	if out, err := cmd.CombinedOutput(); err != nil {
		fw.logger.Warn("nft delete rule failed",
			zap.String("chain", chain),
			zap.Int("handle", handle),
			zap.String("output", strings.TrimSpace(string(out))))
	}
}

// --- nftables JSON parsing ---

// nftRule represents a parsed nftables rule with its handle and comment.
type nftRule struct {
	Handle  int
	Comment string
}

// parseNftJSON extracts rule handles and comments from "nft -j list chain" output.
// The JSON structure is: {"nftables": [{"rule": {"handle": N, "comment": "..."}}]}
func parseNftJSON(data []byte) ([]nftRule, error) {
	var result struct {
		Nftables []json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse nft JSON: %w", err)
	}

	var rules []nftRule
	for _, item := range result.Nftables {
		var wrapper struct {
			Rule *struct {
				Handle  int    `json:"handle"`
				Comment string `json:"comment"`
			} `json:"rule"`
		}
		if err := json.Unmarshal(item, &wrapper); err != nil {
			continue
		}
		if wrapper.Rule != nil {
			rules = append(rules, nftRule{
				Handle:  wrapper.Rule.Handle,
				Comment: wrapper.Rule.Comment,
			})
		}
	}
	return rules, nil
}
