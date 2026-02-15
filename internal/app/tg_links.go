package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	maxTGLinksPerMessage    = 3
	defaultTwitterAPIDomain = "fxtwitter.com"
)

var (
	urlPattern       = regexp.MustCompile(`https?://[^\s]+`)
	pixivIDPattern   = regexp.MustCompile(`^\d+$`)
	yandeIDPattern   = regexp.MustCompile(`^\d+$`)
	twitterIDPattern = regexp.MustCompile(`^\d+$`)
	hashtagPattern   = regexp.MustCompile(`#([A-Za-z0-9_]+)`)
	punctuationTrim  = ".,;:!?)]}>'\"\uFF0C\u3002\uFF01\uFF1F\u3001\uFF09\u3011\u300B"
)

type linkType string

const (
	linkPixiv   linkType = "pixiv"
	linkYande   linkType = "yande"
	linkTwitter linkType = "twitter"
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
		pathVal := strings.Trim(u.EscapedPath(), "/")
		segments := strings.Split(pathVal, "/")

		if host == "pixiv.net" {
			for i := 0; i+1 < len(segments); i++ {
				if segments[i] == "artworks" && pixivIDPattern.MatchString(segments[i+1]) {
					id := segments[i+1]
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

		if host == "yande.re" && len(segments) >= 3 && segments[0] == "post" && segments[1] == "show" && yandeIDPattern.MatchString(segments[2]) {
			id := segments[2]
			key := string(linkYande) + ":" + id
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			links = append(links, supportedLink{Type: linkYande, ID: id, URL: clean})
		}

		if isTwitterHost(host) {
			username, id, ok := parseTwitterPath(segments)
			if !ok {
				continue
			}
			key := string(linkTwitter) + ":" + id
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			links = append(links, supportedLink{Type: linkTwitter, ID: id, URL: canonicalTwitterURL(username, id)})
		}
	}

	return links
}

func isTwitterHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	return host == "x.com" || host == "twitter.com" || host == "mobile.twitter.com"
}

func parseTwitterPath(parts []string) (username, id string, ok bool) {
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "status" || !twitterIDPattern.MatchString(parts[i+1]) {
			continue
		}
		id = parts[i+1]
		if i > 0 {
			username = normalizeTwitterUsername(parts[i-1])
		}
		return username, id, true
	}
	return "", "", false
}

func canonicalTwitterURL(username, tweetID string) string {
	username = normalizeTwitterUsername(username)
	if username == "" {
		return fmt.Sprintf("https://x.com/i/status/%s", tweetID)
	}
	return fmt.Sprintf("https://x.com/%s/status/%s", username, tweetID)
}

func normalizeTwitterUsername(username string) string {
	username = strings.TrimSpace(username)
	username = strings.TrimPrefix(username, "@")
	return username
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
				errorMsgs = append(errorMsgs, fmt.Sprintf("Pixiv %s failed: %v", item.ID, err))
				continue
			}
			if firstID == "" && res.ID != "" {
				firstID, firstTitle, firstURL = res.ID, res.Title, res.SourceURL
			}
			successMsgs = append(successMsgs, res.Summary)
		case linkYande:
			res, err := a.ingestYandeFromLink(ctx, item)
			if err != nil {
				errorMsgs = append(errorMsgs, fmt.Sprintf("Yande %s failed: %v", item.ID, err))
				continue
			}
			if firstID == "" && res.ID != "" {
				firstID, firstTitle, firstURL = res.ID, res.Title, res.SourceURL
			}
			successMsgs = append(successMsgs, res.Summary)
		case linkTwitter:
			res, err := a.ingestTwitterFromLink(ctx, item)
			if err != nil {
				errorMsgs = append(errorMsgs, fmt.Sprintf("Twitter %s failed: %v", item.ID, err))
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
		return nil, fmt.Errorf("all links failed: %s", strings.Join(errorMsgs, "; "))
	}

	summary := strings.Join(successMsgs, "\n")
	if len(errorMsgs) > 0 {
		summary += "\nPartial failures: " + strings.Join(errorMsgs, "; ")
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
		return nil, fmt.Errorf("pixiv config is missing")
	}

	stats, err := a.ingestPixivArtwork(ctx, item.ID, item.URL)
	if err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("Pixiv %s done: +%d, skipped %d, failed %d", item.ID, stats.Downloaded, stats.Skipped, stats.Failed)
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
			Summary:   fmt.Sprintf("Yande %s skipped: blocked", item.ID),
		}, nil
	}
	if exists, _ := a.DB.Exists(ctx, imgID); exists {
		return &TGIngestResult{
			ID:        imgID,
			Title:     "Yandex",
			SourceURL: item.URL,
			Summary:   fmt.Sprintf("Yande %s skipped: already exists", item.ID),
		}, nil
	}

	post, err := fetchYandePost(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	imgURL := post.bestImageURL()
	if imgURL == "" {
		return nil, fmt.Errorf("no downloadable image URL")
	}
	data, err := downloadWithHeaders(ctx, imgURL, "https://yande.re/")
	if err != nil {
		return nil, err
	}

	img, err := a.publishImage(ctx, data, imagePublishMeta{
		ID:         imgID,
		Title:      "Yandex",
		ArtistName: "Arts",
		ArtistID:   "none",
		SourceURL:  item.URL,
		Source:     "yande",
		Tags:       strings.TrimSpace(post.Tags),
		CreatedAt:  time.Now().Unix(),
	})
	if err != nil {
		return nil, err
	}

	return &TGIngestResult{
		ID:        img.ID,
		Title:     img.Title,
		SourceURL: img.SourceURL,
		Summary:   fmt.Sprintf("Yande %s ingested (+1)", item.ID),
	}, nil
}

