package devices

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Machine monitoring (docs/server/machines.md) is the manager's: every node sends a sample every 10 s (sysmon); the manager
// keeps the latest in memory for the live values and every sample in Postgres for the charts: raw for the latest
// hour, then one row a minute, nothing older than a day. What each machine is (node.info's system) is kept too, for
// when it is offline, and goes into its devices.state. The token usage the nodes meter (node/usage.go) is summed up
// here as well, for the Dynamic Island and GET /api/v1/usage. The hub relays the routes (devicewire.MetricsRoutes).

const (
	metricsRawFor   = time.Hour
	metricsKeep     = 24*time.Hour + 10*time.Minute // a 24-hour chart starts full
	metricsTidyEach = time.Minute
	// metricsFresh: a latest sample older than this is not "now" any more (the node stopped sending).
	metricsFresh = 3 * proto.MetricEvery

	// metricsQueue: samples waiting for Postgres (minutes of a few machines); more are dropped, with a log line.
	metricsQueue = 1024
	// metricsGather: how long the writer waits after a tick's first sample for the other machines' samples of that
	// tick (the nodes sample on the tick's boundary, node/metrics.go), to store them in one transaction.
	metricsGather       = 2 * time.Second
	metricsWriteTimeout = 10 * time.Second
	// metricsStopWrite: how long the samples that came before the manager stopped may take to be stored.
	metricsStopWrite = 2 * time.Second
)

type metricsStore interface {
	PutMachineSystem(ctx context.Context, machine string, s proto.System) error
	MachineSystems(ctx context.Context) (map[string]proto.System, error)
	InsertMetric(ctx context.Context, machine string, s proto.MetricSample) error
	RollupMetrics(ctx context.Context, before time.Time) (int64, error)
	PruneMetrics(ctx context.Context, before time.Time) (int64, error)
	MetricSeries(ctx context.Context, machines []string, from, to time.Time, step int) (map[string][]proto.MetricPoint, error)
}

// metricsBatchStore stores several machines' samples in one transaction (db.InsertMetrics): one commit, so one WAL
// flush, a tick instead of one a machine. A store without it gets one InsertMetric a sample.
type metricsBatchStore interface {
	InsertMetrics(ctx context.Context, samples []db.MachineSample) (rejected []error, err error)
}

type metricsBook struct {
	mu      sync.Mutex
	latest  map[string]proto.MetricSample
	systems map[string]proto.System
}

func (b *metricsBook) system(machine string) *proto.System {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.systems[machine]; ok {
		return &s
	}
	return nil
}

func (b *metricsBook) setSystem(machine string, s proto.System) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.systems == nil {
		b.systems = map[string]proto.System{}
	}
	b.systems[machine] = s
}

// now is a machine's latest sample if it is recent.
func (b *metricsBook) now(machine string, at time.Time) *proto.MetricSample {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.latest[machine]
	if !ok || at.Sub(time.Unix(s.T, 0)) > metricsFresh {
		return nil
	}
	return &s
}

// MachineResources returns the retained machine snapshot and latest sample, including stale samples so callers can report their age.
// A nil result means the machine is no longer managed here. It does not query the node or the metrics history.
func (d *Devices) MachineResources(id string) (*proto.Machine, *proto.MetricSample, *bool) {
	m := d.machineByID(id)
	if m == nil {
		return nil, nil, nil
	}
	machine := m.snapshot()
	m.mu.Lock()
	info, online := m.info, m.online
	m.mu.Unlock()
	var outdated *bool
	if online {
		target := d.hubVersion()
		if m.cfg.Kind != "local" {
			target = d.nodeTarget(info)
		}
		if machine.NodeIncompatible == "node_protocol_old" {
			v := true
			outdated = &v
		} else if machine.NodeIncompatible == "" && target != "" && target != "dev" && target == info.Version {
			v := false
			outdated = &v
		}
	}
	d.metrics.mu.Lock()
	sample, ok := d.metrics.latest[id]
	d.metrics.mu.Unlock()
	if !ok {
		return &machine, nil, outdated
	}
	return &machine, &sample, outdated
}

