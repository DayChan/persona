package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// sessionLimitLine is Claude Code's line for a session limit that resets in about in (to the minute, in UTC), and that reset.
func sessionLimitLine(in time.Duration) (string, time.Time) {
	reset := time.Now().UTC().Add(in).Truncate(time.Minute)
	return fmt.Sprintf("You've hit your session limit · resets %s (UTC)", reset.Format("3:04pm")), reset
}

// editHealth changes launcher's record on machine in place, as time passing would.
func editHealth(s *Service, machine, launcher string, edit func(*LauncherHealth)) {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	edit(s.health.recs[healthKey(machine, launcher)])
}

func checkedCount(f *fakeHub, key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, k := range f.checked {
		if k == key {
			n++
		}
	}
	return n
}

// A subscription's usage limit is its own class whatever comes before its words; a gateway's 429, rate limit or quota
// keeps the class it had.
func TestLauncherHealthClassifiesUsageLimits(t *testing.T) {
	for text, want := range map[string]string{
		"You've hit your session limit · resets 4:20pm (Asia/Shanghai)":                              errKindUsageLimit,
		"Error: You've hit your weekly limit · resets Sep 28, 4pm (America/New_York)":                errKindUsageLimit,
		"You've hit your limit · resets 9am (UTC)":                                                   errKindUsageLimit,
		"Claude AI usage limit reached|1790000000":                                                   errKindUsageLimit,
		"ERROR: You've hit your usage limit. Upgrade to Pro (<url>) or try again at 3:05 PM.":        errKindUsageLimit,
		`stream error: {"codex_error_info":"usage_limit_exceeded"}`:                                  errKindUsageLimit,
		`turn/completed: {"codexErrorInfo":"usageLimitExceeded"}`:                                    errKindUsageLimit,
		"API Error: Request rejected (429) · quota reached":                                          errKindOther,
		`API Error: 429 {"type":"error","error":{"type":"rate_limit_error","message":"rate limit"}}`: errKindOther,
		`API Error: 403 {"error":"monthly quota exceeded"}`:                                          errKindAuth,
		"You've hit your stride, nice work":                                                          errKindOther,
	} {
		if got := classifyLauncherError(text); got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
}

// Unusable until a known reset, however long ago it was recorded, and no longer after it; one too far off is misread
// and counts as no reset, like a limit whose words name no time zone (Codex's): the usual healthStale.
func TestLauncherHealthUsageLimitUnusableUntilTheReset(t *testing.T) {
	now := time.Now()
	ahead, past, absurd := now.Add(time.Hour), now.Add(-time.Minute), now.Add(9*24*time.Hour)
	limit := func(checked time.Duration, reset *time.Time) LauncherHealth {
		return LauncherHealth{State: healthFailing, Kind: errKindUsageLimit, CheckedAt: now.Add(-checked), ResetsAt: reset}
	}
	for _, c := range []struct {
		name string
		r    LauncherHealth
		want bool
	}{
		{"reset ahead, recorded longer ago than healthStale", limit(7*time.Hour, &ahead), true},
		{"reset past", limit(time.Hour, &past), false},
		{"no reset, recent", limit(time.Hour, nil), true},
		{"no reset, stale", limit(7*time.Hour, nil), false},
		{"absurd reset, recent", limit(time.Hour, &absurd), true},
		{"absurd reset, stale", limit(7*time.Hour, &absurd), false},
	} {
		if got := c.r.unusable(); got != c.want {
			t.Errorf("%s: unusable = %v, want %v", c.name, got, c.want)
		}
	}
	if _, ok := limit(time.Hour, &absurd).limitReset(); ok {
		t.Error("an absurd reset was read as one")
	}

	// Recorded: the reset off the words when they name a time zone, none when they do not; one handed in too far
	// off is dropped. A limit after a block clears the block: the request got through.
	s, f := launcherHealthHub(t)
	ctx := context.Background()
	s.healthRound(ctx, true)
	if r := healthState(s, "l4", "work"); r.State != healthBlocked {
		t.Fatalf("l4 not blocked first: %+v", r)
	}
	line, reset := sessionLimitLine(2 * time.Hour)
	r := s.recordHealth(ctx, "l4", "work", healthResult{err: "Error: " + line, source: healthSourceCheck})
	if r.State != healthFailing || r.Kind != errKindUsageLimit || r.ResetsAt == nil || !r.ResetsAt.Equal(reset) || r.BlockedReason != "" || !r.unusable() {
		t.Fatalf("with a reset: %+v", r)
	}
	r = s.recordHealth(ctx, "box", "work", healthResult{err: "You've hit your usage limit. Try again at 3:05 PM.", source: healthSourceCheck})
	if r.Kind != errKindUsageLimit || r.ResetsAt != nil || !r.unusable() {
		t.Fatalf("without a reset: %+v", r)
	}
	r = s.recordHealth(ctx, "box", "work", healthResult{err: line, source: healthSourceCheck, kind: errKindUsageLimit, resetsAt: &absurd})
	if r.ResetsAt != nil {
		t.Fatalf("an absurd reset kept: %+v", r)
	}
	if r = s.recordHealth(ctx, "l4", "work", healthResult{ok: true, source: healthSourceCheck}); r.State != healthOK || r.Kind != "" || r.ResetsAt != nil {
		t.Fatalf("after a pass: %+v", r)
	}
	if len(f.pushes) != 0 {
		t.Fatalf("pushed: %+v", f.pushes)
	}
}

// A launcher at its usage limit is routed around and listed with its reset until then; no repair round takes it
// however many times it fails, and no round spends a check on it before the reset (a check asked for still runs).
// The first round after the reset checks it, whatever the interval, and a pass clears it.
func TestLauncherHealthUsageLimitWaitsForTheReset(t *testing.T) {
	s, f := launcherHealthHub(t)
	f.hello.Agents.LauncherHealth.Repair.Skill = "agent-cli-setup" // any other failure gets a repair agent
	line, reset := sessionLimitLine(3 * time.Hour)
	f.checks["l4/work"] = proto.LauncherCheck{Agent: "claude", Error: line}
	ctx := context.Background()
	s.healthRound(ctx, true)
	r := healthState(s, "l4", "work")
	if r.State != healthFailing || r.Kind != errKindUsageLimit || r.ResetsAt == nil || !r.ResetsAt.Equal(reset) || r.Repairs != 0 || r.Repair != nil ||
		len(f.starts) != 0 || len(f.repairs) != 0 {
		t.Fatalf("l4: %+v, starts %v, repairs %v", r, f.starts, f.repairs)
	}

	editHealth(s, "l4", "work", func(r *LauncherHealth) { r.Fails, r.CheckedAt = 5, time.Now().Add(-7*time.Hour) })
	n := checkedCount(f, "l4/work")
	s.healthRound(ctx, true)
	r = healthState(s, "l4", "work")
	if !r.unusable() || r.State != healthFailing || r.Repairs != 0 || len(f.starts) != 0 || len(f.repairs) != 0 || checkedCount(f, "l4/work") != n {
		t.Fatalf("before the reset: %+v, starts %v, checks %d → %d", r, f.starts, n, checkedCount(f, "l4/work"))
	}
	s.repairMachine(ctx, Device{ID: "l4", Reachable: true}, s.healthConfig(ctx))
	if r := healthState(s, "l4", "work"); r.State != healthFailing || len(f.starts) != 0 {
		t.Fatalf("repaired: %+v, starts %v", r, f.starts)
	}

	resets := "resets_at=" + reset.Format(time.RFC3339)
	if list := s.describeAgents(ctx, "all", "", ""); !strings.Contains(list, "machine=l4 launcher=work · failing · usage_limit") || !strings.Contains(list, resets) {
		t.Fatalf("agents_list:\n%s", list)
	}
	res, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"l4","cwd":"/home/me/code/app","prompt":"修 CI"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.starts) != 1 || f.starts[0]["cwd"] != "/home/me/code/app" || !strings.Contains(res, "usage limit on l4 ("+resets) || !strings.Contains(res, "runs on server instead") {
		t.Fatalf("start %v: %s", f.starts, res)
	}
	if alt, note, moved := s.healthDetour(ctx, Device{ID: "l4"}, "work"); !moved || alt.ID != "server" || !strings.Contains(note, resets) {
		t.Fatalf("detour: %s %q %v", alt.ID, note, moved)
	}
	var route struct {
		Launchers []launcherWithHealth `json:"launchers"`
	}
	body := getJSON(t, s, "/api/v1/machines/l4/launchers")
	if err := json.Unmarshal(body, &route); err != nil || len(route.Launchers) == 0 || route.Launchers[0].Health == nil ||
		route.Launchers[0].Health.ResetsAt == nil || !route.Launchers[0].Health.ResetsAt.Equal(reset) {
		t.Fatalf("picker: %s", body)
	}

	// Asked for by hand, it runs.
	postJSON(t, s, "/api/v1/launchers/health/check?machine=l4&launcher=work")
	if checkedCount(f, "l4/work") != n+1 || healthState(s, "l4", "work").Kind != errKindUsageLimit {
		t.Fatalf("the check asked for: %d checks, %+v", checkedCount(f, "l4/work"), healthState(s, "l4", "work"))
	}

	// Reset, and checked within the interval: usable at once; the round waits a minute past the reset, then checks.
	delete(f.checks, "l4/work")
	recent := func(ago time.Duration) {
		editHealth(s, "l4", "work", func(r *LauncherHealth) {
			at := time.Now().Add(-ago)
			r.CheckedAt, r.ResetsAt = time.Now().Add(-10*time.Minute), &at
		})
	}
	recent(20 * time.Second)
	if r := healthState(s, "l4", "work"); r.unusable() {
		t.Fatalf("unusable after the reset: %+v", r)
	}
	if _, _, moved := s.healthDetour(ctx, Device{ID: "l4"}, "work"); moved {
		t.Fatal("routed around after the reset")
	}
	s.healthRound(ctx, true)
	if checkedCount(f, "l4/work") != n+1 {
		t.Fatal("checked right at the reset")
	}
	recent(2 * time.Minute)
	s.healthRound(ctx, true)
	r = healthState(s, "l4", "work")
	if checkedCount(f, "l4/work") != n+2 || r.State != healthOK || r.Kind != "" || r.ResetsAt != nil || r.Fails != 0 {
		t.Fatalf("after the reset: %d checks, %+v", checkedCount(f, "l4/work"), r)
	}
	if len(f.pushes) != 0 {
		t.Fatalf("pushed: %+v", f.pushes)
	}
}

