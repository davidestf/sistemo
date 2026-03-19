package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/davidestf/sistemo/internal/agent/network"
	"go.uber.org/zap"
)

var safeBridgeName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,15}$`)

// Network handles network-related API requests.
type Network struct {
	logger *zap.Logger
	db     *sql.DB
}

// NewNetwork creates a Network handler.
func NewNetwork(logger *zap.Logger, db *sql.DB) *Network {
	return &Network{logger: logger, db: db}
}

// Create handles POST /networks — creates a named bridge.
func (h *Network) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Name       string `json:"name"`
		Subnet     string `json:"subnet"`
		BridgeName string `json:"bridge_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" || req.Subnet == "" || req.BridgeName == "" {
		writeError(w, http.StatusBadRequest, "name, subnet, and bridge_name are required")
		return
	}
	if !safeBridgeName.MatchString(req.BridgeName) {
		writeError(w, http.StatusBadRequest, "bridge_name must be 1-15 alphanumeric/dash/underscore characters")
		return
	}

	if err := network.CreateNamedBridge(req.BridgeName, req.Subnet, h.logger); err != nil {
		h.logger.Error("create named bridge failed", zap.String("bridge", req.BridgeName), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"name":   req.Name,
		"subnet": req.Subnet,
		"bridge": req.BridgeName,
		"status": "created",
	})
}

// Delete handles DELETE /networks/{name} — removes a named bridge.
func (h *Network) Delete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !isValidSafeID(name) {
		writeError(w, http.StatusBadRequest, "invalid network name")
		return
	}
	if name == "default" || name == "sistemo0" {
		writeError(w, http.StatusBadRequest, "cannot delete the default network")
		return
	}

	// Look up bridge_name from DB; fall back to derivation for backwards compat.
	var bridgeName string
	if h.db != nil {
		err := h.db.QueryRow("SELECT bridge_name FROM network WHERE name = ?", name).Scan(&bridgeName)
		if err != nil && err != sql.ErrNoRows {
			h.logger.Error("db lookup for bridge_name failed", zap.String("network", name), zap.Error(err))
		}
	}
	if bridgeName == "" {
		bridgeName = "br-" + name
		if len(bridgeName) > 15 {
			bridgeName = "br-" + name[:12]
		}
	}
	if !safeBridgeName.MatchString(bridgeName) {
		writeError(w, http.StatusBadRequest, "invalid bridge name derived from network name")
		return
	}

	network.DeleteNamedBridge(bridgeName, h.logger)

	writeJSON(w, http.StatusOK, map[string]string{
		"name":   name,
		"status": "deleted",
	})
}

// List handles GET /networks — lists named networks.
func (h *Network) List(w http.ResponseWriter, r *http.Request) {
	bridges := network.ListNamedBridges()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"default": fmt.Sprintf("sistemo0 (%s)", network.BridgeCIDR),
		"bridges": bridges,
	})
}
