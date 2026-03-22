package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/agent/api"
	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/network"
	"github.com/davidestf/sistemo/internal/agent/vm"
	"github.com/davidestf/sistemo/internal/daemon"
	"github.com/davidestf/sistemo/internal/db"
)

// ExitError is returned when a command wants to exit with a specific code (e.g. SSH forwarding).
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

// generateSSHKeyWithLock generates an SSH key at keyPath using flock to prevent races.
func generateSSHKeyWithLock(sshDir, keyPath string) error {
	os.MkdirAll(sshDir, 0700)
	lockPath := keyPath + ".lock"
	lockFile, err := os.Create(lockPath)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	defer os.Remove(lockPath)
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	if _, err := os.Stat(keyPath); err == nil {
		return nil // already exists
	}
	out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "", "-q").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh-keygen: %w (%s)", err, string(out))
	}
	return os.Chmod(keyPath, 0600)
}

func runInstall(logger *zap.Logger, dataDir string, upgrade bool) error {
	dataDir = getDataDir(dataDir)

	// Create directory structure
	for _, sub := range []string{"", "bin", "kernel", "ssh", "vms", "images", "volumes"} {
		dir := filepath.Join(dataDir, sub)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	// SSH dir needs restricted perms
	os.Chmod(filepath.Join(dataDir, "ssh"), 0700)

	fmt.Println("Sistemo data directory:", dataDir)

	// Generate SSH key if missing
	sshKeyPath := filepath.Join(dataDir, "ssh", "sistemo_key")
	if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
		fmt.Print("Generating SSH key... ")
		if err := generateSSHKeyWithLock(filepath.Join(dataDir, "ssh"), sshKeyPath); err != nil {
			fmt.Println("failed")
			return fmt.Errorf("generate SSH key: %w", err)
		}
		fmt.Println("done")
	} else {
		fmt.Println("SSH key: already exists")
	}

	// Detect architecture
	arch := runtime.GOARCH
	fcArch := "x86_64"
	if arch == "arm64" {
		fcArch = "aarch64"
	}

	// Download Firecracker from official GitHub releases
	fcVersion := "v1.14.2" // pinned stable version
	fcPath := filepath.Join(dataDir, "bin", "firecracker")
	if upgrade || !fileExists(fcPath) {
		fmt.Printf("Downloading Firecracker %s... ", fcVersion)
		fcURL := fmt.Sprintf("https://github.com/firecracker-microvm/firecracker/releases/download/%s/firecracker-%s-%s.tgz",
			fcVersion, fcVersion, fcArch)
		if err := downloadAndExtractFirecracker(fcURL, fcPath, fcVersion, fcArch); err != nil {
			fmt.Println("failed")
			fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
			fmt.Fprintln(os.Stderr, "  You can install Firecracker manually:")
			fmt.Fprintf(os.Stderr, "    Download from https://github.com/firecracker-microvm/firecracker/releases\n")
			fmt.Fprintf(os.Stderr, "    Copy the firecracker binary to %s\n", fcPath)
		} else {
			fmt.Println("done")
		}
	} else {
		fmt.Println("Firecracker: already installed")
	}

	// Download guest kernel from Sistemo CDN (we build and host this)
	// Override with SISTEMO_KERNEL_URL for custom hosting
	kernelPath := filepath.Join(dataDir, "kernel", "vmlinux")
	if upgrade || !fileExists(kernelPath) {
		fmt.Print("Downloading guest kernel... ")
		kernelURL := os.Getenv("SISTEMO_KERNEL_URL")
		if kernelURL == "" {
			kernelURL = fmt.Sprintf("https://get.sistemo.io/kernel/vmlinux-%s", fcArch)
		}
		if err := downloadBinary(kernelURL, kernelPath, 0644); err != nil {
			fmt.Println("failed")
			fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
			fmt.Fprintln(os.Stderr, "  You can install a kernel manually:")
			fmt.Fprintf(os.Stderr, "    Copy a Firecracker-compatible vmlinux to %s\n", kernelPath)
			fmt.Fprintln(os.Stderr, "  Or set SISTEMO_KERNEL_URL to a direct download URL")
		} else {
			fmt.Println("done")
		}
	} else {
		fmt.Println("Kernel: already installed")
	}

	// Check KVM
	fmt.Print("Checking KVM... ")
	if _, err := os.Stat("/dev/kvm"); err != nil {
		fmt.Println("NOT FOUND")
		fmt.Fprintln(os.Stderr, "  /dev/kvm not found. Enable virtualization in BIOS and load kvm module.")
	} else {
		// Check if user can access it
		f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
		if err != nil {
			fmt.Println("no access")
			fmt.Fprintln(os.Stderr, "  /dev/kvm exists but you can't access it. Run:")
			fmt.Fprintln(os.Stderr, "    sudo usermod -aG kvm $USER")
			fmt.Fprintln(os.Stderr, "    # Then log out and back in")
		} else {
			f.Close()
			fmt.Println("ok")
		}
	}

	fmt.Println()
	fmt.Println("Setup complete. Next steps:")
	fmt.Println("  1. Start the daemon:  sudo sistemo up")
	fmt.Println("  2. Deploy a VM:       sistemo vm deploy debian")
	fmt.Println("  3. Open terminal:     sistemo vm terminal debian")
	fmt.Println()
	fmt.Println("To update Firecracker/kernel later: sistemo install --upgrade")
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// downloadBinary downloads a file from url to dest. Automatically decompresses gzip.
func downloadBinary(url, dest string, mode os.FileMode) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s from %s", resp.Status, url)
	}

	var src io.Reader = resp.Body

	// Auto-detect gzip (by header or URL suffix)
	if resp.Header.Get("Content-Encoding") == "gzip" || strings.HasSuffix(url, ".gz") {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		defer zr.Close()
		src = zr
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, src); err != nil {
		os.Remove(dest)
		return err
	}
	return nil
}

