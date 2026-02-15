package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
)

const originStartPrefix = "o_"

func (a *App) buildOriginButtonURL(imageID string, storageMsgID int) string {
	if u := a.buildOriginDeepLink(imageID); u != "" {
		return u
	}
	if a.Cfg.StorageChannelID != 0 && storageMsgID > 0 {
		return channelMessageLink(a.Cfg.StorageChannelID, storageMsgID)
	}
	return ""
}

func (a *App) buildOriginDeepLink(imageID string) string {
	imageID = strings.TrimSpace(imageID)
	if imageID == "" {
		return ""
	}
	botUsername := strings.TrimSpace(a.Cfg.BotUsername)
	secret := strings.TrimSpace(a.Cfg.OriginLinkSecret)
	if botUsername == "" || secret == "" {
		return ""
	}

	exp := time.Now().Unix() + int64(a.Cfg.OriginLinkTTLSeconds)
	token := a.buildOriginToken(imageID, exp)
	return fmt.Sprintf("https://t.me/%s?start=%s", botUsername, token)
}

func (a *App) buildOriginToken(imageID string, exp int64) string {
	sig := a.signOriginToken(imageID, exp)
	return fmt.Sprintf("%s%d_%s_%s", originStartPrefix, exp, sig, imageID)
}

func (a *App) signOriginToken(imageID string, exp int64) string {
	payload := fmt.Sprintf("%d|%s", exp, imageID)
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
	if strings.TrimSpace(payload) == "" {
		return nil, true, nil
	}
	if !strings.HasPrefix(payload, originStartPrefix) {
		return nil, true, nil
	}

	imageID, err := a.verifyOriginToken(payload)
	if err != nil {
		return &TGIngestResult{Summary: "原图链接无效或已过期喵~"}, true, nil
	}

	originID, title, ok, err := a.DB.GetImageOrigin(ctx, imageID)
	if err != nil {
		return &TGIngestResult{Summary: "查询原图失败了喵~"}, true, nil
	}
	if !ok || strings.TrimSpace(originID) == "" {
		return &TGIngestResult{Summary: "这张图的原图暂时不可用喵~"}, true, nil
	}

	caption := strings.TrimSpace(title)
	if caption == "" {
		caption = "Original"
	}
	if _, err := a.TG.SendDocumentByFileID(ctx, msg.Chat.ID, originID, caption); err != nil {
		return &TGIngestResult{Summary: "原图发送失败了喵~"}, true, nil
	}
	return nil, true, nil
}

func (a *App) verifyOriginToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, originStartPrefix) {
		return "", fmt.Errorf("invalid prefix")
	}
	parts := strings.SplitN(token, "_", 4)
	if len(parts) != 4 {
		return "", fmt.Errorf("invalid token format")
	}

	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid token exp")
	}
	if time.Now().Unix() > exp {
		return "", fmt.Errorf("token expired")
	}

	sig := strings.TrimSpace(parts[2])
	imageID := strings.TrimSpace(parts[3])
	if imageID == "" || sig == "" {
		return "", fmt.Errorf("invalid token payload")
	}

	expected := a.signOriginToken(imageID, exp)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", fmt.Errorf("invalid token signature")
	}
	return imageID, nil
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
