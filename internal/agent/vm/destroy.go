package vm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentmw "github.com/davidestf/sistemo/internal/agent/api/middleware"
	network "github.com/davidestf/sistemo/internal/agent/network"
	"github.com/davidestf/sistemo/internal/db"
	"go.uber.org/zap"
)

func deleteVM(ctx context.Context, m *Manager, vmID string, preserveStorage bool) (bool, error) {
	deleteStart := time.Now()
	log := agentmw.LoggerFromCtx(ctx, m.logger).With(zap.String("vm_id", vmID))
	vmDir := filepath.Join(m.cfg.VMBaseDir, vmID)

	log.Info("starting deletion")

	m.mu.RLock()
	vmInfo := m.vms[vmID]
	m.mu.RUnlock()

	// Clean up port expose iptables rules + delete DB rows
	vmIP, bridge := getVMIPAndBridge(vmInfo, m.db, vmID, vmDir)
	cleanupPortRulesIPTables(m, vmID, vmIP, bridge, true)

	t := time.Now()
	processKilled := findAndKillVM(vmInfo, vmID, vmDir, m.logger)
	if !processKilled {
		log.Warn("could not find any firecracker process to kill")
	}
	log.Info("phase: kill_process", zap.Duration("elapsed", time.Since(t)), zap.Bool("killed", processKilled))

	// Clean up namespace
	t = time.Now()
	nsName := ""
	if vmInfo != nil {
		nsName = vmInfo.Namespace
	} else if data, err := os.ReadFile(filepath.Join(vmDir, "namespace")); err == nil {
		nsName = strings.TrimSpace(string(data))
	}
	if nsName != "" {
		ns := &network.VMNetwork{NamespaceName: nsName, Logger: m.logger}
		ns.Cleanup(m.cfg.HostInterface)
	}
	log.Info("phase: namespace_cleanup", zap.Duration("elapsed", time.Since(t)))

	if !preserveStorage {
		// Clean up external rootfs (local copy or symlink) if any
		if data, err := os.ReadFile(filepath.Join(vmDir, "rootfs_path")); err == nil {
			rootfsPath := strings.TrimSpace(string(data))
			if rootfsPath != "" && rootfsPath != filepath.Join(vmDir, "rootfs.ext4") {
				os.Remove(rootfsPath)
			}
		}
	}

	m.unregisterVM(vmID)
	if err := network.ReleaseIP(m.db, vmID); err != nil {
		log.Warn("failed to release IP", zap.Error(err))
	}
	if !preserveStorage {
		if err := os.RemoveAll(vmDir); err != nil {
			log.Warn("failed to remove VM directory", zap.String("vm_id", vmID), zap.Error(err))
		}
	}
	log.Info("delete complete", zap.Duration("total", time.Since(deleteStart)), zap.Bool("preserve_storage", preserveStorage))
	return processKilled, nil
}

// stopVM stops a running VM (kills process, cleans namespace, unregisters) but keeps vmDir so the VM can be started again.
func stopVM(ctx context.Context, m *Manager, vmID string) (bool, error) {
	log := agentmw.LoggerFromCtx(ctx, m.logger).With(zap.String("vm_id", vmID))
	vmDir := filepath.Join(m.cfg.VMBaseDir, vmID)

	m.mu.RLock()
	vmInfo := m.vms[vmID]
	m.mu.RUnlock()

	processKilled := findAndKillVM(vmInfo, vmID, vmDir, m.logger)

	// Clean up port expose iptables rules (DB rows kept so they can be re-applied on start)
	vmIP, bridge := getVMIPAndBridge(vmInfo, m.db, vmID, vmDir)
	cleanupPortRulesIPTables(m, vmID, vmIP, bridge, false)

	nsName := ""
	if vmInfo != nil {
		nsName = vmInfo.Namespace
	} else if data, err := os.ReadFile(filepath.Join(vmDir, "namespace")); err == nil {
		nsName = strings.TrimSpace(string(data))
	}
	if nsName != "" {
		ns := &network.VMNetwork{NamespaceName: nsName, Logger: m.logger}
		ns.Cleanup(m.cfg.HostInterface)
	}

	m.unregisterVM(vmID)
	log.Info("VM stopped (vmDir kept)", zap.Bool("process_killed", processKilled))
	return processKilled, nil
}

