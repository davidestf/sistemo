package main

import (
	"testing"

	"github.com/davidestf/sistemo/internal/db"
)

func TestLookupVM_ByName(t *testing.T) {
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Insert a VM
	database.Exec(
		`INSERT INTO machine (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES ('abc-123', 'web-server', 'running', 'debian', 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)

	// Lookup by name
	vmID, err := lookupVM(database, "web-server")
	if err != nil {
		t.Fatalf("lookupVM by name: %v", err)
	}
	if vmID != "abc-123" {
		t.Errorf("lookupVM by name = %q, want %q", vmID, "abc-123")
	}
}

func TestLookupVM_ByID(t *testing.T) {
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	database.Exec(
		`INSERT INTO machine (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES ('abc-123', 'web-server', 'running', 'debian', 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)

	// Lookup by ID
	vmID, err := lookupVM(database, "abc-123")
	if err != nil {
		t.Fatalf("lookupVM by ID: %v", err)
	}
	if vmID != "abc-123" {
		t.Errorf("lookupVM by ID = %q, want %q", vmID, "abc-123")
	}
}

func TestLookupVM_ExcludesDeleted(t *testing.T) {
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	database.Exec(
		`INSERT INTO machine (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES ('abc-123', 'dead-vm', 'deleted', 'debian', 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)

	// Deleted VM should not be found (default excludes "deleted")
	_, err = lookupVM(database, "dead-vm")
	if err == nil {
		t.Error("lookupVM should return error for deleted VM")
	}
}

func TestLookupVM_NotFound(t *testing.T) {
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	_, err = lookupVM(database, "nonexistent")
	if err == nil {
		t.Error("lookupVM should return error for nonexistent VM")
	}
}

func TestLookupVM_ExcludesCustomStatuses(t *testing.T) {
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	database.Exec(
		`INSERT INTO machine (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES ('vm-001', 'error-vm', 'error', 'debian', 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)

	// Default exclude is "deleted", so error VM should be found
	vmID, err := lookupVM(database, "error-vm")
	if err != nil {
		t.Fatalf("lookupVM for error VM: %v", err)
	}
	if vmID != "vm-001" {
		t.Errorf("vmID = %q, want vm-001", vmID)
	}

	// With custom exclude "error", it should not be found
	_, err = lookupVM(database, "error-vm", "error")
	if err == nil {
		t.Error("lookupVM with exclude 'error' should not find error VM")
	}
}

func TestLookupVM_MultipleExcludeStatuses(t *testing.T) {
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	database.Exec(
		`INSERT INTO machine (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES ('vm-001', 'stopped-vm', 'stopped', 'debian', 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)

	// Exclude both "deleted" and "stopped"
	_, err = lookupVM(database, "stopped-vm", "deleted", "stopped")
	if err == nil {
		t.Error("lookupVM with exclude 'deleted','stopped' should not find stopped VM")
	}
}

func TestLookupVM_PrefersIDOverName(t *testing.T) {
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Create a VM whose name matches another VM's ID
	database.Exec(
		`INSERT INTO machine (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES ('vm-001', 'web', 'running', 'debian', 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)

	// Lookup by the actual ID
	vmID, err := lookupVM(database, "vm-001")
	if err != nil {
		t.Fatalf("lookupVM: %v", err)
	}
	if vmID != "vm-001" {
		t.Errorf("vmID = %q, want vm-001", vmID)
	}
}

func TestLookupVM_RunningVM(t *testing.T) {
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	database.Exec(
		`INSERT INTO machine (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES ('vm-run', 'running-vm', 'running', 'debian', 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)

	vmID, err := lookupVM(database, "running-vm")
	if err != nil {
		t.Fatalf("lookupVM running: %v", err)
	}
	if vmID != "vm-run" {
		t.Errorf("vmID = %q, want vm-run", vmID)
	}
}

func TestLookupVM_StoppedVM(t *testing.T) {
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	database.Exec(
		`INSERT INTO machine (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES ('vm-stop', 'stopped-vm', 'stopped', 'debian', 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)

	// Stopped VMs should be found (only "deleted" excluded by default)
	vmID, err := lookupVM(database, "stopped-vm")
	if err != nil {
		t.Fatalf("lookupVM stopped: %v", err)
	}
	if vmID != "vm-stop" {
		t.Errorf("vmID = %q, want vm-stop", vmID)
	}
}
