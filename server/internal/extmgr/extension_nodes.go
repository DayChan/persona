package extmgr

// Plugins' programs on computers (docs/plans/plan-extensions.md "7", #1830): the hub keeps what should run where (the
// plugin's switch, configuration and the machines picked in its node.machines field) and makes each machine so —
// when it connects, when the plugin changes, and by renewing the leases every 10 minutes. Nothing is queued for a
// machine that is away: it is brought in line when it is back. The node runs them (node/plugins.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	// extensionLease is how long a machine keeps a plugin's program running without hearing from the hub
	// (plan "Decisions"), renewed every extensionLeaseEvery.
	extensionLease      = 30 * time.Minute
	extensionLeaseEvery = 10 * time.Minute
	// extensionNodeOutdated is a machine whose node cannot run plugins' programs (older than #1830).
	extensionNodeOutdated = "outdated"
	extensionNodeOffline  = "offline"
)

// extensionNodes is what the hub knows of plugins' programs on machines.
type extensionNodes struct {
	mu sync.Mutex
	// states: plugin id → machine id → how it is there (what the apps show).
	states map[string]map[string]proto.PluginNodeState
	// packages: "<id>/<version>/<platform>" → the package file sent to machines of that platform.
	packages map[string]nodePackage
	// syncing: one sync at a time per machine.
	syncing map[string]*sync.Mutex
}

type nodePackage struct {
	file, digest string
	size         int64
}

func (pn *extensionNodes) lock(machine string) *sync.Mutex {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if pn.syncing == nil {
		pn.syncing = map[string]*sync.Mutex{}
	}
	l := pn.syncing[machine]
	if l == nil {
		l = &sync.Mutex{}
		pn.syncing[machine] = l
	}
	return l
}

// keepNodeState records st of plugin id's program on machine while id is installed in version (in any version when
// version is ""). That is decided under the lock drop takes once the registry has changed, so a sync that read the
// registry before an upgrade or an uninstall, or a machine's late word, either wrote before drop (which removes it) or
// is not kept: no state of a version that is gone stays behind.
func (ps *extensions) keepNodeState(id, version, machine string, st proto.PluginNodeState) {
	pn := &ps.nodes
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if !ps.installedAt(id, version) || !ps.nodeIdentityCurrent(id, st.Revision) {
		return
	}
	pn.set(id, machine, st)
}

// installedAt says whether extension id is installed, in version unless that is "". Asked with nodes.mu held: the
// registry never takes that lock.
func (ps *extensions) installedAt(id, version string) bool {
	p := ps.reg.GetExact(id)
	return p != nil && (version == "" || p.Record.Version == version)
}

// set records st; pn.mu is held.
func (pn *extensionNodes) set(pluginID, machine string, st proto.PluginNodeState) {
	if pn.states == nil {
		pn.states = map[string]map[string]proto.PluginNodeState{}
	}
	if pn.states[pluginID] == nil {
		pn.states[pluginID] = map[string]proto.PluginNodeState{}
	}
	pn.states[pluginID][machine] = st
}

func (pn *extensionNodes) forget(pluginID string, keep []string) {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	for m := range pn.states[pluginID] {
		if !slices.Contains(keep, m) {
			delete(pn.states[pluginID], m)
		}
	}
}

// drop forgets what the hub keeps of plugin id's program on computers that is no longer installed: every package but
// keepVersion's (cached and on disk under root/node-packages), and, when id is gone (keepVersion ""), its machine
// states. Package files are matched as exactly <id>-<version>-<platform>.tgz, so an id that this one prefixes keeps
// its own.
func (pn *extensionNodes) drop(root, id, keepVersion string) {
	pn.mu.Lock()
	if keepVersion == "" {
		delete(pn.states, id)
	}
	for key, pkg := range pn.packages {
		rest, ok := strings.CutPrefix(key, id+"/")
		if !ok {
			continue
		}
		if version, _, _ := strings.Cut(rest, "/"); version != keepVersion {
			delete(pn.packages, key)
			_ = os.Remove(pkg.file)
		}
	}
	pn.mu.Unlock()
	// Files made before the hub last started are not in the cache.
	dir := filepath.Join(root, "node-packages")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if version, ok := nodePackageVersion(e.Name(), id); ok && version != keepVersion {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// nodePackageVersion is the version of a package file named <id>-<version>-<os>-<arch>.tgz (nodePackage) of plugin
// id. An id never has a dot and a version has one before any dash, so another id that id prefixes never matches.
func nodePackageVersion(name, id string) (string, bool) {
	rest, ok := strings.CutPrefix(name, id+"-")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, ".tgz")
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, "-")
	if len(parts) < 3 {
		return "", false
	}
	version := strings.Join(parts[:len(parts)-2], "-")
	if _, err := extension.ParseVersion(version); err != nil {
		return "", false
	}
	return version, true
}

