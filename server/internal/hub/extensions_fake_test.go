package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The extensions are the manager's (internal/extmgr): in the hub's tests the assistant's process carries a fake of
// them (fakeExt) that answers what the hub asks over the bus — whose an extension's token is (extensions.authorize)
// and what the hub's own features need (extensions.hub_call) — as the manager would.

// fakeExt is the manager's extensions as a hub test sets them up.
type fakeExt struct {
	h      *Hub
	mu     sync.Mutex
	tokens map[string]extwire.Authorized // token hash → whose
	// calls answer extensions.hub_call by method (nil: {}).
	calls map[string]func(json.RawMessage) (any, error)
	// emitted are the events the hub told the extensions (extensions.emit).
	emitted []extwire.Emit
	// apps answer the assistant's app.calls that are the manager's (onApp).
	apps map[string]func(json.RawMessage) (any, error)
	// asked are the extensions.authorize the hub sent; served the escaped paths of the requests it relayed here.
	asked  []extwire.Authorize
	served []string
}

var fakeExts sync.Map // *Hub → *fakeExt

// fakeExtOf is h's fake (made the first time).
func fakeExtOf(h *Hub) *fakeExt {
	v, _ := fakeExts.LoadOrStore(h, &fakeExt{h: h, tokens: map[string]extwire.Authorized{}, calls: map[string]func(json.RawMessage) (any, error){}})
	return v.(*fakeExt)
}

// extTokensOf is h's fake extensions with the assistant's process connected (extension tokens are asked of it).
func extTokensOf(t *testing.T, h *Hub) *fakeExt {
	t.Helper()
	f := fakeExtOf(h)
	if _, ok := testAssistants.Load(h); !ok {
		connectAssistant(t, h)
	}
	return f
}

// issue makes a token for extension id with scopes, as the manager makes one when it starts its program.
func (f *fakeExt) issue(id, name string, scopes []string) string {
	tok := extTokenPrefix + randomToken()
	f.mu.Lock()
	f.tokens[hashToken(tok)] = extwire.Authorized{Extension: id, Name: name, Scopes: slices.Clone(scopes)}
	f.mu.Unlock()
	return tok
}

// on sets what the manager answers a hub_call method.
func (f *fakeExt) on(method string, fn func(json.RawMessage) (any, error)) {
	f.mu.Lock()
	f.calls[method] = fn
	f.mu.Unlock()
}

func (f *fakeExt) Hello(json.RawMessage) {}
func (f *fakeExt) BusUp()                {}

func (f *fakeExt) Request(_ context.Context, method string, params json.RawMessage) (any, bool, error) {
	switch method {
	case extwire.MethodAuthorize:
		var a extwire.Authorize
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, true, err
		}
		f.mu.Lock()
		f.asked = append(f.asked, a)
		out, ok := f.tokens[a.Hash]
		f.mu.Unlock()
		if !ok {
			return extwire.Authorized{}, true, nil
		}
		out.OK, out.Scope = fakeScopeOpens(out.Scopes, a.Pattern, a.Path)
		return out, true, nil
	case extwire.MethodHubCall:
		var c extwire.HubCall
		if err := json.Unmarshal(params, &c); err != nil {
			return nil, true, err
		}
		f.mu.Lock()
		fn := f.calls[c.Method]
		f.mu.Unlock()
		if fn == nil {
			return map[string]any{}, true, nil
		}
		out, err := fn(c.Params)
		return out, true, err
	}
	return nil, false, nil
}

// inboxTasks are the tasks the inbox links messages to (the assistant's ActiveTasks).
func inboxTasks(t *testing.T, h *Hub) []prompts.InboxTask {
	t.Helper()
	return assistantOf(t, h).svc.ActiveTasks(context.Background())
}

// fakeScopeOpens is the manager's check (extmgr scopeOpens) on the hub's patterns.
func fakeScopeOpens(scopes []string, pattern, path string) (bool, string) {
	scope, ok := extwire.RouteScopes[pattern]
	if !ok {
		return false, ""
	}
	if scope == extension.ScopeCallPrefix {
		if scope = extwire.ExtensionCallScope(path); scope == "" {
			return false, ""
		}
	}
	return slices.Contains(scopes, scope), scope
}

func (f *fakeExt) Event(name string, params json.RawMessage) bool {
	if name != extwire.EventEmit {
		return false
	}
	var e extwire.Emit
	_ = json.Unmarshal(params, &e)
	f.mu.Lock()
	f.emitted = append(f.emitted, e)
	f.mu.Unlock()
	return true
}

// onApp sets what the manager answers the assistant's app.call app/method in its process (connectors.snapshots…).
func (f *fakeExt) onApp(app, method string, fn func(json.RawMessage) (any, error)) {
	f.mu.Lock()
	if f.apps == nil {
		f.apps = map[string]func(json.RawMessage) (any, error){}
	}
	f.apps[app+" "+method] = fn
	f.mu.Unlock()
}

