package handlers

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/davidestf/sistemo/internal/agent/network"
	"go.uber.org/zap"
)

// --- Misc types ---

type auditEntry struct {
	ID         int    `json:"id"`
	Timestamp  string `json:"timestamp"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name"`
	Details    string `json:"details"`
	Success    bool   `json:"success"`
}

type volumeEntry struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	SizeMB          int     `json:"size_mb"`
	Path            string  `json:"path"`
	Status          string  `json:"status"`
	MachineID       *string `json:"machine_id"`
	Role            string  `json:"role"`
	Created         string  `json:"created"`
	LastStateChange string  `json:"last_state_change"`
}

// --- Handlers ---

// AuditLog returns audit log entries with pagination and optional action filter.
func (h *DashboardAPI) AuditLog(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}
	actionFilter := r.URL.Query().Get("action")

	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"entries": []auditEntry{}, "total": 0})
		return
	}

	// Build query with optional filter
	where := ""
	args := []interface{}{}
	if actionFilter != "" {
		where = " WHERE action = ?"
		args = append(args, actionFilter)
	}

	// Get total count
	var total int
	h.db.QueryRow("SELECT COUNT(*) FROM audit_log"+where, args...).Scan(&total)

	// Get entries
	query := "SELECT id, timestamp, action, target_type, target_id, target_name, details, success FROM audit_log" + where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	queryArgs := make([]interface{}, 0, len(args)+2)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := h.db.Query(query, queryArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query audit log")
		return
	}
	defer rows.Close()

	var entries []auditEntry
	for rows.Next() {
		var e auditEntry
		var targetType, targetID, targetName, details sql.NullString
		var success int
		if rows.Scan(&e.ID, &e.Timestamp, &e.Action, &targetType, &targetID, &targetName, &details, &success) != nil {
			continue
		}
		e.TargetType = targetType.String
		e.TargetID = targetID.String
		e.TargetName = targetName.String
		e.Details = details.String
		e.Success = success == 1
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []auditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": entries, "total": total})
}

// Volumes lists all volumes from SQLite.
func (h *DashboardAPI) Volumes(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"volumes": []volumeEntry{}})
		return
	}
	rows, err := h.db.Query("SELECT id, name, size_mb, path, status, machine_id, role, created, last_state_change FROM volume ORDER BY created DESC")
	if err != nil {
		h.logger.Warn("volumes query failed", zap.Error(err))
		writeJSON(w, http.StatusOK, map[string]interface{}{"volumes": []volumeEntry{}})
		return
	}
	defer rows.Close()

	var volumes []volumeEntry
	for rows.Next() {
		var v volumeEntry
		var machineID, role sql.NullString
		if rows.Scan(&v.ID, &v.Name, &v.SizeMB, &v.Path, &v.Status, &machineID, &role, &v.Created, &v.LastStateChange) != nil {
			continue
		}
		if machineID.Valid && machineID.String != "" {
			v.MachineID = &machineID.String
		}
		v.Role = role.String
		if v.Role == "" {
			v.Role = "data"
		}
		volumes = append(volumes, v)
	}
	if volumes == nil {
		volumes = []volumeEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"volumes": volumes})
}

// Networks returns all networks with machine counts.
func (h *DashboardAPI) Networks(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"networks": []networkV1Response{}})
		return
	}

	// Count machines on default network (no network_id)
	var defaultCount int
	h.db.QueryRow("SELECT COUNT(*) FROM machine WHERE (network_id IS NULL OR network_id = '') AND status != 'deleted'").Scan(&defaultCount)

	results := []networkV1Response{{
		Name:         "default",
		Subnet:       h.cfg.BridgeSubnet,
		BridgeName:   network.BridgeName,
		MachineCount: defaultCount,
	}}

	rows, err := h.db.Query(`
		SELECT n.name, n.subnet, n.bridge_name, n.created_at,
		       COUNT(m.id)
		FROM network n
		LEFT JOIN machine m ON m.network_id = n.id AND m.status != 'deleted'
		GROUP BY n.id
		ORDER BY n.name`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var n networkV1Response
			var createdAt sql.NullString
			if rows.Scan(&n.Name, &n.Subnet, &n.BridgeName, &createdAt, &n.MachineCount) == nil {
				n.CreatedAt = createdAt.String
				results = append(results, n)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"networks": results})
}