// errNodeVersionGone: the version a sync read is not installed any more (it was upgraded or uninstalled meanwhile).
// The sync after the change brings the machine in line, so it is not a plugin's error to log.
var errNodeVersionGone = errors.New("this version of the extension is not installed any more")

// ExtensionNodeView is a plugin's program on one machine, as the apps show it.
type ExtensionNodeView struct {
	MachineID string `json:"machine_id"`
	Version   string `json:"version,omitempty"`
	State     string `json:"state"` // running | starting | stopped | error | missing | lease_expired | offline | outdated
	Error     string `json:"error,omitempty"`
}

// views are plugin id's programs on machines, by machine id.
func (pn *extensionNodes) views(pluginID string) []ExtensionNodeView {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	var out []ExtensionNodeView
	for m, st := range pn.states[pluginID] {
		out = append(out, ExtensionNodeView{MachineID: m, Version: st.Version, State: st.State, Error: st.Error})
	}
	slices.SortFunc(out, func(a, b ExtensionNodeView) int { return strings.Compare(a.MachineID, b.MachineID) })
	return out
}

// nodeMachines are the machines p's program on computers is switched on for (its node.machines field).
func (ps *extensions) nodeMachines(p *extension.Extension) []string {
	if p == nil || p.Manifest.NodeProgram() == nil {
		return nil
	}
	var cfg map[string]json.RawMessage
	_ = json.Unmarshal(ps.reg.Config(p), &cfg)
	var ids []string
	_ = json.Unmarshal(cfg[p.Manifest.Node.Machines], &ids)
	return ids
}

// nodeRunsOn says whether p's program should run on m now: the plugin is on and set up, m is picked, and the program
// has a build for m's platform.
func (ps *extensions) nodeRunsOn(ctx context.Context, p *extension.Extension, m *Machine) bool {
	if p.Manifest.NodeProgram() == nil || !p.Record.Enabled || p.Problem != "" || len(ps.reg.Missing(ctx, p)) > 0 {
		return false
	}
	if !slices.Contains(ps.nodeMachines(p), m.cfg.ID) {
		return false
	}
	m.mu.Lock()
	goos, goarch := m.info.OS, m.info.Arch
	m.mu.Unlock()
	return p.Manifest.NodeRuns(goos, goarch)
}

// nodePackage is p's package for platform, made once per version (the same bytes each time: NodePackage).
func (ps *extensions) nodePackage(p *extension.Extension, platform string) (nodePackage, error) {
	key := p.Record.ID + "/" + p.Record.Version + "/" + platform
	pn := &ps.nodes
	pn.mu.Lock()
	pkg, ok := pn.packages[key]
	pn.mu.Unlock()
	if ok {
		if _, err := os.Stat(pkg.file); err == nil {
			return pkg, nil
		}
	}
	dir := filepath.Join(ps.root, "node-packages")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nodePackage{}, err
	}
	file := filepath.Join(dir, p.Record.ID+"-"+p.Record.Version+"-"+platform+".tgz")
	digest, size, err := extension.NodePackageFile(p.Manifest, p.Dir, platform, file)
	if err != nil {
		return nodePackage{}, err
	}
	pkg = nodePackage{file: file, digest: digest, size: size}
	pn.mu.Lock()
	defer pn.mu.Unlock()
	// Kept only while this version is installed, decided under the lock drop takes (keepNodeState): a sync that read
	// the registry before an upgrade or an uninstall leaves no package of the version that is gone.
	if !ps.installedAt(p.Record.ID, p.Record.Version) {
		_ = os.Remove(file)
		return nodePackage{}, errNodeVersionGone
	}
	if pn.packages == nil {
		pn.packages = map[string]nodePackage{}
	}
	pn.packages[key] = pkg
	return pkg, nil
}

