package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/davidestf/sistemo/internal/db"
	"go.uber.org/zap"
)

// Volume handles volume-related API requests.
type Volume struct {
	logger  *zap.Logger
	db      *sql.DB
	dataDir string
}

// NewVolume creates a Volume handler.
func NewVolume(logger *zap.Logger, db *sql.DB, dataDir string) *Volume {
	return &Volume{logger: logger, db: db, dataDir: dataDir}
}

// Create handles POST /volumes — creates a new persistent volume.
func (h *Volume) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Name   string `json:"name"`
		SizeMB int    `json:"size_mb"`
		Role   string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.SizeMB <= 0 {
		writeError(w, http.StatusBadRequest, "size_mb is required and must be positive")
		return
	}
	if req.Role == "" {
		req.Role = "data"
	}

	id := uuid.NewString()

	if req.Name == "" {
		req.Name = id[:8]
	}
	if !isValidSafeID(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid volume name")
		return
	}

	if err := os.MkdirAll(h.dataDir, 0o755); err != nil {
		h.logger.Error("create volumes dir failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to create volumes directory")
		return
	}

	volPath := filepath.Join(h.dataDir, id+".ext4")

	// Truncate file to the requested size.
	f, err := os.Create(volPath)
	if err != nil {
		h.logger.Error("create volume file failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to create volume file")
		return
	}
	if err := f.Truncate(int64(req.SizeMB) * 1024 * 1024); err != nil {
		f.Close()
		os.Remove(volPath)
		h.logger.Error("truncate volume file failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to allocate volume")
		return
	}
	f.Close()

	// Format as ext4.
	cmd := exec.Command("mkfs.ext4", "-q", "-F", volPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(volPath)
		h.logger.Error("mkfs.ext4 failed", zap.Error(err), zap.String("output", string(out)))
		writeError(w, http.StatusInternalServerError, "failed to format volume")
		return
	}

	// Insert into DB.
	_, err = h.db.Exec(
		`INSERT INTO volume (id, name, size_mb, path, status, role) VALUES (?, ?, ?, ?, 'online', ?)`,
		id, req.Name, req.SizeMB, volPath, req.Role,
	)
	if err != nil {
		os.Remove(volPath)
		h.logger.Error("insert volume record failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to save volume record")
		return
	}

	db.LogAction(h.db, "create", "volume", id, req.Name, fmt.Sprintf("size_mb=%d role=%s", req.SizeMB, req.Role), true)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      id,
		"name":    req.Name,
		"size_mb": req.SizeMB,
		"path":    volPath,
		"status":  "online",
		"role":    req.Role,
	})
}

// List handles GET /volumes — lists all volumes.
func (h *Volume) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT id, name, size_mb, path, status, attached, created, last_state_change, role FROM volume")
	if err != nil {
		h.logger.Error("list volumes failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to list volumes")
		return
	}
	defer rows.Close()

	type volumeRow struct {
		ID              string  `json:"id"`
		Name            string  `json:"name"`
		SizeMB          int     `json:"size_mb"`
		Path            string  `json:"path"`
		Status          string  `json:"status"`
		Attached        *string `json:"attached"`
		Created         string  `json:"created"`
		LastStateChange string  `json:"last_state_change"`
		Role            string  `json:"role"`
	}

	var volumes []volumeRow
	for rows.Next() {
		var v volumeRow
		if err := rows.Scan(&v.ID, &v.Name, &v.SizeMB, &v.Path, &v.Status, &v.Attached, &v.Created, &v.LastStateChange, &v.Role); err != nil {
			h.logger.Error("scan volume row failed", zap.Error(err))
			continue
		}
		volumes = append(volumes, v)
	}
	if volumes == nil {
		volumes = []volumeRow{}
	}
	writeJSON(w, http.StatusOK, volumes)
}

// Get handles GET /volumes/{idOrName} — returns a single volume.
func (h *Volume) Get(w http.ResponseWriter, r *http.Request) {
	idOrName := chi.URLParam(r, "idOrName")
	if !isValidSafeID(idOrName) {
		writeError(w, http.StatusBadRequest, "invalid volume id or name")
		return
	}

	var id, name, path, status, created, lastChange, role string
	var sizeMB int
	var attached *string
	err := h.db.QueryRow(
		"SELECT id, name, size_mb, path, status, attached, created, last_state_change, role FROM volume WHERE id = ? OR name = ?",
		idOrName, idOrName,
	).Scan(&id, &name, &sizeMB, &path, &status, &attached, &created, &lastChange, &role)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if err != nil {
		h.logger.Error("get volume failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to get volume")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":                id,
		"name":              name,
		"size_mb":           sizeMB,
		"path":              path,
		"status":            status,
		"attached":          attached,
		"created":           created,
		"last_state_change": lastChange,
		"role":              role,
	})
}

