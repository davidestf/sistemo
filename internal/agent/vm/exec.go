package vm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// execOnVM runs a script on a VM via SSH through its network namespace.
func execOnVM(ctx context.Context, m *Manager, vmID, script string, timeoutSec int) (*ExecResult, error) {
	m.mu.RLock()
	info, ok := m.vms[vmID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("VM %s not found", vmID)
	}

	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	if timeoutSec > 120 {
		timeoutSec = 120
	}

	timeout := time.Duration(timeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sshArgs := []string{
		"-i", m.cfg.SSHKeyPath,
		"-T",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "PreferredAuthentications=publickey",
		"-o", "GSSAPIAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "PasswordAuthentication=no",
		"-o", "AddressFamily=inet",
		"-o", "ConnectTimeout=8",
		fmt.Sprintf("%s@%s", m.cfg.SSHUser, info.IP),
		script,
	}

	// With bridge architecture, VM has a unique IP reachable from the host directly
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	if ctx.Err() != nil && cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	return &ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(dstFile, srcFile)
	syncErr := dstFile.Sync()
	closeErr := dstFile.Close()

	if copyErr != nil {
		os.Remove(dst)
		return copyErr
	}
	if syncErr != nil {
		os.Remove(dst)
		return syncErr
	}
	return closeErr
}
