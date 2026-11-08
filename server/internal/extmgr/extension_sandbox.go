package extmgr

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/egress"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
)

// An installed extension's program runs in a sandbox, reaching out only through the hub's egress proxy and only to the
// hosts of permissions.network (docs/extensions/extensions.md "Sandbox").
// Whether it does: the owner's choice for that extension, otherwise on unless its
// official catalog currently verifies with the final allowlisted key. Legacy
// nonofficial pins and trusted-market configuration never confer an exemption.
// Extensions installed before sandbox support keep their recorded choices.

// sandboxProbe is what this machine can do (tests replace it).
var sandboxProbe = sandbox.Probe

// extensionSandboxKey is the hub setting with the owner's choices.
const extensionSandboxKey = "extension_sandbox"

// officialMarketKeys contains only the final official catalog key id (not a secret).
var officialMarketKeys = []string{"3cce0f471911a1cf"}

type sandboxSettings struct {
	// Seeded: the extensions installed before sandboxes were recorded off, once.
	Seeded bool `json:"seeded"`
	// On is the owner's choice per extension; one not in it follows the default.
	On map[string]bool `json:"on,omitempty"`
}

// extensionSandboxes is the hub's side: the choices, the proxy.
type extensionSandboxes struct {
	mu       sync.Mutex
	save     sync.Mutex // one write of the settings at a time, of the newest (saveSandboxes)
	settings sandboxSettings
	proxy    *egress.Proxy
	proxyErr error
	once     sync.Once
	// noDefault: nothing is sandboxed unless switched on (the package's tests, whose test programs live outside their
	// packages).
	noDefault bool
}

