package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/machine"
	"github.com/davidestf/sistemo/internal/db"
)

// setupDashboardRouter creates a DashboardAPI with a real DB and test Manager.
func setupDashboardRouter(t *testing.T) (*chi.Mux, *sql.DB, *config.Config) {
	t.Helper()

	tmpDir := t.TempDir()
	database, err := db.New(tmpDir)
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := &config.Config{
		DataDir:       tmpDir,
		BridgeSubnet:  "10.200.0.0/16",
		MaxVCPUs:      64,
		MaxMemoryMB:   262144,
		MaxStorageMB:  1048576,
	}

	mgr := machine.NewTestManager()
	logger := zap.NewNop()
	api := NewDashboardAPI(mgr, cfg, database, logger)

	r := chi.NewRouter()
	r.Get("/api/v1/machines", api.ListMachines)
	r.Get("/api/v1/machines/{machineID}", api.GetMachine)
	r.Get("/api/v1/system", api.System)
	r.Get("/api/v1/audit", api.AuditLog)
	r.Get("/api/v1/images", api.Images)
	r.Delete("/api/v1/images/{name}", api.ImageDelete)
	r.Get("/api/v1/volumes", api.Volumes)
	r.Get("/api/v1/networks", api.Networks)

	return r, database, cfg
}

func dashboardGet(t *testing.T, router *chi.Mux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func dashboardDelete(t *testing.T, router *chi.Mux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func dashboardDecodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode JSON: %v (body: %s)", err, rec.Body.String())
	}
	return result
}

// insertTestMachine inserts a machine row into the database for testing.
func insertTestMachine(t *testing.T, database *sql.DB, id, name, status, image string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO machine (id, name, status, image, vcpus, memory_mb, storage_mb, created_at, last_state_change)
		 VALUES (?, ?, ?, ?, 2, 512, 2048, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, name, status, image,
	)
	if err != nil {
		t.Fatalf("insert test machine %q: %v", name, err)
	}
}

// insertTestAuditEntry inserts an audit log entry for testing.
func insertTestAuditEntry(t *testing.T, database *sql.DB, action, targetType, targetID, targetName, details string, success bool) {
	t.Helper()
	succ := 0
	if success {
		succ = 1
	}
	_, err := database.Exec(
		`INSERT INTO audit_log (timestamp, action, target_type, target_id, target_name, details, success)
		 VALUES (datetime('now'), ?, ?, ?, ?, ?, ?)`,
		action, targetType, targetID, targetName, details, succ,
	)
	if err != nil {
		t.Fatalf("insert audit entry: %v", err)
	}
}

// --- Machine List Tests ---

func TestListMachines_Empty(t *testing.T) {
	router, _, _ := setupDashboardRouter(t)

	rec := dashboardGet(t, router, "/api/v1/machines")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	machines, ok := body["machines"].([]interface{})
	if !ok {
		t.Fatalf("machines field is not an array: %v", body["machines"])
	}
	if len(machines) != 0 {
		t.Errorf("machines count = %d, want 0", len(machines))
	}
}

func TestListMachines_WithData(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	insertTestMachine(t, database, "m-001", "web-server", "running", "debian")
	insertTestMachine(t, database, "m-002", "db-server", "stopped", "ubuntu")

	rec := dashboardGet(t, router, "/api/v1/machines")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	machines, ok := body["machines"].([]interface{})
	if !ok {
		t.Fatalf("machines field is not an array: %v", body["machines"])
	}
	if len(machines) != 2 {
		t.Errorf("machines count = %d, want 2", len(machines))
	}

	// Check response shape of first machine
	first := machines[0].(map[string]interface{})
	requiredFields := []string{"id", "name", "status", "image", "vcpus", "memory_mb", "storage_mb", "created_at", "last_state_change", "port_rules", "pid", "network_name"}
	for _, field := range requiredFields {
		if _, exists := first[field]; !exists {
			t.Errorf("machine response missing field %q", field)
		}
	}
}

func TestListMachines_ExcludesDeleted(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	insertTestMachine(t, database, "m-001", "alive-machine", "running", "debian")
	insertTestMachine(t, database, "m-002", "dead-machine", "deleted", "debian")

	rec := dashboardGet(t, router, "/api/v1/machines")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	machines := body["machines"].([]interface{})
	if len(machines) != 1 {
		t.Errorf("machines count = %d, want 1 (deleted excluded)", len(machines))
	}
	m := machines[0].(map[string]interface{})
	if m["name"] != "alive-machine" {
		t.Errorf("machine name = %q, want alive-machine", m["name"])
	}
}