// downloadAndExtractFirecracker downloads a .tgz from the official Firecracker GitHub
// releases, extracts it, and copies the firecracker binary to destBin.
func downloadAndExtractFirecracker(url, destBin, version, arch string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s from %s", resp.Status, url)
	}

	tmpDir, err := os.MkdirTemp("", "sistemo-fc-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Save tgz to temp
	tgzPath := filepath.Join(tmpDir, "firecracker.tgz")
	f, err := os.Create(tgzPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	f.Close()

	// Extract
	cmd := exec.Command("tar", "-xzf", tgzPath, "-C", tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar extract: %w (%s)", err, string(out))
	}

	// Find the firecracker binary: release-vX.Y.Z-arch/firecracker-vX.Y.Z-arch
	fcName := fmt.Sprintf("firecracker-%s-%s", version, arch)
	candidates := []string{
		filepath.Join(tmpDir, fmt.Sprintf("release-%s-%s", version, arch), fcName),
		filepath.Join(tmpDir, fcName),
	}

	var srcPath string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			srcPath = p
			break
		}
	}

	// Fallback: walk and find any executable starting with "firecracker"
	if srcPath == "" {
		filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.HasPrefix(info.Name(), "firecracker") &&
				!strings.HasSuffix(info.Name(), ".debug") &&
				info.Mode()&0111 != 0 {
				srcPath = path
				return filepath.SkipAll
			}
			return nil
		})
	}
	if srcPath == "" {
		return fmt.Errorf("firecracker binary not found in archive")
	}

	// Copy to destination
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destBin, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(destBin)
		return err
	}
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

	// Write embedded scripts to a temp dir
	tmpDir, err := os.MkdirTemp("", "sistemo-build-*")
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

