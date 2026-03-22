package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check sistemo installation health",
		Long:  "Run diagnostic checks on your sistemo installation.\nReports pass/fail for each component with actionable fix suggestions.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir := getDataDirFromCmd(cmd)
			runDoctor(dataDir)
			return nil
		},
	}
}

type checkResult struct {
	name    string
	ok      bool
	message string
}

func runDoctor(dataDir string) {
	var results []checkResult

	results = append(results, checkKVM())
	results = append(results, checkFirecracker(dataDir))
	results = append(results, checkKernel(dataDir))
	results = append(results, checkSSHKey(dataDir))
	results = append(results, checkBridge())
	results = append(results, checkMasquerade())
	results = append(results, checkDaemon())
	results = append(results, checkDiskSpace(dataDir))
	results = append(results, checkDatabase(dataDir))
	results = append(results, checkAPIKey())

	passed := 0
	failed := 0
	for _, r := range results {
		if r.ok {
			fmt.Printf("  ok    %s\n", r.message)
			passed++
		} else {
			fmt.Printf("  FAIL  %s\n", r.message)
			failed++
		}
	}

	fmt.Println()
	if failed == 0 {
		fmt.Printf("%d/%d checks passed\n", passed, passed)
	} else {
		fmt.Printf("%d/%d checks passed, %d failed\n", passed, len(results), failed)
		os.Exit(1)
	}
}

func checkKVM() checkResult {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return checkResult{name: "kvm", ok: false,
			message: "KVM not available (/dev/kvm not found). Enable virtualization in BIOS and load kvm module."}
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return checkResult{name: "kvm", ok: false,
			message: "KVM exists but not writable. Run: sudo usermod -aG kvm $USER (then re-login)"}
	}
	f.Close()
	return checkResult{name: "kvm", ok: true, message: "KVM available (/dev/kvm exists and writable)"}
}

func checkFirecracker(dataDir string) checkResult {
	fcPath := filepath.Join(dataDir, "bin", "firecracker")
	if _, err := os.Stat(fcPath); err != nil {
		// Check PATH as fallback
		if p, err := exec.LookPath("firecracker"); err == nil {
			fcPath = p
		} else {
			return checkResult{name: "firecracker", ok: false,
				message: "Firecracker binary not found. Run: sistemo install"}
		}
	}
	out, err := exec.Command(fcPath, "--version").CombinedOutput()
	if err != nil {
		return checkResult{name: "firecracker", ok: false,
			message: fmt.Sprintf("Firecracker binary exists but --version failed: %v", err)}
	}
	version := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	return checkResult{name: "firecracker", ok: true,
		message: fmt.Sprintf("Firecracker binary found (%s) at %s", version, fcPath)}
}

func checkKernel(dataDir string) checkResult {
	kernelPath := filepath.Join(dataDir, "kernel", "vmlinux")
	if _, err := os.Stat(kernelPath); err != nil {
		// Try glob for vmlinux*
		matches, _ := filepath.Glob(filepath.Join(dataDir, "kernel", "vmlinux*"))
		if len(matches) > 0 {
			return checkResult{name: "kernel", ok: true,
				message: fmt.Sprintf("Kernel image found at %s", matches[0])}
		}
		return checkResult{name: "kernel", ok: false,
			message: "Kernel image not found. Run: sistemo install"}
	}
	return checkResult{name: "kernel", ok: true,
		message: fmt.Sprintf("Kernel image found at %s", kernelPath)}
}

func checkSSHKey(dataDir string) checkResult {
	keyPath := filepath.Join(dataDir, "ssh", "sistemo_key")
	if _, err := os.Stat(keyPath); err != nil {
		return checkResult{name: "ssh_key", ok: false,
			message: "SSH key pair not found. Run: sistemo install"}
	}
	pubPath := keyPath + ".pub"
	if _, err := os.Stat(pubPath); err != nil {
		return checkResult{name: "ssh_key", ok: false,
			message: "SSH public key missing (private key exists). Re-generate: rm " + keyPath + " && sistemo install"}
	}
	return checkResult{name: "ssh_key", ok: true,
		message: fmt.Sprintf("SSH key pair exists at %s", keyPath)}
}

