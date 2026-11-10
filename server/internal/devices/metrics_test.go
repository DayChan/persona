package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func pts(ts ...int64) []proto.MetricPoint {
	out := make([]proto.MetricPoint, len(ts))
	for i, t := range ts {
		v := float64(i)
		out[i] = proto.MetricPoint{T: t, CPU: &v}
	}
	return out
}

func gapsAt(points []proto.MetricPoint) []int64 {
	var out []int64
	for _, p := range points {
		if p.Gap {
			out = append(out, p.T)
		}
	}
	return out
}

func TestMetricGaps(t *testing.T) {
	end := time.Unix(1000, 0)
	// One missed sample (a 20 s hole) is bridged; the node away for a minute is a break right after its last point.
	got := withGaps(pts(800, 810, 830, 900, 910, 1000), 10, end)
	if g := gapsAt(got); len(g) != 2 || g[0] != 840 || g[1] != 920 {
		t.Fatalf("gaps at %v in %+v", g, got)
	}
	if len(got) != 8 {
		t.Fatalf("%d points", len(got))
	}
	// Offline now: the series ends with a break, so the chart does not run the last value up to now.
	if g := gapsAt(withGaps(pts(700, 710), 10, end)); len(g) != 1 || g[0] != 720 {
		t.Fatalf("trailing gap %v", g)
	}
	// Still sending: no trailing break for the step in progress.
	if g := gapsAt(withGaps(pts(960, 970, 980), 10, end)); len(g) != 0 {
		t.Fatalf("break while online: %v", g)
	}
	if got := withGaps(nil, 60, end); len(got) != 0 {
		t.Fatalf("empty = %+v", got)
	}
}

func TestMetricsBook(t *testing.T) {
	d := New(Options{})
	now := time.Unix(10_000, 0)
	cpu := 42.0
	b, _ := json.Marshal(map[string]any{"sample": proto.MetricSample{T: now.Unix() - 5, CPU: &cpu, MemTotal: 8}})
	d.onMetrics("m1", b)
	if s := d.metrics.now("m1", now); s == nil || *s.CPU != 42 {
		t.Fatalf("latest = %+v", s)
	}
	if s := d.metrics.now("m1", now.Add(time.Minute)); s != nil {
		t.Fatalf("a minute-old sample is not now: %+v", s)
	}
	d.onMetrics("m1", []byte(`{"sample":{}}`)) // no time: ignored
	if s := d.metrics.now("m1", now); s == nil {
		t.Fatal("an empty sample replaced the latest")
	}
	d.onSystem("m1", &proto.System{OS: "linux", Distro: "ubuntu"})
	if s := d.metrics.system("m1"); s == nil || s.Distro != "ubuntu" {
		t.Fatalf("system = %+v", s)
	}
}

// metricsMemStore records the machines of each write: one InsertMetric a sample, or (batchMetricsStore) several in one.
type metricsMemStore struct {
	*memStore
	mu     sync.Mutex
	writes [][]string
	stored chan struct{}
}

func (s *metricsMemStore) record(samples ...db.MachineSample) {
	var machines []string
	for _, x := range samples {
		machines = append(machines, x.Machine)
	}
	s.mu.Lock()
	s.writes = append(s.writes, machines)
	s.mu.Unlock()
	s.stored <- struct{}{}
}

func (s *metricsMemStore) InsertMetric(_ context.Context, machine string, sample proto.MetricSample) error {
	s.record(db.MachineSample{Machine: machine, Sample: sample})
	return nil
}
func (s *metricsMemStore) PutMachineSystem(context.Context, string, proto.System) error { return nil }
func (s *metricsMemStore) MachineSystems(context.Context) (map[string]proto.System, error) {
	return nil, nil
}
func (s *metricsMemStore) RollupMetrics(context.Context, time.Time) (int64, error) { return 0, nil }
func (s *metricsMemStore) PruneMetrics(context.Context, time.Time) (int64, error)  { return 0, nil }
func (s *metricsMemStore) MetricSeries(context.Context, []string, time.Time, time.Time, int) (map[string][]proto.MetricPoint, error) {
	return nil, nil
}

type batchMetricsStore struct{ *metricsMemStore }

func (s batchMetricsStore) InsertMetrics(_ context.Context, samples []db.MachineSample) ([]error, error) {
	s.record(samples...)
	return nil, nil
}

