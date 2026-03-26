package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"

	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/network"
	"github.com/davidestf/sistemo/internal/agent/vm"
	"go.uber.org/zap"
)

// DashboardAPI serves the /api/v1/ endpoints for the dashboard frontend.
// It joins DB state with in-memory manager data for rich responses.
type DashboardAPI struct {
	mgr             *vm.Manager
	cfg             *config.Config
	db              *sql.DB
	logger          *zap.Logger
	BuildScript     []byte // embedded build-rootfs.sh content
	VMInitScript    []byte // embedded vm-init.sh content
}

func NewDashboardAPI(mgr *vm.Manager, cfg *config.Config, db *sql.DB, logger *zap.Logger) *DashboardAPI {
	api := &DashboardAPI{mgr: mgr, cfg: cfg, db: db, logger: logger}
	api.CleanupOrphanedDownloads()
	return api
}

// --- Response types (shared across dashboard_*.go files) ---

type vmV1Response struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Status               string          `json:"status"`
	MaintenanceOperation string          `json:"maintenance_operation,omitempty"`
	Image                string          `json:"image"`
	IPAddress            string          `json:"ip_address"`
	Namespace            string          `json:"namespace,omitempty"`
	VCPUs                int             `json:"vcpus"`
	MemoryMB             int             `json:"memory_mb"`
	StorageMB            int             `json:"storage_mb"`
	NetworkName          string          `json:"network_name"`
	CreatedAt            string          `json:"created_at"`
	LastStateChange      string          `json:"last_state_change"`
	PortRules            []portRuleEntry `json:"port_rules"`
	PID                  int             `json:"pid"`
	ImageDigest          string          `json:"image_digest,omitempty"`
}

type portRuleEntry struct {
	HostPort int    `json:"host_port"`
	VMPort   int    `json:"vm_port"`
	Protocol string `json:"protocol"`
}

