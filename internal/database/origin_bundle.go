package database

import (
	"context"
	"strings"
	"time"
)

type OriginBundleItem struct {
	Order   int
	FileID  string
	Caption string
}

func (c *Client) UpsertOriginBundleItems(ctx context.Context, bundleID string, items []OriginBundleItem) error {
	bundleID = strings.TrimSpace(bundleID)
	if bundleID == "" || len(items) == 0 {
		return nil
	}

	if _, err := c.exec(ctx, "DELETE FROM origin_bundle_items WHERE bundle_id = ?", bundleID); err != nil {
		return err
	}

	now := time.Now().Unix()
	for idx, item := range items {
		fileID := strings.TrimSpace(item.FileID)
		if fileID == "" {
			continue
		}
		order := item.Order
		if order < 0 {
			order = idx
		}
		if _, err := c.exec(
			ctx,
			"INSERT OR REPLACE INTO origin_bundle_items (bundle_id, item_order, file_id, caption, created_at) VALUES (?, ?, ?, ?, ?)",
			bundleID,
			order,
			fileID,
			strings.TrimSpace(item.Caption),
			now,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) GetOriginBundleItems(ctx context.Context, bundleID string) ([]OriginBundleItem, error) {
	bundleID = strings.TrimSpace(bundleID)
	if bundleID == "" {
		return nil, nil
	}

	rows, err := c.exec(
		ctx,
		"SELECT item_order, file_id, caption FROM origin_bundle_items WHERE bundle_id = ? ORDER BY item_order ASC",
		bundleID,
	)
	if err != nil {
		return nil, err
	}

	out := make([]OriginBundleItem, 0, len(rows))
	for _, row := range rows {
		fileID := strings.TrimSpace(rowString(row, "file_id"))
		if fileID == "" {
			continue
		}
		out = append(out, OriginBundleItem{
			Order:   int(rowInt64(row, "item_order")),
			FileID:  fileID,
			Caption: strings.TrimSpace(rowString(row, "caption")),
		})
	}
	return out, nil
}
