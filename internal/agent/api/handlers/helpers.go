package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// safeIDPattern matches alphanumeric IDs with hyphens, underscores, and dots.
// Dots are allowed (e.g. "node-1.0"). Consecutive dots (..) are blocked by
// explicit check in isValidSafeID below.
var safeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// isValidSafeID validates that an ID is safe to use in file paths (no traversal).
func isValidSafeID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	if !safeIDPattern.MatchString(id) {
		return false
	}
	// Block path traversal even with dots allowed in names
	if strings.Contains(id, "..") {
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
