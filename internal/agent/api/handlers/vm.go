// Package handlers contains HTTP handler implementations.
package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/davidestf/sistemo/internal/agent/config"
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

func (h *VM) Delete(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmID")
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
	}
	if !terminated {
		writeJSON(w, http.StatusNotFound, vm.DeleteResponse{VMID: vmID, Terminated: false})
		return
	}
	writeJSON(w, http.StatusOK, vm.DeleteResponse{VMID: vmID, Terminated: true})
}

func (h *VM) Stop(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmID")
	stopped, err := h.mgr.Stop(r.Context(), vmID)
	if err != nil {
		h.logger.Error("stop VM failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stopped && h.db != nil {
		h.db.Exec("UPDATE vm SET status = 'stopped', last_state_change = ? WHERE id = ?",
			time.Now().UTC().Format(time.RFC3339), vmID)
	}
	if !stopped {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"vm_id": vmID, "stopped": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"vm_id": vmID, "stopped": true})
}

func (h *VM) Start(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmID")
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
	vmID := chi.URLParam(r, "vmID")
	result := h.mgr.GetIP(r.Context(), vmID)
	writeJSON(w, http.StatusOK, result)
}

func (h *VM) List(w http.ResponseWriter, r *http.Request) {
	vms := h.mgr.ListVMs()
	writeJSON(w, http.StatusOK, vms)
}

func (h *VM) Exec(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmID")

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
	vmID := chi.URLParam(r, "vmID")
	if vmID == "" {
		writeError(w, http.StatusNotFound, "vm id required")
		return
	}
	logPath := filepath.Join(h.cfg.VMBaseDir, vmID, "firecracker.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "VM not found or no log file")
			return
		}
		h.logger.Error("read log failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