// Delete handles DELETE /volumes/{idOrName} — removes a volume.
func (h *Volume) Delete(w http.ResponseWriter, r *http.Request) {
	idOrName := chi.URLParam(r, "idOrName")
	if !isValidSafeID(idOrName) {
		writeError(w, http.StatusBadRequest, "invalid volume id or name")
		return
	}

	var id, name, path, status string
	var attached *string
	err := h.db.QueryRow(
		"SELECT id, name, path, status, attached FROM volume WHERE id = ? OR name = ?",
		idOrName, idOrName,
	).Scan(&id, &name, &path, &status, &attached)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if err != nil {
		h.logger.Error("lookup volume for delete failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to look up volume")
		return
	}

	if status == "attached" {
		vmID := ""
		if attached != nil {
			vmID = *attached
		}
		writeError(w, http.StatusConflict, fmt.Sprintf("volume is attached to VM %s, detach first", vmID))
		return
	}

	// Remove file from disk.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		h.logger.Error("remove volume file failed", zap.String("path", path), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to remove volume file")
		return
	}

	// Delete from DB.
	if _, err := h.db.Exec("DELETE FROM volume WHERE id = ?", id); err != nil {
		h.logger.Error("delete volume record failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to delete volume record")
		return
	}

	db.LogAction(h.db, "delete", "volume", id, name, "", true)
	writeJSON(w, http.StatusOK, map[string]string{
		"id":     id,
		"name":   name,
		"status": "deleted",
	})
}

// Resize handles POST /volumes/{idOrName}/resize — grows a volume (offline only).
func (h *Volume) Resize(w http.ResponseWriter, r *http.Request) {
	idOrName := chi.URLParam(r, "idOrName")
	if !isValidSafeID(idOrName) {
		writeError(w, http.StatusBadRequest, "invalid volume id or name")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		SizeMB int `json:"size_mb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.SizeMB <= 0 {
		writeError(w, http.StatusBadRequest, "size_mb is required and must be positive")
		return
	}

	// Look up volume.
	var volID, volName, volPath, volStatus string
	var currentSizeMB int
	var attached *string
	err := h.db.QueryRow(
		"SELECT id, name, path, status, size_mb, attached FROM volume WHERE id = ? OR name = ?",
		idOrName, idOrName,
	).Scan(&volID, &volName, &volPath, &volStatus, &currentSizeMB, &attached)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if err != nil {
		h.logger.Error("lookup volume for resize failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to look up volume")
		return
	}

	// Must not be attached to a running VM.
	if attached != nil && *attached != "" {
		var vmStatus string
		if h.db.QueryRow("SELECT status FROM vm WHERE id = ?", *attached).Scan(&vmStatus) == nil && vmStatus == "running" {
			writeError(w, http.StatusConflict, "stop the VM first — cannot resize while running")
			return
		}
	}

	if req.SizeMB <= currentSizeMB {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("new size (%d MB) must be larger than current size (%d MB) — shrinking is not supported", req.SizeMB, currentSizeMB))
		return
	}

	// Grow the file.
	f, err := os.OpenFile(volPath, os.O_WRONLY, 0)
	if err != nil {
		h.logger.Error("open volume for resize failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to open volume file")
		return
	}
	if err := f.Truncate(int64(req.SizeMB) * 1024 * 1024); err != nil {
		f.Close()
		h.logger.Error("truncate volume failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to resize volume file")
		return
	}
	f.Close()

	// Run e2fsck + resize2fs.
	if out, err := exec.Command("e2fsck", "-f", "-y", volPath).CombinedOutput(); err != nil {
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		// e2fsck exit code 1 = errors corrected (OK)
		if exitCode != 0 && exitCode != 1 {
			h.logger.Error("e2fsck failed during resize", zap.Error(err), zap.String("output", string(out)))
			writeError(w, http.StatusInternalServerError, "filesystem check failed before resize")
			return
		}
	}
	if out, err := exec.Command("resize2fs", volPath).CombinedOutput(); err != nil {
		h.logger.Error("resize2fs failed", zap.Error(err), zap.String("output", string(out)))
		writeError(w, http.StatusInternalServerError, "filesystem resize failed")
		return
	}

	// Update DB.
	if _, err := h.db.Exec(
		"UPDATE volume SET size_mb=?, last_state_change=strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?",
		req.SizeMB, volID,
	); err != nil {
		h.logger.Error("update volume size failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to update volume record")
		return
	}

	db.LogAction(h.db, "resize", "volume", volID, volName, fmt.Sprintf("from=%dMB to=%dMB", currentSizeMB, req.SizeMB), true)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":      volID,
		"name":    volName,
		"size_mb": req.SizeMB,
		"status":  "resized",
	})
}

// Attach handles POST /vms/{vmID}/volume/attach — attaches a volume to a stopped VM.
func (h *Volume) Attach(w http.ResponseWriter, r *http.Request) {
	vmIDParam := chi.URLParam(r, "vmID")
	if !isValidSafeID(vmIDParam) {
		writeError(w, http.StatusBadRequest, "invalid VM id")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Volume string `json:"volume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Volume == "" {
		writeError(w, http.StatusBadRequest, "volume is required (name or id)")
		return
	}

	// Look up volume.
	var volID, volName, volStatus string
	err := h.db.QueryRow(
		"SELECT id, name, status FROM volume WHERE id = ? OR name = ?",
		req.Volume, req.Volume,
	).Scan(&volID, &volName, &volStatus)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if err != nil {
		h.logger.Error("lookup volume for attach failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to look up volume")
		return
	}
	if volStatus != "online" {
		writeError(w, http.StatusConflict, fmt.Sprintf("volume status is %q, must be online to attach", volStatus))
		return
	}

	// Validate VM exists and is not running.
	var vmID, vmStatus string
	err = h.db.QueryRow(
		"SELECT id, status FROM vm WHERE (id = ? OR name = ?) AND status != 'deleted'",
		vmIDParam, vmIDParam,
	).Scan(&vmID, &vmStatus)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "VM not found")
		return
	}
	if err != nil {
		h.logger.Error("lookup VM for attach failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to look up VM")
		return
	}
	if vmStatus == "running" {
		writeError(w, http.StatusConflict, "stop the VM first")
		return
	}

	// Update volume.
	if _, err := h.db.Exec(
		"UPDATE volume SET status='attached', attached=?, last_state_change=strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?",
		vmID, volID,
	); err != nil {
		h.logger.Error("attach volume update failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to attach volume")
		return
	}

	db.LogAction(h.db, "attach", "volume", volID, volName, fmt.Sprintf("vm=%s", vmID), true)
	writeJSON(w, http.StatusOK, map[string]string{
		"id":     volID,
		"name":   volName,
		"status": "attached",
		"vm_id":  vmID,
	})
}

