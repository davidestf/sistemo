package machine

import (
	"strings"
	"testing"
)

func TestGenerateDeterministicMAC(t *testing.T) {
	mac1 := generateDeterministicMAC("test-vm-1")
	mac2 := generateDeterministicMAC("test-vm-1")
	mac3 := generateDeterministicMAC("test-vm-2")

	// Same input -> same output
	if mac1 != mac2 {
		t.Errorf("same input gave different MACs: %q vs %q", mac1, mac2)
	}

	// Different input -> different output
	if mac1 == mac3 {
		t.Errorf("different inputs gave same MAC: %q", mac1)
	}

	// Valid format: 6 hex pairs separated by colons
	parts := strings.Split(mac1, ":")
	if len(parts) != 6 {
		t.Errorf("MAC %q has %d parts, want 6", mac1, len(parts))
	}

	// First byte is AA (locally administered, unicast)
	if !strings.HasPrefix(mac1, "AA:FC:") {
		t.Errorf("MAC %q doesn't start with AA:FC:", mac1)
	}
}