// putNodePackage sends pkg to m unless it has it.
func putNodePackage(ctx context.Context, m *Machine, prog proto.PluginNodeProgram, pkg nodePackage) error {
	var have proto.PluginNodeHave
	if err := m.call(ctx, "plugin.node.have", prog, &have); err != nil {
		return err
	}
	if have.Have {
		return nil
	}
	f, err := os.Open(pkg.file)
	if err != nil {
		return err
	}
	defer f.Close()
	var begun struct {
		UploadID string `json:"upload_id"`
	}
	if err := m.call(ctx, "plugin.node.put.begin", proto.PluginNodePutBegin{PluginNodeProgram: prog, Size: pkg.size}, &begun); err != nil {
		return err
	}
	buf := make([]byte, proto.UploadChunkBytes)
	var off int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if err := m.call(ctx, "plugin.node.put.chunk", proto.PluginNodePutChunk{UploadID: begun.UploadID, Offset: off, Data: buf[:n]}, nil); err != nil {
				return err
			}
			off += int64(n)
		}
		if rerr != nil {
			break
		}
	}
	return m.call(ctx, "plugin.node.put.commit", map[string]string{"upload_id": begun.UploadID}, nil)
}

// nodeInit is the initialize p's program on a computer gets: the configuration, and only the secrets its manifest
// sends to computers (node.secrets); no hub token (no hub_api on computers, plan "Decisions").
func (ps *extensions) nodeInit(ctx context.Context, p *extension.Extension) (json.RawMessage, error) {
	all, err := ps.reg.Secrets(ctx, p.Record.ID)
	if err != nil {
		return nil, err
	}
	secrets := map[string]string{}
	for _, name := range p.Manifest.Node.Secrets {
		if v, ok := all[name]; ok {
			secrets[name] = v
		}
	}
	params := extension.InitializeParams{Hub: extension.HubInfo{ExtensionPoints: extension.PointVersions(), Capabilities: extension.HubCapabilities()},
		Extension: extension.ExtensionInfo{ID: p.Record.ID, Version: p.Record.Version}, Config: ps.reg.Config(p), Secrets: secrets,
		Active: map[string][]string{}}
	if ps.h != nil {
		params.Hub.Build = ps.h.build.Number
		params.Hub.Timezone = ps.h.userTZName()
	}
	return json.Marshal(params)
}

// Identity timestamps must match Postgres's microsecond precision before the
// first node bind, or a database reload appears to move the revision backwards.
func extensionIdentityTimestamp(stamp time.Time) int64 {
	return stamp.Truncate(time.Microsecond).UnixNano()
}

func extensionNodeRevision(p *extension.Extension) int64 {
	if p.Record.StorageID == "" {
		return 0
	}
	return extensionIdentityTimestamp(p.Record.UpdatedAt)
}

func extensionNodeIdentity(p *extension.Extension) proto.PluginNodeIdentity {
	return proto.PluginNodeIdentity{ID: p.Record.ID, StorageID: p.Record.StorageKey(), Revision: extensionNodeRevision(p), InstalledAt: extensionIdentityTimestamp(p.Record.InstalledAt)}
}

func (ps *extensions) nodeIdentityCurrent(id string, revision int64) bool {
	p := ps.reg.GetExact(id)
	return p != nil && (extensionNodeRevision(p) == revision || p.Record.StorageID == "" && revision == extensionIdentityTimestamp(p.Record.UpdatedAt))
}

