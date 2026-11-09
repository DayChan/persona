package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The bodies a relay answers with (codes and messages as it sends them).
const (
	quotaBody = `{"error":{"type":"rate_limit_error","code":"account_quota_exhausted","message":"Account usage limit reached"}}`
	busyBody  = `{"error":{"type":"service_unavailable","code":"account_concurrency_full","message":"OpenAI account concurrency is full; please retry shortly"}}`
)

// clock replaces the package's time and log for one test.
type clock struct {
	mu   sync.Mutex
	t    time.Time
	logs []string
}

func fakeClock(t *testing.T) *clock {
	c := &clock{t: time.Date(2026, 11, 2, 19, 43, 0, 0, time.UTC)}
	oldNow, oldLog := now, logf
	now = func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
	logf = func(f string, a ...any) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.logs = append(c.logs, fmt.Sprintf(f, a...))
	}
	t.Cleanup(func() { now, logf = oldNow, oldLog })
	return c
}

func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func (c *clock) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.logs...)
}

// upstream answers each request with what reply says, counting them and the most in flight at once.
type upstream struct {
	srv      *httptest.Server
	sent     atomic.Int32
	inflight atomic.Int32
	most     atomic.Int32
	mu       sync.Mutex
	reply    func(w http.ResponseWriter)
	hold     time.Duration
}

func newUpstream(t *testing.T, reply func(w http.ResponseWriter)) *upstream {
	u := &upstream{reply: reply}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		u.sent.Add(1)
		n := u.inflight.Add(1)
		defer u.inflight.Add(-1)
		for m := u.most.Load(); n > m && !u.most.CompareAndSwap(m, n); m = u.most.Load() {
		}
		u.mu.Lock()
		reply, hold := u.reply, u.hold
		u.mu.Unlock()
		time.Sleep(hold)
		reply(w)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) set(reply func(w http.ResponseWriter)) {
	u.mu.Lock()
	u.reply = reply
	u.mu.Unlock()
}

func (u *upstream) client(key string) *Client {
	return New(Config{BaseURL: u.srv.URL, APIKey: key, Model: "m"})
}

