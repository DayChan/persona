package node

// Plugins' programs on this computer (docs/plans/plan-extensions.md "Seven", #1830). The hub sends a package
// (plugin.node.put.*), says what should run (plugin.node.start, …sync), renews the lease (…renew), and relays calls
// (…call); the programs' events go back as plugin.node.event. They belong to this process, not to one connection: a
// hub that reconnects finds them running; one gone longer than their lease finds them stopped (keep "always": still
// running). Secrets reach a program in initialize only and are never written here.
//
// They live in <persona dir>/node-plugins, never in <persona dir>/plugins: on the hub's machine the node shares the
// persona dir with the hub, whose plugins/ holds its installed plugins (installed/, data/, staging/, markets…). A
// node of #1921 kept its packages there and its sync took installed/ for a plugin no longer installed (#1933).
// What it left there is not moved nor removed: the hub sends the packages again.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"hash"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	pluginStateMissing = "missing"       // no package of that version here
	pluginLeaseExpired = "lease_expired" // the hub did not renew it in time
	// pluginLeaseDefault is a lease when the hub names none: what it renews every 10 minutes (plan "Decisions").
	pluginLeaseDefault = 30 * time.Minute
	pluginUploadTTL    = 10 * time.Minute
	// pluginNotRunning is the code of a call to a program that is not running here.
	pluginNotRunning = "not_running"
	// nodePluginsDir is where this node keeps plugins' programs, in the persona dir.
	nodePluginsDir = "node-plugins"
	// pluginOwnMark is in each plugin's directory the node made (<dir>/<id>/): sync removes only those (#1933).
	pluginOwnMark = ".persona-node"
)

// hubPluginNames are the hub's own in its plugins directory: never a node's, whatever is in them (#1933).
var hubPluginNames = []string{"installed", "data", "staging", "markets", "node-packages"}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// pluginHosts are this process's plugin programs, one set per node directory (tests run several nodes).
var pluginHosts = struct {
	sync.Mutex
	byDir map[string]*pluginHost
}{byDir: map[string]*pluginHost{}}

// plugins is this node's set of plugin programs; the connection n is is the one their events now go to.
func (n *Node) plugins() *pluginHost {
	pluginHosts.Lock()
	defer pluginHosts.Unlock()
	ph := pluginHosts.byDir[n.dir]
	if ph == nil {
		ph = &pluginHost{dir: filepath.Join(n.dir, nodePluginsDir), running: map[string]*pluginRun{}, uploads: map[string]*pluginUpload{},
			logf: n.logger.Printf, now: time.Now}
		pluginHosts.byDir[n.dir] = ph
		go ph.watchLeases()
	}
	if peer := n.peer; peer != nil { // the hub's latest connection is where the programs' events go
		ph.mu.Lock()
		ph.notify = func(event string, params any) { _ = peer.Notify(event, params) }
		ph.lcu = n.lcuComputerCall
		ph.mu.Unlock()
	}
	return ph
}

type pluginHost struct {
	dir  string // <persona dir>/node-plugins: <id>/<version>/ the packages, <id>/data/ the programs' own, <id>/.persona-node
	logf func(string, ...any)
	now  func() time.Time

	lifecycle      sync.Mutex // serializes identity, package replacement, start and destructive sync
	identities     map[string]proto.PluginNodeIdentity
	identityLoaded bool
	mu             sync.Mutex
	running        map[string]*pluginRun
	uploads        map[string]*pluginUpload
	notify         func(event string, params any)
	lcu            lcuCaller // the latest connection's, so LCU's confirmations reach the hub that is there now
}

type pluginRun struct {
	spec       proto.PluginNodeStart
	sum        string // of spec, Init included: the same start again changes nothing
	proc       *host.Proc
	leaseUntil time.Time
	expired    bool
}

type pluginUpload struct {
	prog   proto.PluginNodeProgram
	size   int64
	file   *os.File
	sum    hash.Hash
	offset int64
	at     time.Time
}