func resolveFirecrackerBin(dataDir string) string {
	// 1. Explicit env var
	if p := os.Getenv("FIRECRACKER_BINARY_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// 2. ~/.sistemo/bin/firecracker (installed by 'sistemo install')
	if dataDir != "" {
		p := filepath.Join(dataDir, "bin", "firecracker")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// 3. ./firecracker-bin/ (for development / repo checkout)
	tryDir := func(base string) string {
		// Direct path
		candidate := filepath.Join(base, "firecracker-bin", "firecracker")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		// Search subdirectories (release-vX.Y.Z-arch/)
		binDir := filepath.Join(base, "firecracker-bin")
		entries, err := os.ReadDir(binDir)
		if err != nil {
			return ""
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sub, _ := os.ReadDir(filepath.Join(binDir, e.Name()))
			for _, s := range sub {
				if s.IsDir() || strings.HasSuffix(s.Name(), ".debug") {
					continue
				}
				if strings.HasPrefix(s.Name(), "firecracker") {
					p := filepath.Join(binDir, e.Name(), s.Name())
					if info, err := os.Stat(p); err == nil && info.Mode()&0111 != 0 {
						return p
					}
				}
			}
		}
		return ""
	}
	if cwd, err := os.Getwd(); err == nil {
		if p := tryDir(cwd); p != "" {
			return p
		}
	}

	// 4. System PATH
	if p, err := exec.LookPath("firecracker"); err == nil {
		return p
	}

	return ""
}

func detectDefaultRouteInterface() string {
	out, err := exec.Command("ip", "-4", "route", "show", "default").Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	fields := strings.Fields(line)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "dev" {
			return fields[i+1]
		}
	}
	return ""
}

func findKernelInDir(dir string) string {
	matches, err := filepath.Glob(filepath.Join(dir, "vmlinux*"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	abs, _ := filepath.Abs(matches[0])
	return abs
}

func runDaemon(logger *zap.Logger, dataDir string) error {
	dataDir = getDataDir(dataDir)
	if syscall.Geteuid() == 0 && os.Getenv("SUDO_USER") != "" {
		rootSistemo := filepath.Join("/root", ".sistemo")
		if dataDir == "" || dataDir == rootSistemo {
			if home, ok := os.LookupEnv("SUDO_HOME"); ok && home != "" {
				dataDir = filepath.Join(home, ".sistemo")
			} else {
				dataDir = filepath.Join("/home", os.Getenv("SUDO_USER"), ".sistemo")
			}
			logger.Info("using invoking user's data dir (for SSH key)", zap.String("data_dir", dataDir))
		}
	}
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".sistemo")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	// Resolve paths that will be set on the config struct after loading.
	vmBaseDir := filepath.Join(dataDir, "vms")
	imageCacheDir := filepath.Join(dataDir, "images")
	sshDir := filepath.Join(dataDir, "ssh")
	sshKeyPath := filepath.Join(sshDir, "sistemo_key")
	if err := generateSSHKeyWithLock(sshDir, sshKeyPath); err != nil {
		return fmt.Errorf("generate SSH key: %w", err)
	}
	// When running via sudo, chown the entire data directory to the invoking user
	// so that non-sudo CLI commands (vm deploy, vm list, image build) can access the DB and files.
	if syscall.Geteuid() == 0 && os.Getenv("SUDO_UID") != "" {
		var uid, gid int
		if _, err := fmt.Sscanf(os.Getenv("SUDO_UID"), "%d", &uid); err == nil {
			if _, err := fmt.Sscanf(os.Getenv("SUDO_GID"), "%d", &gid); err == nil {
				filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return nil
					}
					_ = os.Chown(path, uid, gid)
					return nil
				})
			}
		}
	}

	// Resolve kernel path: explicit env var > data dir > project dir
	kernelPath := os.Getenv("KERNEL_IMAGE_PATH")
	if kernelPath != "" {
		if _, err := os.Stat(kernelPath); err != nil {
			kernelPath = ""
		}
	}
	if kernelPath == "" {
		if k := findKernelInDir(filepath.Join(dataDir, "kernel")); k != "" {
			kernelPath = k
			logger.Info("using kernel from data dir", zap.String("path", k))
		}
	}
	if kernelPath == "" {
		if cwd, _ := os.Getwd(); cwd != "" {
			if k := findKernelInDir(filepath.Join(cwd, "kernel")); k != "" {
				kernelPath = k
				logger.Info("using kernel from project kernel/", zap.String("path", k))
			}
		}
	}

	// Resolve host interface
	hostInterface := os.Getenv("HOST_INTERFACE")
	if hostInterface == "" {
		if iface := detectDefaultRouteInterface(); iface != "" {
			hostInterface = iface
			logger.Info("auto-detected host interface for NAT", zap.String("interface", iface))
		}
	}

	// Resolve Firecracker binary path. Keep env var for child processes (Firecracker itself).
	fcBin := resolveFirecrackerBin(dataDir)
	if fcBin != "" {
		os.Setenv("FIRECRACKER_BINARY_PATH", fcBin)
		logger.Info("using Firecracker binary", zap.String("path", fcBin))
	}

	configFilePath := filepath.Join(dataDir, "config.yml")
	cfg, err := config.LoadWithFile(configFilePath)
	if err != nil {
		return fmt.Errorf("load agent config: %w", err)
	}
	if _, statErr := os.Stat(configFilePath); statErr == nil {
		logger.Info("loaded config file", zap.String("path", configFilePath))
	}

	// Override config fields directly with resolved paths (instead of going through env vars).
	cfg.VMBaseDir = vmBaseDir
	cfg.ImageCacheDir = imageCacheDir
	cfg.SSHKeyPath = sshKeyPath
	if kernelPath != "" {
		cfg.KernelImagePath = kernelPath
	}
	if hostInterface != "" {
		cfg.HostInterface = hostInterface
	}
	if fcBin != "" {
		cfg.FirecrackerBin = fcBin
	}

	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	database, err := db.New(dataDir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	// Clean up VMs stuck in maintenance or failed (crashed mid-operation).
	// 'error' VMs are NOT cleaned — they have valid rootfs and need user attention.
	// 'stopped' VMs are NOT cleaned — they are restartable.
	{
		rows, _ := database.Query(`SELECT id FROM vm WHERE status IN ('maintenance', 'failed')`)
		if rows != nil {
			var staleIDs []string
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					staleIDs = append(staleIDs, id)
				}
			}
			rows.Close()
			for _, id := range staleIDs {
				vmDir := filepath.Join(dataDir, "vms", id)
				os.RemoveAll(vmDir)
				db.SafeExec(database, `DELETE FROM ip_allocation WHERE vm_id = ?`, id)
				db.SafeExec(database, `DELETE FROM port_rule WHERE vm_id = ?`, id)
				db.SafeExec(database, `DELETE FROM vm WHERE id = ?`, id)
			}
			if len(staleIDs) > 0 {
				logger.Info("cleaned up failed VMs from previous run", zap.Int("count", len(staleIDs)))
			}
		}
	}

	// Clean up stale iptables port rules for VMs that no longer exist.
	// This handles the case where the daemon was killed with VMs still running.
	{
		rows, _ := database.Query(`SELECT pr.vm_id, pr.host_port, pr.vm_port, pr.protocol
			FROM port_rule pr LEFT JOIN vm v ON pr.vm_id = v.id
			WHERE v.id IS NULL OR v.status = 'deleted'`)
		if rows != nil {
			var staleVMIDs []string
			cleaned := 0
			for rows.Next() {
				var vmID string
				var hostPort, vmPort int
				var protocol string
				if rows.Scan(&vmID, &hostPort, &vmPort, &protocol) == nil {
					vmIP := network.GetAllocatedIP(database, vmID)
					if vmIP != "" {
						// Read bridge from vm_spec.json if it exists
						var bridge string
						specPath := filepath.Join(dataDir, "vms", vmID, "vm_spec.json")
						if specData, err := os.ReadFile(specPath); err == nil {
							var spec struct{ NetworkBridge string `json:"network_bridge"` }
							json.Unmarshal(specData, &spec)
							bridge = spec.NetworkBridge
						}
						n := network.NewVMNetwork(vmID, vmIP, logger, bridge)
						n.UnexposePort(cfg.HostInterface, hostPort, vmPort, protocol)
					}
					staleVMIDs = append(staleVMIDs, vmID)
					cleaned++
				}
			}
			rows.Close()
			// Delete stale port_rule rows (SQLite doesn't support aliases in DELETE)
			for _, id := range staleVMIDs {
				db.SafeExec(database, `DELETE FROM port_rule WHERE vm_id = ?`, id)
			}
			if cleaned > 0 {
				logger.Info("cleaned up stale port rules from previous run", zap.Int("count", cleaned))
			}
		}
	}

	// Clean up stale ip_allocation rows for deleted or missing VMs.
	// Keep IPs for running, stopped, AND error VMs (all are restartable).
	db.SafeExec(database, `DELETE FROM ip_allocation WHERE vm_id NOT IN (SELECT id FROM vm WHERE status IN ('running', 'stopped', 'error'))`)

	if syscall.Geteuid() != 0 {
		logger.Warn("daemon running as non-root — VM create will fail (mount/namespace need root). Stop and run: sudo ./sistemo up")
	}
	mgr := vm.NewManager(cfg, logger, database)
	router := api.NewRouter(cfg, mgr, logger, database)
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("daemon listening", zap.Int("port", cfg.Port), zap.String("data_dir", dataDir))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("server error: %w", err)
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
	case err := <-errCh:
		return err
	}
	logger.Info("shutting down gracefully (waiting up to 10s for connections to drain)")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
	}
	logger.Info("daemon stopped")
	return nil
}

