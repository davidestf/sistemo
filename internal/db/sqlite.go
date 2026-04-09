// Package db provides SQLite-backed persistence for the Sistemo self-hosted daemon.
// All state lives in ~/.sistemo/sistemo.db (single file, no external database).
package db

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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

	// SQLite supports only one writer at a time. WAL mode allows concurrent readers,
	// so we allow a small pool for read parallelism while serializing writes.
	db.SetMaxOpenConns(4)

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
	// After migration 012, the table is named "machine" (was "vm").
	addColumnIfMissing(db, "machine", "network_id", "TEXT")
	addColumnIfMissing(db, "machine", "root_volume", "TEXT")
	addColumnIfMissing(db, "machine", "image_digest", "TEXT")
	addColumnIfMissing(db, "image_build", "image_digest", "TEXT")

	// Restore network_id data after migration + column re-add.
	restoreNetworkIDs(db, networkMap)

	// Migrate volume index.json to SQLite if present.
	migrateVolumeIndex(db, dataDir)

	// Migrate existing images to content-addressable store (sha256 digests).
	migrateExistingImages(db, dataDir)

	return db, nil
}

// saveNetworkIDs reads machine.network_id before a table rebuild migration.
// Returns empty map if the column or table doesn't exist.
// Tries "machine" first (post-012), falls back to "vm" (pre-012).
func saveNetworkIDs(db *sql.DB) map[string]string {
	m := map[string]string{}
	// Try the new table name first, fall back to old.
	rows, err := db.Query("SELECT id, network_id FROM machine WHERE network_id IS NOT NULL AND network_id != ''")
	if err != nil {
		rows, err = db.Query("SELECT id, network_id FROM vm WHERE network_id IS NOT NULL AND network_id != ''")
		if err != nil {
			return m
		}
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
		if _, err := db.Exec("UPDATE machine SET network_id = ? WHERE id = ?", netID, id); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to restore network_id for machine %s: %v\n", id, err)
		}
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
	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colType)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to add column %s.%s: %v\n", table, column, err)
	}
}

