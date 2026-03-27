package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/davidestf/sistemo/internal/db"
	"go.uber.org/zap"
)

// --- Build types ---

type buildStatusResponse struct {
	ID          string `json:"id"`
	Status      string `json:"status"` // building, complete, error
	Image       string `json:"image"`
	BuildName   string `json:"build_name"`
	Message     string `json:"message"`
	StartedAt   string `json:"started_at"`
	ImageDigest string `json:"image_digest,omitempty"`
}

// activeBuilds tracks running build processes for cancellation.
var (
	activeBuildsMu sync.Mutex
	activeBuildsMap = map[string]*exec.Cmd{} // build_id -> running cmd
)

// --- Handlers ---

// ImageBuild starts a background Docker image build. State is persisted to DB
// so it survives page refreshes and daemon restarts.
// POST /api/v1/images/build { "image": "nginx:latest" }
func (h *DashboardAPI) ImageBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image  string `json:"image"`
		SizeMB int    `json:"size_mb"` // optional rootfs size in MB (default: auto, min 5120)
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Image == "" {
		writeError(w, http.StatusBadRequest, "image is required")
		return
	}

	buildName := strings.Split(req.Image, ":")[0]
	buildName = strings.ReplaceAll(buildName, "/", "-")
	if !isValidSafeID(buildName) {
		writeError(w, http.StatusBadRequest, "invalid image name — must be alphanumeric with hyphens, dots, or underscores")
		return
	}

	if _, err := exec.LookPath("docker"); err != nil {
		writeError(w, http.StatusBadRequest, "Docker is not installed on this host")
		return
	}

	// Check for existing active build in DB
	if h.db != nil {
		var existingStatus string
		h.db.QueryRow("SELECT status FROM image_build WHERE build_name = ? AND status = 'building'", buildName).Scan(&existingStatus)
		if existingStatus == "building" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "build already in progress", "image": buildName})
			return
		}
	}

	// Create build record in DB
	buildID := fmt.Sprintf("%d", time.Now().UnixNano())
	now := time.Now().UTC().Format(time.RFC3339)
	if h.db != nil {
		if _, err := h.db.Exec("INSERT OR REPLACE INTO image_build (id, image, build_name, status, message, started_at) VALUES (?, ?, ?, 'building', 'Starting build...', ?)",
			buildID, req.Image, buildName, now); err != nil {
			h.logger.Error("failed to insert build record", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "failed to create build record")
			return
		}
	}

	imageName := req.Image

	go func() {
		updateBuild := func(status, message string) {
			if h.db != nil {
				completedAt := ""
				if status != "building" {
					completedAt = time.Now().UTC().Format(time.RFC3339)
				}
				// Only update if still in 'building' state — prevents overwriting a cancelled build
				if _, err := h.db.Exec("UPDATE image_build SET status = ?, message = ?, completed_at = ? WHERE id = ? AND status = 'building'",
					status, message, completedAt, buildID); err != nil {
					h.logger.Warn("failed to update build status", zap.String("build_id", buildID), zap.String("status", status), zap.Error(err))
				}
			}
		}

		outputPath := filepath.Join(h.cfg.DataDir, "images", buildName+".rootfs.ext4")
		sshPubKey := filepath.Join(h.cfg.DataDir, "ssh", "sistemo_key.pub")

		// Use ~/.sistemo/tmp/ for builds instead of /tmp (which may be RAM-backed tmpfs).
		// Large Docker images (3GB+) need disk-backed storage.
		buildTmpBase := filepath.Join(h.cfg.DataDir, "tmp")
		os.MkdirAll(buildTmpBase, 0755)
		tmpDir, err := os.MkdirTemp(buildTmpBase, "build-*")
		if err != nil {
			updateBuild("error", fmt.Sprintf("create temp dir: %v", err))
			return
		}
		defer os.RemoveAll(tmpDir)

		scriptPath := filepath.Join(tmpDir, "build-rootfs.sh")
		if len(h.BuildScript) > 0 {
			if err := os.WriteFile(scriptPath, h.BuildScript, 0755); err != nil {
				updateBuild("error", fmt.Sprintf("write build script: %v", err))
				return
			}
		} else {
			scriptPath = filepath.Join(h.cfg.DataDir, "bin", "build-rootfs.sh")
			if !fileExists(scriptPath) {
				updateBuild("error", "build script not found")
				return
			}
		}

		if len(h.VMInitScript) > 0 {
			if err := os.WriteFile(filepath.Join(tmpDir, "vm-init.sh"), h.VMInitScript, 0755); err != nil {
				updateBuild("error", fmt.Sprintf("write vm-init.sh: %v", err))
				return
			}
		}

		updateBuild("building", "Building rootfs from Docker image...")

		cmd := exec.Command("bash", scriptPath, imageName, sshPubKey, outputPath)
		cmd.Dir = tmpDir
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // create process group for clean kill
		// Determine rootfs size: user-provided or auto-detect from Docker image (min 5GB)
		rootfsMB := 5120 // 5 GB minimum
		if req.SizeMB > 0 {
			rootfsMB = req.SizeMB
			if rootfsMB < 5120 {
				rootfsMB = 5120
			}
		} else {
			// Auto-size: check Docker image size and add 50% headroom
			if sizeOut, err := exec.Command("docker", "image", "inspect", "--format", "{{.Size}}", imageName).Output(); err == nil {
				if sizeBytes, err := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64); err == nil {
					sizeMB := int(sizeBytes/(1024*1024)) * 3 / 2 // 1.5x the image size
					if sizeMB > rootfsMB {
						rootfsMB = sizeMB
					}
				}
			}
		}

		cmd.Env = append(os.Environ(),
			fmt.Sprintf("ROOTFS_SIZE_MB=%d", rootfsMB),
			"SISTEMO_BUILD_TMPDIR="+buildTmpBase,
		)

		// Register process for cancellation
		activeBuildsMu.Lock()
		activeBuildsMap[buildID] = cmd
		activeBuildsMu.Unlock()

		output, err := cmd.CombinedOutput()

		// Deregister process
		activeBuildsMu.Lock()
		delete(activeBuildsMap, buildID)
		activeBuildsMu.Unlock()

		if err != nil {
			// Clean up partial/corrupt image file on failure
			os.Remove(outputPath)

			msg := string(output)
			if len(msg) > 500 {
				msg = msg[:500]
			}
			updateBuild("error", fmt.Sprintf("build failed: %v %s", err, msg))
		} else {
			updateBuild("complete", fmt.Sprintf("Built %s.rootfs.ext4", buildName))

			// Register image in content-addressable store
			if digest, hashErr := db.HashFile(outputPath); hashErr == nil {
				info, _ := os.Stat(outputPath)
				now := time.Now().UTC().Format(time.RFC3339)
				var sizeBytes int64
				if info != nil {
					sizeBytes = info.Size()
				}
				if _, err := h.db.Exec("INSERT OR IGNORE INTO image (digest, name, file, path, size_bytes, source, source_ref, verified_at, created_at) VALUES (?, ?, ?, ?, ?, 'docker_build', ?, ?, ?)",
					digest, buildName, buildName+".rootfs.ext4", outputPath, sizeBytes, imageName, now, now); err != nil {
					h.logger.Warn("failed to register built image", zap.String("build_id", buildID), zap.Error(err))
				}
				if _, err := h.db.Exec("INSERT OR IGNORE INTO image_tag (tag, digest) VALUES (?, ?)", buildName, digest); err != nil {
					h.logger.Warn("failed to insert image tag", zap.String("build_id", buildID), zap.Error(err))
				}
				if _, err := h.db.Exec("UPDATE image_build SET image_digest = ? WHERE id = ?", digest, buildID); err != nil {
					h.logger.Warn("failed to update build digest", zap.String("build_id", buildID), zap.Error(err))
				}
			} else {
				h.logger.Warn("failed to hash built image", zap.String("build_id", buildID), zap.Error(hashErr))
			}
		}

		h.logger.Info("image build completed",
			zap.String("image", imageName),
			zap.String("build_id", buildID),
			zap.Bool("success", err == nil),
		)
	}()

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":     "building",
		"image":      buildName,
		"build_name": buildName,
		"id":         buildID,
	})
}

