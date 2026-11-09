package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The channels' transport the assistant asks for (app.call feishu.*, channels.*, interactions.resolve), moved here
// from the hub with the bots and the plugin channels: the assistant is in this process, so these calls come in
// directly (AppCall), not over the bus.

// ---- the transport the assistant asks for (app.call) ----

const (
	botImageMax = 3 << 20 // a picture's base64 must stay under the model's 5 MB
	botFileMax  = 50 << 20
)

// feishuChatView is what the assistant shows of the bot's chat (feishu.view), for GET /api/v1/bots/feishu.
func (h *Manager) feishuChatView(ctx context.Context) (assistantwire.FeishuView, error) {
	var v assistantwire.FeishuView
	if !h.assistantUp() {
		return v, errBusOffline
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := h.callAssistant(ctx, assistantwire.MethodFeishuView, map[string]any{}, &v)
	return v, err
}

// channelsAppCall answers the channels' transport methods; false for the others.
func (h *Manager) channelsAppCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	var out any
	var err error
	switch p.AppID {
	case assistantwire.AppFeishu:
		out, err = h.feishuCall(ctx, p.Method, p.Params)
	case assistantwire.AppChannels:
		out, err = h.channelsCall(ctx, p.Method, p.Params)
	case assistantwire.AppInteractions:
		if p.Method != assistantwire.InteractionsOwnerAction {
			return nil, false, nil
		}
		var a struct {
			InteractionID string `json:"interaction_id"`
			Action        string `json:"action"`
			Channel       string `json:"channel"`
			Kind          string `json:"kind"`
		}
		if err := json.Unmarshal(p.Params, &a); err != nil {
			return nil, true, err
		}
		// The assistant answers a card from a channel only with the owner's action handed over with that press or
		// message in that channel (a card's for that card, a message's for a kind it takes typed), once
		// (fleet_routes.go channelResolve there; kind is the queue's).
		err = h.takeOwnerAction(a.Action, a.Channel, strings.TrimSpace(a.InteractionID), a.Kind)
		out = map[string]any{}
	default:
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	raw, err := json.Marshal(out)
	return raw, true, err
}

func (h *Manager) feishuCall(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var a struct {
		ReplyTo    string               `json:"reply_to"`
		Card       string               `json:"card"`
		Fallback   string               `json:"fallback"`
		UUID       string               `json:"uuid"`
		NoDetour   bool                 `json:"no_detour"` // feishu.post / feishu.text: a reply to a parent that is gone fails, it is not sent into the chat
		Text       string               `json:"text"`
		MessageID  string               `json:"message_id"`
		Emoji      string               `json:"emoji"`
		ReactionID string               `json:"reaction_id"`
		Key        string               `json:"key"`
		Kind       string               `json:"kind"`
		Name       string               `json:"name"`
		MachineID  string               `json:"machine_id"`
		OpenID     string               `json:"open_id"`
		Enabled    *bool                `json:"enabled"`
		Features   map[string]bool      `json:"features"`
		CotID      string               `json:"cot_id"`
		Events     []feishubot.CotEvent `json:"events"`
		Reason     string               `json:"reason"`
		ChatID     string               `json:"chat_id"`   // feishu.post / feishu.text: the chat to send into (default: the owner's)
		InThread   bool                 `json:"in_thread"` // feishu.post / feishu.text: a reply goes into the parent's thread
		Exclusive  bool                 `json:"exclusive"` // feishu.pin: the bot's other pins in the chat come off
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
	}
	switch method {
	case assistantwire.FeishuStatusGet:
		api, set := h.botAPI()
		nudge := set.nudgeAfter()
		st := assistantwire.FeishuStatus{Ready: api != nil, Enabled: set.Enabled, Features: assistantwire.FeishuFeatures(set.Features), NudgeAfter: &nudge}
		if set.Owner != nil {
			st.Owner, st.PairedAt = set.Owner.OpenID, set.Owner.At
			st.Chats = botSendChats(set)
			st.HomeChat = set.homeChat()
		}
		if h.cfg != nil {
			st.PublicURL = h.cfg.PublicURL
		}
		return st, nil
	case assistantwire.FeishuConfigure:
		return map[string]any{}, h.configureFeishu(ctx, a.Enabled, a.Features)
	case assistantwire.FeishuTest:
		return map[string]any{}, h.testFeishuBot(ctx)
	}
	api, set := h.botAPI()
	if api == nil {
		return nil, errBotOff
	}
	switch method {
	case assistantwire.FeishuPost:
		if a.Card == "" {
			return nil, errors.New("feishu.post needs a card")
		}
		chatID, err := botSendChat(set, a.ChatID)
		if err != nil {
			return nil, err
		}
		parent, err := h.checkReplyParent(ctx, api, set, a.ReplyTo)
		if err != nil {
			return nil, err
		}
		id, err := h.botPostJSONKeyedOpt(ctx, api, chatID, a.ReplyTo, a.Card, a.Fallback, callerKey(a.UUID, feishuMaxUUID-4), a.InThread, a.NoDetour)
		if err == nil {
			h.rememberMessageChat(id, firstNonEmpty(parent, chatID)) // what it sent is in a chat the bot may use
		}
		return map[string]any{"message_id": id}, err
	case assistantwire.FeishuText:
		noDetour := a.NoDetour
		// A reply under the owner's "/ai" in a group that is not opted in (#6146) is the one send open to such a group.
		chatID, found, err := aiSourceReply(set, a.ChatID, a.ReplyTo, a.Text, h.bots.clock().UTC())
		if err != nil {
			return nil, err
		}
		parent := chatID
		if found {
			if !h.useAISourceReply(a.ReplyTo) {
				return nil, errBotSourceReply
			}
			noDetour = true // its original gone: the reply is lost, not sent into a chat the bot may not write in
		} else {
			if chatID, err = botSendChat(set, a.ChatID); err != nil {
				return nil, err
			}
			if parent, err = h.checkReplyParent(ctx, api, set, a.ReplyTo); err != nil {
				return nil, err
			}
		}
		// With no parent the key is unused: replyOrSend sends with its own fresh one-shot key.
		// The caller's key, when it names one, in place of the reply's own (parent, purpose, content): two lines with
		// the same words under one parent are two messages, which the content-derived key would have Feishu drop as a repeat.
		replyUUID := feishuReplyUUIDIn(a.ReplyTo, "t", a.Text, a.InThread)
		if k := callerKey(a.UUID, feishuMaxUUID); k != "" {
			replyUUID = k
		}
		id, _, err := h.replyOrSendOpt(ctx, api, chatID, a.ReplyTo, feishubot.TypeText, feishubot.TextContent(a.Text), replyUUID, a.InThread, noDetour)
		if err == nil && !found {
			h.rememberMessageChat(id, firstNonEmpty(parent, chatID))
		}
		return map[string]any{"message_id": id}, err
	case assistantwire.FeishuUpdateCard:
		return map[string]any{}, api.UpdateCard(ctx, a.MessageID, a.Card)
	case assistantwire.FeishuPin:
		if err := checkOwnDMMessage(ctx, api, set, a.MessageID); err != nil {
			return nil, err
		}
		if err := api.Pin(ctx, a.MessageID); err != nil {
			return nil, feishuCoded(err)
		}
		if a.Exclusive {
			if err := unpinBotOthers(ctx, api, set.Owner.ChatID, a.MessageID); err != nil {
				return nil, feishuCoded(err)
			}
		}
		return map[string]any{}, nil
	case assistantwire.FeishuRecall:
		if err := checkOwnDMMessage(ctx, api, set, a.MessageID); err != nil {
			return nil, err
		}
		return map[string]any{}, feishuCoded(api.Recall(ctx, a.MessageID))
	case assistantwire.FeishuCotCreate:
		// The agent message is made in the owner's DM, so the message it answers has to be in the DM too.
		parent, err := h.checkReplyParent(ctx, api, set, a.ReplyTo)
		if err != nil {
			return nil, err
		}
		if parent != "" && parent != set.Owner.ChatID {
			return nil, errBotReplyChat
		}
		cot, id, err := api.CotCreate(ctx, set.Owner.ChatID, a.ReplyTo)
		if err == nil {
			h.rememberMessageChat(id, set.Owner.ChatID)
		}
		return map[string]any{"cot_id": cot, "message_id": id}, feishuCoded(err)
	case assistantwire.FeishuCotAppend:
		return map[string]any{}, feishuCoded(api.CotAppend(ctx, a.CotID, a.MessageID, a.Events))
	case assistantwire.FeishuCotComplete:
		return map[string]any{}, feishuCoded(api.CotComplete(ctx, a.CotID, a.MessageID, a.Reason))
	case assistantwire.FeishuReact:
		if err := h.checkReactTarget(ctx, api, set, a.MessageID); err != nil {
			return nil, err
		}
		id, err := api.React(ctx, a.MessageID, a.Emoji)
		return map[string]any{"reaction_id": id}, err
	case assistantwire.FeishuUnreact:
		if err := h.checkReactTarget(ctx, api, set, a.MessageID); err != nil {
			return nil, err
		}
		return map[string]any{}, api.Unreact(ctx, a.MessageID, a.ReactionID)
	case assistantwire.FeishuMessages:
		items, err := api.Message(ctx, a.MessageID)
		return map[string]any{"items": items}, err
	case assistantwire.FeishuUserName:
		return map[string]any{"name": h.botName(ctx, api, a.OpenID)}, nil
	case assistantwire.FeishuResource:
		data, mime, err := api.Resource(ctx, a.MessageID, a.Key, a.Kind, botFileMax)
		if err != nil {
			return nil, err
		}
		if len(data) > botImageMax {
			return map[string]any{"mime": mime, "too_large": true}, nil
		}
		return map[string]any{"data": data, "mime": mime}, nil
	case assistantwire.FeishuSave:
		m := h.localMachine(a.MachineID)
		if m == nil {
			return nil, noDevice(a.MachineID)
		}
		data, _, err := api.Resource(ctx, a.MessageID, a.Key, a.Kind, botFileMax)
		if errors.Is(err, feishubot.ErrTooLarge) {
			return map[string]any{"too_large": true}, nil // the rest of the message still goes
		}
		if err != nil {
			return nil, err
		}
		path, err := h.uploadBytes(ctx, m, firstNonEmpty(a.Name, a.Key), data)
		return map[string]any{"path": path}, err
	}
	return nil, fmt.Errorf("app %s has no method %s", assistantwire.AppFeishu, method)
}

func (h *Manager) channelsCall(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var a struct {
		Channel string            `json:"channel"`
		On      bool              `json:"on"`
		Step    string            `json:"step"`
		Values  map[string]string `json:"values"`
		channel.SendParams
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
	}
	switch method {
	case assistantwire.ChannelsList:
		return map[string]any{"channels": h.channelList(ctx)}, nil
	case assistantwire.ChannelsSend:
		return h.channelSendPiece(ctx, a.Channel, a.SendParams)
	case assistantwire.ChannelsTyping:
		return map[string]any{}, h.extensionCall(ctx, a.Channel, channel.MTyping, channel.TypingParams{On: a.On}, nil)
	case assistantwire.ChannelsEnable:
		err := h.enableChannel(ctx, a.Channel, a.On)
		if errors.Is(err, errNeedsTriageModel) {
			return nil, &rpc.Error{Code: assistantwire.CodeNeedsTriageModel, Message: err.Error()}
		}
		return map[string]any{}, err
	case assistantwire.ChannelsUnbind:
		return map[string]any{}, h.unbindChannel(ctx, a.Channel)
	}
	return nil, fmt.Errorf("app %s has no method %s", assistantwire.AppChannels, method)
}

// feishuCoded gives a Feishu API error its code on the bus: the assistant tells Feishu refusing agent messages from a
// call that failed for now (a rate limit, a busy server). A call that got no answer from Feishu (a timeout, a dropped
// connection, a gateway's page) has code -1: it failed for now too.
func feishuCoded(err error) error {
	if err == nil {
		return nil
	}
	code := -1
	var ae *feishubot.APIError
	if errors.As(err, &ae) {
		code = ae.Code
	}
	data, _ := json.Marshal(map[string]int{"code": code})
	return &rpc.Error{Code: assistantwire.CodeFeishuAPI, Message: err.Error(), Data: data}
}

// checkReactTarget refuses an emoji (or taking one away) on a message of a chat the bot may not use: the owner's private
// chat, the groups opted in, and the messages the owner's "@bot /ai" was taken from (aiSource, which carries its chat) are
// the only places it reacts. Where a message is is found as for a reply's parent (checkReplyParent).
func (h *Manager) checkReactTarget(ctx context.Context, api *feishubot.Client, set FeishuBotSettings, messageID string) error {
	if messageID == "" {
		return errors.New("feishu: a reaction needs a message")
	}
	if set.aiSource(messageID, h.bots.clock().UTC()) != nil {
		return nil
	}
	_, err := h.checkReplyParent(ctx, api, set, messageID)
	return err
}

// errNotBotDMMessage: a pin or a withdrawal named a message that is not the bot's own in the owner's private chat.
var errNotBotDMMessage = errors.New("feishu: only the bot's own messages in the owner's private chat can be pinned or withdrawn")

// checkOwnDMMessage: messageID is a message the bot sent in the owner's private chat, the only ones feishu.pin and
// feishu.recall take (the workbench's cards).
func checkOwnDMMessage(ctx context.Context, api *feishubot.Client, set FeishuBotSettings, messageID string) error {
	if messageID == "" || set.Owner == nil || set.Owner.ChatID == "" {
		return errNotBotDMMessage
	}
	items, err := api.Message(ctx, messageID)
	if err != nil {
		return feishuCoded(err)
	}
	if len(items) == 0 {
		return errNotBotDMMessage
	}
	it := items[0]
	if it.ChatID != set.Owner.ChatID || it.Sender.SenderType != "app" || it.Sender.ID != api.AppID() {
		return errNotBotDMMessage
	}
	return nil
}

// unpinBotOthers takes off the pins the bot made in chatID, but keep's; the owner's own pins stay.
func unpinBotOthers(ctx context.Context, api *feishubot.Client, chatID, keep string) error {
	pins, err := api.Pins(ctx, chatID)
	if err != nil {
		return err
	}
	for _, p := range pins {
		if p.MessageID == keep || !p.ByApp(api.AppID()) {
			continue
		}
		if err := api.Unpin(ctx, p.MessageID); err != nil {
			return err
		}
	}
	return nil
}
