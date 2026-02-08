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
  height INTEGER
);

CREATE INDEX IF NOT EXISTS idx_images_created_at ON images(created_at);
CREATE INDEX IF NOT EXISTS idx_images_artist ON images(artist_name);
