package agent

import "time"

const (
	// Reconciler
	DefaultReconcilerInterval = 30 * time.Second

	// SSH readiness
	DefaultSSHTimeout  = 20 * time.Second
	SSHDialTimeout     = 200 * time.Millisecond
	SSHPollInterval    = 50 * time.Millisecond

	// Firecracker process readiness
	DefaultFCReadinessTimeout = 2 * time.Second
	FCReadinessPollInterval   = 10 * time.Millisecond

	// Download retry
	DefaultMaxDownloadRetries = 3
	DefaultDownloadBaseDelay  = 1 * time.Second
	MaxDownloadBackoff        = 30 * time.Second
)
