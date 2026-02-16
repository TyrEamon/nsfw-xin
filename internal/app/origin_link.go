package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"pixiv-tg-gallery/internal/database"

	"github.com/go-telegram/bot/models"
)

const (
	originStartPrefix       = "o_"
	originBundleStartPrefix = "ob_"
)

func (a *App) buildOriginButtonURL(imageID, originID string, storageMsgID int) string {
	if u := a.buildOriginDeepLink(imageID); u != "" {
		return u
	}
	if a.Cfg.StorageChannelID != 0 && storageMsgID > 0 {
		return channelMessageLink(a.Cfg.StorageChannelID, storageMsgID)
	}
	_ = originID
	return ""
}

func (a *App) buildOriginDeepLink(imageID string) string {
	imageID = strings.TrimSpace(imageID)
	if imageID == "" || !a.isOriginDeepLinkEnabled() {
		return ""
	}

	exp := int64(0)
	if a.Cfg.OriginLinkTTLSeconds > 0 {
		exp = time.Now().Unix() + int64(a.Cfg.OriginLinkTTLSeconds)
	}
	token := a.buildOriginToken(imageID, exp)
	return fmt.Sprintf("https://t.me/%s?start=%s", strings.TrimSpace(a.Cfg.BotUsername), token)
}

func (a *App) buildOriginBundleURL(ctx context.Context, origins []discussionOriginLink) (string, error) {
	if len(origins) <= 1 || !a.isOriginDeepLinkEnabled() {
		return "", nil
	}

	items := make([]database.OriginBundleItem, 0, len(origins))
	for idx, origin := range origins {
		fileID := strings.TrimSpace(origin.OriginID)
		caption := strings.TrimSpace(origin.Label)

		if fileID == "" && strings.TrimSpace(origin.ImageID) != "" {
			existingFileID, title, ok, err := a.DB.GetImageOrigin(ctx, origin.ImageID)
			if err != nil {
				return "", err
			}
			if ok {
				fileID = strings.TrimSpace(existingFileID)
				if caption == "" {
					caption = strings.TrimSpace(title)
				}
			}
		}

		if fileID == "" {
			continue
		}
		if caption == "" {
			caption = "Original"
		}

		items = append(items, database.OriginBundleItem{
			Order:   idx,
			FileID:  fileID,
			Caption: caption,
		})
	}
	if len(items) <= 1 {
		return "", nil
	}

	bundleID := buildOriginBundleID(items)
	if bundleID == "" {
		return "", nil
	}
	if err := a.DB.UpsertOriginBundleItems(ctx, bundleID, items); err != nil {
		return "", err
	}

	exp := int64(0)
	if a.Cfg.OriginLinkTTLSeconds > 0 {
		exp = time.Now().Unix() + int64(a.Cfg.OriginLinkTTLSeconds)
	}
	token := a.buildOriginBundleToken(bundleID, exp)
	return fmt.Sprintf("https://t.me/%s?start=%s", strings.TrimSpace(a.Cfg.BotUsername), token), nil
}

func buildOriginBundleID(items []database.OriginBundleItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		if strings.TrimSpace(item.FileID) == "" {
			continue
		}
		_, _ = fmt.Fprintf(&b, "%d|%s\n", item.Order, strings.TrimSpace(item.FileID))
	}
	if b.Len() == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:12])
}

func (a *App) isOriginDeepLinkEnabled() bool {
	return strings.TrimSpace(a.Cfg.BotUsername) != "" && strings.TrimSpace(a.Cfg.OriginLinkSecret) != ""
}

func (a *App) buildOriginToken(imageID string, exp int64) string {
	sig := a.signOriginToken(imageID, exp)
	return fmt.Sprintf("%s%d_%s_%s", originStartPrefix, exp, sig, imageID)
}

func (a *App) buildOriginBundleToken(bundleID string, exp int64) string {
	sig := a.signOriginBundleToken(bundleID, exp)
	return fmt.Sprintf("%s%d_%s_%s", originBundleStartPrefix, exp, sig, bundleID)
}

func (a *App) signOriginToken(imageID string, exp int64) string {
	payload := fmt.Sprintf("%d|%s", exp, imageID)
	return a.signOriginPayload(payload)
}

func (a *App) signOriginBundleToken(bundleID string, exp int64) string {
	payload := fmt.Sprintf("bundle|%d|%s", exp, bundleID)
	return a.signOriginPayload(payload)
}

func (a *App) signOriginPayload(payload string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(a.Cfg.OriginLinkSecret)))
	_, _ = mac.Write([]byte(payload))
	sum := mac.Sum(nil)
	if len(sum) > 8 {
		sum = sum[:8]
	}
	return hex.EncodeToString(sum)
}

func parseStartPayload(text string) (payload string, isStart bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(strings.TrimSpace(fields[0]))
	if !strings.HasPrefix(cmd, "/start") {
		return "", false
	}
	if len(fields) < 2 {
		return "", true
	}
	return strings.TrimSpace(fields[1]), true
}

func (a *App) handleStartPayload(ctx context.Context, msg *models.Message, payload string) (*TGIngestResult, bool, error) {
	if msg == nil || msg.Chat.Type != "private" {
		return nil, false, nil
	}
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil, true, nil
	}

	switch {
	case strings.HasPrefix(payload, originStartPrefix):
		return a.handleStartSingleOrigin(ctx, msg, payload)
	case strings.HasPrefix(payload, originBundleStartPrefix):
		return a.handleStartOriginBundle(ctx, msg, payload)
	default:
		return nil, true, nil
	}
}

