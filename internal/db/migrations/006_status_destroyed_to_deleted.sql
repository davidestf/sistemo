-- Rename status 'destroyed' to 'deleted' for consistency with CLI (sistemo vm delete).
-- SQLite CHECK constraints block UPDATE, so we rebuild the table.
-- Note: network_id column is handled separately (added by addColumnIfMissing after migrations).
-- A Go post-migration step restores network_id data from the temp table if it exists.

CREATE TABLE vm_new (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'maintenance' CHECK (status IN ('maintenance', 'running', 'stopped', 'deleted', 'error', 'failed', 'unhealthy')),
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

INSERT INTO vm_new (id, name, status, maintenance_operation, image, ip_address, namespace, rootfs_path, vcpus, memory_mb, storage_mb, created_at, last_state_change, expires_at)
SELECT id, name,
    CASE WHEN status = 'destroyed' THEN 'deleted' ELSE status END,
    maintenance_operation, image, ip_address, namespace, rootfs_path,
    vcpus, memory_mb, storage_mb, created_at, last_state_change, expires_at
FROM vm;

DROP TABLE vm;
ALTER TABLE vm_new RENAME TO vm;

CREATE INDEX idx_vm_status ON vm(status);
CREATE INDEX idx_vm_last_state_change ON vm(last_state_change) WHERE status = 'maintenance';
CREATE UNIQUE INDEX idx_vm_name_active ON vm(name) WHERE status NOT IN ('deleted');
