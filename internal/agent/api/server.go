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
	"github.com/davidestf/sistemo/internal/agent/machine"
	dbpkg "github.com/davidestf/sistemo/internal/db"
	"go.uber.org/zap"
)

// RouterOpts holds optional configuration for NewRouter.
type RouterOpts struct {
	BuildScript  []byte // embedded build-rootfs.sh for Docker image builds
	VMInitScript []byte // embedded vm-init.sh for Docker image builds
	TiniStatic   []byte // embedded tini static binary for PID 1 signal handling
}

// NewRouter builds the HTTP router with authentication middleware.
// jwtSecret is the HMAC key for signing/verifying dashboard JWTs.
func NewRouter(cfg *config.Config, mgr *machine.Manager, logger *zap.Logger, db *sql.DB, jwtSecret []byte, opts ...RouterOpts) http.Handler {
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
	machineHandler := handlers.NewMachine(mgr, cfg, logger, db)
	terminalHandler := handlers.NewTerminal(mgr, cfg, logger, db)
	healthHandler := handlers.NewHealth(mgr, cfg, logger)
	networkHandler := handlers.NewNetwork(logger, db)
	volumesDir := filepath.Join(filepath.Dir(cfg.VMBaseDir), "volumes")
	volumeHandler := handlers.NewVolume(logger, db, volumesDir)
	dashAPI := handlers.NewDashboardAPI(mgr, cfg, db, logger)
	if len(opts) > 0 {
		dashAPI.BuildScript = opts[0].BuildScript
		dashAPI.VMInitScript = opts[0].VMInitScript
		dashAPI.TiniStatic = opts[0].TiniStatic
	}

	sessionTTL := time.Duration(cfg.SessionTimeoutHours) * time.Hour
	authHandler := handlers.NewAuth(db, logger, jwtSecret, sessionTTL, func() {
		adminExistsCache.Store(true)
	})

	// Non-versioned routes (infrastructure, not API)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"service":"sistemo","api":"/api/v1/","dashboard":"/dashboard/"}`))
	})
	r.Head("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/health", healthHandler.Health)
	r.Get("/ready", healthHandler.Ready)
	r.Get("/info", healthHandler.Info)
	r.Get("/terminals/machine/{machineID}", terminalHandler.TerminalPageOrWebSocket)

	// Legacy routes — deprecated, will be removed in v2.0.
	// Clients should migrate to /api/v1/ equivalents.
	r.Post("/machines", deprecatedRoute(machineHandler.Create))
	r.Delete("/machines/{machineID}", deprecatedRoute(machineHandler.Delete))
	r.Post("/machines/{machineID}/stop", deprecatedRoute(machineHandler.Stop))
	r.Post("/machines/{machineID}/start", deprecatedRoute(machineHandler.Start))
	r.Get("/machines/{machineID}/ip", deprecatedRoute(machineHandler.GetIP))
	r.Get("/machines/{machineID}/logs", deprecatedRoute(machineHandler.Logs))
	r.Get("/machines", deprecatedRoute(machineHandler.List))
	r.Post("/machines/{machineID}/exec", deprecatedRoute(machineHandler.Exec))
	r.Post("/machines/{machineID}/expose", deprecatedRoute(machineHandler.Expose))
	r.Delete("/machines/{machineID}/expose/{hostPort}", deprecatedRoute(machineHandler.Unexpose))
	r.Post("/machines/{machineID}/terminal-session", deprecatedRoute(terminalHandler.CreateSession))
	r.Post("/networks", deprecatedRoute(networkHandler.Create))
	r.Get("/networks", deprecatedRoute(networkHandler.List))
	r.Delete("/networks/{name}", deprecatedRoute(networkHandler.Delete))
	r.Get("/volumes", deprecatedRoute(volumeHandler.List))
	r.Post("/volumes", deprecatedRoute(volumeHandler.Create))
	r.Get("/volumes/{idOrName}", deprecatedRoute(volumeHandler.Get))
	r.Delete("/volumes/{idOrName}", deprecatedRoute(volumeHandler.Delete))
	r.Post("/volumes/{idOrName}/resize", deprecatedRoute(volumeHandler.Resize))
	r.Post("/machines/{machineID}/volume/attach", deprecatedRoute(volumeHandler.Attach))
	r.Post("/machines/{machineID}/volume/detach", deprecatedRoute(volumeHandler.Detach))

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
		r.Get("/machines", dashAPI.ListMachines)
		r.Get("/machines/{machineID}", dashAPI.GetMachine)
		r.Get("/system", dashAPI.System)
		r.Get("/audit-log", dashAPI.AuditLog)
		r.Get("/images", dashAPI.Images)
		r.Get("/volumes", dashAPI.Volumes)
		r.Get("/networks", dashAPI.Networks)
		r.Get("/registry", dashAPI.Registry)
		r.Post("/registry/download", dashAPI.RegistryDownload)
		r.Delete("/images/{name}", dashAPI.ImageDelete)
		r.Post("/images/build", dashAPI.ImageBuild)
		r.Post("/images/build/dockerfile", dashAPI.DockerfileBuild)
		r.Get("/images/build/{name}/status", dashAPI.ImageBuildStatus)
		r.Get("/images/build/{id}/logs", dashAPI.ImageBuildLogs)
		r.Post("/images/build/{name}/cancel", dashAPI.ImageBuildCancel)
		r.Get("/images/builds", dashAPI.ImageBuilds)

		// Protected endpoints — mutations
		r.Post("/machines", machineHandler.Create)
		r.Delete("/machines/{machineID}", machineHandler.Delete)
		r.Post("/machines/{machineID}/stop", machineHandler.Stop)
		r.Post("/machines/{machineID}/start", machineHandler.Start)
		r.Get("/machines/{machineID}/ip", machineHandler.GetIP)
		r.Get("/machines/{machineID}/logs", machineHandler.Logs)
		r.Post("/machines/{machineID}/exec", machineHandler.Exec)
		r.Post("/machines/{machineID}/expose", machineHandler.Expose)
		r.Delete("/machines/{machineID}/expose/{hostPort}", machineHandler.Unexpose)
		r.Post("/machines/{machineID}/terminal-session", terminalHandler.CreateSession)
		r.Post("/machines/{machineID}/volume/attach", volumeHandler.Attach)
		r.Post("/machines/{machineID}/volume/detach", volumeHandler.Detach)
		r.Post("/networks", networkHandler.Create)
		r.Delete("/networks/{name}", networkHandler.Delete)
		r.Get("/volumes/{idOrName}", volumeHandler.Get)
		r.Post("/volumes", volumeHandler.Create)
		r.Delete("/volumes/{idOrName}", volumeHandler.Delete)
		r.Post("/volumes/{idOrName}/resize", volumeHandler.Resize)
	})

	return r
}

// deprecatedRoute wraps a handler with HTTP deprecation headers (RFC 8594).
// Legacy routes remain functional but signal they will be removed in v2.0.
func deprecatedRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Sunset", "Sat, 01 Jan 2028 00:00:00 GMT")
		w.Header().Set("Link", `</api/v1/>; rel="successor-version"`)
		next(w, r)
	}
}