func runList(logger *zap.Logger, database *sql.DB) error {
	rows, err := database.Query(`
		SELECT v.id, v.name, v.status, v.image, v.ip_address, COALESCE(n.name, 'default')
		FROM vm v LEFT JOIN network n ON v.network_id = n.id
		WHERE v.status != 'deleted'`)
	if err != nil {
		return fmt.Errorf("query vms: %w", err)
	}
	defer rows.Close()
	var rowsData []struct{ id, name, status, image, ip, network string }
	for rows.Next() {
		var id, name, status, image, ip, net sql.NullString
		if err := rows.Scan(&id, &name, &status, &image, &ip, &net); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		img := image.String
		if img != "" && len(img) > 40 {
			img = "..." + filepath.Base(img)
		}
		rowsData = append(rowsData, struct{ id, name, status, image, ip, network string }{
			id.String, name.String, status.String, img, ip.String, net.String,
		})
	}
	if len(rowsData) == 0 {
		fmt.Println("No VMs. Deploy one with: sistemo vm deploy <image>")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tIMAGE\tIP\tNETWORK")
	for _, r := range rowsData {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.id, r.name, r.status, r.image, r.ip, r.network)
	}
	tw.Flush()
	return nil
}

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
// Order: URL → local file → local cache → registry download → error.
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

