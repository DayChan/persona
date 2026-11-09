package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
)

// summary is a summary as the assistant hands it over: one agent waiting on card ix.
func summary(ix string) link.Attention {
	return link.Attention{NeedsYou: 1, Items: []link.AttentionItem{{MachineID: "lap", SessionID: "s-1", Title: "Fix the login redirect",
		Place: "Laptop", Kind: "permission", Detail: "Bash: npm test", InteractionID: ix, Route: "/s/lap/s-1"}}}
}

const testHubAddress = "https://hub.example.com:18662"

// attentionLink is a link machine's Persona Link connected to d whose hello asks for the summary (features).
func attentionLink(t *testing.T, d *Devices, features ...string) (*testLinkClient, *mgrLink) {
	t.Helper()
	return attentionLinkWith(t, d, devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{linkEntry("Laptop")}},
		features...)
}

// attentionLinkWith is attentionLink with the hub's handover cfg.
func attentionLinkWith(t *testing.T, d *Devices, cfg devicewire.Config, features ...string) (*testLinkClient, *mgrLink) {
	t.Helper()
	d.Configure(cfg)
	return dialTestLinkHello(t, d, link.Message{Type: "hello", Machine: "lap", Version: "test", Address: testHubAddress, Features: features})
}

// noAttentionBefore says something on l after whatever the manager would send now, and checks no summary came before it.
func noAttentionBefore(t *testing.T, c *testLinkClient, l *mgrLink) {
	t.Helper()
	l.sendAttention()
	if err := l.send(link.Message{Type: "node", Error: "sentinel"}); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m := <-c.msgs:
			switch {
			case m.Type == "attention":
				t.Fatalf("a summary was sent: %+v", m.Attention)
			case m.Type == "node" && m.Error == "sentinel":
				return
			}
		case <-timeout:
			t.Fatal("the sentinel never came")
		}
	}
}

// A link that asked for the summary gets it right after its endpoints, with the hub address it went to for the web
// app while the hub has no public https address; then again whenever it changes, and only then.
func TestLinkGetsTheSummaryOnConnectAndOnChange(t *testing.T) {
	d := newTestDevices(t)
	d.SetAttention(summary("ix-1"))
	c, l := attentionLink(t, d, link.FeatureAttention)
	var seen []string
	var first link.Message
	for first.Type != "attention" {
		select {
		case first = <-c.msgs:
			seen = append(seen, first.Type)
		case <-time.After(5 * time.Second):
			t.Fatalf("no summary on connect; got %v", seen)
		}
	}
	if !strings.Contains(strings.Join(seen, " "), "endpoints") {
		t.Fatalf("the summary came before the endpoints: %v", seen)
	}
	a := first.Attention
	if a.Rev == "" || a.WebURL != testHubAddress || a.NeedsYou != 1 || len(a.Items) != 1 || a.Items[0].InteractionID != "ix-1" {
		t.Fatalf("summary %+v", a)
	}

	d.SetAttention(summary("ix-1")) // the same again: nothing to send
	noAttentionBefore(t, c, l)

	d.SetAttention(summary("ix-2"))
	if b := c.next(t, "attention").Attention; b.Rev == a.Rev || b.Items[0].InteractionID != "ix-2" {
		t.Fatalf("after a change %+v (before: %s)", b, a.Rev)
	}

	// The hub got a public https address: the summary carries it from then on.
	d.Configure(devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{linkEntry("Laptop")},
		WebURL: "https://hub.example.com/persona"})
	if b := c.next(t, "attention").Attention; b.WebURL != "https://hub.example.com/persona" || b.Items[0].InteractionID != "ix-2" {
		t.Fatalf("after the web address %+v", b)
	}
}

// An older Persona Link says nothing about the summary in its hello: it never gets one, before or after a change.
func TestLinkWithoutTheFeatureGetsNoSummary(t *testing.T) {
	d := newTestDevices(t)
	d.SetAttention(summary("ix-1"))
	c, l := attentionLink(t, d)
	c.next(t, "endpoints")
	noAttentionBefore(t, c, l)
	d.SetAttention(summary("ix-2"))
	noAttentionBefore(t, c, l)
}

