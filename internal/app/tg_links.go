package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"time"

	"pixiv-tg-gallery/internal/database"
)

const maxTGLinksPerMessage = 3

var (
	urlPattern      = regexp.MustCompile(`https?://[^\s]+`)
	pixivIDPattern  = regexp.MustCompile(`^\d+$`)
	yandeIDPattern  = regexp.MustCompile(`^\d+$`)
	punctuationTrim = ".,;:!?)]}>'\"，。！？、）】》"
)

type linkType string

const (
	linkPixiv linkType = "pixiv"
	linkYande linkType = "yande"
)

type supportedLink struct {
	Type linkType
	ID   string
	URL  string
}

type ingestStats struct {
	FirstID    string
	Title      string
	Downloaded int
	Skipped    int
	Failed     int
}

func extractSupportedLinks(parts ...string) []supportedLink {
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return nil
	}

	raw := urlPattern.FindAllString(text, -1)
	if len(raw) == 0 {
		return nil
	}

	links := make([]supportedLink, 0, len(raw))
	seen := map[string]struct{}{}

	for _, token := range raw {
		clean := strings.TrimRight(token, punctuationTrim)
		u, err := neturl.Parse(clean)
		if err != nil {
			continue
		}

		host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
		path := strings.Trim(u.EscapedPath(), "/")
		parts := strings.Split(path, "/")
		if host == "pixiv.net" {
			for i := 0; i+1 < len(parts); i++ {
				if parts[i] == "artworks" && pixivIDPattern.MatchString(parts[i+1]) {
					id := parts[i+1]
					key := string(linkPixiv) + ":" + id
					if _, ok := seen[key]; ok {
						break
					}
					seen[key] = struct{}{}
					links = append(links, supportedLink{Type: linkPixiv, ID: id, URL: clean})
					break
				}
			}
		}
		if host == "yande.re" && len(parts) >= 3 && parts[0] == "post" && parts[1] == "show" && yandeIDPattern.MatchString(parts[2]) {
			id := parts[2]
			key := string(linkYande) + ":" + id
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			links = append(links, supportedLink{Type: linkYande, ID: id, URL: clean})
		}
	}

	return links
}

func (a *App) handleTGLinks(ctx context.Context, links []supportedLink) (*TGIngestResult, error) {
	if len(links) > maxTGLinksPerMessage {
		links = links[:maxTGLinksPerMessage]
	}

	var (
		firstID, firstTitle, firstURL string
		successMsgs                   []string
		errorMsgs                     []string
	)

	for _, item := range links {
		switch item.Type {
		case linkPixiv:
			res, err := a.ingestPixivFromLink(ctx, item)
			if err != nil {
				errorMsgs = append(errorMsgs, fmt.Sprintf("pixiv %s: %v", item.ID, err))
				continue
			}
			if firstID == "" && res.ID != "" {
				firstID, firstTitle, firstURL = res.ID, res.Title, res.SourceURL
			}
			successMsgs = append(successMsgs, res.Summary)
		case linkYande:
			res, err := a.ingestYandeFromLink(ctx, item)
			if err != nil {
				errorMsgs = append(errorMsgs, fmt.Sprintf("yande %s: %v", item.ID, err))
				continue
			}
			if firstID == "" && res.ID != "" {
				firstID, firstTitle, firstURL = res.ID, res.Title, res.SourceURL
			}
			successMsgs = append(successMsgs, res.Summary)
		}
	}

	if len(successMsgs) == 0 {
		if len(errorMsgs) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf(strings.Join(errorMsgs, "; "))
	}

	summary := strings.Join(successMsgs, "\n")
	if len(errorMsgs) > 0 {
		summary += "\nPartial fail: " + strings.Join(errorMsgs, "; ")
	}

	return &TGIngestResult{
		ID:        firstID,
		Title:     firstTitle,
		SourceURL: firstURL,
		Summary:   summary,
	}, nil
}

func (a *App) ingestPixivFromLink(ctx context.Context, item supportedLink) (*TGIngestResult, error) {
	if a.Pixiv == nil || a.Cfg.PixivPHPSESSID == "" {
		return nil, fmt.Errorf("pixiv crawler is not configured")
	}

	stats, err := a.ingestPixivArtwork(ctx, item.ID, item.URL)
	if err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("Pixiv %s: added=%d skipped=%d failed=%d", item.ID, stats.Downloaded, stats.Skipped, stats.Failed)
	return &TGIngestResult{
		ID:        stats.FirstID,
		Title:     stats.Title,
		SourceURL: item.URL,
		Summary:   msg,
	}, nil
}