func runDeploy(logger *zap.Logger, database *sql.DB, image string, vcpus, memoryMB, storageMB int, attachPaths []string, nameOverride string, exposePorts []string, networkName string) error {
	if !filepath.IsAbs(image) && !strings.HasPrefix(image, "http://") && !strings.HasPrefix(image, "https://") {
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
	req := &daemon.CreateVMRequest{
		Name:            name,
		Image:           image,
		VCPUs:           vcpus,
		MemoryMB:        memoryMB,
		StorageMB:       storageMB,
		AttachedStorage: attachPaths,
		InjectInitSSH:   true,
		NetworkBridge:   networkBridge,
		NetworkSubnet:   networkSubnet,
	}
	resp, err := daemon.CreateVM(baseURL, req)
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
		db.SafeExec(database, "UPDATE vm SET network_id = ? WHERE id = ?", networkID, resp.VMID)
	}

	fmt.Printf("Deployed %q as %s (%s)\n", image, name, resp.VMID)
	fmt.Printf("  IP: %s  Namespace: %s  Boot: %dms\n", resp.IPAddress, resp.Namespace, resp.BootTimeMS)
	if networkName != "" {
		fmt.Printf("  Network: %s\n", networkName)
	}
	fmt.Printf("  Terminal: %s/terminals/vm/%s\n", baseURL, resp.VMID)

	// Expose ports if requested
	for _, portSpec := range exposePorts {
		hostPort, vmPort, err := parsePortMapping(portSpec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: invalid port spec %q: %v\n", portSpec, err)
			continue
		}
		if err := daemon.ExposePort(baseURL, resp.VMID, hostPort, vmPort, "tcp"); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: expose port %d: %v\n", hostPort, err)
			continue
		}
		fmt.Printf("  Exposed host:%d → VM:%d (tcp)\n", hostPort, vmPort)
	}
	return nil
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

	// 2. Docker tag variants: "myapp:latest" → "myapp.rootfs.ext4"
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

func runImageList(dataDir string) {
	imagesDir := filepath.Join(dataDir, "images")

	// Local images
	entries, _ := os.ReadDir(imagesDir)
	var localImages []struct{ name, file string; sizeMB int64 }
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ext4") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".rootfs.ext4")
		name = strings.TrimSuffix(name, ".ext4")
		var sizeMB int64
		if info, err := e.Info(); err == nil {
			sizeMB = info.Size() / (1024 * 1024)
		}
		localImages = append(localImages, struct{ name, file string; sizeMB int64 }{name, e.Name(), sizeMB})
	}

	if len(localImages) > 0 {
		fmt.Println("Local images:")
		fmt.Printf("  %-20s  %-10s  %s\n", "NAME", "SIZE", "FILE")
		for _, img := range localImages {
			fmt.Printf("  %-20s  %-10s  %s\n", img.name, fmt.Sprintf("%d MB", img.sizeMB), img.file)
		}
		fmt.Println()
	} else {
		fmt.Println("No local images.")
		fmt.Println()
	}

	// Remote images
	fmt.Printf("Registry: %s\n", registryURL())
	localNames := make(map[string]bool)
	for _, img := range localImages {
		localNames[strings.ToLower(img.name)] = true
	}

	idx := fetchRegistryIndex()
	if idx != nil && len(idx.Images) > 0 {
		fmt.Println("Available:")
		for _, img := range idx.Images {
			status := fmt.Sprintf("sistemo image pull %s", img.Name)
			if localNames[strings.ToLower(img.Name)] {
				status = "(cached)"
			}
			if img.Description != "" {
				fmt.Printf("  %-15s  %-25s  %s\n", img.Name, img.Description, status)
			} else {
				fmt.Printf("  %-15s  %s\n", img.Name, status)
			}
		}
	} else {
		// Fallback to hardcoded list if registry is unreachable
		fmt.Println("Available:")
		for _, name := range knownRemoteImages {
			if localNames[name] {
				fmt.Printf("  %-15s  (cached)\n", name)
			} else {
				fmt.Printf("  %-15s  sistemo image pull %s\n", name, name)
			}
		}
	}

	fmt.Println()
	fmt.Println("Deploy:  sistemo vm deploy <name>")
	fmt.Println("Pull:    sistemo image pull <name>")
	fmt.Println("Build:   sudo sistemo image build <docker-image>")
}

