package db

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

func setupCleanupDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(0)&_pragma=busy_timeout(5000)", t.Name())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)

	schema := `
		CREATE TABLE machine (id TEXT PRIMARY KEY, name TEXT, status TEXT, ip_address TEXT);
		CREATE TABLE ip_allocation (ip TEXT PRIMARY KEY, machine_id TEXT, allocated_at TEXT);
		CREATE TABLE port_rule (id TEXT PRIMARY KEY, machine_id TEXT, host_port INT, machine_port INT, protocol TEXT, UNIQUE(host_port, protocol));
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestStaleIPAllocationCleanup(t *testing.T) {
	db := setupCleanupDB(t)

	// Insert machines in various states
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-running', 'r', 'running')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-stopped', 's', 'stopped')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-error', 'e', 'error')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-deleted', 'd', 'deleted')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-maintenance', 'm', 'maintenance')")

	// Insert IPs for all
	db.Exec("INSERT INTO ip_allocation (ip, machine_id, allocated_at) VALUES ('10.0.0.1', 'm-running', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, machine_id, allocated_at) VALUES ('10.0.0.2', 'm-stopped', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, machine_id, allocated_at) VALUES ('10.0.0.3', 'm-error', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, machine_id, allocated_at) VALUES ('10.0.0.4', 'm-deleted', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, machine_id, allocated_at) VALUES ('10.0.0.5', 'm-maintenance', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, machine_id, allocated_at) VALUES ('10.0.0.6', 'm-orphan', 'now')") // no machine row

	// Run the cleanup SQL (same as daemon.go startup cleanup)
	db.Exec(`DELETE FROM ip_allocation WHERE machine_id NOT IN (SELECT id FROM machine WHERE status IN ('running', 'stopped', 'error'))`)

	// Check which IPs remain
	remaining := make(map[string]bool)
	rows, _ := db.Query("SELECT machine_id FROM ip_allocation")
	defer rows.Close()
	for rows.Next() {
		var machineID string
		rows.Scan(&machineID)
		remaining[machineID] = true
	}

	// running, stopped, error should be preserved
	if !remaining["m-running"] {
		t.Error("running machine IP should be preserved")
	}
	if !remaining["m-stopped"] {
		t.Error("stopped machine IP should be preserved")
	}
	if !remaining["m-error"] {
		t.Error("error machine IP should be preserved")
	}

	// deleted, maintenance, orphan should be deleted
	if remaining["m-deleted"] {
		t.Error("deleted machine IP should be deleted")
	}
	if remaining["m-maintenance"] {
		t.Error("maintenance machine IP should be deleted")
	}
	if remaining["m-orphan"] {
		t.Error("orphan machine IP should be deleted")
	}
}

func TestStalePortRuleCleanup(t *testing.T) {
	db := setupCleanupDB(t)

	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-alive', 'alive', 'running')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-dead', 'dead', 'deleted')")

	db.Exec("INSERT INTO port_rule (id, machine_id, host_port, machine_port, protocol) VALUES ('r1', 'm-alive', 8080, 80, 'tcp')")
	db.Exec("INSERT INTO port_rule (id, machine_id, host_port, machine_port, protocol) VALUES ('r2', 'm-dead', 9090, 80, 'tcp')")
	db.Exec("INSERT INTO port_rule (id, machine_id, host_port, machine_port, protocol) VALUES ('r3', 'm-orphan', 7070, 80, 'tcp')") // no machine row

	// Run the cleanup SQL (same as daemon.go startup cleanup)
	rows, _ := db.Query(`SELECT pr.machine_id FROM port_rule pr LEFT JOIN machine m ON pr.machine_id = m.id WHERE m.id IS NULL OR m.status = 'deleted'`)
	var staleIDs []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		staleIDs = append(staleIDs, id)
	}
	rows.Close()
	for _, id := range staleIDs {
		db.Exec("DELETE FROM port_rule WHERE machine_id = ?", id)
	}

	// Check remaining
	var count int
	db.QueryRow("SELECT COUNT(*) FROM port_rule").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 port_rule remaining, got %d", count)
	}

	var machineID string
	db.QueryRow("SELECT machine_id FROM port_rule").Scan(&machineID)
	if machineID != "m-alive" {
		t.Errorf("remaining port_rule should be for m-alive, got %q", machineID)
	}
}

func TestMaintenanceMachineCleanup(t *testing.T) {
	db := setupCleanupDB(t)

	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-maint', 'm', 'maintenance')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-failed', 'f', 'failed')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-running', 'r', 'running')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-stopped', 's', 'stopped')")
	db.Exec("INSERT INTO machine (id, name, status) VALUES ('m-error', 'e', 'error')")

	// Run cleanup SQL
	rows, _ := db.Query(`SELECT id FROM machine WHERE status IN ('maintenance', 'failed')`)
	var staleIDs []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		staleIDs = append(staleIDs, id)
	}
	rows.Close()
	for _, id := range staleIDs {
		db.Exec("DELETE FROM machine WHERE id = ?", id)
	}

	// Check remaining
	var count int
	db.QueryRow("SELECT COUNT(*) FROM machine").Scan(&count)
	if count != 3 {
		t.Errorf("expected 3 machines remaining (running, stopped, error), got %d", count)
	}

	// Verify maintenance and failed are gone
	var maint int
	db.QueryRow("SELECT COUNT(*) FROM machine WHERE status IN ('maintenance', 'failed')").Scan(&maint)
	if maint != 0 {
		t.Errorf("maintenance/failed machines should be deleted, got %d", maint)
	}
}
