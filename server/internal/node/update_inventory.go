package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

var updateInventoryInstance = newUUID()

func updateDigest(value any) string {
	// Only concrete JSON contract types reach here; none contains a channel,
	// function or float. Still fail closed if that changes.
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (n *Node) updateInventory(ctx context.Context, target nodeclient.UpdateInventoryTarget) (nodeclient.UpdateInventory, error) {
	return n.collectUpdateInventory(ctx, target, updateProcessCensus)
}

func (n *Node) collectUpdateInventory(ctx context.Context, target nodeclient.UpdateInventoryTarget, census func(context.Context) ([]updateProcess, error)) (nodeclient.UpdateInventory, error) {
	out := nodeclient.UpdateInventory{Schema: 1, Capability: nodeproto.ManagerUpdateInventory, Target: target,
		Instance: updateInventoryInstance, Version: n.Version, Sources: map[string]string{}, Rows: []nodeclient.UpdateActivity{}}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	n.mu.Lock()
	machine, epoch := n.machineID, n.engEpoch
	n.mu.Unlock()
	if target.Manager == "" || target.Manager == "assistant" || !validID(target.Main) ||
		target.Machine == "" || machine != target.Machine || n.uid == "" || target.MachineUID != n.uid || !validUpdateRevision(target.AuthorityRevision) {
		return out, errors.New("explicit update inventory identity conflicts or is unavailable")
	}
	if n.sessionScope != nil || n.state == nil || n.stateReady == nil || n.state.isLoading() {
		return out, errors.New("complete node scope/state authority unavailable")
	}
	select {
	case <-n.stateReady:
	default:
		return out, errors.New("node state has not finished loading")
	}
	before, err := census(ctx)
	if err != nil {
		return out, err
	}
	retained, err := n.observeUpdateRetained(ctx, target)
	if err != nil {
		return out, err
	}
	out.Sources["retained-state"] = retained
	processes, structure := n.updateAssistantSet()
	if err := n.updateSourceCoverage(ctx, processes, out.Sources); err != nil {
		return out, err
	}
	for _, p := range processes {
		rows, revision, err := n.observeUpdateAssistant(ctx, p)
		if err != nil {
			return out, err
		}
		out.Rows = append(out.Rows, rows...)
		out.Sources["assistant:"+strconv.Itoa(p.cmd.Process.Pid)] = revision
	}
	r := n.codices
	if r == nil {
		return out, errors.New("Codex source registry unavailable")
	}
	r.mu.Lock()
	config := cloneCodexConfig(r.config)
	remotes := map[string]*codexRemote{}
	for id, c := range r.remotes {
		remotes[id] = c
	}
	r.mu.Unlock()
	coveredEndpoints := map[string]bool{}
	for _, spec := range config.Servers {
		c := remotes[spec.ID]
		if c == nil || c.spec != spec {
			return out, errors.New("configured app-server inventory is unavailable")
		}
		observation, err := c.observeUpdateThreads(ctx)
		if err != nil {
			return out, err
		}
		out.Sources["app-server:"+spec.ID] = observation.Revision
		for _, row := range observation.Threads {
			row.ID = codexSourceID(spec.Home, row.ID, config.LegacyHome)
			out.Rows = append(out.Rows, updateCodexActivity("app-server:"+spec.ID, row, observation.Revision))
		}
		coveredEndpoints[spec.Endpoint] = true
	}
	if len(remotes) != len(config.Servers) {
		return out, errors.New("app-server source set changed or is incomplete")
	}
	owned := map[int]bool{}
	for _, p := range processes {
		owned[p.cmd.Process.Pid] = true
	}
	for _, p := range before {
		if !owned[p.PID] && (p.Endpoint == "" || p.Endpoint == "stdio://" || !coveredEndpoints[p.Endpoint]) {
			return out, fmt.Errorf("unobserved live agent process %d", p.PID)
		}
	}
	finalRetained, err := n.observeUpdateRetained(ctx, target)
	if err != nil {
		return out, err
	}
	if finalRetained != retained {
		return out, errors.New("retained session authority changed during read")
	}
	finalSources := map[string]string{}
	if err := n.updateSourceCoverage(ctx, processes, finalSources); err != nil {
		return out, err
	}
	for key, revision := range finalSources {
		if out.Sources[key] != revision {
			return out, errors.New("host coverage changed during read")
		}
	}
	for key, revision := range out.Sources {
		if revision == "" {
			return out, errors.New("source revision unavailable: " + key)
		}
	}
	after, err := census(ctx)
	if err != nil {
		return out, err
	}
	_, finalStructure := n.updateAssistantSet()
	n.mu.Lock()
	unchanged := n.machineID == machine && n.engEpoch == epoch
	n.mu.Unlock()
	r.mu.Lock()
	finalConfig := cloneCodexConfig(r.config)
	sameRemotes := len(remotes) == len(r.remotes)
	for id, c := range remotes {
		sameRemotes = sameRemotes && r.remotes[id] == c
	}
	r.mu.Unlock()
	if !unchanged || structure != finalStructure || updateDigest(before) != updateDigest(after) ||
		updateDigest(config) != updateDigest(finalConfig) || !sameRemotes {
		return out, errors.New("node identity or source ownership changed during complete read")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	out.Sources["owner-processes"] = updateDigest(after)
	out.Sources["assistant-ownership"] = structure
	out.Sources["codex-config"] = updateDigest(config)
	// The persisted main is covered even when it has no live runtime. This is
	// derived only after every live source answered; no historical status is used.
	mainFound := false
	for _, row := range out.Rows {
		mainFound = mainFound || row.Session == target.Main
	}
	if !mainFound {
		out.Rows = append(out.Rows, nodeclient.UpdateActivity{Source: "not-loaded", Session: target.Main, Status: proto.StatusOffline, Revision: structure})
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Session < b.Session
	})
	out.Revision, err = nodeclient.InventoryRevision(out)
	if err != nil {
		return out, err
	}
	out.Complete = true
	out.CompletedAt = time.Now().UTC()
	return out, nil
}

// Include processes not yet entered in procs, and residents kept across a Hub
// reconnect. A detached/foreign owner is a coverage conflict, never omitted.
func (n *Node) updateAssistantSet() ([]*assistantProc, string) {
	set := map[*assistantProc]bool{}
	n.mu.Lock()
	for _, p := range n.procs {
		set[p] = true
	}
	for _, p := range n.akeys {
		set[p] = true
	}
	if n.spare != nil {
		set[n.spare] = true
	}
	n.mu.Unlock()
	liveAssistants.Range(func(k, _ any) bool { set[k.(*assistantProc)] = true; return true })
	out := []*assistantProc{}
	keys := []string{}
	for p := range set {
		if p == nil {
			continue
		}
		out = append(out, p)
		pid := 0
		if p.cmd != nil && p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
		}
		keys = append(keys, fmt.Sprintf("%p:%d:%t:%p", p, pid, p.alive(), p.owner.Load()))
	}
	sort.Strings(keys)
	sort.Slice(out, func(i, j int) bool { return fmt.Sprintf("%p", out[i]) < fmt.Sprintf("%p", out[j]) })
	return out, updateDigest(keys)
}

