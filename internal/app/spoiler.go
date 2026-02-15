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

	switch cmd {
	case "/spoilerstatus":
		return "status", true
	case "/spoileron":
		return "on", true
	case "/spoileroff":
		return "off", true
	default:
		return "", false
	}
}

func (a *App) handleSpoilerCommand(ctx context.Context, msg *models.Message, action string) (*TGIngestResult, error) {
	if msg == nil {
		return nil, nil
	}
	if !a.isTGIngestAuthorized(msg) {
		return &TGIngestResult{Summary: "\u54fc\uff0c\u8fd9\u4e2a\u529f\u80fd\u53ea\u7ed9\u4e3b\u4eba\u548c\u767d\u540d\u5355\u7528\u55b5~"}, nil
	}

	switch action {
	case "status":
		return &TGIngestResult{Summary: spoilerStatusText(a.TG.GetPreviewHasSpoiler())}, nil
	case "on", "off":
		enabled := action == "on"
		a.TG.SetPreviewHasSpoiler(enabled)
		if err := a.DB.SetCrawlerState(ctx, tgSpoilerStateKey, boolToState(enabled)); err != nil {
			state := "off"
			if enabled {
				state = "on"
			}
			return &TGIngestResult{Summary: fmt.Sprintf("\u8ff7\u96fe\u5df2\u5207\u5230%s\u55b5~\uff08\u4f46\u5199\u5165D1\u5931\u8d25\uff0c\u4ec5\u672c\u6b21\u751f\u6548\uff09", state)}, nil
		}
		return &TGIngestResult{Summary: spoilerStatusText(enabled)}, nil
	default:
		return &TGIngestResult{Summary: "\u7528\u6cd5\uff1a/spoilerstatus /spoileron /spoileroff"}, nil
	}
}

func spoilerStatusText(enabled bool) string {
	if enabled {
		return "\u5f53\u524d\u8ff7\u96fe\u6a21\u5f0f\uff1a\u5f00\u542f\u55b5~"
	}
	return "\u5f53\u524d\u8ff7\u96fe\u6a21\u5f0f\uff1a\u5173\u95ed\u55b5~"
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
