package network

import (
	"fmt"
	"net"
)

// IsPortAvailable checks if a host port is free by attempting to listen on it.
func IsPortAvailable(port int, protocol string) bool {
	if protocol == "udp" {
		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", port))
		if err != nil {
			return false
		}
		ln, err := net.ListenUDP("udp", addr)
		if err != nil {
			return false
		}
		_ = ln.Close()
		return true
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// PortRule describes a port forwarding rule.
type PortRule struct {
	MachineID   string `json:"machine_id"`
	HostPort    int    `json:"host_port"`
	MachinePort int    `json:"machine_port"`
	Protocol    string `json:"protocol"`
}
