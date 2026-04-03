package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/agent/api"
	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/network"
	"github.com/davidestf/sistemo/internal/agent/machine"
	"github.com/davidestf/sistemo/internal/db"
)

func resolveFirecrackerBin(dataDir string) string {
	// 1. Explicit env var
	if p := os.Getenv("FIRECRACKER_BINARY_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// 2. ~/.sistemo/bin/firecracker (installed by 'sistemo install')
	if dataDir != "" {
		p := filepath.Join(dataDir, "bin", "firecracker")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// 3. ./firecracker-bin/ (for development / repo checkout)
	tryDir := func(base string) string {
		// Direct path
		candidate := filepath.Join(base, "firecracker-bin", "firecracker")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		// Search subdirectories (release-vX.Y.Z-arch/)
		binDir := filepath.Join(base, "firecracker-bin")
		entries, err := os.ReadDir(binDir)
		if err != nil {
			return ""
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sub, _ := os.ReadDir(filepath.Join(binDir, e.Name()))
			for _, s := range sub {
				if s.IsDir() || strings.HasSuffix(s.Name(), ".debug") {
					continue
				}
				if strings.HasPrefix(s.Name(), "firecracker") {
					p := filepath.Join(binDir, e.Name(), s.Name())
					if info, err := os.Stat(p); err == nil && info.Mode()&0111 != 0 {
						return p
					}
				}
			}
		}
		return ""
	}
	if cwd, err := os.Getwd(); err == nil {
		if p := tryDir(cwd); p != "" {
			return p
		}
	}

	// 4. System PATH
	if p, err := exec.LookPath("firecracker"); err == nil {
		return p
	}

	return ""
}

func detectDefaultRouteInterface() string {
	out, err := exec.Command("ip", "-4", "route", "show", "default").Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	fields := strings.Fields(line)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "dev" {
			return fields[i+1]
		}
	}
	return ""
}

func findKernelInDir(dir string) string {
	matches, err := filepath.Glob(filepath.Join(dir, "vmlinux*"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	abs, _ := filepath.Abs(matches[0])
	return abs
}

func runDaemon(logger *zap.Logger, dataDir string) error {
	dataDir = getDataDir(dataDir)
	if syscall.Geteuid() == 0 && os.Getenv("SUDO_USER") != "" {
		rootSistemo := filepath.Join("/root", ".sistemo")
		if dataDir == "" || dataDir == rootSistemo {
			if home, ok := os.LookupEnv("SUDO_HOME"); ok && home != "" {
				dataDir = filepath.Join(home, ".sistemo")
			} else {
				dataDir = filepath.Join("/home", os.Getenv("SUDO_USER"), ".sistemo")
			}
			logger.Info("using invoking user's data dir (for SSH key)", zap.String("data_dir", dataDir))
		}
	}
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".sistemo")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	// Resolve paths that will be set on the config struct after loading.
	vmBaseDir := filepath.Join(dataDir, "vms")
	imageCacheDir := filepath.Join(dataDir, "images")
	sshDir := filepath.Join(dataDir, "ssh")
	sshKeyPath := filepath.Join(sshDir, "sistemo_key")
	if err := generateSSHKeyWithLock(sshDir, sshKeyPath); err != nil {
		return fmt.Errorf("generate SSH key: %w", err)
	}
	// When running via sudo, chown the entire data directory to the invoking user
	// so that non-sudo CLI commands (vm deploy, vm list, image build) can access the DB and files.
	if syscall.Geteuid() == 0 && os.Getenv("SUDO_UID") != "" {
		var uid, gid int
		if _, err := fmt.Sscanf(os.Getenv("SUDO_UID"), "%d", &uid); err == nil {
			if _, err := fmt.Sscanf(os.Getenv("SUDO_GID"), "%d", &gid); err == nil {
				filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return nil
					}
					_ = os.Chown(path, uid, gid)
					return nil
				})
			}
		}
	}

	// Resolve kernel path: explicit env var > data dir > project dir
	kernelPath := os.Getenv("KERNEL_IMAGE_PATH")
	if kernelPath != "" {
		if _, err := os.Stat(kernelPath); err != nil {
			kernelPath = ""
		}
	}
	if kernelPath == "" {
		if k := findKernelInDir(filepath.Join(dataDir, "kernel")); k != "" {
			kernelPath = k
			logger.Info("using kernel from data dir", zap.String("path", k))
		}
	}
	if kernelPath == "" {
		if cwd, _ := os.Getwd(); cwd != "" {
			if k := findKernelInDir(filepath.Join(cwd, "kernel")); k != "" {
				kernelPath = k
				logger.Info("using kernel from project kernel/", zap.String("path", k))
			}
		}
	}

	// Resolve host interface
	hostInterface := os.Getenv("HOST_INTERFACE")
	if hostInterface == "" {
		if iface := detectDefaultRouteInterface(); iface != "" {
			hostInterface = iface
			logger.Info("auto-detected host interface for NAT", zap.String("interface", iface))
		}
	}

	// Resolve Firecracker binary path. Keep env var for child processes (Firecracker itself).
	fcBin := resolveFirecrackerBin(dataDir)
	if fcBin != "" {
		os.Setenv("FIRECRACKER_BINARY_PATH", fcBin)
		logger.Info("using Firecracker binary", zap.String("path", fcBin))
	}

	configFilePath := filepath.Join(dataDir, "config.yml")
	cfg, err := config.LoadWithFile(configFilePath)
	if err != nil {
		return fmt.Errorf("load agent config: %w", err)
	}
	if _, statErr := os.Stat(configFilePath); statErr == nil {
		logger.Info("loaded config file", zap.String("path", configFilePath))
	}

	// Override config fields directly with resolved paths (instead of going through env vars).
	cfg.DataDir = dataDir
	cfg.VMBaseDir = vmBaseDir
	cfg.ImageCacheDir = imageCacheDir
	cfg.SSHKeyPath = sshKeyPath
	if kernelPath != "" {
		cfg.KernelImagePath = kernelPath
	}
	if hostInterface != "" {
		cfg.HostInterface = hostInterface
	}
	if fcBin != "" {
		cfg.FirecrackerBin = fcBin
	}

	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	database, err := db.New(dataDir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	// Clean up VMs stuck in maintenance or failed (crashed mid-operation).
	// 'error' VMs are NOT cleaned — they have valid rootfs and need user attention.
	// 'stopped' VMs are NOT cleaned — they are restartable.
	{
		rows, _ := database.Query(`SELECT id FROM machine WHERE status IN ('maintenance', 'failed')`)
		if rows != nil {
			var staleIDs []string
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					staleIDs = append(staleIDs, id)
				}
			}
			rows.Close()
			for _, id := range staleIDs {
				machineDir := filepath.Join(dataDir, "vms", id)
				os.RemoveAll(machineDir)
				db.SafeExec(database, `DELETE FROM ip_allocation WHERE machine_id = ?`, id)
				db.SafeExec(database, `DELETE FROM port_rule WHERE machine_id = ?`, id)
				db.SafeExec(database, `UPDATE volume SET status='online', machine_id=NULL WHERE machine_id = ?`, id)
				db.SafeExec(database, `DELETE FROM machine WHERE id = ?`, id)
			}
			if len(staleIDs) > 0 {
				logger.Info("cleaned up failed machines from previous run", zap.Int("count", len(staleIDs)))
			}
		}
	}

	// Clean up stale nftables port rules for machines that no longer exist.
	// This handles the case where the daemon was killed with machines still running.
	{
		rows, _ := database.Query(`SELECT pr.machine_id, pr.host_port, pr.machine_port, pr.protocol
			FROM port_rule pr LEFT JOIN machine m ON pr.machine_id = m.id
			WHERE m.id IS NULL OR m.status = 'deleted'`)
		if rows != nil {
			var staleMachineIDs []string
			cleaned := 0
			for rows.Next() {
				var machineID string
				var hostPort, machinePort int
				var protocol string
				if rows.Scan(&machineID, &hostPort, &machinePort, &protocol) == nil {
					machineIP := network.GetAllocatedIP(database, machineID)
					if machineIP != "" {
						// Read bridge from vm_spec.json if it exists
						var bridge string
						specPath := filepath.Join(dataDir, "vms", machineID, "vm_spec.json")
						if specData, err := os.ReadFile(specPath); err == nil {
							var spec struct{ NetworkBridge string `json:"network_bridge"` }
							json.Unmarshal(specData, &spec)
							bridge = spec.NetworkBridge
						}
						n := network.NewVMNetwork(machineID, machineIP, logger, bridge)
						n.UnexposePort(cfg.HostInterface, hostPort, machinePort, protocol)
					}
					staleMachineIDs = append(staleMachineIDs, machineID)
					cleaned++
				}
			}
			rows.Close()
			for _, id := range staleMachineIDs {
				db.SafeExec(database, `DELETE FROM port_rule WHERE machine_id = ?`, id)
			}
			if cleaned > 0 {
				logger.Info("cleaned up stale port rules from previous run", zap.Int("count", cleaned))
			}
		}
	}

	// Clean up stale ip_allocation rows for deleted or missing machines.
	// Keep IPs for running, stopped, AND error machines (all are restartable).
	db.SafeExec(database, `DELETE FROM ip_allocation WHERE machine_id NOT IN (SELECT id FROM machine WHERE status IN ('running', 'stopped', 'error'))`)

	if syscall.Geteuid() != 0 {
		logger.Warn("daemon running as non-root — VM create will fail (mount/namespace need root). Stop and run: sudo ./sistemo up")
	}
	jwtSecret, err := db.GetJWTSecret(database)
	if err != nil {
		return fmt.Errorf("get JWT secret: %w", err)
	}

	mgr := machine.NewManager(context.Background(), cfg, logger, database)
	router := api.NewRouter(cfg, mgr, logger, database, []byte(jwtSecret), api.RouterOpts{
		BuildScript:  embeddedBuildScript,
		VMInitScript: embeddedVMInit,
	})
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("daemon listening", zap.Int("port", cfg.Port), zap.String("data_dir", dataDir))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("server error: %w", err)
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
	case err := <-errCh:
		return err
	}
	logger.Info("shutting down gracefully (waiting up to 10s for connections to drain)")
	mgr.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
	}
	logger.Info("daemon stopped")
	return nil
}
