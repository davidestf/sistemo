package vm

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"go.uber.org/zap"

	_ "modernc.org/sqlite"
)

func setupManagerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(0)&_pragma=busy_timeout(5000)", t.Name())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)

	schema := `
		CREATE TABLE vm (id TEXT PRIMARY KEY, name TEXT, status TEXT, ip_address TEXT, namespace TEXT, maintenance_operation TEXT, last_state_change TEXT);
		CREATE TABLE ip_allocation (ip TEXT PRIMARY KEY, vm_id TEXT, allocated_at TEXT);
		CREATE TABLE port_rule (id TEXT PRIMARY KEY, vm_id TEXT, host_port INT, vm_port INT, protocol TEXT, UNIQUE(host_port, protocol));
		CREATE TABLE network (id TEXT PRIMARY KEY, name TEXT UNIQUE, subnet TEXT, bridge_name TEXT, created_at TEXT);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testManager(db *sql.DB) *Manager {
	return &Manager{
		db:     db,
		logger: zap.NewNop(),
		vms:    make(map[string]*VMInfo),
	}
}

func TestCleanupDeadRunningVMs_MarksAsStopped(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	// Insert a running VM that is NOT in memory (process dead after reboot)
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('dead-vm', 'test', 'running')")
	db.Exec("INSERT INTO ip_allocation (ip, vm_id, allocated_at) VALUES ('10.200.0.2', 'dead-vm', 'now')")
	db.Exec("INSERT INTO port_rule (id, vm_id, host_port, vm_port, protocol) VALUES ('r1', 'dead-vm', 8080, 80, 'tcp')")

	m.cleanupDeadRunningVMs()

	// Verify status changed to stopped
	var status string
	db.QueryRow("SELECT status FROM vm WHERE id = 'dead-vm'").Scan(&status)
	if status != "stopped" {
		t.Errorf("dead VM status = %q, want %q", status, "stopped")
	}

	// Verify IP is preserved
	var ip string
	db.QueryRow("SELECT ip FROM ip_allocation WHERE vm_id = 'dead-vm'").Scan(&ip)
	if ip != "10.200.0.2" {
		t.Errorf("dead VM IP = %q, want preserved as %q", ip, "10.200.0.2")
	}

	// Verify port rules are preserved
	var portCount int
	db.QueryRow("SELECT COUNT(*) FROM port_rule WHERE vm_id = 'dead-vm'").Scan(&portCount)
	if portCount != 1 {
		t.Errorf("dead VM port rules = %d, want 1 (preserved)", portCount)
	}
}

func TestCleanupDeadRunningVMs_PreservesAlive(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	// Insert a running VM AND register it in memory (process alive)
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('alive-vm', 'test', 'running')")
	m.registerVM(&VMInfo{VMID: "alive-vm", PID: 12345, Status: "running"})

	m.cleanupDeadRunningVMs()

	// Verify status unchanged
	var status string
	db.QueryRow("SELECT status FROM vm WHERE id = 'alive-vm'").Scan(&status)
	if status != "running" {
		t.Errorf("alive VM status = %q, want %q", status, "running")
	}
}

func TestCleanupDeadRunningVMs_SkipsStopped(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	// Insert a stopped VM (not in memory — that's normal for stopped)
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('stopped-vm', 'test', 'stopped')")

	m.cleanupDeadRunningVMs()

	// Verify status unchanged (only queries 'running' VMs)
	var status string
	db.QueryRow("SELECT status FROM vm WHERE id = 'stopped-vm'").Scan(&status)
	if status != "stopped" {
		t.Errorf("stopped VM status = %q, want %q", status, "stopped")
	}
}

func TestCleanupDeadRunningVMs_SkipsError(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	db.Exec("INSERT INTO vm (id, name, status) VALUES ('error-vm', 'test', 'error')")

	m.cleanupDeadRunningVMs()

	var status string
	db.QueryRow("SELECT status FROM vm WHERE id = 'error-vm'").Scan(&status)
	if status != "error" {
		t.Errorf("error VM status = %q, want %q (unchanged)", status, "error")
	}
}

func TestCleanupDeadRunningVMs_MultipleVMs(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	// 3 running VMs, 1 alive in memory, 2 dead
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm1', 'alive', 'running')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm2', 'dead1', 'running')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm3', 'dead2', 'running')")
	m.registerVM(&VMInfo{VMID: "vm1", PID: 1, Status: "running"})

	m.cleanupDeadRunningVMs()

	var s1, s2, s3 string
	db.QueryRow("SELECT status FROM vm WHERE id = 'vm1'").Scan(&s1)
	db.QueryRow("SELECT status FROM vm WHERE id = 'vm2'").Scan(&s2)
	db.QueryRow("SELECT status FROM vm WHERE id = 'vm3'").Scan(&s3)

	if s1 != "running" {
		t.Errorf("alive VM status = %q, want running", s1)
	}
	if s2 != "stopped" {
		t.Errorf("dead VM 2 status = %q, want stopped", s2)
	}
	if s3 != "stopped" {
		t.Errorf("dead VM 3 status = %q, want stopped", s3)
	}
}

func TestManagerConcurrentRegisterUnregister(t *testing.T) {
	m := testManager(nil)

	var wg sync.WaitGroup
	n := 100
	wg.Add(n * 2)

	// Register n VMs concurrently
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			m.registerVM(&VMInfo{VMID: fmt.Sprintf("vm-%d", idx), PID: idx, Status: "running"})
		}(i)
	}
	// Unregister half concurrently
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				m.unregisterVM(fmt.Sprintf("vm-%d", idx))
			}
		}(i)
	}
	wg.Wait()

	// Should not panic and should have roughly half the VMs
	list := m.ListVMs()
	if len(list) < 20 || len(list) > 80 {
		t.Errorf("concurrent register/unregister: got %d VMs, expected 20-80", len(list))
	}
}
