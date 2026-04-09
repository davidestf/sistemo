package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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
	Progress    int    `json:"progress"`     // 0-100 percentage
	ProgressMsg string `json:"progress_msg"` // current step description
}

// activeBuilds tracks running build processes for cancellation.
var (
	activeBuildsMu  sync.Mutex
	activeBuildsMap = map[string]*exec.Cmd{} // build_id -> running cmd
)

// Default build timeout (overridden by config.BuildTimeoutMin).
const defaultBuildTimeoutMin = 60

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
		_ = h.db.QueryRow("SELECT status FROM image_build WHERE build_name = ? AND status = 'building'", buildName).Scan(&existingStatus)
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
		updateBuild := func(status, message string, progress int, progressMsg string) {
			if h.db != nil {
				completedAt := ""
				if status != "building" {
					completedAt = time.Now().UTC().Format(time.RFC3339)
				}
				if _, err := h.db.Exec(
					"UPDATE image_build SET status = ?, message = ?, completed_at = ?, progress = ?, progress_msg = ? WHERE id = ? AND status = 'building'",
					status, message, completedAt, progress, progressMsg, buildID); err != nil {
					h.logger.Warn("failed to update build status", zap.String("build_id", buildID), zap.String("status", status), zap.Error(err))
				}
			}
		}

		outputPath := filepath.Join(h.cfg.DataDir, "images", buildName+".rootfs.ext4")
		sshPubKey := filepath.Join(h.cfg.DataDir, "ssh", "sistemo_key.pub")

		// Use ~/.sistemo/tmp/ for builds instead of /tmp (which may be RAM-backed tmpfs).
		buildTmpBase := filepath.Join(h.cfg.DataDir, "tmp")
		_ = os.MkdirAll(buildTmpBase, 0755)
		tmpDir, err := os.MkdirTemp(buildTmpBase, "build-*")
		if err != nil {
			updateBuild("error", fmt.Sprintf("create temp dir: %v", err), 0, "")
			return
		}
		defer os.RemoveAll(tmpDir)

		scriptPath := filepath.Join(tmpDir, "build-rootfs.sh")
		if len(h.BuildScript) > 0 {
			if err := os.WriteFile(scriptPath, h.BuildScript, 0755); err != nil {
				updateBuild("error", fmt.Sprintf("write build script: %v", err), 0, "")
				return
			}
		} else {
			scriptPath = filepath.Join(h.cfg.DataDir, "bin", "build-rootfs.sh")
			if !fileExists(scriptPath) {
				updateBuild("error", "build script not found", 0, "")
				return
			}
		}

		if len(h.VMInitScript) > 0 {
			if err := os.WriteFile(filepath.Join(tmpDir, "vm-init.sh"), h.VMInitScript, 0755); err != nil {
				updateBuild("error", fmt.Sprintf("write vm-init.sh: %v", err), 0, "")
				return
			}
		}

		if len(h.TiniStatic) > 0 {
			if err := os.WriteFile(filepath.Join(tmpDir, "tini-static"), h.TiniStatic, 0755); err != nil {
				updateBuild("error", fmt.Sprintf("write tini-static: %v", err), 0, "")
				return
			}
		}

		cmd := exec.Command("bash", scriptPath, imageName, sshPubKey, outputPath)
		cmd.Dir = tmpDir
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		// Determine rootfs size: user-provided or auto-detect from Docker image (min 5GB)
		rootfsMB := 5120
		if req.SizeMB > 0 {
			rootfsMB = req.SizeMB
			if rootfsMB < 5120 {
				rootfsMB = 5120
			}
		} else {
			if sizeOut, err := exec.Command("docker", "image", "inspect", "--format", "{{.Size}}", imageName).Output(); err == nil {
				if sizeBytes, err := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64); err == nil {
					sizeMB := int(sizeBytes/(1024*1024)) * 3 / 2
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

		// Stream output to log file instead of capturing in memory.
		logPath := filepath.Join(h.cfg.DataDir, "tmp", "build-"+buildID+".log")
		logFile, err := os.Create(logPath)
		if err != nil {
			updateBuild("error", fmt.Sprintf("create log file: %v", err), 0, "")
			return
		}

		// Pipe stdout/stderr through a scanner to parse PROGRESS markers,
		// while also writing everything to the log file.
		pr, pw := io.Pipe()
		cmd.Stdout = pw
		cmd.Stderr = pw

		// Background goroutine: read output, parse progress, write to log file
		outputDone := make(chan string, 1) // last N lines for error message
		go func() {
			defer logFile.Close()
			scanner := bufio.NewScanner(pr)
			scanner.Buffer(make([]byte, 64*1024), 256*1024)
			var lastLines []string
			for scanner.Scan() {
				line := scanner.Text()
				fmt.Fprintln(logFile, line)

				// Parse PROGRESS:N:message markers
				if strings.HasPrefix(line, "PROGRESS:") {
					parts := strings.SplitN(line, ":", 3)
					if len(parts) == 3 {
						if pct, err := strconv.Atoi(parts[1]); err == nil {
							updateBuild("building", "Building...", pct, parts[2])
						}
					}
					continue
				}

				// Keep last 20 lines for error reporting
				lastLines = append(lastLines, line)
				if len(lastLines) > 20 {
					lastLines = lastLines[1:]
				}
			}
			outputDone <- strings.Join(lastLines, "\n")
		}()

		// Register process for cancellation
		activeBuildsMu.Lock()
		activeBuildsMap[buildID] = cmd
		activeBuildsMu.Unlock()

		// Start with timeout
		timeoutMin := defaultBuildTimeoutMin
		if h.cfg.BuildTimeoutMin > 0 {
			timeoutMin = h.cfg.BuildTimeoutMin
		}

		err = cmd.Start()
		if err != nil {
			pw.Close()
			<-outputDone
			updateBuild("error", fmt.Sprintf("failed to start build: %v", err), 0, "")
			activeBuildsMu.Lock()
			delete(activeBuildsMap, buildID)
			activeBuildsMu.Unlock()
			return
		}

		// Timeout watchdog — use buildDone channel so goroutines don't
		// touch cmd.Process after Wait() has returned and reaped it.
		buildDone := make(chan struct{})
		timer := time.AfterFunc(time.Duration(timeoutMin)*time.Minute, func() {
			select {
			case <-buildDone:
				return
			default:
			}
			h.logger.Warn("build timeout reached, killing", zap.String("build_id", buildID), zap.Int("timeout_min", timeoutMin))
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			time.AfterFunc(10*time.Second, func() {
				select {
				case <-buildDone:
					return
				default:
				}
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			})
		})

		waitErr := cmd.Wait()
		close(buildDone)
		timer.Stop()
		pw.Close()
		lastOutput := <-outputDone

		// Deregister process
		activeBuildsMu.Lock()
		delete(activeBuildsMap, buildID)
		activeBuildsMu.Unlock()

		// Clean up log file after build (keep for errors)
		defer func() {
			if waitErr != nil {
				// Keep log file for debugging — will be cleaned up on next build or manually
			} else {
				os.Remove(logPath)
			}
		}()

		if waitErr != nil {
			os.Remove(outputPath)
			updateBuild("error", fmt.Sprintf("build failed: %v\n%s", waitErr, lastOutput), 0, "")
		} else {
			updateBuild("complete", fmt.Sprintf("Built %s.rootfs.ext4", buildName), 100, "Build complete")

			// Register image in content-addressable store (atomic transaction)
			if digest, hashErr := db.HashFile(outputPath); hashErr == nil {
				info, _ := os.Stat(outputPath)
				now := time.Now().UTC().Format(time.RFC3339)
				var sizeBytes int64
				if info != nil {
					sizeBytes = info.Size()
				}

				tx, txErr := h.db.Begin()
				if txErr == nil {
					defer func() { _ = tx.Rollback() }() // no-op after successful commit
					if _, err := tx.Exec("INSERT OR IGNORE INTO image (digest, name, file, path, size_bytes, source, source_ref, verified_at, created_at) VALUES (?, ?, ?, ?, ?, 'docker_build', ?, ?, ?)",
						digest, buildName, buildName+".rootfs.ext4", outputPath, sizeBytes, imageName, now, now); err != nil {
						h.logger.Warn("failed to insert image", zap.String("build_id", buildID), zap.Error(err))
					} else if _, err := tx.Exec("INSERT OR REPLACE INTO image_tag (tag, digest) VALUES (?, ?)", buildName, digest); err != nil {
						h.logger.Warn("failed to insert image tag", zap.String("build_id", buildID), zap.Error(err))
					} else if _, err := tx.Exec("UPDATE image_build SET image_digest = ? WHERE id = ?", digest, buildID); err != nil {
						h.logger.Warn("failed to update build digest", zap.String("build_id", buildID), zap.Error(err))
					} else if commitErr := tx.Commit(); commitErr != nil {
						h.logger.Warn("failed to commit image registration", zap.String("build_id", buildID), zap.Error(commitErr))
					}
				} else {
					h.logger.Warn("failed to begin image registration transaction", zap.String("build_id", buildID), zap.Error(txErr))
				}
			} else {
				h.logger.Warn("failed to hash built image", zap.String("build_id", buildID), zap.Error(hashErr))
			}
		}

		h.logger.Info("image build completed",
			zap.String("image", imageName),
			zap.String("build_id", buildID),
			zap.Bool("success", waitErr == nil),
		)
	}()

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":     "building",
		"image":      buildName,
		"build_name": buildName,
		"id":         buildID,
	})
}

