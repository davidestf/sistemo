package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	sisterr "github.com/davidestf/sistemo/internal/errors"
)

// safeIDPattern matches alphanumeric IDs with hyphens, underscores, and dots.
// Dots are allowed (e.g. "node-1.0") but ".." is blocked by requiring the first char
// to be alphanumeric and not allowing consecutive dots via the overall pattern.
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

// writeSistemoError writes a structured error response from any error value.
// If the error is (or wraps) a *SistemoError, the response includes the error
// code and derives the HTTP status automatically. Otherwise it falls back to
// a plain {"error":"msg"} response using the provided status.
func writeSistemoError(w http.ResponseWriter, status int, err error) {
	var se *sisterr.SistemoError
	if errors.As(err, &se) {
		writeJSON(w, se.ToHTTPStatus(), map[string]string{
			"error": se.Message,
			"code":  string(se.Code),
		})
		return
	}
	writeError(w, status, err.Error())
}