// A repair agent's recheck that meets the usage limit does not block the launcher: the repair got it as far as the
// subscription.
func TestLauncherHealthRepairRecheckMeetsAUsageLimit(t *testing.T) {
	s, f := launcherHealthHub(t)
	f.hello.Agents.LauncherHealth.Repair.Skill = "agent-cli-setup"
	ctx := context.Background()
	s.healthRound(ctx, true)
	if r := healthState(s, "l4", "work"); r.State != healthRepairing {
		t.Fatalf("repairing: %+v", r)
	}
	line, reset := sessionLimitLine(time.Hour)
	f.mu.Lock()
	f.checks["l4/work"] = proto.LauncherCheck{Agent: "claude", Error: line}
	f.sessions["server"] = []proto.Session{{SessionID: "s1", MachineID: "server", State: proto.StateUnread, Status: proto.StatusIdle, LastText: "reconfigured the gateway"}}
	f.mu.Unlock()
	s.health.mu.Lock()
	s.health.repairs["l4"].StartedAt = time.Now().Add(-3 * time.Minute)
	s.health.mu.Unlock()
	s.followRepairs(ctx, s.healthConfig(ctx))
	r := healthState(s, "l4", "work")
	if r.State != healthFailing || r.Kind != errKindUsageLimit || r.BlockedReason != "" || r.ResetsAt == nil || !r.ResetsAt.Equal(reset) || len(s.health.repairs) != 0 {
		t.Fatalf("after the recheck: %+v, repairs %v", r, s.health.repairs)
	}
	s.setHealthState(ctx, "l4", "work", healthBlocked, "still failing", nil)
	if r := healthState(s, "l4", "work"); r.State != healthFailing {
		t.Fatalf("blocked: %+v", r)
	}
}