// DockerfileBuild builds a rootfs from a user-provided Dockerfile.
// POST /api/v1/images/build/dockerfile (multipart/form-data)
func (h *DashboardAPI) DockerfileBuild(w http.ResponseWriter, r *http.Request) {
	// Limit total upload to 100MB (context tar can be large)
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form or upload too large (max 100MB)")
		return
	}

	dockerfileContent := r.FormValue("dockerfile")
	if strings.TrimSpace(dockerfileContent) == "" {
		writeError(w, http.StatusBadRequest, "dockerfile content is required")
		return
	}

	buildName := r.FormValue("name")
	if buildName == "" {
		buildName = fmt.Sprintf("custom-%d", time.Now().Unix())
	}
	buildName = strings.ReplaceAll(buildName, "/", "-")
	buildName = strings.ReplaceAll(buildName, ":", "-")
	if !isValidSafeID(buildName) {
		writeError(w, http.StatusBadRequest, "invalid name — must be alphanumeric with hyphens, dots, or underscores")
		return
	}

	if _, err := exec.LookPath("docker"); err != nil {
		writeError(w, http.StatusBadRequest, "Docker is not installed on this host")
		return
	}

	// Check for existing active build
	if h.db != nil {
		var existingStatus string
		_ = h.db.QueryRow("SELECT status FROM image_build WHERE build_name = ? AND status = 'building'", buildName).Scan(&existingStatus)
		if existingStatus == "building" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "build already in progress", "image": buildName})
			return
		}
	}

	// Prepare context directory
	buildTmpBase := filepath.Join(h.cfg.DataDir, "tmp")
	_ = os.MkdirAll(buildTmpBase, 0755)
	contextDir, err := os.MkdirTemp(buildTmpBase, "dockerfile-ctx-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create build context directory")
		return
	}

	// Extract context tar if provided
	contextFile, _, err := r.FormFile("context")
	if err == nil {
		defer contextFile.Close()
		// Extract tar to context dir
		extractCmd := exec.Command("tar", "-xf", "-", "-C", contextDir)
		extractCmd.Stdin = contextFile
		if out, err := extractCmd.CombinedOutput(); err != nil {
			os.RemoveAll(contextDir)
			writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to extract build context: %s", strings.TrimSpace(string(out))))
			return
		}
	}

	// Write Dockerfile (overwrites any Dockerfile from context tar)
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(dockerfileContent), 0644); err != nil {
		os.RemoveAll(contextDir)
		writeError(w, http.StatusInternalServerError, "failed to write Dockerfile")
		return
	}

	// Create build record
	buildID := fmt.Sprintf("%d", time.Now().UnixNano())
	now := time.Now().UTC().Format(time.RFC3339)
	if h.db != nil {
		if _, err := h.db.Exec("INSERT OR REPLACE INTO image_build (id, image, build_name, status, message, started_at) VALUES (?, ?, ?, 'building', 'Starting Dockerfile build...', ?)",
			buildID, "Dockerfile:"+buildName, buildName, now); err != nil {
			os.RemoveAll(contextDir)
			writeError(w, http.StatusInternalServerError, "failed to create build record")
			return
		}
	}

	sizeMBStr := r.FormValue("size_mb")
	sizeMB := 0
	if sizeMBStr != "" {
		sizeMB, _ = strconv.Atoi(sizeMBStr)
	}

	go func() {
		defer os.RemoveAll(contextDir)

		updateBuild := func(status, message string, progress int, progressMsg string) {
			if h.db != nil {
				completedAt := ""
				if status != "building" {
					completedAt = time.Now().UTC().Format(time.RFC3339)
				}
				if _, err := h.db.Exec(
					"UPDATE image_build SET status = ?, message = ?, completed_at = ?, progress = ?, progress_msg = ? WHERE id = ? AND status = 'building'",
					status, message, completedAt, progress, progressMsg, buildID); err != nil {
					h.logger.Warn("failed to update build status", zap.String("build_id", buildID), zap.String("status", status), zap.Error(err))
				}
			}
		}

		outputPath := filepath.Join(h.cfg.DataDir, "images", buildName+".rootfs.ext4")
		sshPubKey := filepath.Join(h.cfg.DataDir, "ssh", "sistemo_key.pub")

		// Resolve build script
		scriptPath := filepath.Join(h.cfg.DataDir, "bin", "build-rootfs.sh")
		if len(h.BuildScript) > 0 {
			tmpScript := filepath.Join(contextDir, "build-rootfs.sh")
			if err := os.WriteFile(tmpScript, h.BuildScript, 0755); err != nil {
				updateBuild("error", fmt.Sprintf("write build script: %v", err), 0, "")
				return
			}
			scriptPath = tmpScript
		} else if !fileExists(scriptPath) {
			updateBuild("error", "build script not found", 0, "")
			return
		}

		if len(h.VMInitScript) > 0 {
			if err := os.WriteFile(filepath.Join(contextDir, "vm-init.sh"), h.VMInitScript, 0755); err != nil {
				updateBuild("error", fmt.Sprintf("write vm-init.sh: %v", err), 0, "")
				return
			}
		}

		if len(h.TiniStatic) > 0 {
			if err := os.WriteFile(filepath.Join(contextDir, "tini-static"), h.TiniStatic, 0755); err != nil {
				updateBuild("error", fmt.Sprintf("write tini-static: %v", err), 0, "")
				return
			}
		}

		// Build command: --dockerfile mode
		cmd := exec.Command("bash", scriptPath, "--dockerfile", contextDir, sshPubKey, outputPath)
		cmd.Dir = contextDir
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		rootfsMB := 5120
		if sizeMB >= 5120 {
			rootfsMB = sizeMB
		}

		cmd.Env = append(os.Environ(),
			fmt.Sprintf("ROOTFS_SIZE_MB=%d", rootfsMB),
			"SISTEMO_BUILD_TMPDIR="+buildTmpBase,
			"SISTEMO_BUILD_TAG=sistemo-df-"+buildID,
		)

		// Stream output to log file (same pattern as ImageBuild)
		logPath := filepath.Join(h.cfg.DataDir, "tmp", "build-"+buildID+".log")
		logFile, err := os.Create(logPath)
		if err != nil {
			updateBuild("error", fmt.Sprintf("create log file: %v", err), 0, "")
			return
		}

		pr, pw := io.Pipe()
		cmd.Stdout = pw
		cmd.Stderr = pw

		outputDone := make(chan string, 1)
		go func() {
			defer logFile.Close()
			scanner := bufio.NewScanner(pr)
			scanner.Buffer(make([]byte, 64*1024), 256*1024)
			var lastLines []string
			for scanner.Scan() {
				line := scanner.Text()
				fmt.Fprintln(logFile, line)
				if strings.HasPrefix(line, "PROGRESS:") {
					parts := strings.SplitN(line, ":", 3)
					if len(parts) == 3 {
						if pct, err := strconv.Atoi(parts[1]); err == nil {
							updateBuild("building", "Building...", pct, parts[2])
						}
					}
					continue
				}
				lastLines = append(lastLines, line)
				if len(lastLines) > 20 {
					lastLines = lastLines[1:]
				}
			}
			outputDone <- strings.Join(lastLines, "\n")
		}()

		activeBuildsMu.Lock()
		activeBuildsMap[buildID] = cmd
		activeBuildsMu.Unlock()

		timeoutMin := defaultBuildTimeoutMin
		if h.cfg.BuildTimeoutMin > 0 {
			timeoutMin = h.cfg.BuildTimeoutMin
		}

		err = cmd.Start()
		if err != nil {
			pw.Close()
			<-outputDone
			updateBuild("error", fmt.Sprintf("failed to start build: %v", err), 0, "")
			activeBuildsMu.Lock()
			delete(activeBuildsMap, buildID)
			activeBuildsMu.Unlock()
			return
		}

		buildDone := make(chan struct{})
		timer := time.AfterFunc(time.Duration(timeoutMin)*time.Minute, func() {
			select {
			case <-buildDone:
				return
			default:
			}
			h.logger.Warn("build timeout reached, killing", zap.String("build_id", buildID))
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			time.AfterFunc(10*time.Second, func() {
				select {
				case <-buildDone:
					return
				default:
				}
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			})
		})

		waitErr := cmd.Wait()
		close(buildDone)
		timer.Stop()
		pw.Close()
		lastOutput := <-outputDone

		activeBuildsMu.Lock()
		delete(activeBuildsMap, buildID)
		activeBuildsMu.Unlock()

		defer func() {
			if waitErr != nil {
				// Keep log for debugging
			} else {
				os.Remove(logPath)
			}
		}()

		if waitErr != nil {
			os.Remove(outputPath)
			updateBuild("error", fmt.Sprintf("build failed: %v\n%s", waitErr, lastOutput), 0, "")
		} else {
			updateBuild("complete", fmt.Sprintf("Built %s.rootfs.ext4", buildName), 100, "Build complete")

			if digest, hashErr := db.HashFile(outputPath); hashErr == nil {
				info, _ := os.Stat(outputPath)
				now := time.Now().UTC().Format(time.RFC3339)
				var sizeBytes int64
				if info != nil {
					sizeBytes = info.Size()
				}
				tx, txErr := h.db.Begin()
				if txErr == nil {
					defer func() { _ = tx.Rollback() }()
					if _, err := tx.Exec("INSERT OR IGNORE INTO image (digest, name, file, path, size_bytes, source, source_ref, verified_at, created_at) VALUES (?, ?, ?, ?, ?, 'dockerfile_build', ?, ?, ?)",
						digest, buildName, buildName+".rootfs.ext4", outputPath, sizeBytes, "Dockerfile:"+buildName, now, now); err != nil {
						h.logger.Warn("failed to insert image", zap.String("build_id", buildID), zap.Error(err))
					} else if _, err := tx.Exec("INSERT OR REPLACE INTO image_tag (tag, digest) VALUES (?, ?)", buildName, digest); err != nil {
						h.logger.Warn("failed to insert image tag", zap.String("build_id", buildID), zap.Error(err))
					} else if _, err := tx.Exec("UPDATE image_build SET image_digest = ? WHERE id = ?", digest, buildID); err != nil {
						h.logger.Warn("failed to update build digest", zap.String("build_id", buildID), zap.Error(err))
					} else if commitErr := tx.Commit(); commitErr != nil {
						h.logger.Warn("failed to commit image registration", zap.String("build_id", buildID), zap.Error(commitErr))
					}
				}
			}
		}

		h.logger.Info("dockerfile build completed",
			zap.String("build_name", buildName),
			zap.String("build_id", buildID),
			zap.Bool("success", waitErr == nil),
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

	var bs buildStatusResponse
	// Try by id first, then by build_name
	err := h.db.QueryRow(
		"SELECT id, status, image, build_name, message, started_at, COALESCE(image_digest, ''), COALESCE(progress, 0), COALESCE(progress_msg, '') FROM image_build WHERE id = ?",
		name,
	).Scan(&bs.ID, &bs.Status, &bs.Image, &bs.BuildName, &bs.Message, &bs.StartedAt, &bs.ImageDigest, &bs.Progress, &bs.ProgressMsg)
	if err != nil {
		err = h.db.QueryRow(
			"SELECT id, status, image, build_name, message, started_at, COALESCE(image_digest, ''), COALESCE(progress, 0), COALESCE(progress_msg, '') FROM image_build WHERE build_name = ? ORDER BY started_at DESC LIMIT 1",
			name,
		).Scan(&bs.ID, &bs.Status, &bs.Image, &bs.BuildName, &bs.Message, &bs.StartedAt, &bs.ImageDigest, &bs.Progress, &bs.ProgressMsg)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "no build found for this image")
		return
	}

	// Auto-expire stale builds
	if bs.Status == "building" {
		timeoutMin := defaultBuildTimeoutMin
		if h.cfg.BuildTimeoutMin > 0 {
			timeoutMin = h.cfg.BuildTimeoutMin
		}
		if started, err := time.Parse(time.RFC3339, bs.StartedAt); err == nil {
			if time.Since(started) > time.Duration(timeoutMin)*time.Minute {
				bs.Status = "error"
				bs.Message = fmt.Sprintf("Build timed out (exceeded %d minutes)", timeoutMin)
				_, _ = h.db.Exec("UPDATE image_build SET status='error', message=?, completed_at=? WHERE id=?",
					bs.Message, time.Now().UTC().Format(time.RFC3339), bs.ID)
			}
		}
	}

	writeJSON(w, http.StatusOK, bs)
}

