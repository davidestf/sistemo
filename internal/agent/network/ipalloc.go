package network

import (
	"database/sql"
	"fmt"
	"net"
	"sync"
	"time"
)

// ipAllocMu serializes IP allocation to prevent duplicate allocation under concurrency.
// SQLite's locking can handle this, but a Go-level mutex is simpler and faster.
var ipAllocMu sync.Mutex

// AllocateIP finds the lowest available IP in the bridge subnet and assigns it to machineID.
// Serialized with a mutex to prevent race conditions under concurrent machine creation.
func AllocateIP(db *sql.DB, machineID string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database required for IP allocation")
	}

	ipAllocMu.Lock()
	defer ipAllocMu.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query("SELECT ip FROM ip_allocation ORDER BY ip")
	if err != nil {
		return "", fmt.Errorf("query ip_allocation: %w", err)
	}
	allocated := make(map[string]bool)
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) == nil {
			allocated[ip] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("scan ip_allocation: %w", err)
	}

	// Use the parsed bridge subnet for IP iteration.
	// Fallback to 10.200.0.0/16 if not yet parsed (should not happen in normal flow).
	subnet := bridgeIPNet
	if subnet == nil {
		_, subnet, _ = net.ParseCIDR(DefaultBridgeSubnet)
	}
	baseIP := subnet.IP.To4()
	ones, bits := subnet.Mask.Size()
	hostBits := bits - ones
	maxHosts := (1 << hostBits) - 2
	baseU32 := uint32(baseIP[0])<<24 | uint32(baseIP[1])<<16 | uint32(baseIP[2])<<8 | uint32(baseIP[3])

	for i := 2; i <= maxHosts; i++ {
		u := baseU32 + uint32(i)
		candidate := net.IP{byte(u >> 24), byte(u >> 16), byte(u >> 8), byte(u)}
		ipStr := candidate.String()
		if !allocated[ipStr] {
			now := time.Now().UTC().Format(time.RFC3339)
			_, err := tx.Exec(
				"INSERT INTO ip_allocation (ip, machine_id, allocated_at) VALUES (?, ?, ?)",
				ipStr, machineID, now,
			)
			if err != nil {
				continue // UNIQUE constraint violation, try next
			}
			if err := tx.Commit(); err != nil {
				return "", fmt.Errorf("commit ip_allocation: %w", err)
			}
			return ipStr, nil
		}
	}
	return "", fmt.Errorf("no free IPs in %s", BridgeCIDR)
}

// ReleaseIP frees the IP allocated to machineID. Returns nil if no IP was allocated.
func ReleaseIP(db *sql.DB, machineID string) error {
	if db == nil {
		return nil
	}
	result, err := db.Exec("DELETE FROM ip_allocation WHERE machine_id = ?", machineID)
	if err != nil {
		return fmt.Errorf("release IP for %s: %w", machineID, err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil // no IP was allocated, not an error
	}
	return nil
}

// AllocateIPInSubnet allocates an IP from a specific subnet (for named networks).
// The subnet must be a valid CIDR like "10.201.0.0/24".
func AllocateIPInSubnet(db *sql.DB, machineID, cidr string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database required for IP allocation")
	}

	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("invalid subnet %q: %w", cidr, err)
	}

	ipAllocMu.Lock()
	defer ipAllocMu.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query("SELECT ip FROM ip_allocation ORDER BY ip")
	if err != nil {
		return "", fmt.Errorf("query ip_allocation: %w", err)
	}
	allocated := make(map[string]bool)
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) == nil {
			allocated[ip] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("scan ip_allocation: %w", err)
	}

	// Iterate through the subnet using 32-bit arithmetic to avoid byte overflow.
	// Skip .0 (network) and .1 (gateway), start from .2.
	baseIP := ipNet.IP.To4()
	ones, bits := ipNet.Mask.Size()
	hostBits := bits - ones
	maxHosts := (1 << hostBits) - 2 // exclude network and broadcast

	baseU32 := uint32(baseIP[0])<<24 | uint32(baseIP[1])<<16 | uint32(baseIP[2])<<8 | uint32(baseIP[3])

	for i := 2; i <= maxHosts; i++ {
		u := baseU32 + uint32(i)
		candidate := net.IP{byte(u >> 24), byte(u >> 16), byte(u >> 8), byte(u)}
		ipStr := candidate.String()

		if !ipNet.Contains(candidate) {
			continue
		}
		if allocated[ipStr] {
			continue
		}

		now := time.Now().UTC().Format(time.RFC3339)
		_, err := tx.Exec(
			"INSERT INTO ip_allocation (ip, machine_id, allocated_at) VALUES (?, ?, ?)",
			ipStr, machineID, now,
		)
		if err != nil {
			continue // UNIQUE constraint violation, try next
		}
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("commit ip_allocation: %w", err)
		}
		return ipStr, nil
	}
	return "", fmt.Errorf("no free IPs in %s", cidr)
}

// GetAllocatedIP returns the IP currently allocated to machineID, or empty string if none.
func GetAllocatedIP(db *sql.DB, machineID string) string {
	if db == nil {
		return ""
	}
	var ip string
	err := db.QueryRow("SELECT ip FROM ip_allocation WHERE machine_id = ?", machineID).Scan(&ip)
	if err != nil {
		return ""
	}
	return ip
}
