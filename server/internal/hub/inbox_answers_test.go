package hub

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// countingInbox is a manager on the bus whose inbox list is body (status, when set), counting how often it is listed;
// a list waits for gate when one is set. Every other request is answered {}.
type countingInbox struct {
	mu     sync.Mutex
	body   []byte
	status int
	gate   chan struct{}
	listed atomic.Int32
	asked  chan struct{} // a list was asked for (buffered)
}

func (c *countingInbox) serve(ctx context.Context, r assistantwire.HTTPRequest) (any, error) {
	if r.Method != http.MethodGet || r.Path != "/api/v1/inbox" {
		return assistantwire.HTTPResponse{Status: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: json.RawMessage("{}\n")}, nil
	}
	c.listed.Add(1)
	select {
	case c.asked <- struct{}{}:
	default:
	}
	c.mu.Lock()
	body, status, gate := c.body, cmp0(c.status, 200), c.gate
	c.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return assistantwire.HTTPResponse{Status: status, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}, Body: body}, nil
}

func cmp0(v, or int) int {
	if v == 0 {
		return or
	}
	return v
}

func (c *countingInbox) set(f func(c *countingInbox)) {
	c.mu.Lock()
	f(c)
	c.mu.Unlock()
}

// newCountingInbox connects a countingInbox as h's assistant, its list n items of the mock inbox.
func newCountingInbox(t *testing.T, h *Hub, n int) *countingInbox {
	t.Helper()
	m, _ := connectInboxManager(t, h)
	c := &countingInbox{body: inboxBody(t, n), asked: make(chan struct{}, 64)}
	m.mu.Lock()
	m.http = c.serve
	m.mu.Unlock()
	return c
}

// getInbox is GET path through routes as an app asks it, in lang, taking gzip when gz.
func getInbox(ctx context.Context, h *Hub, routes http.Handler, path string, lang i18n.Lang, gz bool, header ...string) *httptest.ResponseRecorder {
	req := localRequest("GET", path).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	req.Header.Set("Accept-Language", string(lang))
	if gz {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	return rec
}

// waiting is how many requests wait for a merged inbox being fetched.
func (c *inboxAnswers) waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, call := range c.calls {
		n += call.waiters
	}
	return n
}

// expire ages the answers kept past inboxAnswerTTL.
func (c *inboxAnswers) expire() {
	c.mu.Lock()
	for _, a := range c.kept {
		a.at = a.at.Add(-inboxAnswerTTL)
	}
	c.mu.Unlock()
}

// k apps asking for the merged inbox at once (after a push) make one list of the manager, and all get its bytes.
func TestMergedInboxOneFetchForConcurrentRequests(t *testing.T) {
	for _, k := range []int{1, 4, 8} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			h := newTestHub(t)
			m := newCountingInbox(t, h, 30)
			gate := make(chan struct{})
			m.set(func(c *countingInbox) { c.gate = gate })
			routes := h.routes()
			recs := make([]*httptest.ResponseRecorder, k)
			var wg sync.WaitGroup
			for i := range k {
				wg.Go(func() { recs[i] = getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, i%2 == 0) })
			}
			eventually(t, 5*time.Second, "the others wait for the first", func() bool {
				return m.listed.Load() == 1 && h.inboxAnswers.waiting() == k-1
			})
			close(gate)
			wg.Wait()
			if n := m.listed.Load(); n != 1 {
				t.Fatalf("%d apps: the manager listed %d times", k, n)
			}
			want := decodedBody(t, recs[0])
			for i, rec := range recs {
				if rec.Code != 200 || !bytes.Equal(decodedBody(t, rec), want) || !json.Valid(want) {
					t.Fatalf("app %d: %d %.200s", i, rec.Code, decodedBody(t, rec))
				}
			}
			// And the next one within the moment is the kept answer.
			if rec := getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false); !bytes.Equal(rec.Body.Bytes(), want) || m.listed.Load() != 1 {
				t.Fatalf("again: listed %d, %.200s", m.listed.Load(), rec.Body)
			}
		})
	}
}

