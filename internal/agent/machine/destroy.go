package machine

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

func deleteMachine(ctx context.Context, m *Manager, machineID string, preserveStorage bool) (bool, error) {
	deleteStart := time.Now()
	log := agentmw.LoggerFromCtx(ctx, m.logger).With(zap.String("machine_id", machineID))
	machineDir := filepath.Join(m.cfg.VMBaseDir, machineID)

	log.Info("starting deletion")

	m.mu.RLock()
	machineInfo := m.machines[machineID]
	m.mu.RUnlock()

	// Clean up port expose nftables rules + delete DB rows
	machineIP, bridge := getMachineIPAndBridge(machineInfo, m.db, machineID, machineDir)
	cleanupPortRulesNftables(m, machineID, machineIP, bridge, true)

	t := time.Now()
	processKilled := findAndKillMachine(machineInfo, machineID, machineDir, m.logger)
	if !processKilled {
		log.Warn("could not find any firecracker process to kill")
	}
	log.Info("phase: kill_process", zap.Duration("elapsed", time.Since(t)), zap.Bool("killed", processKilled))

	// Clean up namespace
	t = time.Now()
	nsName := ""
	if machineInfo != nil {
		nsName = machineInfo.Namespace
	} else if data, err := os.ReadFile(filepath.Join(machineDir, "namespace")); err == nil {
		nsName = strings.TrimSpace(string(data))
	}
	if nsName != "" {
		ns := &network.VMNetwork{NamespaceName: nsName, Logger: m.logger}
		ns.Cleanup(m.cfg.HostInterface)
	}
	log.Info("phase: namespace_cleanup", zap.Duration("elapsed", time.Since(t)))

	// Read root_volume_path from vm_spec.json (if present, machine uses a managed volume)
	var rootVolumePath string
	if specData, err := os.ReadFile(filepath.Join(machineDir, "vm_spec.json")); err == nil {
		var spec struct {
			RootVolumePath string `json:"root_volume_path"`
		}
		if json.Unmarshal(specData, &spec) == nil {
			rootVolumePath = spec.RootVolumePath
		}
	}

	if rootVolumePath != "" {
		// Machine uses a managed root volume.
		// Only remove the volume file if it's still attached to this machine.
		// If user already detached it, the volume belongs to them — don't touch it.
		rootStillAttached := true // conservative default: assume attached unless proven otherwise
		if m.db != nil {
			var attached sql.NullString
			err := m.db.QueryRow("SELECT machine_id FROM volume WHERE path=?", rootVolumePath).Scan(&attached)
			if err == sql.ErrNoRows {
				rootStillAttached = false // volume record gone, safe to remove file
			} else if err != nil {
				log.Warn("failed to check volume attachment status, assuming attached (conservative)", zap.Error(err))
				// Keep rootStillAttached = true to prevent accidental deletion
			} else {
				rootStillAttached = attached.Valid && attached.String == machineID
			}
		}

		if rootStillAttached && !preserveStorage {
			if err := os.Remove(rootVolumePath); err != nil && !os.IsNotExist(err) {
				log.Warn("failed to remove root volume file", zap.String("path", rootVolumePath), zap.Error(err))
			} else {
				log.Info("removed root volume file", zap.String("path", rootVolumePath))
				// Delete the DB record too — file is gone, no point keeping a ghost entry
				db.SafeExec(m.db, "DELETE FROM volume WHERE path = ?", rootVolumePath)
			}
		} else if rootStillAttached && preserveStorage {
			// User wants to keep storage — detach the volume so it's reusable
			db.SafeExec(m.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE path=?",
				time.Now().UTC().Format(time.RFC3339), rootVolumePath)
		}
		// Always remove machineDir — it only has metadata files, not the rootfs
		if err := os.RemoveAll(machineDir); err != nil {
			log.Warn("failed to remove machine directory", zap.String("machine_id", machineID), zap.Error(err))
		}
	} else {
		// Old-style machine: rootfs is inside machineDir
		if !preserveStorage {
			// Clean up external rootfs (local copy or symlink) if any
			if data, err := os.ReadFile(filepath.Join(machineDir, "rootfs_path")); err == nil {
				rootfsPath := strings.TrimSpace(string(data))
				if rootfsPath != "" && rootfsPath != filepath.Join(machineDir, "rootfs.ext4") {
					os.Remove(rootfsPath)
				}
			}
		}
		if !preserveStorage {
			if err := os.RemoveAll(machineDir); err != nil {
				log.Warn("failed to remove machine directory", zap.String("machine_id", machineID), zap.Error(err))
			}
		}
	}

	// Detach all non-root data volumes so user can reuse them
	if m.db != nil {
		db.SafeExec(m.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE machine_id=? AND role != 'root'",
			time.Now().UTC().Format(time.RFC3339), machineID)
	}

	m.unregisterMachine(machineID)
	if err := network.ReleaseIP(m.db, machineID); err != nil {
		log.Warn("failed to release IP", zap.Error(err))
	}
	log.Info("delete complete", zap.Duration("total", time.Since(deleteStart)), zap.Bool("preserve_storage", preserveStorage))
	return processKilled, nil
}