func runImagePull(logger *zap.Logger, dataDir, name string) error {
	imagesDir := filepath.Join(dataDir, "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return fmt.Errorf("create images dir: %w", err)
	}

	dest := filepath.Join(imagesDir, name+".rootfs.ext4")
	suffix := archSuffix()

	fmt.Printf("Pulling %s from %s... ", name, registryURL())
	err := downloadImageToFile(registryURL()+name+suffix+".rootfs.ext4.gz", dest)
	if err != nil {
		err = downloadImageToFile(registryURL()+name+suffix+".rootfs.ext4", dest)
	}
	if err != nil {
		fmt.Printf("failed: %v\n", err)
		return fmt.Errorf("pull image %s: %w", name, err)
	}

	info, _ := os.Stat(dest)
	sizeMB := int64(0)
	if info != nil {
		sizeMB = info.Size() / (1024 * 1024)
	}
	fmt.Printf("done (%d MB)\n", sizeMB)
	fmt.Printf("Deploy with: sistemo vm deploy %s\n", name)
	return nil
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
	defer f.Close()
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
	return nil
}

// lookupVM resolves a VM name or ID to its UUID. Returns the VM ID or an error.
// excludeStatuses lists statuses to exclude from the lookup (e.g. "deleted", "error", "failed").
func lookupVM(database *sql.DB, nameOrID string, excludeStatuses ...string) (string, error) {
	if len(excludeStatuses) == 0 {
		excludeStatuses = []string{"deleted"}
	}
	placeholders := make([]string, len(excludeStatuses))
	args := []interface{}{nameOrID, nameOrID}
	for i, s := range excludeStatuses {
		placeholders[i] = "?"
		args = append(args, s)
	}
	query := fmt.Sprintf(
		"SELECT id FROM vm WHERE (id = ? OR name = ?) AND status NOT IN (%s) LIMIT 1",
		strings.Join(placeholders, ", "),
	)
	var vmID string
	err := database.QueryRow(query, args...).Scan(&vmID)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("VM not found: %s", nameOrID)
	}
	if err != nil {
		return "", fmt.Errorf("lookup vm: %w", err)
	}
	return vmID, nil
}

func runDelete(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	_, err = daemon.DeleteVM(baseURL, vmID)
	if err != nil {
		daemonUnreachable := strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "connection reset") ||
			strings.Contains(err.Error(), "dial") ||
			strings.Contains(err.Error(), "unreachable") ||
			strings.Contains(err.Error(), "timeout")
		if daemonUnreachable {
			// Daemon is down — update DB directly as fallback
			fmt.Fprintln(os.Stderr, "Warning: daemon unreachable; marking VM as deleted in database.")
			now := time.Now().UTC().Format(time.RFC3339)
			db.SafeExec(database, "UPDATE vm SET status = 'deleted', last_state_change = ? WHERE id = ?", now, vmID)
		} else {
			return fmt.Errorf("delete VM: %w", err)
		}
	}
	fmt.Printf("Deleted %s\n", vmID)
	return nil
}

func runStop(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return fmt.Errorf("VM not found: %s", nameOrID)
	}
	stopped, err := daemon.StopVM(baseURL, vmID)
	if err != nil {
		return fmt.Errorf("stop VM: %w", err)
	}
	if !stopped {
		fmt.Printf("VM %s not found on daemon.\n", vmID)
		return nil
	}
	fmt.Printf("Stopped %s\n", vmID)
	return nil
}

