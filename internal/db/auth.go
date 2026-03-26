package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// ErrAdminExists is returned when attempting to create an admin account that already exists.
var ErrAdminExists = errors.New("admin account already exists")

// AdminUser represents the dashboard admin account.
type AdminUser struct {
	ID        int
	Username  string
	CreatedAt string
	UpdatedAt string
}

// AdminExists returns true if at least one admin user exists.
func AdminExists(db *sql.DB) (bool, error) {
	if db == nil {
		return false, nil
	}
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM admin_user").Scan(&count)
	if err != nil {
		// Table may not exist yet (pre-migration)
		return false, nil
	}
	return count > 0, nil
}

// CreateAdmin creates the initial admin user with a bcrypt-hashed password.
// Returns an error if an admin already exists (single-user model).
func CreateAdmin(db *sql.DB, username, password string) error {
	exists, err := AdminExists(db)
	if err != nil {
		return fmt.Errorf("check admin exists: %w", err)
	}
	if exists {
		return ErrAdminExists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(
		"INSERT INTO admin_user (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)",
		username, string(hash), now, now,
	)
	if err != nil {
		return fmt.Errorf("insert admin: %w", err)
	}
	return nil
}

// ValidateAdmin checks username and password against the stored admin account.
// Returns the AdminUser on success, nil on wrong credentials (not an error).
func ValidateAdmin(db *sql.DB, username, password string) (*AdminUser, error) {
	var user AdminUser
	var hash string
	err := db.QueryRow(
		"SELECT id, username, password_hash, created_at, updated_at FROM admin_user WHERE username = ?",
		username,
	).Scan(&user.ID, &user.Username, &hash, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query admin: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, nil
	}
	return &user, nil
}

// ResetAdminPassword updates the password for the given admin username.
func ResetAdminPassword(db *sql.DB, username, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result, err := db.Exec(
		"UPDATE admin_user SET password_hash = ?, updated_at = ? WHERE username = ?",
		string(hash), now, username,
	)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("admin user %q not found", username)
	}
	return nil
}

// GetJWTSecret retrieves the JWT signing secret from the database.
// If none exists, generates a cryptographically random 32-byte hex secret,
// stores it, and returns it. The secret persists across daemon restarts.
// Uses INSERT OR IGNORE to avoid race conditions on concurrent first-calls.
func GetJWTSecret(db *sql.DB) (string, error) {
	var secret string
	err := db.QueryRow("SELECT value FROM auth_settings WHERE key = 'jwt_secret'").Scan(&secret)
	if err == nil && secret != "" {
		return secret, nil
	}

	// Generate new secret and try to insert. INSERT OR IGNORE ensures only the
	// first writer wins if multiple goroutines race here. After insert, we always
	// SELECT to get the definitive value (which may differ from ours if we lost the race).
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	candidate := hex.EncodeToString(buf)

	_, _ = db.Exec("INSERT OR IGNORE INTO auth_settings (key, value) VALUES ('jwt_secret', ?)", candidate)

	// Always read back — the stored value is authoritative
	err = db.QueryRow("SELECT value FROM auth_settings WHERE key = 'jwt_secret'").Scan(&secret)
	if err != nil {
		return "", fmt.Errorf("read jwt secret: %w", err)
	}
	return secret, nil
}
