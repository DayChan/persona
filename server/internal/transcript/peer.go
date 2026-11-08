package transcript

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A message another Claude session sent this one ("Another Claude session sent a message:" + the tagged body + an
// English note on how to treat it).
var crossRe = regexp.MustCompile(`(?s)<cross-session-message([^>]*)>\s*(.*?)\s*</cross-session-message>`)
var crossNameRe = regexp.MustCompile(`from-name="([^"]*)"`)

// peerMessage is another Claude session's message that came while this one was idle: Claude Code (2.1.299 on) writes
// it as a hidden (isMeta) user line with the sender in its origin, and it starts a turn, so it reads as the message it
// is, the sender's name on it, and opens the turn like a typed one (ParseLine). One absorbed mid-turn is a
// queued_command attachment instead (parseLine). A subagent handing back its report has a peer origin too: its report
// comes with its task notification (handbackOf). ok false for anything else.
func peerMessage(mk func(int, string, string) proto.Item, origin, content json.RawMessage) (proto.Item, bool) {
	var o struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		Body     string `json:"body"`
		Handback bool   `json:"handback"`
	}
	if len(origin) == 0 || json.Unmarshal(origin, &o) != nil || o.Kind != "peer" || o.Handback {
		return proto.Item{}, false
	}
	var text string
	if json.Unmarshal(content, &text) != nil {
		var blocks []block
		_ = json.Unmarshal(content, &blocks)
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	return peerItem(mk, text, o.Name, o.Body)
}

// attachmentOrigin is an attachment line's origin as Claude Code wrote it (only queued commands with a peer origin
// read it, so not every line is decoded twice).
func attachmentOrigin(line []byte) json.RawMessage {
	var l struct {
		Attachment struct {
			Origin json.RawMessage `json:"origin"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &l) != nil {
		return nil
	}
	return l.Attachment.Origin
}

// FromSession is who a message another Claude session sent is from when it does not say (the apps and the hub put it
// in the reader's language: hub/transcript_text.go, assistant/fleet_text.go).
const FromSession = "另一个会话"

// peerItem is a message another Claude session sent, as the chat shows it: the body the origin carries (the tagged one
// in text when it does not), the sender's name on it. ok false when there is no body.
func peerItem(mk func(int, string, string) proto.Item, text, name, body string) (proto.Item, bool) {
	from, tagged, ok := CrossSession(text)
	if b := strings.TrimSpace(body); b != "" || !ok {
		tagged = b
	}
	if name != "" {
		from = name
	} else if !ok {
		from = FromSession
	}
	// The user's own message from the phone, through the session's peer inbox (node/claudepeer.go): its words, without
	// what Persona told the model about where they came from.
	if from == PersonaPeer {
		if words, ok := prompts.PeerMessageBody(tagged); ok {
			tagged = strings.TrimSpace(words)
		}
	}
	if tagged == "" {
		return proto.Item{}, false
	}
	it := mk(0, "user", proto.KindText)
	it.Text, it.From = clip(tagged), from
	return it, true
}

// PersonaPeer is the sender name node signs the messages it sends through a claude's peer inbox with (the user's
// own words from the phone, node/claudepeer.go peerSender).
const PersonaPeer = "Persona"

// CrossSession returns the sender and body of a cross-session message, ok false for anything else.
func CrossSession(s string) (from, body string, ok bool) {
	m := crossRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	from = FromSession
	if n := crossNameRe.FindStringSubmatch(m[1]); n != nil && n[1] != "" {
		from = n[1]
	}
	return from, strings.TrimSpace(m[2]), true
}
