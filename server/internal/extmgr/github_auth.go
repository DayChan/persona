package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/github"
	"github.com/nwverse/persona/server/internal/connector/proc"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Where GitHub's token comes from, as the user picks it in Settings › Connectors › GitHub (docs/extensions/connectors.md "2 · 7"): a
// computer's gh login or the device flow. The choice is kept in settings ("github_auth"); hub.json's
// github.auth.from_machine is only the default until the user chooses.

const githubAuthKey = "github_auth"

// githubAuth is the user's choice.
type githubAuth struct {
	Source  string `json:"source"`            // github.SourceMachine | github.SourceDevice
	Machine string `json:"machine,omitempty"` // the machine's id, for SourceMachine
}

// githubMachine is a computer GitHub's token can come from, with its gh login as GitHub sees it.
type githubMachine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	github.MachineLogin
}

// githubAuthView is GET /api/v1/connectors/github/auth: the choice, the computers to choose from, and what the token
// in use may do (scopes are known once GitHub said; missing lists what the connector needs and does not have).
type githubAuthView struct {
	Source      string                 `json:"source"`
	Machine     string                 `json:"machine,omitempty"`
	Machines    []githubMachine        `json:"machines"`
	Scopes      []string               `json:"scopes,omitempty"`
	ScopesKnown bool                   `json:"scopes_known"`
	Missing     []github.RequiredScope `json:"missing,omitempty"`
}

// githubConnector is the hub's own GitHub connector while it runs (not while an extension took its place).
func (h *Manager) githubConnector() *github.Connector {
	c, _ := h.connectors.Get("github").(*github.Connector)
	return c
}

// ownGitHub is the hub's own GitHub connector, running or set aside for an extension's (connectors_migrate.go): it
// still checks each computer's gh login for the page.
func (h *Manager) ownGitHub() *github.Connector {
	if c := h.githubConnector(); c != nil {
		return c
	}
	if v, ok := h.migrated.Load("github"); ok {
		c, _ := v.(*github.Connector)
		return c
	}
	return nil
}

// githubExtension is an extension's GitHub connector when one took the hub's place (#2830).
func (h *Manager) githubExtension() *proc.Conn {
	c, _ := h.connectors.Get("github").(*proc.Conn)
	return c
}

// githubChoice is the user's choice, kept from before; hub.json's github.auth.from_machine until they chose.
func (h *Manager) githubChoice(ctx context.Context) (githubAuth, bool) {
	var a githubAuth
	if h.store != nil {
		if ok, err := h.store.Setting(ctx, githubAuthKey, &a); err == nil && ok {
			return a, true
		}
	}
	if h.cfg.GitHub != nil && h.cfg.GitHub.Configured() {
		return githubAuth{Source: github.SourceMachine, Machine: h.cfg.GitHub.Auth.FromMachine}, false
	}
	return githubAuth{Source: github.SourceDevice}, false
}

// restoreGitHubSource applies the user's choice, kept from before, at start.
func (h *Manager) restoreGitHubSource(ctx context.Context) {
	c := h.githubConnector()
	if c == nil {
		return
	}
	if a, chosen := h.githubChoice(ctx); chosen {
		h.applyGitHubSource(ctx, c, a)
	}
}

func (h *Manager) applyGitHubSource(ctx context.Context, c *github.Connector, a githubAuth) {
	if a.Source == github.SourceMachine && a.Machine != "" {
		c.UseMachine(ctx, a.Machine, h.machineName(a.Machine), h.ghToken(a.Machine))
		return
	}
	c.UseDevice()
}

// applyGitHubExtensionSource gives an extension's GitHub connector the user's choice (or hub.json's default); the
// connector asks for a computer's token itself (credentials.gh_token).
func (h *Manager) applyGitHubExtensionSource(ctx context.Context, c *proc.Conn, a githubAuth) (serve.AuthView, error) {
	src := serve.AuthSource{Source: a.Source}
	if a.Source == github.SourceMachine && a.Machine != "" {
		src.Machine, src.MachineName = a.Machine, h.machineName(a.Machine)
	} else {
		src.Source = github.SourceDevice
	}
	return c.SetAuth(ctx, src)
}