// Six machines' samples of a tick are stored in one write (one transaction: one commit and WAL flush), what came
// before the manager stopped is stored, and a store without InsertMetrics gets one InsertMetric a sample.
func TestMetricsStoredATickAtOnce(t *testing.T) {
	for _, batched := range []bool{true, false} {
		st := &metricsMemStore{memStore: newMemStore(), stored: make(chan struct{}, 64)}
		var store Store = st
		if batched {
			store = batchMetricsStore{st}
		}
		d := New(Options{Store: store})
		d.metricsWait = time.Second // the first tick waits that long; the next ones end once the six machines sent
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.writeMetrics(ctx, d.metricsStore())
		}()
		writes := 0
		send := func(tick int64, machines int, wait bool) {
			for m := range machines {
				b, _ := json.Marshal(map[string]any{"sample": proto.MetricSample{T: 1_000_000 + 10*tick, MemTotal: 1}})
				d.onMetrics(fmt.Sprintf("m%d", m), b)
			}
			if !wait {
				return
			}
			if batched {
				writes++
			} else {
				writes += machines
			}
			for {
				st.mu.Lock()
				n := len(st.writes)
				st.mu.Unlock()
				if n >= writes {
					break
				}
				select {
				case <-st.stored:
				case <-time.After(10 * time.Second):
					t.Fatalf("batched %v: tick %d not stored: %v", batched, tick, st.writes)
				}
			}
		}
		for tick := range int64(3) {
			send(tick, 6, true)
		}
		send(3, 2, false) // then the manager stops before the other four came
		cancel()
		<-done
		sizes := []int{}
		for _, w := range st.writes {
			sizes = append(sizes, len(w))
		}
		want := []int{6, 6, 6, 2}
		if !batched {
			want = slices.Repeat([]int{1}, 20)
		}
		if !slices.Equal(sizes, want) {
			t.Fatalf("batched %v: writes of %v samples, want %v", batched, sizes, want)
		}
	}
}

func TestUsageSummary(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	now := time.Date(2026, 10, 29, 9, 0, 30, 0, loc) // 30 s into the minute
	minute := now.Unix() / 60 * 60
	var book usageBook
	report := func(machine string, full bool, buckets ...proto.UsageBucket) {
		b, _ := json.Marshal(map[string]any{"buckets": buckets, "full": full, "load": 4.0, "cpus": 8})
		book.apply(machine, b, now)
	}
	report("a", true,
		proto.UsageBucket{Minute: minute, Model: "claude-opus-5-5", Input: 1_000_000, Output: 100, Requests: 1},
		proto.UsageBucket{Minute: minute - 60, Model: "claude-opus-5-5", CacheWrite: 1_000_000, Output: 400, Requests: 2},
		proto.UsageBucket{Minute: minute - 10*3600, Model: "claude-opus-5-5", Output: 999, Requests: 9}, // yesterday here
	)
	report("b", true, proto.UsageBucket{Minute: minute, Model: "mystery", CacheRead: 50, Output: 250, Requests: 1})
	// A later report replaces a minute's total instead of adding to it.
	report("b", false, proto.UsageBucket{Minute: minute, Model: "mystery", CacheRead: 50, Output: 500, Requests: 2})

	s := book.summary(now, loc)
	if s.Requests != 5 || s.Output != 1000 || s.Tokens != 2_001_050 {
		t.Fatalf("today = %+v", s)
	}
	// 1000 output tokens over the last 2.5 minutes.
	if s.TPM != 400 || len(s.Machines) != 2 || s.Machines[0].Load != 0.5 {
		t.Fatalf("tpm / machines = %d %+v", s.TPM, s.Machines)
	}
	// A full report (the node reconnected) replaces everything.
	report("b", true)
	if s := book.summary(now, loc); s.Output != 500 {
		t.Fatalf("after a full report: %+v", s)
	}
}

