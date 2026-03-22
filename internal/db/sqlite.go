// Package db provides SQLite-backed persistence for the Sistemo self-hosted daemon.
// All state lives in ~/.sistemo/sistemo.db (single file, no external database).
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	_ "modernc.org/sqlite"
)

var safeIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const dbFileName = "sistemo.db"

// New opens or creates the SQLite database in the Sistemo data directory.
// Directory is created if it does not exist. Migrations are run on first open.
func New(dataDir string) (*sql.DB, error) {
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = os.Getenv("HOME")
		}
		dataDir = filepath.Join(home, ".sistemo")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, dbFileName)
	dsn := dbPath + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	// Save network_id data before migrations (migration 006 rebuilds the vm table
	// without network_id, which is added dynamically). This is a no-op on fresh DBs.
	networkMap := saveNetworkIDs(db)

	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}

	// Schema evolution: add columns that can't use IF NOT EXISTS in SQLite ALTER TABLE.
	addColumnIfMissing(db, "vm", "network_id", "TEXT")

	// Restore network_id data after migration + column re-add.
	restoreNetworkIDs(db, networkMap)

	return db, nil
}

// saveNetworkIDs reads vm.network_id before a table rebuild migration.
// Returns empty map if the column or table doesn't exist.
func saveNetworkIDs(db *sql.DB) map[string]string {
	m := map[string]string{}
	rows, err := db.Query("SELECT id, network_id FROM vm WHERE network_id IS NOT NULL AND network_id != ''")
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var id, netID string
		if rows.Scan(&id, &netID) == nil {
			m[id] = netID
		}
	}
	return m
}

// restoreNetworkIDs writes saved network_id values back after a table rebuild.
func restoreNetworkIDs(db *sql.DB, m map[string]string) {
	for id, netID := range m {
		db.Exec("UPDATE vm SET network_id = ? WHERE id = ?", netID, id)
	}
}

// addColumnIfMissing adds a column to a table if it doesn't already exist.
// SQLite ALTER TABLE ADD COLUMN doesn't support IF NOT EXISTS.
// All arguments must be safe SQL identifiers (letters, digits, underscores).
func addColumnIfMissing(db *sql.DB, table, column, colType string) {
	if !safeIdentifier.MatchString(table) || !safeIdentifier.MatchString(column) || !safeIdentifier.MatchString(colType) {
		return // reject unsafe identifiers
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt sql.NullString
		var pk int
		if rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk) == nil && name == column {
			return // column already exists
		}
	}
	db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colType))
}

func runMigrations(db *sql.DB) error {
	// Create migration tracking table (idempotent)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
	)`); err != nil {
		return fmt.Errorf("create schema_migration table: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".sql" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		// Skip already-applied migrations
		var applied string
		if db.QueryRow("SELECT name FROM schema_migration WHERE name = ?", name).Scan(&applied) == nil {
			continue
		}
		body, err := migrationsFS.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			return err
		}
		// Run migration + tracking insert in a transaction to prevent half-applied state
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migration (name) VALUES (?)", name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
