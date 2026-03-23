// Package api sets up the HTTP router and middleware.
package api

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/davidestf/sistemo/internal/agent/api/handlers"
	agentmw "github.com/davidestf/sistemo/internal/agent/api/middleware"
	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/vm"
	"go.uber.org/zap"
)

// NewRouter builds the HTTP router. db may be nil; when set, the terminal handler uses it to resolve VM namespace after daemon restart.
func NewRouter(cfg *config.Config, mgr *vm.Manager, logger *zap.Logger, db *sql.DB) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	if cfg.RateLimitRPS > 0 {
		rl := agentmw.NewIPRateLimiter(float64(cfg.RateLimitRPS), cfg.RateLimitBurst)
		r.Use(rl.Middleware())
	}
	r.Use(agentmw.Logger(logger))
	r.Use(middleware.Recoverer)

	// API key auth (skip health endpoints). Optional for self-hosted local use.
	if cfg.APIKey != "" {
		r.Use(agentmw.APIKeyAuth(cfg.APIKey))
	} else {
		logger.Warn("HOST_API_KEY unset — API is unauthenticated (suitable for localhost only)")
	}

	// Handler groups
	vmHandler := handlers.NewVM(mgr, cfg, logger, db)
	terminalHandler := handlers.NewTerminal(mgr, cfg, logger, db)
	healthHandler := handlers.NewHealth(mgr, cfg, logger)
	dashboardHandler := handlers.NewDashboard(db, logger)
	networkHandler := handlers.NewNetwork(logger, db)

	// Routes (GET and HEAD so curl -I works)
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

	r.Get("/dashboard", dashboardHandler.Dashboard)

	return r
}