func (a *App) handleStartSingleOrigin(ctx context.Context, msg *models.Message, payload string) (*TGIngestResult, bool, error) {
	imageID, err := a.verifyOriginToken(payload)
	if err != nil {
		return &TGIngestResult{Summary: "\u539f\u56fe\u94fe\u63a5\u65e0\u6548\u6216\u5df2\u8fc7\u671f\u55b5~"}, true, nil
	}

	originID, title, ok, err := a.DB.GetImageOrigin(ctx, imageID)
	if err != nil {
		return &TGIngestResult{Summary: "\u67e5\u8be2\u539f\u56fe\u5931\u8d25\u4e86\u55b5~"}, true, nil
	}
	if !ok || strings.TrimSpace(originID) == "" {
		return &TGIngestResult{Summary: "\u8fd9\u5f20\u56fe\u7684\u539f\u56fe\u6682\u65f6\u4e0d\u53ef\u7528\u55b5~"}, true, nil
	}

	caption := strings.TrimSpace(title)
	if caption == "" {
		caption = "Original"
	}
	if _, err := a.TG.SendDocumentByFileID(ctx, msg.Chat.ID, originID, caption); err != nil {
		return &TGIngestResult{Summary: "\u539f\u56fe\u53d1\u9001\u5931\u8d25\u4e86\u55b5~"}, true, nil
	}
	return nil, true, nil
}

func (a *App) handleStartOriginBundle(ctx context.Context, msg *models.Message, payload string) (*TGIngestResult, bool, error) {
	bundleID, err := a.verifyOriginBundleToken(payload)
	if err != nil {
		return &TGIngestResult{Summary: "\u8fd9\u7ec4\u539f\u56fe\u94fe\u63a5\u65e0\u6548\u6216\u5df2\u8fc7\u671f\u55b5~"}, true, nil
	}

	items, err := a.DB.GetOriginBundleItems(ctx, bundleID)
	if err != nil {
		return &TGIngestResult{Summary: "\u67e5\u8be2\u8fd9\u7ec4\u539f\u56fe\u5931\u8d25\u4e86\u55b5~"}, true, nil
	}
	if len(items) == 0 {
		return &TGIngestResult{Summary: "\u8fd9\u7ec4\u539f\u56fe\u6682\u65f6\u4e0d\u53ef\u7528\u55b5~"}, true, nil
	}

	sent := 0
	failed := 0
	for i, item := range items {
		caption := strings.TrimSpace(item.Caption)
		if caption == "" {
			caption = fmt.Sprintf("Original %d/%d", i+1, len(items))
		}
		if _, err := a.TG.SendDocumentByFileID(ctx, msg.Chat.ID, item.FileID, caption); err != nil {
			failed++
			log.Printf("origin bundle send failed bundle=%s idx=%d err=%v", bundleID, i, err)
			continue
		}
		sent++
		if i < len(items)-1 {
			time.Sleep(300 * time.Millisecond)
		}
	}

	if sent == 0 {
		return &TGIngestResult{Summary: "\u8fd9\u7ec4\u539f\u56fe\u53d1\u9001\u5931\u8d25\u4e86\u55b5~"}, true, nil
	}
	if failed > 0 {
		return &TGIngestResult{Summary: fmt.Sprintf("\u539f\u56fe\u5957\u56fe\u53d1\u4e86 %d \u5f20\uff0c\u8fd8\u6709 %d \u5f20\u5931\u8d25\u4e86\u55b5~", sent, failed)}, true, nil
	}
	return &TGIngestResult{Summary: fmt.Sprintf("\u539f\u56fe\u5957\u56fe\u53d1\u9001\u5b8c\u6210\u55b5~ \u5171 %d \u5f20", sent)}, true, nil
}

func (a *App) verifyOriginToken(token string) (string, error) {
	exp, sig, imageID, err := parseOriginToken(token, originStartPrefix)
	if err != nil {
		return "", err
	}
	if a.Cfg.OriginLinkTTLSeconds > 0 && time.Now().Unix() > exp {
		return "", fmt.Errorf("token expired")
	}
	expected := a.signOriginToken(imageID, exp)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", fmt.Errorf("invalid token signature")
	}
	return imageID, nil
}

func (a *App) verifyOriginBundleToken(token string) (string, error) {
	exp, sig, bundleID, err := parseOriginToken(token, originBundleStartPrefix)
	if err != nil {
		return "", err
	}
	if a.Cfg.OriginLinkTTLSeconds > 0 && time.Now().Unix() > exp {
		return "", fmt.Errorf("token expired")
	}
	expected := a.signOriginBundleToken(bundleID, exp)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", fmt.Errorf("invalid token signature")
	}
	return bundleID, nil
}

func parseOriginToken(token, prefix string) (exp int64, sig, value string, err error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, prefix) {
		return 0, "", "", fmt.Errorf("invalid prefix")
	}
	parts := strings.SplitN(token, "_", 4)
	if len(parts) != 4 {
		return 0, "", "", fmt.Errorf("invalid token format")
	}

	exp, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, "", "", fmt.Errorf("invalid token exp")
	}
	sig = strings.TrimSpace(parts[2])
	value = strings.TrimSpace(parts[3])
	if sig == "" || value == "" {
		return 0, "", "", fmt.Errorf("invalid token payload")
	}
	return exp, sig, value, nil
}

func (a *App) isTGIngestAuthorized(msg *models.Message) bool {
	if msg == nil {
		return false
	}
	if msg.Chat.Type != "private" {
		return false
	}
	if msg.From == nil {
		return false
	}
	return a.Cfg.IsTGUserAllowed(msg.From.ID)
}
