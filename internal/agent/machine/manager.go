package machine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	agent "github.com/davidestf/sistemo/internal/agent"
	agentmw "github.com/davidestf/sistemo/internal/agent/api/middleware"
	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/network"
	"github.com/davidestf/sistemo/internal/db"
	"go.uber.org/zap"
)

// Manager orchestrates machine lifecycle operations.
type Manager struct {
	cfg          *config.Config
	logger       *zap.Logger
	db           *sql.DB
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.RWMutex
	machines     map[string]*MachineInfo
	machineLocks sync.Map // map[string]*sync.Mutex — per-machine operation lock
}

func NewManager(ctx context.Context, cfg *config.Config, logger *zap.Logger, db *sql.DB) *Manager {
	ctx, cancel := context.WithCancel(ctx)
	m := &Manager{cfg: cfg, logger: logger, db: db, ctx: ctx, cancel: cancel, machines: make(map[string]*MachineInfo)}
	if err := os.MkdirAll(cfg.VMBaseDir, 0755); err != nil {
		logger.Fatal("failed to create machine base directory", zap.String("path", cfg.VMBaseDir), zap.Error(err))
	}

	// Parse bridge subnet from config and create the bridge
	if err := network.ParseBridgeSubnet(cfg.BridgeSubnet); err != nil {
		logger.Fatal("invalid bridge_subnet", zap.Error(err))
	}
	if err := network.EnsureBridge(cfg.HostInterface, logger); err != nil {
		logger.Fatal("failed to create bridge — machine networking will not work", zap.Error(err))
	}
	m.recreateNamedBridges()
	m.cleanupStaleBridges()
	m.rehydrateFromDisk()
	m.cleanupDeadRunningMachines()
	m.cleanupStaleMaintenanceMachines()
	m.restorePortRules()
	preserve := make(map[string]struct{})
	m.mu.RLock()
	for _, info := range m.machines {
		if info.Namespace != "" {
			preserve[info.Namespace] = struct{}{}
		}
	}
	m.mu.RUnlock()
	network.CleanupAllNamespaces(cfg.HostInterface, logger, preserve)
	go m.runReconciler()
	return m
}

// Shutdown cancels the manager's context, stopping the reconciler and background work.
func (m *Manager) Shutdown() { m.cancel() }

// runReconciler periodically checks for dead machine processes and cleans them up.
func (m *Manager) runReconciler() {
	interval := agent.DefaultReconcilerInterval
	if m.cfg.ReconcilerIntervalSec > 0 {
		interval = time.Duration(m.cfg.ReconcilerIntervalSec) * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			m.logger.Info("reconciler stopped")
			return
		case <-ticker.C:
			m.reconcile()
		}
	}
}

func (m *Manager) reconcile() {
	m.mu.RLock()
	snapshot := make(map[string]*MachineInfo, len(m.machines))
	for k, v := range m.machines {
		snapshot[k] = v
	}
	m.mu.RUnlock()

	for machineID, info := range snapshot {
		if info.PID <= 0 {
			continue
		}
		if syscall.Kill(info.PID, 0) != nil {
			// Acquire per-machine lock to prevent race with concurrent Start/Create
			lock := m.getMachineLock(machineID)
			lock.Lock()

			// Re-check: another operation may have re-registered this machine with a new PID
			m.mu.RLock()
			current, exists := m.machines[machineID]
			m.mu.RUnlock()
			if exists && current.PID != info.PID {
				lock.Unlock()
				continue // machine was restarted, skip cleanup
			}
			// Also check if a delete is already in progress (or completed)
			if m.db != nil {
				var dbStatus string
				if m.db.QueryRow("SELECT status FROM machine WHERE id=?", machineID).Scan(&dbStatus) == nil {
					if dbStatus == "deleted" || dbStatus == "maintenance" {
						lock.Unlock()
						m.unregisterMachine(machineID)
						continue // Delete in progress or done, skip reconciler cleanup
					}
				}
			}

			m.logger.Warn("reconciler: machine process dead, cleaning up",
				zap.String("machine_id", machineID), zap.Int("pid", info.PID))
			// Clean nftables rules (they'll be re-applied on start)
			// but keep port_rule DB rows and IP allocation for restart.
			if m.db != nil && info.IP != "" {
				rows, err := m.db.Query("SELECT host_port, machine_port, protocol FROM port_rule WHERE machine_id = ?", machineID)
				if err == nil {
					var rules []network.PortRule
					for rows.Next() {
						var r network.PortRule
						if err := rows.Scan(&r.HostPort, &r.MachinePort, &r.Protocol); err != nil {
							m.logger.Warn("reconciler: failed to scan port rule", zap.Error(err))
							continue
						}
						rules = append(rules, r)
					}
					rows.Close()
					if len(rules) > 0 {
						net := network.NewVMNetwork(machineID, info.IP, m.logger, info.NetworkBridge)
						net.CleanupPortRules(m.cfg.HostInterface, rules)
					}
					// Do NOT delete port_rule rows — preserved for restart
				}
			}
			if info.Namespace != "" {
				ns := &network.VMNetwork{NamespaceName: info.Namespace, Logger: m.logger}
				ns.Cleanup(m.cfg.HostInterface)
			}
			// Set to stopped (not error) — machine is restartable with same IP and ports.
			// Do NOT release IP — preserved for restart.
			if m.db != nil {
				db.SafeExec(m.db, "UPDATE machine SET status = 'stopped', maintenance_operation = NULL, last_state_change = ? WHERE id = ?",
					time.Now().UTC().Format(time.RFC3339), machineID)
			}
			m.unregisterMachine(machineID)
			lock.Unlock()
		}
	}
}

