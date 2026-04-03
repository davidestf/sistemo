package network

// Firewall abstracts host-level firewall rule management.
// The only implementation is NftFirewall (nftables). All rules are managed
// under "table inet sistemo" with comment-based tagging for identification.
type Firewall interface {
	// EnsureMasquerade adds POSTROUTING masquerade rules for a bridge subnet.
	// Two rules: subnet→internet (source rewrite) and localhost→bridge (127.0.0.0/8 rewrite).
	EnsureMasquerade(subnet, bridge string) error

	// RemoveMasquerade removes the masquerade rules for a bridge.
	RemoveMasquerade(subnet, bridge string) error

	// EnsureBridgeRules adds FORWARD rules for a bridge: outbound, return traffic, and intra-bridge.
	EnsureBridgeRules(bridge string) error

	// RemoveBridgeRules removes all FORWARD rules for a bridge.
	RemoveBridgeRules(bridge string) error

	// AddDNAT adds port forwarding rules: PREROUTING DNAT (external), OUTPUT DNAT (localhost),
	// and FORWARD ACCEPT for the destination. Atomic: all three succeed or none are added.
	AddDNAT(hostPort, machinePort int, machineIP, protocol, bridge string) error

	// RemoveDNAT removes port forwarding rules for a specific host port → machine port mapping.
	RemoveDNAT(hostPort, machinePort int, machineIP, protocol string) error

	// FlushDNATForPort removes ALL DNAT rules targeting a specific host port,
	// regardless of destination IP. Used on startup to clear stale rules.
	FlushDNATForPort(hostPort int, protocol string) error

	// AddForward adds a FORWARD ACCEPT rule for traffic to a machine IP and port.
	AddForward(machineIP string, port int, protocol string) error

	// RemoveForward removes a FORWARD ACCEPT rule for traffic to a machine IP and port.
	RemoveForward(machineIP string, port int, protocol string) error

	// EnsureIsolation adds bidirectional DROP rules between two bridges.
	EnsureIsolation(bridgeA, bridgeB string) error

	// RemoveIsolation removes all isolation rules involving a bridge.
	RemoveIsolation(bridge string) error

	// EnsureSystemForward inserts accept rules for a bridge into the system's filter table
	// (inet filter or ip filter). Required because nftables evaluates ALL forward chains
	// independently — if the system table has policy drop, our sistemo table's accept is ignored.
	EnsureSystemForward(bridge string) error

	// RemoveSystemForward removes sistemo's compat rules for a bridge from system filter tables.
	RemoveSystemForward(bridge string)

	// BlockSMTPInNamespace blocks outbound SMTP (ports 25, 465, 587) inside a network namespace.
	BlockSMTPInNamespace(namespace string) error

	// Cleanup removes all sistemo firewall rules (deletes the entire nftables table).
	Cleanup() error
}
