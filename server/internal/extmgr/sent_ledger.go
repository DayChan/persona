package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/mail"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The ledger of what Persona wrote as the user (sent_as_user, docs/extensions/connectors.md "Sent as the user", #5278). The
// connectors' write gate runs here, so this is where a write that went out is known with the conversation that asked
// and the card the user answered. Registry.SetSent tells recordSent of every gated write whose action succeeded;
// refusals, declines, failures and the user's own sends (Registry.Run) never get here. A send an extension's connector
// made on its own, outside the gate, comes in through sent.record instead (extensionSentRecord), and a write the
// node's Lark gate proxy forwarded through lark.sent (RecordNodeSent).

// sentStore keeps and reads the ledger (db.RecordSentAsUser…).
type sentStore interface {
	RecordSentAsUser(ctx context.Context, s db.SentAsUser) (int64, error)
	SentAsUsers(ctx context.Context, before int64, limit int, connector string) ([]db.SentAsUser, int64, error)
	PruneSentAsUser(ctx context.Context, before time.Time) (int64, error)
}

var _ sentStore = (*db.DB)(nil)

const (
	sentLedgerDays  = 90 // how long a row is kept unless manager.json sent_ledger_days says otherwise
	sentPruneEvery  = 6 * time.Hour
	sentSummaryMost = 300 // characters of a row's one-line summary
	sentValueMost   = 200 // characters of one value in a row's target
)

type sentTraceKey struct{}

// sentTrace follows one gated call: who asked, and the card the user answered. callConnector puts it in the call's
// ctx and fills the card when it asks (approveCard); recordSent reads it back when the write has gone out.
type sentTrace struct {
	by         asker
	card       string // the id of the card the user was asked on ("" when no card was shown)
	answeredBy string // proto.Resolution.AnsweredBy of its answer: empty when the user answered in the app
}

// newConnectors is the connectors' registry for the gate's rules, with the ledger listening to what goes out.
func (h *Manager) newConnectors(rules []connector.Rule) *connector.Registry {
	r := connector.NewRegistry(rules)
	r.SetSent(h.recordSent)
	return r
}

// recordSent writes a row for a write that went out. A row that cannot be written does not undo the write, which
// already happened: it is logged, without the text.
func (h *Manager) recordSent(ctx context.Context, s connector.Sent) {
	st, _ := h.store.(sentStore)
	if st == nil {
		return
	}
	trace, _ := ctx.Value(sentTraceKey{}).(*sentTrace)
	row := sentRow(s, trace)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := st.RecordSentAsUser(ctx, row); err != nil {
		h.logf("sent ledger: %s: %v", s.Action, err)
	}
}

// sentRow is the ledger's row for s: what the connector's arguments say of where it went and what it said, who asked
// and how the gate let it through.
func sentRow(s connector.Sent, trace *sentTrace) db.SentAsUser {
	target, brief := sentTarget(s.Action, s.Args)
	row := db.SentAsUser{At: s.At, Connector: s.Connector, Action: s.Action, Target: mustJSON(target), Text: cleanText(s.Text),
		MessageRef: proto.CleanMessageRef(s.MessageRef)}
	row.Summary = sentSummary(s, brief)
	origin := map[string]any{}
	if trace != nil {
		if trace.by.machine != "" {
			origin["machine_id"] = trace.by.machine
		}
		if trace.by.session != "" {
			origin["session_id"] = trace.by.session
		}
	}
	row.Origin = mustJSON(origin)
	through := map[string]any{"kind": s.Through.Kind}
	switch s.Through.Kind {
	case connector.ThroughRule:
		through["rule"] = s.Through.Rule
		if s.Through.UserRule { // one the user made from this page, not one that is written in manager.json
			through["user_rule"] = true
		}
	case connector.ThroughCard:
		if trace != nil && trace.card != "" {
			through["interaction_id"] = trace.card
			if trace.answeredBy != "" {
				through["answered_by"] = trace.answeredBy
			}
		}
	}
	row.LetThrough = mustJSON(through)
	return row
}