// Read full host snapshots directly: the display refresh mutates caches and can
// fall back to an old agent list. Neither is an update evidence source.
func (n *Node) updateSourceCoverage(ctx context.Context, processes []*assistantProc, sources map[string]string) error {
	for _, p := range processes {
		if p.cmd == nil || p.cmd.Process == nil || !p.alive() {
			return errors.New("assistant process identity is incomplete")
		}
		if owner := p.owner.Load(); owner != nil && owner != n {
			return errors.New("resident assistant owner is changing")
		}
	}
	n.starts.mu.Lock()
	starting := false
	for _, rec := range n.starts.m {
		starting = starting || rec.ended.IsZero()
	}
	n.starts.mu.Unlock()
	n.mu.Lock()
	codexStarting := len(n.codexStarting) > 0
	n.mu.Unlock()
	if starting || codexStarting {
		return errors.New("session launch is pending or unresolved")
	}
	if n.runtimes == nil {
		return errors.New("runtime registry unavailable")
	}
	n.runtimes.mu.Lock()
	runtimes := len(n.runtimes.conns) + len(n.runtimes.known)
	n.runtimes.mu.Unlock()
	if runtimes > 0 {
		return errors.New("connected runtime lacks authoritative update inventory")
	}
	if n.runtimeHost != nil {
		n.runtimeHost.dmu.Lock()
		daemons := len(n.runtimeHost.daemons)
		n.runtimeHost.dmu.Unlock()
		if daemons > 0 {
			return errors.New("configured runtime daemon lacks authoritative update inventory")
		}
	}
	n.eng.mu.Lock()
	ready, configured := n.eng.ready, n.eng.botmux != nil
	n.eng.mu.Unlock()
	if ready == nil || !configured {
		return errors.New("host configuration has not settled")
	}
	select {
	case <-ready:
	default:
		return errors.New("host configuration has not settled")
	}
	if n.hosts == nil {
		return errors.New("host configuration unavailable")
	}
	for _, h := range n.hosts.All() {
		fresh, ok := h.(updateSnapshotSource)
		if !ok {
			return errors.New("configured host lacks a fresh update inventory contract")
		}
		snapshot, err := fresh.SnapshotForUpdate(ctx)
		if errors.Is(err, host.ErrNotRunning) {
			sources["host:"+h.Kind()] = "not-running"
			continue
		}
		if err != nil || snapshot == nil {
			return errors.New("complete host snapshot unavailable")
		}
		// Terminal agent protocols do not expose a complete background/child
		// inventory. Retain the blocker even if a display says idle.
		if len(snapshot.Agents) > 0 {
			return errors.New("terminal agent lacks authoritative background inventory")
		}
		sources["host:"+h.Kind()] = updateDigest(snapshot)
	}
	return nil
}

func updateCodexActivity(source string, row updateCodexThread, revision string) nodeclient.UpdateActivity {
	return nodeclient.UpdateActivity{Source: source, Session: row.ID, Live: true, Status: row.Status,
		Background: row.BackgroundTerminals, PendingInteractions: row.PendingInteractions, Revision: revision}
}

func validUpdateRevision(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size
}