// What moves an answer on: the manager telling the apps of its inbox, a change passed to it, the moment passing, the
// roster; what a request names goes to the manager every time.
func TestMergedInboxFetchedAgain(t *testing.T) {
	h := newTestHub(t)
	m := newCountingInbox(t, h, 12)
	routes := h.routes()
	get := func(header ...string) *httptest.ResponseRecorder {
		t.Helper()
		rec := getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false, header...)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return rec
	}
	listed := func(want int32, what string) {
		t.Helper()
		if n := m.listed.Load(); n != want {
			t.Fatalf("%s: listed %d times, want %d", what, n, want)
		}
	}
	get()
	get()
	listed(1, "twice at once")

	if err := h.relayBroadcastFrom(busAssistant, json.RawMessage(`{"type":"inbox"}`)); err != nil {
		t.Fatal(err)
	}
	get()
	listed(2, "after the manager's inbox push")
	get()
	listed(2, "after the manager's inbox push, again")

	if code, _ := call(t, routes, "POST", "/api/v1/inbox/x/done", h.cfg.Token, `{"done":true}`); code != 200 {
		t.Fatalf("done: %d", code)
	}
	get()
	listed(3, "after an item was marked done")

	h.inboxAnswers.expire()
	get()
	listed(4, "after the moment")

	h.ownersChanged()
	get()
	listed(5, "after the roster changed")

	get(assistantwire.ManagerHeader, busAssistant)
	get(assistantwire.ManagerHeader, busAssistant)
	listed(7, "naming the manager")

	other := getInbox(t.Context(), h, routes, "/api/v1/inbox", i18n.En, false)
	if other.Code != 200 {
		t.Fatal(other.Code)
	}
	listed(8, "another query")
	getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.ZhHans, false)
	listed(9, "another language")
	get()
	listed(9, "the first again")
}

// A list fetched while a change passed to the manager is not kept: the next one is fetched afresh.
func TestMergedInboxNotKeptAcrossAChange(t *testing.T) {
	h := newTestHub(t)
	m := newCountingInbox(t, h, 6)
	gate := make(chan struct{})
	m.set(func(c *countingInbox) { c.gate = gate })
	routes := h.routes()
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false) }()
	<-m.asked
	if code, _ := call(t, routes, "POST", "/api/v1/inbox/x/done", h.cfg.Token, `{"done":true}`); code != 200 {
		t.Fatalf("done: %d", code)
	}
	m.set(func(c *countingInbox) { c.gate = nil })
	close(gate)
	if rec := <-done; rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false)
	if n := m.listed.Load(); n != 2 {
		t.Fatalf("listed %d times, want 2", n)
	}
}

// An answer that is not a 2xx is nobody else's: each request gets its own, and waiting ones fetch for themselves.
func TestMergedInboxFailuresNotShared(t *testing.T) {
	h := newTestHub(t)
	m := newCountingInbox(t, h, 6)
	m.set(func(c *countingInbox) {
		c.status, c.body = 503, json.RawMessage(`{"error":"inbox not ready","code":"inbox_unavailable"}`)
	})
	routes := h.routes()
	for range 2 {
		if rec := getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false); rec.Code != 503 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	if n := m.listed.Load(); n != 2 {
		t.Fatalf("listed %d times, want 2", n)
	}

	// The first of 4 fails: the 3 waiting for it list for themselves.
	gate := make(chan struct{})
	m.set(func(c *countingInbox) { c.gate = gate })
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range 4 {
		wg.Go(func() { codes[i] = getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false).Code })
	}
	eventually(t, 5*time.Second, "the others wait for the first", func() bool { return h.inboxAnswers.waiting() == 3 })
	m.set(func(c *countingInbox) { c.gate = nil })
	close(gate)
	wg.Wait()
	if n := m.listed.Load(); n != 2+4 {
		t.Fatalf("listed %d times, want 6: %v", n, codes)
	}

	// A waiting request is bound by its own context, not the first one's.
	gate = make(chan struct{})
	m.set(func(c *countingInbox) { c.gate, c.status = gate, 0 })
	first := make(chan int)
	go func() { first <- getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false).Code }()
	eventually(t, 5*time.Second, "the first is with the manager", func() bool { return m.listed.Load() == 7 })
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	getInbox(ctx, h, routes, "/api/v1/inbox?sources=all", i18n.En, false)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("a waiting request whose context ended took %s", d)
	}
	close(gate)
	if code := <-first; code != 200 {
		t.Fatal(code)
	}
}

