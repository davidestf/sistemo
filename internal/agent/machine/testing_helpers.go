package machine

import (
	"go.uber.org/zap"
)

// NewTestManager creates a minimal Manager for use in tests outside the machine package.
// It has no real Firecracker capability but its ListMachines() returns an empty list
// and won't panic. Use this for handler tests that need a non-nil *Manager.
func NewTestManager() *Manager {
	return &Manager{
		logger:   zap.NewNop(),
		machines: make(map[string]*MachineInfo),
	}
}