// emptySummary: a, as sent to a computer that may not have the summary, tells nothing.
func emptySummary(a *link.Attention) bool {
	return a != nil && a.Rev != "" && a.NeedsYou == 0 && a.More == 0 && a.WebURL == "" && a.Items != nil && len(a.Items) == 0
}

// A computer waiting to be confirmed gets nothing of the assistant's, a work computer no personal data: both get an
// empty summary. The first gets the summary once confirmed.
func TestSummaryWithheldFromUnconfirmedAndWorkComputers(t *testing.T) {
	d := newTestDevices(t)
	d.SetAttention(summary("ix-1"))
	cfg := devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{linkEntry("Laptop")}, Pending: []string{"lap"}}
	c, _ := attentionLinkWith(t, d, cfg, link.FeatureAttention)
	if a := c.next(t, "attention").Attention; !emptySummary(a) {
		t.Fatalf("waiting to be confirmed %+v", a)
	}
	cfg.Pending = nil // confirmed
	d.Configure(cfg)
	if a := c.next(t, "attention").Attention; a.Items[0].InteractionID != "ix-1" {
		t.Fatalf("once confirmed %+v", a)
	}

	d = newTestDevices(t)
	d.SetAttention(summary("ix-1"))
	work, _ := json.Marshal(MachineConfig{ID: "lap", Name: "Laptop", Kind: kindLink, Trust: "work"})
	c, l := attentionLinkWith(t, d, devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{work}},
		link.FeatureAttention)
	if a := c.next(t, "attention").Attention; !emptySummary(a) {
		t.Fatalf("work computer %+v", a)
	}
	d.SetAttention(summary("ix-2"))
	noAttentionBefore(t, c, l) // still the same empty one
}

// A computer that had the summary and may no longer have it (here: it is waiting to be confirmed again, which
// rebuilds no machine and drops no link) gets an empty one, which takes back what it had; and the summary again once it
// may. While the hub cannot say which computers wait, nothing new goes out: what a computer has stays.
func TestSummaryTakenBackWhenItMayNoLongerHaveIt(t *testing.T) {
	d := newTestDevices(t)
	d.SetAttention(summary("ix-1"))
	cfg := devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{linkEntry("Laptop")}}
	c, l := attentionLinkWith(t, d, cfg, link.FeatureAttention)
	if a := c.next(t, "attention").Attention; a.Items[0].InteractionID != "ix-1" {
		t.Fatalf("summary %+v", a)
	}
	cfg.Pending = []string{"lap"}
	d.Configure(cfg)
	if a := c.next(t, "attention").Attention; !emptySummary(a) {
		t.Fatalf("once it waits to be confirmed %+v", a)
	}
	if l.m != d.cfgMachine("lap") || l.ctx.Err() != nil {
		t.Fatal("the machine was rebuilt or the link dropped: this tests nothing")
	}
	cfg.Pending = nil
	d.Configure(cfg)
	if a := c.next(t, "attention").Attention; a.Items[0].InteractionID != "ix-1" {
		t.Fatalf("allowed again %+v", a)
	}
	cfg.PendingUnknown = true // the hub cannot read its list: no empty summary, no new one
	d.Configure(cfg)
	d.SetAttention(summary("ix-2"))
	noAttentionBefore(t, c, l)
	cfg.PendingUnknown = false
	d.Configure(cfg)
	if a := c.next(t, "attention").Attention; a.Items[0].InteractionID != "ix-2" {
		t.Fatalf("once the hub can say again %+v", a)
	}

	// Passive mode: the connector is posted the empty summary, and the full one once allowed again.
	ac := newAttConnector(t)
	ac.set(func(ac *attConnector) { ac.features = []string{link.FeatureAttention} })
	m := serviceMachineAt(t, ac.srv, ac.fp, "jc_test")
	a := summary("ix-1")
	m.d.attention.Store(&a)
	probe(m)
	setPending := func(ids ...string) {
		m.d.mu.Lock()
		m.d.cfg.Pending = ids
		m.d.mu.Unlock()
		m.d.spreadAttention()
		sendWoken(context.Background(), m)
	}
	lastPost := func() link.Attention {
		ac.mu.Lock()
		defer ac.mu.Unlock()
		return ac.posts[len(ac.posts)-1]
	}
	setPending(m.cfg.ID)
	if last := lastPost(); !emptySummary(&last) {
		t.Fatalf("passive, taken back: last post %+v", last)
	}
	if n, held := ac.state(); n != 2 || held != lastPost().Rev {
		t.Fatalf("passive, taken back: %d posts, holds %q", n, held)
	}
	setPending()
	if n, _ := ac.state(); n != 3 || len(lastPost().Items) != 1 {
		t.Fatalf("passive, allowed again: %d posts, last %+v", n, lastPost())
	}
}

