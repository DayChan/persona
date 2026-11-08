package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestExtensionIdentityAssistantCapabilityAndRegisteredStorageOwner(t *testing.T) {
	h := newTestManager(t)
	_ = kbExtensionSpec(t, h, "current-models", extension.Permissions{Hub: []string{extension.ScopeModels, extension.ScopeKBWrite}})
	p := h.extensions.reg.GetExact("current-models")
	p.Record.StorageID = "previous-models"
	supported := false
	var calls []string
	var envelope json.RawMessage
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		calls = append(calls, method)
		if method == assistantwire.MethodExtensionIdentity {
			if supported {
				return json.RawMessage(`{"supported":true}`), nil
			}
			return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method}
		}
		envelope = append(json.RawMessage{}, params...)
		return json.RawMessage(`{}`), nil
	}
	params := json.RawMessage(`{"storage_id":"foreign-owner","messages":[{"role":"user","content":"fixture"}]}`)
	if _, err := h.extensions.extensionModelCall(context.Background(), p.Record.ID, params); err == nil {
		t.Fatal("old assistant silently received a renamed owner")
	}
	if len(calls) != 1 || calls[0] != assistantwire.MethodExtensionIdentity {
		t.Fatalf("destructive call before capability: %v", calls)
	}
	supported = true
	if _, err := h.extensions.extensionModelCall(context.Background(), p.Record.ID, params); err != nil {
		t.Fatal(err)
	}
	var env assistantwire.ExtensionComplete
	if err := json.Unmarshal(envelope, &env); err != nil || env.Extension != p.Record.ID || env.StorageID != "previous-models" || string(env.Params) != string(params) {
		t.Fatalf("identity envelope: %s %v", envelope, err)
	}
	if _, err := h.extensions.extensionModelCall(context.Background(), "previous-models", params); err == nil {
		t.Fatal("program authority resolved a storage/old identity")
	}
	if report := h.extensions.modelExtensions(); len(report) != 1 || report[0].ID != p.Record.ID || report[0].StorageID != "previous-models" {
		t.Fatalf("model report: %+v", report)
	}
}

// model.complete (extension_model_calls.go, #6102): only with the models scope itself, handed to the assistant with
// the extension's id; plugins.models lists the extensions that have a purpose.
func TestExtensionModelCallNeedsItsScopeAndIsAttributed(t *testing.T) {
	h := newTestManager(t)
	granted := kbExtensionSpec(t, h, "example-models", extension.Permissions{Hub: []string{extension.ScopeModels}})
	kbOnly := kbExtensionSpec(t, h, "example-kb", extension.Permissions{Hub: []string{extension.ScopeKBRead}})
	full := kbExtensionSpec(t, h, "example-full", extension.Permissions{HubAPI: true})
	var mu sync.Mutex
	var asked []string
	var got []json.RawMessage
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, method)
		got = append(got, params)
		return json.RawMessage(`{"text":"ok","model":"m","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2,"requests":1}}`), nil
	}
	params := `{"messages":[{"role":"user","content":"hi"}],"extension":"someone-else"}`
	for _, spec := range []struct {
		name string
		call func(context.Context, string, json.RawMessage) (any, error)
	}{{"kb only", kbOnly.Handle}, {"legacy full grant", full.Handle}} {
		_, err := spec.call(context.Background(), assistantwire.MethodModelComplete, json.RawMessage(params))
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), `"models"`) {
			t.Fatalf("%s: %v", spec.name, err)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("a denied call reached the assistant: %v", asked)
	}
	out, err := granted.Handle(context.Background(), assistantwire.MethodModelComplete, json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(out); !strings.Contains(string(raw), `"text":"ok"`) {
		t.Fatalf("%s", raw)
	}
	var env assistantwire.ExtensionComplete
	if len(asked) != 1 || asked[0] != assistantwire.MethodModelsExtensionComplete || json.Unmarshal(got[0], &env) != nil ||
		env.Extension != "example-models" || string(env.Params) != params {
		t.Fatalf("the assistant got %v %s", asked, got)
	}
	// The internal method is not the extensions'.
	_, err = granted.Handle(context.Background(), assistantwire.MethodModelsExtensionComplete, json.RawMessage(`{}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != extension.CodeUnknownMethod {
		t.Fatalf("internal method exposed: %v", err)
	}

	raw, err := pluginsCall(t, h, assistantwire.PluginsModels, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"extensions":[{"id":"example-models","name":"Example"}]}` {
		t.Fatalf("plugins.models: %s", raw)
	}
	h.extensions.reg.Get("example-models").Record.Enabled = false
	if raw, _ := pluginsCall(t, h, assistantwire.PluginsModels, map[string]any{}); string(raw) != `{"extensions":[]}` {
		t.Fatalf("a switched-off extension keeps its purpose: %s", raw)
	}
	if _, err := granted.Handle(context.Background(), assistantwire.MethodModelComplete, json.RawMessage(params)); !errors.As(err, &re) || re.Code != extension.CodePermissionDenied {
		t.Fatalf("switched off: %v", err)
	}
}
