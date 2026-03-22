package vm

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

	agentmw "github.com/davidestf/sistemo/internal/agent/api/middleware"
	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/network"
	"go.uber.org/zap"
)

// Manager orchestrates VM lifecycle operations.
type Manager struct {
	cfg     *config.Config
	logger  *zap.Logger
	db      *sql.DB
	mu      sync.RWMutex
	vms     map[string]*VMInfo
	vmLocks sync.Map // map[string]*sync.Mutex — per-VM operation lock
}

func NewManager(cfg *config.Config, logger *zap.Logger, db *sql.DB) *Manager {
	m := &Manager{cfg: cfg, logger: logger, db: db, vms: make(map[string]*VMInfo)}
	if err := os.MkdirAll(cfg.VMBaseDir, 0755); err != nil {
		logger.Fatal("failed to create VM base directory", zap.String("path", cfg.VMBaseDir), zap.Error(err))
	}

	// Parse bridge subnet from config and create the bridge
	if err := network.ParseBridgeSubnet(cfg.BridgeSubnet); err != nil {
		logger.Fatal("invalid bridge_subnet", zap.Error(err))
	}
	if err := network.EnsureBridge(cfg.HostInterface, logger); err != nil {
		logger.Fatal("failed to create bridge — VM networking will not work", zap.Error(err))
	}
	m.cleanupStaleBridges()
	m.rehydrateFromDisk()
	m.cleanupDeadRunningVMs()
	m.restorePortRules()
	preserve := make(map[string]struct{})
	m.mu.RLock()
	for _, info := range m.vms {
		if info.Namespace != "" {
			preserve[info.Namespace] = struct{}{}
		}
	}
	m.mu.RUnlock()
	network.CleanupAllNamespaces(cfg.HostInterface, logger, preserve)
	go m.runReconciler()
	return m
}

// runReconciler periodically checks for dead VM processes and cleans them up.
func (m *Manager) runReconciler() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m.reconcile()
	}
}

func (m *Manager) reconcile() {
	m.mu.RLock()
	snapshot := make(map[string]*VMInfo, len(m.vms))
	for k, v := range m.vms {
		snapshot[k] = v
	}
	m.mu.RUnlock()

	for vmID, info := range snapshot {
		if info.PID <= 0 {
			continue
		}
		if syscall.Kill(info.PID, 0) != nil {
			// Acquire per-VM lock to prevent race with concurrent Start/Create
			lock := m.getVMLock(vmID)
			lock.Lock()

			// Re-check: another operation may have re-registered this VM with a new PID
			m.mu.RLock()
			current, exists := m.vms[vmID]
			m.mu.RUnlock()
			if exists && current.PID != info.PID {
				lock.Unlock()
				continue // VM was restarted, skip cleanup
			}

			m.logger.Warn("reconciler: VM process dead, cleaning up",
				zap.String("vm_id", vmID), zap.Int("pid", info.PID))
			// Clean up port expose iptables rules before removing network
			if m.db != nil && info.IP != "" {
				rows, err := m.db.Query("SELECT host_port, vm_port, protocol FROM port_rule WHERE vm_id = ?", vmID)
				if err == nil {
					var rules []network.PortRule
					for rows.Next() {
						var r network.PortRule
						if err := rows.Scan(&r.HostPort, &r.VMPort, &r.Protocol); err != nil {
							m.logger.Warn("reconciler: failed to scan port rule", zap.Error(err))
							continue
						}
						rules = append(rules, r)
					}
					rows.Close()
					if len(rules) > 0 {
						net := network.NewVMNetwork(vmID, info.IP, m.logger, info.NetworkBridge)
						net.CleanupPortRules(m.cfg.HostInterface, rules)
					}
					m.db.Exec("DELETE FROM port_rule WHERE vm_id = ?", vmID)
				}
			}
			if info.Namespace != "" {
				ns := &network.VMNetwork{NamespaceName: info.Namespace, Logger: m.logger}
				ns.Cleanup(m.cfg.HostInterface)
			}
			// Update DB status and release IP so resources don't leak.
			if m.db != nil {
				m.db.Exec("UPDATE vm SET status = 'error', maintenance_operation = 'process_died', last_state_change = ? WHERE id = ?",
					time.Now().UTC().Format(time.RFC3339), vmID)
			}
			network.ReleaseIP(m.db, vmID)
			m.unregisterVM(vmID)
			lock.Unlock()
		}
	}
}

