package extmgr

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension/host"
)

// ---- all the extensions' ----

// extensionMetricsStore keeps the closed buckets (the database).
type extensionMetricsStore interface {
	PutExtensionMetrics(ctx context.Context, extension string, start time.Time, data json.RawMessage) error
	ExtensionMetrics(ctx context.Context, extension string, since time.Time) ([]db.ExtensionMetricsRow, error)
	DeleteExtensionMetricsBefore(ctx context.Context, before time.Time) error
}

var _ extensionMetricsStore = (*db.DB)(nil)

// extensionMetrics are every extension's.
type extensionMetrics struct {
	now    func() time.Time
	redact func(string) string // the hub's own credentials, whatever line they turn up in

	mu   sync.Mutex
	byID map[string]*extensionStats
}

func newExtensionMetrics() *extensionMetrics {
	return &extensionMetrics{now: time.Now, byID: map[string]*extensionStats{}}
}

// of is extension id's, made the first time.
func (m *extensionMetrics) of(id string) *extensionStats {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.byID[id]
	if s == nil {
		s = &extensionStats{id: id, now: m.now, redact: func(line string) string {
			if m.redact != nil { // set once, before the extensions start
				return m.redact(line)
			}
			return line
		}}
		m.byID[id] = s
	}
	return s
}

