CREATE TABLE IF NOT EXISTS port_rule (
    id TEXT PRIMARY KEY,
    vm_id TEXT NOT NULL,
    host_port INTEGER NOT NULL,
    vm_port INTEGER NOT NULL,
    protocol TEXT NOT NULL DEFAULT 'tcp',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (vm_id) REFERENCES vm(id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_port_rule_host_port ON port_rule(host_port, protocol);
CREATE INDEX IF NOT EXISTS idx_port_rule_vm ON port_rule(vm_id);