// forget drops a machine's latest sample (it is no longer one of the machines). What it is (systems) stays: it is
// kept in Postgres as well and is only shown for a machine the apps list.
func (b *metricsBook) forget(machine string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.latest, machine)
}

// forgetMachine drops what is kept in memory of a machine that went away (no longer in hub.json, or a member's
// computer no longer reachable), so the island does not list it for another day. Kept if another machine still
// answers to the id (a hub.json entry became a member's computer, or the other way round).
func (d *Devices) forgetMachine(id string) {
	if d.machineByID(id) != nil {
		return
	}
	d.usage.forget(id)
	d.metrics.forget(id)
}

func (d *Devices) metricsStore() metricsStore {
	if s, ok := d.opts.Store.(metricsStore); ok {
		return s
	}
	return nil
}

// onSystem keeps what a node said it is (node.info) for when it is offline.
func (d *Devices) onSystem(machine string, s *proto.System) {
	if s == nil {
		return
	}
	d.metrics.setSystem(machine, *s)
	if st := d.metricsStore(); st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := st.PutMachineSystem(ctx, machine, *s); err != nil {
			d.logf("[%s] save system: %v", machine, err)
		}
	}
}

// onMetrics takes a node's "metrics" notification.
func (d *Devices) onMetrics(machine string, params json.RawMessage) {
	var p struct {
		Sample proto.MetricSample `json:"sample"`
	}
	if json.Unmarshal(params, &p) != nil || p.Sample.T == 0 {
		return
	}
	d.metrics.mu.Lock()
	if d.metrics.latest == nil {
		d.metrics.latest = map[string]proto.MetricSample{}
	}
	d.metrics.latest[machine] = p.Sample
	d.metrics.mu.Unlock()
	if d.metricsStore() == nil {
		return
	}
	s := db.MachineSample{Machine: machine, Sample: p.Sample}
	s.Sample.Cores = nil // the latest sample's only, not stored
	select {
	case d.metricWrites <- s: // written by writeMetrics, not on the node's read loop
	default: // Postgres is far behind
		d.logf("[%s] metrics: a sample not saved, the queue is full", machine)
	}
}

// writeMetrics stores the queued samples, a tick's samples of all machines in one transaction (the nodes sample on
// the tick's boundary, node/metrics.go): after a tick's first sample it waits until every machine that sent one
// lately (metricsFresh) has sent this tick's, at most d.metricsWait; with none lately (the first sample, or all were
// quiet) it waits that long for whichever come. A store without InsertMetrics gets one InsertMetric each. When ctx
// ends it stores what has come, then returns.
func (d *Devices) writeMetrics(ctx context.Context, st metricsStore) {
	seen := map[string]time.Time{} // when each machine's last sample came
	for {
		var first db.MachineSample
		select {
		case <-ctx.Done():
			d.saveMetricsAtStop(st, nil)
			return
		case first = <-d.metricWrites:
		}
		now := time.Now()
		waiting := map[string]bool{} // the machines that sent lately and not yet in this batch
		for m, at := range seen {
			if now.Sub(at) > metricsFresh {
				delete(seen, m) // offline, or no longer one of the machines
			} else {
				waiting[m] = true
			}
		}
		unknown := len(waiting) == 0
		batch := []db.MachineSample{first}
		seen[first.Machine] = now
		delete(waiting, first.Machine)
		gather := time.NewTimer(d.metricsWait)
	collect:
		for unknown || len(waiting) > 0 {
			select {
			case s := <-d.metricWrites:
				batch = append(batch, s)
				seen[s.Machine] = time.Now()
				delete(waiting, s.Machine)
			case <-gather.C:
				break collect
			case <-ctx.Done():
				gather.Stop()
				d.saveMetricsAtStop(st, batch)
				return
			}
		}
		gather.Stop()
		// Not cut off when the manager stops, only bounded: metricsStopWrite more, as for what is still queued.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsWriteTimeout)
		stop := context.AfterFunc(ctx, func() { time.AfterFunc(metricsStopWrite, cancel) })
		d.saveMetrics(wctx, st, batch)
		stop()
		cancel()
	}
}