func runStart(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	resp, err := daemon.StartVM(baseURL, vmID)
	if err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	fmt.Printf("Started %s\n", vmID)
	fmt.Printf("  IP: %s  Namespace: %s  Boot: %dms\n", resp.IPAddress, resp.Namespace, resp.BootTimeMS)
	fmt.Printf("  Terminal: %s/terminals/vm/%s\n", baseURL, vmID)
	return nil
}

func runRestart(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}

	// Stop (ignore "not found" — VM might already be stopped)
	stopped, err := daemon.StopVM(baseURL, vmID)
	if err != nil {
		return fmt.Errorf("stop VM: %w", err)
	}
	if stopped {
		fmt.Printf("Stopped %s\n", vmID)
	}

	// Start
	resp, err := daemon.StartVM(baseURL, vmID)
	if err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	fmt.Printf("Started %s\n", vmID)
	fmt.Printf("  IP: %s  Namespace: %s  Boot: %dms\n", resp.IPAddress, resp.Namespace, resp.BootTimeMS)
	return nil
}

func runTerminal(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	vmID, err := lookupVM(database, nameOrID, "deleted", "error", "failed", "stopped", "maintenance")
	if err != nil {
		return fmt.Errorf("VM not found or not running: %s", nameOrID)
	}
	baseURL := daemon.URL()
	url := fmt.Sprintf("%s/terminals/vm/%s", baseURL, vmID)
	fmt.Printf("Open in your browser: %s\n", url)
	openBrowser(url)
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	cmd.Stderr = nil
	cmd.Stdout = nil
	_ = cmd.Start()
}

func runStatus(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	row := database.QueryRow(
		"SELECT id, name, status, image, ip_address, namespace, created_at, network_id FROM vm WHERE id = ?",
		vmID,
	)
	var id, name, status, image, ip, ns, created, networkID sql.NullString
	if err := row.Scan(&id, &name, &status, &image, &ip, &ns, &created, &networkID); err != nil {
		return fmt.Errorf("lookup vm details: %w", err)
	}
	fmt.Printf("ID:        %s\n", id.String)
	fmt.Printf("Name:      %s\n", name.String)
	fmt.Printf("Status:    %s\n", status.String)
	fmt.Printf("Image:     %s\n", image.String)
	fmt.Printf("IP:        %s\n", ip.String)
	fmt.Printf("Namespace: %s\n", ns.String)
	fmt.Printf("Created:   %s\n", created.String)
	if networkID.Valid && networkID.String != "" {
		var netName string
		if database.QueryRow("SELECT name FROM network WHERE id = ?", networkID.String).Scan(&netName) == nil {
			fmt.Printf("Network:   %s\n", netName)
		}
	} else {
		fmt.Printf("Network:   default\n")
	}

	// Show exposed ports
	portRows, err := database.Query("SELECT host_port, vm_port, protocol FROM port_rule WHERE vm_id = ?", id.String)
	if err == nil {
		defer portRows.Close()
		first := true
		for portRows.Next() {
			var hp, vp int
			var proto string
			if portRows.Scan(&hp, &vp, &proto) == nil {
				if first {
					fmt.Printf("Ports:\n")
					first = false
				}
				fmt.Printf("  host:%d → VM:%d (%s)\n", hp, vp, proto)
			}
		}
	}
	return nil
}

func runLogs(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	baseURL := daemon.URL()
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(baseURL + "/vms/" + vmID + "/logs")
	if err != nil {
		return fmt.Errorf("fetch logs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("logs request failed: HTTP %d", resp.StatusCode)
	}
	io.Copy(os.Stdout, resp.Body)
	return nil
}

func runSSH(logger *zap.Logger, database *sql.DB, dataDir, nameOrID string) error {
	vmID, err := lookupVM(database, nameOrID, "deleted", "error", "failed", "stopped", "maintenance")
	if err != nil {
		return fmt.Errorf("VM not found or not running: %s", nameOrID)
	}
	var ip sql.NullString
	if err := database.QueryRow("SELECT ip_address FROM vm WHERE id = ?", vmID).Scan(&ip); err != nil {
		return fmt.Errorf("lookup vm IP: %w", err)
	}
	if !ip.Valid || ip.String == "" {
		return fmt.Errorf("VM has no IP address: %s", vmID)
	}

	sshKeyPath := filepath.Join(dataDir, "ssh", "sistemo_key")

	sshArgs := []string{
		"-i", sshKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "SendEnv=-*",
		"-o", "SetEnv=LANG=C",
		fmt.Sprintf("root@%s", ip.String),
	}

	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &ExitError{Code: exitErr.ExitCode()}
		}
		return fmt.Errorf("ssh: %w", err)
	}
	return nil
}

