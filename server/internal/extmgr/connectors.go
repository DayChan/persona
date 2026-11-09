package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/github"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Connectors: each source of the user's information plugs in as a connector, and the hub's pipes pick up what it
// implements — the assistant's tools (writes behind the gate), Settings › Connectors' status, realtime kicks
// (internal/connector, docs/plans/plan-connectors.md).

// addConnector registers c, refusing one that offers a tool the hub has itself ("kb_search", "phone_…"): calls go
// to the connector first. A connector may share a prefix with the hub's tools ("lark") as long as it does not offer
// them; the hub's own then move to it (docs/plans/plan-connectors.md M4).
func (h *Manager) addConnector(c connector.Connector) error {
	for _, t := range connector.ToolNames(c) {
		for _, b := range h.builtinTools() {
			if t == b || strings.HasSuffix(b, "_") && strings.HasPrefix(t, b) {
				return fmt.Errorf("connector %q: %s would shadow the hub's own", c.ID(), t)
			}
		}
	}
	return h.connectors.Add(c)
}

// builtinTools are the names of every tool the hub offers besides connectors', whether or not it is set up to offer
// them now (the phone's by their prefix: they come and go with the app), the assistant's as it last listed them (none
// while it has not connected yet: connectors register as the hub starts, and do not wait for it).
func (h *Manager) builtinTools() []string {
	names := []string{"phone_"}
	for _, t := range h.assistantToolsNow() {
		names = append(names, t.Name)
	}
	return names
}

// connectorChanged is a connector's realtime event: collect the inbox or archive soon.
func (h *Manager) connectorChanged(id string, scope connector.Scope) {
	switch scope {
	case connector.ScopeInbox:
		h.kickInbox()
	case connector.ScopeChats, connector.ScopeItems:
		h.kbKick(id, scope)
	}
}

// callConnector runs a connector's tool for an agent or assistant conversation (by: none when the user acts in a
// connector's thread); a write the gate leaves to the user is asked on the phone like mail_send, in that conversation.
func (h *Manager) callConnector(ctx context.Context, by asker, name string, args json.RawMessage) proto.ToolResult {
	if inHealth(h.askerCwd(by)) { // a health conversation's writes: on the phone each time (assistant.go inHealth)
		ctx = connector.ConfirmEach(ctx)
	}
	// The ledger of what went out (sent_ledger.go) is told of the write with this ctx: who asked, and the card the user answered.
	trace := &sentTrace{by: by}
	ctx = context.WithValue(ctx, sentTraceKey{}, trace)
	return h.connectors.Call(ctx, name, args, func(ctx context.Context, summary string, input json.RawMessage) bool {
		r, id := h.approveCard(ctx, by, name, summary, input)
		trace.card, trace.answeredBy = id, r.AnsweredBy
		return r.Allow != nil && *r.Allow
	})
}