// githubExtensionTaken sets an extension's GitHub connector up as the hub's own was: the user's sign-in choice, and
// the inbox settings the hub's own had (hub.json's github.inbox) when the user never changed them.
func (h *Manager) githubExtensionTaken(ctx context.Context, c *proc.Conn, cs connectorSettings) {
	a, _ := h.githubChoice(ctx)
	if _, err := h.applyGitHubExtensionSource(ctx, c, a); err != nil {
		h.logf("github: the extension's sign-in source: %v", err)
	}
	if _, chosen := cs.Options["github"]; chosen || !c.Manifest().Has(connector.CapOptions) {
		return
	}
	if own := h.ownGitHub(); own != nil {
		if err := c.SetOptions(own.Options()); err != nil {
			h.logf("github: the extension's inbox settings: %v", err)
		}
	}
}

// githubAuthNow is what the page shows: every machine's gh login, asked at once (a few seconds at most). c checks the
// logins (the hub's own connector, also while an extension's runs); ext, when set, says the choice and the scopes.
func (h *Manager) githubAuthNow(ctx context.Context, c *github.Connector, ext *proc.Conn) githubAuthView {
	v := githubAuthView{Source: c.Source(), Machine: c.MachineID(), Machines: []githubMachine{}}
	v.Scopes, v.ScopesKnown = c.Scopes()
	if ext != nil {
		av, err := ext.Auth(ctx)
		if err != nil {
			a, _ := h.githubChoice(ctx)
			av = serve.AuthView{Source: a.Source, Machine: a.Machine}
		}
		v.Source, v.Machine, v.Scopes, v.ScopesKnown = av.Source, av.Machine, av.Scopes, av.ScopesKnown
	}
	if v.ScopesKnown {
		v.Missing = github.MissingScopes(v.Scopes)
	}
	ms := h.machinesNow()
	out := make([]githubMachine, len(ms))
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, m := range ms {
		out[i] = githubMachine{ID: m.cfg.ID, Name: m.cfg.Name}
		if out[i].Name == "" {
			out[i].Name = m.cfg.ID
		}
		if !m.isOnline() {
			out[i].State = "offline"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i].MachineLogin = c.Probe(ctx, h.ghToken(m.cfg.ID))
		}()
	}
	wg.Wait()
	v.Machines = out
	return v
}

var errNoGitHub = errors.New("no GitHub connector")