func TestGetMachine_Found(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	insertTestMachine(t, database, "m-001", "web-server", "running", "debian")

	rec := dashboardGet(t, router, "/api/v1/machines/m-001")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	if body["id"] != "m-001" {
		t.Errorf("id = %v, want m-001", body["id"])
	}
	if body["name"] != "web-server" {
		t.Errorf("name = %v, want web-server", body["name"])
	}
}

func TestGetMachine_NotFound(t *testing.T) {
	router, _, _ := setupDashboardRouter(t)

	rec := dashboardGet(t, router, "/api/v1/machines/nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestGetMachine_DeletedNotFound(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	insertTestMachine(t, database, "m-001", "deleted-machine", "deleted", "debian")

	rec := dashboardGet(t, router, "/api/v1/machines/m-001")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (deleted machine should not be found)", rec.Code, http.StatusNotFound)
	}
}

// --- System Tests ---

func TestSystem_ResponseShape(t *testing.T) {
	router, _, _ := setupDashboardRouter(t)

	rec := dashboardGet(t, router, "/api/v1/system")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)

	topLevelFields := []string{"health", "host", "daemon", "stats", "limits"}
	for _, field := range topLevelFields {
		if _, exists := body[field]; !exists {
			t.Errorf("system response missing top-level field %q", field)
		}
	}

	// Check health sub-fields
	health, ok := body["health"].(map[string]interface{})
	if !ok {
		t.Fatal("health is not an object")
	}
	if _, exists := health["status"]; !exists {
		t.Error("health missing 'status' field")
	}
	if _, exists := health["checks"]; !exists {
		t.Error("health missing 'checks' field")
	}

	// Check host sub-fields
	host, ok := body["host"].(map[string]interface{})
	if !ok {
		t.Fatal("host is not an object")
	}
	for _, f := range []string{"hostname", "cpus", "memory_mb"} {
		if _, exists := host[f]; !exists {
			t.Errorf("host missing field %q", f)
		}
	}

	// Check daemon sub-fields
	daemon, ok := body["daemon"].(map[string]interface{})
	if !ok {
		t.Fatal("daemon is not an object")
	}
	for _, f := range []string{"go_version", "arch", "goroutines"} {
		if _, exists := daemon[f]; !exists {
			t.Errorf("daemon missing field %q", f)
		}
	}

	// Check stats sub-fields
	stats, ok := body["stats"].(map[string]interface{})
	if !ok {
		t.Fatal("stats is not an object")
	}
	for _, f := range []string{"total", "running", "stopped", "errored", "vcpus_allocated", "memory_mb_allocated"} {
		if _, exists := stats[f]; !exists {
			t.Errorf("stats missing field %q", f)
		}
	}

	// Check limits sub-fields
	limits, ok := body["limits"].(map[string]interface{})
	if !ok {
		t.Fatal("limits is not an object")
	}
	for _, f := range []string{"max_vcpus", "max_memory_mb", "max_storage_mb"} {
		if _, exists := limits[f]; !exists {
			t.Errorf("limits missing field %q", f)
		}
	}
}

func TestSystem_StatsReflectMachines(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	insertTestMachine(t, database, "m-001", "m1", "running", "debian")
	insertTestMachine(t, database, "m-002", "m2", "running", "debian")
	insertTestMachine(t, database, "m-003", "m3", "stopped", "debian")
	insertTestMachine(t, database, "m-004", "m4", "error", "debian")
	insertTestMachine(t, database, "m-005", "m5", "deleted", "debian")

	rec := dashboardGet(t, router, "/api/v1/system")
	body := dashboardDecodeJSON(t, rec)

	stats := body["stats"].(map[string]interface{})
	// total excludes deleted
	if total := stats["total"].(float64); total != 4 {
		t.Errorf("stats.total = %v, want 4 (excludes deleted)", total)
	}
	if running := stats["running"].(float64); running != 2 {
		t.Errorf("stats.running = %v, want 2", running)
	}
	if stopped := stats["stopped"].(float64); stopped != 1 {
		t.Errorf("stats.stopped = %v, want 1", stopped)
	}
	if errored := stats["errored"].(float64); errored != 1 {
		t.Errorf("stats.errored = %v, want 1", errored)
	}
}

// --- Audit Log Tests ---

func TestAuditLog_Default(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	for i := 0; i < 5; i++ {
		insertTestAuditEntry(t, database, "machine.create", "machine", "m-id", "test-machine", "created", true)
	}

	rec := dashboardGet(t, router, "/api/v1/audit")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	entries := body["entries"].([]interface{})
	if len(entries) != 5 {
		t.Errorf("entries count = %d, want 5", len(entries))
	}
	total := body["total"].(float64)
	if total != 5 {
		t.Errorf("total = %v, want 5", total)
	}

	// Check entry shape
	entry := entries[0].(map[string]interface{})
	for _, f := range []string{"id", "timestamp", "action", "target_type", "target_id", "target_name", "details", "success"} {
		if _, exists := entry[f]; !exists {
			t.Errorf("audit entry missing field %q", f)
		}
	}
}

