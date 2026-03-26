package main

import (
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
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