// getVMIPAndBridge returns the VM's IP and bridge name from in-memory info or DB/disk fallback.
func getVMIPAndBridge(vmInfo *VMInfo, db *sql.DB, vmID, vmDir string) (string, string) {
	if vmInfo != nil {
		return vmInfo.IP, vmInfo.NetworkBridge
	}
	vmIP := network.GetAllocatedIP(db, vmID)
	var bridge string
	if specData, err := os.ReadFile(filepath.Join(vmDir, "vm_spec.json")); err == nil {
		var spec struct {
			NetworkBridge string `json:"network_bridge"`
		}
		if json.Unmarshal(specData, &spec) == nil {
			bridge = spec.NetworkBridge
		}
	}
	return vmIP, bridge
}

// findAndKillVM tries 3 methods to find and kill the VM process: in-memory PID, PID file, /proc scan.
func findAndKillVM(vmInfo *VMInfo, vmID, vmDir string, logger *zap.Logger) bool {
	killed := false

	// Method 1: In-memory VM info
	if vmInfo != nil && vmInfo.PID > 0 {
		if killProcessGroup(vmInfo.PID, vmID, logger) {
			return true
		}
	}

	// Method 2: PID file
	if data, err := os.ReadFile(filepath.Join(vmDir, "firecracker.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
			if killProcessGroup(pid, vmID, logger) {
				return true
			}
		}
	}

	// Method 3: Search by socket path
	socketPath := filepath.Join(vmDir, "firecracker.socket")
	if pids := findProcessesBySocket(socketPath); len(pids) > 0 {
		for _, pid := range pids {
			if killProcessGroup(pid, vmID, logger) {
				killed = true
			}
		}
	}
	return killed
}

// cleanupPortRulesIPTables removes iptables DNAT rules for a VM's exposed ports.
// If deleteRows is true, also deletes port_rule DB rows (delete). If false, keeps them (stop).
func cleanupPortRulesIPTables(m *Manager, vmID, vmIP, bridge string, deleteRows bool) {
	if m.db == nil || vmIP == "" {
		return
	}
	rows, err := m.db.Query("SELECT host_port, vm_port, protocol FROM port_rule WHERE vm_id = ?", vmID)
	if err != nil {
		return
	}
	var rules []network.PortRule
	for rows.Next() {
		var r network.PortRule
		if rows.Scan(&r.HostPort, &r.VMPort, &r.Protocol) == nil {
			rules = append(rules, r)
		}
	}
	rows.Close()
	if len(rules) > 0 {
		net := network.NewVMNetwork(vmID, vmIP, m.logger, bridge)
		net.CleanupPortRules(m.cfg.HostInterface, rules)
	}
	if deleteRows {
		db.SafeExec(m.db, "DELETE FROM port_rule WHERE vm_id = ?", vmID)
	}
}

func killProcessGroup(pid int, vmID string, logger *zap.Logger) bool {
	if pid <= 0 || !processExists(pid) {
		return false
	}
	if !isFirecrackerProcess(pid, vmID) {
		return false
	}

	pgid := -pid
	logger.Info("sending SIGTERM to process group", zap.Int("pid", pid))
	syscall.Kill(pgid, syscall.SIGTERM)

	// Check immediately — most processes die within milliseconds
	if !processExists(pid) {
		return true
	}

	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if !processExists(pid) {
			return true
		}
	}

	logger.Info("process still alive after 5s, sending SIGKILL", zap.Int("pid", pid))
	syscall.Kill(pgid, syscall.SIGKILL)

	if !processExists(pid) {
		return true
	}

	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		if !processExists(pid) {
			return true
		}
	}
	return !processExists(pid)
}

func isFirecrackerProcess(pid int, vmID string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	cmdline := strings.ReplaceAll(string(data), "\x00", " ")
	isFC := strings.Contains(cmdline, "firecracker")
	isIPNetns := strings.Contains(cmdline, "ip netns exec")
	return (isFC || isIPNetns) && strings.Contains(cmdline, vmID)
}

func findProcessesBySocket(socketPath string) []int {
	var pids []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return pids
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), socketPath) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func processExists(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}
