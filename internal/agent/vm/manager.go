package vm

import (
	"context"
	"fmt"
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
	mu      sync.RWMutex
	vms     map[string]*VMInfo
	vmLocks sync.Map // map[string]*sync.Mutex — per-VM operation lock
}

func NewManager(cfg *config.Config, logger *zap.Logger) *Manager {
	m := &Manager{cfg: cfg, logger: logger, vms: make(map[string]*VMInfo)}
	os.MkdirAll(cfg.VMBaseDir, 0755)
	m.rehydrateFromDisk()
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
			m.logger.Warn("reconciler: VM process dead, cleaning up",
				zap.String("vm_id", vmID), zap.Int("pid", info.PID))
			if info.Namespace != "" {
				ns := &network.VMNetwork{NamespaceName: info.Namespace, Logger: m.logger}
				ns.Cleanup(m.cfg.HostInterface)
			}
			m.unregisterVM(vmID)
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
		m.registerVM(&VMInfo{
			VMID:      vmID,
			PID:       pid,
			Namespace: namespace,
			IP:        network.VMIP,
			Status:    "running",
		})
		m.logger.Info("rehydrated VM from disk", zap.String("vm_id", vmID), zap.String("namespace", namespace), zap.Int("pid", pid))
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
		via := "namespace-isolation"
		result.IP = &info.IP
		result.Namespace = &info.Namespace
		result.DiscoveredVia = &via
	} else {
		vmDir := filepath.Join(m.cfg.VMBaseDir, vmID)
		if _, err := os.Stat(vmDir); err == nil {
			ip := network.VMIP
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
	vmDir := filepath.Join(m.cfg.VMBaseDir, vmID)
	if _, err := os.Stat(vmDir); err == nil {
		return network.VMIP
	}
	return ""
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

// waitForSSH polls TCP port 22 on the VM inside its network namespace.
func (m *Manager) waitForSSH(namespaceName, vmIP string, timeout time.Duration) bool {
	start := time.Now()
	attempt := 0
	for time.Since(start) < timeout {
		attempt++
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		cmd := execCommandContext(ctx, "ip", "netns", "exec", namespaceName,
			"bash", "-c", fmt.Sprintf("echo > /dev/tcp/%s/22", vmIP))
		err := cmd.Run()
		cancel()
		if err == nil {
			m.logger.Info("SSH ready",
				zap.String("namespace", namespaceName),
				zap.Int("attempts", attempt),
				zap.Duration("elapsed", time.Since(start)))
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.logger.Warn("SSH readiness check timed out",
		zap.String("namespace", namespaceName),
		zap.Duration("timeout", timeout))
	return false
}
