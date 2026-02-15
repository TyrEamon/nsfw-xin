package app

import (
	"context"
	"log"
	"time"

	"pixiv-tg-gallery/internal/telegram"

	"github.com/go-telegram/bot/models"
)

const discussionStateTTL = 15 * time.Minute

type pendingDiscussionComment struct {
	Text      string
	Buttons   telegram.DiscussionButtons
	CreatedAt time.Time
}

type discussionRelay struct {
	MessageID int
	SeenAt    time.Time
}

func (a *App) queueOrSendDiscussionComment(ctx context.Context, publishMessageID int, text string, buttons telegram.DiscussionButtons) (int, error) {
	if a.Cfg.DiscussionGroupID == 0 || publishMessageID <= 0 {
		return 0, nil
	}
	if text == "" {
		return 0, nil
	}

	now := time.Now()
	pending := pendingDiscussionComment{Text: text, Buttons: buttons, CreatedAt: now}

	a.discussionMu.Lock()
	a.cleanupDiscussionStateLocked(now)
	relay, hasRelay := a.observedDiscussion[publishMessageID]
	if !hasRelay {
		a.pendingDiscussion[publishMessageID] = pending
		a.discussionMu.Unlock()
		log.Printf("[DISCUSSION] queued publish_msg_id=%d waiting relay", publishMessageID)
		return 0, nil
	}
	a.discussionMu.Unlock()

	msgID, err := a.TG.SendDiscussionReply(ctx, relay.MessageID, text, buttons)
	if err != nil {
		a.discussionMu.Lock()
		a.pendingDiscussion[publishMessageID] = pending
		a.observedDiscussion[publishMessageID] = relay
		a.discussionMu.Unlock()
		return 0, err
	}

	a.discussionMu.Lock()
	delete(a.pendingDiscussion, publishMessageID)
	delete(a.observedDiscussion, publishMessageID)
	a.discussionMu.Unlock()

	log.Printf("[DISCUSSION] immediate sent publish_msg_id=%d relay_msg_id=%d comment_msg_id=%d", publishMessageID, relay.MessageID, msgID)
	return msgID, nil
}

func (a *App) HandleDiscussionRelay(ctx context.Context, msg *models.Message) {
	publishMessageID, ok := a.extractPublishMessageID(msg)
	if !ok {
		return
	}

	now := time.Now()
	a.discussionMu.Lock()
	a.cleanupDiscussionStateLocked(now)
	a.observedDiscussion[publishMessageID] = discussionRelay{MessageID: msg.ID, SeenAt: now}

	pending, hasPending := a.pendingDiscussion[publishMessageID]
	if hasPending {
		delete(a.pendingDiscussion, publishMessageID)
	}
	a.discussionMu.Unlock()

	if !hasPending {
		log.Printf("[DISCUSSION] relay observed publish_msg_id=%d relay_msg_id=%d", publishMessageID, msg.ID)
		return
	}

	commentMsgID, err := a.TG.SendDiscussionReply(ctx, msg.ID, pending.Text, pending.Buttons)
	if err != nil {
		a.discussionMu.Lock()
		a.pendingDiscussion[publishMessageID] = pending
		a.observedDiscussion[publishMessageID] = discussionRelay{MessageID: msg.ID, SeenAt: now}
		a.discussionMu.Unlock()
		log.Printf("[DISCUSSION] relay send failed publish_msg_id=%d relay_msg_id=%d err=%v", publishMessageID, msg.ID, err)
		return
	}

	a.discussionMu.Lock()
	delete(a.observedDiscussion, publishMessageID)
	a.discussionMu.Unlock()

	log.Printf("[DISCUSSION] relay sent publish_msg_id=%d relay_msg_id=%d comment_msg_id=%d", publishMessageID, msg.ID, commentMsgID)
}

func (a *App) cleanupDiscussionStateLocked(now time.Time) {
	for key, item := range a.pendingDiscussion {
		if now.Sub(item.CreatedAt) > discussionStateTTL {
			delete(a.pendingDiscussion, key)
		}
	}
	for key, item := range a.observedDiscussion {
		if now.Sub(item.SeenAt) > discussionStateTTL {
			delete(a.observedDiscussion, key)
		}
	}
}

func (a *App) extractPublishMessageID(msg *models.Message) (int, bool) {
	if msg == nil || a.Cfg.DiscussionGroupID == 0 {
		return 0, false
	}
	if msg.Chat.ID != a.Cfg.DiscussionGroupID {
		return 0, false
	}
	if msg.ForwardOrigin != nil && msg.ForwardOrigin.Type == models.MessageOriginTypeChannel && msg.ForwardOrigin.MessageOriginChannel != nil {
		origin := msg.ForwardOrigin.MessageOriginChannel
		if origin.Chat.ID == a.Cfg.PublishChannelID && origin.MessageID > 0 {
			return origin.MessageID, true
		}
	}
	if msg.IsAutomaticForward && msg.MessageThreadID > 0 {
		return msg.MessageThreadID, true
	}
	return 0, false
}
