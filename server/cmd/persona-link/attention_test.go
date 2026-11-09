package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/link"
)

// persona-link run hands the hub's "Needs you" summary to the menu bar app and the tray as its status's attention: in
// the JSON line on stdout and in link.status.json. Once it stops, the saved status has none.
func TestKeepLinkPutsTheSummaryInItsStatus(t *testing.T) {
	t.Setenv("PERSONA_HOME", t.TempDir())
	want := link.Attention{Rev: "r1", NeedsYou: 21, More: 1, WebURL: "https://hub.example.com/persona", Items: []link.AttentionItem{{
		MachineID: "laptop", SessionID: "s-1", Title: "Fix the login redirect", Place: "Laptop · web › 2", Kind: "permission",
		Detail: "Bash: npm test", Notify: true, InteractionID: "ix-1", Since: "2026-11-09T07:00:00Z", Route: "/s/laptop/s-1"}}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != link.Path || r.Header.Get("Authorization") != "Bearer jd_x" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		var hello link.Message
		if _, b, err := c.Read(r.Context()); err != nil || json.Unmarshal(b, &hello) != nil || !slices.Contains(hello.Features, link.FeatureAttention) {
			return
		}
		b, _ := json.Marshal(link.Message{Type: "attention", Attention: &want})
		if c.Write(r.Context(), websocket.MessageText, b) != nil {
			return
		}
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "link.json")
	if err := link.Update(path, func(c *link.Config) error {
		*c = link.Config{Machine: "laptop", FP: link.Pin(srv.Certificate()), Token: "jd_x",
			Endpoints: []link.Endpoint{{URL: srv.URL, Kind: "lan"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- keepLink(ctx, path, "", &out) }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	timeout := time.After(10 * time.Second)
	for saved := false; !saved; {
		select {
		case <-tick.C:
			st, err := link.ReadStatus(path)
			if saved = err == nil && st.Attention != nil; saved && (st.State != "connected" || st.Attention.Rev != "r1" ||
				st.Attention.More != 1 || st.Attention.Items[0] != want.Items[0]) {
				t.Fatalf("saved status %+v", st)
			}
		case <-timeout:
			t.Fatal("no summary in link.status.json")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var line link.Status
	for _, raw := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var s link.Status
		if json.Unmarshal([]byte(raw), &s) == nil && s.Attention != nil {
			line = s
		}
	}
	if line.Attention == nil || line.Attention.WebURL != want.WebURL || line.Attention.NeedsYou != 21 || len(line.Attention.Items) != 1 {
		t.Fatalf("no summary in the JSON lines: %s", out.String())
	}
	if st, err := link.ReadStatus(path); err != nil || st.State != "stopped" || st.Attention != nil {
		t.Fatalf("after it stopped: %+v %v", st, err)
	}
}
