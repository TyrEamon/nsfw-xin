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
	Kind      incomingMediaKind
	Filename  string
	MessageID int
	AddedAt   int64
}

type tgGroupPrepared struct {
	ID           string
	Kind         incomingMediaKind
	Filename     string
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
		return &TGIngestResult{Summary: "\u54fc\uff0c\u8fd9\u4e2a\u529f\u80fd\u53ea\u7ed9\u4e3b\u4eba\u548c\u767d\u540d\u5355\u7528\u55b5~"}, nil
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
			return &TGIngestResult{Summary: "\u597d\u5566\uff0c\u5df2\u8fdb\u5165\u7ec4\u56fe\u6a21\u5f0f\u55b5~\u628a\u56fe\u90fd\u53d1\u5b8c\u540e\u518d\u53d1 /final\u3002"}, nil
		}
		return &TGIngestResult{Summary: fmt.Sprintf("\u52c9\u5f3a\u5e2e\u4f60\u8bb0\u4e0b\u6807\u9898\u5566\uff1a%s\n\u53d1\u5b8c\u8bb0\u5f97 /final \u55b5~", title)}, nil
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

func (a *App) appendTGGroupItem(msg *models.Message, media incomingMedia, title string) (bool, int, error) {
	if msg == nil {
		return false, 0, nil
	}
	media.FileID = strings.TrimSpace(media.FileID)
	if media.FileID == "" {
		return false, 0, nil
	}

	a.groupMu.Lock()
	defer a.groupMu.Unlock()

	session, ok := a.tgGroupSessions[msg.Chat.ID]
	if !ok {
		return false, 0, nil
	}
	if len(session.Items) >= maxTGGroupItems {
		return true, len(session.Items), fmt.Errorf("\u7ec4\u56fe\u4e0a\u9650\u8fbe\u5230(%d)", maxTGGroupItems)
	}

	session.Items = append(session.Items, tgGroupItem{
		FileID:    media.FileID,
		Kind:      media.Kind,
		Filename:  strings.TrimSpace(media.Filename),
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
		return &TGIngestResult{Summary: "\u4f60\u90fd\u6ca1\u5f00\u7ec4\u56fe\u6a21\u5f0f\u5440\uff0c\u5148 /group \u518d\u8bf4\u55b5~"}, nil
	}
	if len(session.Items) == 0 {
		return &TGIngestResult{Summary: "\u7ec4\u56fe\u91cc\u7a7a\u7a7a\u7684\uff0c\u81f3\u5c11\u53d1\u4e00\u5f20\u56fe\u518d /final \u55b5~"}, nil
	}
	if session.StartedAt <= 0 {
		session.StartedAt = time.Now().Unix()
	}
	if strings.TrimSpace(session.Title) == "" {
		session.Title = "TG"
	}

	stats, err := a.publishTGGroup(ctx, msg.Chat.ID, session)
	if err != nil {
		return &TGIngestResult{Summary: "\u7ec4\u56fe\u53d1\u5e03\u7ffb\u8f66\u4e86\u55b5\uff1a" + err.Error()}, nil
	}
	if stats.Downloaded == 0 {
		return &TGIngestResult{Summary: fmt.Sprintf("\u7ec4\u56fe\u53d1\u5e03\u5b8c\u6210\u55b5~ \u65b0\u589e0\uff0c\u8df3\u8fc7%d\uff0c\u5931\u8d25%d", stats.Skipped, stats.Failed)}, nil
	}
	return &TGIngestResult{
		ID:      stats.FirstID,
		Title:   stats.Title,
		Summary: fmt.Sprintf("\u7ec4\u56fe\u53d1\u5e03\u5b8c\u6210\u55b5~ \u65b0\u589e%d\uff0c\u8df3\u8fc7%d\uff0c\u5931\u8d25%d", stats.Downloaded, stats.Skipped, stats.Failed),
	}, nil
}

func (a *App) publishTGGroup(ctx context.Context, chatID int64, session tgGroupSession) (*tgGroupStats, error) {
	stats := &tgGroupStats{Title: session.Title}
	prepared := make([]tgGroupPrepared, 0, len(session.Items))

	for idx, item := range session.Items {
		if ctx.Err() != nil {
			return stats, nil
		}

		persistable := item.Kind == incomingMediaImage
		pid := fmt.Sprintf("tggrp_%d_%d_p%d", chatID, session.StartedAt, idx)
		if persistable {
			if blocked, err := a.DB.IsBlocked(ctx, pid); err == nil && blocked {
				stats.Skipped++
				continue
			}
			if exists, _ := a.DB.Exists(ctx, pid); exists {
				stats.Skipped++
				continue
			}
		}

		data, _, err := a.TG.DownloadFile(ctx, item.FileID)
		if err != nil {
			stats.Failed++
			log.Printf("TG group download failed pid=%s err=%v", pid, err)
			continue
		}

		filename := strings.TrimSpace(item.Filename)
		if filename == "" {
			filename = buildTGGroupFilename(pid, item.Kind)
		}

		originID, storageMsgID, err := a.TG.SendOriginDocumentWithFilename(ctx, data, filename, "Original")
		if err != nil {
			stats.Failed++
			log.Printf("TG group origin send failed pid=%s err=%v", pid, err)
			continue
		}

		width, height := 0, 0
		if persistable {
			width, height = detectImageSize(data)
		}

		prepared = append(prepared, tgGroupPrepared{
			ID:           pid,
			Kind:         item.Kind,
			Filename:     filename,
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

	if hasOnlyImagePrepared(prepared) {
		return a.publishTGGroupImageOnly(ctx, session, stats, prepared)
	}
	return a.publishTGGroupMixed(ctx, session, stats, prepared)
}

func hasOnlyImagePrepared(items []tgGroupPrepared) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.Kind != incomingMediaImage {
			return false
		}
	}
	return true
}

func buildTGGroupFilename(id string, kind incomingMediaKind) string {
	suffix := ".bin"
	switch kind {
	case incomingMediaImage:
		suffix = ".jpg"
	case incomingMediaAnimation:
		suffix = ".mp4"
	case incomingMediaVideo:
		suffix = ".mp4"
	}
	if strings.TrimSpace(id) == "" {
		id = "tg_group"
	}
	return id + suffix
}

func (a *App) publishTGGroupImageOnly(ctx context.Context, session tgGroupSession, stats *tgGroupStats, prepared []tgGroupPrepared) (*tgGroupStats, error) {
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
			originLinks := make([]discussionOriginLink, 0, len(group))
			for i, item := range group {
				originLinks = append(originLinks, discussionOriginLink{
					ImageID:      item.ID,
					OriginID:     item.OriginID,
					StorageMsgID: item.StorageMsgID,
					Label:        fmt.Sprintf("\u539f\u56fe%d", i+1),
				})
			}
			discussionMsgID = a.sendDiscussionCommentWithOrigins(ctx, anchorMeta, previewResults[0].PublishMsgID, originLinks)
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

func (a *App) publishTGGroupMixed(ctx context.Context, session tgGroupSession, stats *tgGroupStats, prepared []tgGroupPrepared) (*tgGroupStats, error) {
	type preparedResult struct {
		Prepared tgGroupPrepared
		Preview  telegram.PreviewSendResult
		Sent     bool
	}

	results := make([]preparedResult, len(prepared))
	groupCaption := buildPreviewCaption(normalizePublishMeta(imagePublishMeta{
		Title:      session.Title,
		ArtistName: "Arts",
		ArtistID:   "none",
		SourceURL:  "none",
		Source:     "tg",
		CreatedAt:  time.Now().Unix(),
	}))

	for i, item := range prepared {
		caption := ""
		if i == len(prepared)-1 {
			caption = groupCaption
		}

		var (
			preview telegram.PreviewSendResult
			err     error
		)
		switch item.Kind {
		case incomingMediaImage:
			preview, err = a.TG.SendPreviewPhoto(ctx, item.Data, caption)
		case incomingMediaAnimation:
			preview, err = a.TG.SendPreviewMotion(ctx, item.Data, item.Filename, caption, true)
		default:
			preview, err = a.TG.SendPreviewMotion(ctx, item.Data, item.Filename, caption, false)
		}
		if err != nil {
			stats.Failed++
			log.Printf("TG mixed group preview failed pid=%s kind=%s err=%v", item.ID, item.Kind, err)
			continue
		}

		results[i] = preparedResult{Prepared: item, Preview: preview, Sent: true}
		time.Sleep(1200 * time.Millisecond)
	}

	anchor := -1
	for i := len(results) - 1; i >= 0; i-- {
		if results[i].Sent {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		return stats, nil
	}

	anchorMeta := normalizePublishMeta(imagePublishMeta{
		Title:      session.Title,
		ArtistName: "Arts",
		ArtistID:   "none",
		SourceURL:  "none",
		Source:     "tg",
		CreatedAt:  time.Now().Unix(),
	})
	originLinks := make([]discussionOriginLink, 0, len(results))
	for _, item := range results {
		if !item.Sent {
			continue
		}
		originLinks = append(originLinks, discussionOriginLink{
			ImageID:      item.Prepared.ID,
			OriginID:     item.Prepared.OriginID,
			StorageMsgID: item.Prepared.StorageMsgID,
			Label:        fmt.Sprintf("\u539f\u56fe%d", len(originLinks)+1),
		})
	}
	discussionMsgID := a.sendDiscussionCommentWithOrigins(ctx, anchorMeta, results[anchor].Preview.PublishMsgID, originLinks)

	for _, result := range results {
		if !result.Sent {
			continue
		}

		if result.Prepared.Kind != incomingMediaImage {
			stats.Downloaded++
			continue
		}

		meta := normalizePublishMeta(imagePublishMeta{
			ID:         result.Prepared.ID,
			Title:      session.Title,
			ArtistName: "Arts",
			ArtistID:   "none",
			SourceURL:  "none",
			Source:     "tg",
			CreatedAt:  time.Now().Unix(),
		})

		width := result.Preview.Width
		height := result.Preview.Height
		if width <= 0 {
			width = result.Prepared.Width
		}
		if height <= 0 {
			height = result.Prepared.Height
		}

		persist := telegram.SendResult{
			PreviewID:    result.Preview.PreviewID,
			OriginID:     result.Prepared.OriginID,
			PublishMsgID: result.Preview.PublishMsgID,
			StorageMsgID: result.Prepared.StorageMsgID,
			Width:        width,
			Height:       height,
		}

		img, err := a.persistPublishedImage(ctx, meta, persist, discussionMsgID)
		if err != nil {
			stats.Failed++
			log.Printf("TG mixed group persist failed pid=%s err=%v", result.Prepared.ID, err)
			continue
		}
		stats.Downloaded++
		if stats.FirstID == "" {
			stats.FirstID = img.ID
		}
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
