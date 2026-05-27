package app

import (
	"context"
	"fmt"
	"html"
	"log"
	"strings"
	"unicode/utf8"

	"pixiv-tg-gallery/internal/database"
	"pixiv-tg-gallery/internal/telegram"
)

const (
	previewCaptionRuneLimit      = 950
	expandableQuoteRuneThreshold = 180
	expandableQuoteLineThreshold = 4
	maxDiscussionOriginButtons   = 10
)

type imagePublishMeta struct {
	ID         string
	Title      string
	ArtistName string
	ArtistID   string
	SourceURL  string
	SourceText string
	Source     string
	Tags       string
	CreatedAt  int64
}

func (a *App) publishImage(ctx context.Context, data []byte, meta imagePublishMeta) (database.Image, error) {
	meta = normalizePublishMeta(meta)

	result, err := a.TG.SendArtwork(ctx, data, telegram.SendOptions{
		PreviewCaption: buildPreviewCaption(meta),
		OriginCaption:  "Original",
	})
	if err != nil {
		return database.Image{}, err
	}

	discussionMsgID := a.sendDiscussionComment(ctx, meta, result.PublishMsgID, result.OriginID, result.StorageMsgID)
	return a.persistPublishedImage(ctx, meta, result, discussionMsgID)
}

type discussionOriginLink struct {
	ImageID      string
	OriginID     string
	StorageMsgID int
	Label        string
}

func (a *App) sendDiscussionComment(ctx context.Context, meta imagePublishMeta, publishMsgID int, originID string, storageMsgID int) int {
	origins := []discussionOriginLink{{ImageID: meta.ID, OriginID: originID, StorageMsgID: storageMsgID, Label: "\u539f\u56fe"}}
	return a.sendDiscussionCommentWithOrigins(ctx, meta, publishMsgID, origins)
}

func (a *App) sendDiscussionCommentWithOrigins(ctx context.Context, meta imagePublishMeta, publishMsgID int, origins []discussionOriginLink) int {
	if a.Cfg.DiscussionGroupID == 0 {
		return 0
	}
	comment := buildDiscussionComment(meta)
	if comment == "" {
		return 0
	}

	buttons := telegram.DiscussionButtons{DetailsURL: channelMessageLink(a.Cfg.PublishChannelID, publishMsgID)}
	originButtons := a.buildDiscussionOriginButtons(origins)
	if len(origins) > 1 {
		if bundleURL, err := a.buildOriginBundleURL(ctx, origins); err != nil {
			log.Printf("discussion origin bundle warning id=%s publish_msg_id=%d err=%v", meta.ID, publishMsgID, err)
		} else if strings.TrimSpace(bundleURL) != "" {
			buttons.OriginURL = bundleURL
		} else if len(originButtons) == 1 {
			buttons.OriginURL = originButtons[0].URL
		} else if len(originButtons) > 1 {
			buttons.OriginButtons = originButtons
		}
	} else if len(originButtons) == 1 {
		buttons.OriginURL = originButtons[0].URL
	}

	msgID, err := a.queueOrSendDiscussionComment(ctx, publishMsgID, comment, buttons)
	if err != nil {
		log.Printf("discussion comment warning id=%s publish_msg_id=%d err=%v", meta.ID, publishMsgID, err)
	}
	return msgID
}

func (a *App) buildDiscussionOriginButtons(origins []discussionOriginLink) []telegram.DiscussionLinkButton {
	if len(origins) == 0 {
		return nil
	}

	buttons := make([]telegram.DiscussionLinkButton, 0, len(origins))
	for _, origin := range origins {
		if len(buttons) >= maxDiscussionOriginButtons {
			break
		}
		url := a.buildOriginButtonURL(origin.ImageID, origin.OriginID, origin.StorageMsgID)
		if strings.TrimSpace(url) == "" {
			continue
		}
		text := strings.TrimSpace(origin.Label)
		if text == "" {
			if len(origins) == 1 {
				text = "\u539f\u56fe"
			} else {
				text = fmt.Sprintf("\u539f\u56fe%d", len(buttons)+1)
			}
		}
		buttons = append(buttons, telegram.DiscussionLinkButton{Text: text, URL: url})
	}
	return buttons
}