// rehydrateFromDisk scans VMBaseDir and re-registers any machine that still has a running Firecracker process.
// This allows exec, terminal, and list to work after a daemon restart.
func (m *Manager) rehydrateFromDisk() {
	entries, err := os.ReadDir(m.cfg.VMBaseDir)
	if err != nil {
		m.logger.Warn("rehydrate: could not read machine base dir", zap.Error(err))
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		machineID := e.Name()
		machineDir := filepath.Join(m.cfg.VMBaseDir, machineID)
		nsData, err := os.ReadFile(filepath.Join(machineDir, "namespace"))
		if err != nil {
			continue
		}
		pidData, err := os.ReadFile(filepath.Join(machineDir, "firecracker.pid"))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if err != nil || pid <= 0 {
			continue
		}
		if syscall.Kill(pid, 0) != nil {
			continue
		}
		namespace := strings.TrimSpace(string(nsData))
		if namespace == "" {
			continue
		}
		machineIP := network.GetAllocatedIP(m.db, machineID)
		if machineIP == "" {
			m.logger.Warn("skipping rehydration: no IP in DB for machine", zap.String("machine_id", machineID))
			continue
		}
		// Read network bridge from vm_spec.json (for named network machines)
		var netBridge string
		if specData, err := os.ReadFile(filepath.Join(machineDir, "vm_spec.json")); err == nil {
			var spec struct {
				NetworkBridge string `json:"network_bridge"`
			}
			if json.Unmarshal(specData, &spec) == nil {
				netBridge = spec.NetworkBridge
			}
		}
		m.registerMachine(&MachineInfo{
			MachineID:     machineID,
			PID:           pid,
			Namespace:     namespace,
			IP:            machineIP,
			Status:        "running",
			NetworkBridge: netBridge,
		})
		m.logger.Info("rehydrated machine from disk", zap.String("machine_id", machineID), zap.String("namespace", namespace), zap.Int("pid", pid), zap.String("ip", machineIP))
	}
}

// recreateNamedBridges ensures all named bridges from the network DB table exist on the system.
// Named bridges are kernel state and don't survive reboots. This recreates them on daemon startup
// so VMs on named networks can start after a reboot.
func (m *Manager) recreateNamedBridges() {
	if m.db == nil {
		return
	}
	rows, err := m.db.Query("SELECT name, subnet, bridge_name FROM network")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name, subnet, bridgeName string
		if rows.Scan(&name, &subnet, &bridgeName) != nil {
			continue
		}
		if err := network.CreateNamedBridge(bridgeName, subnet, m.logger); err != nil {
			m.logger.Error("failed to recreate named bridge", zap.String("bridge", bridgeName), zap.String("subnet", subnet), zap.Error(err))
		} else {
			m.logger.Info("recreated named bridge on startup", zap.String("bridge", bridgeName), zap.String("subnet", subnet))
		}
	}
}

