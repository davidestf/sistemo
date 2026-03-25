package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/daemon"
)

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
