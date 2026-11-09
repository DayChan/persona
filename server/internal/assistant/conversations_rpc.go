package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// What an extension granted assistant.read reads of the assistant's conversations (#1288, docs/extensions/extensions.md
// "Assistant conversations"): the conversations of the assistant's own space (never the health space's, never a
// guest's), and of each only the words, the user's and the assistant's. The hub checked the grant; this only reads.

const (
	conversationsLimit     = 50
	conversationsLimitMost = 200
	messagesLimit          = 200
	messagesLimitMost      = 500
	// messagesScanPages bounds how many of the node's pages (500 items each) one call reads: a long conversation is
	// scanned over several calls, each returning next_seq.
	messagesScanPages = 8
	messagesNodePage  = 500
)

// slashCommandRe is a message that is only a slash command (/compact, /clear, /model x…): an instruction to the
// tool, not the user's words.
var slashCommandRe = lazyre.New(`^/[A-Za-z][\w:-]*(\s[^\n]*)?$`)

// conversationsRequest answers assistant.conversations and assistant.messages; false for any other method.
func (s *Service) conversationsRequest(ctx context.Context, method string, params json.RawMessage) (any, bool, error) {
	switch method {
	case assistantwire.MethodAssistantConversations:
		var p assistantwire.AssistantConversationsParams
		if err := strictParams(method, params, &p); err != nil {
			return nil, true, err
		}
		out, err := s.extensionConversations(ctx, p)
		return out, true, err
	case assistantwire.MethodAssistantMessages:
		var p assistantwire.AssistantMessagesParams
		if err := strictParams(method, params, &p); err != nil {
			return nil, true, err
		}
		out, err := s.extensionMessages(ctx, p)
		return out, true, err
	}
	return nil, false, nil
}

func strictParams(method string, raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return conversationsInvalid(method, err.Error())
	}
	return nil
}

func conversationsInvalid(method, message string) error {
	return &rpc.Error{Code: bus.CodeInvalidParams, Message: method + ": " + message}
}

func parseTimeParam(method, name, v string, required bool) (time.Time, error) {
	if v == "" {
		if required {
			return time.Time{}, conversationsInvalid(method, name+" is required (an RFC 3339 time)")
		}
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, conversationsInvalid(method, name+" must be an RFC 3339 time")
	}
	return t, nil
}

// ownConversations are the assistant's conversations an extension may read: its own space's, no guest's.
func (s *Service) ownConversations(ctx context.Context, archived bool) ([]proto.Session, string, error) {
	here := s.settings(ctx)
	if here.MachineID == "" {
		return nil, "", nil
	}
	all, err := s.sessions(ctx, here.MachineID)
	if err != nil {
		return nil, "", conversationsCallError(err)
	}
	var out []proto.Session
	for _, c := range all {
		if !c.Assistant || spaceOf(c) != "" || c.Archived && !archived || s.isGuest(ctx, c.MachineID, c.SessionID) {
			continue
		}
		out = append(out, c)
	}
	return out, here.Main, nil
}

func conversationsCallError(err error) error {
	var re *rpc.Error
	switch {
	case errors.Is(err, apierr.ErrOffline), errors.Is(err, rpc.ErrClosed), errors.As(err, &re) && re.Code == assistantwire.CodeMachineOffline:
		return &rpc.Error{Code: assistantwire.CodeMachineOffline, Message: "the assistant's computer is not connected"}
	case errors.Is(err, ErrNoDevice):
		return &rpc.Error{Code: assistantwire.CodeMachineOffline, Message: "the assistant's computer is not set up"}
	}
	return err
}

func (s *Service) extensionConversations(ctx context.Context, p assistantwire.AssistantConversationsParams) (assistantwire.AssistantConversationsResult, error) {
	m := assistantwire.MethodAssistantConversations
	out := assistantwire.AssistantConversationsResult{Items: []assistantwire.AssistantConversation{}}
	since, err := parseTimeParam(m, "since", p.Since, true)
	if err != nil {
		return out, err
	}
	until, err := parseTimeParam(m, "until", p.Until, false)
	if err != nil {
		return out, err
	}
	if p.Limit < 0 || p.Limit > conversationsLimitMost || p.Offset < 0 {
		return out, conversationsInvalid(m, fmt.Sprintf("limit is 0 to %d, offset nonnegative", conversationsLimitMost))
	}
	if p.Limit == 0 {
		p.Limit = conversationsLimit
	}
	list, main, err := s.ownConversations(ctx, p.IncludeArchived)
	if err != nil {
		return out, err
	}
	origins, _ := s.conversationOrigins()
	var hits []assistantwire.AssistantConversation
	for _, c := range list {
		if c.UpdatedAt.Before(since) {
			continue
		}
		_, started := s.titleOf(c.SessionID)
		if !until.IsZero() && !started.IsZero() && !started.Before(until) {
			continue
		}
		e := assistantwire.AssistantConversation{ID: c.SessionID, Title: ownTitle(c), Main: c.SessionID == main, Archived: c.Archived,
			Origin: c.Origin, Updated: c.UpdatedAt}
		if o, ok := origins[c.SessionID]; ok && e.Origin == "" {
			e.Origin = o.origin
		}
		if e.Origin == "" {
			e.Origin = OriginUser // what the hub cannot tell is the user's (origins.go)
		}
		if !started.IsZero() {
			e.Started = &started
		}
		hits = append(hits, e)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Updated.After(hits[j].Updated) })
	out.Total = len(hits)
	if p.Offset < len(hits) {
		end := min(p.Offset+p.Limit, len(hits))
		out.Items = hits[p.Offset:end]
		if end < len(hits) {
			out.NextOffset = &end
		}
	}
	return out, nil
}