// SandboxView is an installed extension's sandbox as the apps and the admin UI show it.
type SandboxView struct {
	// Enabled: it runs in a sandbox (or would, when the machine has none: Active false, Why says why).
	Enabled bool `json:"enabled"`
	// Default is what it would be without the owner's choice; Chosen whether there is one.
	Default bool   `json:"default"`
	Chosen  bool   `json:"chosen"`
	Active  bool   `json:"active"`
	Why     string `json:"why,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Trusted: it came from a trusted market.
	Trusted bool          `json:"trusted,omitempty"`
	Egress  egress.Counts `json:"egress"`
}

type sandboxState struct {
	SandboxView
	applies bool
}

// loadSandboxes reads the owner's choices; the first time, the installed extensions are recorded off: they keep
// running as they did before there were sandboxes.
func (ps *extensions) loadSandboxes(ctx context.Context) {
	if ps.h == nil || ps.h.store == nil {
		return
	}
	sb := &ps.sandboxes
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if _, err := ps.h.store.Setting(ctx, extensionSandboxKey, &sb.settings); err != nil {
		ps.logf("extensions: sandbox settings: %v", err)
		return
	}
	if sb.settings.Seeded {
		return
	}
	if sb.settings.On == nil {
		sb.settings.On = map[string]bool{}
	}
	for _, p := range ps.reg.List() {
		if sandboxApplies(p) {
			if _, ok := sb.settings.On[p.Record.StorageKey()]; !ok {
				sb.settings.On[p.Record.StorageKey()] = false
				ps.logf("extension %s: installed before sandboxes, keeps running without one (personad extension sandbox %s on)", p.Record.ID, p.Record.ID)
			}
		}
	}
	sb.settings.Seeded = true
	if err := ps.h.store.PutSetting(ctx, extensionSandboxKey, sb.settings); err != nil {
		ps.logf("extensions: sandbox settings: %v", err)
	}
}

// sandboxApplies: the hub runs p's program from a package it installed (not built in, not hub.json's, not elsewhere).
func sandboxApplies(p *extension.Extension) bool {
	return p != nil && p.Record.Source == extension.SourceInstalled && p.Record.Remote == nil && p.Manifest.Runtime != nil
}

// sandboxOf is whether p runs in a sandbox, and why.
func (ps *extensions) sandboxOf(ctx context.Context, p *extension.Extension) sandboxState {
	if p != nil && p.Manifest.Runtime.IsDeno() {
		st := sandboxState{applies: true, SandboxView: SandboxView{Enabled: true, Default: true}}
		development, err := sandbox.SourceDevelopmentResources(selfExecutable())
		st.Active = err == nil
		if development {
			st.Detail = "DEVELOPMENT_ONLY_RESOURCE_FALLBACK: " + i18n.T(ps.h.contextLang(ctx), "extension-source.development_resources")
		}
		if err != nil {
			st.Why = i18n.T(ps.h.contextLang(ctx), "extension-status.sandbox_unavailable")
			st.Detail = err.Error()
		}
		if px := ps.sandboxes.proxyIfStarted(); px != nil {
			st.Egress = px.Stats(p.Record.ID)
		}
		return st
	}
	if !sandboxApplies(p) {
		return sandboxState{}
	}
	st := sandboxState{applies: true}
	st.Trusted = ps.fromTrustedMarket(ctx, p)
	st.Default = !st.Trusted && !ps.sandboxes.noDefault
	ps.sandboxes.mu.Lock()
	on, chosen := ps.sandboxes.settings.On[p.Record.StorageKey()]
	ps.sandboxes.mu.Unlock()
	st.Enabled, st.Chosen = st.Default, chosen
	if chosen {
		st.Enabled = on
	}
	if st.Enabled {
		sup := sandboxProbe()
		st.Active = sup.Available()
		if !st.Active {
			l := ps.h.contextLang(ctx)
			why, detail := i18n.ExtensionFailureText(l, sup.Why, "extension-status.sandbox_unavailable")
			st.Detail = detail
			if detail == "" {
				st.Why = i18n.T(l, "hub-extension_sandbox.unavailable", i18n.Args{"why": why})
			} else {
				st.Why = why
			}
		}
	}
	if px := ps.sandboxes.proxyIfStarted(); px != nil {
		st.Egress = px.Stats(p.Record.ID)
	}
	return st
}

// fromTrustedMarket: p was installed from a market whose signing key the hub trusts.
func (ps *extensions) fromTrustedMarket(ctx context.Context, p *extension.Extension) bool {
	if p.Record.MarketKey == "" || !slices.Contains(officialMarketKeys, keyIDOf(p.Record.MarketKey)) {
		return false
	}
	mid := extension.FromMarket(p.Record.Origin)
	if mid == "" || ps.h == nil || ps.h.marketsLoad(ctx) != nil {
		return false
	}
	ms := ps.h.marketsState()
	ms.mu.Lock()
	s := ms.byID[mid]
	key := ""
	if s != nil && extension.IsOfficialMarket(s.rec.Spec) && p.Record.MarketKey == extension.Official.SigningKey && s.rec.Signature.State == extension.SigSigned && s.rec.Signature.KeyID == keyIDOf(p.Record.MarketKey) {
		key = p.Record.MarketKey
	}
	ms.mu.Unlock()
	if key == "" {
		return false
	}
	return slices.Contains(officialMarketKeys, keyIDOf(key))
}

func (sb *extensionSandboxes) proxyIfStarted() *egress.Proxy {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.proxy
}

// egress is the hub's egress proxy, started the first time an extension needs it.
func (ps *extensions) egress() (*egress.Proxy, error) {
	sb := &ps.sandboxes
	sb.once.Do(func() {
		hubAddr := "127.0.0.1:18660"
		if ps.h != nil && ps.h.cfg != nil {
			// The hub on this machine, or another manager's relay to it (hub_relay.go): nowhere when that is down.
			hub, err := ps.hubURL()
			if u, perr := url.Parse(hub); err == nil && perr == nil {
				hubAddr = u.Host
			} else {
				hubAddr = ""
			}
		}
		px := &egress.Proxy{HubAddr: hubAddr, Logf: ps.logf}
		if ps.stats != nil { // the admin UI's counts (extension_metrics.go)
			stats := ps.stats
			px.OnDecision = func(ext, _ string, allowed bool) {
				name := "egress.denied"
				if allowed {
					name = "egress.allowed"
				}
				stats.of(ext).Count(name, 1)
			}
		}
		err := px.Start()
		sb.mu.Lock()
		if err == nil {
			sb.proxy = px
		}
		sb.proxyErr = err
		sb.mu.Unlock()
	})
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.proxy, sb.proxyErr
}

// settingsTargets are the addresses extension id's settings name now that its granted permissions.network lets it
// reach ("config:<field>", #5906). Only what the user saved: an extension cannot write its own configuration.
func (ps *extensions) settingsTargets(id string) []egress.Target {
	p := ps.reg.Get(id)
	if p == nil {
		return nil
	}
	granted := extension.Permissions{Network: p.Record.Granted.Network}
	if len(granted.ConfiguredNetworks()) == 0 {
		return nil
	}
	return extension.ConfiguredTargets(&extension.Manifest{Permissions: granted}, ps.reg.Config(p))
}

// settingsMoved: extension id's settings changed; a running program reaches the address they name now, and no longer
// the one before (its restart registers anew too).
func (ps *extensions) settingsMoved(id string) {
	ps.sandboxes.mu.Lock()
	px := ps.sandboxes.proxy
	ps.sandboxes.mu.Unlock()
	if px != nil {
		px.SetTargets(id, ps.settingsTargets(id))
	}
}

// forgetEgress: p's program stopped for good this run (switched off, upgraded, removed); a new start registers anew.
func (ps *extensions) forgetEgress(id string) {
	if px := ps.sandboxes.proxyIfStarted(); px != nil {
		px.Unregister(id)
	}
}

// forgetSandbox drops the owner's choice for an extension no longer installed: installed again, it starts from the
// default.
func (ps *extensions) forgetSandbox(ctx context.Context, id string) {
	if id != "" && ps.root != "" {
		_ = os.RemoveAll(filepath.Join(ps.root, "run", id)) // its sockets
	}
	sb := &ps.sandboxes
	sb.mu.Lock()
	_, ok := sb.settings.On[id]
	if ok {
		delete(sb.settings.On, id)
	}
	sb.mu.Unlock()
	if ok && ps.h != nil && ps.h.store != nil {
		if err := ps.saveSandboxes(ctx); err != nil {
			ps.logf("extensions: sandbox settings: %v", err)
		}
	}
}

// saveSandboxes writes the owner's choices as they are now. The copy is taken after the previous write, under the
// lock: it has every choice so far, and its map is not the one the next choice writes to while it is encoded.
func (ps *extensions) saveSandboxes(ctx context.Context) error {
	sb := &ps.sandboxes
	sb.save.Lock()
	defer sb.save.Unlock()
	sb.mu.Lock()
	settings := sb.settings
	settings.On = maps.Clone(settings.On)
	sb.mu.Unlock()
	return ps.h.store.PutSetting(ctx, extensionSandboxKey, settings)
}

// setSandbox records the owner's choice (nil: back to the default) and restarts it.
func (ps *extensions) setSandbox(ctx context.Context, p *extension.Extension, on *bool, who string) error {
	if p.Manifest.Runtime.IsDeno() && on != nil && !*on {
		return i18n.Err("extension-source.isolation_required")
	}
	if !sandboxApplies(p) {
		return i18n.Err("hub-extension_sandbox.not_applicable")
	}
	if ps.h == nil || ps.h.store == nil {
		return i18n.Err("hub-extension_sandbox.no_database")
	}
	sb := &ps.sandboxes
	sb.mu.Lock()
	if sb.settings.On == nil {
		sb.settings.On = map[string]bool{}
	}
	if on == nil {
		delete(sb.settings.On, p.Record.StorageKey())
	} else {
		sb.settings.On[p.Record.StorageKey()] = *on
	}
	sb.settings.Seeded = true
	sb.mu.Unlock()
	if err := ps.saveSandboxes(ctx); err != nil {
		return err
	}
	st := ps.sandboxOf(ctx, p)
	ps.reg.Audit(ctx, p.Record.ID, "sandbox", who, map[string]any{"enabled": st.Enabled, "chosen": st.Chosen})
	ps.logf("extension %s: sandbox %v (by %s)", p.Record.ID, st.Enabled, who)
	run := ps.ctx // its program lives as long as the extensions do, not as long as this request
	if run == nil {
		run = context.Background()
	}
	ps.apply(run, p.Record.ID)
	return nil
}

// confine puts the sandbox and the egress proxy around s, the way p's program runs: in a sandbox, its HTTP socket
// moves to its run directory, the hub's API is HubHost through the proxy, and the program runs inside; with a sandbox
// on but none on this machine, it is at least pointed at the proxy.
func (ps *extensions) confine(ctx context.Context, p *extension.Extension, hp *hubExtension, s *host.Spec) error {
	if p.Manifest.Runtime.IsDeno() {
		return ps.confineSource(ctx, p, s)
	}
	st := ps.sandboxOf(ctx, p)
	if !st.applies || !st.Enabled {
		return nil
	}
	px, err := ps.egress()
	if err != nil {
		return err
	}
	id := p.Record.ID
	g := p.Record.Granted
	// The addresses its settings name are read at each start (and again when the settings change: settingsTargets).
	ruleNow := func() egress.Rule {
		return egress.Rule{Hosts: slices.Clone(g.Network), Hub: g.FullHub() || len(g.Hub) > 0, Targets: ps.settingsTargets(id)}
	}
	if !st.Active {
		start := s.StartEnv
		s.StartEnv = func() []string {
			var env []string
			if start != nil {
				env = start()
			}
			return append(env, sandbox.ProxyEnv(px.Register(id, ruleNow()))...)
		}
		return nil
	}
	run := filepath.Join(ps.root, "run", p.Record.StorageKey())
	// An agent runtime (permissions.agent_runtimes) reaches this machine's node through a socket of its own that leads
	// to the node's runtime.sock and nowhere else (#5413): the node's is in the home directory, which the sandbox hides.
	runtimeSock, proxySock := "", ""
	if g.AgentRuntimes {
		runtimeSock = filepath.Join(run, "runtime.sock")
	}
	if runtime.GOOS != "darwin" { // Linux's way out of the sandbox; macOS's is loopback TCP
		proxySock = filepath.Join(run, "proxy.sock")
	}
	// The manager binds these as the program starts, which cannot go on without them.
	for _, sock := range []string{runtimeSock, proxySock} {
		if err := checkSocketPath(sock); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(run, 0o700); err != nil {
		return err
	}
	if s.HTTP {
		sock := filepath.Join(run, "http.sock")
		hp.socket, s.Socket = sock, sock
		s.Env = setEnv(s.Env, "PERSONA_PLUGIN_SOCKET", sock)
	}
	if runtimeSock != "" {
		s.Env = identity.Environment(s.Env, "PERSONA_RUNTIME_SOCKET="+runtimeSock)
	}
	hubURL := "http://" + egress.HubHost
	s.Env = identity.Environment(s.Env, "PERSONA_HUB_URL="+hubURL)
	if start := s.StartEnv; start != nil {
		s.StartEnv = func() []string { return identity.Environment(start(), "PERSONA_HUB_URL="+hubURL) }
	}
	if initf := s.Init; initf != nil {
		s.Init = func(ctx context.Context) (extension.InitializeParams, error) {
			params, err := initf(ctx)
			if params.Hub.URL != "" {
				params.Hub.URL = hubURL
			}
			return params, err
		}
	}
	limits := p.Manifest.Runtime.Resources.Limits()
	sup := sandboxProbe()
	pkg, data := p.Dir, ps.reg.DataDir(id)
	tools := ""
	if len(ps.reg.Tools(id, p.Record.Version)) > 0 {
		tools = ps.reg.ToolsDir(id)
	}
	s.Wrap = func(command string, args []string) (string, []string, []string, error) {
		proxyURL := px.Register(id, ruleNow())
		if runtimeSock != "" {
			if err := px.RelayUnix(id, runtimeSock, nodeRuntimeSocket()); err != nil {
				return "", nil, nil, err
			}
		}
		plan := sandbox.Plan{ID: id, Command: command, Args: args, Pkg: pkg, Data: data, Run: run, Tools: tools,
			MemoryMB: limits.MemoryMB, CPUPercent: limits.CPUPercent, Scope: sup.Scope, Env: os.Environ()}
		if runtime.GOOS == "darwin" {
			plan.ProxyURL = proxyURL
			name, a, env := sandbox.Darwin(plan)
			return name, a, env, nil
		}
		plan.ProxySocket, plan.Self = proxySock, selfExecutable()
		if err := px.ListenUnix(id, plan.ProxySocket); err != nil {
			return "", nil, nil, err
		}
		name, a, env := sandbox.Linux(sup.Bwrap, plan)
		return name, a, env, nil
	}
	return nil
}

// nodeRuntimeSocket is where agent runtimes reach this machine's node (node.RuntimeSocketPath); tests replace it.
var nodeRuntimeSocket = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.SocketDir(home), "runtime.sock")
}

// setEnv replaces the value of an existing environment key.
func setEnv(env []string, name, value string) []string {
	out := slices.Clone(env)
	for i, kv := range out {
		k, _, _ := strings.Cut(kv, "=")
		if k == name {
			out[i] = k + "=" + value
		}
	}
	return out
}

// sandboxRoutes: the owner turns an extension's sandbox on or off (this machine's master token, or the admin UI).
func (h *Manager) sandboxRoutes(api *http.ServeMux) {
	api.HandleFunc("PUT /api/v1/extensions/{id}/-/sandbox", func(w http.ResponseWriter, r *http.Request) {
		if !h.fromThisMachine(r) {
			writeErrCode(w, http.StatusForbidden, "this_machine_only", i18n.Err("hub-extension_sandbox.owner_only"))
			return
		}
		h.putSandbox(w, r, actorOr(r, "personad extension"))
	})
}

func (h *Manager) putSandbox(w http.ResponseWriter, r *http.Request, who string) {
	ps := h.extensions
	var p *extension.Extension
	if ps != nil {
		p = ps.reg.Get(r.PathValue("id"))
	}
	if p == nil {
		writeErrCode(w, http.StatusNotFound, "not_found", errors.New("no such extension"))
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"` // null: back to the default
	}
	if !readExtensionJSON(w, r, &in) {
		return
	}
	if err := ps.setSandbox(r.Context(), p, in.Enabled, who); err != nil {
		writeErrCode(w, http.StatusConflict, "no_sandbox", err)
		return
	}
	writeJSON(w, detailOut(r, ps.detail(r.Context(), ps.reg.Get(p.Record.ID))))
}
