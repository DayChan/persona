package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	wireschema "github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/extension"
	progconf "github.com/nwverse/persona/server/internal/extension/conformance"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What CheckProgram asks for, the way the hub does: a thread's page, an archive call's conversations.
const (
	programThreadLimit  = 20
	programArchiveMost  = 20 // assistant/kb_sources.go: kbConnectorLimit
	programArchiveCall  = 6  // calls to follow the archive's cursor to its end
	programHistoryChats = 3  // conversations whose history is read back
	programHistoryPages = 4  // pages read back in each
	programHistoryLimit = 20 // what each page asks for (a hint: a source may answer more)
	programCallTimeout  = 30 * time.Second
	programSignInWait   = 15 * time.Second
)

// programCaps are the capabilities the hub can reach in a program: connector/proc.Conn implements their interfaces, and a
// manifest declaring another one does not pass connector.Check there. CapFallback has none: it is a promise about the
// inbox things (checkCollect holds them to it). CapRecall has none either: it is a program's word that recall is there.
// CheckProgram never calls recall, recall.do (a recall deletes a message) or recall.can (execution-time preflight only).
var programCaps = []connector.Capability{connector.CapStatus, connector.CapTools, connector.CapActions, connector.CapEvents,
	connector.CapInbox, connector.CapArchive, connector.CapItems, connector.CapLogin, connector.CapThreads, connector.CapFallback,
	connector.CapRecall, connector.CapHistory, connector.CapHistoryAnchors, connector.CapCatalog}

// ProgramOption tunes CheckProgram.
type ProgramOption func(*programOptions)

type programOptions struct {
	login map[string]string
	data  bool
}

// WithLogin signs the program in through its login steps, giving each field it asks for by name from values, and signs
// it out at the end. The program must then be ok, so its other capabilities answer for real. Only for a backend that
// is a fixture: never give it a real account's credentials.
func WithLogin(values map[string]string) ProgramOption {
	return func(o *programOptions) { o.login = values }
}

// RequireData fails an empty answer of inbox.collect, thread.page, archive.changes, history.chats and history.anchors: for a backend known to have
// something to show, so a program that passes by saying nothing does not pass.
func RequireData() ProgramOption { return func(o *programOptions) { o.data = true } }

// CheckProgram holds a connector program written in any language to the connector protocol (docs/protocol/connector-protocol.md).
// Check compares the wire with a Go connector's own answers; a program has no Go side, so what it says is held to the
// Go types the hub reads instead: each answer is decoded strictly (a field the hub does not know, a key that is not
// snake_case, a time that is not RFC 3339 fails) and then to what the hub relies on, see the check functions below.
//
// m is the plugin's manifest, run the running program (plugin/conformance.Program) and init what to send with
// initialize (the plugin's id and version come from m). For every connector m contributes it calls, once and in the
// order of the hub's use: status, login.step, tools.list and tools.call, actions.list and actions.describe,
// inbox.collect (twice: a thread's key must stay), thread.page (and one page back, and one media), archive.changes
// (to the end of its cursor, and once more), history.chats and archive.history (a few pages back in a few
// conversations), history.anchors (and the page around one), items.list, and listens to the changed events it sends.
//
// It never calls actions.do, inbox.reply, recall or recall.do (writes only ever run after the user's explicit action),
// sends actions.describe empty arguments (a program refuses them or answers well), and calls logout only after
// WithLogin signed in. How the program starts, and stops with the hub, is plugin/conformance.Check's.
func CheckProgram(t testing.TB, m *extension.Manifest, run progconf.Run, init extension.InitializeParams, opts ...ProgramOption) {
	t.Helper()
	items, err := connectorItems(m)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p := &probe{t: t, ctx: ctx, hub: rpc.New("h", run.ToProgram), secrets: map[string][]byte{}}
	for _, o := range opts {
		o(&p.opts)
	}
	for _, it := range items {
		p.ids = append(p.ids, it.ID)
	}
	p.hub.OnRequest = p.fromProgram
	p.hub.OnEvent = p.onEvent
	go func() { _ = p.hub.Serve(ctx, run.FromProgram) }()

	p.initialize(m, items, init)
	for _, it := range items {
		p.checkConnector(it)
	}
	p.report(items)
}

