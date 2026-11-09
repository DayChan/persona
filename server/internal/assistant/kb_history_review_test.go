package assistant

import (
	"context"
	"errors"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Regressions: positions read outside every window are not counted as covered, so a window an anchor found later
// widens over them reads them again, and nothing within 50 of an anchor goes missing.
func TestHistoryPositionalWindowWidensOverDroppedReads(t *testing.T) {
	const n = 1000
	msgs := historyConv("g", n, recentStart(n, 10*time.Minute), 10*time.Minute, 850, 990)
	src := positionalSource("g", msgs, 850, 990)
	src.convs["g"][895].Mine = true // the search missed it; found when the lower window is read
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.run()
	if got := ht.kept("g"); !slices.Equal(got, span(800, n-1)) {
		t.Fatalf("kept %v", got)
	}
	if st := ht.state("g"); st.State != kb.HistoryDone {
		t.Fatalf("state %+v", st)
	}
}

// An uncovered year before the conversation began is not streamed through: one page around position 0 says when it
// began, and the years before that are dropped, so the calls stay close to the windows' own.
func TestHistoryFallbackSkipsYearsBeforeTheChat(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	const n = 1150
	start := now.Add(-8 * 24 * time.Hour)
	msgs := historyConv("g", n, start, 10*time.Minute, 300, 900)
	src := positionalSource("g", msgs, 300, 900)
	ht := newHistoryTest(t, src)
	ht.clock.t = now
	ht.plan(true, nil)
	ht.run()
	if got := ht.kept("g"); !slices.Equal(got, append(span(250, 350), span(850, 950)...)) {
		t.Fatalf("kept %v", got)
	}
	probes := 0
	for _, q := range src.queries {
		if q.Around == "0" {
			probes++
		}
	}
	if probes != 1 || src.count("page") > 20 {
		t.Fatalf("%d pages (%d probes) for two windows: %+v", src.count("page"), probes, src.queries)
	}
	st := ht.state("g")
	if st.State != kb.HistoryDone || !st.Positions.Start.Equal(start) || len(st.Positions.Fallback) != 0 {
		t.Fatalf("state %+v %+v", st, st.Positions)
	}
}

// A pause the source asked for outlives the process: a restart in the middle of it waits out what is left before
// the next call, at the pace it had.
func TestHistoryPauseSurvivesARestart(t *testing.T) {
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 60, recentStart(60, time.Hour), time.Hour)}}
	var pacedAt, nextAt time.Time
	src.hook = func(k int, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
		switch k {
		case 2:
			pacedAt = src.clock.Now()
			return connectorwire.HistoryPage{Pace: &connectorwire.Pace{RetryAfterMS: 3600_000}}, nil, true
		case 3:
			nextAt = src.clock.Now()
		}
		return connectorwire.HistoryPage{}, nil, false
	}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ht.d.sleep = func(ctx context.Context, d time.Duration) error {
		if !pacedAt.IsZero() && ht.clock.Now().Sub(pacedAt) > time.Minute {
			cancel() // the process goes a minute into the pause
		}
		return ht.clock.Sleep(ctx, d)
	}
	if err := ht.d.runOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run: %v", err)
	}
	if src.count("page") != 2 {
		t.Fatalf("%d pages", src.count("page"))
	}
	// A new process.
	ht.d.sleep, ht.d.instance = ht.clock.Sleep, "test-2"
	ht.run()
	if nextAt.Sub(pacedAt) < time.Hour {
		t.Fatalf("read again %v after a retry_after of an hour", nextAt.Sub(pacedAt))
	}
	if got := ht.kept("a"); len(got) != 60 {
		t.Fatalf("kept %d", len(got))
	}
}

// What the pacer watches for carries over a restart: a very slow call after it, within the watch of the one before,
// still stops; the multiplier stays.
func TestHistoryPacerStateCarriesOver(t *testing.T) {
	zone := time.UTC
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, zone)
	pc := newHistoryPacer(historyProfile("standard"), mrand.New(mrand.NewPCG(3, 4)), zone, now, time.Time{})
	if v := pc.observe("history", 6*time.Second, now); !v.verySlow || v.rest == 0 {
		t.Fatalf("very slow: %+v", v)
	}
	pc.quietUntil(now)
	saved := pc.snapshot()
	pc2 := newHistoryPacer(historyProfile("standard"), mrand.New(mrand.NewPCG(5, 6)), zone, now, time.Time{})
	pc2.restore(saved, now.Add(time.Minute))
	if pc2.mult != pc.mult || pc2.batchLeft != pc.batchLeft || !slices.Equal(pc2.quiet, pc.quiet) {
		t.Fatalf("restored %+v from %+v", pc2, pc)
	}
	if v := pc2.observe("history", 6*time.Second, now.Add(40*time.Minute)); v.stop.Code != "very_slow_twice" {
		t.Fatalf("the watch was lost: %+v", v)
	}
}

