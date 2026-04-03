package main

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/daemon"
	"github.com/davidestf/sistemo/internal/db"
)

// Default image registry. Override with SISTEMO_REGISTRY_URL.
const defaultRegistryURL = "https://registry.sistemo.io/images/"

func registryURL() string {
	if u := os.Getenv("SISTEMO_REGISTRY_URL"); u != "" {
		return strings.TrimSuffix(u, "/") + "/"
	}
	return defaultRegistryURL
}

// registryImage describes an image available on the remote registry.
type registryImage struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        string `json:"file"`
	Arch        string `json:"arch"`
}

// registryIndex is the JSON structure at registry.sistemo.io/images/index.json.
type registryIndex struct {
	Version int             `json:"version"`
	Images  []registryImage `json:"images"`
}

// fetchRegistryIndex fetches the image list from the remote registry.
// Returns nil on any error (network, parse, timeout).
func fetchRegistryIndex() *registryIndex {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(registryURL() + "index.json")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var idx registryIndex
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return nil
	}
	return &idx
}

// knownRemoteImages is the fallback list when the registry is unreachable.
var knownRemoteImages = []string{"debian", "ubuntu", "almalinux"}

// archSuffix returns "-arm64" on ARM64 systems, empty string on x86_64.
func archSuffix() string {
	if runtime.GOARCH == "arm64" {
		return "-arm64"
	}
	return ""
}

// resolveImage resolves an image argument to an absolute path or URL.
// Order: URL -> local file -> local cache -> registry download -> error.
func resolveImage(logger *zap.Logger, dataDir, image string) (string, error) {
	// 1. HTTP/HTTPS URL — pass through to daemon
	if strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		return image, nil
	}

	// 2. Explicit local file (contains / or ends in .ext4)
	if strings.Contains(image, "/") || strings.HasSuffix(image, ".ext4") {
		abs, err := filepath.Abs(image)
		if err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs, nil
			}
		}
		return "", fmt.Errorf("image file not found: %s", image)
	}

	// 3. Cached in ~/.sistemo/images/
	localPath := findLocalImage(dataDir, image)
	if localPath != "" {
		return localPath, nil
	}

	// 4. Download from registry
	// On ARM64: try debian-arm64.rootfs.ext4.gz first, fall back to debian.rootfs.ext4.gz
	imagesDir := filepath.Join(dataDir, "images")
	os.MkdirAll(imagesDir, 0755)
	dest := filepath.Join(imagesDir, image+".rootfs.ext4")
	suffix := archSuffix()
	// Don't double the suffix if the image name already includes it (e.g. "debian-arm64")
	if strings.HasSuffix(image, suffix) {
		suffix = ""
	}

	registryFile := image + suffix + ".rootfs.ext4.gz"
	gzURL := registryURL() + registryFile

	fmt.Printf("Downloading %s from %s...\n", image, registryURL())
	err := downloadImageToFile(gzURL, dest)
	if err != nil {
		// Try uncompressed
		rawURL := registryURL() + image + suffix + ".rootfs.ext4"
		err = downloadImageToFile(rawURL, dest)
	}
	if err != nil {
		os.Remove(dest) // clean up partial download
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "Image %q not found.\n\n", image)
		fmt.Fprintln(os.Stderr, "Sistemo looked in:")
		fmt.Fprintf(os.Stderr, "  1. Local images:  %s (not found)\n", filepath.Join(dataDir, "images"))
		fmt.Fprintf(os.Stderr, "  2. Registry:      %s%s (failed: %v)\n", registryURL(), image+suffix+".rootfs.ext4.gz", err)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "To get an image:")
		fmt.Fprintf(os.Stderr, "  Build from Docker:   sudo sistemo image build %s\n", image)
		fmt.Fprintln(os.Stderr, "  List available:      sistemo image list")
		fmt.Fprintln(os.Stderr, "  Use a local file:    sistemo vm deploy ./path/to/rootfs.ext4")
		fmt.Fprintln(os.Stderr, "")
		return "", fmt.Errorf("image %q not found", image)
	}
	fmt.Printf("Saved to %s\n", dest)
	return dest, nil
}