// checkRenameNodeCapabilities runs before the source guard takes its market lock.
// A configured legacy/offline node must not leave the old run active after the
// public identity commits, or require a destructive old-wire sync to retire it.
func (h *Manager) checkRenameNodeCapabilities(ctx context.Context, p *extension.Extension) error {
	if p == nil {
		return i18n.Err("extension-identity.invalid")
	}
	if !p.Record.Enabled || p.Manifest.NodeProgram() == nil {
		return nil
	}
	for _, id := range h.extensions.nodeMachines(p) {
		m := h.machineByID(id)
		if m == nil || !m.isOnline() {
			return i18n.Err("extension-identity.node_required")
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var capability proto.PluginNodeIdentityReply
		err := m.call(probeCtx, "plugin.node.identity", proto.PluginNodeIdentityRequest{}, &capability)
		cancel()
		if err != nil || !capability.Supported {
			return i18n.Err("extension-identity.node_required")
		}
	}
	return nil
}

func commitNodeIdentities(ctx context.Context, m *Machine, slots []proto.PluginNodeIdentity) error {
	var supported proto.PluginNodeIdentityReply
	if err := m.call(ctx, "plugin.node.identity", proto.PluginNodeIdentityRequest{}, &supported); err != nil {
		return err
	}
	if !supported.Supported {
		return errors.New("node does not support extension identity slots")
	}
	return m.call(ctx, "plugin.node.identity", proto.PluginNodeIdentityRequest{Slots: slots}, &supported)
}

func (ps *extensions) nodeSnapshotCurrent(all []*extension.Extension) bool {
	current := ps.reg.List()
	if len(current) != len(all) {
		return false
	}
	for _, p := range all {
		cur := ps.reg.SnapshotExact(p.Record.ID)
		if cur == nil || cur.Record.Version != p.Record.Version || cur.Record.StorageKey() != p.Record.StorageKey() || !cur.Record.UpdatedAt.Equal(p.Record.UpdatedAt) || !cur.Record.InstalledAt.Equal(p.Record.InstalledAt) {
			return false
		}
	}
	return true
}

// syncExtensionNodes makes m run what it should of plugins' programs, and nothing else.
func (h *Manager) syncExtensionNodes(ctx context.Context, m *Machine) {
	ps := h.extensions
	if ps == nil || ps.reg == nil {
		return
	}
	l := ps.nodes.lock(m.cfg.ID)
	l.Lock()
	defer l.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute) // a package of 200 MB over a slow link
	defer cancel()
	var programs []proto.PluginNodeProgram
	var installed []string
	var slots []proto.PluginNodeIdentity
	all := ps.reg.Snapshots()
	online := m.isOnline()
	var capability proto.PluginNodeIdentityReply
	if online {
		if err := m.call(ctx, "plugin.node.identity", proto.PluginNodeIdentityRequest{}, &capability); err != nil && !nodeOutdated(err) {
			return
		}
	}
	identityAware := capability.Supported
	syncRevision := capability.Revision
	for i, p := range all {
		if capability.Supported && p.Record.Source == extension.SourceInstalled && p.Record.StorageID == "" && slices.ContainsFunc(capability.Slots, func(slot proto.PluginNodeIdentity) bool {
			return slot.ID == p.Record.ID || slot.StorageID == p.Record.StorageKey()
		}) {
			// Fresh reinstall after a previous rename: only the capable node needs a new generation binding.
			// Never mutate the registry or send these fields to a legacy node.
			copy := *p
			copy.Record.StorageID = p.Record.StorageKey()
			p, all[i] = &copy, &copy
		}
		installed = append(installed, p.Record.StorageKey())
		if p.Record.StorageID != "" {
			slots = append(slots, extensionNodeIdentity(p))
			syncRevision = max(syncRevision, extensionNodeRevision(p))
		}
	}
	if !ps.nodeSnapshotCurrent(all) {
		return
	}
	if online && len(slots) > 0 {
		if err := commitNodeIdentities(ctx, m, slots); err != nil {
			for _, p := range all {
				if p.Record.StorageID != "" {
					ps.keepNodeState(p.Record.ID, p.Record.Version, m.cfg.ID, proto.PluginNodeState{ID: p.Record.ID, Version: p.Record.Version, Revision: extensionNodeRevision(p), State: extensionNodeOutdated, Error: i18n.T(h.contextLang(ctx), "extension-identity.node_required")})
				}
			}
			return // Never send a destructive old-node sync or create a second data directory.
		}
	}
	for _, p := range all {
		if p.Manifest.NodeProgram() == nil { // none, or built in (no package to keep there)
			continue
		}
		if prog := h.syncExtensionNode(ctx, p, m, online); prog != nil {
			programs = append(programs, *prog)
		}
	}
	if !online {
		return
	}
	if programs == nil {
		programs = []proto.PluginNodeProgram{}
	}
	if !ps.nodeSnapshotCurrent(all) {
		return
	}
	var states proto.PluginNodeStates
	if err := m.call(ctx, "plugin.node.sync", proto.PluginNodeSync{Revision: syncRevision, Programs: programs, Installed: installed, InstalledSlots: slots, Identity: identityAware}, &states); err != nil &&
		!nodeOutdated(err) {
		h.logf("[%s] plugins' programs: %v", m.cfg.ID, err)
	}
}

