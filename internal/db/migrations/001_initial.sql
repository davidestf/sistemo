-- Sistemo self-hosted: minimal schema for single-host VM state.

CREATE TABLE IF NOT EXISTS vm (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'maintenance' CHECK (status IN ('maintenance', 'running', 'stopped', 'destroyed', 'error', 'failed', 'unhealthy')),
    maintenance_operation TEXT,
    image TEXT NOT NULL,
    ip_address TEXT,
    namespace TEXT,
    rootfs_path TEXT,
    vcpus INTEGER NOT NULL DEFAULT 2,
    memory_mb INTEGER NOT NULL DEFAULT 512,
    storage_mb INTEGER NOT NULL DEFAULT 2048,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_state_change TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_vm_status ON vm(status);
CREATE INDEX IF NOT EXISTS idx_vm_last_state_change ON vm(last_state_change) WHERE status = 'maintenance';
CREATE UNIQUE INDEX IF NOT EXISTS idx_vm_name_active ON vm(name) WHERE status NOT IN ('destroyed');
