// Package api sets up the HTTP router and middleware.
package api

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/davidestf/sistemo/internal/agent/api/handlers"
	agentmw "github.com/davidestf/sistemo/internal/agent/api/middleware"
	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/vm"
	dbpkg "github.com/davidestf/sistemo/internal/db"
	"go.uber.org/zap"
)

// RouterOpts holds optional configuration for NewRouter.
type RouterOpts struct {
	BuildScript  []byte // embedded build-rootfs.sh for Docker image builds
	VMInitScript []byte // embedded vm-init.sh for Docker image builds
}

// NewRouter builds the HTTP router with authentication middleware.
// jwtSecret is the HMAC key for signing/verifying dashboard JWTs.
func NewRouter(cfg *config.Config, mgr *vm.Manager, logger *zap.Logger, db *sql.DB, jwtSecret []byte, opts ...RouterOpts) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(agentmw.SaveOriginalRemoteAddr) // Must run BEFORE RealIP
	r.Use(middleware.RealIP)
	if cfg.RateLimitRPS > 0 {
		rl := agentmw.NewIPRateLimiter(float64(cfg.RateLimitRPS), cfg.RateLimitBurst)
		r.Use(rl.Middleware())
	}
	r.Use(agentmw.Logger(logger))
	r.Use(middleware.Recoverer)

	// Authentication: API key (CLI/scripts) + JWT (dashboard) coexist.
	// When no API key is set AND no admin account exists, the system is fully open.
	var adminExistsCache atomic.Bool
	adminExistsFn := func() bool {
		if adminExistsCache.Load() {
			return true
		}
		exists, err := dbpkg.AdminExists(db)
		if err != nil {
			// Fail-closed: if DB is broken, assume admin exists (require auth)
			logger.Warn("AdminExists check failed, assuming admin exists", zap.Error(err))
			return true
		}
		if exists {
			adminExistsCache.Store(true)
		}
		return exists
	}
	adminExistsFn() // prime cache

	r.Use(agentmw.DashboardAuth(cfg.APIKey, jwtSecret, adminExistsFn))

	if cfg.APIKey == "" {
		logger.Warn("HOST_API_KEY unset — API key auth disabled (dashboard auth active once admin account is created)")
	}

	// Handler groups
	vmHandler := handlers.NewVM(mgr, cfg, logger, db)
	terminalHandler := handlers.NewTerminal(mgr, cfg, logger, db)
	healthHandler := handlers.NewHealth(mgr, cfg, logger)
	networkHandler := handlers.NewNetwork(logger, db)
	volumesDir := filepath.Join(filepath.Dir(cfg.VMBaseDir), "volumes")
	volumeHandler := handlers.NewVolume(logger, db, volumesDir)
	dashAPI := handlers.NewDashboardAPI(mgr, cfg, db, logger)
	if len(opts) > 0 {
		dashAPI.BuildScript = opts[0].BuildScript
		dashAPI.VMInitScript = opts[0].VMInitScript
	}

	sessionTTL := time.Duration(cfg.SessionTimeoutHours) * time.Hour
	authHandler := handlers.NewAuth(db, logger, jwtSecret, sessionTTL, func() {
		adminExistsCache.Store(true)
	})

	// Routes
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"service":"sistemo","docs":"/health /vms /terminals/vm/:id","dashboard":"/dashboard"}`))
	})
	r.Head("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/health", healthHandler.Health)
	r.Get("/ready", healthHandler.Ready)
	r.Get("/info", healthHandler.Info)

	r.Post("/vms", vmHandler.Create)
	r.Delete("/vms/{vmID}", vmHandler.Delete)
	r.Post("/vms/{vmID}/stop", vmHandler.Stop)
	r.Post("/vms/{vmID}/start", vmHandler.Start)
	r.Get("/vms/{vmID}/ip", vmHandler.GetIP)
	r.Get("/vms/{vmID}/logs", vmHandler.Logs)
	r.Get("/vms", vmHandler.List)
	r.Post("/vms/{vmID}/exec", vmHandler.Exec)
	r.Post("/vms/{vmID}/expose", vmHandler.Expose)
	r.Delete("/vms/{vmID}/expose/{hostPort}", vmHandler.Unexpose)

	r.Get("/terminals/vm/{vmID}", terminalHandler.TerminalPageOrWebSocket)
	r.Post("/vms/{vmID}/terminal-session", terminalHandler.CreateSession)

	r.Post("/networks", networkHandler.Create)
	r.Get("/networks", networkHandler.List)
	r.Delete("/networks/{name}", networkHandler.Delete)

	r.Get("/volumes", volumeHandler.List)
	r.Post("/volumes", volumeHandler.Create)
	r.Get("/volumes/{idOrName}", volumeHandler.Get)
	r.Delete("/volumes/{idOrName}", volumeHandler.Delete)
	r.Post("/volumes/{idOrName}/resize", volumeHandler.Resize)
	r.Post("/vms/{vmID}/volume/attach", volumeHandler.Attach)
	r.Post("/vms/{vmID}/volume/detach", volumeHandler.Detach)

	// Dashboard SPA — embedded Svelte app
	dashSPA := DashboardHandler()
	r.Get("/dashboard", http.RedirectHandler("/dashboard/", http.StatusMovedPermanently).ServeHTTP)
	r.Get("/dashboard/", dashSPA)
	r.Get("/dashboard/*", dashSPA)

	// Dashboard API v1
	r.Route("/api/v1", func(r chi.Router) {
		// Auth endpoints (public — skipped by DashboardAuth middleware)
		r.Get("/auth/status", authHandler.Status)
		r.Post("/auth/setup", authHandler.Setup)
		r.Post("/auth/login", authHandler.Login)

		// Protected endpoints — reads (dashboard-enriched responses)
		r.Get("/vms", dashAPI.ListVMs)
		r.Get("/vms/{vmID}", dashAPI.GetVM)
		r.Get("/system", dashAPI.System)
		r.Get("/audit-log", dashAPI.AuditLog)
		r.Get("/images", dashAPI.Images)
		r.Get("/volumes", dashAPI.Volumes)
		r.Get("/networks", dashAPI.Networks)
		r.Get("/registry", dashAPI.Registry)
		r.Post("/registry/download", dashAPI.RegistryDownload)
		r.Delete("/images/{name}", dashAPI.ImageDelete)
		r.Post("/images/build", dashAPI.ImageBuild)
		r.Get("/images/build/{name}/status", dashAPI.ImageBuildStatus)
		r.Post("/images/build/{name}/cancel", dashAPI.ImageBuildCancel)
		r.Get("/images/builds", dashAPI.ImageBuilds)

		// Protected endpoints — mutations (same handlers as legacy routes)
		r.Post("/vms", vmHandler.Create)
		r.Delete("/vms/{vmID}", vmHandler.Delete)
		r.Post("/vms/{vmID}/stop", vmHandler.Stop)
		r.Post("/vms/{vmID}/start", vmHandler.Start)
		r.Get("/vms/{vmID}/logs", vmHandler.Logs)
		r.Post("/vms/{vmID}/expose", vmHandler.Expose)
		r.Delete("/vms/{vmID}/expose/{hostPort}", vmHandler.Unexpose)
		r.Post("/vms/{vmID}/volume/attach", volumeHandler.Attach)
		r.Post("/vms/{vmID}/volume/detach", volumeHandler.Detach)
		r.Post("/networks", networkHandler.Create)
		r.Delete("/networks/{name}", networkHandler.Delete)
		r.Post("/volumes", volumeHandler.Create)
		r.Delete("/volumes/{idOrName}", volumeHandler.Delete)
		r.Post("/volumes/{idOrName}/resize", volumeHandler.Resize)
	})

	return r
}