// ownTitle is the conversation's title, "" while it has none (the node then gives the directory's name or Claude
// Code's default).
func ownTitle(c proto.Session) string {
	if t := strings.TrimSpace(c.Title); t != "" && t != "Claude Code" && t != path.Base(c.Cwd) {
		return t
	}
	return ""
}

func (s *Service) extensionMessages(ctx context.Context, p assistantwire.AssistantMessagesParams) (assistantwire.AssistantMessagesResult, error) {
	m := assistantwire.MethodAssistantMessages
	out := assistantwire.AssistantMessagesResult{Messages: []assistantwire.AssistantMessage{}}
	if strings.TrimSpace(p.ID) == "" {
		return out, conversationsInvalid(m, "id is required")
	}
	since, err := parseTimeParam(m, "since", p.Since, false)
	if err != nil {
		return out, err
	}
	until, err := parseTimeParam(m, "until", p.Until, false)
	if err != nil {
		return out, err
	}
	if p.Limit < 0 || p.Limit > messagesLimitMost || p.AfterSeq != nil && *p.AfterSeq < 0 {
		return out, conversationsInvalid(m, fmt.Sprintf("limit is 0 to %d, after_seq nonnegative", messagesLimitMost))
	}
	if p.Limit == 0 {
		p.Limit = messagesLimit
	}
	list, _, err := s.ownConversations(ctx, true)
	if err != nil {
		return out, err
	}
	var c *proto.Session
	for i := range list {
		if list[i].SessionID == p.ID {
			c = &list[i]
			break
		}
	}
	if c == nil {
		return out, &rpc.Error{Code: assistantwire.CodeConversationNotFound, Message: m + ": no conversation " + p.ID + " in the assistant's own space"}
	}
	after := 0
	if p.AfterSeq != nil {
		after = *p.AfterSeq
	}
	for range messagesScanPages {
		var page struct {
			Items     []proto.Item `json:"items"`
			MoreAfter bool         `json:"more_after"`
		}
		if err := s.call(ctx, c.MachineID, DeviceSessionItems, map[string]any{"session_id": c.SessionID, "after_seq": after, "lazy_images": true}, &page); err != nil {
			return out, conversationsCallError(err)
		}
		more := page.MoreAfter
		for _, it := range page.Items {
			if it.Seq <= after { // a node that does not page by after_seq answers everything
				continue
			}
			after = it.Seq
			if !until.IsZero() && !it.TS.IsZero() && !it.TS.Before(until) {
				return out, nil // in time order: nothing more in range
			}
			if !since.IsZero() && it.TS.Before(since) {
				continue
			}
			msg, ok := extensionMessage(it)
			if !ok {
				continue
			}
			out.Messages = append(out.Messages, msg)
			if len(out.Messages) == p.Limit {
				next := after
				out.NextSeq = &next
				return out, nil
			}
		}
		if !more || len(page.Items) == 0 {
			return out, nil
		}
	}
	next := after // scanned as far as one call goes: more on the next
	out.NextSeq = &next
	return out, nil
}

// extensionMessage is item as an extension reads it: the user's words or the assistant's reply; false for anything
// else (tools, thinking, notices, images, what Persona typed in for the model, another session's message, a slash
// command).
func extensionMessage(it proto.Item) (assistantwire.AssistantMessage, bool) {
	if it.Kind != proto.KindText {
		return assistantwire.AssistantMessage{}, false
	}
	msg := assistantwire.AssistantMessage{Seq: it.Seq, At: it.TS, Truncated: it.FullSize > 0}
	switch it.Role {
	case "user":
		if it.From != "" || it.Injected != nil || prompts.ItemSource(it) != nil || it.ClientMsgID == "" && !prompts.UserAuthoredContext(it.Text) && prompts.ParseInjected(it.Text) != nil {
			return msg, false
		}
		text := transcript.StripInjected(it.Text)
		if text == "" || slashCommandRe.MatchString(text) {
			return msg, false
		}
		msg.Role, msg.Text = "user", text
	case "assistant":
		text := strings.TrimSpace(wire.AssistantCardText(it.Text))
		if text == "" {
			return msg, false
		}
		msg.Role, msg.Text = "assistant", text
	default:
		return msg, false
	}
	return msg, true
}