// ImageBuildLogs returns the last N lines of a build's log file.
// GET /api/v1/images/build/{id}/logs?tail=100
func (h *DashboardAPI) ImageBuildLogs(w http.ResponseWriter, r *http.Request) {
	buildID := chi.URLParam(r, "id")
	if !isValidSafeID(buildID) {
		writeError(w, http.StatusBadRequest, "invalid build identifier")
		return
	}

	tailN := 100
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 && n <= 1000 {
			tailN = n
		}
	}

	logPath := filepath.Join(h.cfg.DataDir, "tmp", "build-"+buildID+".log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"lines": []string{}, "total": 0})
		return
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	total := len(lines)
	if len(lines) > tailN {
		lines = lines[len(lines)-tailN:]
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"lines": lines, "total": total})
}

// ImageBuildCancel cancels a running build.
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

	var buildID string
	err := h.db.QueryRow("SELECT id FROM image_build WHERE id = ? AND status = 'building'", name).Scan(&buildID)
	if err != nil {
		err = h.db.QueryRow("SELECT id FROM image_build WHERE build_name = ? AND status = 'building'", name).Scan(&buildID)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "no active build found")
		return
	}

	// Kill with grace period: SIGTERM → 10s → SIGKILL
	activeBuildsMu.Lock()
	cmd, ok := activeBuildsMap[buildID]
	if ok && cmd.Process != nil {
		pid := cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		h.logger.Info("sent SIGTERM to build process group", zap.String("build_id", buildID), zap.Int("pid", pid))
		go func() {
			time.Sleep(10 * time.Second)
			activeBuildsMu.Lock()
			if _, still := activeBuildsMap[buildID]; still && cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				h.logger.Info("sent SIGKILL to build process group", zap.String("build_id", buildID))
			}
			activeBuildsMu.Unlock()
		}()
	}
	activeBuildsMu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = h.db.Exec("UPDATE image_build SET status='error', message='Cancelled by user', completed_at=? WHERE id=? AND status='building'",
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

	rows, err := h.db.Query("SELECT id, status, image, build_name, message, started_at, COALESCE(image_digest, ''), COALESCE(progress, 0), COALESCE(progress_msg, '') FROM image_build ORDER BY started_at DESC LIMIT 50")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"builds": []buildStatusResponse{}})
		return
	}
	defer rows.Close()

	var builds []buildStatusResponse
	for rows.Next() {
		var b buildStatusResponse
		if rows.Scan(&b.ID, &b.Status, &b.Image, &b.BuildName, &b.Message, &b.StartedAt, &b.ImageDigest, &b.Progress, &b.ProgressMsg) == nil {
			builds = append(builds, b)
		}
	}
	if builds == nil {
		builds = []buildStatusResponse{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"builds": builds})
}
