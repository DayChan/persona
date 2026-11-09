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

// A computer waiting to be confirmed gets nothing of the assistant's, a work computer no personal data: no summary for
// either. The first gets it once confirmed.
func TestSummaryWithheldFromUnconfirmedAndWorkComputers(t *testing.T) {
	d := newTestDevices(t)
	d.SetAttention(summary("ix-1"))
	cfg := devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{linkEntry("Laptop")}, Pending: []string{"lap"}}
	c, l := attentionLinkWith(t, d, cfg, link.FeatureAttention)
	c.next(t, "endpoints")
	noAttentionBefore(t, c, l)
	cfg.Pending = nil // confirmed
	d.Configure(cfg)
	if a := c.next(t, "attention").Attention; a.Items[0].InteractionID != "ix-1" {
		t.Fatalf("once confirmed %+v", a)
	}

	d = newTestDevices(t)
	d.SetAttention(summary("ix-1"))
	work, _ := json.Marshal(MachineConfig{ID: "lap", Name: "Laptop", Kind: kindLink, Trust: "work"})
	c, l = attentionLinkWith(t, d, devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{work}},
		link.FeatureAttention)
	c.next(t, "endpoints")
	noAttentionBefore(t, c, l)
	d.SetAttention(summary("ix-2"))
	noAttentionBefore(t, c, l)
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

func probe(m *Machine) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.probeService(ctx)
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
	rev := d.attentionFor("").Rev
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

	d.SetAttention(summary("ix-2")) // a change goes out at once, not with the next probe
	rev = d.attentionFor("").Rev
	eventually(t, 5*time.Second, "the changed summary posted", func() bool {
		if !m.attPost.TryLock() { // still posting: the next probe would leave its post to it
			return false
		}
		defer m.attPost.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.svc.attRev == rev
	})
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.postAttention(ctx)
	if n, _ := ac.state(); n != 1 {
		t.Fatalf("posted %d times to a connector holding the hub off", n)
	}
}
