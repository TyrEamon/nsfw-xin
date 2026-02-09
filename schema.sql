CREATE TABLE IF NOT EXISTS images (
  id TEXT PRIMARY KEY,
  preview_id TEXT,
  origin_id TEXT,
  title TEXT,
  artist_name TEXT,
  artist_id TEXT,
  source_url TEXT,
  source TEXT,
  tags TEXT,
  created_at INTEGER,
  width INTEGER,
  height INTEGER,
  status TEXT NOT NULL DEFAULT 'active'
);

CREATE INDEX IF NOT EXISTS idx_images_created_at ON images(created_at);
CREATE INDEX IF NOT EXISTS idx_images_artist ON images(artist_name);
CREATE INDEX IF NOT EXISTS idx_images_status_created_at ON images(status, created_at);

CREATE TABLE IF NOT EXISTS favorites (
  image_id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_favorites_created_at ON favorites(created_at);

CREATE TABLE IF NOT EXISTS ingest_blocklist (
  block_key TEXT PRIMARY KEY,
  reason TEXT,
  created_at INTEGER NOT NULL
);