func (h *Manager) connectorRoutes(api *http.ServeMux) {
	h.githubAuthRoutes(api)
	h.connectorThreadRoutes(api)
	h.sentRoutes(api)
	h.sentRecallRoutes(api)
	h.chatRuleRoutes(api)
	api.HandleFunc("GET /api/v1/connectors", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"connectors": h.listedConnectors(r.Context(), r.URL.Query().Get("all") == "1")})
	})
	// Signing in, a step at a time (connector.Login): the app shows each Prompt's fields and sends them back. The
	// values (a phone number, a code, a password) go to the connector only: never into a log or a response.
	api.HandleFunc("POST /api/v1/connectors/{id}/login", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		l, ok := h.connectorLogin(w, id)
		if !ok {
			return
		}
		var body struct {
			Step   string            `json:"step"`
			Values map[string]string `json:"values"`
		}
		if err := decodeJSONBody(w, r, &body, 16<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		p, err := h.connectorLoginStep(r.Context(), id, l, body.Step, body.Values)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		if err := withQR(&p); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, p)
	})
	api.HandleFunc("POST /api/v1/connectors/{id}/logout", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		l, ok := h.connectorLogin(w, id)
		if !ok {
			return
		}
		if err := h.connectorLogout(r.Context(), id, l); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// Settings › Connectors' switch: off, the connector stops and brings nothing in (Registry.SetEnabled); kept in
	// settings.
	api.HandleFunc("PUT /api/v1/connectors/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if h.connectors.Get(id) == nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no connector %q", id))
			return
		}
		var body struct {
			Enabled *bool           `json:"enabled"`
			Options json.RawMessage `json:"options"` // the connector's own settings (connector.Options)
			Archive *bool           `json:"archive"` // "Add to knowledge base"
			Inbox   *bool           `json:"inbox"`   // "Add to Inbox"
		}
		if err := decodeJSONBody(w, r, &body, 64<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if len(body.Options) > 0 && string(body.Options) != "null" {
			before := h.optionsOf(id)
			if err := h.setConnectorOptions(r.Context(), id, body.Options); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			if !bytes.Equal(before, h.optionsOf(id)) {
				h.auditConnector(r.Context(), id, auditConnectorOptions, nil)
			}
		}
		for f, on := range map[connector.Feed]*bool{connector.FeedArchive: body.Archive, connector.FeedInbox: body.Inbox} {
			if on == nil {
				continue
			}
			if err := h.setConnectorFeed(r.Context(), id, f, *on); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		if body.Enabled != nil {
			if err := h.setConnectorEnabled(r.Context(), id, *body.Enabled); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		for _, info := range h.listedConnectors(r.Context(), true) {
			if info.ID == id {
				writeJSON(w, info)
				return
			}
		}
	})
	// "Delete saved data" for any connector: switched off first (and kept off: on, the next sync would bring it back),
	// then what the hub kept of it goes (forgetSource, as for iMessage). Signing out is apart (/logout).
	api.HandleFunc("DELETE /api/v1/connectors/{id}/data", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if h.connectors.Get(id) == nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no connector %q", id))
			return
		}
		if err := h.setConnectorEnabled(r.Context(), id, false); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		res, err := h.forgetSource(r.Context(), id)
		if err != nil {
			h.logf("connector %s: delete: %v", id, err)
			writeForgetErr(w, err)
			return
		}
		h.logf("connector %s: deleted %d chat(s), %d person(s), %d post(s), %d knowledge item(s), %d inbox item(s)", id, res.Chats, res.People, res.Posts, res.Items, res.Inbox)
		writeJSON(w, res)
	})
	// "Delete saved knowledge base content": "Add to knowledge base" turned off first (and kept off: on, the next round
	// would bring it back), then its knowledge-base content goes. The connector keeps running: the inbox
	// and the tools are untouched.
	api.HandleFunc("DELETE /api/v1/connectors/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if h.connectors.Get(id) == nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no connector %q", id))
			return
		}
		if err := h.setConnectorFeed(r.Context(), id, connector.FeedArchive, false); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		var res sourceForgotten
		var err error
		res.KBForgotten, err = h.forgetArchive(r.Context(), id)
		h.auditConnector(r.Context(), id, auditConnectorArchiveDeleted, forgotten(map[string]any{"chats": res.Chats, "people": res.People, "posts": res.Posts, "items": res.Items}, err))
		if err != nil {
			h.logf("connector %s: delete the archive: %v", id, err)
			writeForgetErr(w, err)
			return
		}
		h.logf("connector %s: deleted %d chat(s), %d person(s), %d post(s), %d item(s) from the knowledge base", id, res.Chats, res.People, res.Posts, res.Items)
		writeJSON(w, res)
	})
	h.imessageRoutes(api)
}

// sourceForgotten is what deleting a source's data removed (DELETE /api/v1/connectors/{id}/data,
// DELETE /api/v1/imessage/data).
type sourceForgotten struct {
	assistantwire.KBForgotten
	Inbox int `json:"inbox"`
}