func (n *Node) handlePlugins(ctx context.Context, method string, params json.RawMessage) (any, error) {
	ph := n.plugins()
	switch method {
	case "plugin.node.identity":
		var p proto.PluginNodeIdentityRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if err := ph.applyIdentities(p.Slots); err != nil {
			return nil, err
		}
		ph.lifecycle.Lock()
		reply := proto.PluginNodeIdentityReply{Supported: true, Revision: ph.identityRevision()}
		for _, slot := range ph.identities {
			reply.Slots = append(reply.Slots, slot)
		}
		ph.lifecycle.Unlock()
		return reply, nil
	case "plugin.node.have":
		var p proto.PluginNodeProgram
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return proto.PluginNodeHave{Have: ph.has(p)}, nil
	case "plugin.node.put.begin":
		var p proto.PluginNodePutBegin
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		id, err := ph.begin(p)
		return map[string]string{"upload_id": id}, err
	case "plugin.node.put.chunk":
		var p proto.PluginNodePutChunk
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, ph.chunk(p)
	case "plugin.node.put.commit":
		var p struct {
			UploadID string `json:"upload_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, ph.commit(p.UploadID)
	case "plugin.node.start":
		var p proto.PluginNodeStart
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return ph.start(ctx, p)
	case "plugin.node.stop":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		ph.stop(p.ID)
		return map[string]bool{"ok": true}, nil
	case "plugin.node.sync":
		var p proto.PluginNodeSync
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return ph.syncRequest(p)
	case "plugin.node.renew":
		var p proto.PluginNodeRenew
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		ph.renew(p)
		return map[string]bool{"ok": true}, nil
	case "plugin.node.status":
		return ph.status(), nil
	case "plugin.node.dial":
		var p struct {
			ID     string `json:"id"` // the stream (internal/streams), the hub's
			Plugin string `json:"plugin"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
			return nil, errors.New("plugin.node.dial: need id and plugin")
		}
		return map[string]any{}, n.dialPlugin(ctx, ph, p.ID, p.Plugin)
	case "plugin.node.call":
		var p proto.PluginNodeCall
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return ph.call(ctx, p)
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

func (ph *pluginHost) loadIdentities() error {
	if ph.identityLoaded {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(ph.dir, ".identities.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ids := map[string]proto.PluginNodeIdentity{}
	if err == nil {
		if err = json.Unmarshal(b, &ids); err != nil {
			return err
		}
	}
	ph.identities, ph.identityLoaded = ids, true
	return nil
}

// applyIdentities runs only over the node's authenticated manager RPC channel.
// Commit the slot binding before starting a successor; stale revisions cannot reopen a retired ID.
func (ph *pluginHost) applyIdentities(slots []proto.PluginNodeIdentity) error {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	if err := ph.loadIdentities(); err != nil {
		return err
	}
	if len(slots) == 0 {
		return nil
	}
	next := map[string]proto.PluginNodeIdentity{}
	for k, v := range ph.identities {
		next[k] = v
	}
	seen := map[string]bool{}
	reset := map[string]bool{}
	for _, s := range slots {
		if s.Retired || !extension.ValidID(s.ID) || !extension.ValidID(s.StorageID) || s.Revision <= 0 || seen[s.StorageID] {
			return errors.New("invalid extension identity slot")
		}
		seen[s.StorageID] = true
		if old, ok := next[s.StorageID]; ok && (s.Revision < old.Revision || s.Revision == old.Revision && s.ID != old.ID) {
			return errors.New("stale extension identity revision")
		}
		if _, err := os.Lstat(filepath.Join(ph.dir, s.StorageID)); err == nil && !ph.owns(s.StorageID) {
			return errors.New("extension slot is not owned by this node")
		}
		for key, old := range next {
			if key != s.StorageID && old.ID != s.ID {
				continue
			}
			if s.Revision < old.Revision || s.InstalledAt < old.InstalledAt {
				return errors.New("stale extension installation")
			}
			if key != s.StorageID && s.InstalledAt <= old.InstalledAt {
				return errors.New("extension identity belongs to another slot")
			}
			if old.Retired && key == s.StorageID && s.InstalledAt <= old.InstalledAt {
				return errors.New("retired extension installation")
			}
			if s.InstalledAt > old.InstalledAt && s.Revision > old.Revision {
				reset[key] = true // Explicit uninstall/reinstall, never an identity upgrade.
				if key != s.StorageID {
					old.Retired = true
					next[key] = old
				}
			}
		}
		next[s.StorageID] = s
	}
	public := map[string]string{}
	for slot, s := range next {
		if s.Retired {
			continue
		}
		if other, ok := public[s.ID]; ok && other != slot {
			return errors.New("extension identity belongs to another slot")
		}
		public[s.ID] = slot
	}
	ph.mu.Lock()
	var resetRuns []string
	for id, r := range ph.running {
		if reset[r.spec.StorageKey()] {
			resetRuns = append(resetRuns, id)
		}
	}
	ph.mu.Unlock()
	for _, id := range resetRuns {
		ph.stop(id)
	}
	for slot := range reset {
		if ph.owns(slot) {
			if err := os.RemoveAll(filepath.Join(ph.dir, slot)); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(ph.dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(ph.dir, ".identities-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(ph.dir, ".identities.json")); err != nil {
		return err
	}
	ph.identities = next
	ph.mu.Lock()
	var retire []string
	for id, r := range ph.running {
		if s, ok := next[r.spec.StorageKey()]; ok && (s.ID != id || r.spec.Revision != s.Revision) {
			retire = append(retire, id)
		}
	}
	ph.mu.Unlock()
	for _, id := range retire {
		ph.stop(id)
	}
	return nil
}

// checkIdentity is called with lifecycle held. Empty fields retain the same-ID wire protocol.
func (ph *pluginHost) checkIdentity(id, slot string, revision int64) error {
	if err := ph.loadIdentities(); err != nil {
		return err
	}
	if !extension.ValidID(slot) {
		return errors.New("invalid extension storage slot")
	}
	for key, s := range ph.identities {
		if !s.Retired && s.ID == id && key != slot {
			return errors.New("extension identity belongs to another slot")
		}
	}
	if s, ok := ph.identities[slot]; ok {
		if s.Retired || s.ID != id || s.Revision != revision {
			return errors.New("stale extension identity revision")
		}
	} else if slot != id || revision != 0 {
		return errors.New("extension identity has not been committed on this node")
	}
	return nil
}

func checkProgram(p proto.PluginNodeProgram) error {
	if !extension.ValidID(p.ID) {
		return fmt.Errorf("plugin id %q", p.ID)
	}
	if _, err := extension.ParseVersion(p.Version); err != nil {
		return fmt.Errorf("plugin %s version %q", p.ID, p.Version)
	}
	if p.Digest != "" && !digestPattern.MatchString(p.Digest) {
		return fmt.Errorf("plugin %s digest", p.ID)
	}
	return nil
}

func (ph *pluginHost) versionDir(id, version string) string {
	return filepath.Join(ph.dir, id, version)
}
func (ph *pluginHost) dataDir(id string) string { return filepath.Join(ph.dir, id, "data") }

// own makes id's directory, marked as this node's (pluginOwnMark): the only kind sync removes.
func (ph *pluginHost) own(id string) error {
	at := filepath.Join(ph.dir, id)
	if st, err := os.Lstat(at); err == nil && (!st.IsDir() || !ph.owns(id)) {
		return errors.New("extension slot is not owned by this node")
	}
	if err := os.MkdirAll(at, 0o700); err != nil {
		return err
	}
	mark := filepath.Join(at, pluginOwnMark)
	if _, err := os.Stat(mark); err == nil {
		return nil
	}
	return os.WriteFile(mark, []byte("plugin "+id+": a program the hub runs on this computer (node/plugins.go)\n"), 0o600)
}

// owns says whether id's directory is one this node made (not the hub's, nor anything else that is there).
func (ph *pluginHost) owns(id string) bool {
	if slices.Contains(hubPluginNames, id) {
		return false
	}
	base, err := os.Lstat(filepath.Join(ph.dir, id))
	if err != nil || !base.IsDir() {
		return false
	}
	st, err := os.Lstat(filepath.Join(ph.dir, id, pluginOwnMark))
	return err == nil && st.Mode().IsRegular()
}

// has: the package of that version is here, and is the one with that digest.
func (ph *pluginHost) has(p proto.PluginNodeProgram) bool {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	if ph.checkIdentity(p.ID, p.StorageKey(), p.Revision) != nil {
		return false
	}
	if checkProgram(p) != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(ph.versionDir(p.StorageKey(), p.Version), ".persona-digest"))
	return err == nil && strings.TrimSpace(string(b)) == p.Digest
}

func (ph *pluginHost) begin(p proto.PluginNodePutBegin) (string, error) {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	if err := ph.checkIdentity(p.ID, p.StorageKey(), p.Revision); err != nil {
		return "", err
	}
	if err := checkProgram(p.PluginNodeProgram); err != nil {
		return "", err
	}
	if p.Digest == "" || p.Size <= 0 || p.Size > extension.PackageMost {
		return "", fmt.Errorf("plugin %s: a package of %d bytes with a digest", p.ID, p.Size)
	}
	if err := os.MkdirAll(filepath.Join(ph.dir, ".uploads"), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Join(ph.dir, ".uploads"), p.ID+"-*.tgz")
	if err != nil {
		return "", err
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	ph.mu.Lock()
	defer ph.mu.Unlock()
	ph.dropStaleLocked()
	ph.uploads[id] = &pluginUpload{prog: p.PluginNodeProgram, size: p.Size, file: f, sum: sha256.New(), at: ph.now()}
	return id, nil
}

// dropStaleLocked forgets uploads nobody went on with (a hub that went away halfway).
func (ph *pluginHost) dropStaleLocked() {
	for id, u := range ph.uploads {
		if ph.now().Sub(u.at) > pluginUploadTTL {
			u.file.Close()
			_ = os.Remove(u.file.Name())
			delete(ph.uploads, id)
		}
	}
}

func (ph *pluginHost) chunk(p proto.PluginNodePutChunk) error {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	u := ph.uploads[p.UploadID]
	switch {
	case u == nil:
		return errors.New("no such upload")
	case p.Offset != u.offset:
		return fmt.Errorf("chunk at %d, expected %d", p.Offset, u.offset)
	case u.offset+int64(len(p.Data)) > u.size:
		return errors.New("more than the package's size")
	}
	if _, err := u.file.Write(p.Data); err != nil {
		return err
	}
	u.sum.Write(p.Data)
	u.offset += int64(len(p.Data))
	u.at = ph.now()
	return nil
}

func (ph *pluginHost) commit(uploadID string) error {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	ph.mu.Lock()
	u := ph.uploads[uploadID]
	delete(ph.uploads, uploadID)
	ph.mu.Unlock()
	if u == nil {
		return errors.New("no such upload")
	}
	defer os.Remove(u.file.Name())
	u.file.Close()
	if err := ph.checkIdentity(u.prog.ID, u.prog.StorageKey(), u.prog.Revision); err != nil {
		return err
	}
	if u.offset != u.size {
		return fmt.Errorf("plugin %s: %d of %d bytes", u.prog.ID, u.offset, u.size)
	}
	if got := hex.EncodeToString(u.sum.Sum(nil)); got != u.prog.Digest {
		return fmt.Errorf("plugin %s: the package's sha256 is %s, not %s", u.prog.ID, got, u.prog.Digest)
	}
	if err := ph.own(u.prog.StorageKey()); err != nil {
		return err
	}
	dir := ph.versionDir(u.prog.StorageKey(), u.prog.Version)
	tmp := dir + ".unpacking"
	_ = os.RemoveAll(tmp)
	if err := extension.UnpackNodePackage(u.file.Name(), tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("plugin %s: %w", u.prog.ID, err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".persona-digest"), []byte(u.prog.Digest+"\n"), 0o644); err != nil {
		return err
	}
	ph.mu.Lock()
	r := ph.running[u.prog.ID]
	busy := r != nil && r.spec.Version == u.prog.Version
	ph.mu.Unlock()
	if busy { // the same version with other bytes (a package rebuilt): stop it before its files change under it
		ph.stop(u.prog.ID)
	}
	_ = os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}

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

// sync stops what should not run here and removes its packages: of a plugin still to run, the version to run and the
// one before it (what an upgrade falls back to); of one not installed any more, its data too. It removes only what this
// node made (owns): a directory without its mark is left alone, whatever its name (#1933).
func (ph *pluginHost) identityRevision() int64 {
	var revision int64
	for _, slot := range ph.identities {
		revision = max(revision, slot.Revision)
	}
	return revision
}

func (ph *pluginHost) validateSync(p proto.PluginNodeSync) error {
	if err := ph.loadIdentities(); err != nil {
		return err
	}
	if len(ph.identities) > 0 && (!p.Identity || p.Revision < ph.identityRevision()) {
		return errors.New("stale or legacy extension identity sync")
	}
	for slot := range ph.identities {
		if slices.Contains(p.Installed, slot) && !slices.ContainsFunc(p.InstalledSlots, func(s proto.PluginNodeIdentity) bool { return s.StorageID == slot }) {
			return errors.New("committed extension slot missing from identity-aware sync")
		}
	}
	for _, s := range p.InstalledSlots {
		if err := ph.checkIdentity(s.ID, s.StorageID, s.Revision); err != nil {
			return err
		}
	}
	for _, prog := range p.Programs {
		if err := checkProgram(prog); err != nil {
			return err
		}
		if err := ph.checkIdentity(prog.ID, prog.StorageKey(), prog.Revision); err != nil {
			return err
		}
	}
	return nil
}

func (ph *pluginHost) sync(p proto.PluginNodeSync) proto.PluginNodeStates {
	states, _ := ph.syncRequest(p)
	return states
}

func (ph *pluginHost) syncRequest(p proto.PluginNodeSync) (proto.PluginNodeStates, error) {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	if err := ph.validateSync(p); err != nil {
		return ph.status(), err
	}
	want := map[string]string{}
	for _, prog := range p.Programs {
		if checkProgram(prog) == nil {
			want[prog.StorageKey()] = prog.Version
		}
	}
	ph.mu.Lock()
	var stale []string
	for id, r := range ph.running {
		if want[r.spec.StorageKey()] != r.spec.Version {
			stale = append(stale, id)
		}
	}
	ph.mu.Unlock()
	for _, id := range stale {
		ph.stop(id)
	}
	entries, _ := os.ReadDir(ph.dir)
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !extension.ValidID(id) || !ph.owns(id) {
			continue
		}
		version, wanted := want[id]
		if !wanted {
			if slices.ContainsFunc(p.InstalledSlots, func(s proto.PluginNodeIdentity) bool { return s.StorageID == id }) {
				// Disabled migrated installations retain rollback packages as well as data.
				continue
			}
			if slices.Contains(p.Installed, id) {
				ph.prune(id, "") // Legacy same-ID behavior retains data, not packages.
			} else {
				_ = os.RemoveAll(filepath.Join(ph.dir, id))
			}
			continue
		}
		ph.prune(id, version)
	}
	return ph.status(), nil
}

// prune removes id's packages but version and the newest one before it (all of them when version is "").
func (ph *pluginHost) prune(id, version string) {
	type older struct {
		v    extension.Version
		name string
	}
	keep, _ := extension.ParseVersion(version)
	var before []older
	entries, _ := os.ReadDir(filepath.Join(ph.dir, id))
	for _, e := range entries {
		v, err := extension.ParseVersion(e.Name())
		if !e.IsDir() || err != nil || e.Name() == version {
			continue
		}
		if version != "" && v.Compare(keep) < 0 {
			before = append(before, older{v, e.Name()})
			continue
		}
		_ = os.RemoveAll(filepath.Join(ph.dir, id, e.Name()))
	}
	slices.SortFunc(before, func(a, b older) int { return a.v.Compare(b.v) })
	for i, o := range before {
		if i < len(before)-1 { // the newest of the older ones stays
			_ = os.RemoveAll(filepath.Join(ph.dir, id, o.name))
		}
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
