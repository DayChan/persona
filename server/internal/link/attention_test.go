package link

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func sampleAttention(rev string) Attention {
	return Attention{Rev: rev, NeedsYou: 1, WebURL: "https://hub.example.com/persona", Items: []AttentionItem{{
		MachineID: "laptop", SessionID: "s-1", Title: "Fix the login redirect", Place: "Laptop · web › 2", Kind: "permission",
		Detail: "Bash: npm test", Notify: true, InteractionID: "ix-1", Since: "2026-11-09T07:00:00Z", Route: "/s/laptop/s-1"}}}
}

// Active mode: persona-link asks for the summary in its hello, puts each one the hub sends into its status (a message
// type it does not know changes nothing), and forgets it when the link goes: a list of the last link never shows on
// the next.
func TestLinkPutsTheSummaryInItsStatus(t *testing.T) {
	drop := make(chan struct{})
	hellos := make(chan Message, 4)
	var links atomic.Int32
	srv, _ := fakeHubHello(t, func(c *websocket.Conn, hello Message) {
		hellos <- hello
		if links.Add(1) == 1 { // the first link only
			sendJSON(t, c, `{"type":"queue_depth","depth":3}`)
			a := sampleAttention("r1")
			b, _ := json.Marshal(Message{Type: "attention", Attention: &a})
			sendJSON(t, c, string(b))
			<-drop
			return
		}
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				return
			}
		}
	})
	ch := runAgainst(t, srv, "jd_x", Options{})
	s := waitFor(t, ch, func(s Status) bool { return s.Attention != nil })
	if s.State != "connected" || s.Attention.Rev != "r1" || s.Attention.NeedsYou != 1 || len(s.Attention.Items) != 1 ||
		s.Attention.Items[0] != sampleAttention("r1").Items[0] || s.Attention.WebURL != "https://hub.example.com/persona" {
		t.Fatalf("status %+v", s)
	}
	if hello := <-hellos; !slices.Contains(hello.Features, FeatureAttention) {
		t.Fatalf("hello %+v does not ask for the summary", hello)
	}
	close(drop)
	s = waitFor(t, ch, func(s Status) bool { return s.State == "waiting" })
	if s.Attention != nil {
		t.Fatalf("the summary outlived its link: %+v", s.Attention)
	}
	s = waitFor(t, ch, func(s Status) bool { return s.State == "connected" }) // the next link
	if s.Attention != nil {
		t.Fatalf("a new link starts with the last one's summary: %+v", s.Attention)
	}
}

// postRaw posts body to the attention endpoint with token, as it is, and returns the status.
func (h *hubSide) postRaw(token string, body []byte) int {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.base+ConnectorAttentionPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// infoOf is the connector's info as the hub reads it with token.
func (h *hubSide) infoOf(token string) ConnectorInfo {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.base+ConnectorInfoPath, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var info ConnectorInfo
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&info) != nil {
		h.t.Fatalf("info: HTTP %d", resp.StatusCode)
	}
	return info
}

// pairedConnector is a connector paired with tokenA, and the hub's side of it.
func pairedConnector(t *testing.T) (*connector, *hubSide) {
	t.Helper()
	c := startConnector(t, newFakeNode(t).path, Options{})
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	if r := h.pair(code, tokenA); r.status != http.StatusOK {
		t.Fatalf("pair: %+v", r)
	}
	return c, h
}

