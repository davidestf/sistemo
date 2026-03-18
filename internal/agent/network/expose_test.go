package network

import (
	"strings"
	"testing"
)

func TestExposePort_InvalidPorts(t *testing.T) {
	n := &VMNetwork{VMIP: "10.200.0.5"}
	tests := []struct {
		name     string
		hostPort int
		vmPort   int
		protocol string
	}{
		{"host port zero", 0, 80, "tcp"},
		{"host port negative", -1, 80, "tcp"},
		{"host port too high", 65536, 80, "tcp"},
		{"vm port zero", 8080, 0, "tcp"},
		{"vm port negative", 8080, -1, "tcp"},
		{"vm port too high", 8080, 65536, "tcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := n.ExposePort("eth0", tt.hostPort, tt.vmPort, tt.protocol)
			if err == nil {
				t.Errorf("ExposePort(%d, %d, %q) should return error", tt.hostPort, tt.vmPort, tt.protocol)
			}
			if err != nil && !strings.Contains(err.Error(), "1-65535") {
				t.Errorf("error %q should mention valid port range", err.Error())
			}
		})
	}
}

func TestExposePort_InvalidProtocol(t *testing.T) {
	n := &VMNetwork{VMIP: "10.200.0.5"}
	for _, proto := range []string{"icmp", "sctp", "TCP", "UDP", ""} {
		err := n.ExposePort("eth0", 8080, 80, proto)
		if err == nil {
			t.Errorf("ExposePort with protocol %q should return error", proto)
		}
		if err != nil && !strings.Contains(err.Error(), "tcp or udp") {
			t.Errorf("error %q should mention valid protocols", err.Error())
		}
	}
}

func TestGetBootArgs_ContainsIPAndGateway(t *testing.T) {
	args := GetBootArgs("10.200.0.42")

	if !strings.Contains(args, "10.200.0.42") {
		t.Errorf("boot args %q missing VM IP", args)
	}
	if !strings.Contains(args, BridgeIP) {
		t.Errorf("boot args %q missing gateway %s", args, BridgeIP)
	}
	// Verify the format: ip=<vmIP>::<gateway>:<netmask>::eth0:off
	expected := "ip=10.200.0.42::10.200.0.1:255.255.0.0::eth0:off"
	if args != expected {
		t.Errorf("GetBootArgs = %q, want %q", args, expected)
	}
}