// connectorItems are the connectors m contributes.
func connectorItems(m *extension.Manifest) ([]extension.ConnectorItem, error) {
	var items []extension.ConnectorItem
	if err := json.Unmarshal(m.Contributes["connectors"], &items); err != nil || len(items) == 0 {
		return nil, fmt.Errorf("contributes no connectors (%v)", err)
	}
	for _, it := range items {
		for kind := range it.KindDescriptions {
			if !slices.Contains(it.Kinds, kind) {
				return nil, fmt.Errorf("connector %s describes kind %q, which it does not declare", it.ID, kind)
			}
		}
	}
	return items, nil
}

// probe is the hub's side of the conversation with one program, and what it has seen of it.
type probe struct {
	t    testing.TB
	ctx  context.Context
	hub  *rpc.Peer
	opts programOptions
	ids  []string // the plugin's connectors

	// Only the goroutine of CheckProgram reads and writes these: the connector being checked, whether its status said
	// ok (the hub asks nothing of one that is not), its actions, and the secret values the sign-in was given.
	item     extension.ConnectorItem
	signedIn bool
	actions  []proto.Tool
	watch    []string

	mu       sync.Mutex // the program's calls and events arrive on another goroutine
	problems []string
	events   []observed
	secrets  map[string][]byte // what each connector keeps with secrets.save
}

// observed is an event the program sent.
type observed struct {
	name   string
	params json.RawMessage
}

func (p *probe) problem(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.problems = append(p.problems, msg)
}

// failf records a problem with the connector being checked; the test fails at the end, with all of them.
func (p *probe) failf(format string, args ...any) {
	p.mu.Lock()
	id := p.item.ID
	p.mu.Unlock()
	p.problem(id + ": " + fmt.Sprintf(format, args...))
}

// fatalf ends the test where the program cannot answer, after saying what was found until then.
func (p *probe) fatalf(format string, args ...any) {
	p.t.Helper()
	p.flush()
	p.t.Fatalf(format, args...)
}

func (p *probe) flush() {
	p.t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, msg := range p.problems {
		p.t.Errorf("%s", msg)
	}
	p.problems = nil
}

func (p *probe) has(c connector.Capability) bool {
	return slices.Contains(p.item.Capabilities, string(c))
}

// said looks in what the program answered for a secret the sign-in was given: an answer never carries one.
func (p *probe) said(what string) {
	for _, secret := range p.watch {
		if strings.Contains(what, secret) {
			p.failf("an answer contains a secret the sign-in was given (answers, statuses and logs never carry one)")
			return
		}
	}
}

// secretCall is the program's call for its secret: the connector it is for, and the data to keep.
type secretCall struct {
	extension.Target
	serve.SecretData
}

// fromProgram answers the program's calls: secrets.*, each connector's own.
func (p *probe) fromProgram(_ context.Context, method string, params json.RawMessage) (any, error) {
	rest, ok := strings.CutPrefix(method, serve.Namespace+".")
	if !ok || !slices.Contains([]string{serve.MSecretsLoad, serve.MSecretsSave, serve.MSecretsDelete}, rest) {
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method}
	}
	if err := wireschema.ValidateParams(method, params); err != nil {
		p.problem(fmt.Sprintf("%s: the params do not hold to schema/extension: %v", method, err))
	}
	var c secretCall
	if err := decodeStrict(params, &c); err != nil {
		p.problem(method + ": " + err.Error())
		return nil, err
	}
	if !slices.Contains(p.ids, c.Contribution) {
		p.problem(fmt.Sprintf("%s names contribution %q, the plugin has %v", method, c.Contribution, p.ids))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch rest {
	case serve.MSecretsSave:
		p.secrets[c.Contribution] = c.Data
	case serve.MSecretsDelete:
		delete(p.secrets, c.Contribution)
	default:
		return serve.SecretData{Data: p.secrets[c.Contribution]}, nil
	}
	return struct{}{}, nil
}

func (p *probe) onEvent(event string, params json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, observed{event, slices.Clone(params)})
}

func (p *probe) stored(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.secrets[id])
}