// forgetSource removes what the hub kept of a source (a connector's id): its knowledge-base content (and
// its index rows, project associations and cursors), and has the manager remove the inbox's items. The caller
// stops the source first. The inbox's items go whatever the knowledge base answers, and either part fails while the
// assistant (the manager) is away: asked again, the deletion finishes what is left.
func (h *Manager) forgetSource(ctx context.Context, source string) (sourceForgotten, error) {
	var out sourceForgotten
	var kerr, ierr error
	out.KBForgotten, kerr = h.forgetArchive(ctx, source)
	out.Inbox, ierr = h.forgetInbox(ctx, source)
	err := errors.Join(kerr, ierr)
	h.auditConnector(ctx, source, auditConnectorDataDeleted, forgotten(map[string]any{"chats": out.Chats, "people": out.People, "posts": out.Posts, "items": out.Items, "inbox": out.Inbox}, err))
	return out, err
}

// forgetArchive removes a connector's knowledge-base content, including its declared archive sources.
func (h *Manager) forgetArchive(ctx context.Context, source string) (assistantwire.KBForgotten, error) {
	if h.kbOff() {
		return assistantwire.KBForgotten{}, nil
	}
	// A big source takes a while (every chat's directory, the projects' timelines): longer than a call's default, and
	// not cut short when the app gives up waiting.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), kbForgetWait)
	defer cancel()
	p := assistantwire.KBForget{Source: source}
	if c := h.connectorByID(source); c != nil {
		p.ArchiveSources = h.forgetArchiveSources(c)
	}
	var f assistantwire.KBForgotten
	if err := h.kbAsk(ctx, assistantwire.MethodKBForget, p, &f); err != nil {
		return assistantwire.KBForgotten{}, err
	}
	return f, nil
}

// kbForgetWait is how long deleting a source's data waits for the knowledge base.
const kbForgetWait = 10 * time.Minute

// writeForgetErr answers a deletion that failed: 503 assistant_offline while the knowledge base (the assistant's)
// is not there (its part was not deleted: the user tries again), else 500.
func writeForgetErr(w http.ResponseWriter, err error) {
	var re *rpc.Error
	switch {
	case errors.As(err, &re) && re.Code == assistantwire.CodeKBUnavailable: // connected, its knowledge base not open yet
		writeErrCode(w, http.StatusServiceUnavailable, re.Code, errors.New(re.Message))
	case kbOffline(err):
		writeErrCode(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, errAssistantOffline)
	default:
		writeErr(w, http.StatusInternalServerError, err)
	}
}

// withQR draws a QR step's code for the app (whatever the connector sent as QR is replaced: the code is always URL's).
func withQR(p *connector.Prompt) error {
	p.QR = nil
	if p.Kind != connector.PromptQRCode {
		return nil
	}
	if p.URL == "" {
		return errors.New("a QR sign-in step without a URL")
	}
	rows, size, err := qrRows(p.URL)
	if err != nil {
		return err
	}
	p.QR = &connector.QRCode{Size: size, Rows: rows}
	return nil
}

// connectorLogin is connector id's sign-in, or writes why there is none.
func (h *Manager) connectorLogin(w http.ResponseWriter, id string) (connector.Login, bool) {
	c := h.connectors.Get(id)
	if c == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no connector %q", id))
		return nil, false
	}
	l, ok := connector.As[connector.Login](c)
	if !ok {
		writeErr(w, http.StatusConflict, fmt.Errorf("%s has no sign-in", c.Name()))
		return nil, false
	}
	return l, true
}

// connectorSettingsKey holds the user's switch per connector, their own settings and the feeds turned off:
// {"switch": {"telegram": true}, "options": {"github": {"ci": "mine"}}, "archive": {"telegram": false}}.
const connectorSettingsKey = "connectors"

type connectorSettings struct {
	Switch  map[string]bool            `json:"switch,omitempty"`
	Options map[string]json.RawMessage `json:"options,omitempty"`
	// Archive and Inbox are its feeds the user turned on or off (connector.SetFeed); one not here is on.
	Archive map[string]bool `json:"archive,omitempty"`
	Inbox   map[string]bool `json:"inbox,omitempty"`
}

// feeds is the settings' map for feed f.
func (cs *connectorSettings) feeds(f connector.Feed) *map[string]bool {
	if f == connector.FeedInbox {
		return &cs.Inbox
	}
	return &cs.Archive
}

