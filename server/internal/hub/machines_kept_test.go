package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/rpc"
)

// keptIdents is memIdents with the settings and roster lookup of a manager store (the rest is not used here).
type keptIdents struct {
	managerStore // nil: not used
	ids          *memIdents
	mu           sync.Mutex
	kept         map[string]json.RawMessage
}

func (s *keptIdents) SaveBusIdentity(ctx context.Context, b db.BusIdentity, hash string) error {
	return s.ids.SaveBusIdentity(ctx, b, hash)
}
func (s *keptIdents) BusIdentityByHash(ctx context.Context, hash string) (*db.BusIdentity, error) {
	return s.ids.BusIdentityByHash(ctx, hash)
}
func (s *keptIdents) BusIdentities(ctx context.Context) ([]db.BusIdentity, error) {
	return s.ids.BusIdentities(ctx)
}
func (s *keptIdents) DeleteBusIdentity(ctx context.Context, id string) (bool, error) {
	return s.ids.DeleteBusIdentity(ctx, id)
}
func (s *keptIdents) BusIdentityByID(_ context.Context, id string) (*db.BusIdentity, error) {
	s.ids.mu.Lock()
	defer s.ids.mu.Unlock()
	if b, ok := s.ids.byID[id]; ok {
		return &b, nil
	}
	return nil, nil
}
func (s *keptIdents) SeeBusIdentity(context.Context, string, *db.BusIdentity) error { return nil }
func (s *keptIdents) RenameBusIdentity(context.Context, string, string) (bool, error) {
	return false, nil
}
func (s *keptIdents) PendingManagerInstalls(context.Context) ([]db.ManagerInstallCode, error) {
	return nil, nil
}
func (s *keptIdents) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.kept[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}
func (s *keptIdents) PutSetting(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kept[key] = raw
	return nil
}

func hubJSONMachines(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "hub.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Machines []MachineConfig `json:"machines"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range f.Machines {
		ids = append(ids, m.ID)
	}
	return ids
}

func ownListIDs(t *testing.T, file string) []string {
	t.Helper()
	list, from, ok, err := managerconf.OwnMachines(file)
	if err != nil || !ok || !from.Own {
		return nil
	}
	var ids []string
	for _, e := range list {
		var m MachineConfig
		_ = json.Unmarshal(e, &m)
		ids = append(ids, m.ID)
	}
	return ids
}