// rehydrateFromDisk scans VMBaseDir and re-registers any VM that still has a running Firecracker process.
// This allows exec, terminal, and list to work after a daemon restart.
func (m *Manager) rehydrateFromDisk() {
	entries, err := os.ReadDir(m.cfg.VMBaseDir)
	if err != nil {
		m.logger.Warn("rehydrate: could not read VM base dir", zap.Error(err))
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		vmID := e.Name()
		vmDir := filepath.Join(m.cfg.VMBaseDir, vmID)
		nsData, err := os.ReadFile(filepath.Join(vmDir, "namespace"))
		if err != nil {
			continue
		}
		pidData, err := os.ReadFile(filepath.Join(vmDir, "firecracker.pid"))
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
		vmIP := network.GetAllocatedIP(m.db, vmID)
		if vmIP == "" {
			m.logger.Warn("skipping rehydration: no IP in DB for VM", zap.String("vm_id", vmID))
			continue
		}
		// Read network bridge from vm_spec.json (for named network VMs)
		var netBridge string
		if specData, err := os.ReadFile(filepath.Join(vmDir, "vm_spec.json")); err == nil {
			var spec struct {
				NetworkBridge string `json:"network_bridge"`
			}
			if json.Unmarshal(specData, &spec) == nil {
				netBridge = spec.NetworkBridge
			}
		}
		m.registerVM(&VMInfo{
			VMID:          vmID,
			PID:           pid,
			Namespace:     namespace,
			IP:            vmIP,
			Status:        "running",
			NetworkBridge: netBridge,
		})
		m.logger.Info("rehydrated VM from disk", zap.String("vm_id", vmID), zap.String("namespace", namespace), zap.Int("pid", pid), zap.String("ip", vmIP))
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

// cleanupDeadRunningVMs finds DB rows with status='running' that were NOT rehydrated
// (their process died while daemon was down). Marks them as error and releases resources.
func (m *Manager) cleanupDeadRunningVMs() {
	if m.db == nil {
		return
	}
	rows, err := m.db.Query("SELECT id FROM vm WHERE status = 'running'")
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
		_, alive := m.vms[id]
		m.mu.RUnlock()
		if !alive {
			deadIDs = append(deadIDs, id)
		}
	}
	for _, id := range deadIDs {
		m.db.Exec("UPDATE vm SET status = 'error', maintenance_operation = 'process_died', last_state_change = ? WHERE id = ?",
			time.Now().UTC().Format(time.RFC3339), id)
		m.db.Exec("DELETE FROM port_rule WHERE vm_id = ?", id)
		network.ReleaseIP(m.db, id)
		m.logger.Info("cleaned up dead running VM from previous run", zap.String("vm_id", id))
	}
}

// restorePortRules re-applies iptables DNAT rules for all port_rule entries of running VMs.
// Called on daemon startup after rehydration to restore port forwarding lost during restart.
func (m *Manager) restorePortRules() {
	if m.db == nil {
		return
	}
	rows, err := m.db.Query(
		`SELECT pr.vm_id, pr.host_port, pr.vm_port, pr.protocol
		 FROM port_rule pr JOIN vm v ON pr.vm_id = v.id
		 WHERE v.status = 'running'`)
	if err != nil {
		m.logger.Warn("failed to query port_rules for restore", zap.Error(err))
		return
	}
	defer rows.Close()
	restored := 0
	for rows.Next() {
		var vmID string
		var hostPort, vmPort int
		var protocol string
		if rows.Scan(&vmID, &hostPort, &vmPort, &protocol) != nil {
			continue
		}
		m.mu.RLock()
		info, ok := m.vms[vmID]
		m.mu.RUnlock()
		if !ok || info.IP == "" {
			continue
		}
		net := network.NewVMNetwork(vmID, info.IP, m.logger, info.NetworkBridge)
		if err := net.ExposePort(m.cfg.HostInterface, hostPort, vmPort, protocol); err != nil {
			m.logger.Warn("failed to restore port rule",
				zap.String("vm_id", vmID), zap.Int("host_port", hostPort), zap.Error(err))
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

func (m *Manager) getVMLock(vmID string) *sync.Mutex {
	val, _ := m.vmLocks.LoadOrStore(vmID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

func (m *Manager) Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error) {
	lock := m.getVMLock(req.VMID)
	lock.Lock()
	defer lock.Unlock()

	m.reqLogger(ctx).Info("creating VM", zap.String("vm_id", req.VMID), zap.String("image", req.Image))
	return createVM(ctx, m, req)
}

func (m *Manager) Delete(ctx context.Context, vmID string, preserveStorage bool) (bool, error) {
	lock := m.getVMLock(vmID)
	lock.Lock()
	defer lock.Unlock()
	defer m.vmLocks.Delete(vmID)

	m.reqLogger(ctx).Info("deleting VM", zap.String("vm_id", vmID), zap.Bool("preserve_storage", preserveStorage))
	return deleteVM(ctx, m, vmID, preserveStorage)
}

func (m *Manager) Stop(ctx context.Context, vmID string) (bool, error) {
	lock := m.getVMLock(vmID)
	lock.Lock()
	defer lock.Unlock()

	m.reqLogger(ctx).Info("stopping VM", zap.String("vm_id", vmID))
	return stopVM(ctx, m, vmID)
}

func (m *Manager) Start(ctx context.Context, vmID string) (*CreateResponse, error) {
	lock := m.getVMLock(vmID)
	lock.Lock()
	defer lock.Unlock()

	m.reqLogger(ctx).Info("starting VM", zap.String("vm_id", vmID))
	return startVM(ctx, m, vmID)
}

func (m *Manager) GetIP(ctx context.Context, vmID string) *IPResult {
	m.mu.RLock()
	info, ok := m.vms[vmID]
	m.mu.RUnlock()

	result := &IPResult{VMID: vmID}
	if ok {
		via := "bridge"
		result.IP = &info.IP
		result.Namespace = &info.Namespace
		result.DiscoveredVia = &via
	} else {
		ip := network.GetAllocatedIP(m.db, vmID)
		if ip != "" {
			result.IP = &ip
		}
	}
	return result
}

func (m *Manager) GetVMNamespace(vmID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if info, ok := m.vms[vmID]; ok {
		return info.Namespace
	}
	return ""
}

func (m *Manager) GetVMIP(vmID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if info, ok := m.vms[vmID]; ok {
		return info.IP
	}
	return network.GetAllocatedIP(m.db, vmID)
}

func (m *Manager) ListVMs() []*VMInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*VMInfo, 0, len(m.vms))
	for _, v := range m.vms {
		list = append(list, v)
	}
	return list
}

// ExposePort sets up iptables DNAT to forward hostPort on the host to vmPort inside the VM.
func (m *Manager) ExposePort(vmID string, hostPort, vmPort int, protocol string) error {
	m.mu.RLock()
	info, ok := m.vms[vmID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("VM %s not found or not running", vmID)
	}
	net := network.NewVMNetwork(vmID, info.IP, m.logger, info.NetworkBridge)
	return net.ExposePort(m.cfg.HostInterface, hostPort, vmPort, protocol)
}

// UnexposePort removes DNAT rules for the given hostPort on a VM.
func (m *Manager) UnexposePort(vmID string, hostPort, vmPort int, protocol string) error {
	m.mu.RLock()
	info, ok := m.vms[vmID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("VM %s not found or not running", vmID)
	}
	net := network.NewVMNetwork(vmID, info.IP, m.logger, info.NetworkBridge)
	return net.UnexposePort(m.cfg.HostInterface, hostPort, vmPort, protocol)
}

// CleanupPortRules removes all port forwarding rules for a VM.
func (m *Manager) CleanupPortRules(vmID string, rules []network.PortRule) {
	m.mu.RLock()
	info, ok := m.vms[vmID]
	m.mu.RUnlock()
	vmIP := ""
	bridge := ""
	if ok {
		vmIP = info.IP
		bridge = info.NetworkBridge
	} else {
		vmIP = network.GetAllocatedIP(m.db, vmID)
	}
	if vmIP == "" {
		return
	}
	net := network.NewVMNetwork(vmID, vmIP, m.logger, bridge)
	net.CleanupPortRules(m.cfg.HostInterface, rules)
}

func (m *Manager) Exec(ctx context.Context, vmID, script string, timeoutSec int) (*ExecResult, error) {
	m.logger.Info("exec on VM", zap.String("vm_id", vmID))
	return execOnVM(ctx, m, vmID, script, timeoutSec)
}

func (m *Manager) registerVM(info *VMInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vms[info.VMID] = info
}

func (m *Manager) unregisterVM(vmID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.vms, vmID)
}

// waitForSSH polls TCP port 22 on the VM. With the bridge architecture,
// the VM has a unique IP reachable directly from the host.
func (m *Manager) waitForSSH(vmIP string, timeout time.Duration) bool {
	start := time.Now()
	attempt := 0
	var lastErr error
	for time.Since(start) < timeout {
		attempt++
		conn, err := net.DialTimeout("tcp", vmIP+":22", 500*time.Millisecond)
		if err == nil {
			conn.Close()
			m.logger.Info("SSH ready",
				zap.String("vm_ip", vmIP),
				zap.Int("attempts", attempt),
				zap.Duration("elapsed", time.Since(start)))
			return true
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	m.logger.Warn("SSH readiness check timed out",
		zap.String("vm_ip", vmIP),
		zap.Duration("timeout", timeout),
		zap.Error(lastErr))
	return false
}
