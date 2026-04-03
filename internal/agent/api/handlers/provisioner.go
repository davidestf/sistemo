package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/machine"
	"github.com/davidestf/sistemo/internal/db"
)

// ProvisionError wraps an error with an HTTP status code for the handler.
type ProvisionError struct {
	Status  int
	Message string
	Err     error
}

func (e *ProvisionError) Error() string { return e.Message }
func (e *ProvisionError) Unwrap() error { return e.Err }

func provErr(status int, msg string) *ProvisionError {
	return &ProvisionError{Status: status, Message: msg}
}

// provisionState tracks resources allocated during provisioning for rollback.
type provisionState struct {
	machineID         string
	name              string
	rootVolID         string
	rootVolPath       string
	rootVolIsNew      bool // true = we created it, false = user's existing volume
	dataVolIDs        []string
	networkID         string
	imageForDB        string
	effectiveVCPUs    int
	effectiveMemoryMB int
}

// MachineProvisioner handles the multi-phase machine creation process.
// Each phase has a clear responsibility and the rollback function
// cleans up everything that was allocated up to the point of failure.
type MachineProvisioner struct {
	db     *sql.DB
	mgr    *machine.Manager
	cfg    *config.Config
	logger *zap.Logger
}

// Provision runs the full create pipeline: validate → allocate → create → finalize.
func (p *MachineProvisioner) Provision(ctx context.Context, req createMachineRequest) (*machine.CreateResponse, error) {
	state := &provisionState{}

	if err := p.validate(&req, state); err != nil {
		return nil, err
	}

	if err := p.allocateResources(&req, state); err != nil {
		p.rollback(state)
		return nil, err
	}

	createReq := p.buildCreateRequest(&req, state)

	if err := p.resolveAttachedStorage(&req, state, createReq); err != nil {
		p.rollback(state)
		return nil, err
	}

	// Mark data volumes as maintenance before create
	now := time.Now().UTC().Format(time.RFC3339)
	for _, volID := range state.dataVolIDs {
		db.SafeExec(p.db, "UPDATE volume SET status='maintenance', machine_id=?, last_state_change=? WHERE id=?",
			state.machineID, now, volID)
	}

	result, err := p.mgr.Create(ctx, createReq)
	if err != nil {
		p.rollback(state)
		db.LogAction(p.db, "create", "machine", state.machineID, state.name, err.Error(), false)
		return nil, p.userFacingError(err)
	}

	p.finalize(result, state, &req)
	return result, nil
}