// ImageBuildStatus returns the status of a build.
// Accepts both build_id and build_name for backward compat.
// GET /api/v1/images/build/{name}/status
func (h *DashboardAPI) ImageBuildStatus(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !isValidSafeID(name) {
		writeError(w, http.StatusBadRequest, "invalid build identifier")
		return
	}

	if h.db == nil {
		writeError(w, http.StatusNotFound, "no build found")
		return
	}

	// Try by id first, then by build_name (backward compat)
	var bs buildStatusResponse
	err := h.db.QueryRow(
		"SELECT id, status, image, build_name, message, started_at, COALESCE(image_digest, '') FROM image_build WHERE id = ?",
		name,
	).Scan(&bs.ID, &bs.Status, &bs.Image, &bs.BuildName, &bs.Message, &bs.StartedAt, &bs.ImageDigest)
	if err != nil {
		err = h.db.QueryRow(
			"SELECT id, status, image, build_name, message, started_at, COALESCE(image_digest, '') FROM image_build WHERE build_name = ? ORDER BY started_at DESC LIMIT 1",
			name,
		).Scan(&bs.ID, &bs.Status, &bs.Image, &bs.BuildName, &bs.Message, &bs.StartedAt, &bs.ImageDigest)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "no build found for this image")
		return
	}

	// Auto-expire stale builds: if "building" for more than 10 minutes, mark as error
	if bs.Status == "building" {
		if started, err := time.Parse(time.RFC3339, bs.StartedAt); err == nil {
			if time.Since(started) > 30*time.Minute {
				bs.Status = "error"
				bs.Message = "Build timed out (exceeded 30 minutes)"
				h.db.Exec("UPDATE image_build SET status='error', message=?, completed_at=? WHERE id=?",
					bs.Message, time.Now().UTC().Format(time.RFC3339), bs.ID)
			}
		}
	}

	writeJSON(w, http.StatusOK, bs)
}

