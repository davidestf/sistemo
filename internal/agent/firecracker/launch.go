// Package firecracker handles launching Firecracker processes.
package firecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	agent "github.com/davidestf/sistemo/internal/agent"
	"go.uber.org/zap"
)

// LaunchInNamespace starts a Firecracker VM inside a network namespace with optional cgroup limits.
func LaunchInNamespace(vmBaseDir string, vmID string, firecrackerBin string, cfg VMConfig, namespaceName string, vcpus, memoryMB int, logger *zap.Logger) (int, error) {
	vmDir := filepath.Join(vmBaseDir, vmID)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return 0, fmt.Errorf("create VM directory: %w", err)
	}

	configPath := filepath.Join(vmDir, "config.json")
	f, err := os.Create(configPath)
	if err != nil {
		return 0, err
	}
	if err := json.NewEncoder(f).Encode(cfg); err != nil {
		f.Close()
		return 0, err
	}
	f.Close()

	apiSock := filepath.Join(vmDir, "firecracker.socket")
	logPath := filepath.Join(vmDir, "firecracker.log")
	os.Remove(apiSock)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 0, fmt.Errorf("failed to create log file: %v", err)
	}

	// Firecracker v1.14+ enables seccomp by default (built-in BPF syscall filter).
	// No --seccomp-level flag needed — it's on unless --no-seccomp is passed.
	fcArgs := []string{firecrackerBin, "--api-sock", apiSock, "--config-file", configPath}

	var innerArgs []string
	if namespaceName != "" {
		innerArgs = append([]string{"ip", "netns", "exec", namespaceName}, fcArgs...)
	} else {
		innerArgs = fcArgs
	}

	useCgroup := vcpus > 0 && memoryMB > 0

	var cmd *exec.Cmd
	if useCgroup {
		unitName := fmt.Sprintf("fc-%s", vmID)
		// Clear any stale systemd scope from a previous run (stop/start cycle).
		// Without this, systemd-run fails with "Unit was already loaded".
		exec.Command("systemctl", "reset-failed", unitName+".scope").Run()
		exec.Command("systemctl", "stop", unitName+".scope").Run()

		systemdArgs := []string{
			"--scope",
			"--unit=" + unitName,
			"-p", fmt.Sprintf("CPUQuota=%d%%", vcpus*100),
			"-p", fmt.Sprintf("MemoryMax=%dM", memoryMB+64),
			"-p", fmt.Sprintf("MemoryHigh=%dM", memoryMB),
			"--",
		}
		cmd = exec.Command("systemd-run", append(systemdArgs, innerArgs...)...)
		logger.Info("launching Firecracker with cgroup scope",
			zap.String("unit", unitName),
			zap.Int("vcpus", vcpus),
			zap.Int("memory_mb", memoryMB))
	} else {
		cmd = exec.Command(innerArgs[0], innerArgs[1:]...)
	}

	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return 0, fmt.Errorf("failed to start firecracker: %v", err)
	}

	pid := cmd.Process.Pid
	logger.Info("Firecracker process started", zap.Int("pid", pid))

	go func() {
		cmd.Wait()
		logFile.Close()
	}()

	// Poll until the API socket appears or the process dies.
	deadline := time.Now().Add(agent.DefaultFCReadinessTimeout)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return 0, fmt.Errorf("firecracker process exited immediately, check %s", logPath)
		}
		if _, err := os.Stat(apiSock); err == nil {
			logger.Debug("Firecracker API socket ready", zap.String("socket", apiSock))
			break
		}
		if time.Now().After(deadline) {
			// Process is alive but socket never appeared — tolerate this
			// for FC versions that may not create the socket file.
			logger.Warn("Firecracker API socket not found within timeout, continuing anyway",
				zap.String("socket", apiSock),
				zap.Duration("timeout", agent.DefaultFCReadinessTimeout))
			break
		}
		time.Sleep(agent.FCReadinessPollInterval)
	}

	if err := os.WriteFile(filepath.Join(vmDir, "firecracker.pid"), []byte(fmt.Sprintf("%d", pid)), 0644); err != nil {
		logger.Warn("failed to write pid file", zap.Error(err))
	}
	if namespaceName != "" {
		if err := os.WriteFile(filepath.Join(vmDir, "namespace"), []byte(namespaceName), 0644); err != nil {
			logger.Warn("failed to write namespace file", zap.Error(err))
		}
	}

	return pid, nil
}
