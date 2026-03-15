package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewCreatesDB(t *testing.T) {
	dir := t.TempDir()
	database, err := New(dir)
	if err != nil {
		t.Fatalf("New(%q) error: %v", dir, err)
	}
	defer database.Close()

	dbPath := filepath.Join(dir, "sistemo.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not created: %v", err)
	}
}

func TestMigrationCreatesVMTable(t *testing.T) {
	dir := t.TempDir()
	database, err := New(dir)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	defer database.Close()

	// Insert a row
	_, err = database.Exec(
		`INSERT INTO vm (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES ('test-id', 'test-vm', 'running', 'debian', 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}

	// Read it back
	var name, status string
	err = database.QueryRow("SELECT name, status FROM vm WHERE id = 'test-id'").Scan(&name, &status)
	if err != nil {
		t.Fatalf("SELECT failed: %v", err)
	}
	if name != "test-vm" || status != "running" {
		t.Errorf("got name=%q status=%q, want test-vm running", name, status)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	db1, err := New(dir)
	if err != nil {
		t.Fatalf("first New error: %v", err)
	}
	db1.Close()

	// Opening again should not fail (migrations already applied)
	db2, err := New(dir)
	if err != nil {
		t.Fatalf("second New error: %v", err)
	}
	db2.Close()
}
