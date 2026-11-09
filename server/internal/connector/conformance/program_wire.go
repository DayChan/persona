package conformance

// What CheckProgram holds each answer of a connector program to: the strict decoding into the types the hub reads
// (decodeWire), and per method the invariants the hub relies on. They return what they found as one error, so the
// wire fixtures (program_test.go) are held to the very same checks.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	wireschema "github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// issues collects what is wrong with one answer.
type issues []string

func (is *issues) add(format string, args ...any) { *is = append(*is, fmt.Sprintf(format, args...)) }

func (is issues) err() error {
	if len(is) == 0 {
		return nil
	}
	return errors.New(strings.Join(is, "; "))
}

var snakeKey = lazyre.New(`^[a-z][a-z0-9_]*$`)

// opaqueKeys are keys whose value the protocol does not describe: a tool's JSON schema (its own keywords), the blocks
// of a migrated connector's items, and a manifest's descriptions by kind.
var opaqueKeys = []string{"input_schema", "args", "lark", "doc", "moment", "kind_descriptions"}

// decodeStrict decodes one JSON value into out, refusing a field out does not have and anything after the value.
func decodeStrict(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("more than one JSON value")
	}
	return nil
}

// decodeWire decodes the answer to a method: a JSON object, with fields out has and keys that are snake_case (Go
// matches a key without regard to its case, so a Thread for a thread would pass the first, and not the hub's clients).
func decodeWire(raw json.RawMessage, out any) error {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return fmt.Errorf("the answer is not a JSON object: %.40q", raw)
	}
	if err := decodeStrict(raw, out); err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	var is issues
	checkKeys(v, "", &is)
	return is.err()
}

func checkKeys(v any, at string, is *issues) {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if !snakeKey.MatchString(k) {
				is.add("key %q at %q is not snake_case", k, at)
			}
			if !slices.Contains(opaqueKeys, k) {
				checkKeys(x, at+"."+k, is)
			}
		}
	case []any:
		for _, x := range v {
			checkKeys(x, at, is)
		}
	}
}

// checkInitialize holds the answer to initialize to the manifest: who the program is, and the connectors it contributes
// the way the manifest says (the hub goes by the manifest's and logs a difference).
func checkInitialize(m *extension.Manifest, items []extension.ConnectorItem, raw json.RawMessage) error {
	var res extension.InitializeResult
	if err := decodeWire(raw, &res); err != nil {
		return err
	}
	var is issues
	if res.Protocol != extension.Protocol || res.ID != m.ID || res.Version != m.Version {
		is.add("answered protocol %d, id %q, version %q; the manifest says %d, %q, %q", res.Protocol, res.ID, res.Version, extension.Protocol, m.ID, m.Version)
	}
	var listed []json.RawMessage
	if err := json.Unmarshal(res.Contributes["connectors"], &listed); err != nil {
		is.add("contributes.connectors: a list of the connectors")
	}
	got := map[string]extension.ConnectorItem{}
	for _, one := range listed {
		id, err := extension.Points["connectors"].Item(one)
		if err != nil {
			is.add("contributes.connectors: %v", err)
			continue
		}
		var it extension.ConnectorItem
		_ = json.Unmarshal(one, &it)
		got[id] = it
	}
	for _, want := range items {
		if it, ok := got[want.ID]; !ok {
			is.add("does not contribute connector %q", want.ID)
		} else if !sameItem(want, it) {
			is.add("connector %q is not what the manifest says", want.ID)
		}
	}
	return is.err()
}