func TestAuditLog_Empty(t *testing.T) {
	router, _, _ := setupDashboardRouter(t)

	rec := dashboardGet(t, router, "/api/v1/audit")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	entries := body["entries"].([]interface{})
	if len(entries) != 0 {
		t.Errorf("entries count = %d, want 0", len(entries))
	}
	total := body["total"].(float64)
	if total != 0 {
		t.Errorf("total = %v, want 0", total)
	}
}

func TestAuditLog_Pagination(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	for i := 0; i < 20; i++ {
		insertTestAuditEntry(t, database, "machine.create", "machine", "m-id", "test-machine", "created", true)
	}

	// Request with offset=10, limit=5
	rec := dashboardGet(t, router, "/api/v1/audit?offset=10&limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	entries := body["entries"].([]interface{})
	if len(entries) != 5 {
		t.Errorf("entries count = %d, want 5 (with offset=10, limit=5)", len(entries))
	}
	total := body["total"].(float64)
	if total != 20 {
		t.Errorf("total = %v, want 20 (total count ignores pagination)", total)
	}
}

func TestAuditLog_FilterByAction(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	insertTestAuditEntry(t, database, "machine.create", "machine", "m-001", "m1", "created", true)
	insertTestAuditEntry(t, database, "machine.create", "machine", "m-002", "m2", "created", true)
	insertTestAuditEntry(t, database, "machine.delete", "machine", "m-003", "m3", "deleted", true)
	insertTestAuditEntry(t, database, "admin.login", "auth", "", "admin", "login", true)

	rec := dashboardGet(t, router, "/api/v1/audit?action=machine.create")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	entries := body["entries"].([]interface{})
	if len(entries) != 2 {
		t.Errorf("filtered entries = %d, want 2 (only machine.create)", len(entries))
	}
	total := body["total"].(float64)
	if total != 2 {
		t.Errorf("total = %v, want 2", total)
	}

	for _, e := range entries {
		entry := e.(map[string]interface{})
		if entry["action"] != "machine.create" {
			t.Errorf("entry action = %v, want machine.create", entry["action"])
		}
	}
}

// --- Image Tests ---

func TestImages_Empty(t *testing.T) {
	router, _, _ := setupDashboardRouter(t)

	rec := dashboardGet(t, router, "/api/v1/images")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	images := body["images"].([]interface{})
	if len(images) != 0 {
		t.Errorf("images count = %d, want 0", len(images))
	}
}

func TestImages_WithFiles(t *testing.T) {
	router, _, cfg := setupDashboardRouter(t)

	// Create fake image files
	imagesDir := filepath.Join(cfg.DataDir, "images")
	os.MkdirAll(imagesDir, 0755)
	os.WriteFile(filepath.Join(imagesDir, "debian.rootfs.ext4"), []byte("fake rootfs"), 0644)
	os.WriteFile(filepath.Join(imagesDir, "ubuntu.rootfs.ext4"), []byte("fake rootfs data here"), 0644)

	rec := dashboardGet(t, router, "/api/v1/images")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	images := body["images"].([]interface{})
	if len(images) != 2 {
		t.Errorf("images count = %d, want 2", len(images))
	}

	// Check shape
	img := images[0].(map[string]interface{})
	for _, f := range []string{"name", "file", "path", "size_mb", "created_at", "source"} {
		if _, exists := img[f]; !exists {
			t.Errorf("image entry missing field %q", f)
		}
	}
}

func TestImages_IgnoresNonExt4(t *testing.T) {
	router, _, cfg := setupDashboardRouter(t)

	imagesDir := filepath.Join(cfg.DataDir, "images")
	os.MkdirAll(imagesDir, 0755)
	os.WriteFile(filepath.Join(imagesDir, "debian.rootfs.ext4"), []byte("rootfs"), 0644)
	os.WriteFile(filepath.Join(imagesDir, "readme.txt"), []byte("notes"), 0644)
	os.MkdirAll(filepath.Join(imagesDir, "subdir"), 0755) // directories ignored

	rec := dashboardGet(t, router, "/api/v1/images")
	body := dashboardDecodeJSON(t, rec)
	images := body["images"].([]interface{})
	if len(images) != 1 {
		t.Errorf("images count = %d, want 1 (only .ext4 files)", len(images))
	}
}

func TestImageDelete_NotFound(t *testing.T) {
	router, _, _ := setupDashboardRouter(t)

	rec := dashboardDelete(t, router, "/api/v1/images/nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestImageDelete_PathTraversal(t *testing.T) {
	tests := []string{
		"../etc/passwd",
		"..%2fetc%2fpasswd",
		"..",
		"foo..bar",
		"../../../etc/shadow",
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			router, _, _ := setupDashboardRouter(t)
			rec := dashboardDelete(t, router, "/api/v1/images/"+name)
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
				t.Errorf("path traversal %q: status = %d, want 400 or 404", name, rec.Code)
			}
		})
	}
}

