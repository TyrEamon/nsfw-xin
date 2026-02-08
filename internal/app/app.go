package app

import (
	"context"
	"fmt"
	"log"
	"strings"
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
}

type TGIngestResult struct {
	ID        string
	Title     string
	SourceURL string
}

func New(cfg *config.Config, db *database.Client, tg *telegram.Client, pv *pixiv.Client) *App {
	return &App{Cfg: cfg, DB: db, TG: tg, Pixiv: pv}
}

func (a *App) HandleUpload(ctx context.Context, data []byte) error {
	title := "upload"
	artistName := "Arts"
	artistID := "none"
	sourceURL := "none"

	previewID, originID, previewMsgID, originMsgID, width, height, err := a.TG.SendPreviewAndOrigin(ctx, data, title)
	if err != nil {
		return err
	}

	msgID := chooseMsgID(previewMsgID, originMsgID)
	imgID := fmt.Sprintf("upload_%d", msgID)

	img := database.Image{
		ID:         imgID,
		PreviewID:  previewID,
		OriginID:   originID,
		Title:      title,
		ArtistName: artistName,
		ArtistID:   artistID,
		SourceURL:  sourceURL,
		Source:     "upload",
		Width:      width,
		Height:     height,
		CreatedAt:  time.Now().Unix(),
	}

	return a.DB.InsertImage(ctx, img)
}

func (a *App) HandleTGMessage(ctx context.Context, msg *models.Message) (*TGIngestResult, error) {
	if msg == nil {
		return nil, nil
	}
	if msg.Chat.ID == a.Cfg.ChannelID {
		return nil, nil
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
		fileID = msg.Document.FileID
	} else if len(msg.Photo) > 0 {
		fileID = msg.Photo[len(msg.Photo)-1].FileID
	}

	if fileID == "" {
		return nil, nil
	}

	data, _, err := a.TG.DownloadFile(ctx, fileID)
	if err != nil {
		return nil, err
	}

	previewID, originID, previewMsgID, originMsgID, width, height, err := a.TG.SendPreviewAndOrigin(ctx, data, title)
	if err != nil {
		return nil, err
	}

	msgID := chooseMsgID(previewMsgID, originMsgID)
	sourceURL := channelMessageLink(a.Cfg.ChannelID, msgID)

	img := database.Image{
		ID:         fmt.Sprintf("tg_%d", msgID),
		PreviewID:  previewID,
		OriginID:   originID,
		Title:      title,
		ArtistName: "Arts",
		ArtistID:   "none",
		SourceURL:  sourceURL,
		Source:     "tg",
		Width:      width,
		Height:     height,
		CreatedAt:  time.Now().Unix(),
	}

	if err := a.DB.InsertImage(ctx, img); err != nil {
		return nil, err
	}

	return &TGIngestResult{
		ID:        img.ID,
		Title:     img.Title,
		SourceURL: img.SourceURL,
	}, nil
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
	log.Printf("Pixiv crawl started (order=%s)", order)

	if order == "asc" {
		a.crawlPixivAsc(ctx)
	} else {
		a.crawlPixivDesc(ctx)
	}

	log.Println("Pixiv crawl finished")
}

func (a *App) crawlPixivDesc(ctx context.Context) {
	offset := 0
	page := 0
	limit := a.Cfg.PixivLimit

	for {
		ids, total, err := a.Pixiv.FetchBookmarkIDs(offset, limit, a.Cfg.PixivTag)
		if err != nil {
			log.Printf("pixiv bookmarks error: %v", err)
			return
		}
		if len(ids) == 0 {
			return
		}

		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			a.processPixivID(ctx, id)
		}

		page++
		offset += limit
		if shouldStopPageLoop(page, offset, total, a.Cfg.PixivMaxPages) {
			return
		}
		time.Sleep(4 * time.Second)
	}
}

func (a *App) crawlPixivAsc(ctx context.Context) {
	offset := 0
	page := 0
	limit := a.Cfg.PixivLimit
	allIDs := make([]string, 0)

	for {
		ids, total, err := a.Pixiv.FetchBookmarkIDs(offset, limit, a.Cfg.PixivTag)
		if err != nil {
			log.Printf("pixiv bookmarks error: %v", err)
			return
		}
		if len(ids) == 0 {
			break
		}
		allIDs = append(allIDs, ids...)

		page++
		offset += limit
		if shouldStopPageLoop(page, offset, total, a.Cfg.PixivMaxPages) {
			break
		}
		time.Sleep(4 * time.Second)
	}

	for i := len(allIDs) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return
		}
		a.processPixivID(ctx, allIDs[i])
	}
}

func (a *App) processPixivID(ctx context.Context, id string) {
	if exists, _ := a.DB.Exists(ctx, fmt.Sprintf("pixiv_%s_p0", id)); exists {
		return
	}

	detail, err := a.Pixiv.FetchDetail(id)
	if err != nil {
		return
	}
	if detail.Body.IllustType == 2 {
		return
	}

	tags := []string{}
	for _, t := range detail.Body.Tags.Tags {
		tags = append(tags, t.Tag)
	}

	pages, err := a.Pixiv.FetchPages(id)
	if err != nil {
		return
	}
	for i, p := range pages {
		pid := fmt.Sprintf("pixiv_%s_p%d", id, i)
		if exists, _ := a.DB.Exists(ctx, pid); exists {
			continue
		}
		imgData, err := a.Pixiv.Download(p.URL)
		if err != nil {
			continue
		}

		caption := detail.Body.Title
		previewID, originID, _, _, width, height, err := a.TG.SendPreviewAndOrigin(ctx, imgData, caption)
		if err != nil {
			continue
		}

		sourceURL := fmt.Sprintf("https://www.pixiv.net/artworks/%s", id)

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
			log.Printf("insert error: %v", err)
		}

		time.Sleep(2 * time.Second)
	}
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

func channelMessageLink(channelID int64, msgID int) string {
	s := fmt.Sprintf("%d", channelID)
	if strings.HasPrefix(s, "-100") {
		s = s[4:]
	} else if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	return fmt.Sprintf("https://t.me/c/%s/%d", s, msgID)
}

func chooseMsgID(previewMsgID, originMsgID int) int {
	if originMsgID > 0 {
		return originMsgID
	}
	return previewMsgID
}
