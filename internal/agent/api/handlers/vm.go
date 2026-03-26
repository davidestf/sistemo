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
	"github.com/davidestf/sistemo/internal/agent/vm"
	"github.com/davidestf/sistemo/internal/db"
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

func (h *VM) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req createVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if strings.TrimSpace(req.Image) == "" && strings.TrimSpace(req.RootVolume) == "" {
		writeError(w, http.StatusBadRequest, "image or root_volume is required")
		return
	}

	vmid := ""
	if req.VMID != nil && *req.VMID != "" {
		vmid = *req.VMID
	} else {
		vmid = uuid.NewString()
	}
	// Validate VM ID to prevent path traversal (vm_id is used in filesystem paths)
	if !isValidSafeID(vmid) {
		writeError(w, http.StatusBadRequest, "invalid vm_id: must be alphanumeric, hyphens, underscores, or dots")
		return
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

	// Validate and derive name
	name := req.Name
	if name != "" && (len(name) > 128 || !isValidSafeID(name)) {
		writeError(w, http.StatusBadRequest, "invalid name: must be 1-128 alphanumeric, hyphens, underscores, or dots")
		return
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(req.Image), ".rootfs.ext4")
		name = strings.TrimSuffix(name, ".ext4")
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
	}

	// Resolve network name to bridge/subnet if provided (dashboard sends network_name)
	networkID := ""
	if req.NetworkName != "" && req.NetworkName != "default" && h.db != nil {
		var bridgeName, subnet, netID string
		err := h.db.QueryRow("SELECT id, bridge_name, subnet FROM network WHERE name = ?", req.NetworkName).Scan(&netID, &bridgeName, &subnet)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, fmt.Sprintf("network %q not found", req.NetworkName))
			return
		}
		if err != nil {
			h.logger.Error("network lookup failed", zap.String("network_name", req.NetworkName), zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to look up network")
			return
		}
		req.NetworkBridge = bridgeName
		req.NetworkSubnet = subnet
		networkID = netID
	}

	// Insert DB record before creating VM
	imageForDB := req.Image
	if imageForDB == "" && req.RootVolume != "" {
		imageForDB = "volume:" + req.RootVolume
	}
	if h.db != nil {
		now := time.Now().UTC().Format(time.RFC3339)
		_, err := h.db.Exec(
			`INSERT INTO vm (id, name, status, maintenance_operation, image, vcpus, memory_mb, storage_mb, network_id, created_at, last_state_change)
			 VALUES (?, ?, 'maintenance', 'creating', ?, ?, ?, ?, ?, ?, ?)`,
			vmid, name, imageForDB, effectiveVCPUs, effectiveMemoryMB, req.StorageMB, networkID, now, now,
		)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: vm.name") {
				writeError(w, http.StatusConflict, fmt.Sprintf("A VM named %q already exists. Use --name or delete the existing one.", name))
				return
			}
			h.logger.Error("insert vm failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to create VM record")
			return
		}
	}

	// Root volume: either use an existing volume (--volume) or create a new one
	now := time.Now().UTC().Format(time.RFC3339)
	rootVolID := ""
	rootVolPath := ""
	volumesDir := filepath.Join(filepath.Dir(h.cfg.VMBaseDir), "volumes")
	useExistingVolume := strings.TrimSpace(req.RootVolume) != ""

	if useExistingVolume && h.db != nil {
		// Boot from existing volume — look it up
		var volStatus string
		err := h.db.QueryRow(
			"SELECT id, path, status FROM volume WHERE (id = ? OR name = ?) LIMIT 1",
			req.RootVolume, req.RootVolume,
		).Scan(&rootVolID, &rootVolPath, &volStatus)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, fmt.Sprintf("volume %q not found", req.RootVolume))
			return
		}
		if err != nil {
			h.logger.Error("query volume failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to look up volume")
			return
		}
		if volStatus != "online" {
			writeError(w, http.StatusConflict, fmt.Sprintf("volume %q is %s, must be online", req.RootVolume, volStatus))
			return
		}
		// Verify the volume file actually exists on disk
		if _, err := os.Stat(rootVolPath); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("volume file missing on disk: %s", rootVolPath))
			return
		}
		// Mark as attached to this VM
		db.SafeExec(h.db, "UPDATE volume SET status='maintenance', attached=?, role='root', last_state_change=? WHERE id=?",
			vmid, now, rootVolID)
	} else if h.db != nil {
		// Create a new root volume
		rootVolID = uuid.NewString()
		rootVolName := name + "-root"
		effectiveStorageMB := req.StorageMB
		if err := os.MkdirAll(volumesDir, 0755); err != nil {
			h.logger.Error("create volumes dir failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to create volumes directory")
			return
		}
		rootVolPath = filepath.Join(volumesDir, rootVolID+".ext4")
		_, err := h.db.Exec(
			`INSERT INTO volume (id, name, size_mb, path, status, role, attached, last_state_change)
			 VALUES (?, ?, ?, ?, 'maintenance', 'root', ?, ?)`,
			rootVolID, rootVolName, effectiveStorageMB, rootVolPath, vmid, now,
		)
		if err != nil {
			// Clean up the VM record we already inserted
			h.db.Exec("DELETE FROM vm WHERE id=?", vmid)
			if strings.Contains(err.Error(), "UNIQUE constraint failed: volume.name") {
				writeError(w, http.StatusConflict, fmt.Sprintf("A volume named %q already exists. Delete it first or use a different VM name.", rootVolName))
			} else {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("insert root volume: %v", err))
			}
			return
		}
	}

	createReq := &vm.CreateRequest{
		VMID:              vmid,
		Image:             req.Image,
		VCPUs:             effectiveVCPUs,
		MemoryMB:          effectiveMemoryMB,
		StorageMB:         req.StorageMB,
		RootVolumePath:    rootVolPath,
		UseExistingVolume: useExistingVolume,
		AttachedStorage:   req.AttachedStorage,
		Metadata:          req.Metadata,
		InjectInitSSH:     req.InjectInitSSH,
		NetworkBridge:     req.NetworkBridge,
		NetworkSubnet:     req.NetworkSubnet,
	}

	// Resolve attached storage: look up volumes by ID or name in DB,
	// fall back to raw filesystem paths for backward compatibility.
	var attachedVolumeIDs []string
	if len(req.AttachedStorage) > 0 && h.db != nil {
		var resolvedPaths []string
		for _, idOrName := range req.AttachedStorage {
			var volID, volPath, volStatus string
			err := h.db.QueryRow(
				`SELECT id, path, status FROM volume WHERE (id = ? OR name = ?) LIMIT 1`,
				idOrName, idOrName,
			).Scan(&volID, &volPath, &volStatus)
			if err == sql.ErrNoRows {
				// Not in DB — only allow raw filesystem paths for CLI backward compat
				if !strings.HasPrefix(idOrName, "/") {
					if useExistingVolume {
						db.SafeExec(h.db, "UPDATE volume SET status='online', attached=NULL, last_state_change=? WHERE id=?", now, rootVolID)
					} else {
						h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
					}
					db.SafeExec(h.db, "DELETE FROM vm WHERE id=?", vmid)
					writeError(w, http.StatusNotFound, fmt.Sprintf("volume %q not found", idOrName))
					return
				}
				if _, statErr := os.Stat(idOrName); statErr != nil {
					// Clean up root volume on early return
					if useExistingVolume {
						db.SafeExec(h.db, "UPDATE volume SET status='online', attached=NULL, last_state_change=? WHERE id=?", now, rootVolID)
					} else {
						h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
					}
					db.SafeExec(h.db, "DELETE FROM vm WHERE id=?", vmid)
					writeError(w, http.StatusNotFound, fmt.Sprintf("volume %q not found in database and path does not exist", idOrName))
					return
				}
				resolvedPaths = append(resolvedPaths, idOrName)
				continue
			}
			if err != nil {
				if useExistingVolume {
					db.SafeExec(h.db, "UPDATE volume SET status='online', attached=NULL, last_state_change=? WHERE id=?", now, rootVolID)
				} else {
					h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
				}
					db.SafeExec(h.db, "DELETE FROM vm WHERE id=?", vmid)
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("query volume %q: %v", idOrName, err))
				return
			}
			if volStatus != "online" {
				if useExistingVolume {
					db.SafeExec(h.db, "UPDATE volume SET status='online', attached=NULL, last_state_change=? WHERE id=?", now, rootVolID)
				} else {
					h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
				}
					db.SafeExec(h.db, "DELETE FROM vm WHERE id=?", vmid)
				writeError(w, http.StatusConflict, fmt.Sprintf("volume %q is already attached", idOrName))
				return
			}
			resolvedPaths = append(resolvedPaths, volPath)
			attachedVolumeIDs = append(attachedVolumeIDs, volID)
		}
		createReq.AttachedStorage = resolvedPaths
	}

	// Mark data volumes as maintenance to prevent races during VM creation
	for _, volID := range attachedVolumeIDs {
		db.SafeExec(h.db, "UPDATE volume SET status='maintenance', attached=?, last_state_change=? WHERE id=?",
			vmid, now, volID)
	}

	result, err := h.mgr.Create(r.Context(), createReq)
	if err != nil {
		// Clean up immediately — don't leave failed VMs lingering in the DB.
		if h.db != nil {
			db.SafeExec(h.db, `DELETE FROM ip_allocation WHERE vm_id = ?`, vmid)
			db.SafeExec(h.db, `DELETE FROM port_rule WHERE vm_id = ?`, vmid)
			db.SafeExec(h.db, `DELETE FROM vm WHERE id = ?`, vmid)
			// Clean up root volume record and file
			if useExistingVolume {
				db.SafeExec(h.db, "UPDATE volume SET status='online', attached=NULL, last_state_change=? WHERE id=?", now, rootVolID)
			} else {
				h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
				if rootVolPath != "" {
					os.Remove(rootVolPath)
				}
			}
		}
		// Reset any volumes we were about to attach
		for _, volID := range attachedVolumeIDs {
			db.SafeExec(h.db, "UPDATE volume SET status='online', attached=NULL, last_state_change=? WHERE id=?",
				time.Now().UTC().Format(time.RFC3339), volID)
		}
		vmDir := filepath.Join(h.cfg.VMBaseDir, vmid)
		os.RemoveAll(vmDir)
		db.LogAction(h.db, "create", "vm", vmid, name, err.Error(), false)
		h.logger.Error("create VM failed", zap.Error(err))
		// Return a clean user-facing error — log has the full detail
		userMsg := err.Error()
		if strings.Contains(userMsg, "Bad magic number") || strings.Contains(userMsg, "not a valid ext4") {
			userMsg = "image is not a valid ext4 filesystem — check the file is a rootfs.ext4 image, not a compressed archive"
		} else if strings.Contains(userMsg, "e2fsck") || strings.Contains(userMsg, "resize2fs") {
			userMsg = "filesystem resize failed — the image may be corrupt or not ext4"
		} else if len(userMsg) > 200 {
			userMsg = userMsg[:200]
		}
		writeError(w, http.StatusInternalServerError, userMsg)
		return
	}

	// Mark root volume as attached and update VM with root_volume reference
	if h.db != nil {
		now = time.Now().UTC().Format(time.RFC3339)
		db.SafeExec(h.db, "UPDATE volume SET status='attached', last_state_change=? WHERE id=?", now, rootVolID)
		db.SafeExec(h.db, "UPDATE vm SET root_volume=? WHERE id=?", rootVolID, vmid)
	}

	// Mark resolved data volumes as attached to this VM
	for _, volID := range attachedVolumeIDs {
		db.SafeExec(h.db, "UPDATE volume SET status='attached', attached=?, last_state_change=? WHERE id=?",
			vmid, time.Now().UTC().Format(time.RFC3339), volID)
	}

	// Update DB with running status
	if h.db != nil {
		db.SafeExec(h.db,
			`UPDATE vm SET status = 'running', maintenance_operation = NULL, ip_address = ?, namespace = ?, last_state_change = ? WHERE id = ?`,
			result.IPAddress, result.Namespace, time.Now().UTC().Format(time.RFC3339), vmid)
	}

	db.LogAction(h.db, "create", "vm", vmid, name, fmt.Sprintf("image=%s vcpus=%d memory=%dMB", req.Image, effectiveVCPUs, effectiveMemoryMB), true)
	writeJSON(w, http.StatusCreated, result)
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

	now := time.Now().UTC().Format(time.RFC3339)
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE vm SET status = 'maintenance', maintenance_operation = 'deleting', last_state_change = ? WHERE id = ?", now, vmID)
	}

	// Look up root volume before delete (vm_spec files will be removed)
	var rootVolID sql.NullString
	if h.db != nil {
		h.db.QueryRow("SELECT root_volume FROM vm WHERE id=?", vmID).Scan(&rootVolID)
	}

	_, err := h.mgr.Delete(r.Context(), vmID, preserveStorage)
	if err != nil {
		// Mark as error — needs user attention. User can retry delete or inspect.
		if h.db != nil {
			db.SafeExec(h.db, "UPDATE vm SET status = 'error', maintenance_operation = NULL, last_state_change = ? WHERE id = ?",
				now, vmID)
			// Also reset any data volumes attached to this VM
			db.SafeExec(h.db, "UPDATE volume SET status='online', attached=NULL, last_state_change=? WHERE attached=? AND role='data'",
				now, vmID)
		}
		db.LogAction(h.db, "delete", "vm", vmID, "", err.Error(), false)
		h.logger.Error("delete VM failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Handle root volume DB record — only if still attached to this VM.
	// If user already detached it, leave it alone (it belongs to them now).
	if h.db != nil && rootVolID.Valid {
		var volAttached sql.NullString
		h.db.QueryRow("SELECT attached FROM volume WHERE id=?", rootVolID.String).Scan(&volAttached)
		stillAttached := volAttached.Valid && volAttached.String == vmID

		if stillAttached && !preserveStorage {
			db.SafeExec(h.db, "DELETE FROM volume WHERE id=?", rootVolID.String)
		} else if stillAttached && preserveStorage {
			db.SafeExec(h.db, "UPDATE volume SET status='online', attached=NULL, last_state_change=? WHERE id=?", now, rootVolID.String)
		}
		// If not attached (user already detached): do nothing — volume is independent
	}

	// Release all non-root data volumes attached to this VM
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE volume SET status='online', attached=NULL, last_state_change=? WHERE attached=?", now, vmID)
	}
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE vm SET status = 'deleted', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, vmID)
	}
	db.LogAction(h.db, "delete", "vm", vmID, "", "", true)
	writeJSON(w, http.StatusOK, vm.DeleteResponse{VMID: vmID, Terminated: true})
}