// A machine taken out of hub.json is no longer in the usage summary nor has live metrics, and a late notification
// from its stopping connection does not bring it back.
func TestUsageAndMetricsForgetARemovedMachine(t *testing.T) {
	d := newTestDevices(t)
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.ctx = ctx
	d.mu.Unlock()
	t.Cleanup(func() { cancel(); d.workers.Wait() })
	entry := func(id string) json.RawMessage {
		b, _ := json.Marshal(MachineConfig{ID: id, Name: id, Kind: kindLink})
		return b
	}
	d.Configure(devicewire.Config{Machines: []json.RawMessage{entry("kept"), entry("gone")}})
	gone, kept := d.cfgMachine("gone"), d.cfgMachine("kept")
	if gone == nil || kept == nil {
		t.Fatal("machines not started")
	}
	cpu := 1.0
	sample, _ := json.Marshal(map[string]any{"sample": proto.MetricSample{T: time.Now().Unix(), CPU: &cpu}})
	usage, _ := json.Marshal(map[string]any{"full": true, "load": 1.0, "cpus": 2,
		"buckets": []proto.UsageBucket{{Minute: time.Now().Unix() / 60 * 60, Model: "m", Output: 10, Requests: 1}}})
	for _, m := range []*Machine{gone, kept} {
		m.onEvent("metrics", sample)
		m.onEvent("usage", usage)
	}
	ids := func() []string {
		var out []string
		for _, m := range d.Usage().Machines {
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids(); len(got) != 2 {
		t.Fatalf("usage machines before = %v", got)
	}

	d.Configure(devicewire.Config{Machines: []json.RawMessage{entry("kept")}})
	gone.onEvent("metrics", sample) // late, from the connection being let go
	gone.onEvent("usage", usage)
	if got := ids(); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("usage machines after removal = %v", got)
	}
	if d.metrics.now("gone", time.Now()) != nil {
		t.Fatal("the removed machine still has live metrics")
	}
	if d.metrics.now("kept", time.Now()) == nil {
		t.Fatal("the kept machine lost its live metrics")
	}
	// The assistant needs the last sample even when it is no longer current, and no data for a removed machine.
	d.onMetrics("kept", []byte(`{"sample":{"t":1}}`))
	d.onSystem("kept", &proto.System{OS: "linux", Cores: 4})
	machine, latest, outdated := d.MachineResources("kept")
	if machine == nil || machine.System == nil || machine.System.Cores != 4 || latest == nil || latest.T != 1 || outdated != nil {
		t.Fatalf("retained facts: machine=%+v latest=%+v outdated=%v", machine, latest, outdated)
	}
	if machine, latest, outdated := d.MachineResources("gone"); machine != nil || latest != nil || outdated != nil {
		t.Fatalf("removed machine still listed: %+v %+v %v", machine, latest, outdated)
	}
}

// An entry absorbed by another one (the same computer) answers with that computer's latest sample: samples are kept
// under the id of the machine whose connection sent them, not the id the caller asked by.
func TestMachineResourcesOfAnAbsorbedEntry(t *testing.T) {
	d := newTestDevices(t)
	owner := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	link := newMachine(d, MachineConfig{ID: "box-link", Name: "box-link", Kind: kindLink})
	d.addEntry(owner)
	d.addEntry(link)
	f := &d.fleet
	f.mu.Lock()
	f.init()
	f.absorbed[link] = owner
	f.mu.Unlock()
	d.onMetrics("box", []byte(`{"sample":{"t":7}}`))

	for _, id := range []string{"box", "box-link"} {
		machine, latest, _ := d.MachineResources(id)
		if machine == nil || machine.ID != "box" || latest == nil || latest.T != 7 {
			t.Fatalf("MachineResources(%q): machine=%+v latest=%+v", id, machine, latest)
		}
	}
}

// newCancelledOnCleanup is newTestDevices whose machines are stopped and waited for when the test ends.
func newCancelledOnCleanup(t *testing.T) *Devices {
	d := newTestDevices(t)
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.ctx = ctx
	d.mu.Unlock()
	t.Cleanup(func() { cancel(); d.workers.Wait() })
	return d
}

// usageIDs are the machines the usage summary lists.
func usageIDs(d *Devices) []string {
	var out []string
	for _, m := range d.Usage().Machines {
		out = append(out, m.ID)
	}
	return out
}

func testMetricsAndUsage(t *testing.T) (sample, usage json.RawMessage) {
	t.Helper()
	cpu := 1.0
	sample, _ = json.Marshal(map[string]any{"sample": proto.MetricSample{T: time.Now().Unix(), CPU: &cpu}})
	usage, _ = json.Marshal(map[string]any{"full": true, "load": 1.0, "cpus": 2,
		"buckets": []proto.UsageBucket{{Minute: time.Now().Unix() / 60 * 60, Model: "m", Output: 10, Requests: 1}}})
	return sample, usage
}

func memberMesh(hubID string, ids ...string) *devicewire.Mesh {
	rec := devicewire.MemberRecord{HubID: hubID}
	for _, id := range ids {
		cfg, _ := json.Marshal(MachineConfig{ID: id, Name: id})
		rec.Machines = append(rec.Machines, devicewire.RecMachine{ID: id, Cfg: cfg, Kind: kindHub})
	}
	return &devicewire.Mesh{Members: []devicewire.MemberRecord{rec}}
}

// A member's computer no longer reachable through any hub is no longer in the usage summary nor has live metrics,
// and a late notification from its stopping connection does not bring it back.
func TestUsageAndMetricsForgetAnUnreachableMemberComputer(t *testing.T) {
	d := newCancelledOnCleanup(t)
	base := d.config()
	cfg := base
	cfg.Mesh = memberMesh("hub-member", "pc")
	d.Configure(cfg)
	pc := d.machineByID("pc")
	if pc == nil || !pc.derived {
		t.Fatalf("member computer not derived: %v", pc)
	}
	sample, usage := testMetricsAndUsage(t)
	pc.onEvent("metrics", sample)
	pc.onEvent("usage", usage)
	if got := usageIDs(d); len(got) != 1 || got[0] != "pc" {
		t.Fatalf("usage machines before = %v", got)
	}

	cfg = base
	cfg.Mesh = &devicewire.Mesh{}
	d.Configure(cfg)
	if d.machineByID("pc") != nil {
		t.Fatal("the member computer is still a machine")
	}
	pc.onEvent("metrics", sample) // late, from the connection being let go
	pc.onEvent("usage", usage)
	if got := usageIDs(d); len(got) != 0 {
		t.Fatalf("usage machines after it went = %v", got)
	}
	if d.metrics.now("pc", time.Now()) != nil {
		t.Fatal("the unreachable member computer still has live metrics")
	}
}

// A member's computer that went while a hub.json entry took its id keeps its usage and live metrics: the id still
// stands for a machine.
func TestForgetMachineKeepsAnIDStillInUse(t *testing.T) {
	d := newCancelledOnCleanup(t)
	base := d.config()
	cfg := base
	cfg.Mesh = memberMesh("hub-member", "pc")
	d.Configure(cfg)
	pc := d.machineByID("pc")
	if pc == nil || !pc.derived {
		t.Fatalf("member computer not derived: %v", pc)
	}
	sample, usage := testMetricsAndUsage(t)
	pc.onEvent("metrics", sample)
	pc.onEvent("usage", usage)

	entry, _ := json.Marshal(MachineConfig{ID: "pc", Name: "pc", Kind: kindLink})
	cfg = base
	cfg.Mesh = &devicewire.Mesh{}
	cfg.Machines = []json.RawMessage{entry}
	d.Configure(cfg)
	if m := d.machineByID("pc"); m == nil || m.derived {
		t.Fatalf("the hub.json entry does not stand for the id: %v", m)
	}
	if got := usageIDs(d); len(got) != 1 || got[0] != "pc" {
		t.Fatalf("usage machines after the entry took the id = %v", got)
	}
	if d.metrics.now("pc", time.Now()) == nil {
		t.Fatal("the id still in use lost its live metrics")
	}
}

// The report names every entry the manager has with the entry itself, so the hub keeps what each manager has (hub
// machine_entries.go).
func TestTheReportCarriesEachEntry(t *testing.T) {
	d := newTestDevices(t)
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.ctx = ctx
	d.mu.Unlock()
	t.Cleanup(func() { cancel(); d.workers.Wait() })
	entry := func(id string) json.RawMessage {
		b, _ := json.Marshal(MachineConfig{ID: id, Name: "Name of " + id, Kind: kindLink})
		return b
	}
	d.Configure(devicewire.Config{Machines: []json.RawMessage{entry("one"), entry("two")}})
	got := map[string]string{}
	for _, e := range d.states().Entries {
		got[e.ID] = string(e.Cfg)
	}
	if len(got) != 2 || got["one"] != string(entry("one")) || got["two"] != string(entry("two")) {
		t.Fatalf("entries: %v", got)
	}
}