type networkV1Response struct {
	Name       string `json:"name"`
	Subnet     string `json:"subnet"`
	BridgeName string `json:"bridge_name"`
	VMCount    int    `json:"vm_count"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// --- Handlers ---

// ListVMs returns all non-deleted VMs with port rules and live PID.
func (h *DashboardAPI) ListVMs(w http.ResponseWriter, r *http.Request) {
	vms, err := h.queryVMs("")
	if err != nil {
		h.logger.Error("api/v1/vms query failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to query VMs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"vms": vms})
}

// GetVM returns a single VM by ID.
func (h *DashboardAPI) GetVM(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmID")
	if !isValidSafeID(vmID) {
		writeError(w, http.StatusBadRequest, "invalid VM id")
		return
	}
	vms, err := h.queryVMs(vmID)
	if err != nil {
		h.logger.Error("api/v1/vms/{id} query failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to query VM")
		return
	}
	if len(vms) == 0 {
		writeError(w, http.StatusNotFound, "VM not found")
		return
	}
	writeJSON(w, http.StatusOK, vms[0])
}

// queryVMs fetches VMs from DB and enriches with in-memory PID + port rules.
// If vmID is empty, returns all non-deleted VMs. Otherwise filters by ID.
func (h *DashboardAPI) queryVMs(vmID string) ([]vmV1Response, error) {
	if h.db == nil {
		return []vmV1Response{}, nil
	}

	query := `
		SELECT v.id, v.name, v.status, v.maintenance_operation, v.image,
		       v.ip_address, v.namespace, v.vcpus, v.memory_mb, v.storage_mb,
		       COALESCE(n.name, 'default'), v.created_at, v.last_state_change,
		       COALESCE(v.image_digest, '')
		FROM vm v LEFT JOIN network n ON v.network_id = n.id
		WHERE v.status != 'deleted'`
	args := []interface{}{}

	if vmID != "" {
		query += " AND v.id = ?"
		args = append(args, vmID)
	}
	query += " ORDER BY v.last_state_change DESC"

	rows, err := h.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Build PID lookup from in-memory manager
	pidMap := map[string]int{}
	for _, info := range h.mgr.ListVMs() {
		pidMap[info.VMID] = info.PID
	}

	var result []vmV1Response
	for rows.Next() {
		var v vmV1Response
		var maintOp, image, ip, ns, netName, createdAt, lastChange sql.NullString
		var imageDigest string
		if err := rows.Scan(&v.ID, &v.Name, &v.Status, &maintOp, &image,
			&ip, &ns, &v.VCPUs, &v.MemoryMB, &v.StorageMB,
			&netName, &createdAt, &lastChange, &imageDigest); err != nil {
			h.logger.Warn("scan VM row failed", zap.Error(err))
			continue
		}
		v.MaintenanceOperation = maintOp.String
		v.Image = image.String
		v.IPAddress = ip.String
		v.Namespace = ns.String
		v.NetworkName = netName.String
		v.CreatedAt = createdAt.String
		v.LastStateChange = lastChange.String
		v.PID = pidMap[v.ID]
		v.ImageDigest = imageDigest
		v.PortRules = h.queryPortRules(v.ID)
		result = append(result, v)
	}
	if result == nil {
		result = []vmV1Response{} // return [] not null
	}
	return result, rows.Err()
}

func (h *DashboardAPI) queryPortRules(vmID string) []portRuleEntry {
	if h.db == nil {
		return []portRuleEntry{}
	}
	rows, err := h.db.Query("SELECT host_port, vm_port, protocol FROM port_rule WHERE vm_id = ?", vmID)
	if err != nil {
		return []portRuleEntry{}
	}
	defer rows.Close()
	var rules []portRuleEntry
	for rows.Next() {
		var r portRuleEntry
		if rows.Scan(&r.HostPort, &r.VMPort, &r.Protocol) == nil {
			rules = append(rules, r)
		}
	}
	if rules == nil {
		rules = []portRuleEntry{}
	}
	return rules
}

// System returns aggregated system info for the dashboard.
func (h *DashboardAPI) System(w http.ResponseWriter, r *http.Request) {
	// Health checks
	firecrackerOK := fileExists(h.cfg.FirecrackerBin)
	kernelOK := fileExists(h.cfg.KernelImagePath)
	healthStatus := "healthy"
	if !firecrackerOK || !kernelOK {
		healthStatus = "degraded"
	}

	// VM stats from DB
	var total, running, stopped, errored int
	var vcpusAlloc, memAlloc int
	dbOK := true
	if h.db != nil {
		for _, q := range []struct {
			sql  string
			dest *int
		}{
			{"SELECT COUNT(*) FROM vm WHERE status != 'deleted'", &total},
			{"SELECT COUNT(*) FROM vm WHERE status = 'running'", &running},
			{"SELECT COUNT(*) FROM vm WHERE status = 'stopped'", &stopped},
			{"SELECT COUNT(*) FROM vm WHERE status IN ('error','failed','unhealthy')", &errored},
			{"SELECT COALESCE(SUM(vcpus),0) FROM vm WHERE status = 'running'", &vcpusAlloc},
			{"SELECT COALESCE(SUM(memory_mb),0) FROM vm WHERE status = 'running'", &memAlloc},
		} {
			if err := h.db.QueryRow(q.sql).Scan(q.dest); err != nil {
				h.logger.Warn("system stats query failed", zap.String("query", q.sql), zap.Error(err))
				dbOK = false
			}
		}
	}
	if !dbOK {
		healthStatus = "degraded"
	}

	// Host info
	hostname, _ := os.Hostname()
	kernelVersion := ""
	if data, err := os.ReadFile("/proc/version"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) >= 3 {
			kernelVersion = parts[2]
		}
	}
	cpuCount := runtime.NumCPU()
	var totalMemMB int64
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					var kb int64
					fmt.Sscanf(fields[1], "%d", &kb)
					totalMemMB = kb / 1024
				}
				break
			}
		}
	}

	// Disk usage for data dir
	var diskTotalGB, diskUsedGB, diskFreeGB int64
	if stat, err := os.Stat(h.cfg.DataDir); err == nil && stat.IsDir() {
		var fsStat syscall.Statfs_t
		if syscall.Statfs(h.cfg.DataDir, &fsStat) == nil {
			diskTotalGB = int64(fsStat.Blocks) * int64(fsStat.Bsize) / (1024 * 1024 * 1024)
			diskFreeGB = int64(fsStat.Bavail) * int64(fsStat.Bsize) / (1024 * 1024 * 1024)
			diskUsedGB = diskTotalGB - diskFreeGB
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"health": map[string]interface{}{
			"status": healthStatus,
			"checks": map[string]bool{"firecracker": firecrackerOK, "kernel": kernelOK},
		},
		"host": map[string]interface{}{
			"hostname":       hostname,
			"kernel":         kernelVersion,
			"cpus":           cpuCount,
			"memory_mb":      totalMemMB,
			"disk_total_gb":  diskTotalGB,
			"disk_used_gb":   diskUsedGB,
			"disk_free_gb":   diskFreeGB,
		},
		"daemon": map[string]interface{}{
			"go_version": runtime.Version(),
			"arch":       runtime.GOARCH,
			"bridge":     network.BridgeName,
			"goroutines": runtime.NumGoroutine(),
		},
		"stats": map[string]int{
			"total":               total,
			"running":             running,
			"stopped":             stopped,
			"errored":             errored,
			"vcpus_allocated":     vcpusAlloc,
			"memory_mb_allocated": memAlloc,
		},
		"limits": map[string]int{
			"max_vcpus":      h.cfg.MaxVCPUs,
			"max_memory_mb":  h.cfg.MaxMemoryMB,
			"max_storage_mb": h.cfg.MaxStorageMB,
		},
	})
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