func (a *App) persistPublishedImage(ctx context.Context, meta imagePublishMeta, result telegram.SendResult, discussionMsgID int) (database.Image, error) {
	img := database.Image{
		ID:                meta.ID,
		PreviewID:         result.PreviewID,
		OriginID:          result.OriginID,
		Title:             meta.Title,
		ArtistName:        meta.ArtistName,
		ArtistID:          meta.ArtistID,
		SourceURL:         meta.SourceURL,
		SourceText:        meta.SourceText,
		Source:            meta.Source,
		Tags:              meta.Tags,
		Width:             result.Width,
		Height:            result.Height,
		CreatedAt:         meta.CreatedAt,
		PublishChannelID:  a.Cfg.PublishChannelID,
		PublishMessageID:  result.PublishMsgID,
		StorageChannelID:  a.Cfg.StorageChannelID,
		StorageMessageID:  result.StorageMsgID,
		DiscussionGroupID: a.Cfg.DiscussionGroupID,
		DiscussionMsgID:   discussionMsgID,
	}
	if err := a.DB.InsertImage(ctx, img); err != nil {
		return database.Image{}, err
	}
	a.enqueueBackup(ctx, img.ID)
	return img, nil
}

func normalizePublishMeta(meta imagePublishMeta) imagePublishMeta {
	meta.ID = strings.TrimSpace(meta.ID)
	meta.Title = clipRunes(strings.TrimSpace(meta.Title), 120)
	meta.ArtistName = clipRunes(strings.TrimSpace(meta.ArtistName), 80)
	meta.ArtistID = strings.TrimSpace(meta.ArtistID)
	meta.SourceURL = strings.TrimSpace(meta.SourceURL)
	meta.SourceText = clipRunes(strings.TrimSpace(meta.SourceText), 500)
	meta.Source = strings.TrimSpace(meta.Source)
	meta.Tags = strings.TrimSpace(meta.Tags)
	if meta.Title == "" {
		meta.Title = "Untitled"
	}
	if meta.ArtistName == "" {
		meta.ArtistName = "Arts"
	}
	if meta.ArtistID == "" {
		meta.ArtistID = "none"
	}
	if meta.SourceURL == "" {
		meta.SourceURL = "none"
	}
	if meta.Source == "" {
		meta.Source = "unknown"
	}
	return meta
}

func buildPreviewCaption(meta imagePublishMeta) string {
	title := html.EscapeString(meta.Title)
	artist := html.EscapeString(meta.ArtistName)
	sourceURL := strings.TrimSpace(meta.SourceURL)

	header := fmt.Sprintf("%s / %s", title, artist)
	if !isNoneLike(sourceURL) {
		header = fmt.Sprintf("<a href=\"%s\">%s</a> / %s", html.EscapeString(sourceURL), title, artist)
	}

	parts := []string{header}
	if meta.SourceText != "" {
		parts = append(parts, buildPreviewQuote(meta.SourceText))
	}
	tagLine := buildTagLine(meta.Tags)
	if tagLine != "" {
		parts = append(parts, buildPreviewQuote(tagLine))
	}

	caption := strings.Join(parts, "\n")
	if utf8.RuneCountInString(caption) > previewCaptionRuneLimit {
		caption = clipRunes(caption, previewCaptionRuneLimit)
	}
	return caption
}

func buildPreviewQuote(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	escaped := html.EscapeString(text)
	if shouldUseExpandableQuote(text) {
		return "<blockquote expandable>" + escaped + "</blockquote>"
	}
	return "<blockquote>" + escaped + "</blockquote>"
}

func shouldUseExpandableQuote(text string) bool {
	if utf8.RuneCountInString(text) >= expandableQuoteRuneThreshold {
		return true
	}

	lineCount := 1
	for _, r := range text {
		if r != '\n' {
			continue
		}
		lineCount++
		if lineCount >= expandableQuoteLineThreshold {
			return true
		}
	}
	return false
}

func buildDiscussionComment(meta imagePublishMeta) string {
	lines := make([]string, 0, 2)
	if shouldShowSourceLine(meta.Source) && !isNoneLike(meta.SourceURL) {
		lines = append(lines, strings.TrimSpace(meta.SourceURL))
	}
	lines = append(lines, "\u70b9\u51fb\u4e0b\u65b9\u6309\u94ae\u5728\u79c1\u804a\u4e2d\u83b7\u53d6\u539f\u56fe\u6587\u4ef6")
	return strings.Join(lines, "\n")
}

func shouldShowSourceLine(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "pixiv", "twitter", "yande", "pinterest":
		return true
	default:
		return false
	}
}

func buildTagLine(tags string) string {
	if strings.TrimSpace(tags) == "" {
		return ""
	}
	fields := strings.Fields(tags)
	if len(fields) == 0 {
		return ""
	}

	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	total := 0
	for _, f := range fields {
		tag := strings.TrimSpace(strings.TrimLeft(f, "#"))
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		item := "#" + tag
		if total+len(item)+1 > 320 {
			break
		}
		out = append(out, item)
		total += len(item) + 1
	}
	return strings.Join(out, " ")
}

func clipRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func isNoneLike(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "" || v == "none" || v == "-"
}
