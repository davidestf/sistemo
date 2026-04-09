package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	gonet "net"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/davidestf/sistemo/internal/agent/network"
	"github.com/davidestf/sistemo/internal/db"
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
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	// Auto-generate bridge_name if not provided
	if req.BridgeName == "" {
		req.BridgeName = "br-" + req.Name
		if len(req.BridgeName) > 15 {
			req.BridgeName = "br-" + req.Name[:12]
		}
	}
	// Auto-assign subnet if not provided
	if req.Subnet == "" && h.db != nil {
		for i := 201; i <= 254; i++ {
			candidate := fmt.Sprintf("10.%d.0.0/24", i)
			var count int
			if err := h.db.QueryRow("SELECT COUNT(*) FROM network WHERE subnet = ?", candidate).Scan(&count); err != nil {
				continue // skip this subnet on DB error
			}
			if count == 0 {
				req.Subnet = candidate
				break
			}
		}
		if req.Subnet == "" {
			writeError(w, http.StatusConflict, "no free subnets available")
			return
		}
	} else if req.Subnet == "" {
		writeError(w, http.StatusBadRequest, "subnet is required")
		return
	}
	if !safeBridgeName.MatchString(req.BridgeName) {
		writeError(w, http.StatusBadRequest, "bridge_name must be 1-15 alphanumeric/dash/underscore characters")
		return
	}
	if !isValidSafeID(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid network name")
		return
	}
	// Validate subnet is a valid IPv4 CIDR
	if _, _, err := gonet.ParseCIDR(req.Subnet); err != nil {
		writeError(w, http.StatusBadRequest, "invalid subnet: must be a valid CIDR (e.g. 10.201.0.0/24)")
		return
	}

	if err := network.CreateNamedBridge(req.BridgeName, req.Subnet, h.logger); err != nil {
		h.logger.Error("create named bridge failed", zap.String("bridge", req.BridgeName), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Persist network record so Delete can look up the bridge_name.
	if h.db != nil {
		_, err := h.db.Exec(`INSERT INTO network (id, name, subnet, bridge_name, created_at) VALUES (?, ?, ?, ?, datetime('now'))
			ON CONFLICT(name) DO UPDATE SET subnet=excluded.subnet, bridge_name=excluded.bridge_name`,
			uuid.NewString(), req.Name, req.Subnet, req.BridgeName)
		if err != nil {
			h.logger.Error("failed to persist network record", zap.Error(err))
			// Clean up the bridge we just created — don't leave orphaned OS resources
			network.DeleteNamedBridge(req.BridgeName, h.logger)
			writeError(w, http.StatusInternalServerError, "failed to save network record")
			return
		}
	}

	db.LogAction(h.db, "create", "network", req.Name, req.Name, fmt.Sprintf("subnet=%s bridge=%s", req.Subnet, req.BridgeName), true)
	writeJSON(w, http.StatusCreated, map[string]string{
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

	// Check if any machines are using this network
	if h.db != nil {
		var machineCount int
		if err := h.db.QueryRow("SELECT COUNT(*) FROM machine WHERE network_id = (SELECT id FROM network WHERE name = ?) AND status NOT IN ('deleted')", name).Scan(&machineCount); err != nil {
			h.logger.Error("failed to check network usage", zap.String("network", name), zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to check network usage")
			return
		}
		if machineCount > 0 {
			writeError(w, http.StatusConflict, fmt.Sprintf("network %q has %d active machine(s) — delete or move them first", name, machineCount))
			return
		}
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

	// Delete network record from DB
	if h.db != nil {
		db.SafeExec(h.db, "DELETE FROM network WHERE name = ?", name)
	}

	db.LogAction(h.db, "delete", "network", name, name, "", true)
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
