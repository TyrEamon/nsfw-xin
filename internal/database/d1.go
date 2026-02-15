package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Image struct {
	ID                string
	PreviewID         string
	OriginID          string
	Title             string
	ArtistName        string
	ArtistID          string
	SourceURL         string
	SourceText        string
	Source            string
	Tags              string
	Width             int
	Height            int
	CreatedAt         int64
	PublishChannelID  int64
	PublishMessageID  int
	StorageChannelID  int64
	StorageMessageID  int
	DiscussionGroupID int64
	DiscussionMsgID   int
}

type AdminImageCounts struct {
	All    int64
	Active int64
	Hidden int64
}

type Client struct {
	accountID string
	apiToken  string
	dbID      string
	http      *http.Client
}

func New(accountID, apiToken, dbID string) *Client {
	return &Client{
		accountID: accountID,
		apiToken:  apiToken,
		dbID:      dbID,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

type d1Request struct {
	SQL    string        `json:"sql"`
	Params []interface{} `json:"params"`
}

type d1Response struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result []struct {
		Results []map[string]interface{} `json:"results"`
		Success bool                     `json:"success"`
	} `json:"result"`
}

func (c *Client) exec(ctx context.Context, sql string, params ...interface{}) ([]map[string]interface{}, error) {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/d1/database/%s/query", c.accountID, c.dbID)
	body, err := json.Marshal(d1Request{SQL: sql, Params: params})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data d1Response
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if !data.Success {
		if len(data.Errors) > 0 {
			return nil, fmt.Errorf("d1 error: %s", data.Errors[0].Message)
		}
		return nil, fmt.Errorf("d1 error")
	}
	if len(data.Result) == 0 {
		return nil, nil
	}
	return data.Result[0].Results, nil
}

func (c *Client) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS images (
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
		)`,
		"CREATE INDEX IF NOT EXISTS idx_images_created_at ON images(created_at)",
		"CREATE INDEX IF NOT EXISTS idx_images_artist ON images(artist_name)",
		"CREATE INDEX IF NOT EXISTS idx_images_status_created_at ON images(status, created_at)",
		"CREATE INDEX IF NOT EXISTS idx_images_publish_message ON images(publish_channel_id, publish_message_id)",
		"CREATE INDEX IF NOT EXISTS idx_images_storage_message ON images(storage_channel_id, storage_message_id)",
		"CREATE TABLE IF NOT EXISTS favorites (image_id TEXT PRIMARY KEY, created_at INTEGER NOT NULL)",
		"CREATE INDEX IF NOT EXISTS idx_favorites_created_at ON favorites(created_at)",
		"CREATE TABLE IF NOT EXISTS ingest_blocklist (block_key TEXT PRIMARY KEY, reason TEXT, created_at INTEGER NOT NULL)",
		"CREATE TABLE IF NOT EXISTS crawler_state (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL)",
		"CREATE TABLE IF NOT EXISTS image_backups (image_id TEXT PRIMARY KEY, preview_path TEXT, origin_path TEXT, status TEXT NOT NULL DEFAULT 'pending', retry_count INTEGER NOT NULL DEFAULT 0, last_error TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)",
		"CREATE INDEX IF NOT EXISTS idx_image_backups_status_updated ON image_backups(status, updated_at)",
	}

	for _, stmt := range stmts {
		if _, err := c.exec(ctx, stmt); err != nil {
			return err
		}
	}

	cols, err := c.exec(ctx, "PRAGMA table_info(images)")
	if err != nil {
		return err
	}

	has := make(map[string]bool, len(cols))
	for _, col := range cols {
		if name, ok := col["name"].(string); ok {
			has[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}

	alterStmts := []struct {
		col string
		sql string
	}{
		{col: "status", sql: "ALTER TABLE images ADD COLUMN status TEXT NOT NULL DEFAULT 'active'"},
		{col: "source_text", sql: "ALTER TABLE images ADD COLUMN source_text TEXT"},
		{col: "publish_channel_id", sql: "ALTER TABLE images ADD COLUMN publish_channel_id INTEGER"},
		{col: "publish_message_id", sql: "ALTER TABLE images ADD COLUMN publish_message_id INTEGER"},
		{col: "storage_channel_id", sql: "ALTER TABLE images ADD COLUMN storage_channel_id INTEGER"},
		{col: "storage_message_id", sql: "ALTER TABLE images ADD COLUMN storage_message_id INTEGER"},
		{col: "discussion_group_id", sql: "ALTER TABLE images ADD COLUMN discussion_group_id INTEGER"},
		{col: "discussion_message_id", sql: "ALTER TABLE images ADD COLUMN discussion_message_id INTEGER"},
	}

	for _, item := range alterStmts {
		if has[item.col] {
			continue
		}
		if _, err := c.exec(ctx, item.sql); err != nil {
			return err
		}
	}

	return nil
}

func (c *Client) InsertImage(ctx context.Context, img Image) error {
	sql := `INSERT OR IGNORE INTO images (
		id, preview_id, origin_id,
		title, artist_name, artist_id,
		source_url, source_text, source, tags,
		created_at, width, height,
		publish_channel_id, publish_message_id,
		storage_channel_id, storage_message_id,
		discussion_group_id, discussion_message_id,
		status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := c.exec(ctx, sql,
		img.ID,
		img.PreviewID,
		img.OriginID,
		img.Title,
		img.ArtistName,
		img.ArtistID,
		img.SourceURL,
		img.SourceText,
		img.Source,
		img.Tags,
		img.CreatedAt,
		img.Width,
		img.Height,
		img.PublishChannelID,
		img.PublishMessageID,
		img.StorageChannelID,
		img.StorageMessageID,
		img.DiscussionGroupID,
		img.DiscussionMsgID,
		"active",
	)
	return err
}

func (c *Client) Exists(ctx context.Context, id string) (bool, error) {
	sql := "SELECT 1 FROM images WHERE id = ? LIMIT 1"
	results, err := c.exec(ctx, sql, id)
	if err != nil {
		return false, err
	}
	return len(results) > 0, nil
}

func (c *Client) ListImages(ctx context.Context, offset, limit int, orientation string) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 20
	}
	sql := "SELECT id, preview_id, origin_id, title, artist_name, artist_id, source_url, source, tags, created_at, width, height FROM images WHERE status = ?"
	params := []interface{}{"active"}
	if orientation == "h" {
		sql += " AND width >= height"
	} else if orientation == "v" {
		sql += " AND height > width"
	}
	sql += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	params = append(params, limit, offset)

	return c.exec(ctx, sql, params...)
}

