package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Test availability (the apps' button) runs the same check as a health check: once it passes on a machine, a record of
// the launcher failing there no longer moves starts off it, with the periodic checks off as by default (#1838). A failed
// Test records nothing, nor does a passing one of a launcher without a record; not_in_terminal waits for a start.
func TestLauncherTestAvailabilityClearsAFailure(t *testing.T) {
	s, f := launcherHealthHub(t)
	f.hello.Agents.LauncherHealth.Enabled = nil
	ctx := context.Background()
	s.recordHealth(ctx, "l4", "work", healthResult{err: `API Error: 401 {"error":"invalid or missing API key"}`, source: healthSourceStart})
	if r := healthState(s, "l4", "work"); !r.unusable() || r.Kind != errKindAuth {
		t.Fatalf("before: %+v", r)
	}

	if body := postJSON(t, s, "/api/v1/launchers/work/check?machine=l4"); strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("still broken, the Test passed: %s", body)
	}
	if r := healthState(s, "l4", "work"); r.State != healthFailing || r.Source != healthSourceStart || r.Fails != 1 {
		t.Fatalf("a failed Test changed the record: %+v", r)
	}

	f.mu.Lock()
	delete(f.checks, "l4/work") // the key fixed
	f.mu.Unlock()
	if body := postJSON(t, s, "/api/v1/launchers/work/check?machine=l4"); !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("Test: %s", body)
	}
	if r := healthState(s, "l4", "work"); r.State != healthOK || r.Kind != "" || r.Fails != 0 || r.Source != healthSourceCheck || r.Model != "m" {
		t.Fatalf("after a passing Test: %+v", r)
	}
	res, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"l4","cwd":"/home/me/code/app","prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.starts) != 1 || f.starts[0]["cwd"] != "/home/me/code/app" || strings.Contains(res, "instead") || strings.Contains(res, "health check") {
		t.Fatalf("start %v: %s", f.starts, res)
	}

	postJSON(t, s, "/api/v1/launchers/work/check?machine=box")
	if r, ok := s.launcherHealth(ctx, "box", "work"); ok {
		t.Fatalf("a Test with no record kept one: %+v", r)
	}

	s.recordHealth(ctx, "box", "work", healthResult{err: "launcher missing in the terminal", source: healthSourceStart, kind: errKindNotInTerminal})
	postJSON(t, s, "/api/v1/launchers/work/check?machine=box")
	if r := healthState(s, "box", "work"); r.Kind != errKindNotInTerminal || !r.unusable() {
		t.Fatalf("a passing Test cleared not_in_terminal: %+v", r)
	}
}

// Saving a provider with a new key, gateway or model drops its failure records on every machine: what failed says
// nothing of the new setup (#1838). A change that does not touch them (its display name) keeps them, as do a
// not_in_terminal record and a usage limit until its known reset; other launchers' records are not touched.
func TestLauncherSaveForgetsItsFailures(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	if rec := request(s, "POST", "/api/v1/launchers", `{"name":"gw","base_url":"https://gw.example.com","model":"m","api_key":"old-test-key"}`); rec.Code != 200 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	fail := func() {
		s.recordHealth(ctx, "l4", "gw", healthResult{err: `API Error: 401 {"error":"invalid or missing API key"}`, source: healthSourceStart})
		s.recordHealth(ctx, "box", "gw", healthResult{err: "90 秒内没有回答", source: healthSourceCheck})
		s.recordHealth(ctx, "term", "gw", healthResult{err: "launcher missing in the terminal", source: healthSourceStart, kind: errKindNotInTerminal})
		reset := time.Now().Add(2 * time.Hour)
		s.recordHealth(ctx, "limited", "gw", healthResult{err: "You've hit your session limit", source: healthSourceSession, kind: errKindUsageLimit, resetsAt: &reset})
		s.recordHealth(ctx, "l4", "other", healthResult{err: `API Error: 401 {"error":"invalid or missing API key"}`, source: healthSourceStart})
	}
	fail()

	if rec := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m","display_name":"Gateway"}`); rec.Code != 200 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if r := healthState(s, "l4", "gw"); !r.unusable() {
		t.Fatalf("a display name change dropped a failure: %+v", r)
	}

	if rec := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m","api_key":"new-test-key"}`); rec.Code != 200 {
		t.Fatalf("new key: %d %s", rec.Code, rec.Body.String())
	}
	for _, m := range []string{"l4", "box"} {
		if r, ok := s.launcherHealth(ctx, m, "gw"); ok {
			t.Fatalf("%s kept its failure after a new key: %+v", m, r)
		}
	}
	if r := healthState(s, "term", "gw"); r.Kind != errKindNotInTerminal || !r.unusable() {
		t.Fatalf("not_in_terminal dropped: %+v", r)
	}
	if r := healthState(s, "limited", "gw"); r.Kind != errKindUsageLimit || !r.unusable() {
		t.Fatalf("usage limit dropped before its reset: %+v", r)
	}
	if r := healthState(s, "l4", "other"); !r.unusable() {
		t.Fatalf("another launcher's record dropped: %+v", r)
	}

	fail()
	if rec := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m2"}`); rec.Code != 200 {
		t.Fatalf("new model: %d %s", rec.Code, rec.Body.String())
	}
	if r, ok := s.launcherHealth(ctx, "l4", "gw"); ok {
		t.Fatalf("kept its failure after a new model: %+v", r)
	}
}