// setConnectorOptions gives a connector the user's settings and keeps them for the next start; the inbox is
// collected again under them. GitHub's muted repositories go to the inbox's mutes (the manager's,
// inbox_github_mutes.go).
func (h *Manager) setConnectorOptions(ctx context.Context, id string, raw json.RawMessage) error {
	if id == "github" {
		var err error
		if raw, err = h.setGitHubMutes(ctx, raw); err != nil {
			return err
		}
	}
	return h.applyConnectorOptions(ctx, id, raw)
}

// applyConnectorOptions is setConnectorOptions without the mutes.
func (h *Manager) applyConnectorOptions(ctx context.Context, id string, raw json.RawMessage) error {
	o, ok := connector.As[connector.Options](h.connectors.Get(id))
	if !ok {
		return fmt.Errorf("%s has no settings", id)
	}
	if err := o.SetOptions(raw); err != nil {
		return err
	}
	if h.store != nil {
		var cs connectorSettings
		if _, err := h.store.Setting(ctx, connectorSettingsKey, &cs); err != nil {
			return err
		}
		if cs.Options == nil {
			cs.Options = map[string]json.RawMessage{}
		}
		cs.Options[id] = o.Options()
		if err := h.store.PutSetting(ctx, connectorSettingsKey, cs); err != nil {
			return err
		}
	}
	h.kickInbox()
	return nil
}

// setConnectorEnabled switches a connector and keeps the choice for the next start.
func (h *Manager) setConnectorEnabled(ctx context.Context, id string, on bool) error {
	h.kbPostsMu.Lock()
	was := h.connectors.Enabled(id)
	err := h.connectors.SetEnabled(id, on)
	h.kbPostsMu.Unlock()
	if err != nil {
		return err
	}
	if id == "wechat" {
		defer h.sendReport()
	}
	if was != on {
		h.auditConnector(ctx, id, auditConnectorEnabled, map[string]any{"enabled": on})
	}
	if h.store == nil {
		return nil
	}
	var cs connectorSettings
	if _, err := h.store.Setting(ctx, connectorSettingsKey, &cs); err != nil {
		return err
	}
	if cs.Switch == nil {
		cs.Switch = map[string]bool{}
	}
	cs.Switch[id] = on
	return h.store.PutSetting(ctx, connectorSettingsKey, cs)
}

// setConnectorFeed turns one of a connector's feeds on or off and keeps the choice for the next start; turned on, the
// inbox or the archive goes again soon.
func (h *Manager) setConnectorFeed(ctx context.Context, id string, f connector.Feed, on bool) error {
	h.kbPostsMu.Lock()
	was := h.connectors.Feeds(id, f)
	err := h.connectors.SetFeed(id, f, on)
	h.kbPostsMu.Unlock()
	if err != nil {
		return err
	}
	if was != on {
		h.auditConnector(ctx, id, auditConnectorFeed, map[string]any{"feed": f, "on": on})
	}
	if h.store != nil {
		var cs connectorSettings
		if _, err := h.store.Setting(ctx, connectorSettingsKey, &cs); err != nil {
			return err
		}
		m := cs.feeds(f)
		if *m == nil {
			*m = map[string]bool{}
		}
		(*m)[id] = on
		if err := h.store.PutSetting(ctx, connectorSettingsKey, cs); err != nil {
			return err
		}
	}
	if on && f == connector.FeedInbox {
		h.kickInbox()
	} else if on {
		h.kbKick(id, connector.ScopeChats)
	}
	return nil
}

// restoreConnectorSwitches applies the user's switches before the connectors start. One the user never switched is
// on, unless it was added to start off (addConnectorOff: a built-in plugin's waits for the user to turn it on).
func (h *Manager) restoreConnectorSwitches(ctx context.Context) {
	cs := h.connectorSettings(ctx)
	for _, c := range h.connectors.All() {
		h.restoreSwitchesOf(c, cs)
	}
}

