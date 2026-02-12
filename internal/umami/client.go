package umami

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultHTTPTimeout = 20 * time.Second
	defaultCacheTTL    = 60 * time.Second
	defaultLookback    = 7
)

type Config struct {
	BaseURL      string
	WebsiteID    string
	Username     string
	Password     string
	APIToken     string
	LookbackDays int
}

type CountryStat struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Summary struct {
	Visitors  int           `json:"visitors"`
	Visits    int           `json:"visits"`
	Pageviews int           `json:"pageviews"`
	Countries []CountryStat `json:"countries"`
	RangeDays int           `json:"range_days"`
	UpdatedAt int64         `json:"updated_at"`
}

type Client struct {
	baseURL   string
	websiteID string
	username  string
	password  string
	apiToken  string
	lookback  int
	http      *http.Client

	tokenMu      sync.Mutex
	sessionToken string

	cacheMu  sync.Mutex
	cached   *Summary
	cachedAt time.Time
	cacheTTL time.Duration
}

func New(cfg Config) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	lookback := cfg.LookbackDays
	if lookback <= 0 {
		lookback = defaultLookback
	}

	return &Client{
		baseURL:   baseURL,
		websiteID: strings.TrimSpace(cfg.WebsiteID),
		username:  strings.TrimSpace(cfg.Username),
		password:  cfg.Password,
		apiToken:  strings.TrimSpace(cfg.APIToken),
		lookback:  lookback,
		http:      &http.Client{Timeout: defaultHTTPTimeout},
		cacheTTL:  defaultCacheTTL,
	}
}

func (c *Client) Enabled() bool {
	if c.baseURL == "" || c.websiteID == "" {
		return false
	}
	if c.apiToken != "" {
		return true
	}
	return c.username != "" && c.password != ""
}

func (c *Client) GetSummary(ctx context.Context) (*Summary, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("umami is not configured")
	}

	c.cacheMu.Lock()
	if c.cached != nil && time.Since(c.cachedAt) < c.cacheTTL {
		copyVal := *c.cached
		c.cacheMu.Unlock()
		return &copyVal, nil
	}
	c.cacheMu.Unlock()

	summary, err := c.fetchSummary(ctx)
	if err != nil {
		return nil, err
	}

	c.cacheMu.Lock()
	c.cached = summary
	c.cachedAt = time.Now()
	c.cacheMu.Unlock()

	return summary, nil
}

func (c *Client) fetchSummary(ctx context.Context) (*Summary, error) {
	now := time.Now().UTC()
	// Umami expects startAt/endAt in milliseconds.
	start := now.AddDate(0, 0, -c.lookback).UnixMilli()
	end := now.UnixMilli()

	statsPath := fmt.Sprintf("/api/websites/%s/stats?startAt=%d&endAt=%d", url.PathEscape(c.websiteID), start, end)
	var statsResp struct {
		Pageviews int `json:"pageviews"`
		Visitors  int `json:"visitors"`
		Visits    int `json:"visits"`
	}
	if err := c.getJSON(ctx, statsPath, &statsResp); err != nil {
		return nil, err
	}

	countries, err := c.fetchCountries(ctx, start, end)
	if err != nil {
		return nil, err
	}

	return &Summary{
		Visitors:  statsResp.Visitors,
		Visits:    statsResp.Visits,
		Pageviews: statsResp.Pageviews,
		Countries: countries,
		RangeDays: c.lookback,
		UpdatedAt: now.Unix(),
	}, nil
}