// One manager's failure makes the merge nobody else's too, though the merge (the default's list) is a 200.
func TestMergedInboxOtherManagerFailing(t *testing.T) {
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		for _, c := range h.stopBus() {
			_ = c.ws.CloseNow()
		}
	})
	def := &countingInbox{body: inboxBody(t, 4), asked: make(chan struct{}, 64)}
	other := &countingInbox{status: 500, body: json.RawMessage(`{"error":"down"}`), asked: make(chan struct{}, 64)}
	for id, c := range map[string]*countingInbox{busAssistant: def, "m-other": other} {
		fake := &inboxFakeManager{items: map[string]json.RawMessage{}, repos: map[string][]string{}, http: c.serve}
		r := dialRawBusWith(t, srv, busToken(t, h, id, ""), fake.handle)
		if err := r.call("manager.hello", map[string]any{"id": id}, nil); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 5*time.Second, "both connected", func() bool { return len(h.listManagers(t.Context())) == 2 })
	routes := h.routes()
	for range 3 {
		if rec := getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	if d, o := def.listed.Load(), other.listed.Load(); d != 3 || o != 3 {
		t.Fatalf("listed %d and %d times, want 3 each", d, o)
	}
	other.set(func(c *countingInbox) { c.status, c.body = 0, inboxBody(t, 2) })
	for range 3 {
		getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false)
	}
	if d, o := def.listed.Load(), other.listed.Load(); d != 4 || o != 4 {
		t.Fatalf("both answering: listed %d and %d times, want 4 each", d, o)
	}
	// The other manager's push moves its own generation: the merge is fetched again.
	if err := h.relayBroadcastFrom("m-other", json.RawMessage(`{"type":"inbox"}`)); err != nil {
		t.Fatal(err)
	}
	getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false)
	if d, o := def.listed.Load(), other.listed.Load(); d != 5 || o != 5 {
		t.Fatalf("after the other's push: listed %d and %d times, want 5 each", d, o)
	}
}

// The kept answer is the bytes and headers each request got before, in every language, gzipped or not.
func TestMergedInboxSameAnswer(t *testing.T) {
	h := newTestHub(t)
	m := newCountingInbox(t, h, 40)
	routes := h.routes()
	headers := func(rec *httptest.ResponseRecorder) http.Header {
		hd := rec.Header().Clone()
		hd.Del("X-Request-Id")
		return hd
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, gz := range []bool{false, true} {
			h.inboxAnswers.off = true
			want := getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", lang, gz)
			h.inboxAnswers.off = false
			h.ownersChanged()  // none kept from before
			for i := range 3 { // fetched, then kept
				got := getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", lang, gz)
				if got.Code != want.Code || !maps.EqualFunc(headers(got), headers(want), slices.Equal) || !bytes.Equal(got.Body.Bytes(), want.Body.Bytes()) {
					t.Fatalf("%s gzip %v, request %d:\n got %d %v %d bytes\nwant %d %v %d bytes", lang, gz, i, got.Code, headers(got), got.Body.Len(), want.Code, headers(want), want.Body.Len())
				}
			}
			if got := headers(want).Get("Content-Encoding"); (got == "gzip") != gz {
				t.Fatalf("%s gzip %v: Content-Encoding %q", lang, gz, got)
			}
		}
	}
	if n := m.listed.Load(); n != 3*2*2 {
		t.Fatalf("listed %d times, want 12 (one each way, one kept for 3)", n)
	}
	// Gzipped or not, the same answer.
	plain := getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, false)
	zipped := getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", i18n.En, true)
	if !bytes.Equal(decodedBody(t, zipped), plain.Body.Bytes()) {
		t.Fatal("the gzipped answer is another")
	}
}