// cleanupStaleBridges removes br-* bridges that exist on the system but are NOT in the
// network DB table. This handles bridges left behind from previous sessions (daemon crash,
// DB reset, etc.) that would otherwise cause subnet conflicts on network create.
func (m *Manager) cleanupStaleBridges() {
	if m.db == nil {
		return
	}

	// Get bridges from DB
	knownBridges := make(map[string]bool)
	knownBridges[network.BridgeName] = true // sistemo0 is always known
	rows, err := m.db.Query("SELECT bridge_name FROM network")
	if err == nil {
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				knownBridges[name] = true
			}
		}
		rows.Close()
	}

	// Get br-* bridges from system
	for _, br := range network.ListNamedBridges() {
		if !knownBridges[br] {
			m.logger.Info("removing stale bridge not in DB", zap.String("bridge", br))
			network.DeleteNamedBridge(br, m.logger)
		}
	}
}

// cleanupDeadRunningMachines finds DB rows with status='running' that were NOT rehydrated
// (their process died while daemon was down or system rebooted).
// Sets them to 'stopped' — preserving IP and port rules so the user can restart them
// and get the same configuration back. This matches the behavior of `sistemo machine stop`.
func (m *Manager) cleanupDeadRunningMachines() {
	if m.db == nil {
		return
	}
	rows, err := m.db.Query("SELECT id FROM machine WHERE status = 'running'")
	if err != nil {
		return
	}
	defer rows.Close()
	var deadIDs []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			continue
		}
		m.mu.RLock()
		_, alive := m.machines[id]
		m.mu.RUnlock()
		if !alive {
			deadIDs = append(deadIDs, id)
		}
	}
	for _, id := range deadIDs {
		// Set to 'stopped' (not 'error') — machine is restartable with same IP and ports.
		// Do NOT delete port_rules or release IP — preserve them for restart.
		db.SafeExec(m.db, "UPDATE machine SET status = 'stopped', maintenance_operation = NULL, last_state_change = ? WHERE id = ?",
			time.Now().UTC().Format(time.RFC3339), id)
		m.logger.Info("marked dead running machine as stopped (restartable)", zap.String("machine_id", id))
	}
}

