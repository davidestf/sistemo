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

	if strings.TrimSpace(req.Image) == "" && strings.TrimSpace(req.RootVolume) == "" {
		writeError(w, http.StatusBadRequest, "image or root_volume is required")
		return
	}

	machineID := ""
	if req.MachineID != nil && *req.MachineID != "" {
		machineID = *req.MachineID
	} else {
		machineID = uuid.NewString()
	}
	// Validate machine ID to prevent path traversal (machine_id is used in filesystem paths)
	if !isValidSafeID(machineID) {
		writeError(w, http.StatusBadRequest, "invalid machine_id: must be alphanumeric, hyphens, underscores, or dots")
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

	h.logger.Info("create machine request",
		zap.String("machine_id", machineID),
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

	// Insert DB record before creating machine
	imageForDB := req.Image
	if imageForDB == "" && req.RootVolume != "" {
		imageForDB = "volume:" + req.RootVolume
	}
	if h.db != nil {
		now := time.Now().UTC().Format(time.RFC3339)
		_, err := h.db.Exec(
			`INSERT INTO machine (id, name, status, maintenance_operation, image, vcpus, memory_mb, storage_mb, network_id, created_at, last_state_change)
			 VALUES (?, ?, 'maintenance', 'creating', ?, ?, ?, ?, ?, ?, ?)`,
			machineID, name, imageForDB, effectiveVCPUs, effectiveMemoryMB, req.StorageMB, networkID, now, now,
		)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: machine.name") {
				writeError(w, http.StatusConflict, fmt.Sprintf("A machine named %q already exists. Use --name or delete the existing one.", name))
				return
			}
			h.logger.Error("insert machine failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to create machine record")
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
		// Mark as attached to this machine
		db.SafeExec(h.db, "UPDATE volume SET status='maintenance', machine_id=?, role='root', last_state_change=? WHERE id=?",
			machineID, now, rootVolID)
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
			`INSERT INTO volume (id, name, size_mb, path, status, role, machine_id, last_state_change)
			 VALUES (?, ?, ?, ?, 'maintenance', 'root', ?, ?)`,
			rootVolID, rootVolName, effectiveStorageMB, rootVolPath, machineID, now,
		)
		if err != nil {
			// Clean up the machine record we already inserted
			h.db.Exec("DELETE FROM machine WHERE id=?", machineID)
			if strings.Contains(err.Error(), "UNIQUE constraint failed: volume.name") {
				writeError(w, http.StatusConflict, fmt.Sprintf("A volume named %q already exists. Delete it first or use a different machine name.", rootVolName))
			} else {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("insert root volume: %v", err))
			}
			return
		}
	}

	createReq := &machine.CreateRequest{
		MachineID:         machineID,
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
						db.SafeExec(h.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE id=?", now, rootVolID)
					} else {
						h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
					}
					db.SafeExec(h.db, "DELETE FROM machine WHERE id=?", machineID)
					writeError(w, http.StatusNotFound, fmt.Sprintf("volume %q not found", idOrName))
					return
				}
				if _, statErr := os.Stat(idOrName); statErr != nil {
					// Clean up root volume on early return
					if useExistingVolume {
						db.SafeExec(h.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE id=?", now, rootVolID)
					} else {
						h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
					}
					db.SafeExec(h.db, "DELETE FROM machine WHERE id=?", machineID)
					writeError(w, http.StatusNotFound, fmt.Sprintf("volume %q not found in database and path does not exist", idOrName))
					return
				}
				resolvedPaths = append(resolvedPaths, idOrName)
				continue
			}
			if err != nil {
				if useExistingVolume {
					db.SafeExec(h.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE id=?", now, rootVolID)
				} else {
					h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
				}
					db.SafeExec(h.db, "DELETE FROM machine WHERE id=?", machineID)
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("query volume %q: %v", idOrName, err))
				return
			}
			if volStatus != "online" {
				if useExistingVolume {
					db.SafeExec(h.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE id=?", now, rootVolID)
				} else {
					h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
				}
					db.SafeExec(h.db, "DELETE FROM machine WHERE id=?", machineID)
				writeError(w, http.StatusConflict, fmt.Sprintf("volume %q is already attached", idOrName))
				return
			}
			resolvedPaths = append(resolvedPaths, volPath)
			attachedVolumeIDs = append(attachedVolumeIDs, volID)
		}
		createReq.AttachedStorage = resolvedPaths
	}

	// Mark data volumes as maintenance to prevent races during machine creation
	for _, volID := range attachedVolumeIDs {
		db.SafeExec(h.db, "UPDATE volume SET status='maintenance', machine_id=?, last_state_change=? WHERE id=?",
			machineID, now, volID)
	}

	result, err := h.mgr.Create(r.Context(), createReq)
	if err != nil {
		// Clean up immediately — don't leave failed machines lingering in the DB.
		if h.db != nil {
			db.SafeExec(h.db, `DELETE FROM ip_allocation WHERE machine_id = ?`, machineID)
			db.SafeExec(h.db, `DELETE FROM port_rule WHERE machine_id = ?`, machineID)
			db.SafeExec(h.db, `DELETE FROM machine WHERE id = ?`, machineID)
			// Clean up root volume record and file
			if useExistingVolume {
				db.SafeExec(h.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE id=?", now, rootVolID)
			} else {
				h.db.Exec("DELETE FROM volume WHERE id=?", rootVolID)
				if rootVolPath != "" {
					os.Remove(rootVolPath)
				}
			}
		}
		// Reset any volumes we were about to attach
		for _, volID := range attachedVolumeIDs {
			db.SafeExec(h.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE id=?",
				time.Now().UTC().Format(time.RFC3339), volID)
		}
		machineDir := filepath.Join(h.cfg.VMBaseDir, machineID)
		os.RemoveAll(machineDir)
		db.LogAction(h.db, "create", "machine", machineID, name, err.Error(), false)
		h.logger.Error("create machine failed", zap.Error(err))
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

	// Mark root volume as attached and update machine with root_volume reference
	if h.db != nil {
		now = time.Now().UTC().Format(time.RFC3339)
		db.SafeExec(h.db, "UPDATE volume SET status='attached', last_state_change=? WHERE id=?", now, rootVolID)
		db.SafeExec(h.db, "UPDATE machine SET root_volume=? WHERE id=?", rootVolID, machineID)
	}

	// Mark resolved data volumes as attached to this machine
	for _, volID := range attachedVolumeIDs {
		db.SafeExec(h.db, "UPDATE volume SET status='attached', machine_id=?, last_state_change=? WHERE id=?",
			machineID, time.Now().UTC().Format(time.RFC3339), volID)
	}

	// Update DB with running status
	if h.db != nil {
		db.SafeExec(h.db,
			`UPDATE machine SET status = 'running', maintenance_operation = NULL, ip_address = ?, namespace = ?, last_state_change = ? WHERE id = ?`,
			result.IPAddress, result.Namespace, time.Now().UTC().Format(time.RFC3339), machineID)
	}

	// Link machine to image digest for provenance tracking
	if h.db != nil {
		var imageDigest string
		h.db.QueryRow("SELECT digest FROM image WHERE path = ?", req.Image).Scan(&imageDigest)
		if imageDigest == "" {
			// Try by name
			imgName := filepath.Base(req.Image)
			imgName = strings.TrimSuffix(imgName, ".rootfs.ext4")
			imgName = strings.TrimSuffix(imgName, ".ext4")
			h.db.QueryRow("SELECT digest FROM image WHERE name = ? LIMIT 1", imgName).Scan(&imageDigest)
		}
		if imageDigest != "" {
			db.SafeExec(h.db, "UPDATE machine SET image_digest = ? WHERE id = ?", imageDigest, machineID)
		}
	}

	db.LogAction(h.db, "create", "machine", machineID, name, fmt.Sprintf("image=%s vcpus=%d memory=%dMB", req.Image, effectiveVCPUs, effectiveMemoryMB), true)
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

	now := time.Now().UTC().Format(time.RFC3339)
	if h.db != nil {
		db.SafeExec(h.db, "UPDATE machine SET status = 'maintenance', maintenance_operation = 'starting', last_state_change = ? WHERE id = ?", now, machineID)
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
