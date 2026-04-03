-- Rename vm → machine across all tables.
-- Uses proven recreate-copy-drop pattern from migration 006.
-- Note: network_id, root_volume, image_digest columns are handled by addColumnIfMissing
-- (Go post-migration step), so they are NOT included in this migration.

-- Phase 1: vm → machine
CREATE TABLE machine (
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

INSERT INTO machine (id, name, status, maintenance_operation, image, ip_address, namespace, rootfs_path, vcpus, memory_mb, storage_mb, created_at, last_state_change, expires_at)
SELECT id, name, status, maintenance_operation, image, ip_address, namespace, rootfs_path, vcpus, memory_mb, storage_mb, created_at, last_state_change, expires_at
FROM vm;

DROP TABLE vm;

CREATE INDEX idx_machine_status ON machine(status);
CREATE INDEX idx_machine_last_state_change ON machine(last_state_change) WHERE status = 'maintenance';
CREATE UNIQUE INDEX idx_machine_name_active ON machine(name) WHERE status NOT IN ('deleted');

-- Phase 2: port_rule — vm_id → machine_id, vm_port → machine_port
CREATE TABLE port_rule_new (
    id TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL,
    host_port INTEGER NOT NULL,
    machine_port INTEGER NOT NULL,
    protocol TEXT NOT NULL DEFAULT 'tcp',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (machine_id) REFERENCES machine(id)
);

INSERT INTO port_rule_new (id, machine_id, host_port, machine_port, protocol, created_at)
SELECT id, vm_id, host_port, vm_port, protocol, created_at FROM port_rule;

DROP TABLE port_rule;
ALTER TABLE port_rule_new RENAME TO port_rule;

CREATE UNIQUE INDEX idx_port_rule_host_port ON port_rule(host_port, protocol);
CREATE INDEX idx_port_rule_machine ON port_rule(machine_id);

-- Phase 3: ip_allocation — vm_id → machine_id
CREATE TABLE ip_allocation_new (
    ip TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL,
    allocated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (machine_id) REFERENCES machine(id)
);

INSERT INTO ip_allocation_new (ip, machine_id, allocated_at)
SELECT ip, vm_id, allocated_at FROM ip_allocation;

DROP TABLE ip_allocation;
ALTER TABLE ip_allocation_new RENAME TO ip_allocation;

CREATE INDEX idx_ip_allocation_machine ON ip_allocation(machine_id);

-- Phase 4: volume — attached → machine_id
CREATE TABLE volume_new (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    size_mb           INTEGER NOT NULL,
    path              TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'online' CHECK (status IN ('online', 'attached', 'maintenance', 'error')),
    machine_id        TEXT,
    role              TEXT NOT NULL DEFAULT 'data',
    created           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_state_change TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

INSERT INTO volume_new (id, name, size_mb, path, status, machine_id, role, created, last_state_change)
SELECT id, name, size_mb, path, status, attached, role, created, last_state_change FROM volume;

DROP TABLE volume;
ALTER TABLE volume_new RENAME TO volume;

CREATE INDEX idx_volume_status ON volume(status);
CREATE INDEX idx_volume_machine ON volume(machine_id) WHERE machine_id IS NOT NULL;

-- Phase 5: audit_log — update target_type
UPDATE audit_log SET target_type = 'machine' WHERE target_type = 'vm';
