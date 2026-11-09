// Package conformance holds a connector to the protocol (docs/protocol/connector-protocol.md): it runs the connector on the
// program's side of the protocol (connector/serve) over a pipe, calls every capability its manifest declares both
// through the wire and directly, and fails when the two differ — a field the wire loses, a type it changes. Built-in
// connectors are called directly in production; this is what keeps "in the hub is one implementation of the
// protocol" true. A third party's program can be held to the same with Program.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	wireschema "github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Check runs c through the protocol and directly and compares what each declared capability gives back. c is
// built and running as the test has it (its Run and Watch are not started again). It goes through both openings: the
// connector protocol's own (init, bare method names) and the plugin protocol's (initialize, connector.*).
func Check(t testing.TB, c connector.Connector) {
	t.Helper()
	if err := connector.Check(c); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	check(t, c, false)
	check(t, c, true)
}

func check(t testing.TB, c connector.Connector, inPlugin bool) {
	t.Helper()
	opening := "connector protocol"
	if inPlugin {
		opening = "plugin protocol"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	progR, hubW := io.Pipe()
	hubR, progW := io.Pipe()
	t.Cleanup(func() { hubW.Close(); progW.Close(); progR.Close(); hubR.Close() })
	go func() { _ = serve.Serve(ctx, progR, progW, c) }()
	hub := rpc.New("h", hubW)
	hub.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return serve.SecretData{}, nil }
	go func() { _ = hub.Serve(ctx, hubR) }()
	// wire is a method's name and params in this opening's words.
	wire := func(method string, params any) (string, any) {
		if !inPlugin {
			return method, params
		}
		p, err := extension.Targeted(params, c.ID())
		if err != nil {
			t.Fatal(err)
		}
		return serve.InPlugin(method), p
	}
	callRaw := func(method string, params any, out *json.RawMessage) error {
		m, p := wire(method, params)
		err := hub.Call(ctx, m, p, out)
		// What both sides send is the schema's (schema/extension/connector.schema.json): the plugin protocol's names.
		if inPlugin {
			if serr := paramsHold(m, p); serr != nil {
				t.Error(serr)
			}
			if serr := wireschema.ValidateResult(m, *out); err == nil && serr != nil {
				t.Errorf("%s: the answer does not hold to schema/extension: %v", m, serr)
			}
		}
		return err
	}
	call := func(method string, params any) json.RawMessage {
		t.Helper()
		var raw json.RawMessage
		if err := callRaw(method, params, &raw); err != nil {
			t.Fatalf("%s through the %s: %v", method, opening, err)
		}
		return raw
	}

	want := serve.ManifestOf(c)
	if !inPlugin {
		var man serve.Manifest
		if err := json.Unmarshal(call(serve.MInit, serve.InitParams{Protocol: serve.Protocol}), &man); err != nil {
			t.Fatal(err)
		}
		same(t, "manifest", want, man)
	} else {
		var res extension.InitializeResult
		init := extension.InitializeParams{Protocol: extension.Protocol, Extension: extension.ExtensionInfo{ID: "conformance", Version: "1.0.0"},
			Active: map[string][]string{"connectors": {c.ID()}}}
		if err := hub.Call(ctx, extension.MInitialize, init, &res); err != nil {
			t.Fatalf("initialize: %v", err)
		}
		var items []extension.ConnectorItem
		_ = json.Unmarshal(res.Contributes["connectors"], &items)
		if res.Protocol != extension.Protocol || res.ID != "conformance" || len(items) != 1 {
			t.Fatalf("initialize answered %+v", res)
		}
		same(t, "manifest", serve.ItemOf(want), items[0])
		var re *rpc.Error
		if err := hub.Call(ctx, serve.InPlugin(serve.MStatus), map[string]string{"contribution": "nosuchconnector"}, nil); !errors.As(err, &re) ||
			re.Code != extension.CodeNoContribution {
			t.Fatalf("a connector the program does not have: %v", err)
		}
		if err := hub.Call(ctx, extension.MPing, struct{}{}, nil); err != nil {
			t.Fatalf("ping: %v", err)
		}
	}

	m := c.Manifest()
	if m.Has(connector.CapStatus) {
		var got connector.Status
		decode(t, call(serve.MStatus, struct{}{}), &got)
		same(t, "status", connector.StatusOf(ctx, c), got)
	}
	if m.Has(connector.CapCatalog) {
		var got connector.CatalogResult
		decode(t, call(serve.MCatalog, struct{}{}), &got)
		want, err := connector.ReadCatalog(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		same(t, "catalog", connector.CatalogResult{Sources: want}, got)
	}
	if t2, ok := connector.As[connector.ReadTools](c); ok {
		var got serve.ToolsResult
		decode(t, call(serve.MTools, struct{}{}), &got)
		same(t, "tools", serve.ToolsResult{Tools: t2.Tools()}, got)
	}
	if a, ok := connector.As[connector.Actions](c); ok {
		var got serve.ToolsResult
		decode(t, call(serve.MActions, struct{}{}), &got)
		same(t, "actions", serve.ToolsResult{Tools: a.Actions()}, got)
		// Describe only: actions.do writes, and only ever after the hub's gate.
		for _, act := range a.Actions() {
			var through serve.DescribeResult
			var throughErr string
			var raw json.RawMessage
			if err := callRaw(serve.MDescribe, serve.CallParams{Name: act.Name, Args: json.RawMessage(`{}`)}, &raw); err != nil {
				throughErr = err.Error()
			} else {
				decode(t, raw, &through)
			}
			action, summary, err := a.Describe(ctx, act.Name, json.RawMessage(`{}`))
			directErr := ""
			if err != nil {
				directErr, action, summary = err.Error(), connector.Action{}, ""
			}
			same(t, "describe "+act.Name, serve.DescribeResult{Action: action, Summary: summary}, through)
			if directErr != throughErr {
				t.Errorf("describe %s: %q directly, %q through the protocol", act.Name, directErr, throughErr)
			}
		}
	}
	var threads []string // the inbox's, for the thread's page
	if in, ok := connector.As[connector.Inbox](c); ok {
		var got serve.CollectResult
		decode(t, call(serve.MCollect, struct{}{}), &got)
		things, err := in.Collect(ctx)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		same(t, "inbox", serve.CollectResult{Things: things}, got)
		for _, th := range things {
			threads = append(threads, th.Thread)
		}
	}
	if th, ok := connector.As[connector.Threads](c); ok && len(threads) > 0 {
		var got connector.ThreadPage
		decode(t, call(serve.MThread, serve.ThreadParams{Thread: threads[0], Limit: 20}), &got)
		page, err := th.Thread(ctx, threads[0], "", 20)
		if err != nil {
			t.Fatalf("thread: %v", err)
		}
		same(t, "thread", page, got)
		var refs []string
		for _, m := range page.Messages {
			refs = append(refs, m.Sender.Avatar)
			for _, md := range m.Media {
				refs = append(refs, md.Ref, md.Thumb)
			}
		}
		for _, ref := range refs {
			if ref == "" {
				continue
			}
			var got connector.Media
			decode(t, call(serve.MMedia, serve.MediaParams{Ref: ref}), &got)
			m, err := th.Media(ctx, ref)
			if err != nil {
				t.Fatalf("media %s: %v", ref, err)
			}
			same(t, "media", m, got)
			break // one is enough: the wire is the same for all
		}
	}
	if a, ok := connector.As[connector.Archive](c); ok {
		var got serve.ChangesResult
		decode(t, call(serve.MChanges, serve.ChangesParams{Limit: 50}), &got)
		b, next, more, err := a.Changes(ctx, "", 50)
		if err != nil {
			t.Fatalf("changes: %v", err)
		}
		same(t, "changes", serve.ChangesResult{Batches: b, Next: next, More: more}, got)
	}
	if h, ok := connector.As[connector.History](c); ok {
		var got serve.HistoryChatsResult
		decode(t, call(serve.MHistoryChats, struct{}{}), &got)
		chats, err := h.HistoryChats(ctx)
		if err != nil {
			t.Fatalf("history chats: %v", err)
		}
		if chats == nil {
			chats = []connectorwire.HistoryChat{}
		}
		same(t, "history chats", serve.HistoryChatsResult{Chats: chats}, got)
		if len(chats) > 0 {
			q := serve.HistoryParams{Chat: chats[0].ID, Limit: 20}
			var through connectorwire.HistoryPage
			decode(t, call(serve.MHistory, q), &through)
			page, err := h.History(ctx, q)
			if err != nil {
				t.Fatalf("history %s: %v", q.Chat, err)
			}
			same(t, "history", page, through)
			if page.Older != "" {
				q.Before = page.Older
				through = connectorwire.HistoryPage{}
				decode(t, call(serve.MHistory, q), &through)
				if page, err = h.History(ctx, q); err != nil {
					t.Fatalf("history %s before %s: %v", q.Chat, q.Before, err)
				}
				same(t, "older history", page, through)
			}
		}
	}
	if a, ok := connector.As[connector.HistoryAnchors](c); ok {
		q := serve.HistoryAnchorsParams{Limit: 20}
		var got connectorwire.HistoryAnchors
		decode(t, call(serve.MHistoryAnchors, q), &got)
		anchors, err := a.HistoryAnchors(ctx, q)
		if err != nil {
			t.Fatalf("history anchors: %v", err)
		}
		same(t, "history anchors", anchors, got)
	}
}

func decode(t testing.TB, raw json.RawMessage, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
}

// same fails unless direct and through (decoded from the wire) say the same thing, as JSON.
func same(t testing.TB, what string, direct, through any) {
	t.Helper()
	a, _ := json.Marshal(direct)
	b, _ := json.Marshal(through)
	if string(a) != string(b) {
		t.Errorf("%s differs through the wire:\ndirect  %s\nthrough %s", what, a, b)
	}
}
