package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestLogAction(t *testing.T) {
	tmpDir := t.TempDir()
	database, err := New(tmpDir)
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	defer database.Close()

	// Should not panic with nil db
	LogAction(nil, "test", "vm", "id1", "name1", "details", true)

	// Should insert a row
	LogAction(database, "create", "vm", "vm-123", "myvm", "image=debian vcpus=2", true)
	LogAction(database, "delete", "vm", "vm-123", "myvm", "", true)
	LogAction(database, "create", "vm", "vm-456", "failed", "some error", false)

	var count int
	database.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count != 3 {
		t.Fatalf("expected 3 audit entries, got %d", count)
	}

	// Check filtering
	var successCount int
	database.QueryRow("SELECT COUNT(*) FROM audit_log WHERE success = 1").Scan(&successCount)
	if successCount != 2 {
		t.Fatalf("expected 2 successful entries, got %d", successCount)
	}

	// Cleanup
	os.Remove(filepath.Join(tmpDir, "sistemo.db"))
	os.Remove(filepath.Join(tmpDir, "sistemo.db-wal"))
	os.Remove(filepath.Join(tmpDir, "sistemo.db-shm"))
}

func TestAddColumnIfMissing(t *testing.T) {
	tmpDir := t.TempDir()
	database, err := New(tmpDir)
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	defer database.Close()

	// network_id should already exist (added by New)
	var hasCol bool
	rows, _ := database.Query("PRAGMA table_info(vm)")
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt sql.NullString
		var pk int
		rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk)
		if name == "network_id" {
			hasCol = true
		}
	}
	rows.Close()
	if !hasCol {
		t.Fatal("expected network_id column in vm table")
	}

	// Calling again should not error
	addColumnIfMissing(database, "vm", "network_id", "TEXT")
}