// findLocalImage searches ~/.sistemo/images/ for a rootfs matching the given name.
// Checks exact match only: name.rootfs.ext4, name.ext4, or name.
func findLocalImage(dataDir, name string) string {
	imagesDir := filepath.Join(dataDir, "images")

	// 1. Exact: name.rootfs.ext4
	candidates := []string{
		filepath.Join(imagesDir, name+".rootfs.ext4"),
		filepath.Join(imagesDir, name+".ext4"),
		filepath.Join(imagesDir, name),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			if abs != "" {
				return abs
			}
			return p
		}
	}

	// 2. Docker tag variants: "myapp:latest" -> "myapp.rootfs.ext4"
	cleaned := strings.ReplaceAll(name, ":", "-")
	cleaned = strings.TrimSuffix(cleaned, "-latest")
	if cleaned != name {
		extra := []string{
			filepath.Join(imagesDir, cleaned+".rootfs.ext4"),
			filepath.Join(imagesDir, cleaned+".ext4"),
		}
		for _, p := range extra {
			if _, err := os.Stat(p); err == nil {
				abs, _ := filepath.Abs(p)
				if abs != "" {
					return abs
				}
				return p
			}
		}
	}

	return ""
}

func imageName(image string) string {
	if image == "" {
		return "vm"
	}
	// URL or file path — use last path segment without extensions
	if strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") || strings.Contains(image, "/") {
		base := filepath.Base(image)
		base = strings.TrimSuffix(base, ".ext4")
		base = strings.TrimSuffix(base, ".rootfs")
		if base != "" && base != "." {
			return base
		}
	}
	// Docker image with tag (e.g. "debian:12") — strip tag
	if idx := strings.Index(image, ":"); idx > 0 {
		return image[:idx]
	}
	return image
}

func parseSizeMB(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	s = strings.ToUpper(s)
	if strings.HasSuffix(s, "GB") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "GB"))
		if err != nil {
			return 0, err
		}
		return n * 1024, nil
	}
	if strings.HasSuffix(s, "G") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "G"))
		if err != nil {
			return 0, err
		}
		return n * 1024, nil
	}
	if strings.HasSuffix(s, "MB") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "MB"))
		if err != nil {
			return 0, err
		}
		return n, nil
	}
	if strings.HasSuffix(s, "M") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "M"))
		if err != nil {
			return 0, err
		}
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func downloadImageToFile(url, dest string) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	closeFile := true
	defer func() {
		if closeFile {
			f.Close()
		}
	}()
	var src io.Reader
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			os.Remove(dest)
			return err
		}
		defer zr.Close()
		src = zr
	} else {
		peek := make([]byte, 2)
		n, _ := io.ReadFull(resp.Body, peek)
		if n == 2 && peek[0] == 0x1f && peek[1] == 0x8b {
			zr, err := gzip.NewReader(io.MultiReader(bytes.NewReader(peek), resp.Body))
			if err != nil {
				os.Remove(dest)
				return err
			}
			defer zr.Close()
			src = zr
		} else {
			src = io.MultiReader(bytes.NewReader(peek[:n]), resp.Body)
		}
	}
	if _, err := io.Copy(f, src); err != nil {
		os.Remove(dest)
		return err
	}
	if err := f.Sync(); err != nil {
		os.Remove(dest)
		return fmt.Errorf("sync downloaded file: %w", err)
	}
	closeFile = false
	if err := f.Close(); err != nil {
		os.Remove(dest)
		return fmt.Errorf("close downloaded file: %w", err)
	}
	return nil
}