// attConnector is a connector that answers info (with the feature or not, and the Rev it holds) and takes posted
// summaries, failing the next fail of them with 500.
type attConnector struct {
	srv *httptest.Server
	fp  string

	mu       sync.Mutex
	features []string
	held     string
	posts    []link.Attention
	fail     int
	refuse   int // the status every post is answered with (counted), 0: taken
}

func newAttConnector(t *testing.T) *attConnector {
	t.Helper()
	ac := &attConnector{}
	ac.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jc_test" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		ac.mu.Lock()
		defer ac.mu.Unlock()
		switch r.URL.Path {
		case link.ConnectorInfoPath:
			_ = json.NewEncoder(w).Encode(link.ConnectorInfo{Machine: "example-service", Version: "test", Features: ac.features,
				AttentionRev: ac.held})
		case link.ConnectorAttentionPath:
			var a link.Attention
			if json.NewDecoder(r.Body).Decode(&a) != nil {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			ac.posts = append(ac.posts, a)
			if ac.refuse != 0 {
				w.Header().Set("Retry-After", "60")
				http.Error(w, `{"error":"rate_limited"}`, ac.refuse)
				return
			}
			if ac.fail > 0 {
				ac.fail--
				http.Error(w, "busy", http.StatusInternalServerError)
				return
			}
			ac.held = a.Rev
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ac.srv.Close)
	ac.fp = link.Pin(ac.srv.Certificate())
	return ac
}

func (ac *attConnector) state() (posts int, held string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return len(ac.posts), ac.held
}

func (ac *attConnector) set(change func(*attConnector)) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	change(ac)
}

// probe reads m's connector info, then does what m's sender (attentionLoop, which these tests do not start) does when
// the read woke it.
func probe(m *Machine) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.probeService(ctx)
	sendWoken(ctx, m)
}

// sendWoken does what m's sender does for a wake waiting, if one is.
func sendWoken(ctx context.Context, m *Machine) {
	select {
	case <-m.attWake:
		m.postAttention(ctx)
	default:
	}
}

// Passive mode: the manager posts the summary only to a connector whose info listed the feature, then on every change;
// a post that failed, or a connector that lost it (restarted), gets it again after the next info read.
func TestConnectorGetsTheSummaryAfterItsInfoSaysSo(t *testing.T) {
	ac := newAttConnector(t)
	m := serviceMachineAt(t, ac.srv, ac.fp, "jc_test")
	d := m.d
	a := summary("ix-1")
	d.attention.Store(&a)

	probe(m) // an older connector: nothing about the summary in its info
	if n, _ := ac.state(); n != 0 {
		t.Fatalf("posted %d to a connector that did not ask", n)
	}

	ac.set(func(ac *attConnector) { ac.features, ac.fail = []string{link.FeatureAttention}, 1 })
	probe(m) // it takes the summary now; the post fails
	if n, held := ac.state(); n != 1 || held != "" {
		t.Fatalf("after the first post: %d posts, holds %q", n, held)
	}
	probe(m) // its info says it holds none: posted again
	rev := d.attentionFor(m.cfg.ID, "").Rev
	if n, held := ac.state(); n != 2 || held != rev {
		t.Fatalf("after the retry: %d posts, holds %q, want %q", n, held, rev)
	}
	ac.mu.Lock()
	posted := ac.posts[1]
	ac.mu.Unlock()
	if posted.WebURL != "" || posted.Rev != rev || posted.Items[0].InteractionID != "ix-1" {
		t.Fatalf("posted %+v: passive mode has no hub address to fall back to without a public one", posted)
	}
	probe(m) // it holds this one: nothing to post
	if n, _ := ac.state(); n != 2 {
		t.Fatalf("posted again a summary it holds: %d posts", n)
	}

	d.SetAttention(summary("ix-2")) // a change wakes the sender at once, not with the next probe
	rev = d.attentionFor(m.cfg.ID, "").Rev
	if len(m.attWake) != 1 {
		t.Fatal("a change did not wake the sender")
	}
	sendWoken(context.Background(), m)
	if n, held := ac.state(); n != 3 || held != rev {
		t.Fatalf("after the change: %d posts, holds %q", n, held)
	}

	ac.set(func(ac *attConnector) { ac.held = "" }) // the connector restarted
	probe(m)
	if n, held := ac.state(); n != 4 || held != rev {
		t.Fatalf("after a restart: %d posts, holds %q", n, held)
	}
}

