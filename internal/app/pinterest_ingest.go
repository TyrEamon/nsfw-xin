package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	pinterestPageTimeout     = 30 * time.Second
	pinterestDownloadTimeout = 90 * time.Second
	pinterestDownloadRetries = 2
	pinterestRetryBackoff    = 1500 * time.Millisecond
	pinterestMaxPageBytes    = 8 << 20
)

var (
	pinterestImageURLPattern = regexp.MustCompile(`https?://i\.pinimg\.com/[^\s"'<>\\]+?\.(?:jpg|jpeg|png|webp)(?:\?[^\s"'<>\\]+)?`)
	pinterestVideoURLPattern = regexp.MustCompile(`https?://[^"'<>\\\s]+?\.(?:mp4|m3u8)(?:\?[^"'<>\\\s]+)?`)
)

type pinterestMedia struct {
	ID          string
	SourceURL   string
	Title       string
	Description string
	ImageURL    string
	VideoURL    string
}

func (a *App) ingestPinterestFromLink(ctx context.Context, item supportedLink) (*TGIngestResult, error) {
	media, err := fetchPinterestMedia(ctx, item.URL)
	if err != nil {
		return nil, err
	}
	if media.ID == "" {
		return nil, fmt.Errorf("pinterest pin id not found")
	}

	meta := imagePublishMeta{
		Title:      pinterestTitle(media),
		ArtistName: "Pinterest",
		ArtistID:   "pinterest",
		SourceURL:  media.SourceURL,
		SourceText: pinterestDescription(media.Description),
		Source:     "pinterest",
		CreatedAt:  time.Now().Unix(),
	}

	if media.VideoURL != "" {
		data, err := downloadWithHeadersRetry(ctx, media.VideoURL, media.SourceURL, pinterestDownloadTimeout, pinterestDownloadRetries, pinterestRetryBackoff)
		if err != nil {
			return nil, fmt.Errorf("pinterest video download: %w", err)
		}
		filename := buildPinterestMediaFilename(media.ID, media.VideoURL)
		if err := a.publishMotionNoDB(ctx, data, filename, false, meta); err != nil {
			return nil, err
		}
		return &TGIngestResult{
			Title:     meta.Title,
			SourceURL: media.SourceURL,
			Summary:   fmt.Sprintf("Pinterest %s motion published (+1, no DB)", media.ID),
		}, nil
	}

	if media.ImageURL == "" {
		return nil, fmt.Errorf("pinterest image not found")
	}

	imageID := "pinterest_" + media.ID
	if blocked, err := a.DB.IsBlocked(ctx, imageID); err == nil && blocked {
		return &TGIngestResult{ID: imageID, Title: meta.Title, SourceURL: media.SourceURL, Summary: fmt.Sprintf("Pinterest %s skipped: blocked", media.ID)}, nil
	}
	if exists, _ := a.DB.Exists(ctx, imageID); exists {
		return &TGIngestResult{ID: imageID, Title: meta.Title, SourceURL: media.SourceURL, Summary: fmt.Sprintf("Pinterest %s skipped: already exists", media.ID)}, nil
	}

	data, err := downloadWithHeadersRetry(ctx, media.ImageURL, media.SourceURL, pinterestDownloadTimeout, pinterestDownloadRetries, pinterestRetryBackoff)
	if err != nil {
		return nil, fmt.Errorf("pinterest image download: %w", err)
	}

	meta.ID = imageID
	img, err := a.publishImage(ctx, data, meta)
	if err != nil {
		return nil, err
	}
	return &TGIngestResult{
		ID:        img.ID,
		Title:     img.Title,
		SourceURL: img.SourceURL,
		Summary:   fmt.Sprintf("Pinterest %s ingested (+1)", media.ID),
	}, nil
}