func answer(status int, body string, header ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func ok(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"ok":true}`}}})
}

func call(c *Client) error {
	var out struct{ OK bool }
	err := c.JSON(context.Background(), "sys", "user", &out)
	if err == nil && !out.OK {
		return errors.New("no answer")
	}
	return err
}

func TestUpstreamIgnoresNonModelProbes(t *testing.T) {
	var replies atomic.Int32
	u := newUpstream(t, func(w http.ResponseWriter) {
		replies.Add(1)
		ok(w)
	})
	probe := func() {
		t.Helper()
		resp, err := http.Get(u.srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("non-model probe status %d", resp.StatusCode)
		}
	}
	probe()
	if u.sent.Load() != 0 || u.inflight.Load() != 0 || u.most.Load() != 0 || replies.Load() != 0 {
		t.Fatal("a non-model probe changed model-request counts or consumed a reply")
	}
	if err := call(u.client("test-key")); err != nil {
		t.Fatal(err)
	}
	probe()
	if u.sent.Load() != 1 || u.inflight.Load() != 0 || u.most.Load() != 1 || replies.Load() != 1 {
		t.Fatal("a real POST was not counted exactly once, or a later probe changed its counts")
	}
}

func TestQuotaPausesTheConnection(t *testing.T) {
	clk := fakeClock(t)
	u := newUpstream(t, answer(429, quotaBody))
	c := u.client("k")

	// The first request finds out; the next 49 are put off without being sent.
	for i := range 50 {
		err := call(c)
		if !IsPaused(err) {
			t.Fatalf("call %d: %v, want paused", i, err)
		}
		if i == 0 && err.Error() != "额度用完，30 分钟后重试" {
			t.Errorf("text = %q", err.Error())
		}
	}
	if n := u.sent.Load(); n != 1 {
		t.Fatalf("sent %d requests, want 1", n)
	}
	if p := Held(c.Config()); p == nil || p.Reason != ReasonQuota || !p.Until.Equal(now().Add(30*time.Minute)) {
		t.Fatalf("held = %+v", p)
	}
	logs := clk.lines()
	if len(logs) != 1 || !strings.Contains(logs[0], "out of quota (HTTP 429 account_quota_exhausted: Account usage limit reached)") ||
		!strings.Contains(logs[0], "until 20:13:00") {
		t.Fatalf("logs = %q", logs)
	}

	// 29 minutes on: still put off. At 30 one request goes, and it is turned away again: another 30 minutes, no new
	// line in the log.
	clk.add(29 * time.Minute)
	if err := call(c); !IsPaused(err) || err.Error() != "额度用完，1 分钟后重试" {
		t.Fatalf("at 29 min: %v", err)
	}
	clk.add(time.Minute)
	if err := call(c); !IsPaused(err) {
		t.Fatalf("probe: %v", err)
	}
	if err := call(c); !IsPaused(err) {
		t.Fatalf("after the probe: %v", err)
	}
	if n := u.sent.Load(); n != 2 {
		t.Fatalf("sent %d requests, want 2", n)
	}
	if len(clk.lines()) != 1 {
		t.Fatalf("logs = %q", clk.lines())
	}

	// The quota is back: the probe goes through, the connection opens, everything after it is sent.
	u.set(ok)
	clk.add(30 * time.Minute)
	for i := range 5 {
		if err := call(c); err != nil {
			t.Fatalf("call %d after the quota is back: %v", i, err)
		}
	}
	if n := u.sent.Load(); n != 7 {
		t.Fatalf("sent %d requests, want 7", n)
	}
	if Held(c.Config()) != nil {
		t.Fatal("still held")
	}
	logs = clk.lines()
	if len(logs) != 2 || !strings.Contains(logs[1], "answers again after 1h0m0s; 51 request(s) were put off") {
		t.Fatalf("logs = %q", logs)
	}
}

func TestRetryAfterSetsThePause(t *testing.T) {
	fakeClock(t)
	u := newUpstream(t, answer(429, quotaBody, "Retry-After", "120"))
	c := u.client("k")
	if err := call(c); !IsPaused(err) || err.Error() != "额度用完，2 分钟后重试" {
		t.Fatalf("err = %v", err)
	}
	if p := Held(c.Config()); p == nil || !p.Until.Equal(now().Add(2*time.Minute)) {
		t.Fatalf("held = %+v", p)
	}
}

func TestBusyBacksOffExponentially(t *testing.T) {
	clk := fakeClock(t)
	u := newUpstream(t, answer(503, busyBody))
	c := u.client("k")
	for i, want := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second} {
		err := call(c)
		if !IsPaused(err) {
			t.Fatalf("round %d: %v", i, err)
		}
		p := Held(c.Config())
		if p == nil || p.Reason != ReasonBusy || !p.Until.Equal(now().Add(want)) {
			t.Fatalf("round %d: held = %+v, want %s", i, p, want)
		}
		if err := call(c); !IsPaused(err) { // put off, not sent
			t.Fatalf("round %d, during the pause: %v", i, err)
		}
		clk.add(want)
	}
	if n := u.sent.Load(); n != 4 {
		t.Fatalf("sent %d requests, want 4", n)
	}
	u.set(ok)
	if err := call(c); err != nil {
		t.Fatalf("after the pause: %v", err)
	}
	if logs := clk.lines(); len(logs) != 2 || !strings.Contains(logs[0], "is busy (HTTP 503 account_concurrency_full:") {
		t.Fatalf("logs = %q", logs)
	}
	// Busy answers in a row start over once it has answered.
	u.set(answer(503, busyBody))
	_ = call(c)
	if p := Held(c.Config()); p == nil || !p.Until.Equal(now().Add(5*time.Second)) {
		t.Fatalf("held = %+v", p)
	}
}

func TestBusyCapsTheRequestsInFlight(t *testing.T) {
	clk := fakeClock(t)
	oldGrow := growAfter
	growAfter = 100
	t.Cleanup(func() { growAfter = oldGrow })
	// Four out at once (each answers once all four have arrived), all busy: the cap becomes two, and the three that
	// were already out do not add to the pause.
	arrived := make(chan struct{})
	var n atomic.Int32
	u := newUpstream(t, func(w http.ResponseWriter) {
		if n.Add(1) == 4 {
			close(arrived)
		}
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
		}
		answer(503, busyBody)(w)
	})
	c := u.client("k")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = call(c) }()
	}
	wg.Wait()
	g := gateFor(u.srv.URL, "k")
	g.mu.Lock()
	cap, strikes := g.cap, g.strikes
	g.mu.Unlock()
	if u.sent.Load() != 4 || cap != 2 || strikes != 1 {
		t.Fatalf("sent %d, cap %d, strikes %d; want 4, 2, 1", u.sent.Load(), cap, strikes)
	}

	// Back: one probe, then twelve at once, never more than two at the upstream, none lost.
	u.set(ok)
	u.mu.Lock()
	u.hold = 50 * time.Millisecond
	u.mu.Unlock()
	clk.add(5 * time.Second)
	if err := call(c); err != nil {
		t.Fatalf("probe: %v", err)
	}
	u.most.Store(0)
	var failed atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if call(c) != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 || u.most.Load() > 2 {
		t.Fatalf("%d failed, %d at once at most", failed.Load(), u.most.Load())
	}
}

func TestCapGrowsBackAndLifts(t *testing.T) {
	fakeClock(t)
	oldGrow, oldLift := growAfter, capLift
	growAfter, capLift = 2, 3
	t.Cleanup(func() { growAfter, capLift = oldGrow, oldLift })
	u := newUpstream(t, ok)
	c := u.client("k")
	g := gateFor(u.srv.URL, "k")
	g.mu.Lock()
	g.cap = 2
	g.mu.Unlock()
	caps := []int{}
	for range 4 {
		if err := call(c); err != nil {
			t.Fatal(err)
		}
		g.mu.Lock()
		caps = append(caps, g.cap)
		g.mu.Unlock()
	}
	if fmt.Sprint(caps) != "[2 3 3 0]" {
		t.Fatalf("caps = %v", caps)
	}
}

func TestTheGateIsPerConnection(t *testing.T) {
	fakeClock(t)
	u := newUpstream(t, answer(429, quotaBody))
	if err := call(u.client("k1")); !IsPaused(err) {
		t.Fatal(err)
	}
	u.set(ok)
	if err := call(u.client("k2")); err != nil { // another key on the same address
		t.Fatalf("another key: %v", err)
	}
	if err := call(u.client("k1")); !IsPaused(err) {
		t.Fatalf("the held key: %v", err)
	}
	if u.sent.Load() != 2 {
		t.Fatalf("sent %d", u.sent.Load())
	}
}

func TestResponsesAPIIsGatedToo(t *testing.T) {
	fakeClock(t)
	u := newUpstream(t, answer(503, busyBody))
	c := New(Config{API: "responses", BaseURL: u.srv.URL, APIKey: "k", Model: "m"})
	for range 3 {
		if _, err := c.Complete(context.Background(), "s", "u", 10); !IsPaused(err) {
			t.Fatalf("err = %v", err)
		}
	}
	if u.sent.Load() != 1 {
		t.Fatalf("sent %d", u.sent.Load())
	}
}

func TestOtherErrorsDoNotPause(t *testing.T) {
	fakeClock(t)
	u := newUpstream(t, answer(500, `{"error":{"message":"boom"}}`))
	c := u.client("k")
	for range 3 {
		if err := call(c); err == nil || IsPaused(err) || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("err = %v", err)
		}
	}
	if u.sent.Load() != 3 || Held(c.Config()) != nil {
		t.Fatalf("sent %d, held %v", u.sent.Load(), Held(c.Config()))
	}
}

func TestClassify(t *testing.T) {
	fakeClock(t)
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{429, quotaBody, ReasonQuota},
		{503, busyBody, ReasonBusy},
		{429, `{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your per-minute rate limit"}}`, ReasonBusy},
		{429, `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`, ReasonQuota},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, ReasonBusy},
		{503, `upstream connect error`, ReasonBusy},
		{500, quotaBody, ""},
		{400, `{"error":{"message":"bad request"}}`, ""},
	}
	for _, c := range cases {
		if got, _, _ := classify(&httpError{status: c.status, body: c.body}); got != c.want {
			t.Errorf("%d %s: %q, want %q", c.status, c.body, got, c.want)
		}
	}
	if d := retryAfter("30"); d != 30*time.Second {
		t.Errorf("retryAfter(30) = %s", d)
	}
	if d := retryAfter(now().Add(90 * time.Second).Format(http.TimeFormat)); d != 90*time.Second {
		t.Errorf("retryAfter(date) = %s", d)
	}
	if d := retryAfter("999999"); d != afterMost {
		t.Errorf("retryAfter(huge) = %s", d)
	}
}

// Transient tells a failure worth one more try (no answer in time, a gateway's 5xx) from one that answers the same
// again: a request turned down, a model that isn't set up (#1532).
func TestTransient(t *testing.T) {
	status := func(code int) error {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":{"message":"no"}}`, code)
		}))
		defer srv.Close()
		_, err := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}).Complete(context.Background(), "", "hi", 10)
		return err
	}
	slow := func() error {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body) // the server notices the client went only once the body is read
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}))
		defer srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}).Complete(ctx, "", "hi", 10)
		return err
	}
	_, unset := New(Config{}).Complete(context.Background(), "", "hi", 10)
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"502", status(http.StatusBadGateway), true},
		{"500", status(http.StatusInternalServerError), true},
		{"timed out", slow(), true},
		{"400", status(http.StatusBadRequest), false},
		{"not configured", unset, false},
		{"nil", nil, false},
	} {
		if got := Transient(c.err); got != c.want {
			t.Errorf("%s: Transient(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}
