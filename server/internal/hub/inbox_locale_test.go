package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The manager may have stored these errors in a different language; public responses use the current request.
func TestInboxRelayedErrorAndSavedStatusLocale(t *testing.T) {
	h := newTestHub(t)
	manager, _ := connectInboxManager(t, h)
	manager.mu.Lock() // the bus is asking the manager things already
	manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
		if r.Path == "/api/v1/inbox/missing" {
			return assistantwire.HTTPResponse{Status: 404, Header: http.Header{"Content-Type": {"application/json"}}, Body: json.RawMessage(`{"error":"no such inbox item","code":"not_found","retryable":false}`)}, nil
		}
		raw := []byte(`{"items":[{"id":"example","source":"mail","kind":"mail","title":"User title","preview":"no such inbox item","agent":{"error":"the inbox item could not be saved"}}]}`)
		return assistantwire.HTTPResponse{Status: 200, Header: http.Header{"Content-Type": {"application/json"}, "Content-Length": {strconv.Itoa(len(raw))}}, Body: raw}, nil
	}
	manager.mu.Unlock()
	routes := h.routes()
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, path := range []string{"/api/v1/inbox/missing", "/api/v1/inbox"} {
			req := localRequest("GET", path)
			req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			req.Header.Set("Accept-Language", string(lang))
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s: %s", path, rec.Body)
			}
			if path == "/api/v1/inbox/missing" {
				if rec.Code != 404 || body["error"] != i18n.T(lang, "inbox-errors.no_item") || body["code"] != "not_found" {
					t.Fatalf("%s %s: %d %v", lang, path, rec.Code, body)
				}
			} else {
				items := body["items"].([]any)
				item := items[0].(map[string]any)
				if item["agent"].(map[string]any)["error"] != i18n.T(lang, "inbox-errors.save_failed") || item["preview"] != "no such inbox item" {
					t.Fatalf("%s: %v", lang, item)
				}
			}
		}
	}
}

type inboxLocaleWriter struct {
	*httptest.ResponseRecorder
	lang i18n.Lang
}

func (w inboxLocaleWriter) Lang() i18n.Lang { return w.lang }

func TestSavedInboxReceiptUsesCurrentLocale(t *testing.T) {
	saved := clientOpResult{Status: 404, ContentType: "application/json", Body: json.RawMessage(`{"code":"not_found","error":"no such inbox item"}`)}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		rec := httptest.NewRecorder()
		writeClientOpResult(inboxLocaleWriter{rec, lang}, saved)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["error"] != i18n.T(lang, "inbox-errors.no_item") || rec.Code != 404 {
			t.Fatalf("%s: %v", lang, body)
		}
	}
	if string(saved.Body) != `{"code":"not_found","error":"no such inbox item"}` {
		t.Fatal("stored receipt changed")
	}
}