// An agent's own turn that ends on its usage limit marks the launcher it names on its machine until the reset, with no
// check and no repair, once; without a launcher, with the reset past, or on a gateway's 429, nothing is recorded.
func TestLauncherHealthFromAnAgentsUsageLimit(t *testing.T) {
	s, f := launcherHealthHub(t)
	ctx := context.Background()
	now := time.Now()
	line, reset := sessionLimitLine(2 * time.Hour)
	codexReset := now.Add(90 * time.Minute).UTC().Truncate(time.Second)
	old := now.Add(-2 * time.Hour).UTC().Truncate(time.Minute)
	f.setSessions("box",
		// Woken by a background task meanwhile: counted as working, its words still the limit's.
		proto.Session{SessionID: "s1", Launcher: "work", Status: proto.StatusIdle, State: proto.StateWorking, LastText: line, UpdatedAt: now},
		proto.Session{SessionID: "s2", Status: proto.StatusIdle, State: proto.StateUnread, LastText: line, UpdatedAt: now},
		proto.Session{SessionID: "s3", Launcher: "codex", Status: proto.StatusIdle, State: proto.StateUnread, UpdatedAt: now,
			TurnError: "You've hit your usage limit. Try again at 3:05 PM.", TurnErrorKind: proto.TurnErrorUsageLimit, TurnErrorResetsAt: &codexReset},
		proto.Session{SessionID: "s4", Launcher: "gw", Status: proto.StatusIdle, State: proto.StateUnread, UpdatedAt: now,
			LastText: "API Error: Request rejected (429) · quota reached"},
		proto.Session{SessionID: "s5", Launcher: "old", Status: proto.StatusIdle, State: proto.StateUnread, UpdatedAt: now.Add(-3 * time.Hour),
			LastText: fmt.Sprintf("You've hit your session limit · resets %s (UTC)", old.Format("3:04pm"))},
	)
	for _, sid := range []string{"s1", "s2", "s3", "s4", "s5", "s1"} {
		s.launcherLimitTurnEnded(ctx, "box", sid)
	}
	r := healthState(s, "box", "work")
	if r.State != healthFailing || r.Kind != errKindUsageLimit || r.Source != healthSourceSession || r.ResetsAt == nil || !r.ResetsAt.Equal(reset) ||
		r.Fails != 1 || r.Error != line || !r.unusable() {
		t.Fatalf("box/work: %+v", r)
	}
	if r := healthState(s, "box", "codex"); r.Kind != errKindUsageLimit || r.ResetsAt == nil || !r.ResetsAt.Equal(codexReset) {
		t.Fatalf("box/codex: %+v", r)
	}
	var names []string
	for _, r := range s.healthRecords(ctx, "", "") {
		names = append(names, healthKey(r.Machine, r.Launcher))
	}
	if !slices.Equal(names, []string{"box/codex", "box/work"}) {
		t.Fatalf("records: %v", names)
	}
	f.mu.Lock()
	checks, starts := len(f.checked), len(f.starts)
	f.mu.Unlock()
	if checks != 0 || starts != 0 {
		t.Fatalf("checked %d, started %d", checks, starts)
	}
	if _, note, _ := s.healthDetour(ctx, Device{ID: "box"}, "work"); !strings.Contains(note, "resets_at="+reset.Format(time.RFC3339)) {
		t.Fatalf("detour: %q", note)
	}

	// Through the turn's end.
	f.setSessions("l4", proto.Session{SessionID: "s6", Launcher: "work", Status: proto.StatusIdle, State: proto.StateUnread, LastText: line, UpdatedAt: now})
	s.TurnEnded(ctx, SessionRef{MachineID: "l4", SessionID: "s6"})
	waitFor(t, "the limit recorded", func() bool {
		r := healthState(s, "l4", "work")
		return r.Source == healthSourceSession && r.Kind == errKindUsageLimit
	})
}

// Readiness reads a usage-limited launcher as a rate limit that passes by itself, until its reset.
func TestLauncherHealthUsageLimitInReadiness(t *testing.T) {
	_, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: "你好，我是 Persona。", UpdatedAt: time.Now().Add(-time.Hour)})
	ctx := context.Background()
	line, reset := sessionLimitLine(time.Hour)
	s.recordHealth(ctx, "server", "claude", healthResult{err: line, source: healthSourceSession, kind: errKindUsageLimit, resetsAt: &reset})
	if m := readinessStep(t, getReadiness(t, s, "", ""), StepModel); m.State != StepProblem || m.Code != proto.TurnErrorRateLimited {
		t.Fatalf("limited: %+v", m)
	}
	editHealth(s, "server", "claude", func(r *LauncherHealth) { past := time.Now().Add(-time.Minute); r.ResetsAt = &past })
	if m := readinessStep(t, getReadiness(t, s, "", ""), StepModel); m.State != StepOK {
		t.Fatalf("after the reset: %+v", m)
	}
}
