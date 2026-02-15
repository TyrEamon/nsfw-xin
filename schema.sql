CREATE TABLE IF NOT EXISTS images (
  id TEXT PRIMARY KEY,
  preview_id TEXT,
  origin_id TEXT,
  title TEXT,
  artist_name TEXT,
  artist_id TEXT,
  source_url TEXT,
  source_text TEXT,
  source TEXT,
  tags TEXT,
  created_at INTEGER,
  width INTEGER,
  height INTEGER,
  publish_channel_id INTEGER,
  publish_message_id INTEGER,
  storage_channel_id INTEGER,
  storage_message_id INTEGER,
  discussion_group_id INTEGER,
  discussion_message_id INTEGER,
  status TEXT NOT NULL DEFAULT 'active'
);

CREATE INDEX IF NOT EXISTS idx_images_created_at ON images(created_at);
CREATE INDEX IF NOT EXISTS idx_images_artist ON images(artist_name);
CREATE INDEX IF NOT EXISTS idx_images_status_created_at ON images(status, created_at);
CREATE INDEX IF NOT EXISTS idx_images_publish_message ON images(publish_channel_id, publish_message_id);
CREATE INDEX IF NOT EXISTS idx_images_storage_message ON images(storage_channel_id, storage_message_id);

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

CREATE TABLE IF NOT EXISTS crawler_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS image_backups (
  image_id TEXT PRIMARY KEY,
  preview_path TEXT,
  origin_path TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  retry_count INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_image_backups_status_updated ON image_backups(status, updated_at);
