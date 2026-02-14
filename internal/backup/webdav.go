package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type webdavClient struct {
	baseURL  *url.URL
	username string
	password string
	http     *http.Client
}

func newWebDAVClient(rawURL, username, password string) (*webdavClient, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid webdav url")
	}
	if u.Path == "" {
		u.Path = "/"
	}

	return &webdavClient{
		baseURL:  u,
		username: username,
		password: password,
		http: &http.Client{
			Timeout: 45 * time.Second,
		},
	}, nil
}

func (c *webdavClient) HealthCheck(ctx context.Context, healthDir string) error {
	if err := c.EnsureDir(ctx, healthDir); err != nil {
		return err
	}

	testFile := path.Join(healthDir, fmt.Sprintf("health-%d.txt", time.Now().UnixNano()))
	body := []byte("ok")
	if err := c.UploadBytes(ctx, testFile, body, "text/plain; charset=utf-8"); err != nil {
		return err
	}
	if err := c.Delete(ctx, testFile); err != nil {
		return err
	}
	return nil
}

func (c *webdavClient) UploadBytes(ctx context.Context, remotePath string, data []byte, contentType string) error {
	if err := c.EnsureDir(ctx, path.Dir(remotePath)); err != nil {
		return err
	}

	resp, err := c.do(ctx, http.MethodPut, remotePath, bytes.NewReader(data), map[string]string{
		"Content-Type": contentType,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("webdav PUT %s status=%d body=%s", remotePath, resp.StatusCode, readRespBody(resp.Body, 400))
	}
	return nil
}

func (c *webdavClient) EnsureDir(ctx context.Context, dir string) error {
	cleanDir := path.Clean("/" + strings.Trim(strings.TrimSpace(dir), "/"))
	if cleanDir == "/" {
		return nil
	}

	parts := strings.Split(strings.TrimPrefix(cleanDir, "/"), "/")
	current := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		current += "/" + p
		resp, err := c.do(ctx, "MKCOL", current, nil, nil)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusCreated, http.StatusMethodNotAllowed, http.StatusOK, http.StatusNoContent:
			continue
		default:
			return fmt.Errorf("webdav MKCOL %s status=%d", current, resp.StatusCode)
		}
	}
	return nil
}

func (c *webdavClient) Delete(ctx context.Context, remotePath string) error {
	resp, err := c.do(ctx, http.MethodDelete, remotePath, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("webdav DELETE %s status=%d body=%s", remotePath, resp.StatusCode, readRespBody(resp.Body, 400))
	}
}

func (c *webdavClient) do(ctx context.Context, method, remotePath string, body io.Reader, headers map[string]string) (*http.Response, error) {
	u := c.resolveURL(remotePath)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.http.Do(req)
}

func (c *webdavClient) resolveURL(remotePath string) *url.URL {
	u := *c.baseURL
	basePath := c.baseURL.Path
	if basePath == "" {
		basePath = "/"
	}
	u.Path = path.Join(basePath, "/"+strings.Trim(strings.TrimSpace(remotePath), "/"))
	return &u
}

func readRespBody(r io.Reader, max int) string {
	if r == nil || max <= 0 {
		return ""
	}
	buf, _ := io.ReadAll(io.LimitReader(r, int64(max)))
	return strings.TrimSpace(string(buf))
}
