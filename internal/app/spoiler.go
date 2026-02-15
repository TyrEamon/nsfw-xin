package app

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/go-telegram/bot/models"
)

const tgSpoilerStateKey = "tg_preview_has_spoiler"

func (a *App) InitRuntimeFlags(ctx context.Context) {
	if a == nil || a.DB == nil || a.TG == nil {
		return
	}

	val, ok, err := a.DB.GetCrawlerState(ctx, tgSpoilerStateKey)
	if err != nil {
		log.Printf("runtime flag load failed key=%s err=%v", tgSpoilerStateKey, err)
		return
	}
	if !ok {
		if err := a.DB.SetCrawlerState(ctx, tgSpoilerStateKey, boolToState(a.TG.GetPreviewHasSpoiler())); err != nil {
			log.Printf("runtime flag init write failed key=%s err=%v", tgSpoilerStateKey, err)
		}
		return
	}

	if parsed, ok := parseStateBool(val); ok {
		a.TG.SetPreviewHasSpoiler(parsed)
		return
	}

	if err := a.DB.SetCrawlerState(ctx, tgSpoilerStateKey, boolToState(a.TG.GetPreviewHasSpoiler())); err != nil {
		log.Printf("runtime flag normalize write failed key=%s err=%v", tgSpoilerStateKey, err)
	}
}

func parseSpoilerCommand(text string) (action string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(strings.TrimSpace(fields[0]))
	if i := strings.Index(cmd, "@"); i > 0 {
		cmd = cmd[:i]
	}
	if cmd != "/spoiler" {
		return "", false
	}
	if len(fields) < 2 {
		return "status", true
	}

	action = strings.ToLower(strings.TrimSpace(fields[1]))
	switch action {
	case "on", "off", "status":
		return action, true
	default:
		return "help", true
	}
}

func (a *App) handleSpoilerCommand(ctx context.Context, msg *models.Message, action string) (*TGIngestResult, error) {
	if msg == nil {
		return nil, nil
	}
	if !a.isTGIngestAuthorized(msg) {
		return &TGIngestResult{Summary: "No publish permission."}, nil
	}

	switch action {
	case "status":
		return &TGIngestResult{Summary: spoilerStatusText(a.TG.GetPreviewHasSpoiler())}, nil
	case "on", "off":
		enabled := action == "on"
		a.TG.SetPreviewHasSpoiler(enabled)
		if err := a.DB.SetCrawlerState(ctx, tgSpoilerStateKey, boolToState(enabled)); err != nil {
			return &TGIngestResult{Summary: fmt.Sprintf("Spoiler %s (memory only, db write failed)", action)}, nil
		}
		return &TGIngestResult{Summary: spoilerStatusText(enabled)}, nil
	default:
		return &TGIngestResult{Summary: "Usage: /spoiler on|off|status"}, nil
	}
}

func spoilerStatusText(enabled bool) string {
	if enabled {
		return "Spoiler is ON"
	}
	return "Spoiler is OFF"
}

func parseStateBool(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true, true
	case "0", "false", "off", "no":
		return false, true
	default:
		return false, false
	}
}

func boolToState(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