// validate checks input constraints. No side effects — nothing to roll back.
func (p *MachineProvisioner) validate(req *createMachineRequest, state *provisionState) error {
	if strings.TrimSpace(req.Image) == "" && strings.TrimSpace(req.RootVolume) == "" {
		return provErr(400, "image or root_volume is required")
	}

	// Machine ID
	if req.MachineID != nil && *req.MachineID != "" {
		state.machineID = *req.MachineID
	} else {
		state.machineID = uuid.NewString()
	}
	if !isValidSafeID(state.machineID) {
		return provErr(400, "invalid machine_id: must be alphanumeric, hyphens, underscores, or dots")
	}

	// Resources
	state.effectiveVCPUs = ifZero(req.VCPUs, 1)
	state.effectiveMemoryMB = ifZero(req.MemoryMB, 512)

	maxVCPUs := ifZero(p.cfg.MaxVCPUs, 64)
	maxMemoryMB := ifZero(p.cfg.MaxMemoryMB, 262144)
	maxStorageMB := ifZero(p.cfg.MaxStorageMB, 102400)

	if state.effectiveVCPUs < 1 || state.effectiveVCPUs > maxVCPUs {
		return provErr(400, fmt.Sprintf("vcpus must be 1..%d", maxVCPUs))
	}
	if state.effectiveMemoryMB < 128 || state.effectiveMemoryMB > maxMemoryMB {
		return provErr(400, fmt.Sprintf("memory_mb must be 128..%d", maxMemoryMB))
	}
	if req.StorageMB > 0 && req.StorageMB > maxStorageMB {
		return provErr(400, fmt.Sprintf("storage_mb exceeds max %d", maxStorageMB))
	}

	// Name
	state.name = req.Name
	if state.name != "" && (len(state.name) > 128 || !isValidSafeID(state.name)) {
		return provErr(400, "invalid name: must be 1-128 alphanumeric, hyphens, underscores, or dots")
	}
	if state.name == "" {
		state.name = strings.TrimSuffix(filepath.Base(req.Image), ".rootfs.ext4")
		state.name = strings.TrimSuffix(state.name, ".ext4")
		if idx := strings.LastIndex(state.name, "/"); idx >= 0 {
			state.name = state.name[idx+1:]
		}
	}
	if state.name == "" || !isValidSafeID(state.name) {
		return provErr(400, "could not derive a valid name from image path — provide --name explicitly")
	}

	// Network
	if req.NetworkName != "" && req.NetworkName != "default" && p.db != nil {
		var bridgeName, subnet, netID string
		err := p.db.QueryRow("SELECT id, bridge_name, subnet FROM network WHERE name = ?", req.NetworkName).Scan(&netID, &bridgeName, &subnet)
		if err == sql.ErrNoRows {
			return provErr(404, fmt.Sprintf("network %q not found", req.NetworkName))
		}
		if err != nil {
			return provErr(500, "failed to look up network")
		}
		req.NetworkBridge = bridgeName
		req.NetworkSubnet = subnet
		state.networkID = netID
	}

	state.imageForDB = req.Image
	if state.imageForDB == "" && req.RootVolume != "" {
		state.imageForDB = "volume:" + req.RootVolume
	}

	p.logger.Info("create machine request",
		zap.String("machine_id", state.machineID),
		zap.Int("vcpus", state.effectiveVCPUs),
		zap.Int("memory_mb", state.effectiveMemoryMB),
		zap.Int("storage_mb", req.StorageMB))

	return nil
}

// allocateResources creates DB records and resolves the root volume.
func (p *MachineProvisioner) allocateResources(req *createMachineRequest, state *provisionState) error {
	if p.db == nil {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Insert machine record
	_, err := p.db.Exec(
		`INSERT INTO machine (id, name, status, maintenance_operation, image, vcpus, memory_mb, storage_mb, network_id, created_at, last_state_change)
		 VALUES (?, ?, 'maintenance', 'creating', ?, ?, ?, ?, ?, ?, ?)`,
		state.machineID, state.name, state.imageForDB, state.effectiveVCPUs, state.effectiveMemoryMB, req.StorageMB, state.networkID, now, now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: machine.name") {
			return provErr(409, fmt.Sprintf("A machine named %q already exists. Use --name or delete the existing one.", state.name))
		}
		return provErr(500, "failed to create machine record")
	}

	// Root volume
	volumesDir := filepath.Join(filepath.Dir(p.cfg.VMBaseDir), "volumes")
	useExisting := strings.TrimSpace(req.RootVolume) != ""

	if useExisting {
		var volStatus string
		err := p.db.QueryRow("SELECT id, path, status FROM volume WHERE (id = ? OR name = ?) LIMIT 1",
			req.RootVolume, req.RootVolume).Scan(&state.rootVolID, &state.rootVolPath, &volStatus)
		if err == sql.ErrNoRows {
			return provErr(404, fmt.Sprintf("volume %q not found", req.RootVolume))
		}
		if err != nil {
			return provErr(500, "failed to look up volume")
		}
		if volStatus != "online" {
			return provErr(409, fmt.Sprintf("volume %q is %s, must be online", req.RootVolume, volStatus))
		}
		if _, err := os.Stat(state.rootVolPath); err != nil {
			return provErr(500, fmt.Sprintf("volume file missing on disk: %s", state.rootVolPath))
		}
		db.SafeExec(p.db, "UPDATE volume SET status='maintenance', machine_id=?, role='root', last_state_change=? WHERE id=?",
			state.machineID, now, state.rootVolID)
		state.rootVolIsNew = false
	} else {
		state.rootVolID = uuid.NewString()
		rootVolName := state.name + "-root"
		if err := os.MkdirAll(volumesDir, 0755); err != nil {
			return provErr(500, "failed to create volumes directory")
		}
		state.rootVolPath = filepath.Join(volumesDir, state.rootVolID+".ext4")
		_, err := p.db.Exec(
			`INSERT INTO volume (id, name, size_mb, path, status, role, machine_id, last_state_change)
			 VALUES (?, ?, ?, ?, 'maintenance', 'root', ?, ?)`,
			state.rootVolID, rootVolName, req.StorageMB, state.rootVolPath, state.machineID, now,
		)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: volume.name") {
				return provErr(409, fmt.Sprintf("A volume named %q already exists. Delete it first or use a different machine name.", rootVolName))
			}
			return provErr(500, fmt.Sprintf("insert root volume: %v", err))
		}
		state.rootVolIsNew = true
	}

	return nil
}