func fetchPinterestMedia(ctx context.Context, rawURL string) (pinterestMedia, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return pinterestMedia{}, fmt.Errorf("pinterest url is empty")
	}

	page, finalURL, err := fetchPinterestPage(ctx, rawURL)
	if err != nil {
		return pinterestMedia{}, err
	}

	pinID := extractPinterestPinID(finalURL)
	if pinID == "" {
		pinID = extractPinterestPinID(rawURL)
	}
	if pinID == "" {
		pinID = extractPinterestPinID(page)
	}

	videoURL := choosePinterestVideoURL(ctx, extractPinterestVideoCandidates(page), finalURL)
	imageURL := ""
	if videoURL == "" {
		imageURL, err = choosePinterestImageURL(ctx, extractPinterestImageCandidates(page), finalURL)
		if err != nil {
			return pinterestMedia{}, err
		}
	}

	sourceURL := canonicalPinterestURL(pinID)
	if sourceURL == "" {
		sourceURL = finalURL
	}
	return pinterestMedia{
		ID:          pinID,
		SourceURL:   sourceURL,
		Title:       extractFanboxOpenGraphMeta(page, "og:title"),
		Description: extractFanboxOpenGraphMeta(page, "og:description"),
		ImageURL:    imageURL,
		VideoURL:    videoURL,
	}, nil
}

func fetchPinterestPage(ctx context.Context, rawURL string) (body string, finalURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	setPinterestHeaders(req, "https://www.pinterest.com/")

	client := &http.Client{Timeout: pinterestPageTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.Request.URL.String(), fmt.Errorf("pinterest status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, pinterestMaxPageBytes))
	if err != nil {
		return "", resp.Request.URL.String(), err
	}
	return string(data), resp.Request.URL.String(), nil
}

func setPinterestHeaders(req *http.Request, referer string) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ja,en-US;q=0.9,en;q=0.8,zh-CN;q=0.7,zh;q=0.6")
	if strings.TrimSpace(referer) != "" {
		req.Header.Set("Referer", referer)
	}
}

func extractPinterestPinID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := neturl.Parse(raw); err == nil {
		segments := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
		if id, ok := parsePinterestPath(segments); ok {
			return id
		}
	}
	re := regexp.MustCompile(`/pin/(\d+)`)
	if m := re.FindStringSubmatch(raw); len(m) >= 2 {
		return m[1]
	}
	return ""
}

func canonicalPinterestURL(pinID string) string {
	pinID = strings.TrimSpace(pinID)
	if pinID == "" {
		return ""
	}
	return fmt.Sprintf("https://www.pinterest.com/pin/%s/", pinID)
}