// emptyManager is what the manager answers with no extension, connector or bot set up (as the hub's tests had
// before the move: no connectors signed in, no plugins, the Feishu bot off), for the assistant's calls a test does
// not set itself.
func (f *fakeExt) emptyManager(app, method string) (any, bool) {
	switch app + " " + method {
	case assistantwire.AppConnectors + " " + assistantwire.ConnectorsSignedIn:
		return map[string]any{"connectors": []string{}, "clis": map[string]string{}, "lark_cli": false}, true
	case assistantwire.AppConnectors + " " + assistantwire.ConnectorsSnapshots:
		return map[string]any{"snapshots": []any{}}, true
	case assistantwire.AppConnectors + " " + assistantwire.ConnectorsArchives:
		return assistantwire.Archives{Archives: []assistantwire.ArchiveSource{}, Items: []string{}}, true
	case assistantwire.AppConnectors + " " + assistantwire.ConnectorsOwner:
		return map[string]any{"sources": []any{}}, true
	case assistantwire.AppPlugins + " " + assistantwire.PluginsAssistant:
		return map[string]any{"instructions": "", "skills": nil}, true
	case assistantwire.AppPlugins + " " + assistantwire.PluginsSkillsNeed:
		return map[string]any{}, true
	case assistantwire.AppFeishu + " " + assistantwire.FeishuStatusGet:
		st := assistantwire.FeishuStatus{}
		if f.h != nil && f.h.cfg != nil {
			st.PublicURL = f.h.cfg.PublicURL // hub.json's, which the manager reads too
		}
		return st, true
	case assistantwire.AppInbox + " " + assistantwire.InboxConfigMethod:
		return assistantwire.InboxConfig{}, true
	}
	return nil, false
}

func (f *fakeExt) AppCall(_ context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	f.mu.Lock()
	fn := f.apps[p.AppID+" "+p.Method]
	f.mu.Unlock()
	if fn == nil {
		out, ok := f.emptyManager(p.AppID, p.Method)
		if !ok {
			return nil, false, nil
		}
		raw, err := json.Marshal(out)
		return raw, true, err
	}
	out, err := fn(p.Params)
	if err != nil {
		return nil, true, err
	}
	raw, err := json.Marshal(out)
	return raw, true, err
}

func (f *fakeExt) AssistantSaid(string, json.RawMessage) bool { return false }

func (f *fakeExt) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.served = append(f.served, r.URL.EscapedPath())
	f.mu.Unlock()
	writeErr(w, http.StatusNotFound, errors.New("no such route"))
}

// The names the fake answers the nodes' MCP tools by (the manager's AgentTools / CallTool, in its process).
const (
	fakeConnectorTools = "connector_tools" // {assistant} → {tools, assistant_tools}
	fakeConnectorTool  = "connector_tool"  // {name, arguments, assistant, machine_id, session_id} → {handled, result}
)

// AgentTools, CallTool, AssistantSummary and AssistantLark are the fake as the assistant's ExtensionTools.
func (f *fakeExt) AgentTools(_ context.Context, assistant bool) (agents, assistantOnly []proto.Tool) {
	f.mu.Lock()
	fn := f.calls[fakeConnectorTools]
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	raw, _ := json.Marshal(map[string]any{"assistant": assistant})
	out, err := fn(raw)
	if err != nil {
		return nil, nil
	}
	b, _ := json.Marshal(out)
	var res struct {
		Tools          []proto.Tool `json:"tools"`
		AssistantTools []proto.Tool `json:"assistant_tools"`
	}
	_ = json.Unmarshal(b, &res)
	return res.Tools, res.AssistantTools
}

func (f *fakeExt) CallTool(_ context.Context, machine, session, name string, args json.RawMessage, assistant bool) (proto.ToolResult, bool) {
	f.mu.Lock()
	fn := f.calls[fakeConnectorTool]
	f.mu.Unlock()
	if fn == nil {
		return proto.ToolResult{}, false
	}
	raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args, "assistant": assistant, "machine_id": machine, "session_id": session})
	out, err := fn(raw)
	if err != nil {
		return proto.TextResult(err.Error(), true), true
	}
	b, _ := json.Marshal(out)
	var res struct {
		Handled bool             `json:"handled"`
		Result  proto.ToolResult `json:"result"`
	}
	_ = json.Unmarshal(b, &res)
	return res.Result, res.Handled
}

func (f *fakeExt) AssistantSummary(context.Context, string, string) (string, bool) { return "", false }

func (f *fakeExt) ConnectorTool(string) bool { return false }

func (f *fakeExt) AssistantLark(context.Context) (proto.AssistantLark, error) {
	return proto.AssistantLark{}, nil
}

func (f *fakeExt) AnswerComputerElicit(context.Context, string, json.RawMessage) (map[string]any, error) {
	return map[string]any{"action": "cancel"}, nil
}

func (f *fakeExt) RecordNodeSent(context.Context, string, json.RawMessage) (any, error) {
	return struct{}{}, nil
}

func (f *fakeExt) MailAttachment(context.Context, proto.MailAttachmentRequest) (proto.MailAttachment, error) {
	return proto.MailAttachment{}, errors.New("mail is not available")
}
