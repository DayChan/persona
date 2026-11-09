package prompts

import (
	"encoding/json"
	"html"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

var messageContextRe = lazyre.New(`(?s)^\s*<persona-context\b([^>]*)>(.*?)</persona-context>\s*`)
var sourceKindRe = lazyre.New(`\bkind="message-source"`)
var relayTitleRe = lazyre.New(`\btitle="来自对话「(.*?)」"`)
var relayBodyRe = lazyre.New(`^这条消息不是用户本人发的，是 Persona 的另一段对话（([^\s（）]+)）用 (conversation_send|conversation_new) 发来的，第 ([0-9]+) 跳（最多 ([0-9]+) 跳）。\n按里面说的做。要把结果告诉那段对话就用 conversation_send 发回 ([^\s；]+)；只是确认收到就不要回，免得两段对话来回发。$`)

// MessageSourceText persists provenance in the native transcript, not a short-lived
// echo cache. JSON's HTML escaping prevents a title from closing the context block.
func MessageSourceText(text string, source *proto.MessageSource) string {
	if source == nil || source.Kind == "" {
		return text
	}
	if existing, found := sourceMetadata(text); found && existing != nil && *existing == *source {
		return text
	}
	return messageSourceText(text, source)
}

func messageSourceText(text string, metadata any) string {
	b, err := json.Marshal(metadata)
	if err != nil {
		panic(err) // only fixed metadata structs are rendered
	}
	return render("shared/message_source", struct{ Metadata, Text string }{string(b), text})
}

// sourceMetadata returns the first leading provenance block, even if it says the
// author is the user. That sentinel prevents nested, user-pasted blocks impersonating
// an automated sender before client_msg_id arrives or on a user-started conversation.
func sourceMetadata(text string) (source *proto.MessageSource, found bool) {
	for {
		m := messageContextRe.FindStringSubmatch(text)
		if m == nil {
			return nil, false
		}
		if sourceKindRe.MatchString(m[1]) {
			var meta struct {
				proto.MessageSource
				Author string `json:"author"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(m[2])), &meta) != nil {
				return nil, true // malformed explicit metadata must not become a guessed source
			}
			if meta.Author == "user" || meta.Kind == "" {
				return nil, true
			}
			return &meta.MessageSource, true
		}
		text = text[len(m[0]):]
	}
}

// ProtectUserMessage retains the user's words verbatim but marks suspicious pasted
// templates as theirs. Ordinary input needs no wrapper; client_msg_id protects it too.
func ProtectUserMessage(text string) string {
	if UserAuthoredContext(text) {
		return text
	}
	_, metadata := sourceMetadata(text)
	if metadata || ParseRelaySource(text) != nil || ParseInjected(text) != nil {
		return messageSourceText(text, struct {
			Author string `json:"author"`
		}{"user"})
	}
	return text
}

// ParseRelaySource recognizes only the complete canonical relay context. A title,
// an emoji, or ordinary words alone never establish conversation provenance.
func ParseRelaySource(text string) *proto.MessageSource {
	for {
		m := messageContextRe.FindStringSubmatch(text)
		if m == nil {
			return nil
		}
		title := relayTitleRe.FindStringSubmatch(m[1])
		body := relayBodyRe.FindStringSubmatch(strings.TrimSpace(m[2]))
		if title != nil && strings.Contains(m[1], `kind="context"`) && body != nil && body[1] == body[5] {
			return &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: body[1], Title: html.UnescapeString(title[1])}
		}
		text = text[len(m[0]):]
	}
}

// ItemSource prefers explicit fields, then persisted provenance, then existing
// injected metadata and exact legacy templates. An app's own message is never
// text-inferred. Item.From remains separate: a peer's name is not a conversation id.
func ItemSource(it proto.Item) *proto.MessageSource {
	if it.Source != nil && it.Source.Kind != "" {
		return it.Source
	}
	inferText := it.Role == "user" && it.Kind == proto.KindText && it.ClientMsgID == "" && it.From == ""
	if inferText {
		if source, found := sourceMetadata(it.Text); found {
			return source
		}
	}
	if source := proto.SourceFromInjected(it.Injected); source != nil {
		return source
	}
	if !inferText {
		return nil
	}
	if source := ParseRelaySource(it.Text); source != nil {
		return source
	}
	return proto.SourceFromInjected(ParseInjected(it.Text))
}

// UserAuthoredContext marks a protected user message for other legacy classifiers.
func UserAuthoredContext(text string) bool {
	source, found := sourceMetadata(text)
	return found && source == nil
}
