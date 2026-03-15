package network

import (
	"strings"
	"testing"
)

func TestGetShortID(t *testing.T) {
	id1 := getShortID("test-vm-1")
	id2 := getShortID("test-vm-1")
	id3 := getShortID("test-vm-2")

	if id1 != id2 {
		t.Errorf("same input gave different IDs: %q vs %q", id1, id2)
	}
	if id1 == id3 {
		t.Errorf("different inputs gave same ID: %q", id1)
	}
	if len(id1) != 8 {
		t.Errorf("short ID %q length = %d, want 8", id1, len(id1))
	}
}

func TestNewVMNetwork(t *testing.T) {
	net := NewVMNetwork("test-vm-abc", nil)

	if !strings.HasPrefix(net.NamespaceName, "ns-") {
		t.Errorf("namespace %q doesn't start with ns-", net.NamespaceName)
	}
	if !strings.HasPrefix(net.VethOut, "vo-") {
		t.Errorf("veth out %q doesn't start with vo-", net.VethOut)
	}
	if !strings.HasPrefix(net.VethIn, "vi-") {
		t.Errorf("veth in %q doesn't start with vi-", net.VethIn)
	}
	if !strings.HasPrefix(net.TapName, "tap-") {
		t.Errorf("tap %q doesn't start with tap-", net.TapName)
	}
	if net.GatewayIP != GatewayIP {
		t.Errorf("gateway = %q, want %q", net.GatewayIP, GatewayIP)
	}
	if net.VMIP != VMIP {
		t.Errorf("vm ip = %q, want %q", net.VMIP, VMIP)
	}
	if !strings.HasPrefix(net.VethHostIP, "10.200.") {
		t.Errorf("veth host IP %q not in 10.200.0.0/16", net.VethHostIP)
	}
}

func TestGetBootArgs(t *testing.T) {
	args := GetBootArgs()
	if !strings.Contains(args, VMIP) {
		t.Errorf("boot args %q don't contain VM IP %q", args, VMIP)
	}
	if !strings.Contains(args, GatewayIP) {
		t.Errorf("boot args %q don't contain gateway %q", args, GatewayIP)
	}
}

func TestGetUniqueSubnet(t *testing.T) {
	third1, base1 := getUniqueSubnet("vm-1")
	third2, base2 := getUniqueSubnet("vm-1")

	if third1 != third2 || base1 != base2 {
		t.Errorf("same input gave different subnets")
	}

	// Check range
	if third1 < 0 || third1 > 255 {
		t.Errorf("third octet %d out of range", third1)
	}
	if base1 < 0 || base1 > 252 {
		t.Errorf("fourth base %d out of range", base1)
	}
}
