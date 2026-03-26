package handlers

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/davidestf/sistemo/internal/db"
	"go.uber.org/zap"
)

// --- Image types ---

type imageEntry struct {
	Name      string `json:"name"`
	File      string `json:"file"`
	Path      string `json:"path"`
	SizeMB    int64  `json:"size_mb"`
	CreatedAt string `json:"created_at"`
	Source    string `json:"source"`
}

type registryImage struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        string `json:"file"`
	Arch        string `json:"arch"`
	Downloaded  bool   `json:"downloaded"`
}

// --- Registry cache ---

var (
	registryCache     []registryImage
	registryCacheTime time.Time
	registryCacheMu   sync.Mutex

	// Track in-progress downloads to prevent duplicates
	activeDownloads   sync.Map // map[string]bool — key is image name
)

const defaultRegistryURL = "https://registry.sistemo.io/images/"
const registryCacheTTL = 5 * time.Minute

// --- Handlers ---

// Images lists available rootfs images in the images directory.
func (h *DashboardAPI) Images(w http.ResponseWriter, r *http.Request) {
	imagesDir := filepath.Join(h.cfg.DataDir, "images")
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"images": []imageEntry{}})
		return
	}

	// Build set of docker-built images for source detection
	builtImages := make(map[string]bool)
	if h.db != nil {
		rows, err := h.db.Query("SELECT DISTINCT build_name FROM image_build WHERE status = 'complete'")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					builtImages[name] = true
				}
			}
		}
	}

	var images []imageEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ext4") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".rootfs.ext4")
		if name == e.Name() {
			name = strings.TrimSuffix(e.Name(), ".ext4")
		}
		info, err := e.Info()
		if err != nil {
			continue
		}

		source := "registry"
		if builtImages[name] {
			source = "docker build"
		}

		images = append(images, imageEntry{
			Name:      name,
			File:      e.Name(),
			Path:      filepath.Join(imagesDir, e.Name()),
			SizeMB:    info.Size() / (1024 * 1024),
			CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
			Source:    source,
		})
	}
	if images == nil {
		images = []imageEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"images": images})
}

// ImageDelete removes a local image file.
// DELETE /api/v1/images/{name}
func (h *DashboardAPI) ImageDelete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !isValidSafeID(name) {
		writeError(w, http.StatusBadRequest, "invalid image name")
		return
	}

	imagesDir := filepath.Join(h.cfg.DataDir, "images")

	// Try both naming conventions
	filePath := filepath.Join(imagesDir, name+".rootfs.ext4")
	if !fileExists(filePath) {
		filePath = filepath.Join(imagesDir, name+".ext4")
	}
	if !fileExists(filePath) {
		writeError(w, http.StatusNotFound, "image not found")
		return
	}

	// Safety: verify path is under images directory
	cleanPath := filepath.Clean(filePath)
	cleanDir := filepath.Clean(imagesDir)
	if !strings.HasPrefix(cleanPath, cleanDir+"/") {
		writeError(w, http.StatusBadRequest, "invalid image path")
		return
	}

	// Images are templates — VMs get their own root volume copy at deploy time.
	// Safe to delete regardless of running VMs.
	if err := os.Remove(filePath); err != nil {
		h.logger.Error("delete image failed", zap.String("name", name), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to delete image")
		return
	}

	db.LogAction(h.db, "image.delete", "image", name, name, "", true)
	h.logger.Info("image deleted", zap.String("name", name))
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "status": "deleted"})
}

// Registry returns the list of available images from the sistemo registry.
// Results are cached for 5 minutes. Local images are marked as downloaded.
func (h *DashboardAPI) Registry(w http.ResponseWriter, r *http.Request) {
	// Check cache under lock, release before any HTTP fetch
	registryCacheMu.Lock()
	if time.Since(registryCacheTime) < registryCacheTTL && registryCache != nil {
		cached := registryCache
		registryCacheMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{"images": h.markDownloaded(cached)})
		return
	}
	// Snapshot stale cache for fallback
	staleCache := registryCache
	registryCacheMu.Unlock()

	// Fetch registry index (no lock held — won't block other requests)
	regURL := os.Getenv("SISTEMO_REGISTRY_URL")
	if regURL == "" {
		regURL = defaultRegistryURL
	}
	if !strings.HasSuffix(regURL, "/") {
		regURL += "/"
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(regURL + "index.json")
	if err != nil {
		h.logger.Warn("failed to fetch registry index", zap.Error(err))
		if staleCache != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"images": h.markDownloaded(staleCache)})
		} else {
			writeJSON(w, http.StatusOK, map[string]interface{}{"images": h.fallbackRegistry()})
		}
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != 200 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"images": h.fallbackRegistry()})
		return
	}

	// Registry index format: { "version": 1, "images": [...] }
	var index struct {
		Images []registryImage `json:"images"`
	}
	if json.Unmarshal(body, &index) != nil {
		// Try bare array fallback
		var bare []registryImage
		if json.Unmarshal(body, &bare) == nil {
			index.Images = bare
		} else {
			writeJSON(w, http.StatusOK, map[string]interface{}{"images": h.fallbackRegistry()})
			return
		}
	}

	// Filter by current arch (registry uses "x86_64"/"arm64", Go uses "amd64"/"arm64")
	goArch := runtime.GOARCH
	registryArch := goArch
	if goArch == "amd64" {
		registryArch = "x86_64"
	}
	var filtered []registryImage
	for _, img := range index.Images {
		if img.Arch == "" || img.Arch == registryArch || img.Arch == goArch {
			// Skip arm64-suffixed names on amd64 and vice versa
			if strings.HasSuffix(img.Name, "-arm64") && goArch != "arm64" {
				continue
			}
			filtered = append(filtered, img)
		}
	}

	// Update cache under lock
	registryCacheMu.Lock()
	registryCache = filtered
	registryCacheTime = time.Now()
	registryCacheMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{"images": h.markDownloaded(filtered)})
}