// cleanupStaleMaintenanceMachines recovers machines and volumes stuck in 'maintenance' after a daemon crash.
// Machines stuck in 'creating' are incomplete and should be deleted.
// Machines stuck in other maintenance ops are set to 'error' so the user can retry or delete.
// Volumes stuck in 'maintenance' whose machine is not running are reset to 'online'.
func (m *Manager) cleanupStaleMaintenanceMachines() {
	if m.db == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// Machines stuck in maintenance/creating: incomplete creation — clean up everything.
	// Collect IDs first, close rows, THEN do cleanup writes (avoids SQLite read/write lock contention).
	type staleMachine struct{ id, name string }
	var staleCreating []staleMachine
	rows, err := m.db.Query("SELECT id, name FROM machine WHERE status = 'maintenance' AND maintenance_operation = 'creating'")
	if err == nil {
		for rows.Next() {
			var s staleMachine
			if rows.Scan(&s.id, &s.name) == nil {
				staleCreating = append(staleCreating, s)
			}
		}
		rows.Close()
	}
	for _, s := range staleCreating {
		m.logger.Warn("cleaning up incomplete machine creation", zap.String("machine_id", s.id), zap.String("name", s.name))
		db.SafeExec(m.db, "DELETE FROM ip_allocation WHERE machine_id = ?", s.id)
		db.SafeExec(m.db, "DELETE FROM port_rule WHERE machine_id = ?", s.id)
		// Detach all volumes — reset to 'online' so user can reuse them. Never delete user volumes.
		db.SafeExec(m.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE machine_id=?", now, s.id)
		// Remove machine working directory (contains the rootfs COPY, not the original volume)
		os.RemoveAll(filepath.Join(m.cfg.VMBaseDir, s.id))
		db.SafeExec(m.db, "DELETE FROM machine WHERE id = ?", s.id)
		db.LogAction(m.db, "cleanup.stale_creating", "machine", s.id, s.name, "Removed incomplete machine from crashed creation", true)
	}

	// Machines stuck in stopping/starting: the Firecracker process died with the daemon,
	// so these machines are effectively stopped. Set to 'stopped' (not 'error') so the
	// user can restart them without losing their configuration.
	if result, err := m.db.Exec("UPDATE machine SET status = 'stopped', maintenance_operation = NULL, last_state_change = ? WHERE status = 'maintenance' AND maintenance_operation IN ('stopping', 'starting')", now); err != nil {
		m.logger.Warn("failed to reset stale stopping/starting machines", zap.Error(err))
	} else if n, _ := result.RowsAffected(); n > 0 {
		m.logger.Warn("reset stale stopping/starting machines to stopped", zap.Int64("count", n))
	}

	// Machines stuck in deleting or other unknown maintenance: mark as error so user can retry
	if result, err := m.db.Exec("UPDATE machine SET status = 'error', maintenance_operation = NULL, last_state_change = ? WHERE status = 'maintenance'", now); err != nil {
		m.logger.Warn("failed to reset stale maintenance machines", zap.Error(err))
	} else if n, _ := result.RowsAffected(); n > 0 {
		m.logger.Warn("reset stale maintenance machines to error", zap.Int64("count", n))
	}

	// Volumes stuck in maintenance whose attached machine is not running: reset to online
	if result, err := m.db.Exec(`UPDATE volume SET status='online', machine_id=NULL, last_state_change=?
		WHERE status='maintenance'
		AND (machine_id IS NULL OR machine_id NOT IN (SELECT id FROM machine WHERE status='running'))`, now); err != nil {
		m.logger.Warn("failed to reset stale maintenance volumes", zap.Error(err))
	} else if n, _ := result.RowsAffected(); n > 0 {
		m.logger.Warn("reset stale maintenance volumes to online", zap.Int64("count", n))
	}
}

// restorePortRules re-applies nftables DNAT rules for all port_rule entries of running machines.
// Called on daemon startup after rehydration to restore port forwarding lost during restart.
func (m *Manager) restorePortRules() {
	if m.db == nil {
		return
	}
	rows, err := m.db.Query(
		`SELECT pr.machine_id, pr.host_port, pr.machine_port, pr.protocol
		 FROM port_rule pr JOIN machine v ON pr.machine_id = v.id
		 WHERE v.status = 'running'`)
	if err != nil {
		m.logger.Warn("failed to query port_rules for restore", zap.Error(err))
		return
	}
	defer rows.Close()
	restored := 0
	for rows.Next() {
		var machineID string
		var hostPort, machinePort int
		var protocol string
		if rows.Scan(&machineID, &hostPort, &machinePort, &protocol) != nil {
			continue
		}
		m.mu.RLock()
		info, ok := m.machines[machineID]
		m.mu.RUnlock()
		if !ok || info.IP == "" {
			continue
		}
		// Flush any stale DNAT rules for this host port before re-adding.
		// Handles cases where machine IP changed (reboot, redeployment) or duplicate
		// rules accumulated from previous daemon runs.
		network.FlushDNATRulesForPort(hostPort, protocol)

		net := network.NewVMNetwork(machineID, info.IP, m.logger, info.NetworkBridge)
		if err := net.ExposePort(m.cfg.HostInterface, hostPort, machinePort, protocol); err != nil {
			m.logger.Warn("failed to restore port rule",
				zap.String("machine_id", machineID), zap.Int("host_port", hostPort), zap.Error(err))
			continue
		}
		restored++
	}
	if restored > 0 {
		m.logger.Info("restored port expose rules from DB", zap.Int("count", restored))
	}
}

// reqLogger returns a logger tagged with request_id from context (if present).
func (m *Manager) reqLogger(ctx context.Context) *zap.Logger {
	return agentmw.LoggerFromCtx(ctx, m.logger)
}

func (m *Manager) getMachineLock(machineID string) *sync.Mutex {
	val, _ := m.machineLocks.LoadOrStore(machineID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

func (m *Manager) Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error) {
	lock := m.getMachineLock(req.MachineID)
	lock.Lock()
	defer lock.Unlock()

	m.reqLogger(ctx).Info("creating machine", zap.String("machine_id", req.MachineID), zap.String("image", req.Image))
	return createMachine(ctx, m, req)
}

func (m *Manager) Delete(ctx context.Context, machineID string, preserveStorage bool) (bool, error) {
	lock := m.getMachineLock(machineID)
	lock.Lock()
	defer lock.Unlock()
	defer m.machineLocks.Delete(machineID)

	m.reqLogger(ctx).Info("deleting machine", zap.String("machine_id", machineID), zap.Bool("preserve_storage", preserveStorage))
	return deleteMachine(ctx, m, machineID, preserveStorage)
}

func (m *Manager) Stop(ctx context.Context, machineID string) (bool, error) {
	lock := m.getMachineLock(machineID)
	lock.Lock()
	defer lock.Unlock()

	m.reqLogger(ctx).Info("stopping machine", zap.String("machine_id", machineID))
	return stopMachine(ctx, m, machineID)
}

func (m *Manager) Start(ctx context.Context, machineID string) (*CreateResponse, error) {
	lock := m.getMachineLock(machineID)
	lock.Lock()
	defer lock.Unlock()

	m.reqLogger(ctx).Info("starting machine", zap.String("machine_id", machineID))
	return startMachine(ctx, m, machineID)
}

func (m *Manager) GetIP(ctx context.Context, machineID string) *IPResult {
	m.mu.RLock()
	info, ok := m.machines[machineID]
	m.mu.RUnlock()

	result := &IPResult{MachineID: machineID}
	if ok {
		via := "bridge"
		result.IP = &info.IP
		result.Namespace = &info.Namespace
		result.DiscoveredVia = &via
	} else {
		ip := network.GetAllocatedIP(m.db, machineID)
		if ip != "" {
			result.IP = &ip
		}
	}
	return result
}

func (m *Manager) GetMachineNamespace(machineID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if info, ok := m.machines[machineID]; ok {
		return info.Namespace
	}
	return ""
}

func (m *Manager) GetMachineIP(machineID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if info, ok := m.machines[machineID]; ok {
		return info.IP
	}
	return network.GetAllocatedIP(m.db, machineID)
}

func (m *Manager) ListMachines() []*MachineInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*MachineInfo, 0, len(m.machines))
	for _, v := range m.machines {
		list = append(list, v)
	}
	return list
}

