package assistant

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// placeDo sends one request to s's routes, with role as its device's role ("" for full).
func placeDo(s *Service, role, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	s.ServeHTTP(rec, req)
	return rec
}

func listedPlaces(t *testing.T, s *Service) []Place {
	t.Helper()
	rec := placeDo(s, "", "GET", "/api/v1/assistant/places", "")
	var out struct{ Places []Place }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	return out.Places
}

// The user adds, changes and deletes saved places from the app; names are unique, coordinates are checked and the
// radius gets the reminders' default and limits.
func TestPlaceRoutes(t *testing.T) {
	s := newTestService(t, newFakeHub())
	if rec := placeDo(s, "", "GET", "/api/v1/assistant/places", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"places":[]`) {
		t.Fatalf("none yet is an empty list: %d %s", rec.Code, rec.Body)
	}
	rec := placeDo(s, "", "POST", "/api/v1/assistant/places", `{"name":" 家 ","address":"1 Example Road","latitude":31.2304,"longitude":121.4737}`)
	var created struct{ Place Place }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &created) != nil {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	home := created.Place
	if home.ID == "" || home.Name != "家" || home.RadiusMeters != placeRadiusDefault || home.CreatedAt.IsZero() {
		t.Fatalf("trimmed, with an id and the default radius: %+v", home)
	}
	rec = placeDo(s, "", "POST", "/api/v1/assistant/places", `{"name":"公司","latitude":31.24,"longitude":121.5,"radius_meters":5000}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"radius_meters":2000`) {
		t.Fatalf("a radius over the limit is clamped: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []struct{ body, why string }{
		{`{"name":"家","latitude":1,"longitude":1}`, "a name another place has"},
		{`{"name":"","latitude":1,"longitude":1}`, "no name"},
		{`{"name":"a\nb","latitude":1,"longitude":1}`, "a name on two lines"},
		{`{"name":"` + strings.Repeat("长", placeNameMax+1) + `","latitude":1,"longitude":1}`, "a name too long"},
		{`{"name":"x","latitude":91,"longitude":1}`, "a latitude off the Earth"},
		{`{"name":"x","latitude":1,"longitude":-181}`, "a longitude off the Earth"},
		{`{"name":"x","latitude":1}`, "no longitude"},
		{`{"name":"x"`, "broken JSON"},
	} {
		if rec := placeDo(s, "", "POST", "/api/v1/assistant/places", bad.body); rec.Code < 400 {
			t.Errorf("%s is refused: %d %s", bad.why, rec.Code, rec.Body)
		}
	}
	if rec := placeDo(s, "", "POST", "/api/v1/assistant/places", `{"name":"家","latitude":1,"longitude":1}`); rec.Code != 409 {
		t.Fatalf("the same name twice is a conflict: %d", rec.Code)
	}
	if got := listedPlaces(t, s); len(got) != 2 || got[0].Name != "家" || got[1].Name != "公司" {
		t.Fatalf("kept in the order added: %+v", got)
	}

	rec = placeDo(s, "", "PUT", "/api/v1/assistant/places/"+home.ID, `{"name":"家","address":"2 Example Road","latitude":31.2,"longitude":121.4,"radius_meters":50}`)
	var changed struct{ Place Place }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &changed) != nil {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if p := changed.Place; p.ID != home.ID || p.Address != "2 Example Road" || p.Latitude != 31.2 || p.RadiusMeters != placeRadiusMin ||
		!p.CreatedAt.Equal(home.CreatedAt) {
		t.Fatalf("changed in place, a small radius raised to the minimum: %+v", p)
	}
	if rec := placeDo(s, "", "PUT", "/api/v1/assistant/places/"+home.ID, `{"name":"公司","latitude":1,"longitude":1}`); rec.Code != 409 {
		t.Fatalf("renaming to another place's name is a conflict: %d", rec.Code)
	}
	if rec := placeDo(s, "", "PUT", "/api/v1/assistant/places/nope", `{"name":"x","latitude":1,"longitude":1}`); rec.Code != 404 {
		t.Fatalf("changing a place that is not there: %d", rec.Code)
	}

	for _, req := range [][3]string{{"POST", "/api/v1/assistant/places", `{"name":"x","latitude":1,"longitude":1}`},
		{"PUT", "/api/v1/assistant/places/" + home.ID, `{"name":"x","latitude":1,"longitude":1}`}, {"DELETE", "/api/v1/assistant/places/" + home.ID, ""}} {
		if rec := placeDo(s, "read", req[0], req[1], req[2]); rec.Code != 403 {
			t.Errorf("a read-only device cannot %s: %d", req[0], rec.Code)
		}
	}
	if rec := placeDo(s, "read", "GET", "/api/v1/assistant/places", ""); rec.Code != 200 {
		t.Fatalf("a read-only device sees them: %d", rec.Code)
	}

	if rec := placeDo(s, "", "DELETE", "/api/v1/assistant/places/"+home.ID, ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := placeDo(s, "", "DELETE", "/api/v1/assistant/places/"+home.ID, ""); rec.Code != 200 {
		t.Fatalf("deleting twice is fine: %d", rec.Code)
	}
	if got := listedPlaces(t, s); len(got) != 1 || got[0].Name != "公司" {
		t.Fatalf("after delete: %+v", got)
	}
}

func TestPlacesNeedADatabase(t *testing.T) {
	s := newStorelessService(t, newFakeHub())
	if rec := placeDo(s, "", "GET", "/api/v1/assistant/places", ""); rec.Code != 503 {
		t.Fatalf("list without a database: %d", rec.Code)
	}
	if rec := placeDo(s, "", "POST", "/api/v1/assistant/places", `{"name":"家","latitude":1,"longitude":1}`); rec.Code != 503 {
		t.Fatalf("add without a database: %d", rec.Code)
	}
	if _, err := runTool(s, "c1", "list_places", map[string]any{}); err == nil {
		t.Fatal("list_places without a database says so")
	}
}

func TestPlacesAreLimited(t *testing.T) {
	s := newTestService(t, newFakeHub())
	for i := range placesMax {
		body, _ := json.Marshal(map[string]any{"name": "p" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26)), "latitude": 1, "longitude": 1})
		if rec := placeDo(s, "", "POST", "/api/v1/assistant/places", string(body)); rec.Code != 200 {
			t.Fatalf("place %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec := placeDo(s, "", "POST", "/api/v1/assistant/places", `{"name":"one more","latitude":1,"longitude":1}`); rec.Code != 400 {
		t.Fatalf("past the limit: %d %s", rec.Code, rec.Body)
	}
	if _, err := runTool(s, "c1", "save_place", map[string]any{"name": "one more", "latitude": 1, "longitude": 1}); err == nil {
		t.Fatal("save_place past the limit is refused")
	}
}

// The assistant reads the saved places and saves one the user confirmed; saving a name it has changes that place.
func TestPlaceTools(t *testing.T) {
	s := newTestService(t, newFakeHub())
	out, err := runTool(s, "c1", "list_places", map[string]any{})
	if err != nil || !strings.Contains(out, "No saved places") || !strings.Contains(out, "save_place") {
		t.Fatalf("none yet: %q %v", out, err)
	}
	out, err = runTool(s, "c1", "save_place", map[string]any{"name": "公司", "address": "3 Example Road", "latitude": 31.24, "longitude": 121.5})
	if err != nil || !strings.HasPrefix(out, "Saved") || !strings.Contains(out, "radius 150 m") {
		t.Fatalf("save: %q %v", out, err)
	}
	out, err = runTool(s, "c1", "save_place", map[string]any{"name": " 公司 ", "latitude": 31.25, "longitude": 121.51, "radius_meters": 300})
	if err != nil || !strings.HasPrefix(out, "Changed") {
		t.Fatalf("the same name changes it: %q %v", out, err)
	}
	if got := listedPlaces(t, s); len(got) != 1 || got[0].Latitude != 31.25 || got[0].RadiusMeters != 300 || got[0].Address != "" {
		t.Fatalf("one place, as last saved: %+v", got)
	}
	out, err = runTool(s, "c1", "list_places", map[string]any{})
	var listed struct {
		Places []map[string]any `json:"places"`
	}
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || len(listed.Places) != 1 || listed.Places[0]["name"] != "公司" ||
		listed.Places[0]["latitude"] != 31.25 || listed.Places[0]["radius_meters"] != float64(300) || listed.Places[0]["created_at"] != nil {
		t.Fatalf("listed for the model, without the times: %q %v", out, err)
	}
	if _, err := runTool(s, "c1", "save_place", map[string]any{"name": "家"}); err == nil || !strings.Contains(err.Error(), "latitude") {
		t.Fatalf("no coordinates is refused, in English: %v", err)
	}
}
