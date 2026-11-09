package extmgr

import (
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension/host"
)

// How the extensions are running (docs/extensions/extensions.md "How they run", #1857): the hub counts each extension's calls,
// errors and latency by method (the HTTP it relays by route), its runs and why they ended, and keeps the last lines of
// its log. The counts are in 5-minute buckets: 24 hours in memory, each closed bucket written to the database once an
// hour and kept there 7 days. The admin UI reads them (admin_extensions.go).

const (
	metricsBucket     = 5 * time.Minute
	metricsInMemory   = 24 * time.Hour
	metricsKept       = 7 * 24 * time.Hour
	metricsFlushEvery = time.Hour
	metricsMostMethod = 64   // methods (routes) counted apart per extension; the rest are counted as metricsOther
	metricsMostReason = 16   // exit reasons counted apart per bucket
	metricsMostExits  = 20   // recent exits kept with their time
	logLinesKept      = 2000 // per extension
	logLineMost       = 4 << 10
	metricsOther      = "other"
	legacyStatsID     = "-legacy" // the old paths' calls (#1924), kept as an extension's counters
)

// latencyBounds are the histogram's upper bounds, in milliseconds; the last bucket is everything slower.
var latencyBounds = []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000}

// methodCounts are one method's calls in a bucket.
type methodCounts struct {
	Calls  int64            `json:"calls"`
	Errors int64            `json:"errors"`
	Codes  map[string]int64 `json:"codes,omitempty"`
	// Hist counts the calls by latency (latencyBounds, and one more for the slower); a call without a latency
	// (a WebSocket) is not in it.
	Hist []int64 `json:"hist,omitempty"`
}

// metricsBucketData is one extension's counts over one bucket: what is written to the database.
type metricsBucketData struct {
	Methods map[string]*methodCounts `json:"methods,omitempty"`
	Starts  int64                    `json:"starts,omitempty"`
	Stops   int64                    `json:"stops,omitempty"`
	Exits   int64                    `json:"exits,omitempty"`
	Reasons map[string]int64         `json:"reasons,omitempty"`
	// Counters are other counts by name: the old paths' calls by route (legacyStatsID), later the network proxy's.
	Counters map[string]int64 `json:"counters,omitempty"`
}

type metricsBucketRow struct {
	start   time.Time
	data    metricsBucketData
	flushed bool // written as it is now
}

// LogLine is a line of an extension's log.
type LogLine struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Source string    `json:"source"` // program: its own; hub: the hub's about it
	Text   string    `json:"text"`
}

// ExitNote is a run that ended in error.
type ExitNote struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// extensionStats is one extension's (host.Observer).
type extensionStats struct {
	id  string
	now func() time.Time

	mu      sync.Mutex
	buckets []*metricsBucketRow // oldest first, at most metricsInMemory of them
	exits   []ExitNote          // the latest last
	logs    []LogLine           // a ring: logs[next % len] is the oldest once full
	next    int64               // the next line's seq
	subs    map[chan LogLine]struct{}
	redact  func(string) string
}

var _ host.Observer = (*extensionStats)(nil)

// bucketLocked is the bucket of t, made when it is the newest.
func (s *extensionStats) bucketLocked(t time.Time) *metricsBucketData {
	start := t.Truncate(metricsBucket)
	if n := len(s.buckets); n > 0 && !s.buckets[n-1].start.Before(start) {
		for i := n - 1; i >= 0; i-- {
			if s.buckets[i].start.Equal(start) {
				s.buckets[i].flushed = false
				return &s.buckets[i].data
			}
			if s.buckets[i].start.Before(start) {
				break
			}
		}
		// A clock gone back: count it in the newest.
		s.buckets[n-1].flushed = false
		return &s.buckets[n-1].data
	}
	s.buckets = append(s.buckets, &metricsBucketRow{start: start})
	cut := 0
	for cut < len(s.buckets) && start.Sub(s.buckets[cut].start) >= metricsInMemory {
		cut++
	}
	s.buckets = s.buckets[cut:]
	return &s.buckets[len(s.buckets)-1].data
}

// Call counts a call (host.Observer); took < 0 is one without a latency.
func (s *extensionStats) Call(method string, took time.Duration, code string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bucketLocked(s.now())
	if b.Methods == nil {
		b.Methods = map[string]*methodCounts{}
	}
	m := b.Methods[method]
	if m == nil {
		if len(b.Methods) >= metricsMostMethod {
			method = metricsOther
			m = b.Methods[method]
		}
		if m == nil {
			m = &methodCounts{}
			b.Methods[method] = m
		}
	}
	m.Calls++
	if code != "" {
		if m.Codes == nil {
			m.Codes = map[string]int64{}
		}
		m.Codes[code]++
		if isErrorCode(code) {
			m.Errors++
		}
	}
	if took >= 0 {
		if m.Hist == nil {
			m.Hist = make([]int64, len(latencyBounds)+1)
		}
		ms := float64(took) / float64(time.Millisecond)
		i := sort.SearchFloat64s(latencyBounds, ms)
		m.Hist[i]++
	}
}

// isErrorCode says whether a call ending with code counts as an error: not the caller giving up, nor a request the
// extension turned down as the caller's fault (an HTTP 4xx).
func isErrorCode(code string) bool {
	return code != "" && code != host.CodeCanceled && code != "http_4xx"
}

// Run counts a run (host.Observer).
func (s *extensionStats) Run(event, detail string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	b := s.bucketLocked(now)
	switch event {
	case host.RunStart:
		b.Starts++
	case host.RunStop:
		b.Stops++
	case host.RunExit:
		b.Exits++
		reason := s.redactLocked(cut(detail, 300))
		if b.Reasons == nil {
			b.Reasons = map[string]int64{}
		}
		if _, ok := b.Reasons[reason]; ok || len(b.Reasons) < metricsMostReason {
			b.Reasons[reason]++
		} else {
			b.Reasons[metricsOther]++
		}
		s.exits = append(s.exits, ExitNote{At: now, Reason: reason})
		if len(s.exits) > metricsMostExits {
			s.exits = s.exits[len(s.exits)-metricsMostExits:]
		}
	}
}

// Count adds n to a counter.
func (s *extensionStats) Count(name string, n int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bucketLocked(s.now())
	if b.Counters == nil {
		b.Counters = map[string]int64{}
	}
	b.Counters[name] += n
}