// sentSummary is a row's one line: the first line of the card's words when the user was asked, else the action and
// where it went.
func sentSummary(s connector.Sent, brief string) string {
	line := s.Through.Summary
	if line = strings.TrimSpace(line); line != "" {
		line, _, _ = strings.Cut(line, "\n")
		// i18n-ignore: the card's own punctuation (GitHub's card ends its first line with a full-width colon)
		line = strings.TrimRight(strings.TrimSpace(line), "：: ")
	}
	if line == "" {
		line = strings.TrimSpace(s.Action + " " + brief)
	}
	return cutRunes(cleanText(line), sentSummaryMost)
}

// cleanText is s as Postgres takes text: no NUL, only valid UTF-8.
func cleanText(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
}

// shortValue is a short value as a row keeps it: trimmed, cleaned for Postgres and cut to sentValueMost characters. A
// message ref is not cut but kept whole or not at all (proto.CleanMessageRef).
func shortValue(s string) string { return cutRunes(strings.TrimSpace(cleanText(s)), sentValueMost) }

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// sentTarget is where a write went, from the arguments it was given, and the same in a few words. GitHub's comments
// and reviews: the PR or issue (repo#number); mail: the recipients and the subject; any other connector's, an
// extension's included: the fields connectors usually name their destination by (chat, reply_to…), and when none of
// them is there the names of the arguments it was given, so a row still says something.
func sentTarget(action string, args json.RawMessage) (map[string]any, string) {
	switch action {
	case "github_comment", "github_review":
		if t, brief, ok := githubTarget(action, args); ok {
			return t, brief
		}
	case mail.ToolSend:
		if t, brief, ok := mailTarget(args); ok {
			return t, brief
		}
	}
	return genericTarget(args)
}

func githubTarget(action string, args json.RawMessage) (map[string]any, string, bool) {
	var a struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Chat   string `json:"chat"` // the thread page's reply: "owner/repo#12"
		Event  string `json:"event"`
	}
	if json.Unmarshal(args, &a) != nil {
		return nil, "", false
	}
	ref := a.Chat
	if a.Repo != "" && a.Number > 0 {
		ref = fmt.Sprintf("%s#%d", a.Repo, a.Number)
	}
	if ref == "" {
		return nil, "", false
	}
	target := map[string]any{"ref": cutRunes(cleanText(ref), sentValueMost)}
	brief := target["ref"].(string)
	if action == "github_review" && a.Event != "" {
		target["event"] = cutRunes(cleanText(a.Event), sentValueMost)
		brief += " " + target["event"].(string)
	}
	return target, brief, true
}

func mailTarget(args json.RawMessage) (map[string]any, string, bool) {
	var req mail.SendRequest
	if json.Unmarshal(args, &req) != nil || len(req.To) == 0 {
		return nil, "", false
	}
	emails := func(list []mail.Address) []string {
		var out []string
		for _, a := range list {
			out = append(out, cutRunes(cleanText(a.Email), sentValueMost))
		}
		return out
	}
	target := map[string]any{"to": emails(req.To), "subject": cutRunes(cleanText(req.Subject), sentValueMost)}
	if len(req.Cc) > 0 {
		target["cc"] = emails(req.Cc)
	}
	if len(req.Bcc) > 0 {
		target["bcc"] = emails(req.Bcc)
	}
	if req.AccountID != "" {
		target["account_id"] = cutRunes(cleanText(req.AccountID), sentValueMost)
	}
	brief := "to " + strings.Join(emails(req.To), ", ")
	if req.Subject != "" {
		brief += ": " + target["subject"].(string)
	}
	return target, brief, true
}

// targetFields are the arguments connectors usually name their destination by, in the order a row says them.
var targetFields = []string{"chat", "thread", "channel", "conversation", "recipient", "to", "reply_to", "subject", "repo", "number"}

func genericTarget(args json.RawMessage) (map[string]any, string) {
	var fields map[string]any
	_ = json.Unmarshal(args, &fields)
	target := map[string]any{}
	var brief []string
	for _, k := range targetFields {
		if v, ok := scalarOrStrings(fields[k]); ok {
			target[k] = v
			brief = append(brief, k+" "+fmt.Sprint(v))
		}
	}
	if len(target) == 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, cutRunes(cleanText(k), sentValueMost))
		}
		slices.Sort(keys)
		if len(keys) > 0 {
			target["arg_keys"] = keys
		}
		return target, ""
	}
	return target, strings.Join(brief, ", ")
}

