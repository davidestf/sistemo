package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
)

// safeIDPattern matches alphanumeric IDs with hyphens and underscores (no path traversal).
var safeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// isValidSafeID validates that an ID is safe to use in file paths (no traversal).
func isValidSafeID(id string) bool {
	return id != "" && len(id) <= 256 && safeIDPattern.MatchString(id)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
