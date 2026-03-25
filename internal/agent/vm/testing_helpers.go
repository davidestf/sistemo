package vm

import (
	"go.uber.org/zap"
)

// NewTestManager creates a minimal Manager for use in tests outside the vm package.
// It has no real Firecracker capability but its ListVMs() returns an empty list
// and won't panic. Use this for handler tests that need a non-nil *Manager.
func NewTestManager() *Manager {
	return &Manager{
		logger: zap.NewNop(),
		vms:    make(map[string]*VMInfo),
	}
}