// Passive mode: the connector says in its info that it takes the summary and which one it holds; only the paired hub
// may post one, within the cap; it goes with the credential when another hub pairs.
func TestPassiveTakesTheSummary(t *testing.T) {
	c, h := pairedConnector(t)
	if info := h.infoOf(tokenA); !slices.Contains(info.Features, FeatureAttention) || info.AttentionRev != "" {
		t.Fatalf("info before a summary %+v", info)
	}

	a := sampleAttention("r1")
	if r := h.do(http.MethodPost, ConnectorAttentionPath, "", a); r.status != http.StatusUnauthorized {
		t.Fatalf("without the credential: %+v", r)
	}
	if r := h.do(http.MethodPost, ConnectorAttentionPath, tokenA, Attention{NeedsYou: 1}); r.status != http.StatusBadRequest {
		t.Fatalf("without a rev: %+v", r)
	}
	big := sampleAttention("r-big")
	big.Items[0].Detail = strings.Repeat("x", AttentionBodyMax)
	if r := h.do(http.MethodPost, ConnectorAttentionPath, tokenA, big); r.status != http.StatusBadRequest {
		t.Fatalf("over the cap: %+v", r)
	}
	// The whole body counts: white space past the cap, a second value or anything after the object is refused.
	short, _ := json.Marshal(Attention{Rev: "r-short", Items: []AttentionItem{}})
	padded := func(n int) []byte { return append(slices.Clone(short), bytes.Repeat([]byte(" "), n-len(short))...) }
	for name, body := range map[string][]byte{"white space past the cap": padded(AttentionBodyMax + 1),
		"two objects": append(slices.Clone(short), short...), "something after it": append(slices.Clone(short), []byte(" x")...)} {
		if status := h.postRaw(tokenA, body); status != http.StatusBadRequest {
			t.Fatalf("%s: HTTP %d", name, status)
		}
	}
	if status := h.postRaw(tokenA, padded(AttentionBodyMax)); status != http.StatusNoContent {
		t.Fatalf("a body of exactly the cap: HTTP %d", status)
	}
	if r := h.do(http.MethodGet, ConnectorAttentionPath, tokenA, nil); r.status != http.StatusNotFound {
		t.Fatalf("GET: %+v", r)
	}
	if r := h.do(http.MethodPost, ConnectorAttentionPath, tokenA, a); r.status != http.StatusNoContent {
		t.Fatalf("post: %+v", r)
	}
	c.waitLast(t, func(s Status) bool { return s.Attention != nil })
	if s := *c.last.Load(); s.State != "listening" || s.Attention.Rev != "r1" || s.Attention.Items[0] != a.Items[0] {
		t.Fatalf("status %+v", s)
	}
	if info := h.infoOf(tokenA); info.AttentionRev != "r1" {
		t.Fatalf("info after the post %+v", info)
	}

	// Another hub pairs: the first one's summary goes with its credential.
	_, code := c.invite(t)
	if r := h.pair(code, tokenB); r.status != http.StatusOK {
		t.Fatalf("pair again: %+v", r)
	}
	c.waitLast(t, func(s Status) bool { return s.Attention == nil && s.State == "listening" })
}

// waitLast waits until the latest status is ok (waitFor takes the first one that is, which may be older).
func (c *connector) waitLast(t *testing.T, ok func(Status) bool) {
	t.Helper()
	eventually(t, func() bool { s := c.last.Load(); return s != nil && ok(*s) })
}

// A summary is dropped once the paired hub stopped coming in (it may have answered the cards meanwhile); the info says
// the connector holds none, so the hub posts it again when it is back.
func TestPassiveSummaryGoesStale(t *testing.T) {
	was := attentionStale
	attentionStale = 300 * time.Millisecond
	t.Cleanup(func() { attentionStale = was }) // after the connector's cleanup has stopped it
	c, h := pairedConnector(t)
	if r := h.do(http.MethodPost, ConnectorAttentionPath, tokenA, sampleAttention("r1")); r.status != http.StatusNoContent {
		t.Fatalf("post: %+v", r)
	}
	c.waitLast(t, func(s Status) bool { return s.Attention != nil })
	c.waitLast(t, func(s Status) bool { return s.Attention == nil && s.State == "listening" })
	if info := h.infoOf(tokenA); info.AttentionRev != "" {
		t.Fatalf("info after the summary went stale %+v", info)
	}
}