// ImageBuildCancel cancels a running build by killing the process and marking it as error.
// Accepts both build_id and build_name for backward compat.
// POST /api/v1/images/build/{name}/cancel
func (h *DashboardAPI) ImageBuildCancel(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !isValidSafeID(name) {
		writeError(w, http.StatusBadRequest, "invalid build identifier")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusNotFound, "no build found")
		return
	}

	// Find the active build — try by id first, then by build_name
	var buildID string
	err := h.db.QueryRow("SELECT id FROM image_build WHERE id = ? AND status = 'building'", name).Scan(&buildID)
	if err != nil {
		err = h.db.QueryRow("SELECT id FROM image_build WHERE build_name = ? AND status = 'building'", name).Scan(&buildID)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "no active build found")
		return
	}

	// Kill the process if it's still running.
	// Hold the lock while accessing cmd.Process to avoid a TOCTOU race
	// where the build goroutine could modify the map between lookup and kill.
	activeBuildsMu.Lock()
	cmd, ok := activeBuildsMap[buildID]
	if ok && cmd.Process != nil {
		pid := cmd.Process.Pid
		// Kill the entire process group (bash + docker + child processes)
		syscall.Kill(-pid, syscall.SIGKILL)
		h.logger.Info("killed build process group", zap.String("build_id", buildID), zap.Int("pid", pid))
	}
	activeBuildsMu.Unlock()

	// Mark as cancelled in DB
	now := time.Now().UTC().Format(time.RFC3339)
	h.db.Exec("UPDATE image_build SET status='error', message='Cancelled by user', completed_at=? WHERE id=? AND status='building'",
		now, buildID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled", "build_id": buildID})
}

// ImageBuilds returns all recent builds.
// GET /api/v1/images/builds
func (h *DashboardAPI) ImageBuilds(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"builds": []buildStatusResponse{}})
		return
	}

	rows, err := h.db.Query("SELECT id, status, image, build_name, message, started_at, COALESCE(image_digest, '') FROM image_build ORDER BY started_at DESC LIMIT 20")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"builds": []buildStatusResponse{}})
		return
	}
	defer rows.Close()

	var builds []buildStatusResponse
	for rows.Next() {
		var b buildStatusResponse
		if rows.Scan(&b.ID, &b.Status, &b.Image, &b.BuildName, &b.Message, &b.StartedAt, &b.ImageDigest) == nil {
			builds = append(builds, b)
		}
	}
	if builds == nil {
		builds = []buildStatusResponse{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"builds": builds})
}
