package network

import (
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// ipAllocMu serializes IP allocation to prevent duplicate allocation under concurrency.
// SQLite's locking can handle this, but a Go-level mutex is simpler and faster.
var ipAllocMu sync.Mutex

// AllocateIP finds the lowest available IP in the bridge subnet and assigns it to vmID.
// Serialized with a mutex to prevent race conditions under concurrent VM creation.
func AllocateIP(db *sql.DB, vmID string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database required for IP allocation")
	}

	ipAllocMu.Lock()
	defer ipAllocMu.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

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

	// Find first free IP: 10.200.0.2 through 10.200.255.254
	for third := 0; third <= 255; third++ {
		for fourth := 2; fourth <= 254; fourth++ {
			ip := fmt.Sprintf("10.200.%d.%d", third, fourth)
			if !allocated[ip] {
				now := time.Now().UTC().Format(time.RFC3339)
				_, err := tx.Exec(
					"INSERT INTO ip_allocation (ip, vm_id, allocated_at) VALUES (?, ?, ?)",
					ip, vmID, now,
				)
				if err != nil {
					// UNIQUE constraint violation = another transaction won the race, retry
					continue
				}
				if err := tx.Commit(); err != nil {
					return "", fmt.Errorf("commit ip_allocation: %w", err)
				}
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("no free IPs in %s", BridgeCIDR)
}

// ReleaseIP frees the IP allocated to vmID. Returns nil if no IP was allocated.
func ReleaseIP(db *sql.DB, vmID string) error {
	if db == nil {
		return nil
	}
	result, err := db.Exec("DELETE FROM ip_allocation WHERE vm_id = ?", vmID)
	if err != nil {
		return fmt.Errorf("release IP for %s: %w", vmID, err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil // no IP was allocated, not an error
	}
	return nil
}

// GetAllocatedIP returns the IP currently allocated to vmID, or empty string if none.
func GetAllocatedIP(db *sql.DB, vmID string) string {
	if db == nil {
		return ""
	}
	var ip string
	err := db.QueryRow("SELECT ip FROM ip_allocation WHERE vm_id = ?", vmID).Scan(&ip)
	if err != nil {
		return ""
	}
	return ip
}