func (h *DashboardAPI) markDownloaded(images []registryImage) []registryImage {
	imagesDir := filepath.Join(h.cfg.DataDir, "images")
	result := make([]registryImage, len(images))
	copy(result, images)
	for i := range result {
		// Check if image file exists locally
		localPath := filepath.Join(imagesDir, result[i].Name+".rootfs.ext4")
		result[i].Downloaded = fileExists(localPath)
	}
	return result
}

func (h *DashboardAPI) fallbackRegistry() []registryImage {
	imagesDir := filepath.Join(h.cfg.DataDir, "images")
	names := []string{"debian", "ubuntu", "almalinux"}
	var images []registryImage
	for _, name := range names {
		localPath := filepath.Join(imagesDir, name+".rootfs.ext4")
		images = append(images, registryImage{
			Name:        name,
			Description: name + " Linux",
			File:        name + ".rootfs.ext4",
			Arch:        runtime.GOARCH,
			Downloaded:  fileExists(localPath),
		})
	}
	return images
}

// CleanupOrphanedDownloads removes stale download-* temp files from the images directory.
// Call this on startup to clean up after interrupted downloads.
func (h *DashboardAPI) CleanupOrphanedDownloads() {
	imagesDir := filepath.Join(h.cfg.DataDir, "images")
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "download-") {
			path := filepath.Join(imagesDir, e.Name())
			h.logger.Info("cleaning up orphaned download temp file", zap.String("path", path))
			os.Remove(path)
		}
	}
}

// RegistryDownload downloads a registry image to the local images directory.
// POST /api/v1/registry/download { "name": "debian" }
func (h *DashboardAPI) RegistryDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !isValidSafeID(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid image name")
		return
	}

	regURL := os.Getenv("SISTEMO_REGISTRY_URL")
	if regURL == "" {
		regURL = defaultRegistryURL
	}
	if !strings.HasSuffix(regURL, "/") {
		regURL += "/"
	}

	// Arch suffix: arm64 adds "-arm64", amd64 has no suffix (matches CLI behavior)
	suffix := ""
	if runtime.GOARCH == "arm64" {
		suffix = "-arm64"
	}

	outputPath := filepath.Join(h.cfg.DataDir, "images", req.Name+".rootfs.ext4")

	if fileExists(outputPath) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_exists", "path": outputPath})
		return
	}

	// Prevent duplicate concurrent downloads of the same image
	if _, loaded := activeDownloads.LoadOrStore(req.Name, true); loaded {
		writeJSON(w, http.StatusOK, map[string]string{"status": "downloading", "name": req.Name})
		return
	}
	defer activeDownloads.Delete(req.Name)

	os.MkdirAll(filepath.Dir(outputPath), 0755)

	// Try gzip first, then uncompressed (same order as CLI)
	var downloadURL string
	gzURL := regURL + req.Name + suffix + ".rootfs.ext4.gz"
	plainURL := regURL + req.Name + suffix + ".rootfs.ext4"

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Head(gzURL)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			downloadURL = gzURL
		} else {
			downloadURL = plainURL
		}
	} else {
		downloadURL = plainURL
	}

	h.logger.Info("downloading registry image", zap.String("name", req.Name), zap.String("url", downloadURL))

	resp, err = client.Get(downloadURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("download failed: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("registry returned %d", resp.StatusCode))
		return
	}

	// Write to temp file then rename (atomic)
	tmpFile, err := os.CreateTemp(filepath.Dir(outputPath), "download-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create temp file failed")
		return
	}
	tmpPath := tmpFile.Name()

	var reader io.Reader = resp.Body
	if strings.HasSuffix(downloadURL, ".gz") {
		gzReader, gzErr := gzip.NewReader(resp.Body)
		if gzErr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			writeError(w, http.StatusBadGateway, "invalid gzip data")
			return
		}
		reader = gzReader
		defer gzReader.Close()
	}

	if _, err := io.Copy(tmpFile, reader); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("download write failed: %v", err))
		return
	}
	tmpFile.Close()

	if err := os.Rename(tmpPath, outputPath); err != nil {
		os.Remove(tmpPath)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("move file failed: %v", err))
		return
	}

	// Invalidate registry cache so next fetch shows updated downloaded state
	registryCacheMu.Lock()
	registryCacheTime = time.Time{}
	registryCacheMu.Unlock()

	h.logger.Info("registry image downloaded", zap.String("name", req.Name), zap.String("path", outputPath))
	writeJSON(w, http.StatusOK, map[string]string{"status": "downloaded", "name": req.Name, "path": outputPath})
}