// migrateVolumeIndex migrates volumes/index.json into the SQLite volume table.
// This is a one-time migration; index.json is renamed to index.json.migrated on success.
func migrateVolumeIndex(db *sql.DB, dataDir string) {
	indexPath := filepath.Join(dataDir, "volumes", "index.json")
	migratedPath := indexPath + ".migrated"

	// No index.json — nothing to migrate.
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		return
	}

	// Already migrated.
	if _, err := os.Stat(migratedPath); err == nil {
		return
	}

	data, err := os.ReadFile(indexPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to read volume index.json: %v\n", err)
		return
	}

	var entries []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Path   string `json:"path"`
		SizeMB int    `json:"size_mb"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to parse volume index.json: %v\n", err)
		return
	}

	count := 0
	for _, e := range entries {
		if _, err := db.Exec(
			"INSERT OR IGNORE INTO volume (id, name, size_mb, path) VALUES (?, ?, ?, ?)",
			e.ID, e.Name, e.SizeMB, e.Path,
		); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to migrate volume %s: %v\n", e.ID, err)
			continue
		}
		count++
	}

	if err := os.Rename(indexPath, migratedPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to rename index.json to index.json.migrated: %v\n", err)
		return
	}

	fmt.Fprintf(os.Stderr, "migrated %d volumes from index.json to SQLite\n", count)
}

// migrateExistingImages scans ~/.sistemo/images/ and populates the image + image_tag tables
// with sha256 digests. One-time migration on first startup after upgrade to v0.7.
func migrateExistingImages(db *sql.DB, dataDir string) {
	imagesDir := filepath.Join(dataDir, "images")
	sentinel := filepath.Join(imagesDir, ".digest_migrated")

	// Already migrated.
	if _, err := os.Stat(sentinel); err == nil {
		return
	}

	// No images directory.
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return
	}

	// Build set of docker-built image names for source detection.
	builtNames := map[string]bool{}
	rows, err := db.Query("SELECT DISTINCT build_name FROM image_build WHERE status = 'complete'")
	if err == nil {
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				builtNames[name] = true
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: error reading image builds: %v\n", err)
		}
	}

	count := 0
	for _, e := range entries {
		if e.IsDir() || (!hasExt4Suffix(e.Name())) {
			continue
		}

		path := filepath.Join(imagesDir, e.Name())

		// Skip if already in image table (by path).
		var existing string
		if db.QueryRow("SELECT digest FROM image WHERE path = ?", path).Scan(&existing) == nil {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}

		// Compute sha256 (streaming).
		fmt.Fprintf(os.Stderr, "hashing %s (%d MB)...\n", e.Name(), info.Size()/(1024*1024))
		digest, err := HashFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to hash %s: %v\n", e.Name(), err)
			continue
		}

		// Derive name from filename.
		name := e.Name()
		if idx := len(name) - len(".rootfs.ext4"); idx > 0 && name[idx:] == ".rootfs.ext4" {
			name = name[:idx]
		} else if idx := len(name) - len(".ext4"); idx > 0 && name[idx:] == ".ext4" {
			name = name[:idx]
		}

		// Determine source.
		source := "registry"
		if builtNames[name] {
			source = "docker_build"
		}

		now := "2006-01-02T15:04:05Z"
		if !info.ModTime().IsZero() {
			now = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
		}

		_, err = db.Exec(
			"INSERT OR IGNORE INTO image (digest, name, file, path, size_bytes, source, verified_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			digest, name, e.Name(), path, info.Size(), source, now, now,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to insert image %s: %v\n", name, err)
			continue
		}

		// Create tag (name -> digest).
		if _, err := db.Exec("INSERT OR IGNORE INTO image_tag (tag, digest) VALUES (?, ?)", name, digest); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to create tag for %s: %v\n", name, err)
		}

		count++
	}

	// Backfill machine.image_digest for existing machines.
	vmRows, err := db.Query("SELECT id, image FROM machine WHERE image_digest IS NULL AND status != 'deleted'")
	if err == nil {
		type vmRef struct{ id, image string }
		var refs []vmRef
		for vmRows.Next() {
			var r vmRef
			if vmRows.Scan(&r.id, &r.image) == nil {
				refs = append(refs, r)
			}
		}
		_ = vmRows.Close()
		if err := vmRows.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: error reading machines for image backfill: %v\n", err)
		}

		for _, r := range refs {
			var digest string
			// Try matching by path first, then by name.
			if db.QueryRow("SELECT digest FROM image WHERE path = ?", r.image).Scan(&digest) != nil {
				// Try extracting basename and matching.
				base := filepath.Base(r.image)
				name := base
				if idx := len(name) - len(".rootfs.ext4"); idx > 0 && name[idx:] == ".rootfs.ext4" {
					name = name[:idx]
				} else if idx := len(name) - len(".ext4"); idx > 0 && name[idx:] == ".ext4" {
					name = name[:idx]
				}
				db.QueryRow("SELECT digest FROM image WHERE name = ? LIMIT 1", name).Scan(&digest)
			}
			if digest != "" {
				if _, err := db.Exec("UPDATE machine SET image_digest = ? WHERE id = ?", digest, r.id); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to backfill image_digest for machine %s: %v\n", r.id, err)
				}
			}
		}
	}

	// Backfill image_build.image_digest for completed builds.
	buildRows, err := db.Query("SELECT id, build_name FROM image_build WHERE image_digest IS NULL AND status = 'complete'")
	if err == nil {
		type buildRef struct{ id, buildName string }
		var brefs []buildRef
		for buildRows.Next() {
			var b buildRef
			if buildRows.Scan(&b.id, &b.buildName) == nil {
				brefs = append(brefs, b)
			}
		}
		_ = buildRows.Close()
		if err := buildRows.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: error reading builds for digest backfill: %v\n", err)
		}

		for _, b := range brefs {
			var digest string
			db.QueryRow("SELECT digest FROM image WHERE name = ? LIMIT 1", b.buildName).Scan(&digest)
			if digest != "" {
				if _, err := db.Exec("UPDATE image_build SET image_digest = ? WHERE id = ?", digest, b.id); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to backfill image_digest for build %s: %v\n", b.id, err)
				}
			}
		}
	}

	// Write sentinel file.
	_ = os.WriteFile(sentinel, []byte(fmt.Sprintf("migrated %d images\n", count)), 0644)
	if count > 0 {
		fmt.Fprintf(os.Stderr, "migrated %d images to content-addressable store (sha256)\n", count)
	}
}

func hasExt4Suffix(name string) bool {
	return len(name) > 5 && (name[len(name)-5:] == ".ext4")
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
		// Table-rebuild migrations (DROP + CREATE) require FK checks off.
		// PRAGMA foreign_keys cannot be changed inside a transaction, so we
		// disable before and re-enable + verify after.
		needsFKOff := strings.Contains(string(body), "DROP TABLE")
		if needsFKOff {
			if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to disable FK checks for migration %s: %v\n", name, err)
			}
		}

		// Run migration + tracking insert in a transaction to prevent half-applied state
		tx, err := db.Begin()
		if err != nil {
			if needsFKOff {
				_, _ = db.Exec("PRAGMA foreign_keys = ON")
			}
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			if needsFKOff {
				_, _ = db.Exec("PRAGMA foreign_keys = ON")
			}
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migration (name) VALUES (?)", name); err != nil {
			_ = tx.Rollback()
			if needsFKOff {
				_, _ = db.Exec("PRAGMA foreign_keys = ON")
			}
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			if needsFKOff {
				_, _ = db.Exec("PRAGMA foreign_keys = ON")
			}
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		if needsFKOff {
			_, _ = db.Exec("PRAGMA foreign_keys = ON")
			// Verify FK integrity after table rebuild — check all violations
			fkRows, fkErr := db.Query("PRAGMA foreign_key_check")
			if fkErr == nil {
				for fkRows.Next() {
					var table, rowid, parent, fkid string
					if fkRows.Scan(&table, &rowid, &parent, &fkid) == nil {
						fmt.Fprintf(os.Stderr, "warning: FK violation after migration %s: table=%s row=%s parent=%s\n", name, table, rowid, parent)
					}
				}
				_ = fkRows.Close()
			}
		}
	}
	return nil
}
