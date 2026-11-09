package extmgr

import (
	"math"
	"slices"
	"strings"
	"time"
)

// ---- reading the counts ----

// MetricsSummary is the counts over a window, all methods together or one method's.
type MetricsSummary struct {
	Calls     int64            `json:"calls"`
	Errors    int64            `json:"errors"`
	ErrorRate float64          `json:"error_rate"`      // errors / calls, 0 without calls
	P50       *float64         `json:"p50_ms"`          // null without a timed call
	P95       *float64         `json:"p95_ms"`          //
	P99       *float64         `json:"p99_ms"`          //
	Codes     map[string]int64 `json:"codes,omitempty"` // the calls by error code
}

// MethodMetrics is one method's.
type MethodMetrics struct {
	Method string `json:"method"`
	MetricsSummary
}

// MetricsPoint is one step of a window's series.
type MetricsPoint struct {
	At     time.Time `json:"at"`
	Calls  int64     `json:"calls"`
	Errors int64     `json:"errors"`
	P95    *float64  `json:"p95_ms"`
}

// ExtensionMetrics is GET /api/v1/admin/extensions/{id}/metrics.
type ExtensionMetrics struct {
	Extension   string           `json:"extension"`
	Window      string           `json:"window"`
	From        time.Time        `json:"from"`
	To          time.Time        `json:"to"`
	Step        int              `json:"step_seconds"`
	Total       MetricsSummary   `json:"total"`
	Methods     []MethodMetrics  `json:"methods"`
	Series      []MetricsPoint   `json:"series"`
	Starts      int64            `json:"starts"`
	Stops       int64            `json:"stops"`
	Exits       int64            `json:"exits"` // runs that ended in error (each restarted after a backoff)
	Reasons     map[string]int64 `json:"exit_reasons,omitempty"`
	RecentExits []ExitNote       `json:"recent_exits"`
	Counters    map[string]int64 `json:"counters,omitempty"`
}

// metricsWindows are the windows the admin UI asks for, and the step of each one's series.
var metricsWindows = map[string]struct{ span, step time.Duration }{
	"1h":  {time.Hour, 5 * time.Minute},
	"24h": {24 * time.Hour, time.Hour},
	"7d":  {7 * 24 * time.Hour, 6 * time.Hour},
}

// accumulate adds b into acc.
func accumulate(acc *metricsBucketData, b *metricsBucketData) {
	for name, m := range b.Methods {
		if acc.Methods == nil {
			acc.Methods = map[string]*methodCounts{}
		}
		a := acc.Methods[name]
		if a == nil {
			a = &methodCounts{}
			acc.Methods[name] = a
		}
		a.Calls += m.Calls
		a.Errors += m.Errors
		for c, n := range m.Codes {
			if a.Codes == nil {
				a.Codes = map[string]int64{}
			}
			a.Codes[c] += n
		}
		if len(m.Hist) > 0 {
			if a.Hist == nil {
				a.Hist = make([]int64, len(latencyBounds)+1)
			}
			for i, n := range m.Hist {
				if i < len(a.Hist) {
					a.Hist[i] += n
				}
			}
		}
	}
	acc.Starts += b.Starts
	acc.Stops += b.Stops
	acc.Exits += b.Exits
	for r, n := range b.Reasons {
		if acc.Reasons == nil {
			acc.Reasons = map[string]int64{}
		}
		acc.Reasons[r] += n
	}
	for c, n := range b.Counters {
		if acc.Counters == nil {
			acc.Counters = map[string]int64{}
		}
		acc.Counters[c] += n
	}
}

// quantile is the q-th quantile of a latency histogram, interpolated inside its bucket; nil when it is empty.
func quantile(hist []int64, q float64) *float64 {
	var total int64
	for _, n := range hist {
		total += n
	}
	if total == 0 {
		return nil
	}
	rank := q * float64(total)
	var seen float64
	for i, n := range hist {
		if n == 0 {
			continue
		}
		if seen+float64(n) >= rank {
			lo := 0.0
			if i > 0 {
				lo = latencyBounds[i-1]
			}
			if i >= len(latencyBounds) { // slower than the last bound: the bound is all that is known
				v := latencyBounds[len(latencyBounds)-1]
				return &v
			}
			hi := latencyBounds[i]
			v := lo + (hi-lo)*(rank-seen)/float64(n)
			v = math.Round(v*10) / 10
			return &v
		}
		seen += float64(n)
	}
	v := latencyBounds[len(latencyBounds)-1]
	return &v
}

// summarize is the summary of some methods' counts.
func summarize(ms ...*methodCounts) MetricsSummary {
	var out MetricsSummary
	hist := make([]int64, len(latencyBounds)+1)
	for _, m := range ms {
		out.Calls += m.Calls
		out.Errors += m.Errors
		for c, n := range m.Codes {
			if out.Codes == nil {
				out.Codes = map[string]int64{}
			}
			out.Codes[c] += n
		}
		for i, n := range m.Hist {
			if i < len(hist) {
				hist[i] += n
			}
		}
	}
	if out.Calls > 0 {
		out.ErrorRate = math.Round(float64(out.Errors)/float64(out.Calls)*10000) / 10000
	}
	out.P50, out.P95, out.P99 = quantile(hist, 0.5), quantile(hist, 0.95), quantile(hist, 0.99)
	return out
}

func methodsOf(b *metricsBucketData) []*methodCounts {
	out := make([]*methodCounts, 0, len(b.Methods))
	for _, m := range b.Methods {
		out = append(out, m)
	}
	return out
}

// report is the counts of the buckets (oldest first) over [from, to), with a series of step.
func report(id, window string, rows []*metricsBucketRow, from, to time.Time, step time.Duration, exits []ExitNote) ExtensionMetrics {
	out := ExtensionMetrics{Extension: id, Window: window, From: from, To: to, Step: int(step / time.Second),
		Methods: []MethodMetrics{}, Series: []MetricsPoint{}, RecentExits: []ExitNote{}}
	var all metricsBucketData
	steps := map[time.Time]*metricsBucketData{}
	for _, r := range rows {
		if r.start.Before(from) || !r.start.Before(to) {
			continue
		}
		accumulate(&all, &r.data)
		at := from.Add(r.start.Sub(from).Truncate(step))
		if steps[at] == nil {
			steps[at] = &metricsBucketData{}
		}
		accumulate(steps[at], &r.data)
	}
	for at := from; at.Before(to); at = at.Add(step) {
		p := MetricsPoint{At: at}
		if d := steps[at]; d != nil {
			s := summarize(methodsOf(d)...)
			p.Calls, p.Errors, p.P95 = s.Calls, s.Errors, s.P95
		}
		out.Series = append(out.Series, p)
	}
	out.Total = summarize(methodsOf(&all)...)
	for name, m := range all.Methods {
		out.Methods = append(out.Methods, MethodMetrics{Method: name, MetricsSummary: summarize(m)})
	}
	slices.SortFunc(out.Methods, func(a, b MethodMetrics) int {
		if a.Calls != b.Calls {
			return int(b.Calls - a.Calls)
		}
		return strings.Compare(a.Method, b.Method)
	})
	out.Starts, out.Stops, out.Exits, out.Reasons, out.Counters = all.Starts, all.Stops, all.Exits, all.Reasons, all.Counters
	for _, e := range exits {
		if !e.At.Before(from) {
			out.RecentExits = append(out.RecentExits, e)
		}
	}
	return out
}
