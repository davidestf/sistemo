CREATE TABLE IF NOT EXISTS ip_allocation (
    ip TEXT PRIMARY KEY,
    vm_id TEXT NOT NULL,
    allocated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (vm_id) REFERENCES vm(id)
);
CREATE INDEX IF NOT EXISTS idx_ip_allocation_vm ON ip_allocation(vm_id);