func (c *Client) fetchCountries(ctx context.Context, start, end int64) ([]CountryStat, error) {
	paths := []string{
		fmt.Sprintf("/api/websites/%s/metrics/expanded?type=country&startAt=%d&endAt=%d", url.PathEscape(c.websiteID), start, end),
		fmt.Sprintf("/api/websites/%s/metrics?type=country&startAt=%d&endAt=%d", url.PathEscape(c.websiteID), start, end),
	}

	var lastErr error
	for _, p := range paths {
		var rows []map[string]interface{}
		if err := c.getJSON(ctx, p, &rows); err != nil {
			lastErr = err
			continue
		}
		out := make([]CountryStat, 0, len(rows))
		for _, row := range rows {
			name := normalizeCountryName(row["x"])
			if name == "" {
				name = normalizeCountryName(row["name"])
			}
			if name == "" {
				name = normalizeCountryName(row["country"])
			}
			count := parseMetricCount(row["y"])
			if count == 0 {
				count = parseMetricCount(row["count"])
			}
			if count == 0 {
				count = parseMetricCount(row["value"])
			}
			if count <= 0 {
				continue
			}
			if name == "" {
				name = "Unknown"
			}
			out = append(out, CountryStat{Name: name, Count: count})
		}
		return out, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return []CountryStat{}, nil
}

func normalizeCountryName(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case map[string]interface{}:
		keys := []string{"name", "country", "countryName", "country_name", "code", "countryCode", "country_code"}
		for _, key := range keys {
			if val, ok := t[key]; ok {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

func parseMetricCount(v interface{}) int {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int(t)
	case float32:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case int32:
		return int(t)
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
		if f, err := t.Float64(); err == nil {
			return int(f)
		}
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		if i, err := strconv.Atoi(s); err == nil {
			return i
		}
	}
	return 0
}

func (c *Client) getJSON(ctx context.Context, path string, dest interface{}) error {
	resp, body, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("umami status %d: %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal([]byte(body), dest); err != nil {
		return fmt.Errorf("decode umami response: %w", err)
	}
	return nil
}

func (c *Client) doRequest(ctx context.Context, method, path string, payload interface{}) (*http.Response, string, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, "", err
	}

	resp, body, err := c.requestWithToken(ctx, method, path, payload, token)
	if err != nil {
		return nil, "", err
	}
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && c.hasLoginCreds() {
		if refreshErr := c.refreshSessionToken(ctx); refreshErr != nil {
			return resp, body, nil
		}
		token, _ = c.getToken(ctx)
		return c.requestWithToken(ctx, method, path, payload, token)
	}
	return resp, body, nil
}

func (c *Client) requestWithToken(ctx context.Context, method, path string, payload interface{}, token string) (*http.Response, string, error) {
	var bodyReader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, "", err
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	return resp, string(rawBody), nil
}

func (c *Client) getToken(ctx context.Context) (string, error) {
	if c.apiToken != "" {
		return c.apiToken, nil
	}

	c.tokenMu.Lock()
	token := c.sessionToken
	c.tokenMu.Unlock()
	if token != "" {
		return token, nil
	}

	if err := c.refreshSessionToken(ctx); err != nil {
		return "", err
	}

	c.tokenMu.Lock()
	token = c.sessionToken
	c.tokenMu.Unlock()
	if token == "" {
		return "", fmt.Errorf("umami token is empty after login")
	}
	return token, nil
}

func (c *Client) hasLoginCreds() bool {
	return c.username != "" && c.password != ""
}

func (c *Client) refreshSessionToken(ctx context.Context) error {
	if !c.hasLoginCreds() {
		return fmt.Errorf("umami username/password not configured")
	}

	payload := map[string]string{
		"username": c.username,
		"password": c.password,
	}
	resp, body, err := c.requestWithToken(ctx, http.MethodPost, "/api/auth/login", payload, "")
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("umami login failed status %d: %s", resp.StatusCode, body)
	}

	var loginResp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &loginResp); err != nil {
		return fmt.Errorf("decode umami login response: %w", err)
	}
	if strings.TrimSpace(loginResp.Token) == "" {
		return fmt.Errorf("umami login returned empty token")
	}

	c.tokenMu.Lock()
	c.sessionToken = strings.TrimSpace(loginResp.Token)
	c.tokenMu.Unlock()
	return nil
}
