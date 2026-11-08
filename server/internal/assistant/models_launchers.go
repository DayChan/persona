package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// Settings › Model providers (docs/integrations/launchers.md): every launcher in one list. Those in each machine's ~/.zshrc are found by its
// node and remembered by the hub (it hands them over: launchers.zshrc); those added in the app are the assistant's —
// base URL, model, and the API key AES-GCM sealed with the manager's own secret.key (docs/assistant/managers.md "The built-in
// manager's own files") — and given to every node, which runs them as `personad launch <name>`. Deleting a
// ~/.zshrc launcher only hides it in Persona.

const (
	launchersManagedKey = "launchers.managed" // []storedLauncher
	launchersHiddenKey  = "launchers.hidden"  // []string: ~/.zshrc launcher names hidden in Persona
	launchersZshrcKey   = "launchers.zshrc"   // map machine id → []proto.Launcher, the last list seen
	// launchersMetaKey: map machine id → launcherMeta (its shell, rc file, when its list last came in).
	launchersMetaKey = "launchers.zshrc.meta"
)

// launcherSyncTimeout is how long the list waits for one machine (its node caches a minute and keeps listing past
// it, so a slow rc file is there the next time); the page shows that machine as timed out with its last list.
var launcherSyncTimeout = 10 * time.Second

// launcherMeta is what is kept of a machine's last good launcher list besides the list.
type launcherMeta struct {
	Shell    string    `json:"shell,omitempty"`
	RCFile   string    `json:"rc_file,omitempty"`
	SyncedAt time.Time `json:"synced_at"`
}

// LauncherSync is how one machine's launchers came in this time, for Settings › Model providers (docs/integrations/launchers.md, “Sync state”):
// ok, offline (not connected: its last list shows, marked), timeout (no answer in launcherSyncTimeout) or error
// (Error says why).
type LauncherSync struct {
	Machine  string     `json:"machine"`
	Name     string     `json:"name"`
	State    string     `json:"state"`
	Error    string     `json:"error,omitempty"`
	Shell    string     `json:"shell,omitempty"`
	RCFile   string     `json:"rc_file,omitempty"`
	SyncedAt *time.Time `json:"synced_at,omitempty"` // the last good list, also for one that did not answer now
}

// defaultRCFile is where a node from before the rc_file field found its launchers: it only ran zsh scripts there.
const defaultRCFile = "~/.zshrc"