// connectorSettings are the user's connector switches and settings.
func (h *Manager) connectorSettings(ctx context.Context) connectorSettings {
	var cs connectorSettings
	if h.store != nil {
		if _, err := h.store.Setting(ctx, connectorSettingsKey, &cs); err != nil {
			h.logf("connectors: settings: %v", err)
		}
	}
	return cs
}

// connectorOn says whether the user wants connector id on (their switch, or its default).
func (h *Manager) connectorOn(cs connectorSettings, id string) bool {
	on, chosen := cs.Switch[id]
	if !chosen {
		on = !h.connectorsOff[id]
	}
	return on
}

func (h *Manager) restoreSwitchesOf(c connector.Connector, cs connectorSettings) {
	if raw, ok := cs.Options[c.ID()]; ok {
		if o, ok := connector.As[connector.Options](c); ok {
			if err := o.SetOptions(raw); err != nil {
				h.logf("connectors: %s settings: %v", c.ID(), err)
			}
		}
	}
	for _, f := range []connector.Feed{connector.FeedArchive, connector.FeedInbox} {
		if on, ok := (*cs.feeds(f))[c.ID()]; ok && !on {
			if err := h.connectors.SetFeed(c.ID(), f, false); err != nil {
				h.logf("connectors: %s %s: %v", c.ID(), f, err)
			}
		}
	}
	if !h.connectorOn(cs, c.ID()) {
		_ = h.connectors.SetEnabled(c.ID(), false)
	}
}

// addConnectorOff registers a connector that starts switched off until the user turns it on.
func (h *Manager) addConnectorOff(c connector.Connector) error {
	if err := h.addConnector(c); err != nil {
		return err
	}
	if h.connectorsOff == nil {
		h.connectorsOff = map[string]bool{}
	}
	h.connectorsOff[c.ID()] = true
	return nil
}

// connectorSecrets is where connector id keeps its secret (connector.Secrets): one row of connector_secrets, sealed
// with secret.key like launchers' keys and mail passwords. Each connector is handed its own; the plain text is only
// ever in memory.
type connectorSecrets struct {
	h  *Manager
	id string
}

func (h *Manager) secretsFor(id string) connector.Secrets { return connectorSecrets{h: h, id: id} }

var errNoDatabase = errors.New("no database: nothing can be kept")

func (s connectorSecrets) Load(ctx context.Context) ([]byte, error) {
	st, _ := s.h.store.(secretStore)
	if st == nil {
		return nil, errNoDatabase
	}
	sealed, ok, err := st.ConnectorSecret(ctx, s.id)
	if err != nil || !ok {
		return nil, err
	}
	plain, err := s.h.openKey(sealed)
	if err != nil {
		return nil, fmt.Errorf("%s: the stored secret does not open (secret.key replaced?)", s.id)
	}
	return []byte(plain), nil
}

func (s connectorSecrets) Save(ctx context.Context, data []byte) error {
	st, _ := s.h.store.(secretStore)
	if st == nil {
		return errNoDatabase
	}
	sealed, err := s.h.sealKey(string(data))
	if err != nil {
		return err
	}
	if err := st.PutConnectorSecret(ctx, s.id, sealed); err != nil {
		return err
	}
	if s.id == "wechat" {
		return s.h.reportWeChatIdentity(ctx, data)
	}
	return nil
}

func (s connectorSecrets) Delete(ctx context.Context) error {
	st, _ := s.h.store.(secretStore)
	if st == nil {
		return nil
	}
	if err := st.DeleteConnectorSecret(ctx, s.id); err != nil {
		return err
	}
	if s.id == "wechat" {
		return s.h.reportWeChatIdentity(ctx, nil)
	}
	return nil
}

// startConnectors registers the connectors, applies the user's switches and runs them.
func (h *Manager) startConnectors(ctx context.Context) {
	h.registerConnectors()
	h.attachExtensions(ctx) // the plugins' connectors, after the hub's own: those ids are taken
	h.migrateIMessage(ctx)  // manager.json's imessage.machine into Settings › Extensions › Messages, once (imessage_extension.go)
	h.applyIMessage()       // Messages on the Mac picked there, else manager.json's
	h.restoreConnectorSwitches(ctx)
	h.restoreGitHubSource(ctx)
	h.connectors.Start(ctx, h.connectorChanged, h.logf) // GitHub's old muted repositories move when the manager connects
}

