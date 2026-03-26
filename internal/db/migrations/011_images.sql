-- Content-addressable image identity.
-- Every image file gets a sha256 digest as its primary key.
-- Names and tags are mutable display labels, never used for identity.

CREATE TABLE IF NOT EXISTS image (
    digest      TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    file        TEXT NOT NULL,
    path        TEXT NOT NULL,
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    source      TEXT NOT NULL DEFAULT 'unknown'
                CHECK (source IN ('registry', 'docker_build', 'url', 'unknown')),
    source_ref  TEXT,
    verified_at TEXT,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_image_path ON image(path);

CREATE TABLE IF NOT EXISTS image_tag (
    tag     TEXT PRIMARY KEY,
    digest  TEXT NOT NULL REFERENCES image(digest) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_image_tag_digest ON image_tag(digest);
