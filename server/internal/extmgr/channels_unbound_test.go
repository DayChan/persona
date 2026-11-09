package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
)

func newConnectorChannels(t *testing.T, connectorID string) (*Manager, *fakeAssistant) {
	t.Helper()
	h := newTestManager(t)
	h.store = newKeepStore()
	h.connectors = connector.NewRegistry(nil)
	raw, err := json.Marshal([]channel.Item{
		{ID: "first_chat", Name: "First Chat", Kind: channel.KindIM, Connector: connectorID},
		{ID: "second_chat", Name: "Second Chat", Kind: channel.KindIM, Connector: connectorID},
		{ID: "other_chat", Name: "Other Chat", Kind: channel.KindIM, Connector: "other"},
		{ID: "independent_chat", Name: "Independent Chat", Kind: channel.KindIM},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: "example-channels", Name: "Example Channels", Version: "1.0.0",
		Engines:     extension.Engines{ExtensionProtocol: extension.Protocol},
		Runtime:     &extension.Runtime{Command: extension.Command{Path: "example-channels"}},
		Contributes: map[string]json.RawMessage{"channel": raw}}
	reg := extension.NewRegistry(t.TempDir(), nil, nil, 0)
	if err := reg.AddBuiltin(m, "/opt/example", true); err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.extensions = newExtensions(t.TempDir(), nil, h.logf)
	h.extensions.reg = reg
	return h, withAssistant(h)
}

func unboundChannels(t *testing.T, a *fakeAssistant) []string {
	t.Helper()
	var ids []string
	for _, raw := range a.got(assistantwire.MethodChannelUnbound) {
		var p assistantwire.ChannelUnbound
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.Channel)
	}
	slices.Sort(ids)
	return ids
}

type failingChannelLogout struct{ signInConnector }

func (*failingChannelLogout) Logout(context.Context) error { return errors.New("logout failed") }

func TestConnectorLogoutClearsOnlyItsChannels(t *testing.T) {
	h, a := newConnectorChannels(t, "example")
	c := &signInConnector{secrets: h.secretsFor("example")}
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	if code, out := call(t, h.routes(), "POST", "/api/v1/connectors/example/logout", h.cfg.Token, ""); code != http.StatusOK {
		t.Fatalf("logout: %d %v", code, out)
	}
	if !c.out {
		t.Fatal("the connector was not signed out")
	}
	if got := unboundChannels(t, a); !slices.Equal(got, []string{"first_chat", "second_chat"}) {
		t.Fatalf("cleared channels = %v", got)
	}
}

func TestConnectorLogoutFailurePreservesChannels(t *testing.T) {
	h, a := newConnectorChannels(t, "example")
	if err := h.addConnector(&failingChannelLogout{}); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, h.routes(), "POST", "/api/v1/connectors/example/logout", h.cfg.Token, ""); code != http.StatusBadGateway {
		t.Fatalf("logout failure: %d", code)
	}
	if got := unboundChannels(t, a); len(got) != 0 {
		t.Fatalf("failed logout cleared channels: %v", got)
	}
}

func TestConnectorLogoutReportsChannelCleanupFailure(t *testing.T) {
	h, a := newConnectorChannels(t, "example")
	if err := h.addConnector(&signInConnector{secrets: h.secretsFor("example")}); err != nil {
		t.Fatal(err)
	}
	a.on(assistantwire.MethodChannelUnbound, func(json.RawMessage) (any, error) {
		return nil, errors.New("channel state unavailable")
	})
	if code, _ := call(t, h.routes(), "POST", "/api/v1/connectors/example/logout", h.cfg.Token, ""); code != http.StatusBadGateway {
		t.Fatalf("cleanup failure: %d", code)
	}
	if got := unboundChannels(t, a); !slices.Equal(got, []string{"first_chat", "second_chat"}) {
		t.Fatalf("cleanup must attempt every affected channel: %v", got)
	}
}

func TestConnectorWithoutChannelsDoesNotNeedAssistantToLogout(t *testing.T) {
	h, a := newConnectorChannels(t, "other_connector")
	h.opts.Assistant = nil
	c := &signInConnector{secrets: h.secretsFor("example")}
	if err := h.connectorLogout(t.Context(), "example", c); err != nil {
		t.Fatal(err)
	}
	if !c.out || len(unboundChannels(t, a)) != 0 {
		t.Fatal("a connector without channels must only sign out")
	}
}