// storedLauncher is a provider added in Persona as kept in settings: the key sealed.
type storedLauncher struct {
	proto.ManagedLauncher
	KeySealed string `json:"key_sealed,omitempty"`
	// HeadersSealed: Headers (ANTHROPIC_CUSTOM_HEADERS) sealed like the key; the embedded Headers is never stored.
	HeadersSealed string `json:"headers_sealed,omitempty"`
	EnvSealed     string `json:"env_sealed,omitempty"` // Env (#5953) sealed the same way; Env itself is never stored
	Preset        string `json:"preset,omitempty"`     // the preset (built-in or a plugin's) it was added from
	// DisplayName is what the apps call it (any language, "Zhipu GLM"); Name stays the ASCII id the launch uses.
	DisplayName string    `json:"display_name,omitempty"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

// LauncherView is one launcher for the app: where it comes from, which machines have it; never the key.
type LauncherView struct {
	Kind                   string   `json:"kind,omitempty"`
	Agent                  string   `json:"agent,omitempty"`
	API                    string   `json:"api,omitempty"`
	Name                   string   `json:"name"`
	DisplayName            string   `json:"display_name,omitempty"` // managed: the name shown, "" shows Name
	Source                 string   `json:"source"`                 // zshrc (an rc file's) | persona | agy | builtin (plain claude)
	BaseURL                string   `json:"base_url,omitempty"`
	Model                  string   `json:"model,omitempty"`
	SmallModel             string   `json:"small_model,omitempty"`
	SubagentModel          string   `json:"subagent_model,omitempty"`
	CustomModel            string   `json:"custom_model,omitempty"`
	CustomModelName        string   `json:"custom_model_name,omitempty"`
	CustomModelDescription string   `json:"custom_model_description,omitempty"`
	Args                   []string `json:"args,omitempty"`
	HasKey                 bool     `json:"has_key,omitempty"`
	Auth                   string   `json:"auth,omitempty"`     // managed: api_key (x-api-key) or bearer
	Machines               []string `json:"machines,omitempty"` // zshrc: the machines whose rc file defines it; builtin: that have claude
	Hidden                 bool     `json:"hidden,omitempty"`
	// RCFiles (source zshrc only): the files it was found in (~/.zshrc, ~/.bashrc: each machine's login shell's), for the group title.
	RCFiles []string `json:"rc_files,omitempty"`
	// Stale (zshrc, builtin): the machines of Machines that did not answer this time (offline, timed out, failed):
	// what they had the last time.
	Stale []string `json:"stale,omitempty"`
	// Versions (builtin): Claude Code's version on each machine that said.
	Versions map[string]string `json:"versions,omitempty"`
	// Managed only (#2455, proto.ManagedLauncher's fields): never the headers themselves, only has_headers.
	Models        []proto.ProviderModel `json:"models,omitempty"`
	OpusModel     string                `json:"opus_model,omitempty"`
	SonnetModel   string                `json:"sonnet_model,omitempty"`
	HaikuModel    string                `json:"haiku_model,omitempty"`
	FableModel    string                `json:"fable_model,omitempty"`
	SubagentForce bool                  `json:"subagent_force,omitempty"`
	HasHeaders    bool                  `json:"has_headers,omitempty"`
	HasEnv        bool                  `json:"has_env,omitempty"` // never the environment itself
	Proxy         string                `json:"proxy,omitempty"`
	NoProxy       string                `json:"no_proxy,omitempty"`
	TimeoutMS     int                   `json:"timeout_ms,omitempty"`
	CompactWindow int                   `json:"compact_window,omitempty"`
	MaxContext    int                   `json:"max_context,omitempty"`
	Effort        string                `json:"effort,omitempty"`
	// Origin is ManagedLauncher.Source ("" made by hand, "zshrc:<alias>@<machine>", "preset:<id>"); "source" is taken.
	Origin string `json:"origin,omitempty"`
	// Definition per machine (zshrc launchers, detail only): the function body, secrets masked.
	Definitions map[string]string `json:"definitions,omitempty"`
	// Details per machine (zshrc launchers, detail only): what the launcher really sets when run (node/launcherprobe.go).
	Details map[string]proto.LauncherDetail `json:"details,omitempty"`
}

// launcherView is a managed launcher for the app: never the key or the headers.
func launcherView(l storedLauncher) LauncherView {
	return LauncherView{Kind: l.Kind, Agent: l.Agent, API: l.API, Name: l.Name, DisplayName: l.DisplayName, Source: "persona", BaseURL: l.BaseURL, Model: l.Model, SmallModel: l.SmallModel,
		SubagentModel: l.SubagentModel, CustomModel: l.CustomModel, CustomModelName: l.CustomModelName,
		CustomModelDescription: l.CustomModelDescription, Args: l.Args, HasKey: l.KeySealed != "", Auth: l.Auth,
		Models: l.Models, OpusModel: l.OpusModel, SonnetModel: l.SonnetModel, HaikuModel: l.HaikuModel, FableModel: l.FableModel,
		SubagentForce: l.SubagentForce, HasHeaders: l.HeadersSealed != "", HasEnv: l.EnvSealed != "", Proxy: l.Proxy, NoProxy: l.NoProxy, TimeoutMS: l.TimeoutMS,
		CompactWindow: l.CompactWindow, MaxContext: l.MaxContext, Effort: l.Effort, Origin: l.Source}
}

var managedLauncherName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// reservedLauncherNames are taken by claude itself and the /api/v1/launchers/<word> routes.
var reservedLauncherNames = []string{"claude", "presets", "test", "discover", "import", "health"}

// ---- the key ----

// The providers' keys are sealed with the manager's own secret.key (internal/secretkey; the built-in manager's is a
// copy of the data directory's, which sealed them before they were the assistant's, docs/assistant/managers.md "The built-in
// manager's own files").

func (s *Service) sealKey(plain string) (string, error) { return s.models.secret.Seal(plain) }

func (s *Service) openKey(sealed string) (string, error) { return s.models.secret.Open(sealed) }

// ---- what is stored ----

type managedSnapshotKey struct{}

// withManagedSnapshot lets nested model resolution use the same checked provider list as the
// operation holding models.mu, including fallback and in-use decisions.
func withManagedSnapshot(ctx context.Context, list []storedLauncher) context.Context {
	return context.WithValue(ctx, managedSnapshotKey{}, list)
}

func (s *Service) storedLaunchers(ctx context.Context) []storedLauncher {
	if list, ok := ctx.Value(managedSnapshotKey{}).([]storedLauncher); ok {
		return list
	}
	var list []storedLauncher
	if st := s.store(); st != nil {
		_, _ = st.Setting(ctx, launchersManagedKey, &list)
	}
	return list
}

var errManagedLaunchersRead = errors.New("managed provider configuration unavailable")

// readStoredLaunchers preserves every raw sealed record for mutation decisions. A missing setting is empty;
// storage and JSON failures abort the operation. Decryption belongs to consumers that need credentials,
// so an explicitly replaced or removed corrupt credential remains repairable.
func (s *Service) readStoredLaunchers(ctx context.Context) (list []storedLauncher, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w: %w", errManagedLaunchersRead, err)
		}
	}()
	st := s.store()
	if st == nil {
		return nil, i18n.Err("assistant-models.err_no_database")
	}
	if _, err := st.Setting(ctx, launchersManagedKey, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (s *Service) hiddenLaunchers(ctx context.Context) []string {
	var list []string
	if st := s.store(); st != nil {
		_, _ = st.Setting(ctx, launchersHiddenKey, &list)
	}
	return list
}

func (s *Service) putSetting(ctx context.Context, key string, v any) error {
	st := s.store()
	if st == nil {
		return errors.New("no database")
	}
	return st.PutSetting(ctx, key, v)
}

// ---- the nodes ----

// managedForNodes: the launchers added in Persona with their keys and headers, for the nodes (not the Responses ones).
func (s *Service) managedForNodes(ctx context.Context) ([]proto.ManagedLauncher, error) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return nil, err
	}
	return s.nodeLaunchers(stored)
}

// managedNodeCalls is the new-connection configuration callback. Failed reads send no replacement.
func (s *Service) managedNodeCalls(ctx context.Context, machine string) []devicewire.NodeCall {
	if s.machinePending(ctx, machine) {
		return nil
	}
	list, err := s.managedForNodes(ctx)
	if err != nil {
		s.logf("launchers: prepare node configuration: %v", err)
		return nil
	}
	b, err := json.Marshal(map[string]any{"launchers": list})
	if err != nil {
		return nil
	}
	return []devicewire.NodeCall{{MachineID: machine, Method: DeviceLaunchersManaged, Params: b}}
}

// nodeLaunchers prepares the entire payload before any setting write or node call.
func (s *Service) nodeLaunchers(stored []storedLauncher) ([]proto.ManagedLauncher, error) {
	out := []proto.ManagedLauncher{}
	for _, l := range stored {
		if l.Kind == "api" && l.API == "responses" {
			continue
		}
		m, err := s.openStoredLauncher(l)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Service) openStoredLauncher(l storedLauncher) (proto.ManagedLauncher, error) {
	m := l.ManagedLauncher
	var err error
	if m.APIKey, err = s.openKey(l.KeySealed); err != nil {
		return m, i18n.Err("assistant-models.err_key_decrypt")
	}
	if m.Headers, err = s.openKey(l.HeadersSealed); err != nil {
		return m, i18n.Err("assistant-models.err_key_decrypt")
	}
	if m.Env, err = s.openKey(l.EnvSealed); err != nil {
		return m, i18n.Err("assistant-models.err_key_decrypt")
	}
	m.HasHeaders, m.HasEnv = m.Headers != "", m.Env != ""
	return m, nil
}

// pushManaged gives the online nodes (one machine when machine is not "") the launchers added in Persona; they keep
// them in memory. After a change; a node's new connection gets them right after it is configured (main.go's
// devices NodeCalls). Only this manager's own machines: the hub's devices.list has no other (docs/assistant/managers.md), and a
// new connection the hub says is another manager's gets no NodeCalls (internal/devices hubConnected). A computer
// waiting to be confirmed gets none (machine_trust.go; the hub does not list it either).
func (s *Service) pushManaged(ctx context.Context, machine string) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		s.logf("launchers: read managed launchers: %v", err)
		return
	}
	list, err := s.nodeLaunchers(stored)
	if err != nil {
		s.logf("launchers: prepare managed launchers: %v", err)
		return
	}
	s.pushNodeLaunchers(ctx, machine, list)
}

// pushNodeLaunchers publishes a confirmed snapshot while the caller holds models.mu.
func (s *Service) pushNodeLaunchers(ctx context.Context, machine string, list []proto.ManagedLauncher) {
	for _, d := range s.devices(ctx) {
		if !d.Online || (machine != "" && d.ID != machine) || s.machinePending(ctx, d.ID) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := s.call(cctx, d.ID, assistantwire.DeviceLaunchersManaged, map[string]any{"launchers": list}, nil); err != nil {
			s.logf("launchers: give %s the managed launchers: %v", d.ID, err)
		}
		cancel()
	}
}

// zshrcLaunchers is one machine's rc-file launchers, and how they came in (Sync.State "" before any ask).
type zshrcLaunchers struct {
	MachineID string
	Launchers []proto.Launcher
	Sync      LauncherSync
	// ClaudeVersion: Claude Code's version there, as the hub knows it ("" when it has none or never said).
	ClaudeVersion string
}

// zshrcLaunchers is every machine's rc-file launchers (~/.zshrc, ~/.bashrc) in the machines' order: as last seen (an
// offline one's from the last time, kept in settings launchers.zshrc), or asked of the online nodes again first
// (refresh, or nothing seen since the assistant started; the node caches a minute). Each machine gets
// launcherSyncTimeout, and its Sync says whether its list came in now.
func (s *Service) zshrcLaunchers(ctx context.Context, refresh bool) []zshrcLaunchers {
	st := &s.models
	st.zshrcMu.Lock()
	seen, states := st.zshrc, st.zshrcSync
	st.zshrcMu.Unlock()
	devices := s.devices(ctx)
	if seen == nil || refresh {
		seen = map[string][]proto.Launcher{}
		meta := map[string]launcherMeta{}
		if store := s.store(); store != nil {
			_, _ = store.Setting(ctx, launchersZshrcKey, &seen)
			_, _ = store.Setting(ctx, launchersMetaKey, &meta)
		}
		states = map[string]LauncherSync{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		changed := false
		for _, d := range devices {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := s.listLaunchers(ctx, d)
				mu.Lock()
				defer mu.Unlock()
				state := LauncherSync{Machine: d.ID, Name: d.Name, State: "ok"}
				if err != nil {
					state.State, state.Error = launcherSyncState(ctx, err)
				} else {
					if !slices.Equal(seen[d.ID], res.Launchers) {
						seen[d.ID] = res.Launchers
					}
					meta[d.ID], changed = launcherMeta{Shell: res.Shell, RCFile: res.RCFile, SyncedAt: time.Now()}, true
				}
				if m, ok := meta[d.ID]; ok {
					state.Shell, state.RCFile = m.Shell, cmp.Or(m.RCFile, defaultRCFile)
					if at := m.SyncedAt; !at.IsZero() {
						state.SyncedAt = &at
					}
				}
				states[d.ID] = state
			}()
		}
		wg.Wait()
		if store := s.store(); changed && store != nil {
			_ = store.PutSetting(ctx, launchersZshrcKey, seen)
			_ = store.PutSetting(ctx, launchersMetaKey, meta)
		}
		st.zshrcMu.Lock()
		st.zshrc, st.zshrcSync = seen, states
		st.zshrcMu.Unlock()
	}
	out := make([]zshrcLaunchers, 0, len(devices))
	for _, d := range devices {
		out = append(out, zshrcLaunchers{MachineID: d.ID, Launchers: seen[d.ID], Sync: states[d.ID], ClaudeVersion: d.ClaudeVersion})
	}
	return out
}

// launcherListing is a node's launchers.list: the launchers, and the shell and rc file they came from ("" from an
// older node).
type launcherListing struct {
	Launchers []proto.Launcher `json:"launchers"`
	Shell     string           `json:"shell"`
	RCFile    string           `json:"rc_file"`
}

// listLaunchers asks one machine for its list, giving it launcherSyncTimeout; an offline machine is not asked.
func (s *Service) listLaunchers(ctx context.Context, d Device) (launcherListing, error) {
	var res launcherListing
	if !d.Online {
		return res, apierr.ErrOffline
	}
	cctx, cancel := context.WithTimeout(ctx, launcherSyncTimeout)
	defer cancel()
	err := s.call(cctx, d.ID, DeviceLaunchersList, map[string]any{}, &res)
	if err != nil && cctx.Err() != nil && ctx.Err() == nil {
		err = context.DeadlineExceeded
	}
	return res, err
}

// launcherSyncState names why a machine's list did not come: offline, timeout, or error with the reason.
func launcherSyncState(ctx context.Context, err error) (state, reason string) {
	switch {
	case errors.Is(err, apierr.ErrOffline):
		return "offline", ""
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		return "timeout", ""
	}
	return "error", textfmt.Clip(strings.TrimSpace(err.Error()), 300)
}

// configuredLauncher is machine's hub.json launcher name (machines[].launchers, in the hello): a wrapper the node may
// not list, the agent the user said it runs. The node runs a launcher it does not list only when it is vouched for
// like this (configured, node runnableLauncher).
func (s *Service) configuredLauncher(ctx context.Context, machine, name string) (ConfiguredLauncher, bool) {
	for _, l := range s.hello(ctx).Models.Launchers[machine] {
		if l.Name == name {
			return l, true
		}
	}
	return ConfiguredLauncher{}, false
}

// launcherParams are a node launcher method's params for name on machine: configured (and its agent_type) when
// hub.json names it there.
func (s *Service) launcherParams(ctx context.Context, machine, name string, params map[string]any) map[string]any {
	params["name"] = name
	if l, ok := s.configuredLauncher(ctx, machine, name); ok {
		params["configured"] = true
		if l.AgentType != "" {
			params["agent_type"] = l.AgentType
		}
	}
	return params
}

// machineSource: a launcher a machine defines itself, a ~/.zshrc function or alias ("zshrc") or a wrapper script on
// its PATH ("path", node/pathlaunchers.go); its page shows its text and what a probe found.
func machineSource(source string) bool { return source == "zshrc" || source == "path" }

// launcherViews is the whole list: managed first (newest last), then ~/.zshrc ones by name; hidden ones flagged.
func (s *Service) launcherViews(ctx context.Context) []LauncherView {
	return s.launcherViewsOf(ctx, s.zshrcLaunchers(ctx, false))
}

func (s *Service) launcherViewsOf(ctx context.Context, zshrc []zshrcLaunchers) []LauncherView {
	return s.launcherViewsFrom(ctx, zshrc, s.storedLaunchers(ctx))
}

func (s *Service) launcherViewsFrom(ctx context.Context, zshrc []zshrcLaunchers, stored []storedLauncher) []LauncherView {
	var out []LauncherView
	managed := map[string]bool{}
	for _, l := range stored {
		managed[l.Name] = true
		out = append(out, launcherView(l))
	}
	hidden := s.hiddenLaunchers(ctx)
	byName := map[string]*LauncherView{}
	var names []string
	for _, m := range zshrc {
		for _, l := range m.Launchers {
			if managed[l.Name] || l.Name == "claude" || (l.Source != "" && !machineSource(l.Source) && l.Agent != "agy" && l.Agent != "omp") {
				continue // a runtime's launcher without model configuration (DSH, node/runtimes.go) is not listed here
			}
			v := byName[l.Name]
			if v == nil {
				src := "zshrc"
				if l.Source != "" {
					src = l.Source
				}
				v = &LauncherView{Name: l.Name, Source: src, Agent: l.Agent, BaseURL: l.BaseURL, Model: l.Model, Hidden: slices.Contains(hidden, l.Name)}
				byName[l.Name] = v
				names = append(names, l.Name)
			}
			v.Machines = append(v.Machines, m.MachineID)
			if rc := cmp.Or(m.Sync.RCFile, defaultRCFile); v.Source == "zshrc" && !slices.Contains(v.RCFiles, rc) {
				v.RCFiles = append(v.RCFiles, rc)
			}
			if m.Sync.State != "" && m.Sync.State != "ok" {
				v.Stale = append(v.Stale, m.MachineID) // the machine's last list: it is offline or did not answer now
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, *byName[n])
	}
	return out
}

// builtinClaude is the plain `claude` of every machine that has Claude Code (the default launcher, “Claude Code
// default” in Model Configuration; assistant.launcher "claude"): one view, its machines and their versions. ok false
// when no machine is known to have it, or a provider added in Persona took the name.
func (s *Service) builtinClaude(ctx context.Context, zshrc []zshrcLaunchers) (LauncherView, bool) {
	if slices.ContainsFunc(s.storedLaunchers(ctx), func(l storedLauncher) bool { return l.Name == "claude" }) {
		return LauncherView{}, false
	}
	v := LauncherView{Name: "claude", Source: "builtin", Versions: map[string]string{}}
	for _, m := range zshrc {
		listed := slices.ContainsFunc(m.Launchers, func(l proto.Launcher) bool { return l.Name == "claude" })
		if m.ClaudeVersion == "" && !listed {
			continue
		}
		v.Machines = append(v.Machines, m.MachineID)
		if m.ClaudeVersion != "" {
			v.Versions[m.MachineID] = m.ClaudeVersion
		}
		if m.Sync.State != "" && m.Sync.State != "ok" {
			v.Stale = append(v.Stale, m.MachineID)
		}
	}
	return v, len(v.Machines) > 0
}

// launcherSyncList is every machine's sync state, in the machines' order (the ones asked).
func launcherSyncList(zshrc []zshrcLaunchers) []LauncherSync {
	out := []LauncherSync{}
	for _, m := range zshrc {
		if m.Sync.State != "" {
			out = append(out, m.Sync)
		}
	}
	return out
}

// machineLaunchers is what a machine can start (the pickers: new chat, the assistant's model): its own launchers
// (the node's list) but the hidden ones, then the managed ones.
func (s *Service) machineLaunchers(ctx context.Context, machine string) ([]proto.Launcher, error) {
	var res struct {
		Launchers []proto.LauncherEnvelope `json:"launchers"`
	}
	if err := s.call(ctx, machine, DeviceLaunchersList, map[string]any{}, &res); err != nil {
		return nil, err
	}
	hidden := s.hiddenLaunchers(ctx)
	managed := s.storedLaunchers(ctx)
	out := make([]proto.Launcher, 0, len(res.Launchers)+len(managed))
	for _, envelope := range res.Launchers {
		l := proto.Launcher(envelope)
		if slices.Contains(hidden, l.Name) || slices.ContainsFunc(managed, func(m storedLauncher) bool { return m.Name == l.Name }) {
			continue
		}
		l.Source = compat.Source(l.Source)
		if l.Source == "" { // a runtime's launcher (node/runtimes.go) keeps its own source, e.g. "dsh"
			l.Source = "zshrc"
		}
		out = append(out, l)
	}
	for _, m := range managed {
		if m.Kind == "api" {
			continue
		}
		out = append(out, proto.Launcher{Name: m.Name, BaseURL: m.BaseURL, Model: m.Model, Source: "persona", Agent: m.Agent, Assistant: m.Agent == "agy"})
	}
	return out, nil
}

// launcherAvailable: whether the machine can still run this launcher name (plain claude always; a deleted managed
// launcher or a renamed ~/.zshrc one does not). A failed list counts as available.
func (s *Service) launcherAvailable(ctx context.Context, machine, name string) bool {
	if name == "" || name == "claude" {
		return true
	}
	if slices.ContainsFunc(s.storedLaunchers(ctx), func(l storedLauncher) bool { return l.Name == name }) {
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := s.machineLaunchers(cctx, machine)
	if err != nil {
		return true
	}
	return slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == name })
}

// launcherHasModel: whether the launcher offers model (its id or what an alias resolves to), from its list as it was
// a few minutes ago at most. Unknown (an agent with no list, a failed call) counts as yes.
func (s *Service) launcherHasModel(ctx context.Context, machine, launcher, model string) bool {
	key := machine + "\x00" + launcher
	c := &s.models
	c.cacheMu.Lock()
	if c.cacheAt != nil && time.Since(c.cacheAt[key]) < 5*time.Minute {
		set := c.have[key]
		c.cacheMu.Unlock()
		return set == nil || set[model]
	}
	c.cacheMu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := s.askLauncherModels(cctx, machine, launcher, false)
	if err != nil {
		return true
	}
	set := map[string]bool{}
	for _, x := range res.Models {
		set[x.ID] = true
		if x.Resolved != "" {
			set[x.Resolved] = true
		}
	}
	if len(set) == 0 {
		return true
	}
	c.cacheMu.Lock()
	if c.cacheAt == nil {
		c.cacheAt, c.have = map[string]time.Time{}, map[string]map[string]bool{}
	}
	c.cacheAt[key], c.have[key] = time.Now(), set
	c.cacheMu.Unlock()
	return set[model]
}

// askLauncherModels is machine's node's /model list of launcher name (node/launcher_models.go), with the models of
// the plugin gateway behind it.
func (s *Service) askLauncherModels(ctx context.Context, machine, name string, refresh bool) (proto.LauncherModels, error) {
	var res proto.LauncherModels
	if err := s.call(ctx, machine, assistantwire.DeviceLaunchersModels, s.launcherParams(ctx, machine, name, map[string]any{"refresh": refresh}), &res); err != nil {
		return res, err
	}
	if res.Models == nil {
		res.Models = []proto.AgentModel{}
	}
	return s.presetModels(ctx, machine, name, res), nil
}

// ---- the app's API ----

type launcherInput struct {
	Kind                   string   `json:"kind"`
	Agent                  string   `json:"agent"`
	API                    string   `json:"api"`
	Name                   string   `json:"name"`
	BaseURL                string   `json:"base_url"`
	Model                  string   `json:"model"`
	SmallModel             string   `json:"small_model"`
	SubagentModel          string   `json:"subagent_model"`
	CustomModel            string   `json:"custom_model"`
	CustomModelName        string   `json:"custom_model_name"`
	CustomModelDescription string   `json:"custom_model_description"`
	Args                   []string `json:"args"`
	APIKey                 *string  `json:"api_key"` // nil or "" on an update keeps the key
	Auth                   string   `json:"auth"`    // api_key (x-api-key, the default) or bearer (Authorization: Bearer)
	// Preset is the provider it is added from (GET /api/v1/launchers/presets): what is left empty is filled in, the
	// name too (POST only).
	Preset string `json:"preset,omitempty"`
	// DisplayName is the name shown, any language; nil on an update keeps it; empty on a provider the hub names from a preset is the preset's name.
	DisplayName *string `json:"display_name"`
	// Assign (POST only): the categories (assistant, agent.new) to put on the new provider, each only while it is
	// still at its default (docs/assistant/onboarding.md 5.4).
	Assign []string `json:"assign,omitempty"`

	// #2455's fields (proto.ManagedLauncher). Each is a pointer: left out (an app from before them) keeps what is
	// stored on an update. Headers "" clears them; like the key they are sealed and never sent back.
	Models        *[]proto.ProviderModel `json:"models"`
	OpusModel     *string                `json:"opus_model"`
	SonnetModel   *string                `json:"sonnet_model"`
	HaikuModel    *string                `json:"haiku_model"`
	FableModel    *string                `json:"fable_model"`
	SubagentForce *bool                  `json:"subagent_force"`
	Headers       *string                `json:"headers"`
	Env           *string                `json:"env"` // one NAME=value a line (#5953); "" clears it; sealed like Headers
	Proxy         *string                `json:"proxy"`
	NoProxy       *string                `json:"no_proxy"`
	TimeoutMS     *int                   `json:"timeout_ms"`
	CompactWindow *int                   `json:"compact_window"`
	MaxContext    *int                   `json:"max_context"`
	Effort        *string                `json:"effort"`
}

// checkEffort: a thinking level Claude Code takes (CLAUDE_CODE_EFFORT_LEVEL), or none. whose is the error's key
// (err_effort_<whose>: default, model, model_default), model the model it names.
func checkEffort(whose, model, e string) error {
	if e == "" || slices.Contains(proto.EffortLevels, e) {
		return nil
	}
	return i18n.Err("assistant-models_launchers.err_effort_"+whose, i18n.Args{"model": model, "effort": strconv.Quote(e), "levels": strings.Join(proto.EffortLevels, " / ")})
}

// checkGateway is a gateway's base URL: http(s), a host, nothing that carries a credential.
func checkGateway(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return i18n.Err("assistant-models_launchers.err_base_url")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return i18n.Err("assistant-models_launchers.err_base_url_parts")
	}
	return nil
}

// checkHeaders: one "Name: value" a line (ANTHROPIC_CUSTOM_HEADERS); the error never quotes a value.
func checkHeaders(raw string) error {
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, _, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" || strings.ContainsAny(strings.TrimSpace(name), " \t") {
			return i18n.Err("assistant-models_launchers.err_header_line", i18n.Args{"line": i + 1})
		}
	}
	return nil
}

// checkEnv: one NAME=value a line ("export " before it is fine), NAME a shell variable's name; a line starting with #
// is a comment. The rules of the node's envLine (node/launcherenv.go) and the web's envProblem. The error never quotes
// a line: a value may be a secret.
func checkEnv(raw string) error {
	lines := 0
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimFunc(line, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines++
		name, _, ok := strings.Cut(exportPrefix.ReplaceAllString(line, ""), "=")
		if !ok || !envName.MatchString(strings.TrimSpace(name)) || strings.ContainsRune(line, 0) {
			return i18n.Err("assistant-models_launchers.err_env_line", i18n.Args{"line": i + 1})
		}
		if lines > 50 {
			return i18n.Err("assistant-models_launchers.err_env_limit")
		}
	}
	return nil
}

var exportPrefix = regexp.MustCompile(`^export\s+`)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (in launcherInput) checkProvider() error {
	if in.Models != nil {
		seen := map[string]bool{}
		for _, m := range *in.Models {
			id := strings.TrimSpace(m.ID)
			if id == "" {
				return i18n.Err("assistant-models_launchers.err_model_no_id")
			}
			if seen[id] {
				return i18n.Err("assistant-models_launchers.err_model_duplicate", i18n.Args{"model": id})
			}
			seen[id] = true
			if m.ContextWindow < 0 {
				return i18n.Err("assistant-models_launchers.err_model_context_negative", i18n.Args{"model": id})
			}
			for _, e := range m.Efforts {
				if e == "" {
					return i18n.Err("assistant-models_launchers.err_model_effort_empty", i18n.Args{"model": id})
				}
				if err := checkEffort("model", id, e); err != nil {
					return err
				}
			}
			if err := checkEffort("model_default", id, m.DefaultEffort); err != nil {
				return err
			}
			if m.DefaultEffort != "" && len(m.Efforts) > 0 && !slices.Contains(m.Efforts, m.DefaultEffort) {
				return i18n.Err("assistant-models_launchers.err_model_default_effort_unlisted", i18n.Args{"model": id})
			}
		}
	}
	if in.Effort != nil {
		if err := checkEffort("default", "", *in.Effort); err != nil {
			return err
		}
	}
	for _, v := range []struct {
		key   string
		value *int
	}{{"err_timeout_negative", in.TimeoutMS}, {"err_compact_window_negative", in.CompactWindow}, {"err_max_context_negative", in.MaxContext}} {
		if v.value != nil && *v.value < 0 {
			return i18n.Err("assistant-models_launchers." + v.key)
		}
	}
	if in.Proxy != nil && strings.TrimSpace(*in.Proxy) != "" {
		u, err := url.Parse(strings.TrimSpace(*in.Proxy))
		if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https", "socks5", "socks5h"}, u.Scheme) {
			return i18n.Err("assistant-models_launchers.err_proxy")
		}
		// The proxy is stored and shown as is (not sealed): a password in it would go back to every client.
		if u.User != nil {
			return i18n.Err("assistant-models_launchers.err_proxy_credentials")
		}
	}
	if in.Headers != nil {
		if err := checkHeaders(*in.Headers); err != nil {
			return err
		}
	}
	if in.Env != nil {
		if err := checkEnv(*in.Env); err != nil {
			return err
		}
	}
	for _, v := range []*string{in.OpusModel, in.SonnetModel, in.HaikuModel, in.FableModel} {
		if v != nil && len(strings.TrimSpace(*v)) > 200 {
			return i18n.Err("assistant-models_launchers.err_tier_model_long")
		}
	}
	return nil
}

// applyProvider writes the #2455 fields given into s (headers apart: they are sealed by the caller).
func (in launcherInput) applyProvider(s *storedLauncher) {
	str := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	num := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	if in.Models != nil {
		var models []proto.ProviderModel
		for _, m := range *in.Models {
			m.ID, m.Name = strings.TrimSpace(m.ID), strings.TrimSpace(m.Name)
			models = append(models, m)
		}
		s.Models = models
	}
	str(&s.OpusModel, in.OpusModel)
	str(&s.SonnetModel, in.SonnetModel)
	str(&s.HaikuModel, in.HaikuModel)
	str(&s.FableModel, in.FableModel)
	str(&s.Proxy, in.Proxy)
	str(&s.NoProxy, in.NoProxy)
	str(&s.Effort, in.Effort)
	num(&s.TimeoutMS, in.TimeoutMS)
	num(&s.CompactWindow, in.CompactWindow)
	num(&s.MaxContext, in.MaxContext)
	if in.SubagentForce != nil {
		s.SubagentForce = *in.SubagentForce
	}
}

var errNoPreset = i18n.Err("assistant-models_launchers.err_no_preset")

// gatewayHost is the host of a provider's Base URL, lower case; "" without one.
func gatewayHost(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// onlyHost says whether a bypass list names host and nothing else, as iOS reads No Proxy back.
func onlyHost(list, host string) bool {
	hosts := strings.FieldsFunc(strings.ToLower(list), func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	return host != "" && len(hosts) == 1 && hosts[0] == host
}

func (in launcherInput) check() error {
	if in.Kind != "" && in.Kind != "api" && in.Kind != "launcher" {
		return i18n.Err("assistant-models_launchers.err_kind")
	}
	if in.Agent != "" && in.Agent != "claude" && in.Agent != "codex" && in.Agent != "agy" && in.Agent != "omp" {
		return i18n.Err("assistant-models_launchers.err_kind")
	}
	if in.API != "" && in.API != "messages" && in.API != "responses" {
		return i18n.Err("assistant-models_launchers.err_api")
	}
	if in.Kind != "api" && (in.Agent == "" || in.Agent == "claude") && in.API == "responses" {
		return i18n.Err("assistant-models_launchers.err_claude_messages")
	}
	if in.Auth != "" && in.Auth != "api_key" && in.Auth != "bearer" {
		return i18n.Err("assistant-models_launchers.err_auth")
	}
	if in.Kind == "api" && (len(in.Args) != 0 || in.SmallModel != "" || in.SubagentModel != "" || in.CustomModel != "") {
		return i18n.Err("assistant-models_launchers.err_api_launch_fields")
	}
	for _, v := range []struct{ name, value string }{{"err_subagent_model_long", in.SubagentModel}, {"err_custom_model_long", in.CustomModel}} {
		if len(strings.TrimSpace(v.value)) > 200 {
			return i18n.Err("assistant-models_launchers." + v.name)
		}
	}
	if in.CustomModelName == "" && in.CustomModelDescription != "" {
		return i18n.Err("assistant-models_launchers.err_custom_model_name")
	}
	if !managedLauncherName.MatchString(in.Name) || slices.Contains(reservedLauncherNames, in.Name) {
		return i18n.Err("assistant-models_launchers.err_name")
	}
	if in.DisplayName != nil {
		if d := strings.TrimSpace(*in.DisplayName); utf8.RuneCountInString(d) > 64 || strings.IndexFunc(d, unicode.IsControl) >= 0 {
			return i18n.Err("assistant-models_launchers.err_display_name")
		}
	}
	if strings.TrimSpace(in.BaseURL) != "" || in.Kind == "api" || in.Agent == "" || in.Agent == "claude" {
		if err := checkGateway(in.BaseURL); err != nil {
			return err
		}
	}
	if err := in.checkProvider(); err != nil {
		return err
	}
	if strings.TrimSpace(in.Model) == "" {
		return i18n.Err("assistant-models_launchers.err_model_required")
	}
	for _, a := range in.Args {
		if !strings.HasPrefix(a, "-") {
			return i18n.Err("assistant-models_launchers.err_arg_dash", i18n.Args{"arg": strconv.Quote(a)})
		}
	}
	return nil
}

func (s *Service) launcherRoutes() {
	s.handle("GET /api/v1/launchers", func(w http.ResponseWriter, r *http.Request) {
		// Each machine gets launcherSyncTimeout: one that is slow or gone never holds the page (sync says which).
		ctx, cancel := context.WithTimeout(r.Context(), launcherSyncTimeout+5*time.Second)
		defer cancel()
		// A launcher just added to an rc file shows up (the node caches a minute).
		zshrc := s.zshrcLaunchers(ctx, true)
		views := s.launcherViewsOf(ctx, zshrc)
		if b, ok := s.builtinClaude(ctx, zshrc); ok {
			views = append(views, b)
		}
		apierr.WriteJSON(w, map[string]any{"launchers": views, "sync": launcherSyncList(zshrc)})
	})
	s.handle("GET /api/v1/launchers/{name}", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		name := r.PathValue("name")
		if b, ok := s.builtinClaude(ctx, s.zshrcLaunchers(ctx, false)); ok && name == b.Name {
			apierr.WriteJSON(w, map[string]any{"launcher": b})
			return
		}
		for _, v := range s.launcherViews(ctx) {
			if v.Name != name {
				continue
			}
			if machineSource(v.Source) {
				// Every machine at once, the text and the probe apart: a launcher that fetches a token first is slow to
				// probe (the node allows 15 s), and its text should still show.
				v.Definitions, v.Details = map[string]string{}, map[string]proto.LauncherDetail{}
				var mu sync.Mutex
				var wg sync.WaitGroup
				for _, id := range v.Machines {
					wg.Add(2)
					go func() {
						defer wg.Done()
						var res struct {
							Definition string `json:"definition"`
						}
						if err := s.call(ctx, id, assistantwire.DeviceLaunchersShow, map[string]any{"name": name}, &res); err == nil {
							mu.Lock()
							v.Definitions[id] = res.Definition
							mu.Unlock()
						}
					}()
					go func() {
						defer wg.Done()
						var res struct {
							Detail proto.LauncherDetail `json:"detail"`
						}
						if err := s.call(ctx, id, DeviceLaunchersProbe, s.launcherParams(ctx, id, name, map[string]any{}), &res); err == nil {
							mu.Lock()
							v.Details[id] = res.Detail
							mu.Unlock()
						}
					}()
				}
				wg.Wait()
			} else if v.Source == "agy" {
				v.Details = map[string]proto.LauncherDetail{}
				for _, id := range v.Machines {
					l := s.requestCtxLang(ctx)
					models := []proto.LauncherModel{{Role: i18n.T(l, "assistant-models_launchers.role_default"), Env: "agy acp", Model: v.Model}}
					if res, err := s.askLauncherModels(ctx, id, name, false); err == nil && len(res.Models) > 0 {
						models = nil
						for _, am := range res.Models {
							role := i18n.T(l, "assistant-models_launchers.role_optional")
							if am.Default {
								role = i18n.T(l, "assistant-models_launchers.role_default")
							}
							models = append(models, proto.LauncherModel{Role: role, Env: am.ID, Model: am.Name})
						}
					}
					v.Details[id] = proto.LauncherDetail{
						Agent:  "agy",
						Auth:   "Antigravity OAuth (~/.gemini/antigravity-cli)",
						Models: models,
					}
				}
			}
			apierr.WriteJSON(w, map[string]any{"launcher": v})
			return
		}
		apierr.Write(w, http.StatusNotFound, errors.New("no such launcher"))
	})
	s.handle("POST /api/v1/launchers", func(w http.ResponseWriter, r *http.Request) {
		var in launcherInput
		if err := decodeBody(w, r, &in, 1<<20); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		ctx := r.Context()
		if err := s.presetFill(ctx, &in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		for _, id := range in.Assign {
			if !slices.Contains(assignablePurposes, id) {
				apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-models_launchers.err_assign", i18n.Args{"purpose": strconv.Quote(id)}))
				return
			}
		}
		stored, err := s.readStoredLaunchers(ctx)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		views := s.launcherViewsFrom(ctx, s.zshrcLaunchers(ctx, false), stored)
		taken := func(n string) bool {
			return slices.ContainsFunc(views, func(v LauncherView) bool { return v.Name == n })
		}
		if in.Name == "" && in.Preset != "" {
			var n int
			in.Name, n = launcherNameFor(in.Preset, taken)
			if in.DisplayName == nil || strings.TrimSpace(*in.DisplayName) == "" {
				d := s.presetDisplayName(ctx, in.Preset, n)
				in.DisplayName = &d
			}
		}
		if err := in.check(); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if taken(in.Name) {
			apierr.Write(w, http.StatusConflict, i18n.Err("assistant-models_launchers.err_name_taken", i18n.Args{"name": in.Name}))
			return
		}
		l, err := s.saveLauncher(ctx, in, nil)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		out := map[string]any{"launcher": launcherView(l)}
		if in.Assign != nil {
			assigned, kept := s.assignIfDefault(ctx, launcherProvider(l.Name), in.Assign)
			out["assigned"], out["kept"] = assigned, kept
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("PUT /api/v1/launchers/{name}", func(w http.ResponseWriter, r *http.Request) {
		var in launcherInput
		if err := decodeBody(w, r, &in, 1<<20); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		in.Name = r.PathValue("name")
		stored, err := s.readStoredLaunchers(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		var old *storedLauncher
		for _, l := range stored {
			if l.Name == in.Name {
				l := l
				old = &l
				break
			}
		}
		if old != nil && in.Agent == "" && in.Kind != "api" {
			in.Agent = old.Agent
		}
		if err := in.check(); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if old == nil {
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-models_launchers.err_not_managed"))
			return
		}
		saved, err := s.saveLauncher(r.Context(), in, old)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"launcher": launcherView(saved)})
	})
	s.handle("DELETE /api/v1/launchers/{name}", func(w http.ResponseWriter, r *http.Request) {
		ctx, name := r.Context(), r.PathValue("name")
		s.models.mu.Lock()
		list, err := s.readStoredLaunchers(ctx)
		if err != nil {
			s.models.mu.Unlock()
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		if i := slices.IndexFunc(list, func(l storedLauncher) bool { return l.Name == name }); i >= 0 {
			// Check dependencies, delete and publish the confirmed snapshot under the configuration lock.
			uses, err := s.providerModelUses(withManagedSnapshot(ctx, list), launcherProvider(name))
			if err == nil && len(uses) == 0 {
				err = s.storeManagedLaunchers(ctx, slices.Delete(list, i, i+1))
			}
			s.models.mu.Unlock()
			if err != nil {
				apierr.Write(w, 500, err)
				return
			}
			if len(uses) > 0 {
				apierr.Write(w, 409, i18n.Err("assistant-models_launchers.err_in_use", i18n.Args{"uses": strings.Join(uses, i18n.T(s.requestCtxLang(r.Context()), "assistant-models_launchers.list_sep"))}))
				return
			}
			apierr.WriteJSON(w, map[string]any{"ok": true, "deleted": true})
			return
		}
		s.models.mu.Unlock()
		if name == "claude" { // plain claude is every machine's default launcher: there is nothing to hide
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-models_launchers.err_builtin_hide"))
			return
		}
		hidden := s.hiddenLaunchers(ctx) // an rc-file launcher: hidden in Persona, the file is not touched
		if !slices.Contains(hidden, name) {
			hidden = append(hidden, name)
		}
		if err := s.putSetting(ctx, launchersHiddenKey, hidden); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true, "hidden": true})
	})
	// "Test": one real request through the launcher on a machine (default: the assistant's).
	s.handle("POST /api/v1/launchers/{name}/check", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
		defer cancel()
		for _, l := range s.storedLaunchers(ctx) {
			if l.Name == r.PathValue("name") && l.Kind == "api" {
				started := time.Now()
				config, err := s.apiConfig(ctx, ModelBinding{Provider: launcherProvider(l.Name), Model: l.Model})
				answer := ""
				if err == nil {
					answer, err = ai.New(config).Complete(ctx, "", prompts.RenderLauncherCheck(), 128)
				}
				result := proto.LauncherCheck{Agent: "api", Model: config.Model, Answer: textfmt.Clip(textfmt.OneLine(answer), 200), OK: err == nil,
					Millis: int(time.Since(started).Milliseconds())}
				if err != nil {
					result.Error = textfmt.Clip(textfmt.OneLine(err.Error()), 400)
				}
				apierr.WriteJSON(w, map[string]any{"check": result})
				return
			}
		}
		machine := r.URL.Query().Get("machine") // the id the app knows
		if _, ok := s.device(ctx, machine); !ok {
			machine = s.settings(ctx).MachineID
		}
		if _, ok := s.device(ctx, machine); !ok {
			apierr.Write(w, http.StatusBadRequest, errors.New("no machine to test on"))
			return
		}
		var res proto.LauncherCheck
		if err := s.call(ctx, machine, assistantwire.DeviceLaunchersCheck, map[string]any{"name": r.PathValue("name")}, &res); err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"machine": machine, "check": res})
	})
	s.handle("POST /api/v1/launchers/{name}/restore", func(w http.ResponseWriter, r *http.Request) {
		ctx, name := r.Context(), r.PathValue("name")
		hidden := slices.DeleteFunc(s.hiddenLaunchers(ctx), func(n string) bool { return n == name })
		if err := s.putSetting(ctx, launchersHiddenKey, hidden); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}

// sessionLaunchers is the machine's launchers with the configured ones (hub.json machines[].launchers) applied: a
// configured launcher overrides the type, base URL and model of the one with its name, or adds a wrapper the node
// does not list. A launcher without a type gets the agent it was found to start, else one by its name.
func sessionLaunchers(discovered, configured []ConfiguredLauncher) []ConfiguredLauncher {
	list := append([]ConfiguredLauncher{}, discovered...)
	for _, override := range configured {
		found := false
		for i := range list {
			if list[i].Name == override.Name {
				if override.AgentType != "" {
					list[i].AgentType = override.AgentType
				}
				if override.BaseURL != "" {
					list[i].BaseURL = override.BaseURL
				}
				if override.Model != "" {
					list[i].Model = override.Model
				}
				found = true
				break
			}
		}
		if !found && override.Name != "" {
			list = append(list, override)
		}
	}
	for i := range list {
		if list[i].AgentType == "" {
			switch {
			case list[i].Agent != "":
				list[i].AgentType = list[i].Agent
			case list[i].Name == "codex" || list[i].Name == "traex":
				list[i].AgentType = list[i].Name
			default:
				list[i].AgentType = "claude"
			}
		}
	}
	return list
}

var errUnknownMachine = errors.New("unknown machine")

// machineLauncherRoutes are "New Agent"'s pickers on one machine: its launchers with the agent each starts
// (sessionLaunchers), and a launcher's /model list (node/launcher_models.go; the node keeps a list 10 minutes,
// ?refresh=1 asks the CLI again).
func (s *Service) machineLauncherRoutes() {
	s.handle("GET /api/v1/machines/{mid}/launchers", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		mid := r.PathValue("mid")
		if _, ok := s.device(ctx, mid); !ok {
			apierr.Write(w, http.StatusNotFound, errUnknownMachine)
			return
		}
		ls, err := s.machineLaunchers(ctx, mid)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		discovered := make([]ConfiguredLauncher, 0, len(ls))
		for _, l := range ls {
			discovered = append(discovered, ConfiguredLauncher{Launcher: l})
		}
		apierr.WriteJSON(w, map[string]any{"launchers": s.withHealth(ctx, mid, sessionLaunchers(discovered, s.hello(ctx).Models.Launchers[mid])),
			"default": s.pickerDefault(ctx, r)})
	})
	s.handle("GET /api/v1/machines/{mid}/launchers/{name}/models", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
		defer cancel()
		mid := r.PathValue("mid")
		if _, ok := s.device(ctx, mid); !ok {
			apierr.Write(w, http.StatusNotFound, errUnknownMachine)
			return
		}
		res, err := s.askLauncherModels(ctx, mid, r.PathValue("name"), r.URL.Query().Get("refresh") == "1")
		if err != nil {
			writeLauncherModelsErr(w, err)
			return
		}
		apierr.WriteJSON(w, res)
	})
	s.handle("GET /api/v1/launchers/presets", func(w http.ResponseWriter, r *http.Request) {
		apierr.WriteJSON(w, map[string]any{"presets": s.launcherPresets(r.Context())})
	})
}

// saveLauncher stores in (over old on an update) and tells the nodes.
func (s *Service) saveLauncher(ctx context.Context, in launcherInput, old *storedLauncher) (storedLauncher, error) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	list, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return storedLauncher{}, err
	}
	if old == nil && slices.ContainsFunc(list, func(l storedLauncher) bool { return l.Name == in.Name }) {
		return storedLauncher{}, i18n.Err("assistant-models_launchers.err_name_taken", i18n.Args{"name": in.Name})
	}
	// Refresh the stored object under the same lock as profile switches: omitted fields keep today's values.
	if old != nil {
		i := slices.IndexFunc(list, func(l storedLauncher) bool { return l.Name == in.Name })
		if i < 0 {
			return storedLauncher{}, i18n.Err("assistant-models_launchers.err_not_managed")
		}
		old = &list[i]
	}
	now := time.Now().UTC()
	l := storedLauncher{Created: now, Updated: now}
	if old != nil {
		l = *old
		l.Updated = now
	}
	l.Name, l.BaseURL, l.Model, l.SmallModel = in.Name, strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"),
		strings.TrimSpace(in.Model), strings.TrimSpace(in.SmallModel)
	l.SubagentModel, l.CustomModel = strings.TrimSpace(in.SubagentModel), strings.TrimSpace(in.CustomModel)
	l.CustomModelName, l.CustomModelDescription = strings.TrimSpace(in.CustomModelName), strings.TrimSpace(in.CustomModelDescription)
	l.Kind, l.API = in.Kind, in.API
	switch {
	case in.Kind == "api" || in.Agent == "claude":
		l.Agent = ""
	case in.Agent != "":
		l.Agent = in.Agent
	} // no agent in the request (an app without it): an update keeps what is stored
	l.Args = in.Args
	if in.Preset != "" {
		l.Preset = in.Preset
	}
	if in.DisplayName != nil {
		l.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	switch {
	case in.API == "responses": // always goes as Bearer: nothing to keep
		l.Auth = ""
	case in.Auth == "bearer":
		l.Auth = in.Auth
	case in.Auth == "api_key":
		l.Auth = ""
	} // no auth in the request (an app from before it): a change keeps what is stored
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		sealed, err := s.sealKey(strings.TrimSpace(*in.APIKey))
		if err != nil {
			return l, err
		}
		l.KeySealed = sealed
	}
	in.applyProvider(&l)
	// A bypass list that is the gateway's host alone (iOS's Proxy › No Proxy) follows the gateway to a new address: the
	// web form and an import send it back as it was, and the old host would leave the new gateway on the proxy (#6238).
	if old != nil && l.BaseURL != old.BaseURL && onlyHost(l.NoProxy, gatewayHost(old.BaseURL)) {
		l.NoProxy = gatewayHost(l.BaseURL)
	}
	if in.Headers != nil { // left out keeps them, "" clears them
		l.HeadersSealed = ""
		if hd := strings.TrimSpace(*in.Headers); hd != "" {
			sealed, err := s.sealKey(hd)
			if err != nil {
				return l, err
			}
			l.HeadersSealed = sealed
		}
	}
	if in.Env != nil { // left out keeps it, "" clears it
		l.EnvSealed = ""
		if env := strings.TrimSpace(*in.Env); env != "" {
			sealed, err := s.sealKey(env)
			if err != nil {
				return l, err
			}
			l.EnvSealed = sealed
		}
	}
	l.APIKey, l.Headers, l.HasHeaders, l.Env, l.HasEnv = "", "", false, "", false // only ever stored sealed
	if i := slices.IndexFunc(list, func(x storedLauncher) bool { return x.Name == l.Name }); i >= 0 {
		list[i] = l
	} else {
		list = append(list, l)
	}
	if err := s.storeManagedLaunchers(ctx, list); err != nil {
		return l, err
	}
	return l, nil
}

// storeManagedLaunchers commits the complete raw snapshot under models.mu and publishes it when its
// node credentials can be opened. An unrelated corrupt credential defers publication without discarding
// readable records or preventing repair. A post-commit read failure cannot clear the nodes' providers.
func (s *Service) storeManagedLaunchers(ctx context.Context, list []storedLauncher) error {
	nodes, publicationErr := s.nodeLaunchers(list)
	if err := s.putSetting(ctx, launchersManagedKey, list); err != nil {
		return err
	}
	if publicationErr != nil {
		s.logf("launchers: saved providers; node publication deferred: %v", publicationErr)
		return nil
	}
	s.pushNodeLaunchers(ctx, "", nodes)
	return nil
}

// pickMachine is the machine to ask about a launcher the machines in has have: want when it has it, else the
// assistant's, the hub's own, then the others; the first of them online. false when none is.
func (s *Service) pickMachine(ctx context.Context, has []string, want string) (string, bool) {
	devices := s.devices(ctx)
	local := ""
	for _, d := range devices {
		if d.Kind == "local" {
			local = d.ID
			break
		}
	}
	order := append([]string{want, s.settings(ctx).MachineID, local}, has...)
	for _, id := range order {
		if id == "" || !slices.Contains(has, id) {
			continue
		}
		if slices.ContainsFunc(devices, func(d Device) bool { return d.ID == id && d.Online }) {
			return id, true
		}
	}
	return "", false
}

// providerModels is GET /api/v1/model-config/providers/{id}/models: a launcher provider's /model list for Model configuration
// (docs/assistant/model-config.md "Picking a model in Model configuration"). Model configuration is not one machine's, so the list comes from a machine that has the
// launcher (pickMachine), and the answer says which. Other providers have no list to ask for: a plugin's comes with
// it, an API connection has none.
func (s *Service) providerModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	name, ok := strings.CutPrefix(r.PathValue("id"), "launcher.")
	if !ok || name == "" {
		apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-models_launchers.err_no_models"))
		return
	}
	found, everywhere := name == "claude", name == "claude"
	var has []string
	for _, v := range s.launcherViews(ctx) {
		if v.Name != name {
			continue
		}
		if v.Kind == "api" {
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-models_launchers.err_api_no_models"))
			return
		}
		found = true
		everywhere = everywhere || compat.Source(v.Source) == "persona" || len(v.Machines) == 0 // personad launch runs on every machine
		has = append(has, v.Machines...)
	}
	if !found {
		apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-models_launchers.err_no_provider"))
		return
	}
	if everywhere {
		has = has[:0]
		for _, d := range s.devices(ctx) {
			has = append(has, d.ID)
		}
	}
	machine, ok := s.pickMachine(ctx, has, r.URL.Query().Get("machine"))
	if !ok {
		apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("assistant-models_launchers.err_all_offline"))
		return
	}
	res, err := s.askLauncherModels(ctx, machine, name, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeLauncherModelsErr(w, err)
		return
	}
	apierr.WriteJSON(w, struct {
		proto.LauncherModels
		Machine string `json:"machine"`
	}{res, machine})
}

// writeLauncherModelsErr answers a failed /model list: a node too old to list them is 404 like an older hub (the apps
// leave the picker out instead of offering a retry that cannot work).
func writeLauncherModelsErr(w http.ResponseWriter, err error) {
	if strings.HasPrefix(err.Error(), "unknown method") {
		apierr.WriteCode(w, http.StatusNotFound, proto.CodeNodeOutdated, i18n.Err("assistant-models_launchers.err_node_outdated"))
		return
	}
	apierr.Call(w, err)
}