// buildCreateRequest maps validated state into the manager's CreateRequest.
func (p *MachineProvisioner) buildCreateRequest(req *createMachineRequest, state *provisionState) *machine.CreateRequest {
	return &machine.CreateRequest{
		MachineID:         state.machineID,
		Image:             req.Image,
		VCPUs:             state.effectiveVCPUs,
		MemoryMB:          state.effectiveMemoryMB,
		StorageMB:         req.StorageMB,
		RootVolumePath:    state.rootVolPath,
		UseExistingVolume: strings.TrimSpace(req.RootVolume) != "",
		AttachedStorage:   req.AttachedStorage,
		Metadata:          req.Metadata,
		InjectInitSSH:     req.InjectInitSSH,
		NetworkBridge:     req.NetworkBridge,
		NetworkSubnet:     req.NetworkSubnet,
	}
}

// resolveAttachedStorage resolves volume IDs/names to filesystem paths and validates status.
func (p *MachineProvisioner) resolveAttachedStorage(req *createMachineRequest, state *provisionState, createReq *machine.CreateRequest) error {
	if len(req.AttachedStorage) == 0 || p.db == nil {
		return nil
	}

	var resolvedPaths []string

	for _, idOrName := range req.AttachedStorage {
		var volID, volPath, volStatus string
		err := p.db.QueryRow("SELECT id, path, status FROM volume WHERE (id = ? OR name = ?) LIMIT 1",
			idOrName, idOrName).Scan(&volID, &volPath, &volStatus)

		if err == sql.ErrNoRows {
			if !strings.HasPrefix(idOrName, "/") {
				return provErr(404, fmt.Sprintf("volume %q not found", idOrName))
			}
			if _, statErr := os.Stat(idOrName); statErr != nil {
				return provErr(404, fmt.Sprintf("volume %q not found in database and path does not exist", idOrName))
			}
			resolvedPaths = append(resolvedPaths, idOrName)
			continue
		}
		if err != nil {
			return provErr(500, fmt.Sprintf("query volume %q: %v", idOrName, err))
		}
		if volStatus != "online" {
			return provErr(409, fmt.Sprintf("volume %q is already attached", idOrName))
		}

		resolvedPaths = append(resolvedPaths, volPath)
		state.dataVolIDs = append(state.dataVolIDs, volID)
	}

	createReq.AttachedStorage = resolvedPaths
	return nil
}