// saveMetricsAtStop stores batch and whatever else is queued, within metricsStopWrite (the manager is stopping).
func (d *Devices) saveMetricsAtStop(st metricsStore, batch []db.MachineSample) {
	for drained := false; !drained; {
		select {
		case s := <-d.metricWrites:
			batch = append(batch, s)
		default:
			drained = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsStopWrite)
	defer cancel()
	d.saveMetrics(ctx, st, batch)
}

// saveMetrics stores samples, in one transaction when the store can; a sample Postgres rejects is logged and costs
// only itself.
func (d *Devices) saveMetrics(ctx context.Context, st metricsStore, samples []db.MachineSample) {
	if len(samples) == 0 {
		return
	}
	bs, ok := st.(metricsBatchStore)
	if !ok {
		for _, s := range samples {
			if err := st.InsertMetric(ctx, s.Machine, s.Sample); err != nil {
				d.logf("[%s] save metrics: %v", s.Machine, err)
			}
		}
		return
	}
	rejected, err := bs.InsertMetrics(ctx, samples)
	for i, s := range samples {
		if e := err; e != nil || rejected != nil && rejected[i] != nil {
			if e == nil {
				e = rejected[i]
			}
			d.logf("[%s] save metrics: %v", s.Machine, e)
		}
	}
}

// metricsLoop loads the machines' systems, then each minute rolls up the raw rows past the hour and drops what is
// older than a day.
func (d *Devices) metricsLoop(ctx context.Context) {
	st := d.metricsStore()
	if st == nil {
		return
	}
	if systems, err := st.MachineSystems(ctx); err == nil {
		for id, s := range systems {
			if d.metrics.system(id) == nil { // a node that connected meanwhile is newer
				d.metrics.setSystem(id, s)
			}
		}
		d.changed()
	}
	t := time.NewTicker(metricsTidyEach)
	defer t.Stop()
	for {
		now := time.Now()
		if _, err := st.RollupMetrics(ctx, now.Add(-metricsRawFor)); err != nil && ctx.Err() == nil {
			d.logf("metrics rollup: %v", err)
		}
		if _, err := st.PruneMetrics(ctx, now.Add(-metricsKeep)); err != nil && ctx.Err() == nil {
			d.logf("metrics prune: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// metricsRanges: what a chart can show, and its step (about 360 points each). The 1-hour chart is raw.
var metricsRanges = map[string]struct {
	span time.Duration
	step int
}{
	"1h":  {time.Hour, db.RawMetricStep},
	"6h":  {6 * time.Hour, 60},
	"24h": {24 * time.Hour, 240},
}

// withGaps puts a break where a machine sent nothing for more than three steps (offline, the hub restarting): a
// line drawn across it would claim values nobody measured. Short holes (a missed sample) are bridged. A break also
// ends the series when the latest point is that old (the machine is offline now).
func withGaps(points []proto.MetricPoint, step int, end time.Time) []proto.MetricPoint {
	limit := int64(3 * step)
	out := make([]proto.MetricPoint, 0, len(points)+2)
	for i, p := range points {
		if i > 0 && p.T-points[i-1].T > limit {
			out = append(out, proto.MetricPoint{T: points[i-1].T + int64(step), Gap: true})
		}
		out = append(out, roundPoint(p))
	}
	if n := len(points); n > 0 && end.Unix()-points[n-1].T > limit+int64(step) {
		out = append(out, proto.MetricPoint{T: points[n-1].T + int64(step), Gap: true})
	}
	return out
}

func roundPoint(p proto.MetricPoint) proto.MetricPoint {
	r := func(v *float64, digits float64) *float64 {
		if v == nil {
			return nil
		}
		x := math.Round(*v*digits) / digits
		return &x
	}
	p.CPU, p.Load = r(p.CPU, 10), r(p.Load, 100)
	p.Mem, p.Swap, p.Rx, p.Tx, p.Disk = r(p.Mem, 1), r(p.Swap, 1), r(p.Rx, 1), r(p.Tx, 1), r(p.Disk, 1)
	return p
}

// MachineMetrics is a machine's page of history (GET /api/v1/machines/{id}/metrics) or its dashboard card.
type MachineMetrics struct {
	MachineID string              `json:"machine_id"`
	Online    bool                `json:"online"`
	System    *proto.System       `json:"system,omitempty"`
	Latest    *proto.MetricSample `json:"latest,omitempty"` // nil when the node has not sent anything lately
	Points    []proto.MetricPoint `json:"points"`
	// Usage: its tokens, on the dashboard only (usage.go); nil there when its node never reported usage.
	Usage *UsageCard `json:"usage,omitempty"`
}

func (d *Devices) machineMetrics(ctx context.Context, ids []string, span time.Duration, step int, now time.Time) ([]MachineMetrics, error) {
	series := map[string][]proto.MetricPoint{}
	if st := d.metricsStore(); st != nil {
		// From a step boundary, so the first step is not a partial average.
		from := now.Add(-span).Truncate(time.Duration(step) * time.Second)
		var err error
		if series, err = st.MetricSeries(ctx, ids, from, now.Add(time.Minute), step); err != nil {
			return nil, err
		}
	}
	out := make([]MachineMetrics, 0, len(ids))
	for _, id := range ids {
		m := d.machineByID(id)
		if m == nil {
			continue
		}
		id = m.cfg.ID
		out = append(out, MachineMetrics{MachineID: id, Online: m.snapshot().Online, System: d.metrics.system(id),
			Latest: d.metrics.now(id, now), Points: withGaps(series[id], step, now)})
	}
	return out, nil
}

// metricsRoutes are the monitoring's routes (devicewire.MetricsRoutes) on mux.
func (d *Devices) metricsRoutes(mux *http.ServeMux) {
	// The dashboard: every machine's latest sample and its last hour by minute (the sparklines).
	mux.HandleFunc("GET /api/v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		machines := d.machinesNow()
		ids := make([]string, 0, len(machines))
		for _, m := range machines {
			ids = append(ids, m.cfg.ID)
		}
		now := time.Now()
		list, err := d.machineMetrics(r.Context(), ids, time.Hour, 60, now)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		loc := d.location()
		for i := range list {
			list[i].Usage = d.usage.card(list[i].MachineID, now, loc)
		}
		writeJSON(w, map[string]any{"machines": list, "step": 60})
	})
	mux.HandleFunc("GET /api/v1/machines/{id}/metrics", func(w http.ResponseWriter, r *http.Request) {
		m := d.machineByID(r.PathValue("id"))
		if m == nil {
			writeErr(w, http.StatusNotFound, errors.New("no such machine"))
			return
		}
		id := m.cfg.ID
		name := r.URL.Query().Get("range")
		if name == "" {
			name = "1h"
		}
		rg, ok := metricsRanges[name]
		if !ok {
			writeErr(w, http.StatusBadRequest, errors.New("range is 1h, 6h or 24h"))
			return
		}
		list, err := d.machineMetrics(r.Context(), []string{id}, rg.span, rg.step, time.Now())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if len(list) == 0 { // removed or unpaired while the history was read
			writeErr(w, http.StatusNotFound, errors.New("no such machine"))
			return
		}
		writeJSON(w, map[string]any{"metrics": list[0], "range": name, "step": rg.step})
	})
	mux.HandleFunc("GET /api/v1/usage", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"usage": d.Usage()})
	})
}

// ---- token usage ----

// Token usage across the machines, for the Dynamic Island (docs/apple/live-activity.md): every node meters its
// transcripts per minute and model (node/usage.go) and reports changed minutes; the manager keeps the last day of
// them, in memory only (a new node connection resends its whole window), and sums them up.

type usageKey struct {
	minute   int64
	model    string
	launcher string
}

type machineUsage struct {
	buckets map[usageKey]proto.UsageBucket
	load    float64
	cpus    int
	at      time.Time // last report
	// launchers: the node names the launcher of its buckets (an older one does not, usage.go's LaunchersKnown).
	launchers bool
}

type usageBook struct {
	mu       sync.Mutex
	machines map[string]*machineUsage
}

// apply takes a node's "usage" notification.
func (u *usageBook) apply(machine string, params json.RawMessage, now time.Time) bool {
	var p struct {
		Buckets   []proto.UsageBucket `json:"buckets"`
		Full      bool                `json:"full"`
		Load      float64             `json:"load"`
		CPUs      int                 `json:"cpus"`
		Launchers bool                `json:"launchers"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.machines == nil {
		u.machines = map[string]*machineUsage{}
	}
	m := u.machines[machine]
	if m == nil || p.Full {
		m = &machineUsage{buckets: map[usageKey]proto.UsageBucket{}}
		u.machines[machine] = m
	}
	for _, b := range p.Buckets {
		m.buckets[usageKey{b.Minute, b.Model, b.Launcher}] = b
	}
	m.load, m.cpus, m.at, m.launchers = p.Load, p.CPUs, now, p.Launchers
	cutoff := now.Add(-26*time.Hour).Unix() / 60 * 60
	for k := range m.buckets {
		if k.minute < cutoff {
			delete(m.buckets, k)
		}
	}
	return true
}

// forget drops a machine's usage: it is no longer one of the machines.
func (u *usageBook) forget(machine string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.machines, machine)
}

// UsageSummary is what the island and the app show.
type UsageSummary = assistantwire.UsageSummary

// MachineUsage is one machine's share and load.
type MachineUsage = assistantwire.MachineUsage

// tpmSpan: TPM is averaged from the start of the minute two minutes ago until now (2–3 minutes), so it neither
// drops to 0 between replies nor lags much behind.
const tpmSpan = 2

func (u *usageBook) summary(now time.Time, loc *time.Location) UsageSummary {
	y, mo, d := now.In(loc).Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, loc).Unix()
	from := now.Unix()/60*60 - tpmSpan*60
	elapsed := float64(now.Unix()-from) / 60
	out := UsageSummary{Machines: []MachineUsage{}}
	u.mu.Lock()
	defer u.mu.Unlock()
	for id, m := range u.machines {
		mu := MachineUsage{ID: id}
		if now.Sub(m.at) < 2*time.Minute && m.cpus > 0 {
			mu.Load = m.load / float64(m.cpus)
		}
		var recent int64
		for k, b := range m.buckets {
			if k.minute >= from {
				recent += b.Output
			}
			if k.minute < today {
				continue
			}
			tokens := b.Input + b.CacheWrite + b.CacheRead + b.Output
			mu.Tokens += tokens
			out.Tokens += tokens
			out.Output += b.Output
			out.Requests += b.Requests
			out.Input += b.Input
			out.CacheWrite += b.CacheWrite
			out.CacheRead += b.CacheRead
		}
		mu.TPM = int64(float64(recent) / elapsed)
		out.TPM += mu.TPM
		out.Machines = append(out.Machines, mu)
	}
	sort.Slice(out.Machines, func(i, j int) bool { return out.Machines[i].ID < out.Machines[j].ID })
	return out
}

// Usage is the token usage summed up now, "today" in the user's zone (Options.Location).
func (d *Devices) Usage() UsageSummary {
	return d.usage.summary(time.Now(), d.location())
}

// location is the user's time zone (Options.Location), for "today".
func (d *Devices) location() *time.Location {
	if d.opts.Location != nil {
		if l := d.opts.Location(); l != nil {
			return l
		}
	}
	return time.Local
}
