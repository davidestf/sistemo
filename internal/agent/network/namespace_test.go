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
	net := NewVMNetwork("test-vm-abc", "10.200.0.5", nil)

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
	if !strings.HasPrefix(net.NsBridge, "nb-") {
		t.Errorf("ns bridge %q doesn't start with nb-", net.NsBridge)
	}
	if net.VMIP != "10.200.0.5" {
		t.Errorf("vm ip = %q, want %q", net.VMIP, "10.200.0.5")
	}
}

func TestGetBootArgs(t *testing.T) {
	args := GetBootArgs("10.200.0.5")
	if !strings.Contains(args, "10.200.0.5") {
		t.Errorf("boot args %q don't contain VM IP", args)
	}
	if !strings.Contains(args, BridgeIP) {
		t.Errorf("boot args %q don't contain bridge gateway %q", args, BridgeIP)
	}
	if !strings.Contains(args, "255.255.0.0") {
		t.Errorf("boot args %q don't contain /16 netmask", args)
	}
}
