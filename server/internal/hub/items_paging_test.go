package hub

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// The hub hands the node (and through it a runtime) only the paging the app gave: an empty or missing before_seq,
// limit or turns is left out rather than sent as 0, which a runtime may refuse as a bad value on every page.
func TestSessionItemsForwardsOnlyTheGivenPaging(t *testing.T) {
	for _, c := range []struct {
		query string
		want  map[string]any
	}{
		{"before_seq=&turns=2&lazy_images=1", map[string]any{"session_id": "s1", "turns": 2.0, "lazy_images": true}},
		{"", map[string]any{"session_id": "s1"}},
		{"user_inputs=1", map[string]any{"session_id": "s1", "user_inputs": true}},
		{"user_inputs=0", map[string]any{"session_id": "s1"}},
		{"before_seq=40&limit=50", map[string]any{"session_id": "s1", "before_seq": 40.0, "limit": 50.0}},
		{"before_seq=0", map[string]any{"session_id": "s1", "before_seq": 0.0}},
	} {
		var got map[string]any
		m := wireNode(t, func(p json.RawMessage) any {
			_ = json.Unmarshal(p, &got)
			if got["user_inputs"] == true {
				return map[string]any{"total": 0, "inputs": []any{}, "history": "fixture"}
			}
			return map[string]any{"items": []any{}, "has_more": false}
		})
		req := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items?"+c.query, nil)
		req.SetPathValue("sid", "s1")
		w := httptest.NewRecorder()
		(&Hub{}).sessionItems(w, req, m)
		if w.Code != 200 {
			t.Fatalf("?%s: %d %s", c.query, w.Code, w.Body)
		}
		g, _ := json.Marshal(got)
		e, _ := json.Marshal(c.want)
		if string(g) != string(e) {
			t.Errorf("?%s: the node got %s, want %s", c.query, g, e)
		}
	}
}

// Index mode has a position-only wire shape, including when no inputs exist. A node that ignores the flag
// must not silently become a zero count: older nodes keep the client's loaded-only compatibility rail.
func TestSessionItemsUserInputsWireShape(t *testing.T) {
	for _, c := range []struct {
		name     string
		response any
		status   int
		want     string
	}{
		{"empty", map[string]any{"total": 0, "inputs": []any{}, "history": "fixture"}, 200,
			`{"total":0,"inputs":[],"history":"fixture"}`},
		{"positions only", map[string]any{
			"total": 1, "inputs": []any{map[string]any{"id": "input-1#0", "seq": 7, "text": "synthetic body"}},
			"history": "fixture", "items": []any{map[string]any{"text": "synthetic body"}},
		}, 200, `{"total":1,"inputs":[{"id":"input-1#0","seq":7}],"history":"fixture"}`},
		{"older node", map[string]any{"items": []any{}, "has_more": false}, 502, ""},
		{"partial index", map[string]any{"total": 2, "inputs": []any{map[string]any{"id": "input-1#0", "seq": 7}}, "history": "fixture"}, 502, ""},
		{"null index", map[string]any{"total": 0, "inputs": nil, "history": "fixture"}, 502, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := wireNode(t, func(p json.RawMessage) any { return c.response })
			req := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items?user_inputs=1", nil)
			req.SetPathValue("sid", "s1")
			w := httptest.NewRecorder()
			(&Hub{}).sessionItems(w, req, m)
			if w.Code != c.status {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body, c.status)
			}
			if c.status == 200 {
				var got any
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(got)
				var want any
				if err := json.Unmarshal([]byte(c.want), &want); err != nil {
					t.Fatal(err)
				}
				expected, _ := json.Marshal(want)
				if string(body) != string(expected) {
					t.Fatalf("got %s, want %s", body, expected)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("the input index must not be cached")
				}
			}
		})
	}
}
