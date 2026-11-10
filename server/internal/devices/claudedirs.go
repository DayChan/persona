package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Claude Code config directories per machine (node/claudedirs.go): the configured ones (hub.json
// machines[].claude_config_dirs) plus those the node found in running claudes, remembered here so a profile's
// sessions stay listed after the node restarts with none of its claudes running.

func claudeDirsKey(machine string) string { return "claude_dirs/" + machine }

// claudeDirsBlockedKey: the patterns of directories the user keeps out of Persona on that machine
// (proto.ClaudeDirBlocked): not looked in, not remembered, their sessions not listed.
func claudeDirsBlockedKey(machine string) string { return "claude_dirs_blocked/" + machine }

func (m *Machine) blockedClaudeDirs(ctx context.Context) []string {
	out, _ := m.readBlockedClaudeDirs(ctx)
	return out
}

// readBlockedClaudeDirs is blockedClaudeDirs with the read error, for the writers that must not act on a list they
// could not read.
func (m *Machine) readBlockedClaudeDirs(ctx context.Context) ([]string, error) {
	var out []string
	if m.d.opts.Store == nil {
		return nil, nil
	}
	_, err := m.d.opts.Store.Setting(ctx, claudeDirsBlockedKey(m.cfg.ID), &out)
	return proto.CleanClaudeDirPatterns(out), err
}

// nodeHome is the machine's home directory, for "~" in patterns ("" before the node first said).
func (m *Machine) nodeHome() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info.Home
}

// claudeDirs: the directories the node is told to look in — configured, then remembered — without blocked ones.
func (m *Machine) claudeDirs(ctx context.Context) []string {
	dirs := slices.Clone(m.cfg.ClaudeConfigDirs)
	// Where Persona's own claudes keep their sessions is looked in too, even before one of them runs.
	if d := m.cfg.ClaudeStartDir; d != "" && !slices.Contains(dirs, d) {
		dirs = append(dirs, d)
	}
	if m.d.opts.Store != nil {
		var seen []string
		if ok, err := m.d.opts.Store.Setting(ctx, claudeDirsKey(m.cfg.ID), &seen); ok && err == nil {
			for _, d := range seen {
				if !slices.Contains(dirs, d) {
					dirs = append(dirs, d)
				}
			}
		}
	}
	blocked, home := m.blockedClaudeDirs(ctx), m.nodeHome()
	return slices.DeleteFunc(dirs, func(d string) bool { return proto.ClaudeDirBlocked(d, home, blocked) })
}

// withClaudeDirs is a node.configure's params with the config directories added when they name neither claude_dirs
// nor keep_sessions: the node takes such a call's (absent) directories as none (node/node.go node.configure), so a
// per-machine switch (botmux, herdr autostart) or an extension's agent filters would make it forget them until it
// reconnects. Params that are not a JSON object go as they are.
func (m *Machine) withClaudeDirs(ctx context.Context, params any) any {
	raw, err := json.Marshal(params)
	if err != nil {
		return params
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return params
	}
	_, dirs := fields["claude_dirs"]
	_, keep := fields["keep_sessions"]
	if dirs || keep {
		return params
	}
	list, err := json.Marshal(m.claudeDirs(ctx))
	if err != nil {
		return params
	}
	if fields == nil { // null: the node reads it as {}
		fields = map[string]json.RawMessage{}
	}
	fields["claude_dirs"] = list
	return fields
}

// usageWindows is hub.json's machines[].usage_windows: the machine's node reads the agents' subscription rate-limit
// windows unless it says false (#2432).
func (m *Machine) usageWindows() bool { return m.cfg.UsageWindows == nil || *m.cfg.UsageWindows }