func TestImageDelete_Success(t *testing.T) {
	router, _, cfg := setupDashboardRouter(t)

	imagesDir := filepath.Join(cfg.DataDir, "images")
	os.MkdirAll(imagesDir, 0755)
	imgPath := filepath.Join(imagesDir, "debian.rootfs.ext4")
	os.WriteFile(imgPath, []byte("rootfs"), 0644)

	rec := dashboardDelete(t, router, "/api/v1/images/debian")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify file is removed
	if _, err := os.Stat(imgPath); !os.IsNotExist(err) {
		t.Error("image file should have been deleted")
	}
}

// Images are templates — machines get their own root volume copy.
// Deleting an image is always safe regardless of running machines.
// (Previously tested for 409 conflict but that check was removed.)

// --- Volume Tests ---

func TestVolumes_Empty(t *testing.T) {
	router, _, _ := setupDashboardRouter(t)

	rec := dashboardGet(t, router, "/api/v1/volumes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	volumes := body["volumes"].([]interface{})
	if len(volumes) != 0 {
		t.Errorf("volumes count = %d, want 0", len(volumes))
	}
}

func TestVolumes_WithData(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	database.Exec(`INSERT INTO volume (id, name, size_mb, path, status, created, last_state_change)
		VALUES ('vol-001', 'data-vol', 1024, '/tmp/vol', 'online', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)

	rec := dashboardGet(t, router, "/api/v1/volumes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	volumes := body["volumes"].([]interface{})
	if len(volumes) != 1 {
		t.Errorf("volumes count = %d, want 1", len(volumes))
	}

	vol := volumes[0].(map[string]interface{})
	if vol["name"] != "data-vol" {
		t.Errorf("volume name = %v, want data-vol", vol["name"])
	}
	if vol["role"] != "data" {
		t.Errorf("volume role = %v, want data (default)", vol["role"])
	}
}

// --- Network Tests ---

func TestNetworks_Default(t *testing.T) {
	router, _, _ := setupDashboardRouter(t)

	rec := dashboardGet(t, router, "/api/v1/networks")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := dashboardDecodeJSON(t, rec)
	networks := body["networks"].([]interface{})
	// At minimum the "default" network is always returned
	if len(networks) < 1 {
		t.Fatalf("networks count = %d, want at least 1 (default)", len(networks))
	}

	first := networks[0].(map[string]interface{})
	if first["name"] != "default" {
		t.Errorf("first network name = %v, want default", first["name"])
	}
	if first["subnet"] != "10.200.0.0/16" {
		t.Errorf("default subnet = %v, want 10.200.0.0/16", first["subnet"])
	}
}

func TestNetworks_WithCustom(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	database.Exec(`INSERT INTO network (id, name, subnet, bridge_name, created_at)
		VALUES ('net-001', 'backend', '10.201.0.0/24', 'sistemo1', '2026-01-01T00:00:00Z')`)

	rec := dashboardGet(t, router, "/api/v1/networks")
	body := dashboardDecodeJSON(t, rec)
	networks := body["networks"].([]interface{})
	if len(networks) != 2 {
		t.Errorf("networks count = %d, want 2 (default + backend)", len(networks))
	}
}

func TestNetworks_MachineCount(t *testing.T) {
	router, database, _ := setupDashboardRouter(t)

	// Insert machines on default network (no network_id)
	insertTestMachine(t, database, "m-001", "m1", "running", "debian")
	insertTestMachine(t, database, "m-002", "m2", "running", "debian")
	insertTestMachine(t, database, "m-003", "m3", "deleted", "debian") // deleted, shouldn't count

	rec := dashboardGet(t, router, "/api/v1/networks")
	body := dashboardDecodeJSON(t, rec)
	networks := body["networks"].([]interface{})
	defaultNet := networks[0].(map[string]interface{})
	machineCount := defaultNet["machine_count"].(float64)
	if machineCount != 2 {
		t.Errorf("default network machine_count = %v, want 2 (excludes deleted)", machineCount)
	}
}
