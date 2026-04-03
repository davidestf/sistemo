// Package handlers contains HTTP handler implementations.
package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/machine"
	"github.com/davidestf/sistemo/internal/db"
	"go.uber.org/zap"
)

type Machine struct {
	mgr    *machine.Manager
	cfg    *config.Config
	logger *zap.Logger
	db     *sql.DB
}

func NewMachine(mgr *machine.Manager, cfg *config.Config, logger *zap.Logger, db *sql.DB) *Machine {
	return &Machine{mgr: mgr, cfg: cfg, logger: logger, db: db}
}

type createMachineRequest struct {
	MachineID       *string           `json:"machine_id,omitempty"`
	Name            string            `json:"name,omitempty"`
	Image           string            `json:"image"`
	VCPUs           int               `json:"vcpus"`
	MemoryMB        int               `json:"memory_mb"`
	StorageMB       int               `json:"storage_mb,omitempty"`
	RootVolume      string            `json:"root_volume,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	AttachedStorage []string          `json:"attached_storage,omitempty"`
	InjectInitSSH   bool              `json:"inject_init_ssh,omitempty"`
	NetworkName     string            `json:"network_name,omitempty"`
	NetworkBridge   string            `json:"network_bridge,omitempty"`
	NetworkSubnet   string            `json:"network_subnet,omitempty"`
}

func ifZero(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

func (h *Machine) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req createMachineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	prov := &MachineProvisioner{db: h.db, mgr: h.mgr, cfg: h.cfg, logger: h.logger}
	result, err := prov.Provision(r.Context(), req)
	if err != nil {
		if pe, ok := err.(*ProvisionError); ok {
			writeError(w, pe.Status, pe.Message)
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// validMachineID extracts and validates the machineID URL parameter. Returns empty string on failure (response already written).
func validMachineID(w http.ResponseWriter, r *http.Request) string {
	machineID := chi.URLParam(r, "machineID")
	if !isValidSafeID(machineID) {
		writeError(w, http.StatusBadRequest, "invalid machine id")
		return ""
	}
	return machineID
}

func (h *Machine) Delete(w http.ResponseWriter, r *http.Request) {
	machineID := validMachineID(w, r)
	if machineID == "" {
		return
	}
	q := r.URL.Query()
	preserveStorage := q.Get("preserve_storage") == "true" || q.Get("preserve_storage") == "1"

	now := time.Now().UTC().Format(time.RFC3339)
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE machine SET status = 'maintenance', maintenance_operation = 'deleting', last_state_change = ? WHERE id = ?", now, machineID)
	}

	// Look up root volume before delete (vm_spec files will be removed)
	var rootVolID sql.NullString
	if h.db != nil {
		h.db.QueryRow("SELECT root_volume FROM machine WHERE id=?", machineID).Scan(&rootVolID)
	}

	_, err := h.mgr.Delete(r.Context(), machineID, preserveStorage)
	if err != nil {
		// Mark as error — needs user attention. User can retry delete or inspect.
		if h.db != nil {
			db.SafeExec(h.db, "UPDATE machine SET status = 'error', maintenance_operation = NULL, last_state_change = ? WHERE id = ?",
				now, machineID)
			// Also reset any data volumes attached to this machine
			db.SafeExec(h.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE machine_id=? AND role='data'",
				now, machineID)
		}
		db.LogAction(h.db, "delete", "machine", machineID, "", err.Error(), false)
		h.logger.Error("delete machine failed", zap.String("machine_id", machineID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Handle root volume DB record — only if still attached to this machine.
	// If user already detached it, leave it alone (it belongs to them now).
	if h.db != nil && rootVolID.Valid {
		var volAttached sql.NullString
		h.db.QueryRow("SELECT machine_id FROM volume WHERE id=?", rootVolID.String).Scan(&volAttached)
		stillAttached := volAttached.Valid && volAttached.String == machineID

		if stillAttached && !preserveStorage {
			db.SafeExec(h.db, "DELETE FROM volume WHERE id=?", rootVolID.String)
		} else if stillAttached && preserveStorage {
			db.SafeExec(h.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE id=?", now, rootVolID.String)
		}
		// If not attached (user already detached): do nothing — volume is independent
	}

	// Release all non-root data volumes attached to this machine
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE machine_id=?", now, machineID)
	}
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE machine SET status = 'deleted', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, machineID)
	}
	db.LogAction(h.db, "delete", "machine", machineID, "", "", true)
	writeJSON(w, http.StatusOK, machine.DeleteResponse{MachineID: machineID, Terminated: true})
}

func (h *Machine) Stop(w http.ResponseWriter, r *http.Request) {
	machineID := validMachineID(w, r)
	if machineID == "" {
		return
	}

	// Set maintenance status before operation — if daemon crashes mid-stop,
	// startup cleanup will catch machines stuck in maintenance.
	now := time.Now().UTC().Format(time.RFC3339)
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE machine SET status = 'maintenance', maintenance_operation = 'stopping', last_state_change = ? WHERE id = ?", now, machineID)
	}

	stopped, err := h.mgr.Stop(r.Context(), machineID)
	if err != nil {
		// Restore to running on failure
		if h.db != nil {
			db.SafeExec(h.db, "UPDATE machine SET status = 'running', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, machineID)
		}
		h.logger.Error("stop machine failed", zap.String("machine_id", machineID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stopped && h.db != nil {
		db.SafeExec(h.db, "UPDATE machine SET status = 'stopped', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, machineID)
	}
	db.LogAction(h.db, "stop", "machine", machineID, "", "", stopped)
	if !stopped {
		// No process found — machine may already be stopped or process died.
		// Mark as stopped and return success (idempotent).
		if h.db != nil {
			db.SafeExec(h.db, "UPDATE machine SET status = 'stopped', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, machineID)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"machine_id": machineID, "stopped": true, "already_stopped": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"machine_id": machineID, "stopped": true})
}

func (h *Machine) Start(w http.ResponseWriter, r *http.Request) {
	machineID := validMachineID(w, r)
	if machineID == "" {
		return
	}

	// Guard against concurrent start requests (dashboard can fire duplicates)
	now := time.Now().UTC().Format(time.RFC3339)
	if h.db != nil {
		var currentStatus string
		h.db.QueryRow("SELECT status FROM machine WHERE id = ?", machineID).Scan(&currentStatus)
		if currentStatus == "running" || currentStatus == "maintenance" {
			writeError(w, http.StatusConflict, fmt.Sprintf("machine is already %s", currentStatus))
			return
		}
		db.SafeExec(h.db, "UPDATE machine SET status = 'maintenance', maintenance_operation = 'starting', last_state_change = ? WHERE id = ? AND status = 'stopped'", now, machineID)
	}

	result, err := h.mgr.Start(r.Context(), machineID)
	if err != nil {
		if h.db != nil {
			db.SafeExec(h.db, "UPDATE machine SET status = 'stopped', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, machineID)
		}
		db.LogAction(h.db, "start", "machine", machineID, "", err.Error(), false)
		h.logger.Error("start machine failed", zap.String("machine_id", machineID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE machine SET status = 'running', maintenance_operation = NULL, ip_address = ?, namespace = ?, last_state_change = ? WHERE id = ?",
			result.IPAddress, result.Namespace, now, machineID)
	}
	db.LogAction(h.db, "start", "machine", machineID, "", "", true)
	writeJSON(w, http.StatusOK, result)
}

func (h *Machine) GetIP(w http.ResponseWriter, r *http.Request) {
	machineID := validMachineID(w, r)
	if machineID == "" {
		return
	}
	result := h.mgr.GetIP(r.Context(), machineID)
	writeJSON(w, http.StatusOK, result)
}

func (h *Machine) List(w http.ResponseWriter, r *http.Request) {
	machines := h.mgr.ListMachines()
	writeJSON(w, http.StatusOK, machines)
}

func (h *Machine) Exec(w http.ResponseWriter, r *http.Request) {
	machineID := validMachineID(w, r)
	if machineID == "" {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		Script     string `json:"script"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Script == "" {
		writeError(w, http.StatusBadRequest, "script is required")
		return
	}

	result, err := h.mgr.Exec(r.Context(), machineID, body.Script, body.TimeoutSec)
	if err != nil {
		db.LogAction(h.db, "exec", "machine", machineID, "", err.Error(), false)
		h.logger.Error("exec failed", zap.String("machine_id", machineID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	db.LogAction(h.db, "exec", "machine", machineID, "", "", true)
	writeJSON(w, http.StatusOK, result)
}

func (h *Machine) Logs(w http.ResponseWriter, r *http.Request) {
	machineID := validMachineID(w, r)
	if machineID == "" {
		return
	}
	logPath := filepath.Join(h.cfg.VMBaseDir, machineID, "firecracker.log")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "machine not found or no log file")
			return
		}
		h.logger.Error("read log failed", zap.String("machine_id", machineID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	// Stream the last 1MB if file is larger
	const maxBytes = 1 << 20 // 1MB
	info, _ := f.Stat()
	if info != nil && info.Size() > maxBytes {
		f.Seek(-maxBytes, io.SeekEnd)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, f)
}

// Expose handles POST /machines/{machineID}/expose — adds a port forwarding rule.
func (h *Machine) Expose(w http.ResponseWriter, r *http.Request) {
	machineID := validMachineID(w, r)
	if machineID == "" {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		HostPort    int    `json:"host_port"`
		MachinePort int    `json:"machine_port"`
		Protocol    string `json:"protocol"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.HostPort < 1 || req.HostPort > 65535 {
		writeError(w, http.StatusBadRequest, "host_port must be 1-65535")
		return
	}
	if req.MachinePort < 1 || req.MachinePort > 65535 {
		writeError(w, http.StatusBadRequest, "machine_port must be 1-65535")
		return
	}
	if req.Protocol == "" {
		req.Protocol = "tcp"
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" {
		writeError(w, http.StatusBadRequest, "protocol must be tcp or udp")
		return
	}

	// Claim the port in DB first (UNIQUE index prevents races between concurrent requests)
	var ruleID string
	if h.db != nil {
		ruleID = uuid.NewString()
		_, err := h.db.Exec(
			`INSERT INTO port_rule (id, machine_id, host_port, machine_port, protocol) VALUES (?, ?, ?, ?, ?)`,
			ruleID, machineID, req.HostPort, req.MachinePort, req.Protocol,
		)
		if err != nil {
			writeError(w, http.StatusConflict, fmt.Sprintf("host port %d/%s is already exposed", req.HostPort, req.Protocol))
			return
		}
	}

	// Now apply iptables rules — rollback DB if this fails
	if err := h.mgr.ExposePort(machineID, req.HostPort, req.MachinePort, req.Protocol); err != nil {
		if h.db != nil {
			db.SafeExec(h.db, `DELETE FROM port_rule WHERE id = ?`, ruleID)
		}
		h.logger.Error("expose port failed", zap.String("machine_id", machineID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	db.LogAction(h.db, "expose", "machine", machineID, "", fmt.Sprintf("host:%d->machine:%d/%s", req.HostPort, req.MachinePort, req.Protocol), true)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"machine_id":   machineID,
		"host_port":    req.HostPort,
		"machine_port": req.MachinePort,
		"protocol":     req.Protocol,
		"status":       "exposed",
	})
}

// Unexpose handles DELETE /machines/{machineID}/expose/{hostPort} — removes a port forwarding rule.
func (h *Machine) Unexpose(w http.ResponseWriter, r *http.Request) {
	machineID := validMachineID(w, r)
	if machineID == "" {
		return
	}
	hostPortStr := chi.URLParam(r, "hostPort")
	hostPort, err := strconv.Atoi(hostPortStr)
	if err != nil || hostPort < 1 || hostPort > 65535 {
		writeError(w, http.StatusBadRequest, "invalid host port")
		return
	}

	// Look up the rule in DB to get machinePort and protocol
	var machinePort int
	var protocol string
	if h.db != nil {
		err := h.db.QueryRow(
			`SELECT machine_port, protocol FROM port_rule WHERE machine_id = ? AND host_port = ?`,
			machineID, hostPort,
		).Scan(&machinePort, &protocol)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "port rule not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// Try to remove iptables rules. If machine is stopped (not in memory), this may fail
	// but we still delete the DB row — iptables rules were already removed by stop.
	if err := h.mgr.UnexposePort(machineID, hostPort, machinePort, protocol); err != nil {
		h.logger.Warn("unexpose port iptables cleanup failed (machine may be stopped)", zap.String("machine_id", machineID), zap.Error(err))
	}

	// Always remove from DB — even if iptables cleanup failed (machine stopped, rules already gone)
	if h.db != nil {
		db.SafeExec(h.db, `DELETE FROM port_rule WHERE machine_id = ? AND host_port = ?`, machineID, hostPort)
	}

	db.LogAction(h.db, "unexpose", "machine", machineID, "", fmt.Sprintf("host:%d", hostPort), true)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"machine_id": machineID,
		"host_port":  hostPort,
		"status":     "removed",
	})
}