func TestWeChatRevocationClearsChannelsBeforeRebinding(t *testing.T) {
	h, a := newConnectorChannels(t, "wechat")
	sec := h.secretsFor("wechat")
	bound := []byte(`{"creds":{"user_id":"example-owner","token":"example-token"}}`)
	for _, raw := range [][]byte{bound, []byte(`{"creds":{"user_id":"example-owner","token":"example-token"},"cursor":"next","context":"next"}`)} {
		if err := sec.Save(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
	}
	if got := unboundChannels(t, a); len(got) != 0 {
		t.Fatalf("ordinary secret updates cleared channels: %v", got)
	}
	// The plugin writes this state on iLink -14, without going through connectorLogout.
	if err := sec.Save(t.Context(), []byte(`{"old":["example-token"]}`)); err != nil {
		t.Fatal(err)
	}
	if got := unboundChannels(t, a); !slices.Equal(got, []string{"first_chat", "second_chat"}) {
		t.Fatalf("revocation did not synchronously clear channels: %v", got)
	}
	// Binding the same account immediately must not depend on observing an unbound status in a poll.
	if err := sec.Save(t.Context(), bound); err != nil {
		t.Fatal(err)
	}
	if got := unboundChannels(t, a); len(got) != 2 {
		t.Fatalf("rebinding unexpectedly cleared channels again: %v", got)
	}
	if err := sec.Delete(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := unboundChannels(t, a); !slices.Equal(got, []string{"first_chat", "first_chat", "second_chat", "second_chat"}) {
		t.Fatalf("deleting credentials did not clear channels: %v", got)
	}
}

func TestWeChatRevocationReportsChannelCleanupFailure(t *testing.T) {
	h, a := newConnectorChannels(t, "wechat")
	want := errors.New("channel state unavailable")
	a.on(assistantwire.MethodChannelUnbound, func(json.RawMessage) (any, error) { return nil, want })
	sec := h.secretsFor("wechat")
	if err := sec.Save(t.Context(), []byte(`{}`)); !errors.Is(err, want) {
		t.Fatalf("saving revoked credentials: %v", err)
	}
	if err := sec.Delete(t.Context()); !errors.Is(err, want) {
		t.Fatalf("deleting revoked credentials: %v", err)
	}
}

type channelLoginResult struct {
	signInConnector
	prompt connector.Prompt
	err    error
}

func (c *channelLoginResult) LoginStep(context.Context, string, map[string]string) (connector.Prompt, error) {
	return c.prompt, c.err
}

func TestChannelConnectorLoginClearsOnlyCompletedBindings(t *testing.T) {
	for _, tc := range []struct {
		name        string
		done        bool
		loginFail   bool
		cleanupFail bool
	}{
		{name: "completed", done: true},
		{name: "cleanup failure", done: true, cleanupFail: true},
		{name: "pending"},
		{name: "login failure", loginFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, a := newConnectorChannels(t, "example")
			c := &channelLoginResult{prompt: connector.Prompt{Done: tc.done, Account: "Example account"}}
			if tc.loginFail {
				c.err = errors.New("login failed")
			}
			if tc.cleanupFail {
				a.on(assistantwire.MethodChannelUnbound, func(json.RawMessage) (any, error) {
					return nil, errors.New("channel state unavailable")
				})
			}
			p, err := h.connectorLoginStep(t.Context(), "example", c, "scan", nil)
			if (err != nil) != (tc.loginFail || tc.cleanupFail) || p.Done != tc.done {
				t.Fatalf("login: done = %v, error = %v", p.Done, err)
			}
			got := unboundChannels(t, a)
			if tc.done {
				if !slices.Equal(got, []string{"first_chat", "second_chat"}) {
					t.Fatalf("completed binding did not clear previous questions: %v", got)
				}
			} else if len(got) != 0 {
				t.Fatalf("unfinished binding cleared questions: %v", got)
			}
		})
	}
}

func TestConnectorWithoutChannelsDoesNotNeedAssistantToLogin(t *testing.T) {
	h, a := newConnectorChannels(t, "other_connector")
	h.opts.Assistant = nil
	c := &channelLoginResult{prompt: connector.Prompt{Done: true}}
	if p, err := h.connectorLoginStep(t.Context(), "example", c, "scan", nil); err != nil || !p.Done {
		t.Fatalf("login without channels: done = %v, error = %v", p.Done, err)
	}
	if len(unboundChannels(t, a)) != 0 {
		t.Fatal("a connector without channels must only sign in")
	}
}