func (a *App) ingestYandeFromLink(ctx context.Context, item supportedLink) (*TGIngestResult, error) {
	imgID := fmt.Sprintf("yande_%s", item.ID)
	if blocked, err := a.DB.IsBlocked(ctx, imgID); err == nil && blocked {
		return &TGIngestResult{
			ID:        imgID,
			Title:     "Yandex",
			SourceURL: item.URL,
			Summary:   fmt.Sprintf("Yande %s: skipped=blocked", item.ID),
		}, nil
	}
	if exists, _ := a.DB.Exists(ctx, imgID); exists {
		return &TGIngestResult{
			ID:        imgID,
			Title:     "Yandex",
			SourceURL: item.URL,
			Summary:   fmt.Sprintf("Yande %s: skipped=already_exists", item.ID),
		}, nil
	}

	post, err := fetchYandePost(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	imgURL := post.bestImageURL()
	if imgURL == "" {
		return nil, fmt.Errorf("no image url found")
	}
	data, err := downloadWithHeaders(ctx, imgURL, "https://yande.re/")
	if err != nil {
		return nil, err
	}

	previewID, originID, _, _, width, height, err := a.TG.SendPreviewAndOrigin(ctx, data, "Yandex")
	if err != nil {
		return nil, err
	}

	img := database.Image{
		ID:         imgID,
		PreviewID:  previewID,
		OriginID:   originID,
		Title:      "Yandex",
		ArtistName: "Arts",
		ArtistID:   "none",
		SourceURL:  item.URL,
		Source:     "yande",
		Tags:       strings.TrimSpace(post.Tags),
		Width:      width,
		Height:     height,
		CreatedAt:  time.Now().Unix(),
	}
	if err := a.DB.InsertImage(ctx, img); err != nil {
		return nil, err
	}

	return &TGIngestResult{
		ID:        imgID,
		Title:     "Yandex",
		SourceURL: item.URL,
		Summary:   fmt.Sprintf("Yande %s: added=1", item.ID),
	}, nil
}

func (a *App) ingestPixivArtwork(ctx context.Context, id string, sourceURL string) (*ingestStats, error) {
	firstPageID := fmt.Sprintf("pixiv_%s_p0", id)
	if blocked, err := a.DB.IsBlocked(ctx, firstPageID); err == nil && blocked {
		return &ingestStats{Title: id, Skipped: 1}, nil
	}
	if exists, _ := a.DB.Exists(ctx, firstPageID); exists {
		return &ingestStats{Title: id, Skipped: 1}, nil
	}

	detail, err := a.Pixiv.FetchDetail(id)
	if err != nil {
		return nil, fmt.Errorf("pixiv detail: %w", err)
	}
	if detail.Body.IllustType == 2 {
		return nil, fmt.Errorf("ugoira is not supported")
	}

	tags := make([]string, 0, len(detail.Body.Tags.Tags))
	for _, t := range detail.Body.Tags.Tags {
		tags = append(tags, t.Tag)
	}

	pages, err := a.Pixiv.FetchPages(id)
	if err != nil {
		return nil, fmt.Errorf("pixiv pages: %w", err)
	}
	if sourceURL == "" {
		sourceURL = fmt.Sprintf("https://www.pixiv.net/artworks/%s", id)
	}

	stats := &ingestStats{Title: detail.Body.Title}
	for i, p := range pages {
		pid := fmt.Sprintf("pixiv_%s_p%d", id, i)
		if blocked, err := a.DB.IsBlocked(ctx, pid); err == nil && blocked {
			stats.Skipped++
			log.Printf("Pixiv skip page pid=%s reason=blocked", pid)
			continue
		}
		if exists, _ := a.DB.Exists(ctx, pid); exists {
			stats.Skipped++
			log.Printf("Pixiv skip page pid=%s reason=already_exists", pid)
			continue
		}

		imgData, err := a.Pixiv.Download(p.URL)
		if err != nil {
			stats.Failed++
			log.Printf("Pixiv download failed pid=%s err=%v", pid, err)
			continue
		}

		previewID, originID, _, _, width, height, err := a.TG.SendPreviewAndOrigin(ctx, imgData, detail.Body.Title)
		if err != nil {
			stats.Failed++
			log.Printf("Pixiv tg send failed pid=%s err=%v", pid, err)
			continue
		}

		img := database.Image{
			ID:         pid,
			PreviewID:  previewID,
			OriginID:   originID,
			Title:      detail.Body.Title,
			ArtistName: detail.Body.UserName,
			ArtistID:   detail.Body.UserID,
			SourceURL:  sourceURL,
			Source:     "pixiv",
			Tags:       strings.Join(tags, " "),
			Width:      width,
			Height:     height,
			CreatedAt:  time.Now().Unix(),
		}

		if err := a.DB.InsertImage(ctx, img); err != nil {
			stats.Failed++
			log.Printf("Pixiv d1 insert failed pid=%s err=%v", pid, err)
		} else {
			stats.Downloaded++
			if stats.FirstID == "" {
				stats.FirstID = pid
			}
			log.Printf("Pixiv stored pid=%s size=%dx%d", pid, width, height)
		}

		time.Sleep(2 * time.Second)
	}

	return stats, nil
}

type yandePost struct {
	ID        int    `json:"id"`
	FileURL   string `json:"file_url"`
	JPEGURL   string `json:"jpeg_url"`
	PNGURL    string `json:"png_url"`
	SampleURL string `json:"sample_url"`
	Tags      string `json:"tags"`
}

func (p yandePost) bestImageURL() string {
	candidates := []string{p.FileURL, p.JPEGURL, p.PNGURL, p.SampleURL}
	for _, u := range candidates {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if strings.HasPrefix(u, "//") {
			return "https:" + u
		}
		return u
	}
	return ""
}

func fetchYandePost(ctx context.Context, id string) (*yandePost, error) {
	endpoint := fmt.Sprintf("https://yande.re/post.json?tags=id:%s", id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://yande.re/")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yande status %d", resp.StatusCode)
	}

	var arr []yandePost
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("yande post not found")
	}
	return &arr[0], nil
}

func downloadWithHeaders(ctx context.Context, sourceURL, referer string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
