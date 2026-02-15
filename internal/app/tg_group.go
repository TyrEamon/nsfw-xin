package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"strings"
	"time"

	"pixiv-tg-gallery/internal/telegram"

	"github.com/go-telegram/bot/models"
)

const maxTGGroupItems = 200

type tgGroupSession struct {
	StartedAt int64
	Title     string
	Items     []tgGroupItem
}

type tgGroupItem struct {
	FileID    string
	MessageID int
	AddedAt   int64
}

type tgGroupPrepared struct {
	ID           string
	Data         []byte
	OriginID     string
	StorageMsgID int
	Width        int
	Height       int
}

type tgGroupStats struct {
	FirstID    string
	Title      string
	Downloaded int
	Skipped    int
	Failed     int
}

func parseTGGroupCommand(text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(strings.TrimSpace(fields[0]))
	if i := strings.Index(cmd, "@"); i > 0 {
		cmd = cmd[:i]
	}
	switch cmd {
	case "/group", "/final":
		return cmd, true
	default:
		return "", false
	}
}

func (a *App) handleTGGroupCommand(ctx context.Context, msg *models.Message, cmd string) (*TGIngestResult, error) {
	if msg == nil {
		return nil, nil
	}
	if !a.isTGIngestAuthorized(msg) {
		return &TGIngestResult{Summary: "No publish permission."}, nil
	}

	switch cmd {
	case "/group":
		title := parseTGGroupTitle(msg.Text)
		a.groupMu.Lock()
		a.tgGroupSessions[msg.Chat.ID] = tgGroupSession{
			StartedAt: time.Now().Unix(),
			Title:     title,
			Items:     make([]tgGroupItem, 0, 16),
		}
		a.groupMu.Unlock()
		if title == "" {
			return &TGIngestResult{Summary: "Group mode on. Send images/files, then /final."}, nil
		}
		return &TGIngestResult{Summary: fmt.Sprintf("Group mode on. Title: %s", title)}, nil
	case "/final":
		return a.finalizeTGGroup(ctx, msg)
	default:
		return nil, nil
	}
}

func parseTGGroupTitle(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) <= 1 {
		return ""
	}
	return strings.TrimSpace(strings.Join(fields[1:], " "))
}

func (a *App) appendTGGroupItem(msg *models.Message, fileID, title string) (bool, int, error) {
	if msg == nil {
		return false, 0, nil
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return false, 0, nil
	}

	a.groupMu.Lock()
	defer a.groupMu.Unlock()

	session, ok := a.tgGroupSessions[msg.Chat.ID]
	if !ok {
		return false, 0, nil
	}
	if len(session.Items) >= maxTGGroupItems {
		return true, len(session.Items), fmt.Errorf("group limit reached (%d)", maxTGGroupItems)
	}

	session.Items = append(session.Items, tgGroupItem{
		FileID:    fileID,
		MessageID: msg.ID,
		AddedAt:   time.Now().Unix(),
	})

	cleanTitle := strings.TrimSpace(title)
	if cleanTitle != "" && !strings.HasPrefix(cleanTitle, "/") {
		if session.Title == "" || strings.EqualFold(session.Title, "tg") {
			session.Title = cleanTitle
		}
	}

	a.tgGroupSessions[msg.Chat.ID] = session
	return true, len(session.Items), nil
}

func (a *App) finalizeTGGroup(ctx context.Context, msg *models.Message) (*TGIngestResult, error) {
	if msg == nil {
		return nil, nil
	}

	a.groupMu.Lock()
	session, ok := a.tgGroupSessions[msg.Chat.ID]
	if ok {
		delete(a.tgGroupSessions, msg.Chat.ID)
	}
	a.groupMu.Unlock()

	if !ok {
		return &TGIngestResult{Summary: "No active group. Use /group first."}, nil
	}
	if len(session.Items) == 0 {
		return &TGIngestResult{Summary: "Group is empty. Nothing to publish."}, nil
	}
	if session.StartedAt <= 0 {
		session.StartedAt = time.Now().Unix()
	}
	if strings.TrimSpace(session.Title) == "" {
		session.Title = "TG"
	}

	stats, err := a.publishTGGroup(ctx, msg.Chat.ID, session)
	if err != nil {
		return &TGIngestResult{Summary: "Group publish failed: " + err.Error()}, nil
	}
	if stats.Downloaded == 0 {
		return &TGIngestResult{Summary: fmt.Sprintf("Group done: +0, skipped %d, failed %d", stats.Skipped, stats.Failed)}, nil
	}
	return &TGIngestResult{
		ID:      stats.FirstID,
		Title:   stats.Title,
		Summary: fmt.Sprintf("Group done: +%d, skipped %d, failed %d", stats.Downloaded, stats.Skipped, stats.Failed),
	}, nil
}

