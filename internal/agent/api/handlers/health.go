package handlers

import (
	"net/http"
	"os"
	"runtime"

	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/network"
	"github.com/davidestf/sistemo/internal/agent/vm"
	"go.uber.org/zap"
)

type Health struct {
	mgr    *vm.Manager
	cfg    *config.Config
	logger *zap.Logger
}

func NewHealth(mgr *vm.Manager, cfg *config.Config, logger *zap.Logger) *Health {
	return &Health{mgr: mgr, cfg: cfg, logger: logger}
}

func (h *Health) Health(w http.ResponseWriter, r *http.Request) {
	firecrackerOK := true
	if _, err := os.Stat(h.cfg.FirecrackerBin); os.IsNotExist(err) {
		firecrackerOK = false
	}
	kernelOK := true
	if _, err := os.Stat(h.cfg.KernelImagePath); os.IsNotExist(err) {
		kernelOK = false
	}

	runningVMs := len(h.mgr.ListVMs())

	status := "healthy"
	if !firecrackerOK || !kernelOK {
		status = "degraded"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": status,
		"checks": map[string]bool{"firecracker": firecrackerOK, "kernel": kernelOK},
		"stats":  map[string]int{"running_vms": runningVMs},
	})
}

func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	if _, err := os.Stat(h.cfg.FirecrackerBin); os.IsNotExist(err) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"ready":false,"reason":"firecracker binary not found"}`))
		return
	}
	if _, err := os.Stat(h.cfg.KernelImagePath); os.IsNotExist(err) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"ready":false,"reason":"kernel not found"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ready":true}`))
}

func (h *Health) Info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"architecture":   "namespace-isolation",
		"vm_ip":          network.VMIP,
		"gateway_ip":     network.GatewayIP,
		"host_interface": h.cfg.HostInterface,
		"go_version":     runtime.Version(),
		"arch":           runtime.GOARCH,
		"goroutines":     runtime.NumGoroutine(),
	})
}