func (h *Manager) githubAuthRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/connectors/github/auth", func(w http.ResponseWriter, r *http.Request) {
		c := h.ownGitHub()
		if c == nil {
			writeErr(w, http.StatusNotFound, errNoGitHub)
			return
		}
		writeJSON(w, h.githubAuthNow(r.Context(), c, h.githubExtension()))
	})
	// Choose where the token comes from: {"source": "machine", "machine": "<id>"} or {"source": "device"}. Choosing
	// one leaves the other (the device flow's token is forgotten when a computer is chosen).
	api.HandleFunc("PUT /api/v1/connectors/github/auth", func(w http.ResponseWriter, r *http.Request) {
		c := h.ownGitHub()
		if c == nil {
			writeErr(w, http.StatusNotFound, errNoGitHub)
			return
		}
		var a githubAuth
		if err := decodeJSONBody(w, r, &a, 4<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		switch {
		case a.Source == github.SourceDevice:
			a.Machine = ""
		case a.Source == github.SourceMachine && h.localMachine(a.Machine) != nil:
		default:
			writeErr(w, http.StatusBadRequest, fmt.Errorf("source %q / machine %q", a.Source, a.Machine))
			return
		}
		if h.store != nil {
			if err := h.store.PutSetting(r.Context(), githubAuthKey, a); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		detail := map[string]any{"source": a.Source}
		if a.Machine != "" {
			detail["machine"] = a.Machine
		}
		h.auditConnector(r.Context(), c.ID(), auditConnectorAuthSource, detail)
		if ext := h.githubExtension(); ext != nil {
			if _, err := h.applyGitHubExtensionSource(r.Context(), ext, a); err != nil {
				writeErr(w, http.StatusBadGateway, err)
				return
			}
			writeJSON(w, h.githubAuthNow(r.Context(), c, ext))
			return
		}
		h.applyGitHubSource(r.Context(), c, a)
		writeJSON(w, h.githubAuthNow(r.Context(), c, nil))
	})
}

// ghTokenFor answers an extension's credentials.gh_token (one granted gh_login, the GitHub connector that took the
// hub's place): the token of machine's gh login, as the hub's own connector reads it. The token goes to that program
// only, never to a log; why there is none goes as the connector protocol's codes.
func (h *Manager) ghTokenFor(ctx context.Context, machine, host string) (string, error) {
	if host == "" {
		host = "github.com"
	}
	tok, err := h.ghToken(machine)(ctx, host)
	if err == nil {
		return tok, nil
	}
	for _, c := range []struct {
		err  error
		code string
	}{{github.ErrNoMachine, serve.CodeNoMachine}, {github.ErrOffline, serve.CodeOffline}, {github.ErrNodeTooOld, serve.CodeNodeTooOld},
		{github.ErrNoGh, serve.CodeNoGh}, {github.ErrNotSignedIn, serve.CodeNotSignedIn}} {
		if errors.Is(err, c.err) {
			return "", &rpc.Error{Code: c.code, Message: err.Error()}
		}
	}
	return "", err
}

// githubMigratedKey records which install of the extension got manager.json's github settings (githubInstall). An
// uninstall deletes an extension's settings, so a reinstall, a new install, gets them again; a key the user cleared in
// the same install stays cleared. A hub before that wrote true, whichever install it was: done while the extension
// holds one of the keys the move would set.
const githubMigratedKey = "github.migrated_to_extension"

// githubInstall names one install of an extension: its id and when it was installed, in Unix seconds as the database
// keeps it. A built-in extension cannot be uninstalled: its id is enough.
func githubInstall(p *extension.Extension) string {
	if p.Record.Source == extension.SourceBuiltin || p.Record.InstalledAt.IsZero() {
		return p.Record.ID
	}
	return fmt.Sprintf("%s@%d", p.Record.ID, p.Record.InstalledAt.Unix())
}

// migrateGitHubConfig moves manager.json's github.proxy and github.auth.host into the settings of the extension whose
// connector took the hub's place, once per install of it (githubMigratedKey), for keys the user has not set there: its
// GitHub traffic goes the way the hub's own did. The extension restarts with them (a config change). A failure is
// logged and tried at the next start.
func (h *Manager) migrateGitHubConfig(ctx context.Context, extensionID string) {
	gh := h.cfg.GitHub
	if gh == nil || (gh.Proxy == "" && gh.Auth.Host == "") || h.store == nil || h.extensions == nil || h.extensions.reg == nil {
		return
	}
	reg := h.extensions.reg
	p := reg.Get(extensionID)
	if p == nil {
		return
	}
	cfg := map[string]any{}
	_ = json.Unmarshal(p.Record.Config, &cfg)
	props := map[string]json.RawMessage{}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(p.Full.Configuration, &schema) == nil {
		props = schema.Properties
	}
	moves := map[string]string{}
	for key, v := range map[string]string{"proxy": gh.Proxy, "host": gh.Auth.Host} {
		if _, has := props[key]; has && v != "" {
			moves[key] = v
		}
	}
	held := func(key string) bool { cur, _ := cfg[key].(string); return cur != "" }
	var done any
	if ok, err := h.store.Setting(ctx, githubMigratedKey, &done); err == nil && ok {
		switch d := done.(type) {
		case string:
			if d == githubInstall(p) {
				return
			}
		case bool: // written before installs were told apart
			for key := range moves {
				if d && held(key) {
					return
				}
			}
		}
	}
	moved := false
	for key, v := range moves {
		if held(key) {
			continue
		}
		cfg[key], moved = v, true
	}
	if moved {
		raw, _ := json.Marshal(cfg)
		if err := reg.SetConfig(ctx, extensionID, raw, "hub"); err != nil {
			h.logf("github: moving manager.json's github.proxy / auth.host into the extension's settings: %v", err)
			return
		}
		h.logf("github: manager.json's github.proxy / auth.host moved into 设置 › 扩展 › %s", p.Full.Name)
	}
	if cur := reg.Get(extensionID); cur != nil {
		p = cur
	}
	if err := h.store.PutSetting(ctx, githubMigratedKey, githubInstall(p)); err != nil {
		h.logf("github: marking the move done: %v", err)
	}
}
