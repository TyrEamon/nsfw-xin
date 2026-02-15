package app

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"pixiv-tg-gallery/internal/config"
	"pixiv-tg-gallery/internal/database"
	"pixiv-tg-gallery/internal/pixiv"
	"pixiv-tg-gallery/internal/telegram"

	"github.com/go-telegram/bot/models"
)

type App struct {
	Cfg   *config.Config
	DB    *database.Client
	TG    *telegram.Client
	Pixiv *pixiv.Client

	groupMu         sync.Mutex
	tgGroupSessions map[int64]tgGroupSession

	discussionMu       sync.Mutex
	pendingDiscussion  map[int]pendingDiscussionComment
	observedDiscussion map[int]discussionRelay
}

const pixivBootstrapStateKey = "pixiv_bootstrap_done"

type TGIngestResult struct {
	ID        string
	Title     string
	SourceURL string
	Summary   string
}

func New(cfg *config.Config, db *database.Client, tg *telegram.Client, pv *pixiv.Client) *App {
	return &App{
		Cfg:                cfg,
		DB:                 db,
		TG:                 tg,
		Pixiv:              pv,
		tgGroupSessions:    make(map[int64]tgGroupSession),
		pendingDiscussion:  make(map[int]pendingDiscussionComment),
		observedDiscussion: make(map[int]discussionRelay),
	}
}

func (a *App) HandleUpload(ctx context.Context, data []byte) error {
	imgID := fmt.Sprintf("upload_%d", time.Now().UnixNano())
	_, err := a.publishImage(ctx, data, imagePublishMeta{
		ID:         imgID,
		Title:      "upload",
		ArtistName: "Arts",
		ArtistID:   "none",
		SourceURL:  "none",
		Source:     "upload",
		CreatedAt:  time.Now().Unix(),
	})
	return err
}

func (a *App) HandleTGMessage(ctx context.Context, msg *models.Message) (*TGIngestResult, error) {
	if msg == nil {
		return nil, nil
	}
	if msg.Chat.ID == a.Cfg.DiscussionGroupID {
		a.HandleDiscussionRelay(ctx, msg)
		return nil, nil
	}
	if msg.Chat.ID == a.Cfg.PublishChannelID || msg.Chat.ID == a.Cfg.StorageChannelID {
		return nil, nil
	}

	if payload, isStart := parseStartPayload(msg.Text); isStart {
		result, handled, err := a.handleStartPayload(ctx, msg, payload)
		if handled {
			return result, err
		}
	}

	if action, ok := parseSpoilerCommand(msg.Text); ok {
		return a.handleSpoilerCommand(ctx, msg, action)
	}

	if cmd, ok := parseTGGroupCommand(msg.Text); ok {
		return a.handleTGGroupCommand(ctx, msg, cmd)
	}

	title := strings.TrimSpace(msg.Caption)
	if title == "" {
		title = strings.TrimSpace(msg.Text)
	}
	if title == "" {
		title = "TG"
	}

	var fileID string
	if msg.Document != nil {
		fileID = strings.TrimSpace(msg.Document.FileID)
	} else if len(msg.Photo) > 0 {
		fileID = strings.TrimSpace(msg.Photo[len(msg.Photo)-1].FileID)
	}

	links := extractSupportedLinks(msg.Text, msg.Caption)
	if fileID == "" && len(links) == 0 {
		return nil, nil
	}

	if !a.isTGIngestAuthorized(msg) {
		return &TGIngestResult{Summary: "No publish permission."}, nil
	}

	if fileID != "" {
		if queued, count, err := a.appendTGGroupItem(msg, fileID, title); err != nil {
			return &TGIngestResult{Summary: "Group queue failed: " + err.Error()}, nil
		} else if queued {
			return &TGIngestResult{Summary: fmt.Sprintf("Group queued: %d", count)}, nil
		}
	}

	if fileID == "" {
		return a.handleTGLinks(ctx, links)
	}

	data, _, err := a.TG.DownloadFile(ctx, fileID)
	if err != nil {
		return nil, err
	}

	imgID := fmt.Sprintf("tg_%d_%d", msg.Chat.ID, msg.ID)
	img, err := a.publishImage(ctx, data, imagePublishMeta{
		ID:         imgID,
		Title:      title,
		ArtistName: "Arts",
		ArtistID:   "none",
		SourceURL:  "none",
		Source:     "tg",
		CreatedAt:  time.Now().Unix(),
	})
	if err != nil {
		return nil, err
	}

	return &TGIngestResult{
		ID:        img.ID,
		Title:     img.Title,
		SourceURL: img.SourceURL,
		Summary:   fmt.Sprintf("Done meow~\nID: %s", img.ID),
	}, nil
}