// Detach handles POST /vms/{vmID}/volume/detach — detaches a volume from a stopped VM.
func (h *Volume) Detach(w http.ResponseWriter, r *http.Request) {
	vmIDParam := chi.URLParam(r, "vmID")
	if !isValidSafeID(vmIDParam) {
		writeError(w, http.StatusBadRequest, "invalid VM id")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Volume string `json:"volume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Volume == "" {
		writeError(w, http.StatusBadRequest, "volume is required (name or id)")
		return
	}

	// Look up volume.
	var volID, volName, volStatus string
	var attached *string
	err := h.db.QueryRow(
		"SELECT id, name, status, attached FROM volume WHERE id = ? OR name = ?",
		req.Volume, req.Volume,
	).Scan(&volID, &volName, &volStatus, &attached)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if err != nil {
		h.logger.Error("lookup volume for detach failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to look up volume")
		return
	}
	if volStatus != "attached" {
		writeError(w, http.StatusConflict, "volume is not attached")
		return
	}

	// Check attached VM status.
	if attached != nil && *attached != "" {
		var vmStatus string
		err = h.db.QueryRow("SELECT status FROM vm WHERE id = ? AND status != 'deleted'", *attached).Scan(&vmStatus)
		if err == nil && vmStatus == "running" {
			writeError(w, http.StatusConflict, "stop the VM first")
			return
		}
		// If VM not found or deleted, allow detach to proceed.
	}

	// Update volume.
	if _, err := h.db.Exec(
		"UPDATE volume SET status='online', attached=NULL, last_state_change=strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?",
		volID,
	); err != nil {
		h.logger.Error("detach volume update failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to detach volume")
		return
	}

	db.LogAction(h.db, "detach", "volume", volID, volName, "", true)
	writeJSON(w, http.StatusOK, map[string]string{
		"id":     volID,
		"name":   volName,
		"status": "online",
	})
}