func runDeploy(logger *zap.Logger, database *sql.DB, image string, vcpus, memoryMB, storageMB int, attachPaths []string, nameOverride string, exposePorts []string, networkName string, rootVolume string) error {
	if image != "" && !filepath.IsAbs(image) && !strings.HasPrefix(image, "http://") && !strings.HasPrefix(image, "https://") {
		if abs, err := filepath.Abs(image); err == nil {
			if _, err := os.Stat(abs); err == nil {
				image = abs
			}
		}
	}
	if vcpus < 1 {
		vcpus = 1
	}
	if memoryMB < 128 {
		memoryMB = 128
	}
	if storageMB < 256 {
		storageMB = 256
	}
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	name := nameOverride
	if name == "" {
		name = imageName(image)
	}
	// Resolve network name to ID, bridge, subnet if specified
	var networkID, networkBridge, networkSubnet string
	if networkName != "" {
		err := database.QueryRow("SELECT id, bridge_name, subnet FROM network WHERE name = ?", networkName).Scan(&networkID, &networkBridge, &networkSubnet)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Network %q not found. Create it with: sistemo network create %s\n", networkName, networkName)
			return fmt.Errorf("network %q not found", networkName)
		}
	}

	logger.Info("sending create VM request", zap.Int("vcpus", vcpus), zap.Int("memory_mb", memoryMB), zap.Int("storage_mb", storageMB))
	req := &daemon.CreateMachineRequest{
		Name:            name,
		Image:           image,
		VCPUs:           vcpus,
		MemoryMB:        memoryMB,
		StorageMB:       storageMB,
		RootVolume:      rootVolume,
		AttachedStorage: attachPaths,
		InjectInitSSH:   true,
		NetworkBridge:   networkBridge,
		NetworkSubnet:   networkSubnet,
	}
	resp, err := daemon.CreateMachine(baseURL, req)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "already exists") {
			fmt.Fprintf(os.Stderr, "A VM named %q already exists. Use a different name (--name) or delete the existing one first:\n", name)
			fmt.Fprintf(os.Stderr, "  sistemo vm delete %s\n", name)
			return fmt.Errorf("VM named %q already exists", name)
		}
		if (strings.Contains(errStr, "Permission denied") || strings.Contains(errStr, "Operation not permitted")) &&
			(strings.Contains(errStr, "netns") || strings.Contains(errStr, "mount")) {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Network namespace setup failed: the daemon must run as root.")
			fmt.Fprintln(os.Stderr, "  1. Stop the daemon (Ctrl+C in the terminal where 'sistemo up' is running)")
			fmt.Fprintln(os.Stderr, "  2. Start it as root: sudo ./sistemo up")
			fmt.Fprintln(os.Stderr, "  3. In this terminal run again: sistemo vm deploy <image>")
			fmt.Fprintln(os.Stderr, "")
		}
		return fmt.Errorf("create VM: %w", err)
	}
	// Store network association
	if networkID != "" && database != nil {
		db.SafeExec(database, "UPDATE machine SET network_id = ? WHERE id = ?", networkID, resp.MachineID)
	}

	fmt.Printf("Deployed %q as %s (%s)\n", image, name, resp.MachineID)
	fmt.Printf("  IP: %s  Namespace: %s  Boot: %dms\n", resp.IPAddress, resp.Namespace, resp.BootTimeMS)
	if networkName != "" {
		fmt.Printf("  Network: %s\n", networkName)
	}
	fmt.Printf("  Dashboard: %s/dashboard/#/machines/%s\n", baseURL, resp.MachineID)

	// Expose ports if requested
	for _, portSpec := range exposePorts {
		hostPort, vmPort, err := parsePortMapping(portSpec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: invalid port spec %q: %v\n", portSpec, err)
			continue
		}
		if err := daemon.ExposePort(baseURL, resp.MachineID, hostPort, vmPort, "tcp"); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: expose port %d: %v\n", hostPort, err)
			continue
		}
		fmt.Printf("  Exposed host:%d -> VM:%d (tcp)\n", hostPort, vmPort)
	}
	return nil
}

