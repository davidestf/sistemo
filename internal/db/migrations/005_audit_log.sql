-- Audit log for all VM and network operations.
-- Queryable via sistemo history CLI and GET /history API.
CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    action TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT 'vm',
    target_id TEXT,
    target_name TEXT,
    details TEXT,
    success INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_log_target ON audit_log(target_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log(action);