// Very slow is 5 s for pages whatever their baseline (so it trips before a source's own 8 s timeouts), and only
// images get a threshold from their baseline.
func TestHistoryVerySlowIsAbsoluteForPages(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	pc := newHistoryPacer(historyProfile("standard"), mrand.New(mrand.NewPCG(3, 4)), time.UTC, now, time.Time{})
	for i := 0; i < historyBaselineCalls; i++ {
		pc.observe("history", 2*time.Second, now)
		pc.observe(historyImageMethod, 2*time.Second, now)
	}
	if v := pc.observe("history", 5500*time.Millisecond, now); !v.verySlow {
		t.Fatalf("a 5.5 s page with a 2 s baseline: %+v", v)
	}
	if v := pc.observe(historyImageMethod, 5500*time.Millisecond, now); v.verySlow {
		t.Fatalf("a 5.5 s image with a 2 s baseline: %+v", v)
	}
}

// A slow start's two hours count from the first call, however long after the resume it comes.
func TestHistorySlowStartFromTheFirstCall(t *testing.T) {
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 30, recentStart(30, time.Hour), time.Hour)}}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	updateHistoryStatus(ht.data, testSource, func(st *historySourceStatus) { st.SlowPending = true })
	ht.clock.Advance(3 * time.Hour) // a quiet period, a rest, another process: the import starts later
	first := ht.clock.Now()
	ht.run()
	st := ht.d.status(testSource)
	if st.SlowPending || st.SlowUntil.Before(first.Add(historySlowStart)) {
		t.Fatalf("status %+v (first call after %v)", st, first)
	}
}

// Not ready right after a call of ours went through stops (the call may have caused it); at the start of a run it
// pauses.
func TestHistoryNotReadyAfterACallStops(t *testing.T) {
	notReady := &rpc.Error{Code: "NOT_READY"}
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 60, recentStart(60, time.Hour), time.Hour)}}
	src.hook = func(k int, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
		if k == 1 || k == 3 {
			return connectorwire.HistoryPage{}, notReady, true
		}
		return connectorwire.HistoryPage{}, nil, false
	}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.run()
	stop, stopped := readHistoryStop(ht.data)
	if !stopped || stop.Code != "not_ready_after_call" || src.count("page") != 3 {
		t.Fatalf("STOP %+v %v after %d pages", stop, stopped, src.count("page"))
	}
	if st := ht.d.status(testSource); st.LastPause == nil || st.LastPause.Args["code"] != "not_ready" {
		t.Fatalf("the first not ready did not pause: %+v", st)
	}
}