// scalarOrStrings is v when it is a short string, a number, a boolean or a short list of strings, which is all a
// target keeps of an argument (never a nested object, which could be anything).
func scalarOrStrings(v any) (any, bool) {
	switch v := v.(type) {
	case string:
		s := cutRunes(cleanText(v), sentValueMost)
		return s, s != ""
	case float64, bool:
		return v, true
	case []any:
		var out []string
		for _, e := range v {
			s, ok := e.(string)
			if !ok || len(out) == 10 {
				return nil, false
			}
			out = append(out, cutRunes(cleanText(s), sentValueMost))
		}
		return out, len(out) > 0
	}
	return nil, false
}

const (
	sentRecordAction = connector.MethodSentRecord // a row's action when an extension told the hub of the send
	sentTargetKeys   = 20                         // most keys of a recorded target
	sentKeyMost      = 64                         // characters of one key in it

	sentRecordBurst = 60          // sent.record calls one extension may make at once
	sentRecordEvery = time.Second // one more call is allowed this often after that: 60 a minute
)

// sentLimiter is a token bucket per extension for sent.record: a program that loops cannot fill the ledger.
type sentLimiter struct {
	mu      sync.Mutex
	now     func() time.Time // time.Now when nil (tests)
	buckets map[string]*sentBucket
}

type sentBucket struct {
	tokens float64
	at     time.Time
}

// take uses one of id's tokens. When there is none it returns how long until the next one.
func (l *sentLimiter) take(id string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	b := l.buckets[id]
	if b == nil {
		if l.buckets == nil {
			l.buckets = map[string]*sentBucket{}
		}
		b = &sentBucket{tokens: sentRecordBurst, at: now}
		l.buckets[id] = b
	}
	if d := now.Sub(b.at); d > 0 {
		b.tokens = min(sentRecordBurst, b.tokens+float64(d)/float64(sentRecordEvery))
		b.at = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	return time.Duration((1 - b.tokens) * float64(sentRecordEvery))
}

// extensionSentRecord answers an extension's sent.record (docs/extensions/extensions.md "Sent as the user: sent.record"): its
// connector says it sent a message in the user's name outside the write gate, which this keeps in the ledger like the
// gate's own. At most sentRecordBurst calls at once and then one a second per extension, refused or not (rate_limited,
// data {retry_after}). Only a connector of the caller's that declares sends_as_user may (permission_denied otherwise,
// nothing written), whether or not the user switched the connector off since: the send already happened. The row's
// origin and how it was let through are the hub's words, whatever the params say: origin is the extension (the machine
// and session it names are its own word, kept beside it), let_through is auto (with the setting that allowed it). A
// failed write is the caller's to see, as an error with a message and no code: the send already happened and stays so.
func (ps *extensions) extensionSentRecord(ctx context.Context, id string, raw json.RawMessage) (any, error) {
	if wait := ps.sentLimit.take(id); wait > 0 {
		data, _ := json.Marshal(map[string]float64{"retry_after": wait.Seconds()})
		return nil, &rpc.Error{Code: serve.CodeRateLimited, Data: data,
			Message: fmt.Sprintf("sent.record: more than %d calls in a minute, try again in %.0f seconds", sentRecordBurst, max(1, wait.Seconds()))}
	}
	var p connector.SentRecordParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &rpc.Error{Code: "invalid_params", Message: "sent.record: " + err.Error()}
		}
	}
	if ps.reg == nil || !ps.reg.SendsAsUser(id, p.Contribution) {
		return nil, &rpc.Error{Code: extension.CodePermissionDenied,
			Message: "sent.record: " + id + " has no connector " + p.Contribution + " that declares sends_as_user"}
	}
	if p.Text == nil {
		return nil, &rpc.Error{Code: "invalid_params", Message: "sent.record: text is required (it may be empty)"}
	}
	target, brief, err := recordedTarget(p.Target)
	if err != nil {
		return nil, &rpc.Error{Code: "invalid_params", Message: "sent.record: " + err.Error()}
	}
	var st sentStore
	if ps.h != nil {
		st, _ = ps.h.store.(sentStore)
	}
	if st == nil {
		return nil, &rpc.Error{Code: "no_database", Message: "sent.record: no database, there is no ledger"}
	}
	origin := map[string]any{"extension": id}
	if v := shortValue(p.MachineID); v != "" {
		origin["machine_id"] = v
	}
	if v := shortValue(p.SessionID); v != "" {
		origin["session_id"] = v
	}
	through := map[string]any{"kind": connector.ThroughAuto}
	if v := shortValue(p.Setting); v != "" {
		through["setting"] = v
	}
	summary := strings.TrimSpace(cleanText(p.Summary))
	if summary == "" {
		summary = strings.TrimSuffix(strings.TrimSpace(p.Contribution+": "+brief), ":")
	}
	summary, _, _ = strings.Cut(summary, "\n") // the given line or the made one: a value in the target may have several
	row := db.SentAsUser{At: time.Now(), Connector: p.Contribution, Action: sentRecordAction, Target: mustJSON(target),
		Text: cutRunes(cleanText(*p.Text), connector.SentTextMost), Summary: cutRunes(strings.TrimSpace(summary), sentSummaryMost),
		Origin: mustJSON(origin), LetThrough: mustJSON(through), MessageRef: proto.CleanMessageRef(p.MessageRef)}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := st.RecordSentAsUser(ctx, row); err != nil {
		ps.logf("sent ledger: %s: %s: %v", id, p.Contribution, err)
		return nil, errors.New("sent.record: the ledger could not be written")
	}
	return struct{}{}, nil
}