func (c *Client) GetImage(ctx context.Context, id string) (map[string]interface{}, error) {
	sql := "SELECT id, preview_id, origin_id, title, artist_name, artist_id, source_url, source, tags, created_at, width, height, status FROM images WHERE id = ?"
	results, err := c.exec(ctx, sql, id)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

func (c *Client) RandomImage(ctx context.Context, orientation string) (map[string]interface{}, error) {
	sql := "SELECT id, preview_id, title, artist_name, artist_id, source_url, source, tags, created_at, width, height FROM images WHERE status = 'active' AND preview_id != ''"
	params := []interface{}{}
	if orientation == "h" {
		sql += " AND width >= height"
	} else if orientation == "v" {
		sql += " AND height > width"
	}
	sql += " ORDER BY RANDOM() LIMIT 1"

	results, err := c.exec(ctx, sql, params...)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

func (c *Client) ListFavorites(ctx context.Context, offset, limit int, orientation string) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 20
	}
	sql := "SELECT i.id, i.preview_id, i.origin_id, i.title, i.artist_name, i.artist_id, i.source_url, i.source, i.tags, i.created_at, i.width, i.height FROM favorites f JOIN images i ON i.id = f.image_id WHERE i.status = 'active'"
	params := []interface{}{}
	if orientation == "h" {
		sql += " AND i.width >= i.height"
	} else if orientation == "v" {
		sql += " AND i.height > i.width"
	}
	sql += " ORDER BY f.created_at DESC LIMIT ? OFFSET ?"
	params = append(params, limit, offset)
	return c.exec(ctx, sql, params...)
}

func (c *Client) SetFavorite(ctx context.Context, id string, on bool) error {
	if on {
		_, err := c.exec(ctx, "INSERT OR IGNORE INTO favorites (image_id, created_at) VALUES (?, ?)", id, time.Now().Unix())
		return err
	}
	_, err := c.exec(ctx, "DELETE FROM favorites WHERE image_id = ?", id)
	return err
}

func (c *Client) IsBlocked(ctx context.Context, key string) (bool, error) {
	sql := "SELECT 1 FROM ingest_blocklist WHERE block_key = ? LIMIT 1"
	results, err := c.exec(ctx, sql, key)
	if err != nil {
		return false, err
	}
	return len(results) > 0, nil
}

func (c *Client) HideAndBlock(ctx context.Context, id, reason string) error {
	if _, err := c.exec(ctx, "UPDATE images SET status = 'hidden' WHERE id = ?", id); err != nil {
		return err
	}
	if _, err := c.exec(ctx, "DELETE FROM favorites WHERE image_id = ?", id); err != nil {
		return err
	}
	_, err := c.exec(ctx, "INSERT OR IGNORE INTO ingest_blocklist (block_key, reason, created_at) VALUES (?, ?, ?)", id, reason, time.Now().Unix())
	return err
}

func (c *Client) ListAdminImages(ctx context.Context, offset, limit int, status string) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 60
	}
	sql := "SELECT i.id, i.preview_id, i.origin_id, i.title, i.artist_name, i.artist_id, i.source_url, i.source, i.tags, i.created_at, i.width, i.height, i.status, CASE WHEN f.image_id IS NULL THEN 0 ELSE 1 END AS is_favorite FROM images i LEFT JOIN favorites f ON f.image_id = i.id"
	params := []interface{}{}
	if status == "active" || status == "hidden" {
		sql += " WHERE i.status = ?"
		params = append(params, status)
	}
	sql += " ORDER BY i.created_at DESC LIMIT ? OFFSET ?"
	params = append(params, limit, offset)
	return c.exec(ctx, sql, params...)
}

func (c *Client) CountAdminImages(ctx context.Context) (AdminImageCounts, error) {
	rows, err := c.exec(ctx, "SELECT status, COUNT(*) AS c FROM images GROUP BY status")
	if err != nil {
		return AdminImageCounts{}, err
	}

	counts := AdminImageCounts{}
	for _, row := range rows {
		status := strings.TrimSpace(rowString(row, "status"))
		count := rowInt64(row, "c")
		counts.All += count
		switch status {
		case "active":
			counts.Active = count
		case "hidden":
			counts.Hidden = count
		}
	}
	return counts, nil
}

func (c *Client) GetCrawlerState(ctx context.Context, key string) (string, bool, error) {
	results, err := c.exec(ctx, "SELECT value FROM crawler_state WHERE key = ? LIMIT 1", key)
	if err != nil {
		return "", false, err
	}
	if len(results) == 0 {
		return "", false, nil
	}
	val, _ := results[0]["value"].(string)
	return val, true, nil
}

func (c *Client) SetCrawlerState(ctx context.Context, key, value string) error {
	_, err := c.exec(ctx, "INSERT OR REPLACE INTO crawler_state (key, value, updated_at) VALUES (?, ?, ?)", key, value, time.Now().Unix())
	return err
}