func runBuild(logger *zap.Logger, dataDir, image, outPath string) error {
	dataDir = getDataDir(dataDir)
	sshDir := filepath.Join(dataDir, "ssh")
	keyPath := filepath.Join(sshDir, "sistemo_key")
	pubPath := keyPath + ".pub"
	if err := generateSSHKeyWithLock(sshDir, keyPath); err != nil {
		return fmt.Errorf("generate SSH key: %w", err)
	}
	if outPath == "" {
		base := strings.TrimSuffix(filepath.Base(image), ":latest")
		if base == filepath.Base(image) {
			base = strings.ReplaceAll(base, ":", "-")
		}
		// Save to ~/.sistemo/images/ so 'sistemo vm deploy <name>' finds it automatically
		imagesDir := filepath.Join(dataDir, "images")
		os.MkdirAll(imagesDir, 0755)
		outPath = filepath.Join(imagesDir, base+".rootfs.ext4")
	}

	// Use ~/.sistemo/tmp/ for builds instead of /tmp (which may be RAM-backed tmpfs).
	buildTmpBase := filepath.Join(dataDir, "tmp")
	os.MkdirAll(buildTmpBase, 0755)
	tmpDir, err := os.MkdirTemp(buildTmpBase, "build-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	buildScript := filepath.Join(tmpDir, "build-rootfs.sh")
	if err := os.WriteFile(buildScript, embeddedBuildScript, 0755); err != nil {
		return fmt.Errorf("write build script: %w", err)
	}
	vmInitScript := filepath.Join(tmpDir, "vm-init.sh")
	if err := os.WriteFile(vmInitScript, embeddedVMInit, 0755); err != nil {
		return fmt.Errorf("write vm-init script: %w", err)
	}

	cmd := exec.Command("bash", buildScript, image, pubPath, outPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Set SISTEMO_BUILD_TMPDIR so mktemp inside build-rootfs.sh uses disk, not RAM-backed /tmp
	cmd.Env = append(os.Environ(), "SISTEMO_BUILD_TMPDIR="+buildTmpBase)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}
	// When running via sudo, chown the built image to the invoking user
	if syscall.Geteuid() == 0 && os.Getenv("SUDO_UID") != "" {
		var uid, gid int
		if _, err := fmt.Sscanf(os.Getenv("SUDO_UID"), "%d", &uid); err == nil {
			if _, err := fmt.Sscanf(os.Getenv("SUDO_GID"), "%d", &gid); err == nil {
				_ = os.Chown(outPath, uid, gid)
			}
		}
	}
	base := strings.TrimSuffix(filepath.Base(outPath), ".rootfs.ext4")
	base = strings.TrimSuffix(base, ".ext4")
	fmt.Printf("Built %s\n", outPath)
	fmt.Printf("Deploy with: sistemo vm deploy %s\n", base)
	return nil
}

func runSshKey(logger *zap.Logger, dataDir string) error {
	dataDir = getDataDir(dataDir)
	sshDir := filepath.Join(dataDir, "ssh")
	keyPath := filepath.Join(sshDir, "sistemo_key")
	pubPath := keyPath + ".pub"
	if err := generateSSHKeyWithLock(sshDir, keyPath); err != nil {
		return fmt.Errorf("generate SSH key: %w", err)
	}
	pub, err := os.ReadFile(pubPath)
	if err != nil {
		if os.IsPermission(err) {
			fmt.Fprintln(os.Stderr, "Cannot read SSH key (created by daemon as root). Run the daemon once with sudo so it can fix ownership:")
			fmt.Fprintln(os.Stderr, "  sudo ./sistemo up   # then Ctrl+C; or: sudo chown -R $USER ~/.sistemo/ssh")
		}
		return fmt.Errorf("read public key: %w", err)
	}
	fmt.Print(string(pub))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "# Add the line above to your VM's /root/.ssh/authorized_keys so the terminal works.")
	fmt.Fprintln(os.Stderr, "# When running the daemon as root, use the same data dir: sudo ./sistemo up --data-dir=$HOME/.sistemo")
	return nil
}
