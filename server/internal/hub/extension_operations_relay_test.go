package hub

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// TestExtensionOperationRelay: a manager's extension_operation reaches only the apps that declared
// extension_operations, and stays out of the event log (#3277); other broadcasts reach every app as before.
func TestExtensionOperationRelay(t *testing.T) {
	h := newTestHub(t)
	knows := &client{out: make(chan []byte, 4), urlFeatures: map[string]bool{"extension_operations": true}}
	old := &client{out: make(chan []byte, 4)}
	h.clients[knows], h.clients[old] = struct{}{}, struct{}{}
	seq := func() int64 {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.events == nil {
			return 0
		}
		return h.events.seq
	}
	before := seq()
	if err := h.relayBroadcast(json.RawMessage(`{"type":"extension_operation","operation":{"id":"op1"}}`)); err != nil {
		t.Fatal(err)
	}
	if len(knows.out) != 1 || len(old.out) != 0 {
		t.Fatalf("to the one that knows it only: %d %d", len(knows.out), len(old.out))
	}
	if seq() != before {
		t.Fatal("it went into the event log")
	}
	if err := h.relayBroadcast(json.RawMessage(`{"type":"extension_upgrade","extension":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if len(old.out) != 1 {
		t.Fatal("another broadcast does not reach an older app")
	}
}

// TestExtensionOperationPerLanguage: with several apps on each language, every app gets the operation byte for byte
// as localizing it for that app alone gives.
func TestExtensionOperationPerLanguage(t *testing.T) {
	h := newTestHub(t)
	langs := []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja, i18n.En, i18n.ZhHans}
	clients := make([]*client, len(langs))
	for i, l := range langs {
		clients[i] = &client{hub: h, out: make(chan []byte, 4), acceptLang: l, urlFeatures: map[string]bool{extensionOperationsFeature: true}}
		h.clients[clients[i]] = struct{}{}
	}
	b := []byte(`{"type":"extension_operation","operation":{"id":"op1","kind":"install","error":"SDK error: invalid package","steps":[{"id":"nodes","machines":{"errors":{"example-machine":"SSH diagnostic: refused"}}}]}}`)
	h.toWanting(extensionOperationsFeature, b)
	for i, c := range clients {
		if len(c.out) != 1 {
			t.Fatalf("%s: %d messages", langs[i], len(c.out))
		}
		if got, want := <-c.out, i18n.ExtensionEvent(langs[i], b); !bytes.Equal(got, want) {
			t.Fatalf("%s:\n got %s\nwant %s", langs[i], got, want)
		}
	}
	if bytes.Equal(i18n.ExtensionEvent(i18n.En, b), i18n.ExtensionEvent(i18n.ZhHans, b)) {
		t.Fatal("the fixture reads the same in every language: it pins nothing")
	}
}

// TestExtensionOperationCarriesNoEventSeq: an extension_operation skips the event log, so stampEvent never sees it, yet
// the apps read event_seq from every message but hello for their resume cursor. A manager's own top-level one is taken
// out on this path too; one deeper down is the operation's own field and stays.
func TestExtensionOperationCarriesNoEventSeq(t *testing.T) {
	h := newTestHub(t)
	app := &client{hub: h, out: make(chan []byte, 4), urlFeatures: map[string]bool{extensionOperationsFeature: true}}
	h.clients[app] = struct{}{}
	if err := h.relayBroadcast(json.RawMessage(`{"event_seq":999999,"type":"extension_operation","operation":{"event_seq":5,"id":"op1"}}`)); err != nil {
		t.Fatal(err)
	}
	if len(app.out) != 1 {
		t.Fatalf("%d messages, want 1", len(app.out))
	}
	got := <-app.out
	if seqs := topLevel(t, got, "event_seq"); len(seqs) != 0 {
		t.Fatalf("the app got %s: top-level event_seq %v, want none", got, seqs)
	}
	var m struct {
		Type      string `json:"type"`
		Operation struct {
			EventSeq int    `json:"event_seq"`
			ID       string `json:"id"`
		} `json:"operation"`
	}
	if err := json.Unmarshal(got, &m); err != nil || m.Type != "extension_operation" || m.Operation.ID != "op1" || m.Operation.EventSeq != 5 {
		t.Fatalf("the app got %s (%v)", got, err)
	}
}