func (a *App) CanHandleTGMessage(msg *models.Message) bool {
	if msg == nil {
		return false
	}
	if msg.Chat.ID == a.Cfg.DiscussionGroupID && msg.IsAutomaticForward {
		return true
	}
	if _, ok := parseStartPayload(msg.Text); ok {
		return true
	}
	if _, ok := parseSpoilerCommand(msg.Text); ok {
		return true
	}
	if _, ok := parseTGGroupCommand(msg.Text); ok {
		return true
	}
	if len(msg.Photo) > 0 || msg.Document != nil {
		return true
	}
	return len(extractSupportedLinks(msg.Text, msg.Caption)) > 0
}

func (a *App) StartPixivCrawler(ctx context.Context) {
	if a.Pixiv == nil || a.Cfg.PixivPHPSESSID == "" || a.Cfg.PixivUserID == "" {
		log.Println("Pixiv crawler disabled (missing PIXIV_PHPSESSID or PIXIV_USER_ID)")
		return
	}

	go func() {
		a.crawlPixivOnce(ctx)
		ticker := time.NewTicker(time.Duration(a.Cfg.PixivIntervalMinutes) * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.crawlPixivOnce(ctx)
			}
		}
	}()
}

func (a *App) crawlPixivOnce(ctx context.Context) {
	order := strings.ToLower(strings.TrimSpace(a.Cfg.PixivCrawlOrder))
	if order == "" {
		order = "desc"
	}
	bootstrapDone := false
	if val, ok, err := a.DB.GetCrawlerState(ctx, pixivBootstrapStateKey); err == nil && ok && val == "1" {
		bootstrapDone = true
	}
	maxPages := a.resolvePixivMaxPages(bootstrapDone)
	mode := "bootstrap"
	if bootstrapDone {
		mode = "incremental"
	}
	log.Printf(
		"Pixiv crawl started (mode=%s, order=%s, tag=%q, rest=%q, limit=%d, max_pages=%d)",
		mode,
		order,
		a.Cfg.PixivTag,
		a.Cfg.PixivRest,
		a.Cfg.PixivLimit,
		maxPages,
	)

	var err error
	if order == "asc" {
		err = a.crawlPixivAsc(ctx, maxPages)
	} else {
		err = a.crawlPixivDesc(ctx, maxPages)
	}
	if err != nil {
		log.Printf("Pixiv crawl failed: %v", err)
		log.Println("Pixiv crawl finished")
		return
	}

	if !bootstrapDone {
		if err := a.DB.SetCrawlerState(ctx, pixivBootstrapStateKey, "1"); err != nil {
			log.Printf("Pixiv bootstrap state write failed: %v", err)
		} else {
			log.Printf("Pixiv bootstrap state updated: %s=1", pixivBootstrapStateKey)
		}
	}

	log.Println("Pixiv crawl finished")
}

func (a *App) resolvePixivMaxPages(bootstrapDone bool) int {
	if bootstrapDone {
		if a.Cfg.PixivIncrementalMaxPages >= 0 {
			return a.Cfg.PixivIncrementalMaxPages
		}
		return 2
	}
	if a.Cfg.PixivBootstrapMaxPages >= 0 {
		return a.Cfg.PixivBootstrapMaxPages
	}
	return a.Cfg.PixivMaxPages
}