// stopMachine stops a running machine (kills process, cleans namespace, unregisters) but keeps machineDir so the machine can be started again.
func stopMachine(ctx context.Context, m *Manager, machineID string) (bool, error) {
	log := agentmw.LoggerFromCtx(ctx, m.logger).With(zap.String("machine_id", machineID))
	machineDir := filepath.Join(m.cfg.VMBaseDir, machineID)

	m.mu.RLock()
	machineInfo := m.machines[machineID]
	m.mu.RUnlock()

	processKilled := findAndKillMachine(machineInfo, machineID, machineDir, m.logger)

	// Clean up port expose nftables rules (DB rows kept so they can be re-applied on start)
	machineIP, bridge := getMachineIPAndBridge(machineInfo, m.db, machineID, machineDir)
	cleanupPortRulesNftables(m, machineID, machineIP, bridge, false)

	nsName := ""
	if machineInfo != nil {
		nsName = machineInfo.Namespace
	} else if data, err := os.ReadFile(filepath.Join(machineDir, "namespace")); err == nil {
		nsName = strings.TrimSpace(string(data))
	}
	if nsName != "" {
		ns := &network.VMNetwork{NamespaceName: nsName, Logger: m.logger}
		ns.Cleanup(m.cfg.HostInterface)
	}

	m.unregisterMachine(machineID)
	log.Info("machine stopped (machineDir kept)", zap.Bool("process_killed", processKilled))
	return processKilled, nil
}

// getMachineIPAndBridge returns the machine's IP and bridge name from in-memory info or DB/disk fallback.
func getMachineIPAndBridge(machineInfo *MachineInfo, db *sql.DB, machineID, machineDir string) (string, string) {
	if machineInfo != nil {
		return machineInfo.IP, machineInfo.NetworkBridge
	}
	machineIP := network.GetAllocatedIP(db, machineID)
	var bridge string
	if specData, err := os.ReadFile(filepath.Join(machineDir, "vm_spec.json")); err == nil {
		var spec struct {
			NetworkBridge string `json:"network_bridge"`
		}
		if json.Unmarshal(specData, &spec) == nil {
			bridge = spec.NetworkBridge
		}
	}
	return machineIP, bridge
}

// findAndKillMachine tries 3 methods to find and kill the Firecracker VM process: in-memory PID, PID file, /proc scan.
func findAndKillMachine(machineInfo *MachineInfo, machineID, machineDir string, logger *zap.Logger) bool {
	killed := false

	// Method 1: In-memory machine info
	if machineInfo != nil && machineInfo.PID > 0 {
		if killProcessGroup(machineInfo.PID, machineID, logger) {
			return true
		}
	}

	// Method 2: PID file
	if data, err := os.ReadFile(filepath.Join(machineDir, "firecracker.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
			if killProcessGroup(pid, machineID, logger) {
				return true
			}
		}
	}

	// Method 3: Search by socket path
	socketPath := filepath.Join(machineDir, "firecracker.socket")
	if pids := findProcessesBySocket(socketPath); len(pids) > 0 {
		for _, pid := range pids {
			if killProcessGroup(pid, machineID, logger) {
				killed = true
			}
		}
	}
	return killed
}

// cleanupPortRulesNftables removes nftables DNAT rules for a machine's exposed ports.
// If deleteRows is true, also deletes port_rule DB rows (delete). If false, keeps them (stop).
func cleanupPortRulesNftables(m *Manager, machineID, machineIP, bridge string, deleteRows bool) {
	if m.db == nil || machineIP == "" {
		return
	}
	rows, err := m.db.Query("SELECT host_port, machine_port, protocol FROM port_rule WHERE machine_id = ?", machineID)
	if err != nil {
		return
	}
	var rules []network.PortRule
	for rows.Next() {
		var r network.PortRule
		if rows.Scan(&r.HostPort, &r.MachinePort, &r.Protocol) == nil {
			rules = append(rules, r)
		}
	}
	rows.Close()
	if len(rules) > 0 {
		net := network.NewVMNetwork(machineID, machineIP, m.logger, bridge)
		net.CleanupPortRules(m.cfg.HostInterface, rules)
	}
	if deleteRows {
		db.SafeExec(m.db, "DELETE FROM port_rule WHERE machine_id = ?", machineID)
	}
}

func killProcessGroup(pid int, machineID string, logger *zap.Logger) bool {
	if pid <= 0 || !processExists(pid) {
		return false
	}
	if !isFirecrackerProcess(pid, machineID) {
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

func isFirecrackerProcess(pid int, machineID string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	cmdline := strings.ReplaceAll(string(data), "\x00", " ")
	isFC := strings.Contains(cmdline, "firecracker")
	isIPNetns := strings.Contains(cmdline, "ip netns exec")
	return (isFC || isIPNetns) && strings.Contains(cmdline, machineID)
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