func extractPinterestImageCandidates(page string) []string {
	var candidates []string
	for _, variant := range pinterestTextVariants(page) {
		candidates = append(candidates, pinterestImageURLPattern.FindAllString(variant, -1)...)
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		item = cleanPinterestURL(item)
		if item == "" || !strings.Contains(item, "://i.pinimg.com/") {
			continue
		}
		key := strings.Split(item, "#")[0]
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func extractPinterestVideoCandidates(page string) []string {
	var candidates []string
	for _, variant := range pinterestTextVariants(page) {
		candidates = append(candidates, pinterestVideoURLPattern.FindAllString(variant, -1)...)
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		item = cleanPinterestURL(item)
		u, err := neturl.Parse(item)
		if err != nil || !strings.HasSuffix(strings.ToLower(u.Hostname()), "pinimg.com") {
			continue
		}
		if strings.ToLower(path.Ext(u.Path)) != ".mp4" {
			continue
		}
		key := strings.Split(item, "#")[0]
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return pinterestVideoScore(out[i]) > pinterestVideoScore(out[j])
	})
	return out
}

func pinterestTextVariants(text string) []string {
	unescaped := html.UnescapeString(text)
	slashUnescaped := strings.NewReplacer(`\/`, `/`, `\u002F`, `/`, `\u002f`, `/`).Replace(unescaped)
	if slashUnescaped == text {
		return []string{text}
	}
	return []string{text, unescaped, slashUnescaped}
}

func cleanPinterestURL(raw string) string {
	raw = html.UnescapeString(strings.TrimSpace(raw))
	raw = strings.NewReplacer(`\/`, `/`, `\u002F`, `/`, `\u002f`, `/`).Replace(raw)
	return strings.TrimRight(raw, `\`)
}

func choosePinterestImageURL(ctx context.Context, candidates []string, referer string) (string, error) {
	if len(candidates) == 0 {
		return "", fmt.Errorf("no pinterest image candidates")
	}

	expanded := make([]string, 0, len(candidates)*16)
	seen := make(map[string]struct{}, len(candidates)*16)
	for _, candidate := range candidates {
		for _, url := range expandPinterestImageURL(candidate) {
			if _, ok := seen[url]; ok {
				continue
			}
			seen[url] = struct{}{}
			expanded = append(expanded, url)
		}
	}
	sortPinterestImageURLs(expanded, candidates)

	var lastStatus string
	for i, imageURL := range expanded {
		if i >= 48 {
			break
		}
		ok, status, contentType := probePinterestMedia(ctx, imageURL, referer, "image/")
		lastStatus = fmt.Sprintf("%d %s", status, contentType)
		if ok {
			return imageURL, nil
		}
	}
	return "", fmt.Errorf("no downloadable pinterest image found (last probe: %s)", strings.TrimSpace(lastStatus))
}

func choosePinterestVideoURL(ctx context.Context, candidates []string, referer string) string {
	for _, videoURL := range candidates {
		ok, _, _ := probePinterestMedia(ctx, videoURL, referer, "video/")
		if ok {
			return videoURL
		}
	}
	return ""
}

func expandPinterestImageURL(raw string) []string {
	raw = cleanPinterestURL(raw)
	u, err := neturl.Parse(raw)
	if err != nil || strings.ToLower(u.Hostname()) != "i.pinimg.com" {
		return []string{raw}
	}

	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) < 2 {
		return []string{raw}
	}

	size := parts[0]
	sizes := []string{"originals", "1200x", "736x", "564x"}
	if containsString(sizes, size) {
		sizes = append([]string{size}, removeString(sizes, size)...)
	}

	currentExt := strings.ToLower(path.Ext(parts[len(parts)-1]))
	exts := []string{}
	if currentExt != "" {
		exts = append(exts, currentExt)
	}
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		if !containsString(exts, ext) {
			exts = append(exts, ext)
		}
	}

	out := make([]string, 0, len(sizes)*len(exts))
	for _, nextSize := range sizes {
		for _, ext := range exts {
			nextParts := append([]string{nextSize}, parts[1:]...)
			nextParts[len(nextParts)-1] = strings.TrimSuffix(nextParts[len(nextParts)-1], path.Ext(nextParts[len(nextParts)-1])) + ext
			nextURL := *u
			nextURL.Path = "/" + strings.Join(nextParts, "/")
			nextURL.RawQuery = ""
			out = append(out, nextURL.String())
		}
	}
	return out
}

func sortPinterestImageURLs(urls []string, sourceCandidates []string) {
	groupCounts := map[string]int{}
	for _, candidate := range sourceCandidates {
		groupCounts[pinterestImageSignature(candidate)]++
	}
	sort.SliceStable(urls, func(i, j int) bool {
		return groupCounts[pinterestImageSignature(urls[i])]*1000+pinterestImageScore(urls[i]) >
			groupCounts[pinterestImageSignature(urls[j])]*1000+pinterestImageScore(urls[j])
	})
}

func pinterestImageSignature(raw string) string {
	u, err := neturl.Parse(cleanPinterestURL(raw))
	if err != nil || strings.ToLower(u.Hostname()) != "i.pinimg.com" {
		return raw
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) < 2 {
		return strings.ToLower(u.EscapedPath())
	}
	tail := append([]string{}, parts[1:]...)
	tail[len(tail)-1] = strings.TrimSuffix(tail[len(tail)-1], path.Ext(tail[len(tail)-1]))
	return strings.ToLower(strings.Join(tail, "/"))
}

func pinterestImageScore(raw string) int {
	raw = strings.ToLower(cleanPinterestURL(raw))
	score := 0
	switch {
	case strings.Contains(raw, "/originals/"):
		score += 500
	case strings.Contains(raw, "/1200x/"):
		score += 400
	case strings.Contains(raw, "/736x/"):
		score += 300
	case strings.Contains(raw, "/564x/"):
		score += 200
	}
	if strings.HasSuffix(raw, ".png") {
		score += 10
	} else if strings.HasSuffix(raw, ".jpg") || strings.HasSuffix(raw, ".jpeg") {
		score += 8
	} else if strings.HasSuffix(raw, ".webp") {
		score += 5
	}
	if strings.Contains(raw, "/avatars/") || strings.Contains(raw, "/75x75") || strings.Contains(raw, "/60x60") || strings.Contains(raw, "/30x30") {
		score -= 1000
	}
	return score
}

func pinterestVideoScore(raw string) int {
	raw = strings.ToLower(cleanPinterestURL(raw))
	score := 0
	if strings.Contains(raw, "/expmp4/") {
		score += 300
	}
	switch {
	case strings.Contains(raw, "_1080w"):
		score += 1080
	case strings.Contains(raw, "_720w"):
		score += 720
	case strings.Contains(raw, "_540w"):
		score += 540
	case strings.Contains(raw, "_360w"):
		score += 360
	}
	return score
}

func probePinterestMedia(ctx context.Context, mediaURL, referer, contentPrefix string) (bool, int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, mediaURL, nil)
	if err != nil {
		return false, 0, ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "*/*")
	if strings.TrimSpace(referer) != "" {
		req.Header.Set("Referer", referer)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, 0, ""
	}
	defer resp.Body.Close()

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	return resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.HasPrefix(contentType, contentPrefix), resp.StatusCode, contentType
}

func pinterestTitle(media pinterestMedia) string {
	title := strings.TrimSpace(html.UnescapeString(media.Title))
	if title == "" {
		title = "Pinterest/" + media.ID
	}
	return truncateRunes(title, 120)
}

func pinterestDescription(raw string) string {
	return truncateRunes(strings.TrimSpace(html.UnescapeString(raw)), 500)
}

func buildPinterestMediaFilename(pinID, rawURL string) string {
	ext := ".mp4"
	if u, err := neturl.Parse(strings.TrimSpace(rawURL)); err == nil {
		if candidate := strings.ToLower(path.Ext(u.Path)); candidate == ".mp4" || candidate == ".webm" || candidate == ".mov" {
			ext = candidate
		}
	}
	pinID = strings.TrimSpace(pinID)
	if pinID == "" {
		pinID = fmt.Sprintf("%d", time.Now().Unix())
	}
	return fmt.Sprintf("pinterest_%s%s", pinID, ext)
}

func downloadWithHeadersRetry(ctx context.Context, sourceURL, referer string, timeout time.Duration, retries int, backoff time.Duration) ([]byte, error) {
	if retries < 0 {
		retries = 0
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	if backoff <= 0 {
		backoff = time.Second
	}

	attempts := retries + 1
	var lastErr error
	for i := 0; i < attempts; i++ {
		data, err := downloadWithHeadersTimeout(ctx, sourceURL, referer, timeout)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if i >= retries || !isRetryableDownloadErr(err) {
			break
		}

		wait := backoff * time.Duration(i+1)
		if waitErr := sleepWithContext(ctx, wait); waitErr != nil {
			break
		}
	}
	return nil, lastErr
}

func downloadWithHeadersTimeout(ctx context.Context, sourceURL, referer string, timeout time.Duration) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	client := &http.Client{Timeout: timeout}
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

func isRetryableDownloadErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() || netErr.Temporary() {
			return true
		}
	}

	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "temporary") || strings.Contains(msg, "connection reset") || strings.Contains(msg, "unexpected eof") {
		return true
	}
	if strings.Contains(msg, "download status 429") || strings.Contains(msg, "download status 500") || strings.Contains(msg, "download status 502") || strings.Contains(msg, "download status 503") || strings.Contains(msg, "download status 504") {
		return true
	}

	return false
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func removeString(items []string, value string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}
