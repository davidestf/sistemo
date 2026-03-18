-- Named networks for VM isolation. VMs in the same network can reach each other;
-- VMs in different networks are fully isolated.
-- NULL network_id in vm table = default sistemo0 bridge (backwards compatible).
CREATE TABLE IF NOT EXISTS network (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    subnet TEXT NOT NULL,
    bridge_name TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