// ExposePort sets up nftables DNAT to forward hostPort on the host to machinePort inside the machine.
func (m *Manager) ExposePort(machineID string, hostPort, machinePort int, protocol string) error {
	m.mu.RLock()
	info, ok := m.machines[machineID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("machine %s not found or not running", machineID)
	}
	net := network.NewVMNetwork(machineID, info.IP, m.logger, info.NetworkBridge)
	return net.ExposePort(m.cfg.HostInterface, hostPort, machinePort, protocol)
}

// UnexposePort removes DNAT rules for the given hostPort on a machine.
func (m *Manager) UnexposePort(machineID string, hostPort, machinePort int, protocol string) error {
	m.mu.RLock()
	info, ok := m.machines[machineID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("machine %s not found or not running", machineID)
	}
	net := network.NewVMNetwork(machineID, info.IP, m.logger, info.NetworkBridge)
	return net.UnexposePort(m.cfg.HostInterface, hostPort, machinePort, protocol)
}

// CleanupPortRules removes all port forwarding rules for a machine.
func (m *Manager) CleanupPortRules(machineID string, rules []network.PortRule) {
	m.mu.RLock()
	info, ok := m.machines[machineID]
	m.mu.RUnlock()
	machineIP := ""
	bridge := ""
	if ok {
		machineIP = info.IP
		bridge = info.NetworkBridge
	} else {
		machineIP = network.GetAllocatedIP(m.db, machineID)
	}
	if machineIP == "" {
		return
	}
	net := network.NewVMNetwork(machineID, machineIP, m.logger, bridge)
	net.CleanupPortRules(m.cfg.HostInterface, rules)
}

func (m *Manager) Exec(ctx context.Context, machineID, script string, timeoutSec int) (*ExecResult, error) {
	m.logger.Info("exec on machine", zap.String("machine_id", machineID))
	return execOnMachine(ctx, m, machineID, script, timeoutSec)
}

func (m *Manager) registerMachine(info *MachineInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.machines[info.MachineID] = info
}

func (m *Manager) unregisterMachine(machineID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.machines, machineID)
}

// waitForSSH polls TCP port 22 on the machine. With the bridge architecture,
// the machine has a unique IP reachable directly from the host.
func (m *Manager) waitForSSH(machineIP string, timeout time.Duration) bool {
	start := time.Now()
	attempt := 0
	var lastErr error
	for time.Since(start) < timeout {
		attempt++
		conn, err := net.DialTimeout("tcp", machineIP+":22", agent.SSHDialTimeout)
		if err == nil {
			conn.Close()
			m.logger.Info("SSH ready",
				zap.String("machine_ip", machineIP),
				zap.Int("attempts", attempt),
				zap.Duration("elapsed", time.Since(start)))
			return true
		}
		lastErr = err
		time.Sleep(agent.SSHPollInterval)
	}
	m.logger.Warn("SSH readiness check timed out",
		zap.String("machine_ip", machineIP),
		zap.Duration("timeout", timeout),
		zap.Error(lastErr))
	return false
}
