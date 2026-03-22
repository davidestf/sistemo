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
		CREATE TABLE vm (id TEXT PRIMARY KEY, name TEXT, status TEXT, ip_address TEXT);
		CREATE TABLE ip_allocation (ip TEXT PRIMARY KEY, vm_id TEXT, allocated_at TEXT);
		CREATE TABLE port_rule (id TEXT PRIMARY KEY, vm_id TEXT, host_port INT, vm_port INT, protocol TEXT, UNIQUE(host_port, protocol));
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestStaleIPAllocationCleanup(t *testing.T) {
	db := setupCleanupDB(t)

	// Insert VMs in various states
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-running', 'r', 'running')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-stopped', 's', 'stopped')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-error', 'e', 'error')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-destroyed', 'd', 'destroyed')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-maintenance', 'm', 'maintenance')")

	// Insert IPs for all
	db.Exec("INSERT INTO ip_allocation (ip, vm_id, allocated_at) VALUES ('10.0.0.1', 'vm-running', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, vm_id, allocated_at) VALUES ('10.0.0.2', 'vm-stopped', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, vm_id, allocated_at) VALUES ('10.0.0.3', 'vm-error', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, vm_id, allocated_at) VALUES ('10.0.0.4', 'vm-destroyed', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, vm_id, allocated_at) VALUES ('10.0.0.5', 'vm-maintenance', 'now')")
	db.Exec("INSERT INTO ip_allocation (ip, vm_id, allocated_at) VALUES ('10.0.0.6', 'vm-orphan', 'now')") // no vm row

	// Run the cleanup SQL (same as runners.go Phase 3)
	db.Exec(`DELETE FROM ip_allocation WHERE vm_id NOT IN (SELECT id FROM vm WHERE status IN ('running', 'stopped', 'error'))`)

	// Check which IPs remain
	remaining := make(map[string]bool)
	rows, _ := db.Query("SELECT vm_id FROM ip_allocation")
	defer rows.Close()
	for rows.Next() {
		var vmID string
		rows.Scan(&vmID)
		remaining[vmID] = true
	}

	// running, stopped, error should be preserved
	if !remaining["vm-running"] {
		t.Error("running VM IP should be preserved")
	}
	if !remaining["vm-stopped"] {
		t.Error("stopped VM IP should be preserved")
	}
	if !remaining["vm-error"] {
		t.Error("error VM IP should be preserved")
	}

	// destroyed, maintenance, orphan should be deleted
	if remaining["vm-destroyed"] {
		t.Error("destroyed VM IP should be deleted")
	}
	if remaining["vm-maintenance"] {
		t.Error("maintenance VM IP should be deleted")
	}
	if remaining["vm-orphan"] {
		t.Error("orphan VM IP should be deleted")
	}
}

func TestStalePortRuleCleanup(t *testing.T) {
	db := setupCleanupDB(t)

	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-alive', 'alive', 'running')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-dead', 'dead', 'destroyed')")

	db.Exec("INSERT INTO port_rule (id, vm_id, host_port, vm_port, protocol) VALUES ('r1', 'vm-alive', 8080, 80, 'tcp')")
	db.Exec("INSERT INTO port_rule (id, vm_id, host_port, vm_port, protocol) VALUES ('r2', 'vm-dead', 9090, 80, 'tcp')")
	db.Exec("INSERT INTO port_rule (id, vm_id, host_port, vm_port, protocol) VALUES ('r3', 'vm-orphan', 7070, 80, 'tcp')") // no vm row

	// Run the cleanup SQL (same as runners.go Phase 2 — simplified)
	rows, _ := db.Query(`SELECT pr.vm_id FROM port_rule pr LEFT JOIN vm v ON pr.vm_id = v.id WHERE v.id IS NULL OR v.status = 'destroyed'`)
	var staleIDs []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		staleIDs = append(staleIDs, id)
	}
	rows.Close()
	for _, id := range staleIDs {
		db.Exec("DELETE FROM port_rule WHERE vm_id = ?", id)
	}

	// Check remaining
	var count int
	db.QueryRow("SELECT COUNT(*) FROM port_rule").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 port_rule remaining, got %d", count)
	}

	var vmID string
	db.QueryRow("SELECT vm_id FROM port_rule").Scan(&vmID)
	if vmID != "vm-alive" {
		t.Errorf("remaining port_rule should be for vm-alive, got %q", vmID)
	}
}

func TestMaintenanceVMCleanup(t *testing.T) {
	db := setupCleanupDB(t)

	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-maint', 'm', 'maintenance')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-failed', 'f', 'failed')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-running', 'r', 'running')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-stopped', 's', 'stopped')")
	db.Exec("INSERT INTO vm (id, name, status) VALUES ('vm-error', 'e', 'error')")

	// Run Phase 1 cleanup SQL
	rows, _ := db.Query(`SELECT id FROM vm WHERE status IN ('maintenance', 'failed')`)
	var staleIDs []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		staleIDs = append(staleIDs, id)
	}
	rows.Close()
	for _, id := range staleIDs {
		db.Exec("DELETE FROM vm WHERE id = ?", id)
	}

	// Check remaining
	var count int
	db.QueryRow("SELECT COUNT(*) FROM vm").Scan(&count)
	if count != 3 {
		t.Errorf("expected 3 VMs remaining (running, stopped, error), got %d", count)
	}

	// Verify maintenance and failed are gone
	var maint int
	db.QueryRow("SELECT COUNT(*) FROM vm WHERE status IN ('maintenance', 'failed')").Scan(&maint)
	if maint != 0 {
		t.Errorf("maintenance/failed VMs should be deleted, got %d", maint)
	}
}