// syncExtensionNode brings plugin p's program on m in line with p as the sync read it from the registry, and records
// its state there; the program to keep on m, nil for none. p may be out of date by then (an upgrade or an uninstall
// came in the meantime): keepNodeState and nodePackage then keep nothing of it.
func (h *Manager) syncExtensionNode(ctx context.Context, p *extension.Extension, m *Machine, online bool) *proto.PluginNodeProgram {
	ps := h.extensions
	id := p.Record.ID
	if !ps.nodeRunsOn(ctx, p, m) {
		ps.nodes.forget(id, slices.DeleteFunc(ps.nodeMachines(p), func(mid string) bool { return mid == m.cfg.ID }))
		return nil
	}
	if !online {
		ps.keepNodeState(id, p.Record.Version, m.cfg.ID, proto.PluginNodeState{ID: id, Version: p.Record.Version, Revision: extensionNodeRevision(p), State: extensionNodeOffline})
		return nil
	}
	st, prog := h.startExtensionNode(ctx, p, m)
	ps.keepNodeState(id, p.Record.Version, m.cfg.ID, st)
	return prog
}

// startExtensionNode sends m p's package (unless it has it) and starts it; the program to keep there, nil when the node
// cannot run one.
func (h *Manager) startExtensionNode(ctx context.Context, p *extension.Extension, m *Machine) (proto.PluginNodeState, *proto.PluginNodeProgram) {
	ps := h.extensions
	id := p.Record.ID
	st := proto.PluginNodeState{ID: id, Version: p.Record.Version, Revision: extensionNodeRevision(p)}
	if p.Record.StorageID != "" {
		if err := commitNodeIdentities(ctx, m, []proto.PluginNodeIdentity{extensionNodeIdentity(p)}); err != nil {
			st.State, st.Error = extensionNodeOutdated, i18n.T(h.contextLang(ctx), "extension-identity.node_required")
			return st, nil
		}
	}
	m.mu.Lock()
	platform := strings.ToLower(m.info.OS + "-" + m.info.Arch)
	m.mu.Unlock()
	fail := func(err error) (proto.PluginNodeState, *proto.PluginNodeProgram) {
		if nodeOutdated(err) {
			st.State, st.Error = extensionNodeOutdated, i18n.T(h.contextLang(ctx), "hub-extension_nodes.node_outdated")
			return st, nil
		}
		st.State, st.Error = "error", err.Error()
		if !errors.Is(err, errNodeVersionGone) {
			h.logf("[%s] plugin %s: %v", m.cfg.ID, id, err)
		}
		return st, nil
	}
	pkg, err := ps.nodePackage(p, platform)
	if err != nil {
		return fail(err)
	}
	prog := proto.PluginNodeProgram{ID: id, Version: p.Record.Version, Digest: pkg.digest, StorageID: p.Record.StorageID, Revision: extensionNodeRevision(p)}
	if err := putNodePackage(ctx, m, prog, pkg); err != nil {
		return fail(err)
	}
	init, err := ps.nodeInit(ctx, p)
	if err != nil {
		return fail(err)
	}
	if current := ps.reg.SnapshotExact(id); current == nil || current.Record.Version != p.Record.Version || !current.Record.UpdatedAt.Equal(p.Record.UpdatedAt) {
		return fail(errNodeVersionGone)
	}
	n := p.Manifest.Node
	cmd, _ := n.Command.For(platform)
	keep := n.KeepRunning
	if keep == "" {
		keep = extension.KeepLease
	}
	start := proto.PluginNodeStart{ID: id, StorageID: p.Record.StorageID, Revision: extensionNodeRevision(p), Name: p.Manifest.Name, Version: p.Record.Version, Command: cmd, Args: n.Args, Env: n.Env,
		RPC: n.SpeaksRPC(), HTTP: n.HTTP, Keep: keep, LeaseSeconds: int(extensionLease / time.Second), Init: init}
	if err := m.call(ctx, "plugin.node.start", start, &st); err != nil {
		st.Version = p.Record.Version
		if !nodeOutdated(err) {
			st.State, st.Error = "error", err.Error()
			return st, &prog // keep the package there: the version before it may be running (a rollback)
		}
		return fail(err)
	}
	return st, &prog
}

