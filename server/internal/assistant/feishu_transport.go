package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/feishubot"
)

// ---- the hub's transport ----

// botPost sends a card: as a reply to replyTo, else into the owner's chat; one Feishu refuses goes as rich text
// (fallback).
func (s *Service) botPost(ctx context.Context, replyTo string, card *feishubot.Card, fallback string) (string, error) {
	return s.botPostKeyed(ctx, replyTo, card, fallback, "")
}

// botPostKeyed is botPost with a key for a send into the chat that may be made again: the same key lands once.
func (s *Service) botPostKeyed(ctx context.Context, replyTo string, card *feishubot.Card, fallback, key string) (string, error) {
	return s.botPostTo(ctx, feishuTarget{}, replyTo, card, fallback, key)
}

// feishuTarget says where a feishu.post / feishu.text goes beyond the default: the chat to send into (ChatID; "" is the
// owner's private chat, the only one the hub accepts for now) and whether a reply goes into the parent's thread. The
// zero value is what the calls always did.
type feishuTarget struct {
	ChatID   string
	InThread bool
	// NoDetour: a reply whose parent is gone fails (the hub's no_detour) instead of landing in the chat as a message of
	// its own: for what belongs under that parent or nowhere (a to-do's line into its topic, a card into a topic).
	NoDetour bool
}

// params adds the target's fields to p; the zero target adds none, so the call is the same as before.
func (t feishuTarget) params(p map[string]any) map[string]any {
	if t.ChatID != "" {
		p["chat_id"] = t.ChatID
	}
	if t.InThread {
		p["in_thread"] = true
	}
	if t.NoDetour {
		p["no_detour"] = true
	}
	return p
}

// botPostTo is botPostKeyed to a target.
func (s *Service) botPostTo(ctx context.Context, to feishuTarget, replyTo string, card *feishubot.Card, fallback, key string) (string, error) {
	var res struct {
		MessageID string `json:"message_id"`
	}
	p := map[string]any{"reply_to": replyTo, "card": s.workbenchStamp(ctx, card).JSON(), "fallback": fallback}
	if key != "" {
		p["uuid"] = key
	}
	to.params(p)
	err := s.appCall(ctx, AppFeishu, FeishuPost, p, &res)
	return res.MessageID, err
}

// botNote replies to a message with a line of text.
func (s *Service) botNote(ctx context.Context, messageID, text string) {
	s.botNoteTo(ctx, feishuTarget{}, messageID, text)
}

// botNoteTo is botNote to a target.
func (s *Service) botNoteTo(ctx context.Context, to feishuTarget, messageID, text string) {
	if err := s.appCall(ctx, AppFeishu, FeishuText, to.params(map[string]any{"reply_to": messageID, "text": text}), nil); err != nil {
		s.logf("bots: note: %v", err)
	}
}

// botSay sends a line of text into the owner's chat.
func (s *Service) botSay(ctx context.Context, text string) {
	if err := s.appCall(ctx, AppFeishu, FeishuText, map[string]any{"text": text}, nil); err != nil {
		s.logf("bots: say: %v", err)
	}
}

func (s *Service) botUpdateCard(ctx context.Context, messageID string, card *feishubot.Card) error {
	return s.appCall(ctx, AppFeishu, FeishuUpdateCard, map[string]any{"message_id": messageID, "card": s.workbenchStamp(ctx, card).JSON()}, nil)
}

func (s *Service) botReact(ctx context.Context, messageID, emoji string) (string, error) {
	var res struct {
		ReactionID string `json:"reaction_id"`
	}
	err := s.appCall(ctx, AppFeishu, FeishuReact, map[string]any{"message_id": messageID, "emoji": emoji}, &res)
	return res.ReactionID, err
}

func (s *Service) botUnreact(ctx context.Context, messageID, reactionID string) {
	_ = s.appCall(ctx, AppFeishu, FeishuUnreact, map[string]any{"message_id": messageID, "reaction_id": reactionID}, nil)
}

func (s *Service) botMessages(ctx context.Context, messageID string) ([]feishubot.Item, error) {
	var res struct {
		Items []feishubot.Item `json:"items"`
	}
	err := s.appCall(ctx, AppFeishu, FeishuMessages, map[string]any{"message_id": messageID}, &res)
	return res.Items, err
}

// botName is a user's name (the hub's contact lookup; "someone" when it can't be read).
func (s *Service) botName(ctx context.Context, openID string) string {
	var res struct {
		Name string `json:"name"`
	}
	if err := s.appCall(ctx, AppFeishu, FeishuUserName, map[string]any{"open_id": openID}, &res); err != nil || res.Name == "" {
		return "某人"
	}
	return res.Name
}
