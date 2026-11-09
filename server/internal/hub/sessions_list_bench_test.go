//go:build !windows

package hub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// syntheticAgentHub uses only demo data, but includes the preview and nested task
// metadata that make production session lists expensive to encode and decode.
func syntheticAgentHub(t *testing.T) *Hub {
	h := newTestHub(t)
	for machine := range 4 {
		mid := fmt.Sprintf("demo-%d", machine)
		m := newMachine(h, MachineConfig{ID: mid, Kind: "local"})
		h.machines, h.byID[mid] = append(h.machines, m), m
		m.sessions = syntheticSessions(mid, 2000)
	}
	return h
}

// syntheticSessions are n demo sessions of machine mid, with previews and background tasks.
func syntheticSessions(mid string, n int) []proto.Session {
	out := make([]proto.Session, 0, n)
	for i := range n {
		state := []string{"needs_you", "working", "unread", "idle"}[i%4]
		out = append(out, proto.Session{
			MachineID: mid, SessionID: fmt.Sprintf("session-%04d", i), Title: fmt.Sprintf("Demo task %04d", i),
			Cwd: fmt.Sprintf("/demo/project-%02d", i%20), RepoRoot: fmt.Sprintf("/demo/project-%02d", i%20),
			SpaceID: fmt.Sprintf("space-%02d", i%10), SpaceName: fmt.Sprintf("Demo space %02d", i%10),
			Agent: []string{"claude", "codex"}[i%2], State: state, Status: proto.StatusIdle,
			UpdatedAt: time.Unix(1700000000+int64(i), 0), LastText: strings.Repeat("demo preview ", 50),
			Background: []proto.BackgroundTask{{ID: "demo-task", Kind: "shell", Description: "Demo test run"}},
		})
	}
	return out
}

// indexedMachineHub is one machine with n demo sessions and a paged reader, so every list update or sessions.revs
// rebuilds the machine's index rows (listCache.machines).
func indexedMachineHub(n int) (*Hub, *Machine) {
	h := newTestHub(&testing.T{})
	m := newMachine(h, MachineConfig{ID: "demo", Kind: "local"})
	h.machines, h.byID["demo"] = append(h.machines, m), m
	m.sessions = syntheticSessions("demo", n)
	h.sessionListIndex()
	return h, m
}

// BenchmarkReindexOneRev is one sessions.revs for one working session: what the hub redoes for its paged index
// (machine.go "sessions.revs" -> reindexSessions) on a machine of 150 and of 2,000 sessions.
func BenchmarkReindexOneRev(b *testing.B) {
	for _, n := range []int{150, 2000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			h, m := indexedMachineHub(n)
			b.ReportAllocs()
			for b.Loop() {
				m.mu.Lock()
				m.sessions[0].Rev++
				m.mu.Unlock()
				h.reindexSessions(m)
			}
		})
	}
}

// BenchmarkIndexWholeList is the paged index's share of a whole-list update (broadcastSessions): the rows come
// encoded, as the push encodes them for its own clients, and one of them differs from the last update.
func BenchmarkIndexWholeList(b *testing.B) {
	for _, n := range []int{150, 2000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			h, m := indexedMachineHub(n)
			var lists [2][]proto.Session
			var encoded [2][]sentSession
			for i := range lists {
				lists[i] = h.shown("demo", h.decorated(m))
				lists[i][0].Rev = int64(i + 1)
				encoded[i] = encodeSessions(lists[i])
			}
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				h.sessionsMu.Lock()
				h.indexSessionsLocked("demo", lists[i], encoded[i])
				h.sessionsMu.Unlock()
				i ^= 1
			}
		})
	}
}

// Indexing a 150-row list of which one row changed keeps the other rows' indexed payloads (indexSessionsLocked):
// 98,689 B measured, 2,652,067 B when every row was indexed again.
func TestIndexWholeListAllocation(t *testing.T) {
	if !isolatedSessionAllocation(t) {
		return
	}
	h, m := indexedMachineHub(150)
	var lists [2][]proto.Session
	var encoded [2][]sentSession
	for i := range lists {
		lists[i] = h.shown("demo", h.decorated(m))
		lists[i][0].Rev = int64(i + 1)
		encoded[i] = encodeSessions(lists[i])
	}
	i := 0
	per := allocated(func() {
		h.sessionsMu.Lock()
		h.indexSessionsLocked("demo", lists[i], encoded[i])
		h.sessionsMu.Unlock()
		i ^= 1
	})
	if per > 101_700 {
		t.Fatalf("indexing a 150-row list with one changed row allocates %d bytes", per)
	}
}