// A connector that holds the hub off (429 here; a 401 holds it off too) gets no summary until that time is up, however
// often the summary changes: its auth limiter would block the hub's address.
func TestConnectorHoldingTheHubOffGetsNoSummary(t *testing.T) {
	ac := newAttConnector(t)
	ac.set(func(ac *attConnector) {
		ac.features, ac.refuse = []string{link.FeatureAttention}, http.StatusTooManyRequests
	})
	m := serviceMachineAt(t, ac.srv, ac.fp, "jc_test")
	a := summary("ix-1")
	m.d.attention.Store(&a)
	probe(m) // posted, refused
	if n, _ := ac.state(); n != 1 {
		t.Fatalf("%d posts, want the one refused", n)
	}
	b := summary("ix-2")
	m.d.attention.Store(&b)
	m.wakeAttention()
	sendWoken(context.Background(), m)
	if n, _ := ac.state(); n != 1 {
		t.Fatalf("posted %d times to a connector holding the hub off", n)
	}
}

// Each service machine posts through its own sender: a connector that never answers a post holds up neither the info
// reads (one machine after another, probeServices) nor another machine's posts; its sender keeps at most one wake
// waiting however often it is woken, and stops with the machine, ending the post under way.
func TestConnectorSenderHoldsUpNoProbe(t *testing.T) {
	stuck := make(chan struct{})
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == link.ConnectorAttentionPath {
			select { // never answers a post until the test ends or the request is called off
			case <-stuck:
			case <-r.Context().Done():
			}
			return
		}
		_ = json.NewEncoder(w).Encode(link.ConnectorInfo{Machine: "example-service", Version: "test", Features: []string{link.FeatureAttention}})
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(stuck) })
	ms := serviceMachineAt(t, slow, link.Pin(slow.Certificate()), "jc_test")
	ok := newAttConnector(t)
	ok.set(func(ac *attConnector) { ac.features = []string{link.FeatureAttention} })
	mo := serviceMachineAt(t, ok.srv, ok.fp, "jc_test")
	a := summary("ix-1")
	ms.d.attention.Store(&a)
	mo.d.attention.Store(&a)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); ms.attentionLoop(ctx) }()
	go mo.attentionLoop(ctx)
	defer cancel()

	start := time.Now()
	for _, m := range []*Machine{ms, mo} { // as probeServices reads them
		pctx, pcancel := context.WithTimeout(context.Background(), 2*connectorDialTimeout)
		m.probeService(pctx)
		pcancel()
	}
	if took := time.Since(start); took > connectorDialTimeout/2 {
		t.Fatalf("the info reads took %s: a post that does not answer held them up", took)
	}
	eventually(t, 5*time.Second, "the other machine's post", func() bool { n, held := ok.state(); return n == 1 && held != "" })
	for range 50 {
		ms.wakeAttention()
	}
	if n := len(ms.attWake); n != 1 {
		t.Fatalf("%d wakes waiting, want at most one", n)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the sender outlived its machine")
	}
}
