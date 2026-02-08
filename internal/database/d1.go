package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Image struct {
	ID         string
	PreviewID  string
	OriginID   string
	Title      string
	ArtistName string
	ArtistID   string
	SourceURL  string
	Source     string
	Tags       string
	Width      int
	Height     int
	CreatedAt  int64
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

func (c *Client) InsertImage(ctx context.Context, img Image) error {
	sql := "INSERT OR IGNORE INTO images (id, preview_id, origin_id, title, artist_name, artist_id, source_url, source, tags, created_at, width, height) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	_, err := c.exec(ctx, sql,
		img.ID,
		img.PreviewID,
		img.OriginID,
		img.Title,
		img.ArtistName,
		img.ArtistID,
		img.SourceURL,
		img.Source,
		img.Tags,
		img.CreatedAt,
		img.Width,
		img.Height,
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
	sql := "SELECT id, preview_id, origin_id, title, artist_name, artist_id, source_url, source, tags, created_at, width, height FROM images"
	params := []interface{}{}
	if orientation == "h" {
		sql += " WHERE width >= height"
	} else if orientation == "v" {
		sql += " WHERE height > width"
	}
	sql += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	params = append(params, limit, offset)

	return c.exec(ctx, sql, params...)
}

func (c *Client) GetImage(ctx context.Context, id string) (map[string]interface{}, error) {
	sql := "SELECT id, preview_id, origin_id, title, artist_name, artist_id, source_url, source, tags, created_at, width, height FROM images WHERE id = ?"
	results, err := c.exec(ctx, sql, id)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}