// decodedBody is rec's body, gunzipped when it says so.
func decodedBody(t *testing.T, rec *httptest.ResponseRecorder) []byte {
	t.Helper()
	if rec.Header().Get("Content-Encoding") != "gzip" {
		return rec.Body.Bytes()
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The bounds: at most inboxAnswersMax answers, the oldest going first; a body over inboxAnswerMaxBody is not kept.
func TestInboxAnswersBounds(t *testing.T) {
	var c inboxAnswers
	keyN := func(i int) inboxAnswerKey { return inboxAnswerKey{path: "/api/v1/inbox", query: fmt.Sprint(i)} }
	for i := range inboxAnswersMax + 3 {
		k := keyN(i)
		_, call, leader := c.join(k, nil, time.Now().Add(time.Duration(i)*time.Millisecond))
		if !leader {
			t.Fatal("no fetch under way, yet not the one to fetch")
		}
		c.finish(k, nil, call, &inboxAnswer{status: 200, body: []byte("{}"), shared: true})
	}
	if len(c.kept) != inboxAnswersMax || c.kept[keyN(0)] != nil || c.kept[keyN(inboxAnswersMax+2)] == nil {
		t.Fatalf("%d kept", len(c.kept))
	}
	big := keyN(100)
	_, call, _ := c.join(big, nil, time.Now())
	c.finish(big, nil, call, &inboxAnswer{status: 200, body: make([]byte, inboxAnswerMaxBody+1), shared: true})
	if c.kept[big] != nil || c.size != inboxAnswersMax*2 {
		t.Fatalf("the big one kept: %d bytes counted", c.size)
	}
	// The gzipped copy is made once, counts while its answer is kept, and goes with it.
	last := keyN(inboxAnswersMax + 2)
	gz := c.gzipped(c.kept[last])
	if len(gz) == 0 || c.size != inboxAnswersMax*2+len(gz) || &c.gzipped(c.kept[last])[0] != &gz[0] {
		t.Fatalf("%d bytes counted with a gzipped copy of %d", c.size, len(gz))
	}
	c.drop(last)
	if c.size != (inboxAnswersMax-1)*2 {
		t.Fatalf("%d bytes counted after dropping it", c.size)
	}
}

// The validator: a request with If-None-Match gets the tag, and a 304 while the list it names is still the one; the
// moment of the listing (as_of) alone is no change. Without one, no tag (TestMergedInboxSameAnswer: the same bytes).
func TestMergedInboxValidator(t *testing.T) {
	h := newTestHub(t)
	m := newCountingInbox(t, h, 30)
	first := m.body
	routes := h.routes()
	get := func(lang i18n.Lang, gz bool, inm string) *httptest.ResponseRecorder {
		return getInbox(t.Context(), h, routes, "/api/v1/inbox?sources=all", lang, gz, "If-None-Match", inm)
	}
	full := get(i18n.En, true, `W/"i1-"`)
	tag := full.Header().Get("ETag")
	if full.Code != 200 || !regexp.MustCompile(`^W/"i1-[0-9a-f]{32}"$`).MatchString(tag) || full.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("%d, ETag %q, %v", full.Code, tag, full.Header())
	}
	for _, tc := range []struct {
		name   string
		change func()
		lang   i18n.Lang
		inm    string
		full   bool // a 200 (with a tag); else 304
		newTag bool // and another tag
	}{
		{name: "the same list", inm: tag},
		{name: "named among others, strong", inm: `"x", ` + strings.TrimPrefix(tag, "W/")},
		{name: "any", inm: "*"},
		{name: "another moment, the same items", inm: tag, change: func() {
			m.set(func(c *countingInbox) {
				c.body = bytes.Replace(first, []byte(`"as_of":"2026-11-09T12:00:00Z"`), []byte(`"as_of":"2026-11-09T12:00:05Z"`), 1)
			})
		}},
		{name: "another tag", inm: `W/"i1-0123"`, full: true},
		{name: "another language", inm: tag, lang: i18n.ZhHans, full: true, newTag: true},
		{name: "an item more", inm: tag, full: true, newTag: true, change: func() { m.set(func(c *countingInbox) { c.body = inboxBody(t, 31) }) }},
	} {
		if tc.change != nil {
			tc.change()
			h.inboxAnswers.changedAll()
		}
		for _, gz := range []bool{false, true} {
			rec := get(cmp.Or(tc.lang, i18n.En), gz, tc.inm)
			got := rec.Header()
			switch {
			case tc.full && (rec.Code != 200 || (got.Get("ETag") != tag) != tc.newTag || got.Get("ETag") == "" || rec.Body.Len() == 0):
				t.Fatalf("%s, gzip %v: %d, ETag %q", tc.name, gz, rec.Code, got.Get("ETag"))
			case !tc.full && (rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || got.Get("ETag") != tag ||
				got.Get("Content-Encoding") != "" || got.Get("Cache-Control") != "no-store" ||
				got.Get("Content-Type") != full.Header().Get("Content-Type")):
				t.Fatalf("%s, gzip %v: %d %v, %d bytes", tc.name, gz, rec.Code, got, rec.Body.Len())
			}
		}
	}

	// A list some manager did not answer with a 2xx has no tag.
	m.set(func(c *countingInbox) {
		c.status, c.body = 503, json.RawMessage(`{"error":"inbox not ready","code":"inbox_unavailable"}`)
	})
	h.inboxAnswers.changedAll()
	if rec := get(i18n.En, false, "*"); rec.Code != 503 || rec.Header().Get("ETag") != "" {
		t.Fatalf("%d, ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

// The tag leaves out the top-level as_of's value, wherever it is; If-None-Match compares weakly, in lists.
func TestInboxAnswerTag(t *testing.T) {
	tagOf := func(body string) string { return (&inboxAnswer{body: []byte(body)}).etag() }
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{`{"as_of":"1","items":[1]}`, `{"as_of":"2","items":[1]}`, true},
		{`{"items":[{"as_of":"1"}], "as_of" : "1"}`, `{"items":[{"as_of":"1"}], "as_of" : "2"}`, true},
		{`{"items":[{"as_of":"1"}],"as_of":"1"}`, `{"items":[{"as_of":"2"}],"as_of":"1"}`, false},
		{`{"as_of":"1","items":[1]}`, `{"as_of":"1","items":[2]}`, false},
		{`[1]`, `[2]`, false},
	} {
		if (tagOf(tc.a) == tagOf(tc.b)) != tc.same {
			t.Errorf("%s and %s: tags %s and %s", tc.a, tc.b, tagOf(tc.a), tagOf(tc.b))
		}
	}
	for _, tc := range []struct {
		inm  []string
		want bool
	}{
		{[]string{`W/"i1-ab"`}, true},
		{[]string{`"i1-ab"`}, true},
		{[]string{`"x"`, ` W/"y" , W/"i1-ab"`}, true},
		{[]string{`*`}, true},
		{[]string{`W/"i1-"`}, false},
		{[]string{`W/"i1-abc"`, `i1-ab`}, false},
	} {
		if got := tagMatches(tc.inm, `W/"i1-ab"`); got != tc.want {
			t.Errorf("%q: %v", tc.inm, got)
		}
	}
}