// recordedTarget is the target an extension gave, as a row keeps it: values that are short strings, numbers,
// booleans or short lists of strings (anything else, nested objects included, is dropped, as a gated write's target
// is), at most sentTargetKeys of them in key order, each key cut to sentKeyMost characters; and the same in a few
// words. A missing target is an error.
func recordedTarget(fields map[string]any) (map[string]any, string, error) {
	if fields == nil {
		return nil, "", errors.New("target: an object of where it went")
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	target := map[string]any{}
	var brief []string
	for _, k := range keys {
		v, ok := scalarOrStrings(fields[k])
		name := cutRunes(cleanText(k), sentKeyMost)
		if !ok || name == "" || len(target) == sentTargetKeys {
			continue
		}
		target[name] = v
		brief = append(brief, name+" "+fmt.Sprint(v))
	}
	return target, strings.Join(brief, ", "), nil
}

// RecordNodeSent answers a node's lark.sent (proto.MethodLarkSent, node/gateproxy.go): its gate proxy forwarded a write
// as the user and the API took it. The node's gate decided before the command ran and cannot see whether it sent, so
// this is where such a write gets its row. machineID is the connection the request came in on, a computer of this
// manager's (permission_denied for any other): the row's origin.machine_id is that, whatever the params say, and so
// are the rest of the row's words but the ones the node can alone know: the call (the gated command, method and path, a
// write only), what it said, the conversation it ran for, how the gate let it through and the id the API's answer gave
// for the message (kept only for a route that creates one, createsMessage). The node being the user's own, its word is
// taken for those, cut and cleaned like the other sources'. A failed write is the node's to log, as the send already
// happened and stays so.
func (h *Manager) RecordNodeSent(ctx context.Context, machineID string, raw json.RawMessage) (any, error) {
	if h.machineByID(machineID) == nil {
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: "lark.sent: not from a computer of this manager"}
	}
	var p proto.LarkSent
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpc.Error{Code: "invalid_params", Message: "lark.sent: " + err.Error()}
	}
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	path := cutRunes(strings.TrimSpace(cleanText(p.Path)), sentValueMost)
	if !nodeWriteMethods[method] || !strings.HasPrefix(path, "/") {
		return nil, &rpc.Error{Code: "invalid_params", Message: "lark.sent: method and path of a write (POST, PUT, PATCH, DELETE or OPTIONS, and a path from /)"}
	}
	through, err := nodeLetThrough(p.LetThrough)
	if err != nil {
		return nil, &rpc.Error{Code: "invalid_params", Message: "lark.sent: " + err.Error()}
	}
	if p.LetThrough.Kind == proto.LarkThroughRule && h.isUserLarkRule(p.LetThrough.Rule) {
		through["user_rule"] = true
	}
	st, _ := h.store.(sentStore)
	if st == nil {
		return nil, &rpc.Error{Code: "no_database", Message: "lark.sent: no database, there is no ledger"}
	}
	target := map[string]any{"method": method, "path": path}
	if v := cutRunes(strings.TrimSpace(cleanText(p.Command)), sentValueMost); v != "" { // the gate the call went through
		target["command"] = v
	}
	for k, v := range gatePathIDs(path) {
		target[k] = v
	}
	origin := map[string]any{"machine_id": machineID}
	if v := cutRunes(strings.TrimSpace(cleanText(p.SessionID)), sentValueMost); v != "" {
		origin["session_id"] = v
	}
	action := method + " " + path
	row := db.SentAsUser{At: time.Now(), Connector: connector.Lark, Action: action, Target: mustJSON(target),
		Text: cutRunes(cleanText(p.Text), connector.SentTextMost), Summary: cutRunes(action, sentSummaryMost),
		Origin: mustJSON(origin), LetThrough: mustJSON(through)}
	if createsMessage(method, path) { // the answer's id is the message's only when the route made one
		row.MessageRef = proto.CleanMessageRef(p.MessageRef)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := st.RecordSentAsUser(ctx, row); err != nil {
		h.logf("sent ledger: %s: %s: %v", machineID, action, err)
		return nil, errors.New("lark.sent: the ledger could not be written")
	}
	return struct{}{}, nil
}

