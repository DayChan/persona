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

// assistant.conversations / assistant.messages (extension_assistant_read.go, #1288): only with assistant.read as
// granted (or the legacy full grant, as for kb.read), forwarded to the manager unchanged.
func TestExtensionAssistantReadNeedsItsGrant(t *testing.T) {
	h := newTestManager(t)
	granted := kbExtensionSpec(t, h, "example-reader", extension.Permissions{Hub: []string{extension.ScopeAssistantRead}})
	kbOnly := kbExtensionSpec(t, h, "example-kb", extension.Permissions{Hub: []string{extension.ScopeKBRead}})
	askOnly := kbExtensionSpec(t, h, "example-ask", extension.Permissions{Hub: []string{extension.ScopeAssistantAsk}})
	full := kbExtensionSpec(t, h, "example-full", extension.Permissions{HubAPI: true})
	var mu sync.Mutex
	var asked []string
	var got []string
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, method)
		got = append(got, string(params))
		return json.RawMessage(`{"messages":[]}`), nil
	}
	params := json.RawMessage(`{"id":"c1","limit":5}`)
	for name, spec := range map[string]func(context.Context, string, json.RawMessage) (any, error){"kb.read": kbOnly.Handle, "assistant.ask": askOnly.Handle} {
		for _, method := range []string{assistantwire.MethodAssistantConversations, assistantwire.MethodAssistantMessages} {
			_, err := spec(context.Background(), method, params)
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), `"assistant.read"`) {
				t.Fatalf("%s %s: %v", name, method, err)
			}
		}
	}
	if len(asked) != 0 {
		t.Fatalf("a denied call reached the manager: %v", asked)
	}
	for _, spec := range []func(context.Context, string, json.RawMessage) (any, error){granted.Handle, full.Handle} {
		if _, err := spec(context.Background(), assistantwire.MethodAssistantMessages, params); err != nil {
			t.Fatal(err)
		}
	}
	if len(asked) != 2 || asked[0] != assistantwire.MethodAssistantMessages || got[0] != string(params) {
		t.Fatalf("the manager got %v %v", asked, got)
	}
	h.extensions.reg.Get("example-reader").Record.Enabled = false
	if _, err := granted.Handle(context.Background(), assistantwire.MethodAssistantConversations, params); err == nil {
		t.Fatal("a switched-off extension still reads")
	}
}
