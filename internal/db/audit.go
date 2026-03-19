package db

import (
	"database/sql"
	"log"
	"time"
)

// LogAction records an operation in the audit log.
// Errors are logged but do not fail the operation — audit is best-effort.
func LogAction(db *sql.DB, action, targetType, targetID, targetName, details string, success bool) {
	if db == nil {
		return
	}
	succ := 0
	if success {
		succ = 1
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO audit_log (timestamp, action, target_type, target_id, target_name, details, success)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts, action, targetType, targetID, targetName, details, succ,
	)
	if err != nil {
		log.Printf("audit_log write failed: %v", err)
	}
}