// nodeOutdated says whether err is a node that does not know plugins' programs (before #1830: its handle answers
// "unknown method plugin.node.have").
func nodeOutdated(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown method plugin.node.")
}

// syncExtensionNodesEverywhere brings every machine in line (a plugin was installed, switched, configured, upgraded,
// removed). Machines that are away are brought in line when they connect.
func (h *Manager) syncExtensionNodesEverywhere(ctx context.Context) {
	for _, m := range h.machinesNow() {
		go h.syncExtensionNodes(ctx, m)
	}
}

// extensionLeaseLoop renews the leases of plugins' programs on the machines that run them.
func (h *Manager) extensionLeaseLoop(ctx context.Context) {
	t := time.NewTicker(extensionLeaseEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if h.extensions == nil || h.extensions.reg == nil {
			continue
		}
		for _, m := range h.machinesNow() {
			if !m.isOnline() {
				continue
			}
			var ids []string
			for _, p := range h.extensions.reg.Snapshots() {
				if h.extensions.nodeRunsOn(ctx, p, m) {
					ids = append(ids, p.Record.ID)
				}
			}
			if len(ids) == 0 {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := m.call(rctx, "plugin.node.renew", proto.PluginNodeRenew{IDs: ids, LeaseSeconds: int(extensionLease / time.Second)}, nil); err != nil &&
				!nodeOutdated(err) {
				h.logf("[%s] plugins' leases: %v", m.cfg.ID, err)
			}
			cancel()
		}
	}
}

// extensionNodeEvent takes what a machine says of a plugin's program: its state, or an event for the plugin's program on
// the hub (node.event: which machine, the event and its params).
func (h *Manager) extensionNodeEvent(m *Machine, event string, params json.RawMessage) {
	if h.extensions == nil {
		return
	}
	switch event {
	case "plugin.node.state":
		var st proto.PluginNodeState
		// A late word from a machine of a plugin that is gone (its "stopped" after the uninstall) is not kept.
		if json.Unmarshal(params, &st) == nil && st.ID != "" && h.extensions.reg != nil {
			h.extensions.keepNodeState(st.ID, st.Version, m.cfg.ID, st)
		}
	case "plugin.node.event":
		var e proto.PluginNodeEvent
		if json.Unmarshal(params, &e) != nil || e.ID == "" {
			return
		}
		if !h.extensions.nodeIdentityCurrent(e.ID, e.Revision) {
			return
		}
		if hp := h.extensions.get(e.ID); hp != nil && hp.proc != nil && hp.proc.Spec().RPC {
			_ = hp.proc.Notify("node.event", map[string]any{"machine_id": m.cfg.ID, "event": e.Event, "params": e.Params})
		}
	}
}

// nodesCall is the plugin protocol's nodes.call from plugin id's program on the hub: a method of its program on one of
// the machines it runs on.
func (h *Manager) nodesCall(ctx context.Context, id string, params json.RawMessage) (any, error) {
	var p struct {
		Machine string          `json:"machine"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Machine == "" || p.Method == "" {
		return nil, &rpc.Error{Code: extension.CodeUnsupported, Message: "nodes.call: machine and method"}
	}
	pl := h.extensions.reg.GetExact(id)
	if pl == nil || pl.Manifest.NodeProgram() == nil {
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: "nodes.call: the plugin has no program on computers"}
	}
	m := h.machineByID(p.Machine)
	if m == nil || !h.extensions.nodeRunsOn(ctx, pl, m) {
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: fmt.Sprintf("nodes.call: not switched on for machine %s", p.Machine)}
	}
	if !m.isOnline() {
		return nil, &rpc.Error{Code: extensionNodeOffline, Message: "machine " + p.Machine + " is offline"}
	}
	var out json.RawMessage
	if err := m.call(ctx, "plugin.node.call", proto.PluginNodeCall{ID: id, Method: p.Method, Params: p.Params}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// serveNodeExtensionHTTP relays an app's request to plugin id's program on machine mid (its node.http): each request
// opens a stream to the program's socket there (plugin.node.dial), WebSockets included. Only a device with full rights,
// only a machine the plugin is switched on for, as the hub-side program's HTTP (/api/v1/plugins/{id}/…).
func (h *Manager) serveNodeExtensionHTTP(w http.ResponseWriter, r *http.Request) {
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("this device can only read and answer"))
		return
	}
	id, mid := r.PathValue("id"), r.PathValue("mid")
	requestedID := id
	var p *extension.Extension
	if h.extensions != nil {
		p = h.extensions.reg.Get(id)
	}
	m := h.machineByID(mid)
	if p == nil || p.Manifest.NodeProgram() == nil || !p.Manifest.Node.HTTP || m == nil || !h.extensions.nodeRunsOn(r.Context(), p, m) {
		writeErrCode(w, http.StatusNotFound, "not_found", fmt.Errorf("plugin %s serves nothing on machine %s", id, mid))
		return
	}
	if !m.isOnline() {
		writeErrCode(w, http.StatusConflict, extensionNodeOffline, i18n.Err("hub-extension_nodes.offline", i18n.Args{"name": m.cfg.Name}))
		return
	}
	id = p.Record.ID // Owner aliases route to the exact current node program.
	prefix := "/api/v1/extensions/" + requestedID + "/-/machines/" + mid
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) { return dialNodeExtension(ctx, m, id) }
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "plugin"
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			pr.Out.URL.RawPath = ""
			if raw := strings.TrimPrefix(pr.In.URL.EscapedPath(), prefix); raw != "" && raw != pr.Out.URL.Path {
				pr.Out.URL.RawPath = raw
			}
			// The hub has already checked the caller: the program never sees the app's credential.
			q := pr.Out.URL.Query()
			q.Del("token")
			pr.Out.URL.RawQuery = q.Encode()
			pr.Out.Header.Del("Authorization")
			pr.Out.Host = "plugin"
		},
		// A stream a request (no pooling across them): each is a connection to the program on that machine.
		Transport:     &http.Transport{DialContext: dial, DisableKeepAlives: true},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Cache-Control", "no-store")
			var re *rpc.Error
			if errors.As(err, &re) && re.Code == "not_running" {
				writeErrCode(w, http.StatusBadGateway, "not_running", fmt.Errorf("plugin %s is not running on %s", id, m.cfg.Name))
				return
			}
			writeErr(w, http.StatusBadGateway, fmt.Errorf("plugin %s is not answering on %s: %w", id, m.cfg.Name, err))
		},
	}
	w.Header().Del("Cache-Control")
	h.setCallerHeaders(r) // who is calling, as for the extension on the hub's machine (extension_caller.go)
	proxy.ServeHTTP(w, r)
}

// dialNodeExtension opens a stream to plugin id's HTTP socket on m: one end of a pipe is the stream, the other the
// connection the proxy talks over.
func dialNodeExtension(ctx context.Context, m *Machine, id string) (net.Conn, error) {
	mux := m.portStreams()
	if mux == nil {
		return nil, fmt.Errorf("%s: %w", m.cfg.Name, errOffline)
	}
	if mux.Len() >= maxPortStreams {
		return nil, errors.New("too many connections to this machine")
	}
	sid := streamPrefix + strconv.FormatInt(streamSeq.Add(1), 10)
	if err := mux.Open(sid); err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := m.call(dctx, "plugin.node.dial", map[string]string{"id": sid, "plugin": id}, nil); err != nil {
		mux.Abort(sid, err)
		return nil, err
	}
	stream, conn := net.Pipe()
	mux.Start(sid, stream)
	return conn, nil
}