// registerConnectors registers the connectors hub.json sets up.
func (h *Manager) registerConnectors() {
	if h.mail != nil {
		if err := h.addConnector(mailConnector{h}); err != nil {
			h.logf("mail connector: %v", err)
		}
	}
	// GitHub on every hub: hub.json's github.auth.from_machine reads that computer's gh login; without it (or
	// before it) the user signs in from the app with the device flow (connector/github/login.go).
	gh := github.Config{}
	if h.cfg.GitHub != nil {
		gh = *h.cfg.GitHub
	}
	if _, err := gh.HTTPClient(0); err != nil {
		h.logf("github: %v (every GitHub request fails until it is fixed)", err)
	}
	var ghc *github.Connector
	if gh.Configured() {
		name := gh.Auth.FromMachine
		if m := h.localMachine(name); m != nil {
			name = m.cfg.Name
		}
		ghc = github.New(gh, name, h.ghToken(gh.Auth.FromMachine))
	} else {
		ghc = github.New(gh, "", nil)
	}
	ghc.UseSecrets(h.secretsFor("github"))
	if err := h.addConnector(ghc); err != nil {
		h.logf("github connector: %v", err)
	}
	// Messages (imessage) is registered once the extensions are there: its Mac is the extension's, else manager.json's
	// (applyIMessage, startConnectors).
}

// siblingProgram is a program released with personad (update.ProgramNames) next to the running personad ("" when there
// is none).
func siblingProgram(name string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// sameBuild holds a built-in plugin's program (named name, for people) to the hub's build: the two come from one
// build (`version -v` says the same).
func sameBuild(h *Manager, name, path string) func(context.Context) error {
	want := fmt.Sprintf("%s\nchannel %s, build %d", h.version, h.build.Channel, h.build.Number)
	return func(ctx context.Context) error {
		if _, err := os.Stat(path); err != nil {
			return errors.New(i18n.T(h.userLang(), "hub-connectors.program_missing", i18n.Args{"name": name, "path": path}))
		}
		vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(vctx, path, "version", "-v").Output()
		if err != nil {
			return errors.New(i18n.T(h.userLang(), "hub-connectors.program_fails", i18n.Args{"name": name, "err": err.Error()}))
		}
		if got := strings.TrimSpace(string(out)); got != strings.TrimSpace(want) {
			first := func(s string) string { line, _, _ := strings.Cut(s, "\n"); return line }
			return errors.New(i18n.T(h.userLang(), "hub-connectors.program_version", i18n.Args{"name": name, "got": first(got), "want": first(want)}))
		}
		return nil
	}
}

// ghToken reads the token of machine's gh login (credential.gh_token); it never goes to a log.
func (h *Manager) ghToken(machine string) github.TokenSource {
	return func(ctx context.Context, host string) (string, error) {
		m := h.localMachine(machine)
		if m == nil {
			return "", github.ErrNoMachine
		}
		var out struct {
			Token string `json:"token"`
		}
		err := m.call(ctx, "credential.gh_token", map[string]any{"host": host}, &out)
		var re *rpc.Error
		switch {
		case errors.Is(err, errOffline):
			return "", github.ErrOffline
		case errors.As(err, &re) && strings.HasPrefix(re.Message, "unknown method"):
			return "", github.ErrNodeTooOld
		case errors.As(err, &re) && re.Code == proto.CodeNoGh:
			return "", github.ErrNoGh
		case errors.As(err, &re) && re.Code == proto.CodeNotSignedIn:
			return "", github.ErrNotSignedIn
		case err != nil:
			return "", fmt.Errorf("%s: %s", m.cfg.Name, github.Scrub(err.Error()))
		case out.Token == "":
			return "", github.ErrNotSignedIn
		}
		return out.Token, nil
	}
}
