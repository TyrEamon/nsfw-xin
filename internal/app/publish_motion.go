package app

import (
	"context"
	"strings"
	"time"
)

func (a *App) publishMotionNoDB(ctx context.Context, data []byte, filename string, asAnimation bool, meta imagePublishMeta) error {
	meta = normalizePublishMeta(meta)

	previewRes, err := a.TG.SendPreviewMotion(ctx, data, filename, buildPreviewCaption(meta), asAnimation)
	if err != nil {
		return err
	}
	originID, storageMsgID, err := a.TG.SendOriginDocumentWithFilename(ctx, data, filename, "Original")
	if err != nil {
		return err
	}

	commentMeta := meta
	commentMeta.ID = ""
	a.sendDiscussionComment(ctx, commentMeta, previewRes.PublishMsgID, originID, storageMsgID)
	return nil
}

func (a *App) publishIncomingMotion(ctx context.Context, data []byte, media incomingMedia, title string) error {
	filename := strings.TrimSpace(media.Filename)
	if filename == "" {
		if media.isAnimation() {
			filename = "tg_animation.mp4"
		} else {
			filename = "tg_video.mp4"
		}
	}
	meta := imagePublishMeta{
		Title:      title,
		ArtistName: "Arts",
		ArtistID:   "none",
		SourceURL:  "none",
		Source:     "tg",
		CreatedAt:  time.Now().Unix(),
	}
	return a.publishMotionNoDB(ctx, data, filename, media.isAnimation(), meta)
}