// isUserLarkRule says whether the rule a node's gate says let a write through is one the user made from the ledger (chat_rules.go):
// the same method and path as one of theirs, allowing, with no body match. The node does not know which of the rules it
// was served are the user's; a hand-written rule of the very same method and path would decide the same way and comes
// first, so then it is not told as theirs.
func (h *Manager) isUserLarkRule(r *proto.LarkRule) bool {
	if r == nil || r.Action != connector.Allow || len(r.Body) > 0 {
		return false
	}
	same := func(o proto.LarkRule) bool {
		return o.Action == connector.Allow && len(o.Body) == 0 && strings.EqualFold(o.Method, r.Method) && o.Path == r.Path
	}
	return slices.ContainsFunc(connector.LarkRules(h.connectors.UserRules(), nil), same) &&
		!slices.ContainsFunc(connector.LarkRules(h.cfg.ConnectorGate, h.cfg.LarkGate), same)
}

// nodeWriteMethods are the methods the gate treats as writes (node/larkgate_rules.go larkRead: all but GET and HEAD).
var nodeWriteMethods = map[string]bool{"POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true}

// nodeLetThrough is a row's let_through for how a node's gate let a write through: a rule of the gate (written as the
// connector_gate rule it came from: connector "lark", the action as "<METHOD> <path>" or "*", the body's fields as
// match), "conversation_always" (the user had allowed that route for the conversation, so no card was shown), or the
// card with its id and who answered it.
func nodeLetThrough(t proto.LarkLetThrough) (map[string]any, error) {
	through := map[string]any{"kind": t.Kind}
	switch t.Kind {
	case proto.LarkThroughRule:
		r := t.Rule
		if r == nil || r.Action != connector.Allow {
			return nil, errors.New("let_through: a rule that allowed it")
		}
		action := "*"
		if r.Method != "*" || r.Path != "**" {
			action = strings.TrimSpace(strings.ToUpper(r.Method) + " " + r.Path)
		}
		rule := map[string]any{"connector": connector.Lark, "action": cutRunes(cleanText(action), sentValueMost), "decision": connector.Allow}
		if len(r.Body) > 0 {
			match, _, err := recordedTarget(r.Body)
			if err != nil {
				return nil, err
			}
			rule["match"] = match
		}
		through["rule"] = rule
	case proto.LarkThroughAlways:
	case proto.LarkThroughCard:
		if v := cutRunes(strings.TrimSpace(cleanText(t.InteractionID)), sentValueMost); v != "" {
			through["interaction_id"] = v
			if by := cutRunes(strings.TrimSpace(cleanText(t.AnsweredBy)), sentValueMost); by != "" {
				through["answered_by"] = by
			}
		}
	default:
		return nil, fmt.Errorf("let_through: kind %q is not rule, conversation_always or card", t.Kind)
	}
	return through, nil
}

// nodeCreationRoutes are the routes of a gated API that create one message in the conversation their path names: the
// text send, and the attachments' own (images and files, docs/extensions/lark-attachments.md; the messaging extension point's
// attachments, docs/extensions/extension-messaging.md "6"). Their answer is the receipt of that message.
var nodeCreationRoutes = []string{"messages", "images", "files", "attachments"}

// createsMessage says whether a write forwarded by the node's gate proxy is one of nodeCreationRoutes: POST
// /conversations/<id>/<route>, an id in it. Anything else leaves the row's message_ref empty, even when its answer has
// an id: a reply (/messages/<id>/replies) and a forward are not known to answer with the new message's id, a reaction or
// a read position answers with the id of a message that is someone else's, and a wrong ref (the one a recall would act
// on) is worse than none.
func createsMessage(method, path string) bool {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	return method == http.MethodPost && len(segs) == 3 && segs[0] == "conversations" && segs[1] != "" && segs[1] != "*" &&
		slices.Contains(nodeCreationRoutes, segs[2])
}

// gatePathIDs are the ids a gated call's path names in the way REST paths do: the segment after "conversations" is the
// conversation, the one after "messages" the message. A collection named twice, or with no id after it, gives
// nothing: a row says no id rather than a guess.
func gatePathIDs(path string) map[string]string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	ids, seen := map[string]string{}, map[string]bool{}
	for i, seg := range segs {
		key := ""
		switch seg {
		case "conversations":
			key = "conversation"
		case "messages":
			key = "message"
		default:
			continue
		}
		if seen[key] {
			delete(ids, key)
			continue
		}
		seen[key] = true
		if i+1 < len(segs) {
			if id := segs[i+1]; id != "" && id != "*" && len([]rune(id)) <= sentValueMost && cleanText(id) == id {
				ids[key] = id
			}
		}
	}
	return ids
}