// configureNode hands the node its config directories, whether it lists botmux's sessions and whether its Claude starts
// stream their replies (live_replies.go), before the first session list. Older nodes do not know node.configure; they keep looking in ~/.claude only.
// hub is the hub's part (devices.connected: Codex settings, the sends it remembers; nil while it is not
// connected: it is done again when it is), then, when own, the manager's own (Options.NodeCalls: the managed launchers
// with their keys; not on a machine the hub says is another manager's now).
func (m *Machine) configureNode(ctx context.Context, peer *rpc.Peer, hub *devicewire.ConnectedReply, own bool) {
	params := map[string]any{"machine_id": m.cfg.ID, "claude_dirs": m.claudeDirs(ctx),
		"claude_dirs_blocked": m.blockedClaudeDirs(ctx), "botmux": m.botmux(ctx), "claude_start_dir": m.cfg.ClaudeStartDir,
		"herdr_autostart": m.herdrAutostart(ctx), "herdr_autoinstall": m.cfg.HerdrAutoinstall, "claude_global_hooks": m.cfg.ClaudeGlobalHooks,
		"usage_windows": m.usageWindows(), "claude_live_replies": m.liveReplies(ctx)}
	// The sessions it lists however quiet are the assistant's to say (its profile); before it said, the node keeps
	// what it has.
	if keep, ok := m.d.keptOn(m.cfg.ID); ok {
		if keep == nil {
			keep = []string{}
		}
		params["keep_sessions"] = keep
	}
	if hub != nil {
		for k, v := range hub.Configure {
			if _, ours := params[k]; !ours {
				params[k] = v
			}
		}
	}
	_ = peer.Call(ctx, "node.configure", params, nil)
	var calls []devicewire.NodeCall
	if hub != nil {
		calls = hub.Calls
	}
	if f := m.d.opts.NodeCalls; f != nil && own {
		calls = append(slices.Clone(calls), f(ctx, m.cfg.ID)...)
	}
	for _, c := range calls {
		p := c.Params
		if len(p) == 0 {
			p = json.RawMessage(`{}`)
		}
		if err := peer.Call(ctx, c.Method, p, nil); err != nil {
			m.d.logf("[%s] %s: %v", m.cfg.ID, c.Method, err)
		}
	}
}

// rememberClaudeDirs stores the directories the node found (claude.dirs event), added to what was known; blocked ones
// are not (an older node reports them all the same).
func (m *Machine) rememberClaudeDirs(params json.RawMessage) {
	var p struct {
		Dirs []string `json:"dirs"`
	}
	if json.Unmarshal(params, &p) != nil || m.d.opts.Store == nil {
		return
	}
	// On the node's read loop: bounded like state.put (nodeStoreWait); a later claude.dirs event adds what was missed.
	ctx, cancel := m.storeCtx()
	defer cancel()
	// Held across the reads and the write: a setBlockedClaudeDirs in between would have its pruning undone.
	m.claudeDirsMu.Lock()
	defer m.claudeDirsMu.Unlock()
	seen, changed, err := m.readRememberedClaudeDirs(ctx)
	// A failed read must not turn into an overwrite: the list would hold only this event's dirs.
	if err != nil {
		m.d.logf("[%s] remember claude dirs: read: %v", m.cfg.ID, err)
		return
	}
	// Nor is anything remembered without the blocked patterns, even ones that do not decode: it might be a dir the
	// user keeps out. Saving the patterns again (storeBlockedClaudeDirs does not read them) mends them.
	blocked, err := m.readBlockedClaudeDirs(ctx)
	if err != nil {
		m.d.logf("[%s] remember claude dirs: read blocked: %v", m.cfg.ID, err)
		return
	}
	home := m.nodeHome()
	for _, d := range p.Dirs {
		if d != "" && !slices.Contains(seen, d) && !proto.ClaudeDirBlocked(d, home, blocked) {
			seen = append(seen, d)
			changed = true
		}
	}
	if changed {
		if err := m.d.opts.Store.PutSetting(ctx, claudeDirsKey(m.cfg.ID), seen); err != nil {
			m.d.logf("[%s] remember claude dirs: %v", m.cfg.ID, err)
		}
	}
}