func (a *App) publishTGGroup(ctx context.Context, chatID int64, session tgGroupSession) (*tgGroupStats, error) {
	stats := &tgGroupStats{Title: session.Title}
	prepared := make([]tgGroupPrepared, 0, len(session.Items))

	for idx, item := range session.Items {
		if ctx.Err() != nil {
			return stats, nil
		}
		pid := fmt.Sprintf("tggrp_%d_%d_p%d", chatID, session.StartedAt, idx)
		if blocked, err := a.DB.IsBlocked(ctx, pid); err == nil && blocked {
			stats.Skipped++
			continue
		}
		if exists, _ := a.DB.Exists(ctx, pid); exists {
			stats.Skipped++
			continue
		}

		data, _, err := a.TG.DownloadFile(ctx, item.FileID)
		if err != nil {
			stats.Failed++
			log.Printf("TG group download failed pid=%s err=%v", pid, err)
			continue
		}

		originID, storageMsgID, err := a.TG.SendOriginDocument(ctx, data, "Original")
		if err != nil {
			stats.Failed++
			log.Printf("TG group origin send failed pid=%s err=%v", pid, err)
			continue
		}

		width, height := detectImageSize(data)
		prepared = append(prepared, tgGroupPrepared{
			ID:           pid,
			Data:         data,
			OriginID:     originID,
			StorageMsgID: storageMsgID,
			Width:        width,
			Height:       height,
		})
		time.Sleep(1200 * time.Millisecond)
	}

	if len(prepared) == 0 {
		return stats, nil
	}

	groups := chunkTGPrepared(prepared, maxPixivAlbumGroup)
	for groupIdx, group := range groups {
		isLastGroup := groupIdx == len(groups)-1
		groupCaption := ""
		if isLastGroup {
			meta := normalizePublishMeta(imagePublishMeta{
				ID:         group[0].ID,
				Title:      session.Title,
				ArtistName: "Arts",
				ArtistID:   "none",
				SourceURL:  "none",
				Source:     "tg",
				CreatedAt:  time.Now().Unix(),
			})
			groupCaption = buildPreviewCaption(meta)
		}

		previewItems := make([]telegram.PreviewMedia, 0, len(group))
		for _, p := range group {
			previewItems = append(previewItems, telegram.PreviewMedia{
				Data:     p.Data,
				Filename: p.ID + "_preview.jpg",
				Width:    p.Width,
				Height:   p.Height,
			})
		}

		previewResults, err := a.TG.SendPreviewMediaGroup(ctx, previewItems, groupCaption)
		if err != nil {
			log.Printf("TG group media send failed group=%d err=%v fallback=single_preview", groupIdx+1, err)
			fallbackGroup := make([]tgGroupPrepared, 0, len(group))
			fallbackPreview := make([]telegram.PreviewSendResult, 0, len(group))
			for i, p := range group {
				caption := ""
				if i == 0 {
					caption = groupCaption
				}
				res, sendErr := a.TG.SendPreviewPhoto(ctx, p.Data, caption)
				if sendErr != nil {
					stats.Failed++
					log.Printf("TG group fallback preview failed pid=%s err=%v", p.ID, sendErr)
					continue
				}
				fallbackGroup = append(fallbackGroup, p)
				fallbackPreview = append(fallbackPreview, res)
			}
			group = fallbackGroup
			previewResults = fallbackPreview
		}

		if len(group) == 0 || len(previewResults) == 0 {
			continue
		}
		if len(group) != len(previewResults) {
			limit := len(group)
			if len(previewResults) < limit {
				limit = len(previewResults)
			}
			group = group[:limit]
			previewResults = previewResults[:limit]
		}

		discussionMsgID := 0
		if isLastGroup {
			anchorMeta := normalizePublishMeta(imagePublishMeta{
				ID:         group[0].ID,
				Title:      session.Title,
				ArtistName: "Arts",
				ArtistID:   "none",
				SourceURL:  "none",
				Source:     "tg",
				CreatedAt:  time.Now().Unix(),
			})
			discussionMsgID = a.sendDiscussionComment(ctx, anchorMeta, previewResults[0].PublishMsgID, group[0].StorageMsgID)
		}

		for i, p := range group {
			meta := normalizePublishMeta(imagePublishMeta{
				ID:         p.ID,
				Title:      session.Title,
				ArtistName: "Arts",
				ArtistID:   "none",
				SourceURL:  "none",
				Source:     "tg",
				CreatedAt:  time.Now().Unix(),
			})

			width := previewResults[i].Width
			height := previewResults[i].Height
			if width <= 0 {
				width = p.Width
			}
			if height <= 0 {
				height = p.Height
			}

			result := telegram.SendResult{
				PreviewID:    previewResults[i].PreviewID,
				OriginID:     p.OriginID,
				PublishMsgID: previewResults[i].PublishMsgID,
				StorageMsgID: p.StorageMsgID,
				Width:        width,
				Height:       height,
			}

			img, err := a.persistPublishedImage(ctx, meta, result, discussionMsgID)
			if err != nil {
				stats.Failed++
				log.Printf("TG group persist failed pid=%s err=%v", p.ID, err)
				continue
			}
			stats.Downloaded++
			if stats.FirstID == "" {
				stats.FirstID = img.ID
			}
		}

		time.Sleep(1500 * time.Millisecond)
	}

	return stats, nil
}

func chunkTGPrepared(items []tgGroupPrepared, size int) [][]tgGroupPrepared {
	if len(items) == 0 {
		return nil
	}
	if size <= 0 {
		size = maxPixivAlbumGroup
	}
	out := make([][]tgGroupPrepared, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		chunk := make([]tgGroupPrepared, end-start)
		copy(chunk, items[start:end])
		out = append(out, chunk)
	}
	return out
}

func detectImageSize(data []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}
