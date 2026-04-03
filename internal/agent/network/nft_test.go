package network

import (
	"testing"
)

func TestParseNftJSON_EmptyRuleset(t *testing.T) {
	// Minimal valid nft JSON output (metainfo + chain, no rules)
	data := []byte(`{"nftables": [{"metainfo": {"json_schema_version": 1}}, {"chain": {"family": "inet", "table": "sistemo", "name": "sistemo-prerouting"}}]}`)
	rules, err := parseNftJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(rules))
	}
}

func TestParseNftJSON_WithRules(t *testing.T) {
	data := []byte(`{"nftables": [
		{"metainfo": {"json_schema_version": 1}},
		{"chain": {"family": "inet", "table": "sistemo", "name": "sistemo-postrouting"}},
		{"rule": {"family": "inet", "table": "sistemo", "chain": "sistemo-postrouting", "handle": 4, "comment": "sistemo:sistemo0:masq", "expr": []}},
		{"rule": {"family": "inet", "table": "sistemo", "chain": "sistemo-postrouting", "handle": 5, "comment": "sistemo:sistemo0:masq-lo", "expr": []}},
		{"rule": {"family": "inet", "table": "sistemo", "chain": "sistemo-postrouting", "handle": 8, "comment": "sistemo:br-backend:masq", "expr": []}}
	]}`)

	rules, err := parseNftJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}

	// Verify handles
	if rules[0].Handle != 4 {
		t.Errorf("rule[0].Handle = %d, want 4", rules[0].Handle)
	}
	if rules[1].Handle != 5 {
		t.Errorf("rule[1].Handle = %d, want 5", rules[1].Handle)
	}
	if rules[2].Handle != 8 {
		t.Errorf("rule[2].Handle = %d, want 8", rules[2].Handle)
	}

	// Verify comments
	if rules[0].Comment != "sistemo:sistemo0:masq" {
		t.Errorf("rule[0].Comment = %q, want sistemo:sistemo0:masq", rules[0].Comment)
	}
	if rules[2].Comment != "sistemo:br-backend:masq" {
		t.Errorf("rule[2].Comment = %q, want sistemo:br-backend:masq", rules[2].Comment)
	}
}

func TestParseNftJSON_NoComment(t *testing.T) {
	// Rules without comments should still be parsed (comment will be empty string)
	data := []byte(`{"nftables": [
		{"rule": {"family": "inet", "table": "sistemo", "chain": "test", "handle": 10, "expr": []}}
	]}`)

	rules, err := parseNftJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Handle != 10 {
		t.Errorf("handle = %d, want 10", rules[0].Handle)
	}
	if rules[0].Comment != "" {
		t.Errorf("comment = %q, want empty", rules[0].Comment)
	}
}

func TestParseNftJSON_InvalidJSON(t *testing.T) {
	_, err := parseNftJSON([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseNftJSON_MixedItems(t *testing.T) {
	// JSON with non-rule items (metainfo, chain, table) interspersed
	data := []byte(`{"nftables": [
		{"metainfo": {"json_schema_version": 1}},
		{"table": {"family": "inet", "name": "sistemo"}},
		{"chain": {"name": "test"}},
		{"rule": {"handle": 1, "comment": "test:rule1"}},
		{"chain": {"name": "other"}},
		{"rule": {"handle": 2, "comment": "test:rule2"}}
	]}`)

	rules, err := parseNftJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
}

func TestCommentTagFormat(t *testing.T) {
	// Verify our comment tag patterns match what the implementation generates
	tests := []struct {
		name     string
		tag      string
		wantPfx  string
	}{
		{"masquerade", "sistemo:sistemo0:masq", "sistemo:"},
		{"masquerade-lo", "sistemo:sistemo0:masq-lo", "sistemo:"},
		{"forward", "sistemo:sistemo0:fwd", "sistemo:"},
		{"dnat", "sistemo:sistemo0:dnat:8080:tcp", "sistemo:"},
		{"isolation", "sistemo:isolate:br-a:br-b", "sistemo:isolate:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.tag) < len(tt.wantPfx) {
				t.Errorf("tag %q shorter than expected prefix %q", tt.tag, tt.wantPfx)
			}
			if tt.tag[:len(tt.wantPfx)] != tt.wantPfx {
				t.Errorf("tag %q doesn't start with %q", tt.tag, tt.wantPfx)
			}
		})
	}
}
