package db

import (
	"database/sql"
	"log"
	"time"
)

// SafeExec runs a DB exec and logs any error. Use for fire-and-forget operations
// (status updates, cleanup) where a failure shouldn't abort the caller.
func SafeExec(db *sql.DB, query string, args ...interface{}) {
	if db == nil {
		return
	}
	if _, err := db.Exec(query, args...); err != nil {
		log.Printf("db exec failed: %v (query: %s)", err, truncate(query, 80))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

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
