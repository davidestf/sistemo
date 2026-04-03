package machine

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
		CREATE TABLE machine (id TEXT PRIMARY KEY, name TEXT, status TEXT, ip_address TEXT, namespace TEXT, maintenance_operation TEXT, last_state_change TEXT);
		CREATE TABLE ip_allocation (ip TEXT PRIMARY KEY, machine_id TEXT, allocated_at TEXT);
		CREATE TABLE port_rule (id TEXT PRIMARY KEY, machine_id TEXT, host_port INT, machine_port INT, protocol TEXT, UNIQUE(host_port, protocol));
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
		db:       db,
		logger:   zap.NewNop(),
		machines: make(map[string]*MachineInfo),
	}
}

func TestCleanupDeadRunningMachines_MarksAsStopped(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	// Insert a running machine that is NOT in memory (process dead after reboot)
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('dead-machine', 'test', 'running')")
	db.Exec("INSERT INTO ip_allocation (ip, machine_id, allocated_at) VALUES ('10.200.0.2', 'dead-machine', 'now')")
	db.Exec("INSERT INTO port_rule (id, machine_id, host_port, machine_port, protocol) VALUES ('r1', 'dead-machine', 8080, 80, 'tcp')")

	m.cleanupDeadRunningMachines()

	// Verify status changed to stopped
	var status string
	db.QueryRow("SELECT status FROM machine WHERE id = 'dead-machine'").Scan(&status)
	if status != "stopped" {
		t.Errorf("dead machine status = %q, want %q", status, "stopped")
	}

	// Verify IP is preserved
	var ip string
	db.QueryRow("SELECT ip FROM ip_allocation WHERE machine_id = 'dead-machine'").Scan(&ip)
	if ip != "10.200.0.2" {
		t.Errorf("dead machine IP = %q, want preserved as %q", ip, "10.200.0.2")
	}

	// Verify port rules are preserved
	var portCount int
	db.QueryRow("SELECT COUNT(*) FROM port_rule WHERE machine_id = 'dead-machine'").Scan(&portCount)
	if portCount != 1 {
		t.Errorf("dead machine port rules = %d, want 1 (preserved)", portCount)
	}
}

func TestCleanupDeadRunningMachines_PreservesAlive(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	// Insert a running machine AND register it in memory (process alive)
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('alive-machine', 'test', 'running')")
	m.registerMachine(&MachineInfo{MachineID: "alive-machine", PID: 12345, Status: "running"})

	m.cleanupDeadRunningMachines()

	// Verify status unchanged
	var status string
	db.QueryRow("SELECT status FROM machine WHERE id = 'alive-machine'").Scan(&status)
	if status != "running" {
		t.Errorf("alive machine status = %q, want %q", status, "running")
	}
}

func TestCleanupDeadRunningMachines_SkipsStopped(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	// Insert a stopped machine (not in memory — that's normal for stopped)
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('stopped-machine', 'test', 'stopped')")

	m.cleanupDeadRunningMachines()

	// Verify status unchanged (only queries 'running' machines)
	var status string
	db.QueryRow("SELECT status FROM machine WHERE id = 'stopped-machine'").Scan(&status)
	if status != "stopped" {
		t.Errorf("stopped machine status = %q, want %q", status, "stopped")
	}
}

func TestCleanupDeadRunningMachines_SkipsError(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	db.Exec("INSERT INTO machine (id, name, status) VALUES ('error-machine', 'test', 'error')")

	m.cleanupDeadRunningMachines()

	var status string
	db.QueryRow("SELECT status FROM machine WHERE id = 'error-machine'").Scan(&status)
	if status != "error" {
		t.Errorf("error machine status = %q, want %q (unchanged)", status, "error")
	}
}

func TestCleanupDeadRunningMachines_MultipleMachines(t *testing.T) {
	db := setupManagerTestDB(t)
	m := testManager(db)

	// 3 running machines, 1 alive in memory, 2 dead
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m1', 'alive', 'running')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m2', 'dead1', 'running')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m3', 'dead2', 'running')")
	m.registerMachine(&MachineInfo{MachineID: "m1", PID: 1, Status: "running"})

	m.cleanupDeadRunningMachines()

	var s1, s2, s3 string
	db.QueryRow("SELECT status FROM machine WHERE id = 'm1'").Scan(&s1)
	db.QueryRow("SELECT status FROM machine WHERE id = 'm2'").Scan(&s2)
	db.QueryRow("SELECT status FROM machine WHERE id = 'm3'").Scan(&s3)

	if s1 != "running" {
		t.Errorf("alive machine status = %q, want running", s1)
	}
	if s2 != "stopped" {
		t.Errorf("dead machine 2 status = %q, want stopped", s2)
	}
	if s3 != "stopped" {
		t.Errorf("dead machine 3 status = %q, want stopped", s3)
	}
}

func TestManagerConcurrentRegisterUnregister(t *testing.T) {
	m := testManager(nil)

	var wg sync.WaitGroup
	n := 100
	wg.Add(n * 2)

	// Register n machines concurrently
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			m.registerMachine(&MachineInfo{MachineID: fmt.Sprintf("m-%d", idx), PID: idx, Status: "running"})
		}(i)
	}
	// Unregister half concurrently
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				m.unregisterMachine(fmt.Sprintf("m-%d", idx))
			}
		}(i)
	}
	wg.Wait()

	// Should not panic and should have roughly half the machines
	list := m.ListMachines()
	if len(list) < 20 || len(list) > 80 {
		t.Errorf("concurrent register/unregister: got %d machines, expected 20-80", len(list))
	}
}