// initialize opens the plugin protocol and holds what the program contributes to the manifest.
func (p *probe) initialize(m *extension.Manifest, items []extension.ConnectorItem, init extension.InitializeParams) {
	p.t.Helper()
	init.Protocol = extension.Protocol
	init.Extension.ID, init.Extension.Version = m.ID, m.Version
	if init.Hub.ExtensionPoints == nil {
		init.Hub.ExtensionPoints = extension.PointVersions()
	}
	if init.Active == nil {
		init.Active = map[string][]string{"connectors": p.ids}
	}
	ctx, cancel := context.WithTimeout(p.ctx, programCallTimeout)
	defer cancel()
	var raw json.RawMessage
	if err := p.hub.Call(ctx, extension.MInitialize, init, &raw); err != nil {
		p.fatalf("initialize: %v", err)
	}
	if err := checkInitialize(m, items, raw); err != nil {
		p.problem("initialize: " + err.Error())
	}
}

// call runs a connector method as the hub does. A refusal (an error answer) comes back as rejected; a program that is
// gone or silent ends the test.
func (p *probe) call(method string, params any) (raw json.RawMessage, rejected *rpc.Error) {
	p.t.Helper()
	wire, err := extension.Targeted(params, p.item.ID)
	if err != nil {
		p.fatalf("%s: %v", method, err)
	}
	ctx, cancel := context.WithTimeout(p.ctx, programCallTimeout)
	defer cancel()
	if err := paramsHold(serve.InPlugin(method), wire); err != nil {
		p.problem(err.Error())
	}
	err = p.hub.Call(ctx, serve.InPlugin(method), wire, &raw)
	var re *rpc.Error
	switch {
	case err == nil:
		p.said(string(raw))
		if err := wireschema.ValidateResult(serve.InPlugin(method), raw); err != nil {
			p.failf("%s: the answer does not hold to schema/extension: %v", method, err)
		}
		return raw, nil
	case errors.As(err, &re):
		p.said(re.Message)
		switch {
		case re.Message == "":
			p.failf("%s: an error answer without a message (it is for people)", method)
		case slices.Contains([]string{serve.CodeUnknownMethod, serve.CodeNoCapability, serve.CodeNotInitialized, extension.CodeNoContribution}, re.Code):
			p.failf("%s: answered %s for what its manifest declares", method, re.Code)
		}
		return nil, re
	}
	p.fatalf("%s: %v", method, err)
	return nil, nil
}

// ask calls method and holds the answer to check. A refusal is a failure only of a program that is ok: the hub asks
// nothing of one that is not.
func ask[T any](p *probe, method string, params any, check func(json.RawMessage) (T, error)) (v T, ok bool) {
	p.t.Helper()
	raw, rejected := p.call(method, params)
	if rejected != nil {
		if p.signedIn {
			p.failf("%s: refused while ok: %s", method, rejected.Message)
		}
		return v, false
	}
	v, err := check(raw)
	if err != nil {
		p.failf("%s: %v", method, err)
		return v, false
	}
	return v, true
}

// need fails an answer without anything in it, when the backend is known to have something (RequireData).
func (p *probe) need(method string, n int) {
	if p.opts.data && p.signedIn && n == 0 {
		p.failf("%s: nothing came back, and the backend has data", method)
	}
}

