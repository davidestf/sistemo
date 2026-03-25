-- Track Docker image builds across daemon restarts and page refreshes.
CREATE TABLE IF NOT EXISTS image_build (
    id TEXT PRIMARY KEY,
    image TEXT NOT NULL,
    build_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'building' CHECK (status IN ('building', 'complete', 'error')),
    message TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    completed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_image_build_status ON image_build(status);
CREATE INDEX IF NOT EXISTS idx_image_build_name ON image_build(build_name);