func (a *App) crawlPixivDesc(ctx context.Context, maxPages int) error {
	offset := 0
	page := 0
	limit := a.Cfg.PixivLimit

	for {
		ids, total, err := a.Pixiv.FetchBookmarkIDs(offset, limit, a.Cfg.PixivTag)
		if err != nil {
			return fmt.Errorf("pixiv bookmarks error: %w", err)
		}
		log.Printf("Pixiv page fetched (offset=%d, count=%d, total=%d)", offset, len(ids), total)
		if len(ids) == 0 {
			log.Printf("Pixiv returned no bookmark IDs (tag=%q, rest=%q)", a.Cfg.PixivTag, a.Cfg.PixivRest)
			return nil
		}

		for _, id := range ids {
			if ctx.Err() != nil {
				return nil
			}
			a.processPixivID(ctx, id)
		}

		page++
		offset += limit
		if shouldStopPageLoop(page, offset, total, maxPages) {
			log.Printf("Pixiv crawl stop condition reached (page=%d, offset=%d, total=%d)", page, offset, total)
			return nil
		}
		time.Sleep(4 * time.Second)
	}
}

func (a *App) crawlPixivAsc(ctx context.Context, maxPages int) error {
	offset := 0
	page := 0
	limit := a.Cfg.PixivLimit
	allIDs := make([]string, 0)

	for {
		ids, total, err := a.Pixiv.FetchBookmarkIDs(offset, limit, a.Cfg.PixivTag)
		if err != nil {
			return fmt.Errorf("pixiv bookmarks error: %w", err)
		}
		log.Printf("Pixiv page fetched (offset=%d, count=%d, total=%d)", offset, len(ids), total)
		if len(ids) == 0 {
			log.Printf("Pixiv returned no bookmark IDs (tag=%q, rest=%q)", a.Cfg.PixivTag, a.Cfg.PixivRest)
			break
		}
		allIDs = append(allIDs, ids...)

		page++
		offset += limit
		if shouldStopPageLoop(page, offset, total, maxPages) {
			break
		}
		time.Sleep(4 * time.Second)
	}
	log.Printf("Pixiv asc queue prepared (ids=%d)", len(allIDs))

	for i := len(allIDs) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return nil
		}
		a.processPixivID(ctx, allIDs[i])
	}
	return nil
}

func (a *App) processPixivID(ctx context.Context, id string) {
	log.Printf("Pixiv processing artwork id=%s", id)

	stats, err := a.ingestPixivArtwork(ctx, id, "")
	if err != nil {
		log.Printf("Pixiv artwork failed id=%s err=%v", id, err)
		return
	}

	log.Printf("Pixiv artwork done id=%s title=%q downloaded=%d skipped=%d failed=%d", id, stats.Title, stats.Downloaded, stats.Skipped, stats.Failed)
}

func shouldStopPageLoop(page, offset, total, maxPages int) bool {
	if maxPages > 0 && page >= maxPages {
		return true
	}
	if total > 0 && offset >= total {
		return true
	}
	return false
}

func (a *App) enqueueBackup(ctx context.Context, imageID string) {
	if !a.Cfg.BackupEnabled || strings.TrimSpace(imageID) == "" {
		return
	}
	if err := a.DB.EnqueueBackupTask(ctx, imageID); err != nil {
		log.Printf("[BACKUP] enqueue failed image=%s err=%v", imageID, err)
	}
}

func channelMessageLink(channelID int64, msgID int) string {
	s := fmt.Sprintf("%d", channelID)
	if strings.HasPrefix(s, "-100") {
		s = s[4:]
	} else if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	return fmt.Sprintf("https://t.me/c/%s/%d", s, msgID)
}
