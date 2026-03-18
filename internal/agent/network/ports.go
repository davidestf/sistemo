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
		ln.Close()
		return true
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// PortRule describes a port forwarding rule.
type PortRule struct {
	VMID     string `json:"vm_id"`
	HostPort int    `json:"host_port"`
	VMPort   int    `json:"vm_port"`
	Protocol string `json:"protocol"`
}