// An image download a stop cuts short leaves its page unwritten and the cursor where it was: after the resume the
// page is read again and every image saved (the one saved before is not asked for again), with no placeholder left.
func TestHistoryImageCutShortIsNotLost(t *testing.T) {
	msgs := historyConv("a", 30, recentStart(30, time.Hour), time.Hour)
	msgs[25].Type, msgs[25].Media = "IMAGE", []string{"img-25"}
	msgs[27].Type, msgs[27].Media = "IMAGE", []string{"img-27"}
	var mu sync.Mutex
	asked := map[string]int{}
	src := &fakeHistory{info: assistantwire.ArchiveSource{ID: testSource, History: true, Media: true},
		chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": msgs},
		media: func(ref string) (connector.Media, error) {
			mu.Lock()
			defer mu.Unlock()
			asked[ref]++
			if ref == "img-27" && asked[ref] == 1 {
				return connector.Media{}, &rpc.Error{Code: "MAIN_QUEUE_TIMEOUT"}
			}
			return connector.Media{Mime: "image/png", Data: []byte("png")}, nil
		}}
	ht := newHistoryTest(t, src)
	ht.e.AttachmentDaysBySource = map[string]int{testSource: 3650}
	ht.d.notify = func(context.Context, string, historyReason) {}
	ht.plan(true, nil)
	ht.run()
	if stop, stopped := readHistoryStop(ht.data); !stopped || stop.Code != "call_timeout" {
		t.Fatalf("STOP %+v", stop)
	}
	if got := ht.kept("a"); len(got) != 0 {
		t.Fatalf("the cut page was written: %v", got)
	}
	if st := ht.state("a"); st.Cursor != "" || st.Read != 0 {
		t.Fatalf("moved past the cut page: %+v", st)
	}
	if err := removeHistoryStop(ht.data); err != nil {
		t.Fatal(err)
	}
	ht.run()
	if got := ht.kept("a"); !slices.Equal(got, span(0, 29)) {
		t.Fatalf("kept %v", got)
	}
	text := ht.text("a")
	if strings.Count(text, "[附件](attachments/") != 2 || strings.Count(text, "[附件]") != 2 { // i18n-ignore: archive data
		t.Fatalf("archived:\n%s", text)
	}
	if asked["img-25"] != 1 || asked["img-27"] != 2 {
		t.Fatalf("asked %v", asked)
	}
}

// A transient error on an image pauses and asks for the same image again.
func TestHistoryImageTransientRetries(t *testing.T) {
	msgs := historyConv("a", 10, recentStart(10, time.Hour), time.Hour)
	msgs[5].Type, msgs[5].Media = "IMAGE", []string{"img-5"}
	n := 0
	src := &fakeHistory{info: assistantwire.ArchiveSource{ID: testSource, History: true, Media: true},
		chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": msgs},
		media: func(ref string) (connector.Media, error) {
			if n++; n == 1 {
				return connector.Media{}, &rpc.Error{Code: "busy"}
			}
			return connector.Media{Mime: "image/png", Data: []byte("png")}, nil
		}}
	ht := newHistoryTest(t, src)
	ht.e.AttachmentDaysBySource = map[string]int{testSource: 3650}
	ht.plan(true, nil)
	ht.run()
	if n != 2 || !strings.Contains(ht.text("a"), "[附件](attachments/") { // i18n-ignore: archive data
		t.Fatalf("%d media calls:\n%s", n, ht.text("a"))
	}
}

// A data directory that is a link is backed up where it leads; a backup that would keep no file fails.
func TestHistoryBackupFollowsALinkedDataDir(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "chats", "c", "messages"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "chats", "c", "messages", "2026-01-01.md"), []byte("- 10:00 a: hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "data")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	p, err := backupKB(link, time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if files := tarNames(t, p); !slices.Contains(files, "data/chats/c/messages/2026-01-01.md") {
		t.Fatalf("archived %v", files)
	}
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := backupKB(empty, time.Now()); err == nil {
		t.Fatal("an empty backup counted")
	}
	entries, _ := os.ReadDir(historyBackupDir(empty))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".kb-") {
			t.Fatalf("left %s", e.Name())
		}
	}
}

// Every reason a person reads has words in each language, and none is left as a bare code.
func TestHistoryReasonsHaveWords(t *testing.T) {
	for _, code := range []string{"very_slow_twice", "pause_too_long", "source_stop", "write_failed", "state_unreadable",
		"state_save_failed", "anchors_unreadable", "anchors_save_failed", "plan_save_failed", "call_timeout", "call_connection_closed",
		"call_no_code", "call_unknown_code", "not_ready_after_call", "stop_file", "asked", "source_pace", "transient", "slow",
		"very_slow", "very_slow_list", "slow_repeated", "plan_changed"} {
		for _, l := range []string{"en", "zh-Hans", "ja"} {
			r := reason(code, "method", "history", "code", "x", "ms", "1", "limit", "1", "minutes", "1", "hours", "2", "class", "error",
				"chat", "abc", "count", "3", "of", "20", "mult", "2.0", "seconds", "60")
			if got := historyReasonText(i18n.Lang(l), r); got == r.String() || strings.Contains(got, "{") {
				t.Fatalf("%s in %s: %q", code, l, got)
			}
		}
	}
	if got := historyReasonText(i18n.Lang("en"), historyReason{Code: "asked", Text: "my words"}); !strings.Contains(got, "my words") {
		t.Fatalf("text: %q", got)
	}
}
