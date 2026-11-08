package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// turnErr is what a Meta says of the latest turn's error, for comparing.
type turnErr struct {
	text, kind         string
	reset              time.Time
	resumes, afterWork bool
}

func errOf(m *Meta) turnErr {
	return turnErr{text: m.TurnError, kind: m.TurnErrorKind, reset: m.TurnErrorResetsAt, resumes: m.TurnErrorResumes,
		afterWork: m.TurnErrorAfterWork}
}

const fixtureLimit = "You've hit your session limit · resets 4:20pm (UTC)"

var fixtureReset = time.Date(2026, 1, 1, 16, 20, 0, 0, time.UTC)

// The usage-limit fixture read a line at a time, the way the node's quick read sees a session that is waiting for its
// limit: the limit after some work, Claude Code's word that it goes on by itself, the turns background tasks wake it
// for (each refused at once, no new notice), a subagent's lines, then the reset and the work it resumes (#1710).
func TestClaudeUsageLimitWait(t *testing.T) {
	limited := turnErr{text: fixtureLimit, kind: proto.TurnErrorUsageLimit, reset: fixtureReset}
	resuming := limited
	resuming.resumes = true
	afterWork := limited
	afterWork.afterWork = true
	afterWorkResuming := afterWork
	afterWorkResuming.resumes = true
	waking := turnErr{resumes: true}
	want := map[int]turnErr{ // line number in the fixture → the error after it
		1:  {},
		5:  afterWork,         // the limit, after thinking and a tool call
		6:  afterWorkResuming, // "continuing automatically at 4:20pm"
		7:  afterWorkResuming, // turn_duration
		10: waking,            // a background task wakes it: the error is over, the promise is not
		11: resuming,          // refused again at once: no work in that turn
		13: resuming,          // a subagent's reply in the session's transcript
		14: resuming,          // and the limit refusing the subagent
		19: resuming,
		22: resuming, // "Usage limit reset · continuing automatically": its continuation
		23: resuming, // Claude Code's own prompt (isMeta) starts no turn
		24: {},       // the first reply: the limit is over
		29: {},       // a subagent still at the limit
	}
	f, err := os.Open(filepath.Join("testdata", "claude-usage-limit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var m Meta
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		ParseLine(n, sc.Bytes(), &m)
		if w, ok := want[n]; ok {
			if got := errOf(&m); got != w {
				t.Errorf("after line %d: %+v, want %+v", n, got, w)
			}
		}
	}
}

func claudeLine(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// apiErrorLine is Claude Code's own reply for a failed request (2.1.282's shape, trimmed).
func apiErrorLine(t *testing.T, uuid, text string, extra map[string]any) string {
	t.Helper()
	fields := map[string]any{"type": "assistant", "uuid": uuid, "timestamp": "2026-01-01T12:00:00Z", "isSidechain": false,
		"isApiErrorMessage": true, "message": map[string]any{"id": "m-" + uuid, "model": "<synthetic>", "role": "assistant",
			"usage":   map[string]any{"input_tokens": 0, "output_tokens": 0},
			"content": []any{map[string]any{"type": "text", "text": text}}}}
	for k, v := range extra {
		fields[k] = v
	}
	return claudeLine(t, fields)
}

func prompt(uuid, text string) string {
	return `{"type":"user","uuid":"` + uuid + `","timestamp":"2026-01-01T12:00:00Z","message":{"role":"user","content":"` + text + `"}}`
}

func wake(uuid string) string {
	return `{"type":"user","uuid":"` + uuid + `","timestamp":"2026-01-01T12:00:00Z","origin":{"kind":"task-notification"},` +
		`"message":{"role":"user","content":"<task-notification>\n<task-id>` + uuid + `</task-id>\n<status>completed</status>\n` +
		`<summary>Background command \"sleep\" completed (exit code 0)</summary>\n</task-notification>"}}`
}

func informational(text string) string {
	return `{"type":"system","subtype":"informational","level":"notice","uuid":"n","timestamp":"2026-01-01T12:00:00Z","content":"` + text + `"}`
}

const (
	replyLine    = `{"type":"assistant","uuid":"r","timestamp":"2026-01-01T12:00:00Z","message":{"id":"mr","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"done"}]}}`
	thinkingLine = `{"type":"assistant","uuid":"k","timestamp":"2026-01-01T12:00:00Z","message":{"id":"mk","model":"claude-opus-5-5","role":"assistant","content":[{"type":"thinking","thinking":"hm"}]}}`
	turnEndLine  = `{"type":"system","subtype":"turn_duration","uuid":"d","parentUuid":"x","timestamp":"2026-01-01T12:00:00Z","durationMs":10}`
)

// Claude Code's error field says what kind of failure it is; a gateway's 429 and an older version's reply say nothing
// the hub could not read off the words.
func TestClaudeAPIErrorKinds(t *testing.T) {
	quota := func(status string, resets any) map[string]any {
		return map[string]any{"error": "rate_limit", "apiErrorStatus": 429,
			"quotaLimits": map[string]any{"status": status, "resetsAt": resets, "rateLimitType": "five_hour"}}
	}
	for _, c := range []struct {
		name, text string
		extra      map[string]any
		kind       string
		reset      time.Time
	}{
		{"subscription limit", fixtureLimit, quota("rejected", 1767284400), proto.TurnErrorUsageLimit, fixtureReset},
		{"reset in milliseconds", fixtureLimit, quota("rejected", 1767284400000), proto.TurnErrorUsageLimit, fixtureReset},
		{"no reset", fixtureLimit, quota("rejected", 0), proto.TurnErrorUsageLimit, time.Time{}},
		{"quota not rejected", "API Error: 429", quota("allowed_warning", 1767284400), "", time.Time{}},
		{"gateway 429 (2.1.318)", "API Error: Request rejected (429) · Example gateway: quota reached.",
			map[string]any{"error": "rate_limit", "apiErrorStatus": 429, "apiErrorIsTransient": false}, "", time.Time{}},
		{"sign-in (2.1.263)", "Please run /login · API Error: 403 forbidden",
			map[string]any{"error": "authentication_failed", "apiErrorStatus": 403}, proto.TurnErrorUser, time.Time{}},
		{"sign-in expired (2.1.316)", "Login expired · Please run /login", map[string]any{"error": "authentication_failed"}, proto.TurnErrorUser, time.Time{}},
		{"model", "There's an issue with the selected model.", map[string]any{"error": "model_not_found", "apiErrorStatus": 404},
			proto.TurnErrorUser, time.Time{}},
		{"overloaded", "API Error: 529 Overloaded", map[string]any{"error": "server_error", "apiErrorStatus": 529}, proto.TurnErrorTransient, time.Time{}},
		{"unknown", "API Error: 400 bad request", map[string]any{"error": "unknown", "apiErrorStatus": 400}, "", time.Time{}},
		{"older Claude Code: no error field", "API Error: 500 internal error", nil, "", time.Time{}},
	} {
		var m Meta
		ParseLine(1, []byte(prompt("u", "go")), &m)
		ParseLine(2, []byte(apiErrorLine(t, "e", "  "+c.text+"\n", c.extra)), &m)
		if m.TurnError != c.text || m.TurnErrorKind != c.kind || !m.TurnErrorResetsAt.Equal(c.reset) || m.TurnErrorAfterWork {
			t.Errorf("%s: %q %q %v after work %v; want %q %q %v", c.name, m.TurnError, m.TurnErrorKind, m.TurnErrorResetsAt,
				m.TurnErrorAfterWork, c.text, c.kind, c.reset)
		}
		if m.LastText != "  "+c.text+"\n" {
			t.Errorf("%s: the reply is no longer the last words: %q", c.name, m.LastText)
		}
	}
}

// Whether Claude Code still goes on by itself once the limit resets: its notices say it and take it back, a wake
// leaves it, a message cancels it, a reply or another kind of failure ends it.
func TestClaudeLimitResumes(t *testing.T) {
	limit := apiErrorLine(t, "e", fixtureLimit, map[string]any{"error": "rate_limit", "apiErrorStatus": 429,
		"quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}})
	armed := informational("Usage limit reached · continuing automatically at 4:20pm · esc or type to cancel")
	for _, c := range []struct {
		name    string
		lines   []string
		resumes bool
		err     bool // the turn's error is still there
	}{
		{"the limit alone", []string{limit}, false, true},
		{"the limit and its notice", []string{limit, armed, turnEndLine}, true, true},
		{"2.1.319's wording", []string{limit, informational("Usage limit reached · continuing automatically at 4:20pm · esc to cancel")}, true, true},
		{"a wake refused again", []string{limit, armed, turnEndLine, wake("w1"), limit, turnEndLine, wake("w2"), limit}, true, true},
		{"a wake still under way", []string{limit, armed, turnEndLine, wake("w1")}, true, false},
		{"a typed message", []string{limit, armed, turnEndLine, prompt("u2", "continue"), limit}, false, true},
		{"esc", []string{limit, armed, informational("Automatic continue cancelled · /rate-limit-options to re-arm")}, false, true},
		{"turned off", []string{limit, armed, informational("Automatic continue was turned off · this task will not resume on its own")}, false, true},
		{"too far out", []string{limit, armed, informational("Automatic continue stopped · the usage limit now resets more than 24 hours out, so this task will not resume on its own")}, false, true},
		{"repeated hits", []string{limit, armed, informational("Automatic continue stopped after repeated usage limit hits · This task will not resume on its own")}, false, true},
		{"slept past the reset", []string{limit, armed, informational("Usage limit has reset · Press enter to continue")}, false, true},
		{"re-armed", []string{limit, armed, informational("Automatic continue cancelled · /rate-limit-options to re-arm"), limit,
			informational("Usage limit reached again · continuing automatically at 9:00pm · esc to cancel")}, true, true},
		{"the reset", []string{limit, armed, turnEndLine, informational("Usage limit reset · Continuing automatically")}, true, true},
		{"available again", []string{limit, armed, informational("Usage limit available again · continuing now")}, true, true},
		{"its continuation answers", []string{limit, armed, turnEndLine, informational("Usage limit reset · continuing automatically"), thinkingLine}, false, false},
		{"another failure", []string{limit, armed, turnEndLine, apiErrorLine(t, "e2", "API Error: 529 Overloaded", map[string]any{"error": "server_error"})}, false, true},
		{"a local command", []string{limit, armed, turnEndLine, prompt("u3", "<command-name>/usage</command-name>"),
			`{"type":"user","uuid":"o3","timestamp":"2026-01-01T12:00:01Z","message":{"role":"user","content":"<local-command-stdout>Usage</local-command-stdout>"}}`}, true, true},
		{"a subagent's notice", []string{limit, `{"type":"system","subtype":"informational","isSidechain":true,"uuid":"n","content":"Usage limit reached · continuing automatically at 4:20pm · esc to cancel"}`}, false, true},
	} {
		var m Meta
		ParseLine(0, []byte(prompt("u1", "go")), &m)
		for i, l := range c.lines {
			ParseLine(i+1, []byte(l), &m)
		}
		if m.TurnErrorResumes != c.resumes || (m.TurnError != "") != c.err {
			t.Errorf("%s: resumes %v, error %q; want resumes %v, an error %v", c.name, m.TurnErrorResumes, m.TurnError, c.resumes, c.err)
		}
	}
}

// AfterWork: the failed turn did something before the error (a continuation the limit refused again did not); a turn
// that ended between does not lend its work to Claude Code's own continuation.
func TestClaudeTurnErrorAfterWork(t *testing.T) {
	limit := apiErrorLine(t, "e", fixtureLimit, map[string]any{"error": "rate_limit", "quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}})
	autoPrompt := `{"type":"user","uuid":"c","timestamp":"2026-01-01T12:00:00Z","isMeta":true,"origin":{"kind":"auto-continuation"},"message":{"role":"user","content":"Continue."}}`
	for _, c := range []struct {
		name  string
		lines []string
		after bool
	}{
		{"refused at once", []string{prompt("u", "go"), limit}, false},
		{"after thinking", []string{prompt("u", "go"), thinkingLine, limit}, true},
		{"after a tool call", []string{prompt("u", "go"), `{"type":"assistant","uuid":"a","message":{"id":"ma","role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{"command":"ls"}}]}}`, limit}, true},
		{"after a reply, in the next turn", []string{prompt("u", "go"), replyLine, turnEndLine, wake("w"), limit}, false},
		{"its own continuation, refused again", []string{prompt("u", "go"), thinkingLine, limit, turnEndLine, autoPrompt, limit}, false},
		{"after a subagent's work", []string{prompt("u", "go"), `{"type":"assistant","isSidechain":true,"uuid":"s","message":{"id":"ms","role":"assistant","content":[{"type":"text","text":"x"}]}}`, limit}, false},
	} {
		var m Meta
		for i, l := range c.lines {
			ParseLine(i, []byte(l), &m)
		}
		if m.TurnError == "" || m.TurnErrorAfterWork != c.after {
			t.Errorf("%s: error %q, after work %v; want %v", c.name, m.TurnError, m.TurnErrorAfterWork, c.after)
		}
	}
}

// A subagent's transcript (every line a sidechain) is its own conversation: its limit is its error, as its words are
// its last text. In the session's transcript the same line is not the session's.
func TestClaudeTurnErrorSidechain(t *testing.T) {
	line := strings.Replace(apiErrorLine(t, "e", fixtureLimit, map[string]any{"error": "rate_limit",
		"quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}}), `"isSidechain":false`, `"isSidechain":true`, 1)
	session := Meta{}
	ParseLine(1, []byte(line), &session)
	if session.TurnError != "" {
		t.Fatalf("a subagent's limit became the session's: %q", session.TurnError)
	}
	sub := Meta{Sidechain: true}
	ParseLine(1, []byte(line), &sub)
	if sub.TurnError != fixtureLimit || sub.TurnErrorKind != proto.TurnErrorUsageLimit || !sub.TurnErrorResetsAt.Equal(fixtureReset) {
		t.Fatalf("the subagent's own transcript: %q %q %v", sub.TurnError, sub.TurnErrorKind, sub.TurnErrorResetsAt)
	}
}

// The quick read's tail: a turn gone back over in the terminal ended on the limit; the conversation left did not.
func TestTailSettlesARewoundTurnError(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u1","parentUuid":null,"timestamp":"2026-01-01T12:00:00Z","message":{"role":"user","content":"one"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-01-01T12:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"answer one"}]}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"t1","parentUuid":"a1","timestamp":"2026-01-01T12:00:06Z","durationMs":6000}`,
		`{"type":"user","uuid":"u2","parentUuid":"t1","timestamp":"2026-01-01T12:10:00Z","message":{"role":"user","content":"two"}}`,
		`{"type":"assistant","uuid":"k2","parentUuid":"u2","timestamp":"2026-01-01T12:10:05Z","message":{"id":"m2","role":"assistant","content":[{"type":"thinking","thinking":"hm"}]}}`,
		strings.Replace(apiErrorLine(t, "e2", fixtureLimit, map[string]any{"parentUuid": "k2", "error": "rate_limit",
			"quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}}), `"timestamp":"2026-01-01T12:00:00Z"`, `"timestamp":"2026-01-01T12:10:09Z"`, 1),
		informational("Usage limit reached · continuing automatically at 4:20pm · esc to cancel"),
		`{"type":"system","subtype":"turn_duration","uuid":"t2","parentUuid":"e2","timestamp":"2026-01-01T12:10:10Z","durationMs":10000}`,
	}
	read := func(lines []string) Meta {
		var tail Tail
		var m Meta
		for i, l := range lines {
			tail.Line(&m, ParseLine(i, []byte(l), &m))
		}
		tail.Settle(&m)
		return m
	}
	if m := read(lines); m.TurnError != fixtureLimit || !m.TurnErrorResumes {
		t.Fatalf("before the rewind: %q, resumes %v", m.TurnError, m.TurnErrorResumes)
	}
	recap := `{"type":"system","subtype":"away_summary","uuid":"s1","parentUuid":"t1","timestamp":"2026-01-01T12:13:00Z","content":"recap"}`
	if m := read(append(lines, recap)); m.TurnError != "" || m.TurnErrorKind != "" || !m.TurnErrorResetsAt.IsZero() || m.TurnErrorResumes || m.LastText != "answer one" {
		t.Fatalf("after the rewind: %q %q %v resumes %v, last text %q", m.TurnError, m.TurnErrorKind, m.TurnErrorResetsAt, m.TurnErrorResumes, m.LastText)
	}
}

// A Codex usage limit resets when the windows the rollout last saw full do: the latest of them, never the message's
// "try again at …" (no time zone). A model's own limit, a window not full and a sample that reset already say nothing.
func TestCodexUsageLimitReset(t *testing.T) {
	const limited = `{"timestamp":"2026-01-01T12:00:09.800Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1",` +
		`"error":{"message":"You've hit your usage limit. Try again at 5:00 PM.","codex_error_info":"usage_limit_exceeded"}}}`
	limits := func(id string, primary, secondary float64) string {
		return `{"timestamp":"2026-01-01T12:00:09.500Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{` +
			`"limit_id":` + id + `,"primary":{"used_percent":` + jsonNum(primary) + `,"window_minutes":300,"resets_at":1767286800},` +
			`"secondary":{"used_percent":` + jsonNum(secondary) + `,"window_minutes":10080,"resets_at":1767690000}}}}`
	}
	started := `{"timestamp":"2026-01-01T12:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`
	agent := `{"timestamp":"2026-01-01T12:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"a1","content":[{"type":"text","text":"working"}]}}}`
	fiveHour, weekly := time.Date(2026, 1, 1, 17, 0, 0, 0, time.UTC), time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		lines []string
		reset time.Time
		after bool
	}{
		{"the 5-hour window full", []string{started, agent, limits(`"codex"`, 100, 41), limited}, fiveHour, true},
		{"both full: the later", []string{started, limits(`null`, 100, 100), limited}, weekly, false},
		{"none full", []string{started, limits(`"codex"`, 80, 41), limited}, time.Time{}, false},
		{"no rate limits", []string{started, agent, limited}, time.Time{}, true},
		{"a model's own limit", []string{started, limits(`"premium"`, 100, 100), limited}, time.Time{}, false},
		{"the account's, then a model's", []string{limits(`"codex"`, 100, 41), started, limits(`"premium"`, 10, 10), limited}, fiveHour, false},
		{"reset before the turn ended", []string{started, limits(`"codex"`, 100, 41),
			strings.Replace(limited, "2026-01-01T12:00:09.800Z", "2026-01-01T18:00:00Z", 1)}, time.Time{}, false},
	} {
		var m Meta
		for i, l := range c.lines {
			ParseLine(i, []byte(l), &m)
		}
		if m.TurnErrorKind != proto.TurnErrorUsageLimit || !m.TurnErrorResetsAt.Equal(c.reset) || m.TurnErrorAfterWork != c.after {
			t.Errorf("%s: %q reset %v after work %v; want reset %v after work %v", c.name, m.TurnErrorKind, m.TurnErrorResetsAt,
				m.TurnErrorAfterWork, c.reset, c.after)
		}
		ParseLine(len(c.lines), []byte(strings.Replace(started, "t1", "t2", 1)), &m)
		if m.TurnError != "" || !m.TurnErrorResetsAt.IsZero() || m.TurnErrorAfterWork {
			t.Errorf("%s: the next turn keeps %q %v %v", c.name, m.TurnError, m.TurnErrorResetsAt, m.TurnErrorAfterWork)
		}
	}
	// Another failure has no reset, whatever the windows say.
	var m Meta
	for i, l := range []string{started, limits(`"codex"`, 100, 41), strings.Replace(limited, `"usage_limit_exceeded"`, `"server_overloaded"`, 1)} {
		ParseLine(i, []byte(l), &m)
	}
	if m.TurnErrorKind != proto.TurnErrorTransient || !m.TurnErrorResetsAt.IsZero() {
		t.Fatalf("overloaded: %q %v", m.TurnErrorKind, m.TurnErrorResetsAt)
	}
}

func jsonNum(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
