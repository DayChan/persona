package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// specSum is what makes two starts the same: everything in it, Init (configuration, secrets) included.
func specSum(s proto.PluginNodeStart) string {
	s.LeaseSeconds = 0 // a renewal, not another start
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (ph *pluginHost) lease(s proto.PluginNodeStart) time.Time {
	d := time.Duration(s.LeaseSeconds) * time.Second
	if d <= 0 {
		d = pluginLeaseDefault
	}
	return ph.now().Add(d)
}

// start runs s: nothing changes when it runs already as s says (the lease is renewed); another version or other
// settings stop the one running first. A new version that does not come up within ReadyWithin is stopped, and the
// version that ran before it is started again.
func (ph *pluginHost) start(ctx context.Context, s proto.PluginNodeStart) (proto.PluginNodeState, error) {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	state := proto.PluginNodeState{ID: s.ID, Version: s.Version, Revision: s.Revision}
	if err := ph.checkIdentity(s.ID, s.StorageKey(), s.Revision); err != nil {
		return state, err
	}
	if err := checkProgram(proto.PluginNodeProgram{ID: s.ID, Version: s.Version}); err != nil {
		return state, err
	}
	if _, ok := builtinPrograms[s.ID]; ok { // never a package: it is this process (builtin.go)
		return state, fmt.Errorf("plugin %s is built into personad", s.ID)
	}
	if s.Keep != extension.KeepLease && s.Keep != extension.KeepAlways {
		return state, fmt.Errorf("plugin %s: keep %q", s.ID, s.Keep)
	}
	dir := ph.versionDir(s.StorageKey(), s.Version)
	if _, err := os.Stat(filepath.Join(dir, ".persona-digest")); err != nil {
		state.State = pluginStateMissing
		return state, nil
	}
	sum := specSum(s)
	ph.mu.Lock()
	cur := ph.running[s.ID]
	if cur != nil && cur.sum == sum && !cur.expired {
		cur.leaseUntil = ph.lease(s)
		st, why := cur.proc.Status()
		ph.mu.Unlock()
		state.State, state.Error = st, why
		return state, nil
	}
	ph.mu.Unlock()
	var before *proto.PluginNodeStart
	if cur != nil {
		if cur.spec.Version != s.Version {
			prev := cur.spec
			before = &prev
		}
		ph.stop(s.ID)
	}
	run, err := ph.run(s, sum)
	if err != nil {
		state.State, state.Error = host.StateError, err.Error()
		return state, nil
	}
	wctx, cancel := context.WithTimeout(ctx, host.ReadyWithin)
	err = run.proc.Wait(wctx)
	cancel()
	st, why := run.proc.Status()
	state.State, state.Error = st, why
	if err != nil && before != nil {
		// The new version did not come up: back to the one that ran (its package is still here).
		ph.stop(s.ID)
		if _, rerr := ph.run(*before, specSum(*before)); rerr == nil {
			ph.logf("plugin %s: %s did not come up (%v); running %s again", s.ID, s.Version, err, before.Version)
			return state, fmt.Errorf("%s 没有起来，退回了 %s：%v", s.Version, before.Version, err)
		}
	}
	return state, nil
}

// run starts s's program (nothing of the id is running).
func (ph *pluginHost) run(s proto.PluginNodeStart, sum string) (*pluginRun, error) {
	dir := ph.versionDir(s.StorageKey(), s.Version)
	rel := filepath.FromSlash(s.Command)
	if s.Command == "" || filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), "..") {
		return nil, fmt.Errorf("plugin %s: command %q", s.ID, s.Command)
	}
	if err := ph.own(s.StorageKey()); err != nil {
		return nil, err
	}
	data := ph.dataDir(s.StorageKey())
	if err := os.MkdirAll(data, 0o700); err != nil {
		return nil, err
	}
	var params extension.InitializeParams
	if len(s.Init) > 0 {
		if err := json.Unmarshal(s.Init, &params); err != nil {
			return nil, fmt.Errorf("plugin %s: initialize: %w", s.ID, err)
		}
	}
	params.Extension.ID, params.Extension.Version = s.ID, s.Version
	params.Extension.Dir, params.Extension.DataDir = dir, data
	id := s.ID
	spec := host.Spec{ID: id, Name: s.Name, Command: filepath.Join(dir, rel), Args: s.Args, Dir: dir, CleanEnv: true,
		RPC: s.RPC, HTTP: s.HTTP, Startup: true, Logf: ph.logf,
		Env: []string{"PERSONA_PLUGIN_ID=" + id, "PERSONA_PLUGIN_DIR=" + dir, "PERSONA_PLUGIN_DATA=" + data,
			"PERSONA_EXTENSION_ID=" + id, "PERSONA_EXTENSION_DIR=" + dir, "PERSONA_EXTENSION_DATA=" + data}}
	if spec.Name == "" {
		spec.Name = id
	}
	for k, v := range s.Env {
		if compat.ReservedEnv(k) {
			continue // the hub checked the manifest; this only keeps a node from being told otherwise
		}
		spec.Env = append(spec.Env, k+"="+v)
	}
	if s.HTTP {
		spec.Socket = filepath.Join(ph.dir, s.StorageKey(), "run.sock")
		spec.Env = append(spec.Env, "PERSONA_PLUGIN_SOCKET="+spec.Socket, "PERSONA_EXTENSION_SOCKET="+spec.Socket)
	}
	spec.Init = func(context.Context) (extension.InitializeParams, error) { return params, nil }
	// A program on a computer talks to its plugin's program on the hub, not to the hub itself (no hub_api).
	spec.Handle = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method + " (a program on a computer has no hub methods)"}
	}
	r := &pluginRun{spec: s, sum: sum, leaseUntil: ph.lease(s)}
	spec.OnEvent = func(event string, params json.RawMessage) {
		ph.sendRun(r, "plugin.node.event", proto.PluginNodeEvent{ID: id, Revision: s.Revision, Event: event, Params: params})
	}
	spec.OnUp = func() {
		ph.sendRun(r, "plugin.node.state", proto.PluginNodeState{ID: id, Version: s.Version, Revision: s.Revision, State: host.StateRunning})
	}
	r.proc = host.Start(context.Background(), spec) // the process's, not this call's
	ph.mu.Lock()
	ph.running[id] = r
	ph.mu.Unlock()
	return r, nil
}

