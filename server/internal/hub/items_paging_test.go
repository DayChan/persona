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
		{"before_seq=40&limit=50", map[string]any{"session_id": "s1", "before_seq": 40.0, "limit": 50.0}},
		{"before_seq=0", map[string]any{"session_id": "s1", "before_seq": 0.0}},
		{"around_seq=420&limit=80&history=history-demo&fold_work=1&lazy_images=1", map[string]any{"session_id": "s1", "around_seq": 420.0, "limit": 80.0, "history": "history-demo", "fold_work": true, "lazy_images": true}},
	} {
		var got map[string]any
		m := wireNode(t, func(p json.RawMessage) any {
			_ = json.Unmarshal(p, &got)
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
