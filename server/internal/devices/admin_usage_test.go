package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/usagesource"
)

const usageTestKey = "ag-secret-key-9f8e7d6c"

// usageTestDevices is a manager's devices with its manager.json in a directory of the test's, and a client of the
// usage sources page as the hub's admin UI relays it (assistantwire.AdminHeader).
func usageTestDevices(t *testing.T, managerJSON string) (*Devices, *usageClient) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "manager.json")
	if managerJSON != "" {
		if err := os.WriteFile(file, []byte(managerJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := newTestDevices(t)
	d.opts.ConfigFile = file
	return d, &usageClient{t: t, h: d.Handler(http.NotFoundHandler()), admin: true}
}

type usageClient struct {
	t     *testing.T
	h     http.Handler
	admin bool
}

func (c *usageClient) do(method, path, body string) (int, map[string]any, *httptest.ResponseRecorder) {
	c.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if c.admin {
		r.Header.Set(assistantwire.AdminHeader, "1")
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w
}

func managerJSON(t *testing.T, d *Devices) map[string]any {
	t.Helper()
	b, err := os.ReadFile(d.opts.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return out
}

func usageFile(t *testing.T, h *Devices) map[string]any {
	t.Helper()
	u, _ := managerJSON(t, h)["usage"].(map[string]any)
	return u
}

func fakeRelay(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+usageTestKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		now := time.Now().UTC()
		switch r.URL.Path {
		case "/api/admin/accounts":
			fmt.Fprint(w, `{"accounts":[{"id":"a","name":"one","provider":"codex","status":"active"},{"id":"b","name":"two","provider":"claude","status":"active"}]}`)
		case "/api/admin/accounts/usage":
			fmt.Fprintf(w, `{"accounts":[
			 {"account_id":"a","checked_at":%[1]q,"windows":[{"kind":"five_hour","used_ratio":0.1,"resets_at":%[2]q,"checked_at":%[1]q},{"kind":"weekly","used_ratio":0.2,"resets_at":%[3]q,"checked_at":%[1]q}]},
			 {"account_id":"b","checked_at":%[1]q,"windows":[{"kind":"weekly","used_ratio":0.3,"resets_at":%[3]q,"checked_at":%[1]q}]}]}`,
				now.Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAdminUsageSourcesLifecycle(t *testing.T) {
	h, c := usageTestDevices(t, `{"token":"t","future_field":{"x":1}}`)
	var bodies []string
	do := func(method, path, body string) (int, map[string]any) {
		code, out, w := c.do(method, path, body)
		bodies = append(bodies, w.Body.String())
		return code, out
	}

	code, out := do("GET", "/api/v1/admin/usage-sources", "")
	if code != 200 || out["enabled"] != true || out["poll_minutes"] != float64(5) || len(out["sources"].([]any)) != 0 || out["limit"] != float64(8) {
		t.Fatalf("empty: %d %v", code, out)
	}

	// add: the key is saved in manager.json and never comes back
	code, out = do("POST", "/api/v1/admin/usage-sources", `{"type":"relay","url":"https://gw.example.com/","key":"`+usageTestKey+`"}`)
	src, _ := out["source"].(map[string]any)
	if code != 200 || src == nil || src["name"] != "Relay" || src["url"] != "https://gw.example.com" || src["has_key"] != true || src["enabled"] != true {
		t.Fatalf("add: %d %v", code, out)
	}
	if _, has := src["key"]; has {
		t.Fatalf("the key came back: %v", src)
	}
	id := src["id"].(string)
	file := usageFile(t, h)
	entries := file["sources"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["key"] != usageTestKey || managerJSON(t, h)["future_field"] == nil || managerJSON(t, h)["token"] != "t" {
		t.Fatalf("manager.json: %v", managerJSON(t, h))
	}
	if st, err := os.Stat(h.opts.ConfigFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", st, err)
	}
	if got := h.usageGW.config(); len(got.Sources) != 1 || got.Sources[0].Key != usageTestKey { // applied at once
		t.Fatalf("not live: %+v", got)
	}

	// rename and turn off: the key stays
	code, out = do("PUT", "/api/v1/admin/usage-sources/"+id, `{"name":"Pool","enabled":false}`)
	src, _ = out["source"].(map[string]any)
	if code != 200 || src["name"] != "Pool" || src["enabled"] != false || src["has_key"] != true {
		t.Fatalf("edit: %d %v", code, out)
	}
	entry := usageFile(t, h)["sources"].([]any)[0].(map[string]any)
	if entry["key"] != usageTestKey || entry["enabled"] != false || entry["name"] != "Pool" {
		t.Fatalf("entry: %v", entry)
	}
	do("PUT", "/api/v1/admin/usage-sources/"+id, `{"enabled":true}`)
	if _, has := usageFile(t, h)["sources"].([]any)[0].(map[string]any)["enabled"]; has {
		t.Error("on is the default and is not written")
	}

	// pointing it elsewhere needs the key again: a saved key does not follow the address
	if code, out = do("PUT", "/api/v1/admin/usage-sources/"+id, `{"url":"https://other.example.com"}`); code != 400 || out["code"] != "key_required" {
		t.Fatalf("moved: %d %v", code, out)
	}
	if usageFile(t, h)["sources"].([]any)[0].(map[string]any)["url"] != "https://gw.example.com" {
		t.Fatal("rejected edit was written")
	}
	if code, out = do("PUT", "/api/v1/admin/usage-sources/"+id, `{"url":"https://other.example.com","key":"new-key"}`); code != 200 {
		t.Fatalf("moved with key: %d %v", code, out)
	}
	if usageFile(t, h)["sources"].([]any)[0].(map[string]any)["key"] != "new-key" {
		t.Fatal("new key not saved")
	}

	// settings
	if code, out = do("PUT", "/api/v1/admin/usage-sources", `{"enabled":false,"poll_minutes":15}`); code != 200 || out["enabled"] != false || out["poll_minutes"] != float64(15) {
		t.Fatalf("settings: %d %v", code, out)
	}
	if f := usageFile(t, h); f["enabled"] != false || f["poll_minutes"] != float64(15) {
		t.Fatalf("settings file: %v", f)
	}
	do("PUT", "/api/v1/admin/usage-sources", `{"enabled":true,"poll_minutes":5}`)
	if f := usageFile(t, h); f["enabled"] != nil || f["poll_minutes"] != nil {
		t.Fatalf("defaults are not written: %v", f)
	}

	// remove
	if code, _ = do("DELETE", "/api/v1/admin/usage-sources/"+id, `{}`); code != 200 {
		t.Fatalf("delete %d", code)
	}
	if code, out = do("DELETE", "/api/v1/admin/usage-sources/"+id, `{}`); code != 404 || out["code"] != "not_found" {
		t.Fatalf("delete twice: %d %v", code, out)
	}
	if managerJSON(t, h)["usage"] != nil || len(h.usageGW.config().Sources) != 0 {
		t.Fatalf("nothing left, nothing written: %v", managerJSON(t, h))
	}

	for _, b := range bodies {
		if strings.Contains(b, usageTestKey) || strings.Contains(b, "new-key") {
			t.Errorf("a key in an answer: %s", b)
		}
	}
}

func TestAdminUsageSourcesValidation(t *testing.T) {
	h, c := usageTestDevices(t, `{"token":"t"}`)
	for body, code := range map[string]string{
		`{"type":"nope","url":"https://gw.example.com","key":"k"}`:                                           "invalid_type",
		`{"url":"https://gw.example.com","key":"k"}`:                                                         "invalid_type",
		`{"type":"relay","url":"gw.example.com","key":"k"}`:                                                  "invalid_url",
		`{"type":"relay","url":"ftp://gw.example.com","key":"k"}`:                                            "invalid_url",
		`{"type":"relay","url":"https://user:pw@gw.example.com","key":"k"}`:                                  "invalid_url",
		`{"type":"relay","url":"https://gw.example.com"}`:                                                    "key_required",
		`{"type":"relay","url":"https://gw.example.com","key":"a b"}`:                                        "invalid_key",
		`{"type":"relay","url":"https://gw.example.com","key":"k","name":"a\nb"}`:                            "invalid_name",
		`{"type":"generic","url":"https://gw.example.com"}`:                                                  "invalid_name",
		`{"type":"relay","url":"https://gw.example.com","key":"k","name":"` + strings.Repeat("x", 41) + `"}`: "invalid_name",
	} {
		if status, out, _ := c.do("POST", "/api/v1/admin/usage-sources", body); status != 400 || out["code"] != code {
			t.Errorf("%s: %d %v", body, status, out)
		}
	}
	if status, out, _ := c.do("PUT", "/api/v1/admin/usage-sources", `{"poll_minutes":0}`); status != 400 || out["code"] != "invalid_poll" {
		t.Errorf("poll: %d %v", status, out)
	}
	if status, _, _ := c.do("PUT", "/api/v1/admin/usage-sources/src-nope", `{"name":"x"}`); status != 404 {
		t.Errorf("unknown id: %d", status)
	}
	if usageFile(t, h) != nil {
		t.Errorf("a rejected request wrote manager.json: %v", managerJSON(t, h))
	}
	for i := 0; i < usageSourcesMost; i++ {
		if status, out, _ := c.do("POST", "/api/v1/admin/usage-sources", fmt.Sprintf(`{"type":"generic","url":"https://gw%d.example.com","name":"g%d"}`, i, i)); status != 200 {
			t.Fatalf("add %d: %d %v", i, status, out)
		}
	}
	if status, out, _ := c.do("POST", "/api/v1/admin/usage-sources", `{"type":"generic","url":"https://more.example.com","name":"more"}`); status != 409 || out["code"] != "too_many" {
		t.Errorf("limit: %d %v", status, out)
	}
}

func TestAdminUsageSourceTestIsOneReadOnlyRead(t *testing.T) {
	h, c := usageTestDevices(t, `{"token":"t"}`)
	gate := fakeRelay(t)

	code, out, w := c.do("POST", "/api/v1/admin/usage-sources/test", `{"type":"relay","url":"`+gate.URL+`","key":"`+usageTestKey+`"}`)
	providers, _ := out["providers"].(map[string]any)
	if code != 200 || out["ok"] != true || out["accounts"] != float64(2) || out["windows"] != float64(3) || providers["codex"] != float64(1) || providers["claude"] != float64(1) {
		t.Fatalf("test: %d %v", code, out)
	}
	if strings.Contains(w.Body.String(), usageTestKey) {
		t.Error("key in the answer")
	}
	if usageFile(t, h) != nil || len(h.usageGW.config().Sources) != 0 {
		t.Error("a test saved something")
	}
	// wrong key and a dead address are results, not server errors
	if _, out, _ := c.do("POST", "/api/v1/admin/usage-sources/test", `{"type":"relay","url":"`+gate.URL+`","key":"wrong"}`); out["ok"] != false || out["code"] != usagesource.CodeUnauthorized {
		t.Errorf("wrong key: %v", out)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if _, out, _ := c.do("POST", "/api/v1/admin/usage-sources/test", `{"type":"relay","url":"`+dead.URL+`","key":"k"}`); out["ok"] != false || out["code"] != usagesource.CodeUnreachable {
		t.Errorf("dead: %v", out)
	}
	if status, out, _ := c.do("POST", "/api/v1/admin/usage-sources/test", `{"type":"relay","url":"nope"}`); status != 400 || out["code"] != "invalid_url" {
		t.Errorf("bad url: %d %v", status, out)
	}

	// a saved source is tested with its saved key, but only at its saved address
	_, added, _ := c.do("POST", "/api/v1/admin/usage-sources", `{"type":"relay","url":"`+gate.URL+`","key":"`+usageTestKey+`"}`)
	id := added["source"].(map[string]any)["id"].(string)
	var sentKey, sentTo string
	h.usageGW.fetch = func(_ context.Context, cfg usagesource.Config, _ time.Time) (usagesource.Result, error) {
		sentKey, sentTo = cfg.Key, cfg.URL
		return usagesource.Result{}, nil
	}
	c.do("POST", "/api/v1/admin/usage-sources/test", `{"id":"`+id+`","type":"relay","url":"`+gate.URL+`"}`)
	if sentKey != usageTestKey || sentTo != gate.URL {
		t.Errorf("saved key not used: %q %q", sentKey, sentTo)
	}
	sentKey = "unset"
	c.do("POST", "/api/v1/admin/usage-sources/test", `{"id":"`+id+`","type":"relay","url":"https://elsewhere.example.com"}`)
	if sentKey != "" {
		t.Errorf("the saved key was sent to another address: %q", sentKey)
	}
}

func TestAdminUsageSourceTestKeySelection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fields  string
		wantKey string
	}{
		{"saved key", `{}`, usageTestKey},
		{"keep saved key", `{"clear_key":false,"key":""}`, usageTestKey},
		{"clear saved key", `{"clear_key":true}`, ""},
		{"clear overrides supplied key", `{"clear_key":true,"key":"new-key"}`, ""},
		{"clear overrides invalid key", `{"clear_key":true,"key":"invalid key"}`, ""},
		{"use supplied key", `{"clear_key":false,"key":" new-key "}`, "new-key"},
		{"changed address", `{"url":"https://elsewhere.example.com"}`, ""},
		{"changed type", `{"type":"relay"}`, ""},
		{"unknown source", `{"id":"other"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			const saved = `{"token":"t","usage":{"sources":[{"id":"saved","name":"Saved","type":"generic","url":"https://gw.example.com","key":"` + usageTestKey + `"}]}}`
			h, c := usageTestDevices(t, saved)
			h.usageConfigNow()
			before, err := os.Stat(h.opts.ConfigFile)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			h.usageGW.fetch = func(_ context.Context, cfg usagesource.Config, _ time.Time) (usagesource.Result, error) {
				calls++
				if cfg.Key != tc.wantKey {
					t.Errorf("tested key = %q, want %q", cfg.Key, tc.wantKey)
				}
				return usagesource.Result{}, nil
			}
			body := map[string]any{"id": "saved", "type": "generic", "url": "https://gw.example.com/"}
			if err := json.Unmarshal([]byte(tc.fields), &body); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			status, out, w := c.do("POST", "/api/v1/admin/usage-sources/test", string(data))
			if status != http.StatusOK || out["ok"] != true || calls != 1 {
				t.Errorf("test: status=%d result=%v calls=%d", status, out, calls)
			}
			if strings.Contains(w.Body.String(), usageTestKey) || strings.Contains(w.Body.String(), "new-key") {
				t.Error("test returned a key")
			}
			contents, err := os.ReadFile(h.opts.ConfigFile)
			if err != nil || string(contents) != saved {
				t.Errorf("test changed manager.json: %s, %v", contents, err)
			}
			after, err := os.Stat(h.opts.ConfigFile)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Error("test rewrote manager.json")
			}
			if got := h.usageGW.config(); len(got.Sources) != 1 || got.Sources[0].Key != usageTestKey {
				t.Errorf("test changed the saved key in memory: %+v", got)
			}
		})
	}
}

func TestAdminUsageTakesUpAHandEditOnRead(t *testing.T) {
	h, c := usageTestDevices(t, `{"token":"t","usage":{"poll_minutes":10,"sources":[{"id":"hand","name":"Hand","type":"generic","url":"https://gw.example.com/x","key":"`+usageTestKey+`"}]}}`)
	_, out, w := c.do("GET", "/api/v1/admin/usage-sources", "")
	srcs := out["sources"].([]any)
	if len(srcs) != 1 || srcs[0].(map[string]any)["has_key"] != true || out["poll_minutes"] != float64(10) || strings.Contains(w.Body.String(), usageTestKey) {
		t.Fatalf("%v", out)
	}
	if got := h.usageGW.config(); len(got.Sources) != 1 || got.PollMinutes != 10 {
		t.Errorf("not applied: %+v", got)
	}
}

// The page is the admin UI's: the hub relays it from its admin routes only and says so; a request without that is
// refused here as well, whatever role it carries.
func TestAdminUsageSourcesOnlyFromTheAdminUI(t *testing.T) {
	h, c := usageTestDevices(t, `{"token":"t"}`)
	c.admin = false
	for _, r := range [][2]string{
		{"GET", "/api/v1/admin/usage-sources"}, {"PUT", "/api/v1/admin/usage-sources"}, {"POST", "/api/v1/admin/usage-sources"},
		{"PUT", "/api/v1/admin/usage-sources/x"}, {"DELETE", "/api/v1/admin/usage-sources/x"}, {"POST", "/api/v1/admin/usage-sources/test"},
	} {
		if status, out, _ := c.do(r[0], r[1], `{"type":"generic","url":"https://gw.example.com","name":"g"}`); status != 403 || out["code"] != "login_required" {
			t.Errorf("%s %s: %d %v", r[0], r[1], status, out)
		}
	}
	if usageFile(t, h) != nil {
		t.Error("something was written")
	}
}