func (ph *pluginHost) sendRun(r *pluginRun, event string, params any) {
	ph.mu.Lock()
	active := ph.running[r.spec.ID] == r && !r.expired
	notify := ph.notify
	ph.mu.Unlock()
	if active && notify != nil {
		notify(event, params)
	}
}

func (ph *pluginHost) send(event string, params any) {
	ph.mu.Lock()
	notify := ph.notify
	ph.mu.Unlock()
	if notify != nil {
		notify(event, params)
	}
}

func (ph *pluginHost) stop(id string) {
	ph.mu.Lock()
	r := ph.running[id]
	delete(ph.running, id)
	ph.mu.Unlock()
	if r != nil {
		r.proc.Stop()
	}
}

func (ph *pluginHost) renew(p proto.PluginNodeRenew) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	for _, id := range p.IDs {
		if r := ph.running[id]; r != nil && !r.expired {
			r.spec.LeaseSeconds = p.LeaseSeconds
			r.leaseUntil = ph.lease(r.spec)
		}
	}
}

func (ph *pluginHost) status() proto.PluginNodeStates {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	out := proto.PluginNodeStates{Programs: []proto.PluginNodeState{}}
	for id, r := range ph.running {
		st := proto.PluginNodeState{ID: id, Version: r.spec.Version, Revision: r.spec.Revision}
		if r.expired {
			st.State = pluginLeaseExpired
		} else {
			st.State, st.Error = r.proc.Status()
		}
		out.Programs = append(out.Programs, st)
	}
	slices.SortFunc(out.Programs, func(a, b proto.PluginNodeState) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (ph *pluginHost) call(ctx context.Context, p proto.PluginNodeCall) (json.RawMessage, error) {
	if _, ok := builtinPrograms[p.ID]; ok { // cua, ego, imessage: in this process, always there (builtin.go)
		ph.mu.Lock()
		lcu := ph.lcu
		ph.mu.Unlock()
		return callBuiltin(ctx, p.ID, p.Method, p.Params, lcu)
	}
	ph.mu.Lock()
	r := ph.running[p.ID]
	ph.mu.Unlock()
	if r == nil || r.expired {
		return nil, &rpc.Error{Code: pluginNotRunning, Message: "plugin " + p.ID + " is not running on this computer"}
	}
	var out json.RawMessage
	var params any
	if len(p.Params) > 0 {
		params = p.Params
	}
	if err := r.proc.Call(ctx, p.Method, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// watchLeases stops the programs kept on a lease once it runs out (the hub has not been there for that long);
// "always" ones go on. A hub that comes back starts them again (plugin.node.start).
func (ph *pluginHost) watchLeases() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		ph.expireLeases()
	}
}

func (ph *pluginHost) expireLeases() {
	now := ph.now()
	ph.mu.Lock()
	var due []*pluginRun
	for _, r := range ph.running {
		if r.spec.Keep != extension.KeepAlways && !r.expired && now.After(r.leaseUntil) {
			r.expired = true
			due = append(due, r)
		}
	}
	ph.mu.Unlock()
	for _, r := range due {
		ph.logf("plugin %s: the hub did not renew its lease; stopped until it is back", r.spec.ID)
		r.proc.Stop()
		ph.send("plugin.node.state", proto.PluginNodeState{ID: r.spec.ID, Version: r.spec.Version, Revision: r.spec.Revision, State: pluginLeaseExpired})
	}
}

// dialPlugin connects stream id to plugin's HTTP socket (a program running here with http): the hub relays the app's
// requests to it, WebSockets included (extmgr/extension_nodes.go, /api/v1/plugins/{id}/-/machines/{mid}/…).
func (n *Node) dialPlugin(ctx context.Context, ph *pluginHost, id, plugin string) error {
	ph.mu.Lock()
	r := ph.running[plugin]
	ph.mu.Unlock()
	if r == nil || r.expired || !r.spec.HTTP {
		return &rpc.Error{Code: pluginNotRunning, Message: "plugin " + plugin + " serves no HTTP on this computer"}
	}
	if n.streams == nil {
		return errors.New("plugin.node.dial: no streams on this connection")
	}
	var d net.Dialer
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := d.DialContext(dctx, "unix", r.proc.Spec().Socket)
	if err != nil {
		return &rpc.Error{Code: pluginNotRunning, Message: "plugin " + plugin + " is not answering on this computer"}
	}
	if err := n.streams.Open(id); err != nil {
		_ = c.Close()
		return err
	}
	n.streams.Start(id, c)
	return nil
}