// known is the extensions with counts.
func (m *extensionMetrics) known() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.byID))
	for id := range m.byID {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// hubLine keeps a line of the hub's own log that is about an extension ("plugin <id>: …"), with its log.
func (m *extensionMetrics) hubLine(line string) {
	if m == nil {
		return
	}
	rest, ok := strings.CutPrefix(line, "plugin ")
	if !ok {
		if rest, ok = strings.CutPrefix(line, "extension "); !ok {
			return
		}
	}
	id, text, ok := strings.Cut(rest, ": ")
	if !ok || id == "" || strings.ContainsAny(id, " \"") {
		return
	}
	m.mu.Lock()
	s := m.byID[id]
	m.mu.Unlock()
	if s != nil { // only for an extension the hub runs: no stats made for whatever follows "plugin "
		s.Log(host.LogHub, text)
	}
}

// metrics is extension id's over window ("1h", "24h", "7d"): the memory's buckets, with the database's for 7 days.
func (m *extensionMetrics) metrics(ctx context.Context, store extensionMetricsStore, id, window string) ExtensionMetrics {
	w, ok := metricsWindows[window]
	if !ok {
		window, w = "24h", metricsWindows["24h"]
	}
	now := m.now()
	to := now.Truncate(w.step).Add(w.step)
	from := to.Add(-w.span)
	s := m.of(id)
	s.mu.Lock()
	rows := make([]*metricsBucketRow, 0, len(s.buckets))
	have := map[time.Time]bool{}
	for _, b := range s.buckets {
		c := &metricsBucketRow{start: b.start}
		accumulate(&c.data, &b.data)
		rows = append(rows, c)
		have[b.start] = true
	}
	exits := slices.Clone(s.exits)
	s.mu.Unlock()
	if store != nil && w.span > metricsInMemory {
		if saved, err := store.ExtensionMetrics(ctx, id, from); err == nil {
			for _, r := range saved {
				if have[r.Start] {
					continue
				}
				var d metricsBucketData
				if json.Unmarshal(r.Data, &d) == nil {
					rows = append(rows, &metricsBucketRow{start: r.Start, data: d})
				}
			}
		}
	}
	return report(id, window, rows, from, to, w.step, exits)
}

// flush writes the buckets that changed since the last flush and are closed (and the open one too when all), and
// drops what is older than the database keeps.
func (m *extensionMetrics) flush(ctx context.Context, store extensionMetricsStore, all bool) error {
	if m == nil || store == nil {
		return nil
	}
	open := m.now().Truncate(metricsBucket)
	var firstErr error
	for _, id := range m.known() {
		s := m.of(id)
		type pending struct {
			row  *metricsBucketRow
			data []byte
		}
		var todo []pending
		s.mu.Lock()
		for _, b := range s.buckets {
			if b.flushed || (!all && !b.start.Before(open)) {
				continue
			}
			data, err := json.Marshal(b.data)
			if err != nil {
				continue
			}
			todo = append(todo, pending{b, data})
		}
		s.mu.Unlock()
		for _, p := range todo {
			if err := store.PutExtensionMetrics(ctx, id, p.row.start, p.data); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			s.mu.Lock()
			if d, _ := json.Marshal(p.row.data); string(d) == string(p.data) { // not counted into since
				p.row.flushed = true
			}
			s.mu.Unlock()
		}
	}
	if err := store.DeleteExtensionMetricsBefore(ctx, m.now().Add(-metricsKept)); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// load takes the database's buckets of the last 24 hours into memory (after a restart), as flushed.
func (m *extensionMetrics) load(ctx context.Context, store extensionMetricsStore, ids []string) {
	if m == nil || store == nil {
		return
	}
	since := m.now().Add(-metricsInMemory)
	for _, id := range ids {
		rows, err := store.ExtensionMetrics(ctx, id, since)
		if err != nil || len(rows) == 0 {
			continue
		}
		s := m.of(id)
		s.mu.Lock()
		have := map[time.Time]bool{}
		for _, b := range s.buckets {
			have[b.start] = true
		}
		for _, r := range rows {
			var d metricsBucketData
			if have[r.Start] || json.Unmarshal(r.Data, &d) != nil {
				continue
			}
			s.buckets = append(s.buckets, &metricsBucketRow{start: r.Start, data: d, flushed: true})
		}
		slices.SortFunc(s.buckets, func(a, b *metricsBucketRow) int { return a.start.Compare(b.start) })
		s.mu.Unlock()
	}
}

// ExtensionOverview is one extension's line of GET /api/v1/admin/extensions/-/metrics.
type ExtensionOverview struct {
	ID       string    `json:"id"`
	Calls    int64     `json:"calls"`
	Errors   int64     `json:"errors"`
	Rate     float64   `json:"error_rate"`
	P95      *float64  `json:"p95_ms"`
	Starts   int64     `json:"starts"`
	Exits    int64     `json:"exits"`
	LastExit *ExitNote `json:"last_exit,omitempty"`
}

// LegacyRoute is one old path's calls (#1924): the window's from the counts, since the hub started from memory.
type LegacyRoute struct {
	Route      string `json:"route"`
	Calls      int64  `json:"calls"`
	SinceStart int64  `json:"since_start"`
}

// MetricsOverview is GET /api/v1/admin/extensions/-/metrics.
type MetricsOverview struct {
	Window     string              `json:"window"`
	From       time.Time           `json:"from"`
	To         time.Time           `json:"to"`
	Extensions []ExtensionOverview `json:"extensions"`
	// Legacy is the calls under the old paths (/api/v1/plugins…): the compatibility layer goes once they stop.
	Legacy []LegacyRoute `json:"legacy_calls"`
}

// overview is every extension in ids (and every one with counts) over window, with the old paths' calls.
func (m *extensionMetrics) overview(ctx context.Context, store extensionMetricsStore, ids []string, window string, legacy map[string]int64) MetricsOverview {
	all := slices.Clone(ids)
	for _, id := range m.known() {
		if id != legacyStatsID && !slices.Contains(all, id) {
			all = append(all, id)
		}
	}
	slices.Sort(all)
	out := MetricsOverview{Extensions: []ExtensionOverview{}, Legacy: []LegacyRoute{}}
	for _, id := range all {
		r := m.metrics(ctx, store, id, window)
		out.Window, out.From, out.To = r.Window, r.From, r.To
		o := ExtensionOverview{ID: id, Calls: r.Total.Calls, Errors: r.Total.Errors, Rate: r.Total.ErrorRate, P95: r.Total.P95,
			Starts: r.Starts, Exits: r.Exits}
		if n := len(r.RecentExits); n > 0 {
			o.LastExit = &r.RecentExits[n-1]
		}
		out.Extensions = append(out.Extensions, o)
	}
	lr := m.metrics(ctx, store, legacyStatsID, window)
	if out.Window == "" {
		out.Window, out.From, out.To = lr.Window, lr.From, lr.To
	}
	routes := map[string]*LegacyRoute{}
	for route, n := range lr.Counters {
		routes[route] = &LegacyRoute{Route: route, Calls: n}
	}
	for route, n := range legacy {
		if routes[route] == nil {
			routes[route] = &LegacyRoute{Route: route}
		}
		routes[route].SinceStart = n
	}
	for _, r := range routes {
		out.Legacy = append(out.Legacy, *r)
	}
	slices.SortFunc(out.Legacy, func(a, b LegacyRoute) int { return strings.Compare(a.Route, b.Route) })
	return out
}

// extensionMetricsLoop writes the counts to the database once an hour (and when the hub stops), after taking the last
// day's back into memory.
func (h *Manager) extensionMetricsLoop(ctx context.Context) {
	ps := h.extensions
	if ps == nil || ps.stats == nil {
		return
	}
	store, _ := h.store.(extensionMetricsStore)
	if store == nil {
		return
	}
	ids := []string{legacyStatsID}
	for _, p := range ps.reg.List() {
		ids = append(ids, p.Record.ID)
	}
	ps.stats.load(ctx, store, ids)
	t := time.NewTicker(metricsFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := ps.stats.flush(fctx, store, true); err != nil {
				h.logf("extensions: writing the counts: %v", err)
			}
			cancel()
			return
		case <-t.C:
			if err := ps.stats.flush(ctx, store, false); err != nil {
				h.logf("extensions: writing the counts: %v", err)
			}
		}
	}
}