func (p *probe) checkConnector(it extension.ConnectorItem) {
	p.t.Helper()
	p.mu.Lock()
	p.item = it
	p.mu.Unlock()
	p.signedIn, p.actions = true, nil // a connector without a status is always ok
	p.checkManifest(it)
	if p.opts.login != nil && !p.has(connector.CapLogin) {
		p.failf("WithLogin was given, and the connector declares no login")
	}

	if p.has(connector.CapStatus) {
		p.status()
	}
	if p.has(connector.CapLogin) {
		if p.login() && p.has(connector.CapStatus) {
			p.waitOK()
		}
	}
	if p.has(connector.CapCatalog) {
		ask(p, serve.MCatalog, struct{}{}, checkCatalog)
	}
	var tools []proto.Tool
	if p.has(connector.CapTools) {
		tools, _ = ask(p, serve.MTools, struct{}{}, func(raw json.RawMessage) ([]proto.Tool, error) { return checkToolList(it.ID, "tools", raw) })
		p.callTools(tools)
	}
	if p.has(connector.CapActions) {
		p.actions, _ = ask(p, serve.MActions, struct{}{}, func(raw json.RawMessage) ([]proto.Tool, error) { return checkToolList(it.ID, "actions", raw) })
		for _, a := range p.actions {
			if slices.ContainsFunc(tools, func(t proto.Tool) bool { return t.Name == a.Name }) {
				p.failf("%s is a tool and an action: the read would go through the gate, or the write around it", a.Name)
			}
		}
		p.describe()
	}
	var things []connector.InboxThing
	if p.has(connector.CapInbox) {
		things = p.collect(it.Kinds)
	}
	if p.has(connector.CapThreads) && len(things) > 0 {
		// An open thing's conversation, if there is one: a finished thing may have nothing left to show.
		p.thread(things[max(slices.IndexFunc(things, func(x connector.InboxThing) bool { return x.Done == "" }), 0)].Thread)
	}
	if p.has(connector.CapArchive) {
		p.archive()
	}
	if p.has(connector.CapHistory) {
		p.history()
	}
	if p.has(connector.CapHistoryAnchors) {
		p.anchors()
	}
	if p.has(connector.CapItems) {
		ask(p, serve.MItems, struct{}{}, checkItems)
	}
	if p.opts.login != nil && p.has(connector.CapLogin) {
		p.logout()
	}
}

// checkManifest holds a connector's declaration to what the hub accepts of a program's (connector.Check).
func (p *probe) checkManifest(it extension.ConnectorItem) {
	for i, c := range it.Capabilities {
		switch {
		case !slices.Contains(programCaps, connector.Capability(c)):
			p.failf("declares %q, which the hub cannot reach in a program", c)
		case slices.Index(it.Capabilities, c) != i:
			p.failf("declares %q twice", c)
		}
	}
	if len(it.Kinds) > 0 && !p.has(connector.CapInbox) {
		p.failf("declares inbox kinds without an inbox")
	}
	if p.has(connector.CapHistory) && !p.has(connector.CapArchive) {
		p.failf("declares history without an archive")
	}
	if p.has(connector.CapHistoryAnchors) && !p.has(connector.CapHistory) {
		p.failf("declares history_anchors without history")
	}
}

func (p *probe) snapshot() []observed {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.events)
}

func (p *probe) status() (connector.Status, bool) {
	p.t.Helper()
	raw, rejected := p.call(serve.MStatus, struct{}{})
	if rejected != nil {
		p.failf("status: refused: %s (it answers from memory)", rejected.Message)
		return connector.Status{}, false
	}
	st, err := checkStatus(raw)
	if err != nil {
		p.failf("status: %v", err)
		return st, false
	}
	p.signedIn = st.State == connector.StateOK
	return st, true
}

// waitOK waits for the status to be ok after the sign-in (a program connects in the background).
func (p *probe) waitOK() {
	p.t.Helper()
	for deadline := time.Now().Add(programSignInWait); ; time.Sleep(250 * time.Millisecond) {
		st, ok := p.status()
		if !ok || st.State == connector.StateOK {
			return
		}
		if time.Now().After(deadline) {
			p.failf("signed in, and the status is still %s: %s", st.State, st.Error)
			return
		}
	}
}

// callTools calls each read tool without arguments: it answers or refuses them, in words.
func (p *probe) callTools(tools []proto.Tool) {
	p.t.Helper()
	for _, tl := range tools {
		if raw, rejected := p.call(serve.MCall, serve.CallParams{Name: tl.Name, Args: json.RawMessage(`{}`)}); rejected == nil {
			var res serve.CallResult
			if err := decodeWire(raw, &res); err != nil {
				p.failf("tools.call %s: %v", tl.Name, err)
			}
		}
	}
}

// describe sends each action empty arguments: refused (they are no arguments), or the card says what it does.
func (p *probe) describe() {
	p.t.Helper()
	for _, a := range p.actions {
		if raw, rejected := p.call(serve.MDescribe, serve.CallParams{Name: a.Name, Args: json.RawMessage(`{}`)}); rejected == nil {
			if err := checkDescribe(a.Name, raw); err != nil {
				p.failf("actions.describe %s: %v", a.Name, err)
			}
		}
	}
}