func (h *VM) Stop(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}

	// Set maintenance status before operation — if daemon crashes mid-stop,
	// startup cleanup will catch VMs stuck in maintenance.
	now := time.Now().UTC().Format(time.RFC3339)
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE vm SET status = 'maintenance', maintenance_operation = 'stopping', last_state_change = ? WHERE id = ?", now, vmID)
	}

	stopped, err := h.mgr.Stop(r.Context(), vmID)
	if err != nil {
		// Restore to running on failure
		if h.db != nil {
			db.SafeExec(h.db, "UPDATE vm SET status = 'running', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, vmID)
		}
		h.logger.Error("stop VM failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stopped && h.db != nil {
		db.SafeExec(h.db, "UPDATE vm SET status = 'stopped', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, vmID)
	}
	db.LogAction(h.db, "stop", "vm", vmID, "", "", stopped)
	if !stopped {
		// No process found — VM may already be stopped or process died.
		// Mark as stopped and return success (idempotent).
		if h.db != nil {
			db.SafeExec(h.db, "UPDATE vm SET status = 'stopped', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, vmID)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"vm_id": vmID, "stopped": true, "already_stopped": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"vm_id": vmID, "stopped": true})
}

func (h *VM) Start(w http.ResponseWriter, r *http.Request) {
	vmID := validVMID(w, r)
	if vmID == "" {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE vm SET status = 'maintenance', maintenance_operation = 'starting', last_state_change = ? WHERE id = ?", now, vmID)
	}

	result, err := h.mgr.Start(r.Context(), vmID)
	if err != nil {
		if h.db != nil {
			db.SafeExec(h.db, "UPDATE vm SET status = 'stopped', maintenance_operation = NULL, last_state_change = ? WHERE id = ?", now, vmID)
		}
		db.LogAction(h.db, "start", "vm", vmID, "", err.Error(), false)
		h.logger.Error("start VM failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE vm SET status = 'running', maintenance_operation = NULL, ip_address = ?, namespace = ?, last_state_change = ? WHERE id = ?",
			result.IPAddress, result.Namespace, now, vmID)
	}
	db.LogAction(h.db, "start", "vm", vmID, "", "", true)
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

	result, err := h.mgr.Exec(r.Context(), vmID, body.Script, body.TimeoutSec)
	if err != nil {
		db.LogAction(h.db, "exec", "vm", vmID, "", err.Error(), false)
		h.logger.Error("exec failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	db.LogAction(h.db, "exec", "vm", vmID, "", "", true)
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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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
			db.SafeExec(h.db, `DELETE FROM port_rule WHERE id = ?`, ruleID)
		}
		h.logger.Error("expose port failed", zap.String("vm_id", vmID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	db.LogAction(h.db, "expose", "vm", vmID, "", fmt.Sprintf("host:%d->vm:%d/%s", req.HostPort, req.VMPort, req.Protocol), true)
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

	// Try to remove iptables rules. If VM is stopped (not in memory), this may fail
	// but we still delete the DB row — iptables rules were already removed by stop.
	if err := h.mgr.UnexposePort(vmID, hostPort, vmPort, protocol); err != nil {
		h.logger.Warn("unexpose port iptables cleanup failed (VM may be stopped)", zap.String("vm_id", vmID), zap.Error(err))
	}

	// Always remove from DB — even if iptables cleanup failed (VM stopped, rules already gone)
	if h.db != nil {
		db.SafeExec(h.db, `DELETE FROM port_rule WHERE vm_id = ? AND host_port = ?`, vmID, hostPort)
	}

	db.LogAction(h.db, "unexpose", "vm", vmID, "", fmt.Sprintf("host:%d", hostPort), true)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"vm_id":     vmID,
		"host_port": hostPort,
		"status":    "removed",
	})
}