// BenchmarkAgents8000Legacy is the whole list of 8,000 sessions, written to a writer that keeps none of it
// (discardResponse), as a connection's does.
func BenchmarkAgents8000Legacy(b *testing.B) {
	h := syntheticAgentHub(&testing.T{})
	r := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	w := &discardResponse{}
	h.listSessions(w, r)
	b.ReportAllocs()
	for b.Loop() {
		h.listSessions(&discardResponse{}, r)
	}
	b.ReportMetric(float64(w.n), "JSON-bytes") // after the loop: b.Loop drops metrics reported before it
}

// TestAgents8000Measurement reports actual handler time, uncompressed JSON bytes,
// process CPU and heap for a repeatable synthetic workload, without a live node.
func TestAgents8000Measurement(t *testing.T) {
	h := syntheticAgentHub(t)
	for _, query := range []string{"", "?summary=1&limit=50", "?counts=1"} {
		r := httptest.NewRequest("GET", "/api/v1/sessions"+query, nil)
		w := httptest.NewRecorder()
		coldStart := time.Now()
		h.listSessions(w, r)
		firstRequest := time.Since(coldStart)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
		bytes := w.Body.Len()
		runtime.GC()
		var before, after runtime.MemStats
		var cpuBefore, cpuAfter syscall.Rusage
		runtime.ReadMemStats(&before)
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &cpuBefore)
		start := time.Now()
		const requests = 100
		for range requests {
			w := httptest.NewRecorder()
			h.listSessions(w, r)
		}
		elapsed := time.Since(start)
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &cpuAfter)
		runtime.ReadMemStats(&after)
		cpu := func(r syscall.Rusage) time.Duration {
			return time.Duration(r.Utime.Sec+r.Stime.Sec)*time.Second + time.Duration(r.Utime.Usec+r.Stime.Usec)*time.Microsecond
		}
		runtime.GC()
		var retained runtime.MemStats
		runtime.ReadMemStats(&retained)
		rss := cpuAfter.Maxrss * 1024 // Linux reports KiB; Darwin reports bytes.
		if runtime.GOOS == "darwin" {
			rss = cpuAfter.Maxrss
		}
		t.Logf("GET /api/v1/sessions%s: sessions=8000 requests=%d first_request=%s mean=%s JSON=%d bytes CPU/request=%s allocated/request=%d bytes retained_heap=%d bytes RSS_peak=%d bytes",
			query, requests, firstRequest, elapsed/requests, bytes, (cpu(cpuAfter)-cpu(cpuBefore))/requests, (after.TotalAlloc-before.TotalAlloc)/requests, retained.HeapAlloc, rss)
	}
}

// The whole list (GET /api/v1/sessions) is every machine's sessions as decorated gives them, read from the kept
// lists and written out a session at a time: the bytes writeJSON gives for them all, the lists unchanged, and what
// an answer allocates bounded by the write buffer rather than the answer's size: 67,450 B for this 403 KB answer
// measured, 2,445,645 B (6.07x the answer) when it was copied and encoded whole.
func TestLegacyListStreams(t *testing.T) {
	if !isolatedSessionAllocation(t) {
		return
	}
	h := newTestHub(t)
	for _, mid := range []string{"ma", "mb"} {
		m := newMachine(h, MachineConfig{ID: mid, Kind: "local"})
		h.machines, h.byID[mid] = append(h.machines, m), m
		m.sessions = busyList()
		m.sessions[0].Manager = "someone-else"
	}
	var decorated []proto.Session
	for _, m := range h.machines {
		decorated = append(decorated, h.shown(m.cfg.ID, h.decorated(m))...)
	}
	for _, c := range []struct {
		query string
		want  []proto.Session
	}{{"", decorated}, {"?machine_id=none", []proto.Session{}}} {
		want := httptest.NewRecorder()
		writeJSON(want, map[string]any{"sessions": c.want})
		got := httptest.NewRecorder()
		h.listSessions(got, httptest.NewRequest("GET", "/api/v1/sessions"+c.query, nil))
		if got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
			t.Fatalf("%q: the whole list differs from the decorated lists", c.query)
		}
	}
	if h.machines[0].sessions[0].Manager != "someone-else" || h.machines[1].sessions[1].Manager != "" {
		t.Fatal("the answer changed a kept list")
	}
	r := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	var size int
	per := allocated(func() {
		w := &discardResponse{}
		h.listSessions(w, r)
		size = w.n
	})
	if per > 68<<10 {
		t.Fatalf("a %d-byte whole list allocates %d bytes", size, per)
	}
}

// discardResponse counts the body and keeps none of it, as a connection's writer passes it on: what an answer
// allocates is then the handler's own, not a recorder's copy of the body.
type discardResponse struct {
	header http.Header
	n      int
}

func (d *discardResponse) Header() http.Header {
	if d.header == nil {
		d.header = http.Header{}
	}
	return d.header
}
func (d *discardResponse) Write(b []byte) (int, error) { d.n += len(b); return len(b), nil }
func (d *discardResponse) WriteHeader(int)             {}