// readRememberedClaudeDirs reads the dirs remembered for the machine. A stored value that does not decode is not an
// error: no later read would do better, so the dirs that did decode stand and rewrite says to write them back over it.
func (m *Machine) readRememberedClaudeDirs(ctx context.Context) (seen []string, rewrite bool, err error) {
	_, err = m.d.opts.Store.Setting(ctx, claudeDirsKey(m.cfg.ID), &seen)
	if err == nil || !undecodable(err) {
		return seen, false, err
	}
	m.d.logf("[%s] remembered claude dirs do not decode, keeping what does: %v", m.cfg.ID, err)
	return slices.DeleteFunc(seen, func(d string) bool { return d == "" }), true, nil
}

// undecodable: err is the stored JSON not fitting, not the store failing to answer.
func undecodable(err error) bool {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &typ)
}

var errBadPattern = errors.New("bad pattern")

// setBlockedClaudeDirs stores the machine's blocked patterns, forgets the remembered directories they block, and
// tells the node: it drops them at once and sends the session list without their sessions.
func (m *Machine) setBlockedClaudeDirs(ctx context.Context, patterns []string) error {
	if m.d.opts.Store == nil {
		return errors.New("no database")
	}
	patterns = proto.CleanClaudeDirPatterns(patterns)
	for _, p := range patterns {
		if !proto.ValidClaudeDirPattern(p) {
			return fmt.Errorf("%w %q: a path or glob starting with / or ~/", errBadPattern, p)
		}
	}
	if err := m.storeBlockedClaudeDirs(ctx, patterns); err != nil {
		return err
	}
	m.d.logf("[%s] blocked claude dirs: %v", m.cfg.ID, patterns)
	m.reconfigureClaudeDirs(ctx)
	return nil
}

// storeBlockedClaudeDirs writes the patterns and prunes the remembered dirs they block, under claudeDirsMu so a
// concurrent rememberClaudeDirs cannot write back a list read before the patterns changed.
func (m *Machine) storeBlockedClaudeDirs(ctx context.Context, patterns []string) error {
	m.claudeDirsMu.Lock()
	defer m.claudeDirsMu.Unlock()
	// The patterns go in whatever the remembered list's state: blocking is the user keeping a dir out, and every use
	// of that list (claudeDirs, claudeDirsState) filters it by the patterns anyway.
	if err := m.d.opts.Store.PutSetting(ctx, claudeDirsBlockedKey(m.cfg.ID), patterns); err != nil {
		return err
	}
	seen, rewrite, err := m.readRememberedClaudeDirs(ctx)
	if err != nil {
		// Pruning waits for the next block: writing a list not read would drop the other remembered dirs.
		m.d.logf("[%s] blocked claude dirs: not pruning the remembered ones: %v", m.cfg.ID, err)
		return nil
	}
	home := m.nodeHome()
	kept := slices.DeleteFunc(slices.Clone(seen), func(d string) bool { return proto.ClaudeDirBlocked(d, home, patterns) })
	if len(kept) == len(seen) && !rewrite {
		return nil
	}
	return m.d.opts.Store.PutSetting(ctx, claudeDirsKey(m.cfg.ID), kept)
}

