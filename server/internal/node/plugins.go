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
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/proto"
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
