package network

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestDB creates an in-memory SQLite database with the ip_allocation table.
// Uses the schema from 003_bridge_network.sql plus UNIQUE(vm_id) to enforce one
// IP per VM. Foreign keys are disabled so we don't need the vm table.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(0)&_pragma=busy_timeout(5000)", t.Name())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	// Serialize concurrent access to avoid SQLite locking issues in tests.
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS ip_allocation (
			ip TEXT PRIMARY KEY,
			vm_id TEXT NOT NULL UNIQUE,
			allocated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		);
		CREATE INDEX IF NOT EXISTS idx_ip_allocation_vm ON ip_allocation(vm_id);
	`)
	if err != nil {
		t.Fatalf("create ip_allocation table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestAllocateIP_FirstIP(t *testing.T) {
	db := setupTestDB(t)
	ip, err := AllocateIP(db, "vm-001")
	if err != nil {
		t.Fatalf("AllocateIP: %v", err)
	}
	if ip != "10.200.0.2" {
		t.Errorf("first IP = %q, want %q", ip, "10.200.0.2")
	}
}

func TestAllocateIP_Sequential(t *testing.T) {
	db := setupTestDB(t)
	expected := []string{"10.200.0.2", "10.200.0.3", "10.200.0.4"}
	for i, want := range expected {
		ip, err := AllocateIP(db, fmt.Sprintf("vm-%03d", i+1))
		if err != nil {
			t.Fatalf("AllocateIP vm-%03d: %v", i+1, err)
		}
		if ip != want {
			t.Errorf("allocation %d: got %q, want %q", i+1, ip, want)
		}
	}
}

func TestAllocateIP_ReleaseAndReuse(t *testing.T) {
	db := setupTestDB(t)

	// Allocate two IPs
	ip1, err := AllocateIP(db, "vm-a")
	if err != nil {
		t.Fatalf("allocate vm-a: %v", err)
	}
	_, err = AllocateIP(db, "vm-b")
	if err != nil {
		t.Fatalf("allocate vm-b: %v", err)
	}

	// Release the first one
	if err := ReleaseIP(db, "vm-a"); err != nil {
		t.Fatalf("release vm-a: %v", err)
	}

	// Allocate again — should reuse the released IP (lowest available)
	ip3, err := AllocateIP(db, "vm-c")
	if err != nil {
		t.Fatalf("allocate vm-c: %v", err)
	}
	if ip3 != ip1 {
		t.Errorf("reused IP = %q, want %q (released IP)", ip3, ip1)
	}
}

func TestAllocateIP_ConcurrentNoDuplicates(t *testing.T) {
	db := setupTestDB(t)
	const n = 10

	// Launch n goroutines that each allocate an IP. With SetMaxOpenConns(1),
	// SQLite serializes the transactions, but goroutines still race to acquire
	// the connection. This verifies that the transaction-based allocation logic
	// produces unique IPs regardless of scheduling order.
	var (
		mu   sync.Mutex
		ips  = make(map[string]string) // ip -> vmID
		wg   sync.WaitGroup
		errs []error
	)

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			vmID := fmt.Sprintf("vm-concurrent-%d", idx)
			ip, err := AllocateIP(db, vmID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("goroutine %d: %v", idx, err))
				return
			}
			if prev, exists := ips[ip]; exists {
				errs = append(errs, fmt.Errorf("duplicate IP %s: vm %s and %s", ip, prev, vmID))
			}
			ips[ip] = vmID
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		t.Error(err)
	}
	if len(ips) != n {
		t.Errorf("got %d unique IPs, want %d", len(ips), n)
	}
}

func TestAllocateIP_SameVMTwice(t *testing.T) {
	db := setupTestDB(t)

	_, err := AllocateIP(db, "vm-dup")
	if err != nil {
		t.Fatalf("first allocate: %v", err)
	}

	// Second allocation for the same VM should fail due to UNIQUE constraint on vm_id.
	// AllocateIP iterates candidate IPs; each INSERT fails on vm_id uniqueness,
	// eventually returning "no free IPs".
	_, err = AllocateIP(db, "vm-dup")
	if err == nil {
		t.Error("expected error on duplicate vm_id allocation, got nil")
	}
}

func TestGetAllocatedIP(t *testing.T) {
	db := setupTestDB(t)
	ip, err := AllocateIP(db, "vm-lookup")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}

	got := GetAllocatedIP(db, "vm-lookup")
	if got != ip {
		t.Errorf("GetAllocatedIP = %q, want %q", got, ip)
	}
}

func TestGetAllocatedIP_UnknownVM(t *testing.T) {
	db := setupTestDB(t)
	got := GetAllocatedIP(db, "vm-does-not-exist")
	if got != "" {
		t.Errorf("GetAllocatedIP for unknown VM = %q, want empty string", got)
	}
}

func TestReleaseIP_UnknownVM(t *testing.T) {
	db := setupTestDB(t)
	err := ReleaseIP(db, "vm-never-allocated")
	if err != nil {
		t.Errorf("ReleaseIP for unknown VM = %v, want nil", err)
	}
}

func TestNilDB_AllocateIP(t *testing.T) {
	_, err := AllocateIP(nil, "vm-nil")
	if err == nil {
		t.Error("AllocateIP(nil) should return error")
	}
}

func TestNilDB_ReleaseIP(t *testing.T) {
	err := ReleaseIP(nil, "vm-nil")
	if err != nil {
		t.Errorf("ReleaseIP(nil) = %v, want nil", err)
	}
}

func TestNilDB_GetAllocatedIP(t *testing.T) {
	got := GetAllocatedIP(nil, "vm-nil")
	if got != "" {
		t.Errorf("GetAllocatedIP(nil) = %q, want empty string", got)
	}
}
