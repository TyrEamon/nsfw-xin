package pixiv

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	http   *http.Client
	cookie string
	userID string
}

func New(cookie, userID string) *Client {
	return &Client{
		http:   &http.Client{Timeout: 30 * time.Second},
		cookie: cookie,
		userID: userID,
	}
}

type bookmarkResp struct {
	Body struct {
		Total int `json:"total"`
		Works []struct {
			ID string `json:"id"`
		} `json:"works"`
		Illusts []struct {
			ID string `json:"id"`
		} `json:"illusts"`
	} `json:"body"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

func (c *Client) FetchBookmarkIDs(offset, limit int, tag string) ([]string, int, error) {
	baseURL := fmt.Sprintf("https://www.pixiv.net/ajax/user/%s/illusts/bookmarks", c.userID)
	q := url.Values{}
	q.Set("tag", tag)
	q.Set("offset", fmt.Sprintf("%d", offset))
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("rest", "show")
	req, err := http.NewRequest(http.MethodGet, baseURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	setHeaders(req, c.cookie)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var data bookmarkResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, 0, err
	}
	if data.Error {
		return nil, 0, fmt.Errorf("pixiv error: %s", data.Message)
	}

	ids := []string{}
	for _, w := range data.Body.Works {
		ids = append(ids, w.ID)
	}
	for _, w := range data.Body.Illusts {
		ids = append(ids, w.ID)
	}

	return ids, data.Body.Total, nil
}

type detailResp struct {
	Body struct {
		IllustID   string `json:"illustId"`
		Title      string `json:"illustTitle"`
		UserID     string `json:"userId"`
		UserName   string `json:"userName"`
		IllustType int    `json:"illustType"`
		Tags       struct {
			Tags []struct {
				Tag string `json:"tag"`
			} `json:"tags"`
		} `json:"tags"`
	} `json:"body"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

func (c *Client) FetchDetail(id string) (*detailResp, error) {
	url := fmt.Sprintf("https://www.pixiv.net/ajax/illust/%s", id)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	setHeaders(req, c.cookie)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data detailResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if data.Error {
		return nil, fmt.Errorf("pixiv error: %s", data.Message)
	}
	return &data, nil
}

type pageResp struct {
	Body []struct {
		Urls struct {
			Original string `json:"original"`
		} `json:"urls"`
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"body"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

func (c *Client) FetchPages(id string) ([]pageRespEntry, error) {
	url := fmt.Sprintf("https://www.pixiv.net/ajax/illust/%s/pages?lang=zh", id)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	setHeaders(req, c.cookie)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data pageResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if data.Error {
		return nil, fmt.Errorf("pixiv error: %s", data.Message)
	}

	items := make([]pageRespEntry, 0, len(data.Body))
	for _, p := range data.Body {
		items = append(items, pageRespEntry{URL: p.Urls.Original, Width: p.Width, Height: p.Height})
	}
	return items, nil
}

type pageRespEntry struct {
	URL    string
	Width  int
	Height int
}

func (c *Client) Download(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	setHeaders(req, c.cookie)
	req.Header.Set("Referer", "https://www.pixiv.net/")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func setHeaders(req *http.Request, cookie string) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if cookie != "" {
		req.Header.Set("Cookie", "PHPSESSID="+cookie)
	}
	req.Header.Set("Referer", "https://www.pixiv.net/")
}
