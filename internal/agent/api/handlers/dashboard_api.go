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
	"github.com/davidestf/sistemo/internal/agent/machine"
	"github.com/davidestf/sistemo/internal/agent/network"
	"go.uber.org/zap"
)

// DashboardAPI serves the /api/v1/ endpoints for the dashboard frontend.
// It joins DB state with in-memory manager data for rich responses.
type DashboardAPI struct {
	mgr             *machine.Manager
	cfg             *config.Config
	db              *sql.DB
	logger          *zap.Logger
	BuildScript     []byte // embedded build-rootfs.sh content
	VMInitScript    []byte // embedded vm-init.sh content
}

func NewDashboardAPI(mgr *machine.Manager, cfg *config.Config, db *sql.DB, logger *zap.Logger) *DashboardAPI {
	api := &DashboardAPI{mgr: mgr, cfg: cfg, db: db, logger: logger}
	api.CleanupOrphanedDownloads()
	return api
}

// --- Response types (shared across dashboard_*.go files) ---

type machineV1Response struct {
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
	HostPort    int    `json:"host_port"`
	MachinePort int    `json:"machine_port"`
	Protocol    string `json:"protocol"`
}

type networkV1Response struct {
	Name         string `json:"name"`
	Subnet       string `json:"subnet"`
	BridgeName   string `json:"bridge_name"`
	MachineCount int    `json:"machine_count"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// --- Handlers ---

// ListMachines returns all non-deleted machines with port rules and live PID.
func (h *DashboardAPI) ListMachines(w http.ResponseWriter, r *http.Request) {
	machines, err := h.queryMachines("")
	if err != nil {
		h.logger.Error("api/v1/machines query failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to query machines")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"machines": machines})
}

// GetMachine returns a single machine by ID.
func (h *DashboardAPI) GetMachine(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")
	if !isValidSafeID(machineID) {
		writeError(w, http.StatusBadRequest, "invalid machine id")
		return
	}
	machines, err := h.queryMachines(machineID)
	if err != nil {
		h.logger.Error("api/v1/machines/{id} query failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to query machine")
		return
	}
	if len(machines) == 0 {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	writeJSON(w, http.StatusOK, machines[0])
}

// queryMachines fetches machines from DB and enriches with in-memory PID + port rules.
// If machineID is empty, returns all non-deleted machines. Otherwise filters by ID.
func (h *DashboardAPI) queryMachines(machineID string) ([]machineV1Response, error) {
	if h.db == nil {
		return []machineV1Response{}, nil
	}

	query := `
		SELECT m.id, m.name, m.status, m.maintenance_operation, m.image,
		       m.ip_address, m.namespace, m.vcpus, m.memory_mb, m.storage_mb,
		       COALESCE(n.name, 'default'), m.created_at, m.last_state_change,
		       COALESCE(m.image_digest, '')
		FROM machine m LEFT JOIN network n ON m.network_id = n.id
		WHERE m.status != 'deleted'`
	args := []interface{}{}

	if machineID != "" {
		query += " AND m.id = ?"
		args = append(args, machineID)
	}
	query += " ORDER BY m.last_state_change DESC"

	rows, err := h.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Build PID lookup from in-memory manager
	pidMap := map[string]int{}
	for _, info := range h.mgr.ListMachines() {
		pidMap[info.MachineID] = info.PID
	}

	var result []machineV1Response
	for rows.Next() {
		var m machineV1Response
		var maintOp, image, ip, ns, netName, createdAt, lastChange sql.NullString
		var imageDigest string
		if err := rows.Scan(&m.ID, &m.Name, &m.Status, &maintOp, &image,
			&ip, &ns, &m.VCPUs, &m.MemoryMB, &m.StorageMB,
			&netName, &createdAt, &lastChange, &imageDigest); err != nil {
			h.logger.Warn("scan machine row failed", zap.Error(err))
			continue
		}
		m.MaintenanceOperation = maintOp.String
		m.Image = image.String
		m.IPAddress = ip.String
		m.Namespace = ns.String
		m.NetworkName = netName.String
		m.CreatedAt = createdAt.String
		m.LastStateChange = lastChange.String
		m.PID = pidMap[m.ID]
		m.ImageDigest = imageDigest
		m.PortRules = h.queryPortRules(m.ID)
		result = append(result, m)
	}
	if result == nil {
		result = []machineV1Response{} // return [] not null
	}
	return result, rows.Err()
}

func (h *DashboardAPI) queryPortRules(machineID string) []portRuleEntry {
	if h.db == nil {
		return []portRuleEntry{}
	}
	rows, err := h.db.Query("SELECT host_port, machine_port, protocol FROM port_rule WHERE machine_id = ?", machineID)
	if err != nil {
		return []portRuleEntry{}
	}
	defer rows.Close()
	var rules []portRuleEntry
	for rows.Next() {
		var r portRuleEntry
		if rows.Scan(&r.HostPort, &r.MachinePort, &r.Protocol) == nil {
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

	// Machine stats from DB
	var total, running, stopped, errored int
	var vcpusAlloc, memAlloc int
	dbOK := true
	if h.db != nil {
		for _, q := range []struct {
			sql  string
			dest *int
		}{
			{"SELECT COUNT(*) FROM machine WHERE status != 'deleted'", &total},
			{"SELECT COUNT(*) FROM machine WHERE status = 'running'", &running},
			{"SELECT COUNT(*) FROM machine WHERE status = 'stopped'", &stopped},
			{"SELECT COUNT(*) FROM machine WHERE status IN ('error','failed','unhealthy')", &errored},
			{"SELECT COALESCE(SUM(vcpus),0) FROM machine WHERE status = 'running'", &vcpusAlloc},
			{"SELECT COALESCE(SUM(memory_mb),0) FROM machine WHERE status = 'running'", &memAlloc},
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