func runExec(logger *zap.Logger, database *sql.DB, nameOrID, command string) error {
	vmID, err := lookupVM(database, nameOrID, "deleted", "error", "failed", "stopped", "maintenance")
	if err != nil {
		return fmt.Errorf("VM not found or not running: %s", nameOrID)
	}
	baseURL := daemon.URL()
	result, err := daemon.Exec(baseURL, vmID, command, 120)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	if result.ExitCode != 0 {
		return &ExitError{Code: result.ExitCode}
	}
	return nil
}

type volumeEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	SizeMB int    `json:"size_mb"`
}

func volumesDir(dataDir string) string   { return filepath.Join(dataDir, "volumes") }
func volumeIndexPath(dataDir string) string { return filepath.Join(volumesDir(dataDir), "index.json") }

func readVolumeIndex(dataDir string) ([]volumeEntry, error) {
	path := volumeIndexPath(dataDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []volumeEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func writeVolumeIndex(dataDir string, list []volumeEntry) error {
	dir := volumesDir(dataDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(volumeIndexPath(dataDir), data, 0644)
}

func resolveVolumePath(dataDir, idOrName string) string {
	list, err := readVolumeIndex(dataDir)
	if err != nil || list == nil {
		return ""
	}
	for _, v := range list {
		if v.ID == idOrName || v.Name == idOrName {
			if _, err := os.Stat(v.Path); err == nil {
				return v.Path
			}
			return ""
		}
	}
	return ""
}

func runStorageCreate(logger *zap.Logger, dataDir string, sizeMB int, name string) error {
	list, err := readVolumeIndex(dataDir)
	if err != nil {
		return fmt.Errorf("read volume index: %w", err)
	}
	if list == nil {
		list = []volumeEntry{}
	}
	id := uuid.New().String()
	dir := volumesDir(dataDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create volumes dir: %w", err)
	}
	path := filepath.Join(dir, id+".ext4")
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create volume file: %w", err)
	}
	if err := f.Truncate(int64(sizeMB) * 1024 * 1024); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("truncate volume: %w", err)
	}
	f.Close()
	cmd := exec.Command("mkfs.ext4", "-q", "-F", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(path)
		return fmt.Errorf("mkfs.ext4: %w (%s)", err, string(out))
	}
	if name == "" {
		name = id[:8]
	}
	list = append(list, volumeEntry{ID: id, Name: name, Path: path, SizeMB: sizeMB})
	if err := writeVolumeIndex(dataDir, list); err != nil {
		os.Remove(path)
		return fmt.Errorf("write volume index: %w", err)
	}
	fmt.Printf("Created volume %s (%s) %d MB at %s\n", id, name, sizeMB, path)
	fmt.Println("To attach: deploy a VM with --attach=ID (e.g. sistemo vm deploy --attach=" + id + " <image>)")
	return nil
}

func runStorageList(logger *zap.Logger, dataDir string) error {
	list, err := readVolumeIndex(dataDir)
	if err != nil {
		return fmt.Errorf("read volume index: %w", err)
	}
	if len(list) == 0 {
		fmt.Println("No volumes. Create one with: sistemo volume create <size_mb> [--name=myvol]")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VOLUME ID\tNAME\tSIZE (MB)\tPATH")
	for _, v := range list {
		path := v.Path
		if _, err := os.Stat(v.Path); err != nil {
			path = v.Path + " (missing)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", v.ID, v.Name, v.SizeMB, path)
	}
	tw.Flush()
	return nil
}

func runStorageDelete(logger *zap.Logger, dataDir, idOrName string) error {
	list, err := readVolumeIndex(dataDir)
	if err != nil {
		return fmt.Errorf("read volume index: %w", err)
	}
	var newList []volumeEntry
	var path string
	for _, v := range list {
		if v.ID == idOrName || v.Name == idOrName {
			path = v.Path
			continue
		}
		newList = append(newList, v)
	}
	if path == "" {
		return fmt.Errorf("volume not found: %s", idOrName)
	}
	if err := writeVolumeIndex(dataDir, newList); err != nil {
		return fmt.Errorf("write volume index: %w", err)
	}
	os.Remove(path)
	fmt.Printf("Deleted volume %s\n", idOrName)
	return nil
}