func checkBridge() checkResult {
	out, err := exec.Command("ip", "addr", "show", "sistemo0").CombinedOutput()
	if err != nil {
		return checkResult{name: "bridge", ok: false,
			message: "Bridge sistemo0 not found. Start the daemon: sudo sistemo up"}
	}
	// Extract IP from output
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "inet ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return checkResult{name: "bridge", ok: true,
					message: fmt.Sprintf("Bridge sistemo0 is up (%s)", fields[1])}
			}
		}
	}
	return checkResult{name: "bridge", ok: true, message: "Bridge sistemo0 exists (no IP assigned yet)"}
}

func checkMasquerade() checkResult {
	out, err := exec.Command("iptables", "-t", "nat", "-L", "POSTROUTING", "-n").CombinedOutput()
	if err != nil {
		if syscall.Geteuid() != 0 {
			return checkResult{name: "masquerade", ok: false,
				message: "Cannot check iptables (not root). Run doctor as root for full check."}
		}
		return checkResult{name: "masquerade", ok: false,
			message: "Cannot query iptables rules"}
	}
	if strings.Contains(string(out), "MASQUERADE") {
		return checkResult{name: "masquerade", ok: true, message: "iptables MASQUERADE rule active"}
	}
	return checkResult{name: "masquerade", ok: false,
		message: "iptables MASQUERADE rule missing. Start the daemon: sudo sistemo up"}
}

func checkDaemon() checkResult {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:7777/health")
	if err != nil {
		return checkResult{name: "daemon", ok: false,
			message: "Daemon not reachable at http://127.0.0.1:7777. Start it: sudo sistemo up"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return checkResult{name: "daemon", ok: false,
			message: fmt.Sprintf("Daemon returned HTTP %d", resp.StatusCode)}
	}
	// Count running VMs
	vmResp, err := client.Get("http://127.0.0.1:7777/vms")
	vmCount := "?"
	if err == nil {
		defer vmResp.Body.Close()
		var vms []json.RawMessage
		if json.NewDecoder(io.LimitReader(vmResp.Body, 1<<20)).Decode(&vms) == nil {
			vmCount = fmt.Sprintf("%d", len(vms))
		}
	}
	return checkResult{name: "daemon", ok: true,
		message: fmt.Sprintf("Daemon reachable at http://127.0.0.1:7777 (%s VMs running)", vmCount)}
}

func checkDiskSpace(dataDir string) checkResult {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &stat); err != nil {
		return checkResult{name: "disk", ok: false,
			message: fmt.Sprintf("Cannot check disk space at %s: %v", dataDir, err)}
	}
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	freeMB := freeBytes / (1024 * 1024)
	freeGB := float64(freeMB) / 1024.0

	if freeMB < 512 {
		return checkResult{name: "disk", ok: false,
			message: fmt.Sprintf("Low disk space: %d MB free (minimum 512 MB)", freeMB)}
	}
	return checkResult{name: "disk", ok: true,
		message: fmt.Sprintf("Disk space: %.1f GB free", freeGB)}
}

func checkDatabase(dataDir string) checkResult {
	dbPath := filepath.Join(dataDir, "sistemo.db")
	if _, err := os.Stat(dbPath); err != nil {
		return checkResult{name: "database", ok: false,
			message: "SQLite database not found. Start the daemon to create it: sudo sistemo up"}
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return checkResult{name: "database", ok: false,
			message: fmt.Sprintf("Cannot open database: %v", err)}
	}
	defer db.Close()

	var vmCount, portCount int
	db.QueryRow("SELECT COUNT(*) FROM vm WHERE status NOT IN ('deleted')").Scan(&vmCount)
	db.QueryRow("SELECT COUNT(*) FROM port_rule").Scan(&portCount)

	// Check WAL mode
	var journalMode string
	db.QueryRow("PRAGMA journal_mode").Scan(&journalMode)

	return checkResult{name: "database", ok: true,
		message: fmt.Sprintf("SQLite database healthy (%s mode, %d VMs, %d port rules)", journalMode, vmCount, portCount)}
}

func checkAPIKey() checkResult {
	if os.Getenv("HOST_API_KEY") != "" {
		return checkResult{name: "api_key", ok: true, message: "HOST_API_KEY is set"}
	}
	return checkResult{name: "api_key", ok: false,
		message: "HOST_API_KEY not set — API is unauthenticated. Set it for network-exposed deployments."}
}