// finalize updates DB records after successful machine creation.
func (p *MachineProvisioner) finalize(result *machine.CreateResponse, state *provisionState, req *createMachineRequest) {
	if p.db == nil {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Mark root volume as attached
	db.SafeExec(p.db, "UPDATE volume SET status='attached', last_state_change=? WHERE id=?", now, state.rootVolID)
	db.SafeExec(p.db, "UPDATE machine SET root_volume=? WHERE id=?", state.rootVolID, state.machineID)

	// Mark data volumes as attached
	for _, volID := range state.dataVolIDs {
		db.SafeExec(p.db, "UPDATE volume SET status='attached', machine_id=?, last_state_change=? WHERE id=?",
			state.machineID, now, volID)
	}

	// Update machine status to running
	db.SafeExec(p.db, `UPDATE machine SET status='running', maintenance_operation=NULL, ip_address=?, namespace=?, last_state_change=? WHERE id=?`,
		result.IPAddress, result.Namespace, now, state.machineID)

	// Link image digest for provenance tracking
	var imageDigest string
	p.db.QueryRow("SELECT digest FROM image WHERE path = ?", req.Image).Scan(&imageDigest)
	if imageDigest == "" {
		imgName := filepath.Base(req.Image)
		imgName = strings.TrimSuffix(imgName, ".rootfs.ext4")
		imgName = strings.TrimSuffix(imgName, ".ext4")
		p.db.QueryRow("SELECT digest FROM image WHERE name = ? LIMIT 1", imgName).Scan(&imageDigest)
	}
	if imageDigest != "" {
		db.SafeExec(p.db, "UPDATE machine SET image_digest=? WHERE id=?", imageDigest, state.machineID)
	}

	db.LogAction(p.db, "create", "machine", state.machineID, state.name,
		fmt.Sprintf("image=%s vcpus=%d memory=%dMB", req.Image, state.effectiveVCPUs, state.effectiveMemoryMB), true)
}

// rollback cleans up all resources allocated by allocateResources.
// Safe to call at any point — only cleans what was actually allocated.
func (p *MachineProvisioner) rollback(state *provisionState) {
	if p.db == nil {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Clean up data volumes (reset to online)
	for _, volID := range state.dataVolIDs {
		db.SafeExec(p.db, "UPDATE volume SET status='online', machine_id=NULL, last_state_change=? WHERE id=?", now, volID)
	}

	// Clean up root volume
	if state.rootVolID != "" {
		if state.rootVolIsNew {
			p.db.Exec("DELETE FROM volume WHERE id=?", state.rootVolID)
			if state.rootVolPath != "" {
				os.Remove(state.rootVolPath)
			}
		} else {
			db.SafeExec(p.db, "UPDATE volume SET status='online', machine_id=NULL, role=NULL, last_state_change=? WHERE id=?", now, state.rootVolID)
		}
	}

	// Clean up machine record and associated resources
	if state.machineID != "" {
		db.SafeExec(p.db, "DELETE FROM ip_allocation WHERE machine_id=?", state.machineID)
		db.SafeExec(p.db, "DELETE FROM port_rule WHERE machine_id=?", state.machineID)
		db.SafeExec(p.db, "DELETE FROM machine WHERE id=?", state.machineID)
		machineDir := filepath.Join(p.cfg.VMBaseDir, state.machineID)
		os.RemoveAll(machineDir)
	}
}

// userFacingError converts internal errors to clean messages.
func (p *MachineProvisioner) userFacingError(err error) *ProvisionError {
	msg := err.Error()
	if strings.Contains(msg, "Bad magic number") || strings.Contains(msg, "not a valid ext4") {
		msg = "image is not a valid ext4 filesystem — check the file is a rootfs.ext4 image, not a compressed archive"
	} else if strings.Contains(msg, "e2fsck") || strings.Contains(msg, "resize2fs") {
		msg = "filesystem resize failed — the image may be corrupt or not ext4"
	} else if len(msg) > 200 {
		msg = msg[:200]
	}
	p.logger.Error("create machine failed", zap.Error(err))
	return &ProvisionError{Status: 500, Message: msg, Err: err}
}
