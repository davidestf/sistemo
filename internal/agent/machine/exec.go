package machine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// execOnMachine runs a script on a machine via SSH through its network namespace.
func execOnMachine(ctx context.Context, m *Manager, machineID, script string, timeoutSec int) (*ExecResult, error) {
	m.mu.RLock()
	info, ok := m.machines[machineID]
	var machineIP string
	if ok {
		machineIP = info.IP
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("machine %s not found", machineID)
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
		fmt.Sprintf("%s@%s", m.cfg.SSHUser, machineIP),
		script,
	}

	// With bridge architecture, machine has a unique IP reachable from the host directly
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
	// Use cp --sparse=always to preserve sparse files. Build images are typically
	// 5GB+ apparent size but only ~300MB actual data — without this, the copy
	// materializes all zero regions and can exhaust RAM/disk on laptops.
	out, err := exec.Command("cp", "--sparse=always", "--reflink=auto", src, dst).CombinedOutput()
	if err != nil {
		os.Remove(dst)
		return fmt.Errorf("cp: %w (%s)", err, bytes.TrimSpace(out))
	}
	return nil
}