// A manager that keeps its own list: once it reports it holds hub.json's machines as its own, they move out of
// hub.json (a copy beside it), and the hub knows them as that manager keeps them; the admin page's add, edit and
// removal go to its list; with it away they are answered as such; a hub that restarts still has them.
func TestAManagerKeepsItsOwnList(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	st := newMeshStore()
	h.store = st
	idents := &keptIdents{ids: &memIdents{}, kept: map[string]json.RawMessage{}}
	h.bus.idents = idents
	box := MachineConfig{ID: "box", Name: "Box", Kind: "ssh", SSHHost: "box.example", TailcatAddr: "tc-s3cret"}
	h.cfg.Machines = []MachineConfig{box}
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"),
		[]byte(`{"machines":[{"id":"box","name":"Box","kind":"ssh","ssh_host":"box.example","tailcat_addr":"tc-s3cret","note":"kept"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	node := newMeshNode("uid-box", "box", "s1")
	node.resident = true
	h.startDevices(ctx)
	srv := newHubServer(t, h)
	file := filepath.Join(t.TempDir(), managerconf.File)
	nodes := map[string]func(context.Context) (net.Conn, error){"box": node.dialer(h)}
	mgr := startTestManagerWith(t, h, srv, nodes, file)

	eventually(t, 10*time.Second, "box moved out of hub.json into the manager's own list", func() bool {
		return len(hubJSONMachines(t, h.cfg.dir)) == 0 && h.keptBy("box") == devicesOwner && h.cfgEntry("box") == nil
	})
	if ids := ownListIDs(t, file); len(ids) != 1 || ids[0] != "box" {
		t.Fatalf("the manager's own list: %v", ids)
	}
	copies, _ := filepath.Glob(filepath.Join(h.cfg.dir, "hub.json.machines-kept-*"))
	if len(copies) != 1 {
		t.Fatalf("no copy of the moved entries beside hub.json: %v", copies)
	}
	if b, _ := os.ReadFile(copies[0]); !strings.Contains(strings.ReplaceAll(string(b), " ", ""), `"note":"kept"`) || !strings.Contains(string(b), "tc-s3cret") {
		t.Fatalf("the copy is not hub.json's entry as it was:\n%s", b)
	}
	if st, err := os.Stat(copies[0]); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the copy's mode: %v %v", st, err)
	}
	if cfg := h.devicesConfigFor(devicesOwner); cfg.Machines != nil || !cfg.MachinesOwn {
		t.Fatalf("the hand-over to a manager that holds its own list: machines %v, machines_own %v", cfg.Machines, cfg.MachinesOwn)
	}
	eventually(t, 10*time.Second, "a devices.config with no machines sent to it", func() bool {
		h.devices.mu.Lock()
		defer h.devices.mu.Unlock()
		return strings.Contains(string(h.devices.sent[devicesOwner]), `"machines":null`)
	})
	if r := h.pendingRestart(""); len(r.Reasons) != 0 {
		t.Fatalf("a machine moved out of hub.json asks for a restart: %v", r.Reasons)
	}
	if mc := h.knownEntry("box"); mc == nil || mc.SSHHost != "box.example" || mc.TailcatAddr != tailcatKept {
		t.Fatalf("what the hub knows of box: %+v", mc)
	}
	eventually(t, 10*time.Second, "box online through its manager", func() bool {
		m := h.machineByID("box")
		return m != nil && m.snapshot().Online
	})

	// The admin page's add goes to the manager's own list, not hub.json.
	if err := h.addMachine(&MachineConfig{ID: "lap", Name: "Lap", Kind: kindLink}, "admin"); err != nil {
		t.Fatal(err)
	}
	if ids := ownListIDs(t, file); len(ids) != 2 || len(hubJSONMachines(t, h.cfg.dir)) != 0 || h.keptBy("lap") != devicesOwner {
		t.Fatalf("after the add: own list %v, hub.json %v", ids, hubJSONMachines(t, h.cfg.dir))
	}
	if err := h.addMachine(&MachineConfig{ID: "lap", Name: "Lap", Kind: kindLink}, "admin"); err == nil {
		t.Fatal("the same id added twice")
	}
	mirror(t, h, "lap")
	// An edit goes to its list (got, changed, put back).
	if kept, err := h.editKeptMachine(ctx, "lap", map[string]any{"name": "Laptop"}); !kept || err != nil {
		t.Fatalf("edit: %v %v", kept, err)
	}
	list, _, _, _ := managerconf.OwnMachines(file)
	if !strings.Contains(string(list[1]), `"Laptop"`) {
		t.Fatalf("the edit is not in the own list: %s", list)
	}
	// A rename of one it keeps goes to its list too, and the machine keeps its connection (machine_rename.go).
	h.machineByID("box").mu.Lock()
	connectedAt := h.machineByID("box").connectedAt
	h.machineByID("box").mu.Unlock()
	if got, err := h.renameMachine(ctx, "box", "Studio box", "admin", "admin"); err != nil || got != "Studio box" {
		t.Fatalf("rename a kept machine: %q %v", got, err)
	}
	list, _, _, _ = managerconf.OwnMachines(file)
	if !strings.Contains(string(list[0]), `"Studio box"`) || len(hubJSONMachines(t, h.cfg.dir)) != 0 {
		t.Fatalf("the rename is not in the own list, or went to hub.json: %s", list)
	}
	eventually(t, 10*time.Second, "box shown renamed, still connected", func() bool {
		m := h.machineByID("box")
		s := m.snapshot()
		m.mu.Lock()
		same := m.connectedAt.Equal(connectedAt)
		m.mu.Unlock()
		return s.Name == "Studio box" && s.Online && same
	})
	// The removal too: it lets it go, and the hub shows it no more.
	if kept, err := h.removeKeptMachine(ctx, "lap"); !kept || err != nil {
		t.Fatalf("remove: %v %v", kept, err)
	}
	if ids := ownListIDs(t, file); len(ids) != 1 || h.keptBy("lap") != "" || h.machineByID("lap") != nil {
		t.Fatalf("after the removal: own list %v", ids)
	}

	// The manager away: an edit of what it keeps is answered as such, nothing changes.
	mgr.stop()
	eventually(t, 10*time.Second, "the manager gone", func() bool { return h.busConnOf(devicesOwner) == nil })
	if _, err := h.removeKeptMachine(ctx, "box"); !errors.Is(err, errKeeperAway) {
		t.Fatalf("a removal with its manager away: %v", err)
	}
	if h.keptBy("box") != devicesOwner {
		t.Fatal("box forgotten with its manager away")
	}
	// Added meanwhile: to hub.json, and into its own list when it is back.
	if err := h.addMachine(&MachineConfig{ID: "desk", Name: "Desk", Kind: kindLink}, "admin"); err != nil {
		t.Fatal(err)
	}
	if ids := hubJSONMachines(t, h.cfg.dir); len(ids) != 1 || ids[0] != "desk" {
		t.Fatalf("added with its manager away: hub.json %v", ids)
	}
	back := startTestManagerWith(t, h, srv, nodes, file)
	eventually(t, 10*time.Second, "desk put in its own list and moved out of hub.json", func() bool {
		return len(hubJSONMachines(t, h.cfg.dir)) == 0 && h.keptBy("desk") == devicesOwner && len(ownListIDs(t, file)) == 2
	})
	back.stop()
	eventually(t, 10*time.Second, "the manager gone again", func() bool { return h.busConnOf(devicesOwner) == nil })

	// A hub that restarts still has it, as its manager last reported it.
	h.saveMachineEntries(ctx)
	next := newTestHub(t)
	next.store, next.bus.idents = st, idents
	next.cfg.dir = h.cfg.dir
	next.startDevices(ctx)
	if m := next.machineByID("box"); m == nil || m.ownerOrCarrier() != devicesOwner || m.snapshot().Hostname == "" {
		t.Fatalf("box after the restart: %+v", m)
	}
}

// fakeKeeper is a manager that keeps its own list in memory (devices.machines.*).
type fakeKeeper struct {
	id   string
	peer *rpc.Peer
	stop context.CancelFunc
	mu   sync.Mutex
	list map[string]json.RawMessage
}

func connectKeeper(t *testing.T, h *Hub, srv *httptest.Server, id string, list ...json.RawMessage) *fakeKeeper {
	t.Helper()
	f := &fakeKeeper{id: id, list: map[string]json.RawMessage{}}
	for _, e := range list {
		f.list[machineEntryID(e)] = e
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, err := bus.Dial(ctx, srv.URL, busToken(t, h, id, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	f.stop = func() { cancel(); _ = ws.CloseNow() }
	t.Cleanup(func() { _ = ws.CloseNow() })
	conn := bus.NewConn(ctx, ws)
	f.peer = rpc.New("k", conn)
	f.peer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var ref devicewire.MachineRef
		var put devicewire.MachinePut
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case devicewire.MethodMachinesGet:
			_ = json.Unmarshal(params, &ref)
			if e, ok := f.list[ref.MachineID]; ok {
				return devicewire.MachineEntry{Entry: e}, nil
			}
			return nil, &rpc.Error{Code: devicewire.CodeNoDevice, Message: "none"}
		case devicewire.MethodMachinesPut:
			_ = json.Unmarshal(params, &put)
			f.list[machineEntryID(put.Entry)] = put.Entry
			return map[string]any{}, nil
		case devicewire.MethodMachinesRemove:
			_ = json.Unmarshal(params, &ref)
			_, had := f.list[ref.MachineID]
			delete(f.list, ref.MachineID)
			return devicewire.MachineRemoved{Removed: had}, nil
		}
		return map[string]any{}, nil
	}
	go func() { _ = f.peer.Serve(ctx, conn) }()
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	if err := f.peer.Call(cctx, busHello, map[string]any{"id": id,
		"capabilities": []string{assistantwire.CapabilityConfigOwn, assistantwire.CapabilityMachinesOwn}}, nil); err != nil {
		t.Fatal(err)
	}
	h.devices.mu.Lock()
	if h.devices.ownListHeld == nil {
		h.devices.ownListHeld = map[string]bool{}
	}
	h.devices.ownListHeld[id] = true // as its report would say
	h.devices.mu.Unlock()
	return f
}

func (f *fakeKeeper) has(id string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.list[id]
}

// A machine a manager keeps moves with its entry, secrets too: got from the one that keeps it, put in the other's own
// list (or hub.json, for one that keeps none), let go by the first; with the one that keeps it away, it does not move.
// Removing a manager takes what it kept with it.
func TestKeptMachinesMove(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	h.store = newMeshStore()
	h.bus.idents = &keptIdents{ids: &memIdents{}, kept: map[string]json.RawMessage{}}
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"machines":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.startDevices(ctx)
	srv := newHubServer(t, h)
	far := json.RawMessage(`{"id":"far","name":"Far","kind":"ssh","ssh_host":"far.example","tailcat_addr":"tc-s3cret"}`)
	a := connectKeeper(t, h, srv, "m-0a0a0a", far)
	b := connectKeeper(t, h, srv, "m-0b0b0b")
	h.devices.moving.Lock()
	err := h.setKept(ctx, "far", a.id)
	h.devices.moving.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if from, err := h.moveMachine(ctx, "far", b.id); err != nil || from != a.id {
		t.Fatalf("move to b: %s %v", from, err)
	}
	if e := b.has("far"); !strings.Contains(string(e), "tc-s3cret") || a.has("far") != nil || h.keptBy("far") != b.id {
		t.Fatalf("after the move: b %s, a %s, kept by %q", e, a.has("far"), h.keptBy("far"))
	}

	// The one that keeps it away: it stays.
	b.stop()
	eventually(t, 15*time.Second, "b gone", func() bool { return h.busConnOf(b.id) == nil })
	if _, err := h.moveMachine(ctx, "far", a.id); !errors.Is(err, errKeeperAway) || h.keptBy("far") != b.id {
		t.Fatalf("a move with its keeper away: %v, kept by %q", err, h.keptBy("far"))
	}

	// To a manager that keeps no list of its own (the carrier, not connected): into hub.json.
	h.devices.moving.Lock()
	err = h.setKept(ctx, "far", a.id)
	h.devices.moving.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.list["far"] = far
	a.mu.Unlock()
	if _, err := h.moveMachine(ctx, "far", devicesOwner); err != nil {
		t.Fatal(err)
	}
	if ids := hubJSONMachines(t, h.cfg.dir); len(ids) != 1 || ids[0] != "far" || h.keptBy("far") != "" || a.has("far") != nil {
		t.Fatalf("moved to the carrier: hub.json %v, kept by %q", ids, h.keptBy("far"))
	}
	if h.entryOwner("far") != devicesOwner || h.cfgEntry("far") == nil {
		t.Fatal("not the carrier's hub.json machine")
	}

	// A manager removed takes what it kept with it.
	h.devices.moving.Lock()
	err = h.setKept(ctx, "near", a.id)
	h.devices.moving.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.releaseMachines(ctx, a.id)
	if h.keptBy("near") != "" || h.knownEntry("near") != nil {
		t.Fatal("a removed manager's machine is still known")
	}
}

// What is staged in hub.json for a manager that keeps its own list settles as that manager has it, never as the hub
// last showed it: a hub.json machine moved to it is put there before it leaves hub.json; one edited in hub.json while
// staged is put again as hub.json has it; one its report names that is no longer its own is taken out of its list. A
// manager whose build lost machines.own is handed no list (its manager.json is not written over), and a manager that
// keeps machines is not removed unless they are dropped on purpose.
func TestSettleOwnListAsTheKeeperHasIt(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	h.store = newMeshStore()
	h.bus.idents = &keptIdents{ids: &memIdents{}, kept: map[string]json.RawMessage{}}
	box := `{"id":"box","name":"Box","kind":"ssh","ssh_host":"box.example","tailcat_addr":"tc-s3cret"}`
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"machines":[`+box+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.cfg.Machines = []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh", SSHHost: "box.example", TailcatAddr: "tc-s3cret"}}
	h.startDevices(ctx)
	srv := newHubServer(t, h)
	a := connectKeeper(t, h, srv, "m-0a0a0a")
	// The carrier reported box (the hub shows it); it moves to a: a is put it before it leaves hub.json.
	h.takeMachineEntries(devicesOwner, devicewireStates("box", json.RawMessage(box)))
	if _, err := h.moveMachine(ctx, "box", a.id); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "box put to a", func() bool { return strings.Contains(string(a.has("box")), "tc-s3cret") })
	if ids := hubJSONMachines(t, h.cfg.dir); len(ids) != 1 {
		t.Fatalf("box left hub.json before a had it: %v", ids)
	}
	h.settleOwnList(a.id) // a's next report
	if ids := hubJSONMachines(t, h.cfg.dir); len(ids) != 0 || h.keptBy("box") != a.id {
		t.Fatalf("box did not move out once a had it: hub.json %v, kept by %q", ids, h.keptBy("box"))
	}

	// Staged and edited in hub.json meanwhile: put again as hub.json has it, then out.
	desk := `{"id":"desk","name":"Desk","kind":"ssh","ssh_host":"desk.example"}`
	a.mu.Lock()
	a.list["desk"] = json.RawMessage(`{"id":"desk","name":"Desk","kind":"ssh","ssh_host":"old.example"}`)
	a.mu.Unlock()
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"machines":[`+desk+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.addMachineLive(MachineConfig{ID: "desk", Name: "Desk", Kind: "ssh", SSHHost: "desk.example"})
	h.devices.moving.Lock()
	h.devices.assign["desk"] = a.id
	h.devices.moving.Unlock()
	h.settleOwnList(a.id)
	if !strings.Contains(string(a.has("desk")), "desk.example") || len(hubJSONMachines(t, h.cfg.dir)) != 1 {
		t.Fatalf("the edited entry: a has %s, hub.json %v", a.has("desk"), hubJSONMachines(t, h.cfg.dir))
	}
	h.settleOwnList(a.id)
	if len(hubJSONMachines(t, h.cfg.dir)) != 0 || h.keptBy("desk") != a.id {
		t.Fatal("desk did not move out once a had it as hub.json had it")
	}

	// One that left hub.json (removed in the admin page, gone at the restart) is not put back.
	h.addMachineLive(MachineConfig{ID: "left", Name: "Left", Kind: "ssh", SSHHost: "left.example"})
	h.devices.moving.Lock()
	h.devices.assign["left"] = a.id
	h.devices.moving.Unlock()
	h.settleOwnList(a.id)
	if a.has("left") != nil || h.keptBy("left") != "" {
		t.Fatal("a machine removed from hub.json was put to its manager")
	}

	// a's report names a machine that is no longer its own: taken out of its list.
	a.mu.Lock()
	a.list["far"] = json.RawMessage(`{"id":"far"}`)
	a.mu.Unlock()
	h.devices.mu.Lock()
	if h.devices.lastNamed == nil {
		h.devices.lastNamed = map[string]map[string]bool{}
	}
	h.devices.lastNamed[a.id] = map[string]bool{"box": true, "desk": true, "far": true}
	h.devices.mu.Unlock()
	h.devices.moving.Lock()
	err := h.setKept(ctx, "far", devicesOwner)
	h.devices.moving.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.settleOwnList(a.id)
	if a.has("far") != nil || a.has("box") == nil {
		t.Fatal("a still has a machine that is not its own (or lost its own)")
	}

	// A manager that keeps machines is not removed unless they are dropped on purpose.
	if code, out := ownershipGet(t, h, srv, "DELETE", "/api/v1/managers/"+a.id, nil); code != 409 || out["code"] != "keeps_machines" {
		t.Fatalf("removing a manager that keeps machines: %d %v", code, out)
	}

	// It holds its list: its hand-over carries none, the computers waiting to be confirmed still.
	if cfg := h.devicesConfigFor(a.id); cfg.Machines != nil || !cfg.MachinesOwn || cfg.PendingUnknown != h.trustUnknown() {
		t.Fatalf("the hand-over to a keeper: %+v", cfg)
	}
	h.devices.mu.Lock()
	h.devices.ownListHeld[a.id] = false // before it said it holds it: the hand-over carries what is staged for it
	h.devices.mu.Unlock()
	if cfg := h.devicesConfigFor(a.id); cfg.Machines == nil {
		t.Fatal("a manager that has not said it holds its list is handed nothing to take over")
	}

	// Its build no longer names machines.own: it is handed no list at all.
	a.stop()
	eventually(t, 15*time.Second, "a gone", func() bool { return h.busConnOf(a.id) == nil })
	if cfg := h.devicesConfigFor(a.id); cfg.Machines != nil || cfg.MachinesOwn {
		t.Fatalf("handed to a keeper without machines.own: %+v", cfg)
	}
}

// devicewireStates is a report naming one entry.
func devicewireStates(id string, cfg json.RawMessage) devicewire.States {
	return devicewire.States{Entries: []devicewire.RecMachine{{ID: id, Cfg: cfg}}}
}

// The hub's own default machine (hub.json lists no machines) moves like any other, and stays with the manager it
// moved to last, a hub restart included, though it is in hub.json again at every start.
func TestTheDefaultMachineStaysMoved(t *testing.T) {
	ctx := context.Background()
	st := newMeshStore()
	idents := &keptIdents{ids: &memIdents{}, kept: map[string]json.RawMessage{}}
	local := MachineConfig{ID: "hubhost", Name: "hubhost", Kind: "local", Icon: "server"}
	newHub := func() *Hub {
		h := newTestHub(t)
		h.store, h.bus.idents = st, idents
		h.cfg.Machines = []MachineConfig{local}
		return h
	}
	h := newHub()
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.startDevices(ctx)
	srv := newHubServer(t, h)
	b := connectKeeper(t, h, srv, "m-0b0b0b")
	c := connectKeeper(t, h, srv, "m-0c0c0c")
	if _, err := h.moveMachine(ctx, "hubhost", b.id); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "put to b", func() bool { return b.has("hubhost") != nil })
	h.settleOwnList(b.id)
	if h.keptBy("hubhost") != b.id {
		t.Fatalf("not kept by b: %q", h.keptBy("hubhost"))
	}
	if _, err := h.moveMachine(ctx, "hubhost", c.id); err != nil {
		t.Fatal(err)
	}
	if c.has("hubhost") == nil || b.has("hubhost") != nil || h.keptBy("hubhost") != c.id {
		t.Fatal("the second move did not go to c")
	}
	// Removing it would not last (it is in hub.json again at every start): refused, with what to do instead.
	if kept, err := h.removeKeptMachine(ctx, "hubhost"); !kept || !errors.Is(err, errDefaultMachine) || c.has("hubhost") == nil {
		t.Fatalf("removing the default machine: %v %v", kept, err)
	}
	next := newHub()
	next.cfg.dir = h.cfg.dir
	next.startDevices(ctx)
	if o := next.entryOwner("hubhost"); o != c.id {
		t.Fatalf("after a restart the default machine is %s's, not c's", o)
	}
}

// A dedicated local machine (one that requires its own manager) never goes to the carrier, kept or not: its move to
// the carrier is refused, its reservation follows its moves, and the release of a manager it left does not take it.
func TestADedicatedKeptMachineNeverGoesToTheCarrier(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	h.store = newMeshStore()
	h.bus.idents = &keptIdents{ids: &memIdents{}, kept: map[string]json.RawMessage{}}
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"machines":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.startDevices(ctx)
	srv := newHubServer(t, h)
	b := connectKeeper(t, h, srv, "m-0b0b0b")
	c := connectKeeper(t, h, srv, "m-0c0c0c")
	if err := h.addOwnedLocalMachine(ctx, &MachineConfig{ID: "ded", Name: "Ded", Kind: "local"}, b.id); err != nil {
		t.Fatal(err)
	}
	if b.has("ded") == nil || h.keptBy("ded") != b.id {
		t.Fatalf("the dedicated machine is not in b's own list: kept by %q", h.keptBy("ded"))
	}
	if _, err := h.moveMachine(ctx, "ded", devicesOwner); err == nil || b.has("ded") == nil {
		t.Fatalf("a dedicated machine moved to the carrier: %v", err)
	}
	if _, err := h.moveMachine(ctx, "ded", c.id); err != nil {
		t.Fatal(err)
	}
	h.devices.mu.Lock()
	reserved := h.devices.assign["ded"]
	h.devices.mu.Unlock()
	if c.has("ded") == nil || b.has("ded") != nil || reserved != c.id {
		t.Fatalf("after the move to c: reservation %q", reserved)
	}
	h.releaseMachines(ctx, b.id)
	if c.has("ded") == nil || h.entryOwner("ded") != c.id {
		t.Fatalf("the release of b took c's dedicated machine: owner %s", h.entryOwner("ded"))
	}
}

// A manager's new connection holds its own list only once its report says so: its first hand-over carries what
// hub.json stages for it, whatever the last connection said.
func TestANewConnectionHoldsItsListOnlyOnceItSaysSo(t *testing.T) {
	h := newTestHub(t)
	h.devices.mu.Lock()
	h.devices.ownListHeld = map[string]bool{devicesOwner: true}
	h.devices.lastNamed = map[string]map[string]bool{devicesOwner: {"box": true}}
	h.devices.mu.Unlock()
	h.devicesConnected(devicesOwner)
	if h.listHeld(devicesOwner) {
		t.Fatal("a new connection counted as holding its list")
	}
	h.devices.mu.Lock()
	_, named := h.devices.lastNamed[devicesOwner]
	h.devices.mu.Unlock()
	if named {
		t.Fatal("the last connection's report kept for the new one")
	}
}

// A kept machine its keeper's report lacks (its list does not read) is still known as last reported, not as its id
// alone.
func TestAKeptMachineMissingFromAReportStaysAsReported(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	h.startDevices(ctx)
	h.devices.moving.Lock()
	err := h.setKept(ctx, "far", "m-0a0a0a")
	h.devices.moving.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.takeMachineEntries("m-0a0a0a", devicewireStates("far", json.RawMessage(`{"id":"far","name":"Far","kind":"link"}`)))
	h.takeMachineEntries("m-0a0a0a", devicewire.States{})
	if mc := h.knownEntry("far"); mc == nil || mc.Kind != kindLink || mc.Name != "Far" {
		t.Fatalf("far after a report without it: %+v", mc)
	}
}
