package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/github"
)

// auditRow is one plugin_audit row as auditingStore keeps it.
type auditRow struct {
	ID, Action, Actor string
	Detail            map[string]any
}

// auditingStore is keepStore that also keeps the audit (plugin_audit), or refuses it when fail is set.
type auditingStore struct {
	*keepStore
	mu   sync.Mutex
	rows []auditRow
	fail bool
}

func newAuditingStore() *auditingStore { return &auditingStore{keepStore: newKeepStore()} }

func (s *auditingStore) AddExtensionAudit(_ context.Context, id, action, actor string, detail json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("the database is away")
	}
	var d map[string]any
	if err := json.Unmarshal(detail, &d); err != nil {
		return err
	}
	s.rows = append(s.rows, auditRow{ID: id, Action: action, Actor: actor, Detail: d})
	return nil
}

// take gives the rows recorded since the last take.
func (s *auditingStore) take() []auditRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.rows
	s.rows = nil
	return out
}

// actions is rows' actions in order.
func actions(rows []auditRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Action
	}
	return out
}

func sameActions(rows []auditRow, want ...string) bool {
	got := actions(rows)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Signing in and out of a connector is recorded like an extension's own sign-in: the start, the account once it is
// done, and the sign-out, with who did it; nothing the user typed (phone number, code, password) or the session
// itself ends up in the record, a mistyped code included (#5281).
func TestConnectorSignInIsAudited(t *testing.T) {
	h := newTestManager(t)
	st := newAuditingStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	if err := h.addConnector(&signInConnector{secrets: h.secretsFor("example")}); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	for _, body := range []string{
		`{"step":""}`,
		`{"step":"phone","values":{"phone":"+1 555 0100"}}`,
		`{"step":"code","values":{"code":"00000"}}`,
		`{"step":"code","values":{"code":"12345"}}`,
		`{"step":"password","values":{"password":"example-password"}}`,
	} {
		if code, out := call(t, mux, "POST", "/api/v1/connectors/example/login", h.cfg.Token, body); code != 200 {
			t.Fatalf("%s: %d %v", body, code, out)
		}
	}
	if code, _ := call(t, mux, "POST", "/api/v1/connectors/example/logout", h.cfg.Token, ""); code != 200 {
		t.Fatalf("logout %d", code)
	}
	rows := st.take()
	if !sameActions(rows, auditConnectorLoginStart, auditConnectorSignedIn, auditConnectorLogout) {
		t.Fatalf("recorded %v", actions(rows))
	}
	for _, r := range rows {
		if r.ID != "example" || r.Actor != "hub token" {
			t.Fatalf("%s: id %q actor %q", r.Action, r.ID, r.Actor)
		}
	}
	if rows[1].Detail["account"] != "示例用户" {
		t.Fatalf("the account is not in the record: %v", rows[1].Detail)
	}
	raw, _ := json.Marshal(rows)
	for _, typed := range []string{"555", "00000", "12345", "example-password", "session"} {
		if strings.Contains(string(raw), typed) {
			t.Fatalf("%q is in the audit: %s", typed, raw)
		}
	}

	// A step that fails (the program is away) records nothing; nor does an unknown connector.
	if code, _ := call(t, mux, "POST", "/api/v1/connectors/nobody/login", h.cfg.Token, `{}`); code != 404 {
		t.Fatalf("unknown connector: %d", code)
	}
	if rows := st.take(); len(rows) != 0 {
		t.Fatalf("recorded %v", actions(rows))
	}
}

// The switch and the feeds are recorded when they change, not when the app sends what they already are; the
// connector's own settings are recorded when they change, without their values.
func TestConnectorSwitchesAreAudited(t *testing.T) {
	h := newTestManager(t)
	st := newAuditingStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	_ = h.addConnector(&optionsConnector{})
	mux := h.routes()
	put := func(body string) {
		t.Helper()
		if code, out := call(t, mux, "PUT", "/api/v1/connectors/example/settings", h.cfg.Token, body); code != 200 {
			t.Fatalf("%s: %d %v", body, code, out)
		}
	}
	put(`{"enabled":true,"archive":true,"inbox":true}`) // what they are already
	if rows := st.take(); len(rows) != 0 {
		t.Fatalf("nothing changed, recorded %v", actions(rows))
	}
	put(`{"enabled":false}`)
	put(`{"archive":false}`)
	put(`{"enabled":false,"archive":false}`) // again: no change
	put(`{"options":{"muted":["example-org/private-notes"]}}`)
	put(`{"options":{"muted":["example-org/private-notes"]}}`) // the same settings again
	rows := st.take()
	if !sameActions(rows, auditConnectorEnabled, auditConnectorFeed, auditConnectorOptions) {
		t.Fatalf("recorded %v", actions(rows))
	}
	if rows[0].Detail["enabled"] != false || rows[1].Detail["feed"] != string(connector.FeedArchive) || rows[1].Detail["on"] != false {
		t.Fatalf("details %v %v", rows[0].Detail, rows[1].Detail)
	}
	if len(rows[2].Detail) != 0 {
		t.Fatalf("the settings' values are in the audit: %v", rows[2].Detail)
	}
}

// Deleting a connector's data or its knowledge base content is recorded with what went (and switching it off, which
// comes first); a deletion that only partly happened says so.
func TestConnectorDeletionsAreAudited(t *testing.T) {
	h := newTestManager(t)
	st := newAuditingStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	_ = h.addConnector(&feedConnector{})
	mux := h.routes()
	forgettingAssistant(h)

	if code, out := call(t, mux, "DELETE", "/api/v1/connectors/example/archive", h.cfg.Token, ""); code != 200 {
		t.Fatalf("archive: %d %v", code, out)
	}
	rows := st.take()
	if !sameActions(rows, auditConnectorFeed, auditConnectorArchiveDeleted) || rows[1].Detail["chats"] != 1.0 ||
		rows[1].Detail["people"] != 1.0 || rows[1].Detail["posts"] != 2.0 || rows[1].Detail["items"] != 3.0 || rows[1].Detail["incomplete"] != nil {
		t.Fatalf("archive recorded %v %v", actions(rows), rows)
	}

	if code, out := call(t, mux, "DELETE", "/api/v1/connectors/example/data", h.cfg.Token, ""); code != 200 {
		t.Fatalf("data: %d %v", code, out)
	}
	rows = st.take()
	if !sameActions(rows, auditConnectorEnabled, auditConnectorDataDeleted) || rows[1].Detail["inbox"] != 1.0 ||
		rows[1].Detail["posts"] != 2.0 || rows[1].Detail["items"] != 3.0 {
		t.Fatalf("data recorded %v %v", actions(rows), rows)
	}

	// The assistant away: nothing went, and the record says the deletion did not finish.
	h.opts.Assistant = nil
	if code, _ := call(t, mux, "DELETE", "/api/v1/connectors/example/data", h.cfg.Token, ""); code != 503 {
		t.Fatalf("with the assistant away: %d", code)
	}
	rows = st.take()
	if !sameActions(rows, auditConnectorDataDeleted) || rows[0].Detail["incomplete"] != true {
		t.Fatalf("an unfinished deletion recorded %v %v", actions(rows), rows)
	}
}

// Choosing where GitHub's token comes from is recorded with the choice.
func TestGitHubSourceIsAudited(t *testing.T) {
	h := newTestManager(t)
	st := newAuditingStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	gh := github.New(github.Config{Auth: github.AuthConfig{FromMachine: "mac"}}, "Example Mac", func(context.Context, string) (string, error) {
		return "", github.ErrOffline
	})
	gh.UseSecrets(h.secretsFor("github"))
	if err := h.addConnector(gh); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	if code, _ := call(t, mux, "PUT", "/api/v1/connectors/github/auth", h.cfg.Token, `{"source":"machine","machine":"nope"}`); code != 400 {
		t.Fatalf("an unknown machine: %d", code)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/connectors/github/auth", h.cfg.Token, `{"source":"device"}`); code != 200 {
		t.Fatalf("device: %d", code)
	}
	rows := st.take()
	if !sameActions(rows, auditConnectorAuthSource) || rows[0].ID != "github" || rows[0].Detail["source"] != "device" ||
		rows[0].Detail["machine"] != nil {
		t.Fatalf("recorded %v", rows)
	}
}

// The record names the device that asked; a record that cannot be written does not undo or refuse the action.
func TestConnectorAuditActorAndFailure(t *testing.T) {
	h := newTestManager(t)
	st := newAuditingStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	_ = h.addConnector(&exampleConnector{})
	ctx := withPrincipal(context.Background(), principal{ID: "dev_phone", Role: roleFull})
	if err := h.setConnectorEnabled(ctx, "example", false); err != nil {
		t.Fatal(err)
	}
	if rows := st.take(); len(rows) != 1 || rows[0].Actor != "device:dev_phone" {
		t.Fatalf("recorded %v", rows)
	}

	st.fail = true
	if err := h.setConnectorEnabled(ctx, "example", true); err != nil || !h.connectors.Enabled("example") {
		t.Fatalf("the switch failed with the audit: %v", err)
	}

	// A store without an audit (no database) records nothing and still works.
	h.store = newKeepStore()
	if err := h.setConnectorEnabled(ctx, "example", false); err != nil || h.connectors.Enabled("example") {
		t.Fatalf("without an audit: %v", err)
	}
}

// optionsConnector is feedConnector (an inbox and an archive) with settings of its own (connector.Options).
type optionsConnector struct {
	feedConnector
	opts json.RawMessage
}

func (*optionsConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox, connector.CapArchive, connector.CapOptions},
		Kinds: []string{"dm"}}
}

func (c *optionsConnector) Options() json.RawMessage {
	if c.opts == nil {
		return json.RawMessage(`{}`)
	}
	return c.opts
}

func (c *optionsConnector) SetOptions(raw json.RawMessage) error {
	c.opts = append(json.RawMessage(nil), raw...)
	return nil
}
