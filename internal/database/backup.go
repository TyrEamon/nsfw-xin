package database

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type BackupTask struct {
	ImageID    string
	PreviewID  string
	OriginID   string
	Source     string
	CreatedAt  int64
	RetryCount int
	Status     string
}

type BackupStats struct {
	Pending    int64
	Processing int64
	Synced     int64
	Failed     int64
	Total      int64
}

type BackupFailedItem struct {
	ImageID    string `json:"image_id"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	SourceURL  string `json:"source_url"`
	LastError  string `json:"last_error"`
	RetryCount int    `json:"retry_count"`
	UpdatedAt  int64  `json:"updated_at"`
}

func (c *Client) EnqueueBackupTask(ctx context.Context, imageID string) error {
	if imageID == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := c.exec(ctx, `
INSERT INTO image_backups (image_id, status, retry_count, created_at, updated_at)
VALUES (?, 'pending', 0, ?, ?)
ON CONFLICT(image_id) DO UPDATE SET
  status = CASE WHEN image_backups.status = 'synced' THEN image_backups.status ELSE 'pending' END,
  last_error = CASE WHEN image_backups.status = 'synced' THEN image_backups.last_error ELSE NULL END,
  updated_at = excluded.updated_at
`, imageID, now, now)
	return err
}

func (c *Client) BackfillBackupTasks(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}

	rows, err := c.exec(ctx, `
SELECT COUNT(*) AS c
FROM images i
LEFT JOIN image_backups b ON b.image_id = i.id
WHERE i.status = 'active'
  AND (COALESCE(i.preview_id, '') != '' OR COALESCE(i.origin_id, '') != '')
  AND b.image_id IS NULL
`)
	if err != nil {
		return 0, err
	}
	candidates := int64(0)
	if len(rows) > 0 {
		candidates = rowInt64(rows[0], "c")
	}
	if candidates == 0 {
		return 0, nil
	}

	now := time.Now().Unix()
	_, err = c.exec(ctx, `
INSERT OR IGNORE INTO image_backups (image_id, status, retry_count, created_at, updated_at)
SELECT i.id, 'pending', 0, ?, ?
FROM images i
LEFT JOIN image_backups b ON b.image_id = i.id
WHERE i.status = 'active'
  AND (COALESCE(i.preview_id, '') != '' OR COALESCE(i.origin_id, '') != '')
  AND b.image_id IS NULL
ORDER BY i.created_at DESC
LIMIT ?
`, now, now, limit)
	if err != nil {
		return 0, err
	}

	if candidates > int64(limit) {
		candidates = int64(limit)
	}
	return candidates, nil
}

func (c *Client) RetryFailedBackupTasks(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		limit = 500
	}

	rows, err := c.exec(ctx, "SELECT COUNT(*) AS c FROM image_backups WHERE status = 'failed'")
	if err != nil {
		return 0, err
	}
	failedCount := int64(0)
	if len(rows) > 0 {
		failedCount = rowInt64(rows[0], "c")
	}
	if failedCount == 0 {
		return 0, nil
	}

	retryCount := int64(limit)
	if retryCount > failedCount {
		retryCount = failedCount
	}

	now := time.Now().Unix()
	_, err = c.exec(ctx, `
UPDATE image_backups
SET status = 'pending', retry_count = 0, last_error = NULL, updated_at = ?
WHERE image_id IN (
  SELECT image_id
  FROM image_backups
  WHERE status = 'failed'
  ORDER BY updated_at ASC
  LIMIT ?
)
`, now, retryCount)
	if err != nil {
		return 0, err
	}

	return retryCount, nil
}

func (c *Client) ListBackupTasks(ctx context.Context, limit, retryMax int) ([]BackupTask, error) {
	if limit <= 0 {
		limit = 50
	}
	if retryMax <= 0 {
		retryMax = 5
	}

	rows, err := c.exec(ctx, `
SELECT
  b.image_id,
  i.preview_id,
  i.origin_id,
  i.source,
  i.created_at,
  b.retry_count,
  b.status
FROM image_backups b
JOIN images i ON i.id = b.image_id
WHERE i.status = 'active'
  AND (b.status = 'pending' OR (b.status = 'failed' AND b.retry_count < ?))
ORDER BY b.updated_at ASC
LIMIT ?
`, retryMax, limit)
	if err != nil {
		return nil, err
	}

	out := make([]BackupTask, 0, len(rows))
	for _, row := range rows {
		out = append(out, BackupTask{
			ImageID:    rowString(row, "image_id"),
			PreviewID:  rowString(row, "preview_id"),
			OriginID:   rowString(row, "origin_id"),
			Source:     rowString(row, "source"),
			CreatedAt:  rowInt64(row, "created_at"),
			RetryCount: int(rowInt64(row, "retry_count")),
			Status:     rowString(row, "status"),
		})
	}
	return out, nil
}

func (c *Client) MarkBackupSynced(ctx context.Context, imageID, previewPath, originPath string) error {
	now := time.Now().Unix()
	_, err := c.exec(ctx, `
UPDATE image_backups
SET status = 'synced',
    preview_path = ?,
    origin_path = ?,
    last_error = NULL,
    updated_at = ?
WHERE image_id = ?
`, previewPath, originPath, now, imageID)
	return err
}

func (c *Client) MarkBackupFailed(ctx context.Context, imageID, lastError string) error {
	now := time.Now().Unix()
	_, err := c.exec(ctx, `
UPDATE image_backups
SET status = 'failed',
    retry_count = retry_count + 1,
    last_error = ?,
    updated_at = ?
WHERE image_id = ?
`, lastError, now, imageID)
	return err
}

func (c *Client) ListFailedBackupTasks(ctx context.Context, limit, offset int) ([]BackupFailedItem, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	countRows, err := c.exec(ctx, "SELECT COUNT(*) AS c FROM image_backups WHERE status = 'failed'")
	if err != nil {
		return nil, 0, err
	}
	total := int64(0)
	if len(countRows) > 0 {
		total = rowInt64(countRows[0], "c")
	}
	if total == 0 {
		return []BackupFailedItem{}, 0, nil
	}

	rows, err := c.exec(ctx, `
SELECT
  b.image_id,
  COALESCE(i.title, '') AS title,
  COALESCE(i.source, '') AS source,
  COALESCE(i.source_url, '') AS source_url,
  COALESCE(b.last_error, '') AS last_error,
  b.retry_count,
  b.updated_at
FROM image_backups b
LEFT JOIN images i ON i.id = b.image_id
WHERE b.status = 'failed'
ORDER BY b.updated_at DESC
LIMIT ? OFFSET ?
`, limit, offset)
	if err != nil {
		return nil, 0, err
	}

	items := make([]BackupFailedItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, BackupFailedItem{
			ImageID:    rowString(row, "image_id"),
			Title:      rowString(row, "title"),
			Source:     rowString(row, "source"),
			SourceURL:  rowString(row, "source_url"),
			LastError:  rowString(row, "last_error"),
			RetryCount: int(rowInt64(row, "retry_count")),
			UpdatedAt:  rowInt64(row, "updated_at"),
		})
	}

	return items, total, nil
}

func (c *Client) MarkBackupResolved(ctx context.Context, imageID string) error {
	imageID = strings.TrimSpace(imageID)
	if imageID == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := c.exec(ctx, `
UPDATE image_backups
SET status = 'synced',
    last_error = NULL,
    updated_at = ?
WHERE image_id = ?
  AND status = 'failed'
`, now, imageID)
	return err
}

func (c *Client) GetBackupStats(ctx context.Context) (BackupStats, error) {
	rows, err := c.exec(ctx, "SELECT status, COUNT(*) AS c FROM image_backups GROUP BY status")
	if err != nil {
		return BackupStats{}, err
	}

	stats := BackupStats{}
	for _, row := range rows {
		status := rowString(row, "status")
		count := rowInt64(row, "c")
		stats.Total += count
		switch status {
		case "pending":
			stats.Pending = count
		case "processing":
			stats.Processing = count
		case "synced":
			stats.Synced = count
		case "failed":
			stats.Failed = count
		}
	}
	return stats, nil
}

func rowString(row map[string]interface{}, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	default:
		return ""
	}
}

func rowInt64(row map[string]interface{}, key string) int64 {
	v, ok := row[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(t, 10, 64)
		return i
	default:
		return 0
	}
}