// login asks for the first step and, with WithLogin, answers the steps until the program says it is done (true).
func (p *probe) login() bool {
	p.t.Helper()
	step, values := "", map[string]string(nil)
	for range 5 {
		raw, rejected := p.call(serve.MLoginStep, serve.LoginParams{Step: step, Values: values})
		if rejected != nil {
			p.failf("login.step %q: refused: %s", step, rejected.Message)
			return false
		}
		prompt, err := checkPrompt(raw)
		if err != nil {
			p.failf("login.step %q: %v", step, err)
			return false
		}
		if p.opts.login == nil {
			return false
		}
		if prompt.Done {
			return true
		}
		if prompt.Kind == connector.PromptQRCode {
			// A fixture has nobody to scan: it says the scan happened when asked again, as the app does.
			step, values = prompt.Step, nil
			continue
		}
		if prompt.Error != "" || prompt.Kind != "" {
			p.failf("login.step %q: cannot go on (error %q, kind %q)", step, prompt.Error, prompt.Kind)
			return false
		}
		step, values = prompt.Step, map[string]string{}
		for _, f := range prompt.Fields {
			v, ok := p.opts.login[f.Name]
			if !ok {
				p.failf("login.step %q asks for %q, WithLogin has no value for it", step, f.Name)
				return false
			}
			values[f.Name] = v
			if f.Secret && len(v) >= 6 {
				p.watch = append(p.watch, v)
			}
		}
	}
	p.failf("login.step: not done after 5 steps")
	return false
}

func (p *probe) logout() {
	p.t.Helper()
	if raw, rejected := p.call(serve.MLogout, struct{}{}); rejected != nil {
		p.failf("logout: refused: %s", rejected.Message)
		return
	} else if err := decodeWire(raw, &struct{}{}); err != nil {
		p.failf("logout: %v", err)
	}
	if p.has(connector.CapStatus) {
		if st, ok := p.status(); ok && st.State == connector.StateOK {
			p.failf("signed out, and the status is still ok")
		}
	}
	if p.stored(p.item.ID) > 0 {
		p.failf("signed out, and its secret is still in the hub's store (secrets.delete)")
	}
}

// collect asks for the inbox twice: the thread of a thing is its key, and must stay the same.
func (p *probe) collect(kinds []string) []connector.InboxThing {
	p.t.Helper()
	fallback := p.has(connector.CapFallback)
	check := func(raw json.RawMessage) ([]connector.InboxThing, error) { return checkCollect(kinds, fallback, raw) }
	things, ok := ask(p, serve.MCollect, struct{}{}, check)
	if !ok {
		return nil
	}
	p.need(serve.MCollect, len(things))
	if again, ok := ask(p, serve.MCollect, struct{}{}, check); ok {
		for _, th := range things {
			if !slices.ContainsFunc(again, func(x connector.InboxThing) bool { return x.Thread == th.Thread }) {
				p.failf("inbox.collect: thread %q is gone the second time (the key of a thread must stay the same)", th.Thread)
			}
		}
	}
	return things
}

// thread reads the newest page of a thread, one page back from it, and one media.
func (p *probe) thread(thread string) {
	p.t.Helper()
	check := func(raw json.RawMessage) (connector.ThreadPage, error) {
		return checkThreadPage(programThreadLimit, p.actions, raw)
	}
	page, ok := ask(p, serve.MThread, serve.ThreadParams{Thread: thread, Limit: programThreadLimit}, check)
	if !ok {
		return
	}
	p.need(serve.MThread, len(page.Messages))
	if page.Before != "" {
		if older, ok := ask(p, serve.MThread, serve.ThreadParams{Thread: thread, Before: page.Before, Limit: programThreadLimit}, check); ok {
			if err := checkOlder(page, older); err != nil {
				p.failf("thread.page before %q: %v", page.Before, err)
			}
		}
	}
	refs := []string{page.Chat.Avatar}
	for _, m := range page.Messages {
		refs = append(refs, m.Sender.Avatar)
		for _, md := range m.Media {
			refs = append(refs, md.Ref, md.Thumb)
		}
	}
	if i := slices.IndexFunc(refs, func(r string) bool { return r != "" }); i >= 0 {
		ask(p, serve.MMedia, serve.MediaParams{Ref: refs[i]}, checkMedia)
	}
}

