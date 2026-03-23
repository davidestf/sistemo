CREATE TABLE IF NOT EXISTS volume (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    size_mb           INTEGER NOT NULL,
    path              TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'online' CHECK (status IN ('online', 'attached', 'maintenance', 'error')),
    attached          TEXT,
    created           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_state_change TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_volume_status ON volume(status);
CREATE INDEX IF NOT EXISTS idx_volume_attached ON volume(attached) WHERE attached IS NOT NULL;
