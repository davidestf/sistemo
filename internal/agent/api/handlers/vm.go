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
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/network"
	"github.com/davidestf/sistemo/internal/agent/vm"
	"go.uber.org/zap"
)

type VM struct {
	mgr    *vm.Manager
	cfg    *config.Config
	logger *zap.Logger
	db     *sql.DB
}

func NewVM(mgr *vm.Manager, cfg *config.Config, logger *zap.Logger, db *sql.DB) *VM {
	return &VM{mgr: mgr, cfg: cfg, logger: logger, db: db}
}

type createVMRequest struct {
	VMID            *string           `json:"vm_id,omitempty"`
	Name            string            `json:"name,omitempty"`
	Image           string            `json:"image"`
	VCPUs           int               `json:"vcpus"`
	MemoryMB        int               `json:"memory_mb"`
	StorageMB       int               `json:"storage_mb,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	AttachedStorage []string          `json:"attached_storage,omitempty"`
	InjectInitSSH   bool              `json:"inject_init_ssh,omitempty"`
}

func ifZero(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

func (h *VM) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req createVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if strings.TrimSpace(req.Image) == "" {
		writeError(w, http.StatusBadRequest, "image is required")
		return
	}

	vmid := ""
	if req.VMID != nil && *req.VMID != "" {
		vmid = *req.VMID
	} else {
		vmid = uuid.NewString()
	}

	effectiveVCPUs := ifZero(req.VCPUs, 1)
	effectiveMemoryMB := ifZero(req.MemoryMB, 512)

	maxVCPUs := h.cfg.MaxVCPUs
	if maxVCPUs <= 0 {
		maxVCPUs = 64
	}
	maxMemoryMB := h.cfg.MaxMemoryMB
	if maxMemoryMB <= 0 {
		maxMemoryMB = 262144
	}
	if effectiveVCPUs < 1 || effectiveVCPUs > maxVCPUs {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("vcpus must be 1..%d", maxVCPUs))
		return
	}
	if effectiveMemoryMB < 128 || effectiveMemoryMB > maxMemoryMB {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("memory_mb must be 128..%d", maxMemoryMB))
		return
	}
	maxStorageMB := h.cfg.MaxStorageMB
	if maxStorageMB <= 0 {
		maxStorageMB = 102400
	}
	if req.StorageMB > 0 && req.StorageMB > maxStorageMB {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("storage_mb exceeds max %d", maxStorageMB))
		return
	}

	h.logger.Info("create VM request",
		zap.String("vm_id", vmid),
		zap.Int("requested_vcpus", req.VCPUs),
		zap.Int("requested_memory_mb", req.MemoryMB),
		zap.Int("effective_memory_mb", effectiveMemoryMB),
		zap.Int("storage_mb", req.StorageMB))

	// Derive name from image if not provided
	name := req.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(req.Image), ".rootfs.ext4")
		name = strings.TrimSuffix(name, ".ext4")
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
	}

	// Insert DB record before creating VM
	if h.db != nil {
		now := time.Now().UTC().Format(time.RFC3339)
		_, err := h.db.Exec(
			`INSERT INTO vm (id, name, status, maintenance_operation, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
			 VALUES (?, ?, 'maintenance', 'creating', ?, ?, ?, ?, ?, ?)`,
			vmid, name, req.Image, effectiveVCPUs, effectiveMemoryMB, req.StorageMB, now, now,
		)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: vm.name") {
				writeError(w, http.StatusConflict, fmt.Sprintf("A VM named %q already exists. Use --name or destroy the existing one.", name))
				return
			}
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("insert vm: %v", err))
			return
		}
	}

	createReq := &vm.CreateRequest{
		VMID:            vmid,
		Image:           req.Image,
		VCPUs:           effectiveVCPUs,
		MemoryMB:        effectiveMemoryMB,
		StorageMB:       req.StorageMB,
		AttachedStorage: req.AttachedStorage,
		Metadata:        req.Metadata,
		InjectInitSSH:   req.InjectInitSSH,
	}

	result, err := h.mgr.Create(r.Context(), createReq)
	if err != nil {
		// Mark as error in DB
		if h.db != nil {
			h.db.Exec("UPDATE vm SET status = 'error', maintenance_operation = NULL, last_state_change = ? WHERE id = ?",
				time.Now().UTC().Format(time.RFC3339), vmid)
		}
		h.logger.Error("create VM failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Update DB with running status
	if h.db != nil {
		h.db.Exec(
			`UPDATE vm SET status = 'running', maintenance_operation = NULL, ip_address = ?, namespace = ?, last_state_change = ? WHERE id = ?`,
			result.IPAddress, result.Namespace, time.Now().UTC().Format(time.RFC3339), vmid)
	}

	writeJSON(w, http.StatusOK, result)
}

// validVMID extracts and validates the vmID URL parameter. Returns empty string on failure (response already written).
func validVMID(w http.ResponseWriter, r *http.Request) string {
	vmID := chi.URLParam(r, "vmID")
	if !isValidSafeID(vmID) {
		writeError(w, http.StatusBadRequest, "invalid vm id")
		return ""
	}
	return vmID
}

func (h *VM) Delete(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}
	q := r.URL.Query()
	preserveStorage := q.Get("preserve_storage") == "true" || q.Get("preserve_storage") == "1"

	terminated, err := h.mgr.Delete(r.Context(), vmID, preserveStorage)
	if err != nil {
		h.logger.Error("delete VM failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.db != nil {
		h.db.Exec("UPDATE vm SET status = 'destroyed', last_state_change = ? WHERE id = ?",
			time.Now().UTC().Format(time.RFC3339), vmID)
		h.db.Exec("DELETE FROM port_rule WHERE vm_id = ?", vmID)
	}
	if !terminated {
		writeJSON(w, http.StatusNotFound, vm.DeleteResponse{VMID: vmID, Terminated: false})
		return
	}
	writeJSON(w, http.StatusOK, vm.DeleteResponse{VMID: vmID, Terminated: true})
}

func (h *VM) Stop(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}

	stopped, err := h.mgr.Stop(r.Context(), vmID)
	if err != nil {
		h.logger.Error("stop VM failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stopped && h.db != nil {
		h.db.Exec("UPDATE vm SET status = 'stopped', last_state_change = ? WHERE id = ?",
			time.Now().UTC().Format(time.RFC3339), vmID)
		// Port rule DB rows are kept so restorePortRules can re-apply them on start.
		// The iptables rules were already removed by stopVM.
	}
	if !stopped {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"vm_id": vmID, "stopped": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"vm_id": vmID, "stopped": true})
}

func (h *VM) Start(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}
	result, err := h.mgr.Start(r.Context(), vmID)
	if err != nil {
		h.logger.Error("start VM failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.db != nil {
		h.db.Exec("UPDATE vm SET status = 'running', ip_address = ?, namespace = ?, last_state_change = ? WHERE id = ?",
			result.IPAddress, result.Namespace, time.Now().UTC().Format(time.RFC3339), vmID)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *VM) GetIP(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}
	result := h.mgr.GetIP(r.Context(), vmID)
	writeJSON(w, http.StatusOK, result)
}

func (h *VM) List(w http.ResponseWriter, r *http.Request) {
	vms := h.mgr.ListVMs()
	writeJSON(w, http.StatusOK, vms)
}

func (h *VM) Exec(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}

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

	result, err := h.mgr.Exec(r.Context(), vmID, body.Script, body.TimeoutSec)
	if err != nil {
		h.logger.Error("exec failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *VM) Logs(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}
	logPath := filepath.Join(h.cfg.VMBaseDir, vmID, "firecracker.log")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "VM not found or no log file")
			return
		}
		h.logger.Error("read log failed", zap.String("vm_id", vmID), zap.Error(err))
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

// Expose handles POST /vms/{vmID}/expose — adds a port forwarding rule.
func (h *VM) Expose(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}
	var req struct {
		HostPort int    `json:"host_port"`
		VMPort   int    `json:"vm_port"`
		Protocol string `json:"protocol"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.HostPort < 1 || req.HostPort > 65535 {
		writeError(w, http.StatusBadRequest, "host_port must be 1-65535")
		return
	}
	if req.VMPort < 1 || req.VMPort > 65535 {
		writeError(w, http.StatusBadRequest, "vm_port must be 1-65535")
		return
	}
	if req.Protocol == "" {
		req.Protocol = "tcp"
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" {
		writeError(w, http.StatusBadRequest, "protocol must be tcp or udp")
		return
	}

	if !network.IsPortAvailable(req.HostPort, req.Protocol) {
		writeError(w, http.StatusConflict, fmt.Sprintf("host port %d/%s is already in use", req.HostPort, req.Protocol))
		return
	}

	// Claim the port in DB first (UNIQUE index prevents races between concurrent requests)
	var ruleID string
	if h.db != nil {
		ruleID = uuid.NewString()
		_, err := h.db.Exec(
			`INSERT INTO port_rule (id, vm_id, host_port, vm_port, protocol) VALUES (?, ?, ?, ?, ?)`,
			ruleID, vmID, req.HostPort, req.VMPort, req.Protocol,
		)
		if err != nil {
			writeError(w, http.StatusConflict, fmt.Sprintf("host port %d/%s is already exposed", req.HostPort, req.Protocol))
			return
		}
	}

	// Now apply iptables rules — rollback DB if this fails
	if err := h.mgr.ExposePort(vmID, req.HostPort, req.VMPort, req.Protocol); err != nil {
		if h.db != nil {
			h.db.Exec(`DELETE FROM port_rule WHERE id = ?`, ruleID)
		}
		h.logger.Error("expose port failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"vm_id":     vmID,
		"host_port": req.HostPort,
		"vm_port":   req.VMPort,
		"protocol":  req.Protocol,
		"status":    "exposed",
	})
}

// Unexpose handles DELETE /vms/{vmID}/expose/{hostPort} — removes a port forwarding rule.
func (h *VM) Unexpose(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}
	hostPortStr := chi.URLParam(r, "hostPort")
	hostPort, err := strconv.Atoi(hostPortStr)
	if err != nil || hostPort < 1 || hostPort > 65535 {
		writeError(w, http.StatusBadRequest, "invalid host port")
		return
	}

	// Look up the rule in DB to get vmPort and protocol
	var vmPort int
	var protocol string
	if h.db != nil {
		err := h.db.QueryRow(
			`SELECT vm_port, protocol FROM port_rule WHERE vm_id = ? AND host_port = ?`,
			vmID, hostPort,
		).Scan(&vmPort, &protocol)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "port rule not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if err := h.mgr.UnexposePort(vmID, hostPort, vmPort, protocol); err != nil {
		h.logger.Error("unexpose port failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Remove from DB
	if h.db != nil {
		h.db.Exec(`DELETE FROM port_rule WHERE vm_id = ? AND host_port = ?`, vmID, hostPort)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"vm_id":     vmID,
		"host_port": hostPort,
		"status":    "removed",
	})
}