// sameItem says whether two contributions are the same, capabilities in any order.
func sameItem(a, b extension.ConnectorItem) bool {
	a.Capabilities, b.Capabilities = slices.Sorted(slices.Values(a.Capabilities)), slices.Sorted(slices.Values(b.Capabilities))
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

var stateNames = []string{connector.StateOK, connector.StateNotConfigured, connector.StateNotSignedIn, connector.StateNoPermission,
	connector.StateRateLimited, connector.StateError}

func checkStatus(raw json.RawMessage) (st connector.Status, err error) {
	if err := decodeWire(raw, &st); err != nil {
		return st, err
	}
	var is issues
	switch {
	case !slices.Contains(stateNames, st.State):
		is.add("state %q is none of %v", st.State, stateNames)
	case st.State == connector.StateOK && st.Error != "":
		is.add("state ok with an error")
	case st.State != connector.StateOK && st.State != connector.StateNotConfigured && st.Error == "" && len(st.Guide) == 0:
		is.add("state %s, and not a word of why (error, guide)", st.State)
	}
	return st, is.err()
}

// checkToolList is the answer to tools.list or actions.list (what, in the messages).
func checkToolList(id, what string, raw json.RawMessage) ([]proto.Tool, error) {
	var res serve.ToolsResult
	if err := decodeWire(raw, &res); err != nil {
		return nil, err
	}
	var is issues
	seen := map[string]bool{}
	for i, tl := range res.Tools {
		if !strings.HasPrefix(tl.Name, id+"_") || tl.Name == id+"_" {
			is.add("%s[%d]: name %q does not start with %q", what, i, tl.Name, id+"_")
		}
		if seen[tl.Name] {
			is.add("%s[%d]: name %q twice", what, i, tl.Name)
		}
		seen[tl.Name] = true
		if strings.TrimSpace(tl.Description) == "" {
			is.add("%s[%d]: %s has no description (the model reads it)", what, i, tl.Name)
		}
		var schema struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(tl.InputSchema, &schema) != nil || schema.Type != "object" {
			is.add("%s[%d]: %s has no input_schema of type object", what, i, tl.Name)
		}
	}
	return res.Tools, is.err()
}

// checkDescribe is the answer to actions.describe of the action name: the card says what it does and who will see it.
func checkDescribe(name string, raw json.RawMessage) error {
	var res serve.DescribeResult
	if err := decodeWire(raw, &res); err != nil {
		return err
	}
	var is issues
	if res.Action.Name != "" && res.Action.Name != name {
		is.add("action.name is %q", res.Action.Name)
	}
	if strings.TrimSpace(res.Summary) == "" {
		is.add("no summary (the card has nothing to ask the user about)")
	}
	if res.Action.Visible == "" {
		is.add("no action.visible (the card cannot say who will see it)")
	}
	return is.err()
}

// checkCollect is the answer to inbox.collect: what the hub needs of a thing to make an item and keep it. With a
// fallback (the capability), every thing but a batch to relate (group) has a tier the hub knows without the model:
// extra.rule_tier, or a tier its rules decided for sure.
func checkCollect(kinds []string, fallback bool, raw json.RawMessage) ([]connector.InboxThing, error) {
	var res serve.CollectResult
	if err := decodeWire(raw, &res); err != nil {
		return nil, err
	}
	var is issues
	seen := map[string]bool{}
	for i, th := range res.Things {
		switch {
		case th.Thread == "":
			is.add("things[%d]: no thread (the thing's stable key)", i)
		case seen[th.Thread]:
			is.add("things[%d]: thread %q twice", i, th.Thread)
		}
		seen[th.Thread] = true
		if len(kinds) > 0 && !slices.Contains(kinds, th.Kind) {
			is.add("things[%d]: kind %q is not one the manifest declares %v", i, th.Kind, kinds)
		}
		if th.Tier != "" && !th.Tier.Known() {
			is.add("things[%d]: tier %q is not action, fyi or noise", i, th.Tier)
		}
		if fallback && th.Group == nil && !th.Tier.Known() && (th.Extra == nil || !connector.Tier(th.Extra.RuleTier).Known()) {
			is.add("things[%d]: the connector declares fallback, and the thing has no extra.rule_tier (action, fyi or noise)", i)
		}
		if !slices.Contains([]string{"", connector.DoneRead, connector.DoneReplied}, th.Done) {
			is.add("things[%d]: done %q is not read or replied", i, th.Done)
		}
		if th.Done == "" && th.At.IsZero() {
			is.add("things[%d]: no at (a thing without a date is older than the inbox window)", i)
		}
		if th.Done == "" && th.Last == "" {
			is.add("things[%d]: no last (a new move cannot be told from the old ones)", i)
		}
		if th.CanReply && th.ReplyTo == "" {
			is.add("things[%d]: can_reply without reply_to (the user is told who will see a reply)", i)
		}
		for _, tg := range th.Tags {
			if tg.Key == "" || tg.Value == "" {
				is.add("things[%d]: a tag needs a key and a value", i)
			}
		}
	}
	return res.Things, is.err()
}

var (
	threadKinds = []string{"dm", "group", "channel", "pr", "issue"}
	mediaKinds  = []string{"image", "sticker", "video", "voice", "file"}
)

// checkThreadPage is the answer to thread.page: oldest first, at most limit, and a reply that goes through an action
// the connector has, which takes {chat, text} (extmgr/connector_threads.go).
func checkThreadPage(limit int, actions []proto.Tool, raw json.RawMessage) (connector.ThreadPage, error) {
	var page connector.ThreadPage
	if err := decodeWire(raw, &page); err != nil {
		return page, err
	}
	var is issues
	if page.Chat.ID == "" || page.Chat.Name == "" {
		is.add("chat needs an id and a name")
	}
	if !slices.Contains(threadKinds, page.Chat.Kind) {
		is.add("chat.kind %q is none of %v", page.Chat.Kind, threadKinds)
	}
	if r := page.Chat.Reply; r != "" {
		i := slices.IndexFunc(actions, func(t proto.Tool) bool { return t.Name == r })
		if i < 0 {
			is.add("chat.reply %q is not one of the actions", r)
		} else {
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			_ = json.Unmarshal(actions[i].InputSchema, &schema)
			for _, arg := range []string{"chat", "text"} {
				if _, ok := schema.Properties[arg]; !ok {
					is.add("chat.reply %q takes no %q (the app sends {chat, text})", r, arg)
				}
			}
		}
	}
	if len(page.Messages) > limit {
		is.add("%d messages, asked for at most %d", len(page.Messages), limit)
	}
	seen := map[string]bool{}
	for i, m := range page.Messages {
		switch {
		case m.ID == "":
			is.add("messages[%d]: no id", i)
		case seen[m.ID]:
			is.add("messages[%d]: id %q twice", i, m.ID)
		}
		seen[m.ID] = true
		switch {
		case m.At.IsZero():
			is.add("messages[%d]: no at", i)
		case i > 0 && m.At.Before(page.Messages[i-1].At):
			is.add("messages[%d]: older than the one before it (a page is oldest first)", i)
		}
		if m.Text == "" && len(m.Media) == 0 {
			is.add("messages[%d]: neither text nor media (an empty bubble)", i)
		}
		if !m.Event && !m.Mine && m.Sender.Name == "" {
			is.add("messages[%d]: no sender.name", i)
		}
		for _, md := range m.Media {
			if !slices.Contains(mediaKinds, md.Kind) {
				is.add("messages[%d]: media kind %q is none of %v", i, md.Kind, mediaKinds)
			}
		}
	}
	return page, is.err()
}

// checkOlder holds a page to the one it was asked from: no message on both, none newer than the first of the newer.
func checkOlder(newer, older connector.ThreadPage) error {
	if len(newer.Messages) == 0 {
		return nil
	}
	var is issues
	for _, m := range older.Messages {
		if slices.ContainsFunc(newer.Messages, func(x connector.ThreadMessage) bool { return x.ID == m.ID }) {
			is.add("message %q is on both pages", m.ID)
		}
		if m.At.After(newer.Messages[0].At) {
			is.add("message %q is newer than the first message of the page after it", m.ID)
		}
	}
	return is.err()
}

func checkMedia(raw json.RawMessage) (m connector.Media, err error) {
	if err := decodeWire(raw, &m); err != nil {
		return m, err
	}
	if m.Mime == "" || len(m.Data) == 0 {
		return m, errors.New("media needs a mime and data")
	}
	return m, nil
}

// checkChanges is the answer to archive.changes from cursor. The hub keeps next and asks from it, and the knowledge
// base appends what it gets and drops a message without a date or text.
func checkChanges(cursor string, limit int, raw json.RawMessage) (serve.ChangesResult, error) {
	var res serve.ChangesResult
	if err := decodeWire(raw, &res); err != nil {
		return res, err
	}
	var is issues
	if len(res.Batches) > limit {
		is.add("%d chats, asked for at most %d", len(res.Batches), limit)
	}
	if res.Next == "" && (cursor != "" || len(res.Batches) > 0) {
		is.add("next is empty after %d chat(s) from cursor %q (the hub would start over, and read it all again)", len(res.Batches), cursor)
	}
	for i, b := range res.Batches {
		if b.Chat.ID == "" || b.Chat.Name == "" || b.Chat.Kind == "" {
			is.add("batches[%d]: chat needs an id, a name and a kind", i)
		}
		seen := map[string]bool{}
		for j, m := range b.Messages {
			at := fmt.Sprintf("batches[%d].messages[%d]", i, j)
			switch {
			case m.ID == "":
				is.add("%s: no id", at)
			case seen[m.ID]:
				is.add("%s: id %q twice", at, m.ID)
			}
			seen[m.ID] = true
			switch {
			case m.At.IsZero():
				is.add("%s: no at (the knowledge base drops a message without a date)", at)
			case j > 0 && m.At.Before(b.Messages[j-1].At):
				is.add("%s: older than the one before it (a chat's messages are oldest first)", at)
			}
			if strings.TrimSpace(m.Text) == "" && len(m.Media) == 0 {
				is.add("%s: no text and no media (the knowledge base drops it)", at)
			}
			for _, h := range m.SenderHandles {
				if h.Kind == "" || h.Value == "" {
					is.add("%s: a sender handle needs a kind and a value", at)
				}
			}
		}
	}
	return res, is.err()
}

// checkHistoryChats is history.chats' answer: each conversation once, with an id, a name and a kind the hub knows. An
// id is unique across the connector's sources: a query and a page name a conversation by it alone.
func checkHistoryChats(raw json.RawMessage) ([]connectorwire.HistoryChat, error) {
	var res serve.HistoryChatsResult
	if err := decodeWire(raw, &res); err != nil {
		return nil, err
	}
	var is issues
	if res.Chats == nil {
		is.add("no chats list (an empty one is [])")
	}
	kinds := connectorwire.HistoryKinds
	seen := map[string]bool{}
	for i, c := range res.Chats {
		switch {
		case c.ID == "" || c.Name == "":
			is.add("chats[%d]: needs an id and a name", i)
		case seen[c.ID]:
			is.add("chats[%d]: %q listed twice (an id is unique across the connector's sources)", i, c.ID)
		}
		seen[c.ID] = true
		if !slices.Contains(kinds, c.Kind) {
			is.add("chats[%d]: kind %q is none of %v", i, c.Kind, kinds)
		}
		if c.Estimate < 0 {
			is.add("chats[%d]: a negative estimate", i)
		}
	}
	return res.Chats, is.err()
}

// checkHistoryPage is archive.history's answer to q: messages oldest first, each once with an id and a date; an
// unreadable conversation has none, and no more or cursor either; more needs messages and the cursor to go on from,
// one that is not where it started (the hub would read the same page forever); no more has no older cursor. The start
// of a conversation then reads the same whichever of the two a program says it with.
func checkHistoryPage(q connectorwire.HistoryQuery, raw json.RawMessage) (connectorwire.HistoryPage, error) {
	var page connectorwire.HistoryPage
	if err := decodeWire(raw, &page); err != nil {
		return page, err
	}
	var is issues
	if page.Unreadable && len(page.Messages) > 0 {
		is.add("unreadable, with %d message(s)", len(page.Messages))
	}
	if page.More && page.Older == "" {
		is.add("more without an older cursor (the hub could not go on)")
	}
	if page.More && len(page.Messages) == 0 {
		is.add("more on a page with no messages (an empty page is the start)")
	}
	if !page.More && page.Older != "" {
		is.add("an older cursor without more (no more is the start, with nothing older)")
	}
	if page.Unreadable && (page.More || page.Older != "" || page.Newer != "") {
		is.add("unreadable, with more or a cursor (an unreadable page carries nothing)")
	}
	if page.More && q.Before != "" && page.Older == q.Before {
		is.add("older is still %q (the same page would be read again)", q.Before)
	}
	if page.Pace != nil && page.Pace.Stop && strings.TrimSpace(page.Pace.Reason) == "" {
		is.add("pace stops without a reason (a person has to know what to do)")
	}
	seen := map[string]bool{}
	for j, m := range page.Messages {
		at := fmt.Sprintf("messages[%d]", j)
		switch {
		case m.ID == "":
			is.add("%s: no id", at)
		case seen[m.ID]:
			is.add("%s: id %q twice", at, m.ID)
		}
		seen[m.ID] = true
		switch {
		case m.At.IsZero():
			is.add("%s: no at (the knowledge base drops a message without a date)", at)
		case j > 0 && m.At.Before(page.Messages[j-1].At):
			is.add("%s: older than the one before it (a page is oldest first)", at)
		}
	}
	return page, is.err()
}

// checkHistoryOlder holds the page read from newer's older cursor to going back: it does not start after newer does.
// Pages may overlap (a source answers more than the limit), so only that is held.
func checkHistoryOlder(newer, older connectorwire.HistoryPage) error {
	if len(newer.Messages) == 0 || len(older.Messages) == 0 {
		return nil
	}
	if older.Messages[0].At.After(newer.Messages[0].At) {
		return fmt.Errorf("the older page starts at %s, after the newer page's %s", older.Messages[0].At.Format("2006-01-02T15:04:05Z07:00"),
			newer.Messages[0].At.Format("2006-01-02T15:04:05Z07:00"))
	}
	if older.Messages[0].ID == newer.Messages[0].ID && older.Messages[len(older.Messages)-1].ID == newer.Messages[len(newer.Messages)-1].ID {
		return errors.New("the older page is the newer one again")
	}
	return nil
}

// checkHistoryAnchors is history.anchors' answer: each anchor says where it is and why it is one; more needs next.
func checkHistoryAnchors(raw json.RawMessage) (connectorwire.HistoryAnchors, error) {
	var res connectorwire.HistoryAnchors
	if err := decodeWire(raw, &res); err != nil {
		return res, err
	}
	var is issues
	if res.More && res.Next == "" {
		is.add("more without a next cursor (the hub could not go on)")
	}
	for i, a := range res.Anchors {
		if a.Chat == "" || a.Position == "" {
			is.add("anchors[%d]: needs a chat and a position", i)
		}
		if !a.Mine && !a.Mention {
			is.add("anchors[%d]: neither mine nor mention", i)
		}
		if a.Message != nil && a.Message.ID == "" {
			is.add("anchors[%d]: a message without an id", i)
		}
	}
	return res, is.err()
}

// checkPrompt is a sign-in step: fields to fill in, a code to enter, or done.
func checkPrompt(raw json.RawMessage) (connector.Prompt, error) {
	var p connector.Prompt
	if err := decodeWire(raw, &p); err != nil {
		return p, err
	}
	var is issues
	switch {
	case p.Done && (len(p.Fields) > 0 || p.Kind != "" || p.Error != ""):
		is.add("done, and asking for something")
	case p.Done && p.Account == "":
		is.add("done without an account (who is signed in)")
	case p.Done:
	case p.Kind == connector.PromptDeviceCode:
		if p.URL == "" || p.UserCode == "" || p.Interval <= 0 {
			is.add("a device_code step needs url, user_code and interval")
		}
	case p.Kind == connector.PromptQRCode:
		if p.URL == "" || p.Interval <= 0 || p.Step == "" {
			is.add("a qr step needs url, interval and a step (the app asks the same one again)")
		}
	case p.Kind != "":
		is.add("kind %q is none of empty, device_code and qr", p.Kind)
	case len(p.Fields) == 0:
		is.add("neither fields nor done (the user has nothing to do)")
	case p.Step == "":
		is.add("a step to fill in has no name (an empty step starts the sign-in over)")
	}
	names := map[string]bool{}
	for _, f := range p.Fields {
		if f.Name == "" || f.Label == "" || names[f.Name] {
			is.add("field %q needs a name (once) and a label", f.Name)
		}
		names[f.Name] = true
	}
	return p, is.err()
}

func checkItems(raw json.RawMessage) (serve.ItemsResult, error) {
	var res serve.ItemsResult
	if err := decodeWire(raw, &res); err != nil {
		return res, err
	}
	var is issues
	for i, it := range res.Items {
		if !slices.Contains([]string{"docs", "meetings", "events"}, it.Kind) || it.ID == "" || it.Title == "" {
			is.add("items[%d]: needs a kind (docs, meetings, events), an id and a title", i)
		}
		for j, sg := range it.Signals {
			switch {
			case !slices.Contains(kbwire.Signals, sg):
				is.add("items[%d]: signal %q is none of %v", i, sg, kbwire.Signals)
			case slices.Contains(it.Signals[:j], sg):
				is.add("items[%d]: signal %q twice", i, sg)
			}
		}
	}
	return res, is.err()
}

// checkChanged is the params of a changed event: from one of the plugin's connectors that declares events, with a scope
// the hub knows (it drops the rest).
func checkChanged(items []extension.ConnectorItem, raw json.RawMessage) error {
	var e serve.ChangedEvent
	if err := decodeWire(raw, &e); err != nil {
		return err
	}
	var is issues
	i := slices.IndexFunc(items, func(it extension.ConnectorItem) bool { return it.ID == e.Contribution })
	switch {
	case i < 0:
		is.add("contribution %q is none of the connectors", e.Contribution)
	case !slices.Contains(items[i].Capabilities, string(connector.CapEvents)):
		is.add("%s sends changed and does not declare events (the hub would not watch for it)", e.Contribution)
	}
	if !connector.Scope(e.Scope).Known() {
		is.add("scope %q is not chats, items or inbox", e.Scope)
	}
	return is.err()
}

// paramsHold holds what the hub sends with a call to its definition in schema/extension (wireschema.Calls): a
// failure is the hub's (or the schema's), not the program's.
func paramsHold(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := wireschema.ValidateParams(method, raw); err != nil {
		return fmt.Errorf("%s: the hub's params do not hold to schema/extension: %v", method, err)
	}
	return nil
}