// archive follows the cursor from the start to the end, then asks once more from there: the knowledge base only
// appends, so a message delivered twice is in the log twice.
func (p *probe) archive() {
	p.t.Helper()
	cursor := ""
	delivered := map[string]bool{}
	call := func(i int) (serve.ChangesResult, bool) {
		check := func(raw json.RawMessage) (serve.ChangesResult, error) {
			return checkChanges(cursor, programArchiveMost, raw)
		}
		res, ok := ask(p, serve.MChanges, serve.ChangesParams{Cursor: cursor, Limit: programArchiveMost}, check)
		if !ok {
			return res, false
		}
		if i == 0 {
			p.need(serve.MChanges, len(res.Batches))
		}
		for _, b := range res.Batches {
			for _, m := range b.Messages {
				if key := b.Chat.ID + "\x00" + m.ID; delivered[key] {
					p.failf("archive.changes: message %q of chat %q delivered twice", m.ID, b.Chat.ID)
				} else {
					delivered[key] = true
				}
			}
		}
		if res.Next == cursor && (len(res.Batches) > 0 || res.More) {
			p.failf("archive.changes: next is still %q after %d chat(s), more=%v (the same would be read again)", cursor, len(res.Batches), res.More)
			return res, false
		}
		return res, true
	}
	for i := range programArchiveCall {
		res, ok := call(i)
		if !ok {
			return
		}
		cursor = res.Next
		if !res.More {
			break
		}
	}
	call(programArchiveCall)
}

// history lists the conversations to import and reads a few of them back from their newest page, the way the knowledge
// base imports one: it goes on from the cursor the page answers (never from how many it asked for), stops at the start,
// skips an unreadable one, and stops where the program asks it to.
func (p *probe) history() {
	p.t.Helper()
	chats, ok := ask(p, serve.MHistoryChats, struct{}{}, checkHistoryChats)
	if !ok {
		return
	}
	p.need(serve.MHistoryChats, len(chats))
	for _, chat := range chats[:min(len(chats), programHistoryChats)] {
		q := serve.HistoryParams{Chat: chat.ID, Limit: programHistoryLimit}
		var newer *connectorwire.HistoryPage
		for range programHistoryPages {
			page, ok := ask(p, serve.MHistory, q, func(raw json.RawMessage) (connectorwire.HistoryPage, error) { return checkHistoryPage(q, raw) })
			if !ok {
				break
			}
			if newer != nil {
				if err := checkHistoryOlder(*newer, page); err != nil {
					p.failf("archive.history of %q before %q: %v", chat.ID, q.Before, err)
					break
				}
			}
			if page.Unreadable || page.Pace != nil || !page.More || len(page.Messages) == 0 {
				break
			}
			newer, q.Before = &page, page.Older
		}
	}
}

// anchors asks for the owner's messages with no window (a fixture's are dated once and stay put, so a window that
// moves with the clock would find none a month later and quietly stop reading the page around one), then the page
// around the first one.
func (p *probe) anchors() {
	p.t.Helper()
	q := serve.HistoryAnchorsParams{Limit: programHistoryLimit}
	res, ok := ask(p, serve.MHistoryAnchors, q, checkHistoryAnchors)
	if !ok || res.Pace != nil {
		return
	}
	p.need(serve.MHistoryAnchors, len(res.Anchors))
	if len(res.Anchors) == 0 {
		return
	}
	a := res.Anchors[0]
	around := serve.HistoryParams{Chat: a.Chat, Around: a.Position, Limit: programHistoryLimit}
	ask(p, serve.MHistory, around, func(raw json.RawMessage) (connectorwire.HistoryPage, error) { return checkHistoryPage(around, raw) })
}

// report fails the test with what the run found; the events the program sent are held to the protocol.
func (p *probe) report(items []extension.ConnectorItem) {
	p.t.Helper()
	for _, e := range p.snapshot() {
		if e.name != serve.InPlugin(serve.EChanged) {
			p.problem(fmt.Sprintf("event %q: the hub only takes %s", e.name, serve.InPlugin(serve.EChanged)))
		} else if err := checkChanged(items, e.params); err != nil {
			p.problem("changed event: " + err.Error())
		} else if err := wireschema.ValidateParams(e.name, e.params); err != nil {
			p.problem("changed event: does not hold to schema/extension: " + err.Error())
		}
	}
	p.flush()
}