// reconfigureClaudeDirs hands an online node its directories and blocked patterns again and takes the session list
// it then has (the apps get it in the next machines / sessions broadcast). Offline, the next connection does it.
func (m *Machine) reconfigureClaudeDirs(ctx context.Context) {
	m.mu.Lock()
	online := m.peer != nil
	m.mu.Unlock()
	if !online {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	// botmux too: this can come between the connection and its own node.configure, and the session list that follows
	// must not be the node's first word on it (an older hub's, node/hostbotmux.go useBotmux).
	if err := m.call(ctx, "node.configure", map[string]any{"claude_dirs": m.claudeDirs(ctx),
		"claude_dirs_blocked": m.blockedClaudeDirs(ctx), "botmux": m.botmux(ctx)}, nil); err != nil {
		m.d.logf("[%s] configure claude dirs: %v", m.cfg.ID, err)
		return
	}
	var list struct {
		Sessions []proto.Session `json:"sessions"`
	}
	if err := m.call(ctx, "sessions.list", map[string]any{}, &list); err == nil {
		m.setSessions(list.Sessions)
		m.d.forward(m.cfg.ID, "sessions.changed", map[string]any{"sessions": m.sessionList()})
	}
}

// claudeDirsState is what GET /api/v1/machines/{mid}/claude-dirs answers: the machine's config directories (blocked
// ones marked) and the blocked patterns.
type claudeDirsState struct {
	Dirs    []proto.ClaudeDir `json:"dirs"`
	Blocked []string          `json:"blocked"`
	Home    string            `json:"home,omitempty"`
	// Live: the list is the node's own (every directory it knows, with counts); false: the hub's record (the
	// machine is offline or its node is older).
	Live bool `json:"live"`
}

func (m *Machine) claudeDirsState(ctx context.Context) claudeDirsState {
	blocked, home := m.blockedClaudeDirs(ctx), m.nodeHome()
	// JSON serializes a nil slice as null; the contract is a JSON array (the web settings page calls .filter on it),
	// so an empty blocklist must be [] like Dirs below.
	blockedJSON := blocked
	if blockedJSON == nil {
		blockedJSON = []string{}
	}
	st := claudeDirsState{Blocked: blockedJSON, Home: home, Dirs: []proto.ClaudeDir{}}
	var res struct {
		Dirs []proto.ClaudeDir `json:"dirs"`
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := m.call(cctx, "claude.dirs.list", map[string]any{}, &res); err == nil && res.Dirs != nil {
		st.Dirs, st.Live = res.Dirs, true
	}
	has := func(d string) bool {
		return slices.ContainsFunc(st.Dirs, func(c proto.ClaudeDir) bool { return c.Path == d })
	}

	// What the node did not list: configured ones it was not told about (blocked), and, from an older or offline
	// node, the remembered ones.
	var seen []string
	if m.d.opts.Store != nil {
		_, _ = m.d.opts.Store.Setting(ctx, claudeDirsKey(m.cfg.ID), &seen)
	}
	add := func(d, source string) {
		if d != "" && !has(d) {
			st.Dirs = append(st.Dirs, proto.ClaudeDir{Path: d, Source: source, Blocked: proto.ClaudeDirBlocked(d, home, blocked)})
		}
	}
	for _, d := range m.cfg.ClaudeConfigDirs {
		add(d, "config")
	}
	if !st.Live {
		for _, d := range seen {
			add(d, "found")
		}
	}
	// The hub says what blocks what (the node only whether): the pages show the rule and lift it.
	for i := range st.Dirs {
		d := &st.Dirs[i]
		if slices.Contains(m.cfg.ClaudeConfigDirs, d.Path) {
			d.Source = "config"
		}
		if d.Default {
			d.Blocked, d.BlockedBy = false, ""
			continue
		}
		// The node's word stands (it also matches the real path); the hub's own record has only the hub's.
		d.BlockedBy = proto.ClaudeDirBlockedBy(d.Path, home, blocked)
		d.Blocked = d.BlockedBy != "" || (st.Live && d.Blocked)
	}
	return st
}

func (d *Devices) getClaudeDirs(w http.ResponseWriter, r *http.Request, m *Machine) {
	writeJSON(w, m.claudeDirsState(r.Context()))
}

func (d *Devices) putClaudeDirsBlocked(w http.ResponseWriter, r *http.Request, m *Machine) {
	var in struct {
		Blocked []string `json:"blocked"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad body: %w", err))
		return
	}
	if err := m.setBlockedClaudeDirs(r.Context(), in.Blocked); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errBadPattern) {
			code = http.StatusBadRequest
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, m.claudeDirsState(r.Context()))
}