func (a *App) ingestTwitterFromLink(ctx context.Context, item supportedLink) (*TGIngestResult, error) {
	stats, err := a.ingestTwitterTweet(ctx, item.ID, item.URL)
	if err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("Twitter %s done: +%d, skipped %d, failed %d", item.ID, stats.Downloaded, stats.Skipped, stats.Failed)
	return &TGIngestResult{
		ID:        stats.FirstID,
		Title:     stats.Title,
		SourceURL: item.URL,
		Summary:   msg,
	}, nil
}

func (a *App) ingestTwitterTweet(ctx context.Context, tweetID, sourceURL string) (*ingestStats, error) {
	firstPageID := fmt.Sprintf("twitter_%s_p0", tweetID)
	if blocked, err := a.DB.IsBlocked(ctx, firstPageID); err == nil && blocked {
		return &ingestStats{Title: tweetID, Skipped: 1}, nil
	}
	if exists, _ := a.DB.Exists(ctx, firstPageID); exists {
		return &ingestStats{Title: tweetID, Skipped: 1}, nil
	}

	tweet, err := fetchTwitterTweet(ctx, a.Cfg.TwitterAPIDomain, tweetID)
	if err != nil {
		return nil, fmt.Errorf("twitter status: %w", err)
	}

	title := buildTwitterTitle(tweet.Text, tweetID, tweet.Author.Username)
	artistName := strings.TrimSpace(tweet.Author.Name)
	if artistName == "" {
		artistName = strings.TrimSpace(tweet.Author.Username)
	}
	if artistName == "" {
		artistName = "Arts"
	}

	artistID := normalizeTwitterUsername(tweet.Author.Username)
	if artistID == "" {
		artistID = "none"
	}

	if strings.TrimSpace(sourceURL) == "" {
		sourceURL = canonicalTwitterURL(artistID, tweetID)
	}

	photos := tweet.photoURLs()
	if len(photos) == 0 {
		return nil, fmt.Errorf("tweet has no photos")
	}
	log.Printf("Twitter tweet fetched id=%s photos=%d", tweetID, len(photos))

	tags := strings.Join(extractHashTags(tweet.Text), " ")
	stats := &ingestStats{Title: title}

	for i, rawURL := range photos {
		pid := fmt.Sprintf("twitter_%s_p%d", tweetID, i)
		if blocked, err := a.DB.IsBlocked(ctx, pid); err == nil && blocked {
			stats.Skipped++
			log.Printf("Twitter skip page pid=%s reason=blocked", pid)
			continue
		}
		if exists, _ := a.DB.Exists(ctx, pid); exists {
			stats.Skipped++
			log.Printf("Twitter skip page pid=%s reason=already_exists", pid)
			continue
		}

		imgURL := buildTwitterImageURL(rawURL)
		imgData, err := downloadWithHeaders(ctx, imgURL, "https://x.com/")
		if err != nil {
			stats.Failed++
			log.Printf("Twitter download failed pid=%s err=%v", pid, err)
			continue
		}

		img, err := a.publishImage(ctx, imgData, imagePublishMeta{
			ID:         pid,
			Title:      title,
			ArtistName: artistName,
			ArtistID:   artistID,
			SourceURL:  sourceURL,
			SourceText: tweet.Text,
			Source:     "twitter",
			Tags:       tags,
			CreatedAt:  time.Now().Unix(),
		})
		if err != nil {
			stats.Failed++
			log.Printf("Twitter publish failed pid=%s err=%v", pid, err)
		} else {
			stats.Downloaded++
			if stats.FirstID == "" {
				stats.FirstID = pid
			}
			log.Printf("Twitter stored pid=%s size=%dx%d", pid, img.Width, img.Height)
		}

		time.Sleep(1500 * time.Millisecond)
	}

	return stats, nil
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

		img, err := a.publishImage(ctx, imgData, imagePublishMeta{
			ID:         pid,
			Title:      detail.Body.Title,
			ArtistName: detail.Body.UserName,
			ArtistID:   detail.Body.UserID,
			SourceURL:  sourceURL,
			Source:     "pixiv",
			Tags:       strings.Join(tags, " "),
			CreatedAt:  time.Now().Unix(),
		})
		if err != nil {
			stats.Failed++
			log.Printf("Pixiv publish failed pid=%s err=%v", pid, err)
		} else {
			stats.Downloaded++
			if stats.FirstID == "" {
				stats.FirstID = pid
			}
			log.Printf("Pixiv stored pid=%s size=%dx%d", pid, img.Width, img.Height)
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

type twitterStatusResp struct {
	Tweet   *twitterTweet `json:"tweet"`
	Message string        `json:"message"`
	Code    int           `json:"code"`
}

type twitterTweet struct {
	ID     string        `json:"id"`
	Text   string        `json:"text"`
	Author twitterAuthor `json:"author"`
	Media  *twitterMedia `json:"media"`
}

type twitterAuthor struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"screen_name"`
}

type twitterMedia struct {
	Photos []twitterMediaItem `json:"photos"`
}

type twitterMediaItem struct {
	URL string `json:"url"`
}

func (t *twitterTweet) photoURLs() []string {
	if t == nil || t.Media == nil || len(t.Media.Photos) == 0 {
		return nil
	}
	out := make([]string, 0, len(t.Media.Photos))
	seen := make(map[string]struct{}, len(t.Media.Photos))
	for _, item := range t.Media.Photos {
		u := strings.TrimSpace(item.URL)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func fetchTwitterTweet(ctx context.Context, domain, tweetID string) (*twitterTweet, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = defaultTwitterAPIDomain
	}

	endpoint := fmt.Sprintf("https://api.%s/_/status/%s", domain, tweetID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("twitter status %d", resp.StatusCode)
	}

	var payload twitterStatusResp
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Code != 0 && payload.Code != 200 {
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("twitter api code %d: %s", payload.Code, msg)
	}
	if payload.Tweet == nil {
		return nil, fmt.Errorf("tweet not found")
	}
	return payload.Tweet, nil
}

func buildTwitterTitle(text, tweetID, username string) string {
	text = strings.TrimSpace(text)
	if text != "" {
		first := strings.TrimSpace(strings.Split(text, "\n")[0])
		if first != "" {
			return truncateRunes(first, 120)
		}
	}
	username = normalizeTwitterUsername(username)
	if username != "" {
		return fmt.Sprintf("%s/%s", username, tweetID)
	}
	return "Twitter/" + tweetID
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) <= limit {
		return string(r)
	}
	return string(r[:limit])
}

func extractHashTags(text string) []string {
	matches := hashtagPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		tag := strings.TrimSpace(m[1])
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func buildTwitterImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	u, err := neturl.Parse(raw)
	if err != nil {
		return raw
	}
	if !strings.Contains(strings.ToLower(u.Hostname()), "twimg.com") {
		return raw
	}

	q := u.Query()
	q.Set("name", "orig")
	if q.Get("format") == "" {
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(u.Path)), ".")
		if ext != "" {
			q.Set("format", ext)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
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