// sentLedgerNow is the clock the prune reads (tests pin it).
var sentLedgerNow = time.Now

// pruneSentLedger deletes the rows older than the days the ledger is kept (sent_ledger_days). The cut is counted in
// UTC: in local time, N calendar days back across a DST change is N×24 h ± 1 h.
func (h *Manager) pruneSentLedger(ctx context.Context) {
	st, _ := h.store.(sentStore)
	days := h.cfg.SentLedgerDays
	if st == nil || days < 0 {
		return
	}
	if days == 0 {
		days = sentLedgerDays
	}
	n, err := st.PruneSentAsUser(ctx, sentLedgerNow().UTC().AddDate(0, 0, -days))
	switch {
	case err != nil && ctx.Err() == nil:
		h.logf("sent ledger: pruning: %v", err)
	case n > 0:
		h.logf("sent ledger: %d rows older than %d days deleted", n, days)
	}
}

// sentLedgerLoop prunes the ledger at the start and every few hours after.
func (h *Manager) sentLedgerLoop(ctx context.Context) {
	if _, ok := h.store.(sentStore); !ok || h.cfg.SentLedgerDays < 0 {
		return
	}
	h.pruneSentLedger(ctx)
	t := time.NewTicker(sentPruneEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.pruneSentLedger(ctx)
		}
	}
}

// sentRoutes: GET /api/v1/connectors/sent?before=&limit=&connector=, the ledger newest first, a page at a time
// (docs/protocol/protocol.md "Connector API"). Read by one of the user's own full devices or the owner on this machine, never
// by an answer-only device, a pairing token (a script or an AI the user paired) or an extension's token: it holds the
// text of what was written as the user.
func (h *Manager) sentRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/connectors/sent", func(w http.ResponseWriter, r *http.Request) {
		if !mayReadLedger(r.Context()) {
			writeErr(w, http.StatusForbidden, errors.New("only a device with full rights reads what was sent as the user"))
			return
		}
		st, _ := h.store.(sentStore)
		if st == nil {
			writeErrCode(w, http.StatusServiceUnavailable, "no_database", errors.New("no database: there is no ledger"))
			return
		}
		q := r.URL.Query()
		limit := db.SentAsUserDefault
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("limit %q: a number from 1", v))
				return
			}
			limit = min(n, db.SentAsUserMost)
		}
		var before int64
		if v := q.Get("before"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 1 {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("before %q: a row id, as next gave it", v))
				return
			}
			before = n
		}
		rows, next, err := st.SentAsUsers(r.Context(), before, limit, q.Get("connector"))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if rows == nil {
			rows = []db.SentAsUser{}
		}
		var nextOut any // null on the last page
		if next > 0 {
			nextOut = next
		}
		writeJSON(w, map[string]any{"items": h.sentItems(r.Context(), rows), "next": nextOut})
	})
}
