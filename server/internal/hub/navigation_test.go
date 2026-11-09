package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestSessionNavigationHTTPIsALightweightNodeRead(t *testing.T) {
	var got map[string]any
	m := wireNodeMethod(t, "session.navigation", func(raw json.RawMessage) any {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Error(err)
		}
		return proto.SessionNavigation{Messages: []proto.NavigationMessage{{ID: "user-demo#text", Seq: 420, Text: "A real user question", HasImage: true}},
			HasMore: true, NextBeforeSeq: 420, History: "history-demo"}
	})
	h := &Hub{byID: map[string]*Machine{"m1": m}}
	mux := http.NewServeMux()
	h.sessionRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/api/v1/sessions/m1/demo-session/navigation?before_seq=500&limit=40&history=history-demo")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GET returned %d: %s err=%v", response.StatusCode, data, err)
	}
	want := map[string]any{"session_id": "demo-session", "before_seq": 500.0, "limit": 40.0, "history": "history-demo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("node request=%+v want=%+v", got, want)
	}
	var out proto.SessionNavigation
	if err := json.Unmarshal(data, &out); err != nil || len(out.Messages) != 1 || out.NextBeforeSeq != 420 || out.History != "history-demo" {
		t.Fatalf("navigation response=%s err=%v", data, err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	if _, exists := generic["items"]; exists {
		t.Fatal("navigation returned full transcript payloads")
	}
	if response.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content type=%q", response.Header.Get("Content-Type"))
	}
}
