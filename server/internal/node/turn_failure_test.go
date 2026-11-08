package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Claude Code's own record of a usage limit (2.1.282's lines, trimmed; #1710): the reply it writes for the refused
// request, with the account's quota, and its notice that it goes on by itself at the reset.
const (
	claudeLimitPrompt = `{"type":"user","uuid":"u1","parentUuid":null,"isSidechain":false,"timestamp":"2026-01-01T12:10:00Z","message":{"role":"user","content":"run the checks"}}`
	claudeLimitWork   = `{"type":"assistant","uuid":"a1","parentUuid":"u1","isSidechain":false,"timestamp":"2026-01-01T12:10:04Z","message":{"id":"m1","model":"claude-opus-5-5","role":"assistant","content":[{"type":"thinking","thinking":"first the checks"}]}}`
	claudeLimitLine   = `{"type":"assistant","uuid":"e1","parentUuid":"a1","isSidechain":false,"timestamp":"2026-01-01T12:14:24Z","message":{"id":"m2","model":"<synthetic>","role":"assistant","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"You've hit your session limit · resets 4:20pm (UTC)"}]},` +
		`"quotaLimits":{"status":"rejected","resetsAt":1767284400,"rateLimitType":"five_hour"},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}`
	claudeLimitNotice = `{"type":"system","subtype":"informational","level":"notice","uuid":"n1","parentUuid":"e1","isSidechain":false,"timestamp":"2026-01-01T12:14:24Z","content":"Usage limit reached · continuing automatically at 4:20pm · esc or type to cancel"}`
	claudeLimitEnd    = `{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"n1","isSidechain":false,"timestamp":"2026-01-01T12:14:25Z","durationMs":265000}`
)

var claudeLimitReset = time.Date(2026, 1, 1, 16, 20, 0, 0, time.UTC)

func writeLines(t *testing.T, path string, lines []string) os.FileInfo {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The quick read carries Claude Code's limit with its reset and promise. A prompt line too big to parse (a long task
// notification, a long paste) still starts a turn: a wake keeps the promise, a message takes it back.
func TestQuickMetaClaudeUsageLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{claudeLimitPrompt, claudeLimitWork, claudeLimitLine, claudeLimitNotice, claudeLimitEnd}
	m := readQuickMeta(path, writeLines(t, path, lines))
	want := turnFailure{text: "You've hit your session limit · resets 4:20pm (UTC)", kind: proto.TurnErrorUsageLimit,
		resetsAt: claudeLimitReset, resumes: true, afterWork: true}
	if m.turnErr != want {
		t.Fatalf("the limit: %+v, want %+v", m.turnErr, want)
	}
	big := strings.Repeat("x", 9<<10)
	lines = append(lines, `{"type":"user","uuid":"w1","parentUuid":"d1","isSidechain":false,"timestamp":"2026-01-01T12:15:00Z","origin":{"kind":"task-notification"},`+
		`"message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n<result>`+big+`</result>\n</task-notification>"}}`)
	if m := readQuickMeta(path, writeLines(t, path, lines)); m.turnErr != (turnFailure{resumes: true}) {
		t.Fatalf("a big wake: %+v", m.turnErr)
	}
	lines = append(lines, strings.Replace(claudeLimitLine, `"uuid":"e1","parentUuid":"a1"`, `"uuid":"e2","parentUuid":"w1"`, 1))
	again := want
	again.afterWork = false
	if m := readQuickMeta(path, writeLines(t, path, lines)); m.turnErr != again {
		t.Fatalf("refused again: %+v, want %+v", m.turnErr, again)
	}
	lines = append(lines, `{"type":"user","uuid":"u2","parentUuid":"e2","isSidechain":false,"timestamp":"2026-01-01T12:16:00Z","message":{"role":"user","content":"go on `+big+`"}}`)
	if m := readQuickMeta(path, writeLines(t, path, lines)); m.turnErr != (turnFailure{}) {
		t.Fatalf("a long message: %+v", m.turnErr)
	}
}

// A Claude pane's row says the limit, its reset and the promise; a read made stale by a write since says no error for
// the few seconds until the next (as for a Codex rollout, #2185), but keeps the promise: nothing types into it then.
func TestClaudePaneUsageLimitRow(t *testing.T) {
	const id = "0d000000-0000-4000-8000-000000001710"
	p := filepath.Join(t.TempDir(), id+".jsonl")
	lines := []string{claudeLimitPrompt, claudeLimitWork, claudeLimitLine, claudeLimitNotice, claudeLimitEnd}
	writeLines(t, p, lines)
	sim := newClaudeSim(simPrompt{"run the checks", noCode})
	n := rewindNode(t, sim, "claude", proto.StatusIdle)
	n.agents = map[string]host.Agent{id: n.agents["s1"]}
	n.paths, n.metas, n.bgScans = map[string]string{id: p}, map[string]quickMeta{}, map[string]*bgScan{}
	n.files, n.watches = map[string]*transcript.File{}, map[string]*watch{}
	row := func() proto.Session {
		t.Helper()
		for _, s := range n.sessionList(context.Background(), false) {
			if s.SessionID == id {
				return s
			}
		}
		t.Fatal("no row")
		return proto.Session{}
	}
	s := row()
	if s.TurnErrorKind != proto.TurnErrorUsageLimit || s.TurnErrorResetsAt == nil || !s.TurnErrorResetsAt.Equal(claudeLimitReset) ||
		!s.TurnErrorResumes || !s.TurnErrorAfterWork || !strings.HasPrefix(s.TurnError, "You've hit your session limit") {
		t.Fatalf("the limit: %q %q %v resumes %v after work %v", s.TurnError, s.TurnErrorKind, s.TurnErrorResetsAt, s.TurnErrorResumes, s.TurnErrorAfterWork)
	}
	lines = append(lines, `{"type":"system","subtype":"away_summary","uuid":"s1","parentUuid":"d1","timestamp":"2026-01-01T12:20:00Z","content":"recap"}`)
	writeLines(t, p, lines) // within metaMinInterval: the last read stands, without its error
	if s := row(); s.TurnError != "" || s.TurnErrorKind != "" || s.TurnErrorResetsAt != nil || !s.TurnErrorResumes || s.TurnErrorAfterWork {
		t.Fatalf("a stale read: %q %q %v resumes %v after work %v", s.TurnError, s.TurnErrorKind, s.TurnErrorResetsAt, s.TurnErrorResumes, s.TurnErrorAfterWork)
	}
}

// A Codex pane whose rollout's latest turn ran into the usage limit: the reset is when the full window of its rate
// limits resets, not the message's "try again at" (no time zone).
func TestCodexPaneUsageLimit(t *testing.T) {
	n, _, _, _ := daemonPane(t)
	limited := append(append([]string{}, turnAfterTurn[:3]...), event("task_started"),
		`{"timestamp":"2026-01-01T00:00:02Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"a2","content":[{"type":"Text","text":"running the checks"}]}}}`,
		`{"timestamp":"2026-01-01T00:00:03Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex",`+
			`"primary":{"used_percent":100.0,"window_minutes":300,"resets_at":1767286800},"secondary":{"used_percent":40.0,"window_minutes":10080,"resets_at":1767690000}}}}`,
		`{"timestamp":"2026-01-01T00:00:04Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2","last_agent_message":null,`+
			`"error":{"message":"You've hit your usage limit. Try again at 5:00 PM.","codex_error_info":"usage_limit_exceeded"}}}`)
	at := time.Now()
	writeRollout(t, n.home, remoteRaw, n.home, at, at, limited...)
	s := paneRow(t, n, remoteRaw)
	if s.TurnErrorKind != proto.TurnErrorUsageLimit || s.TurnErrorResetsAt == nil ||
		!s.TurnErrorResetsAt.Equal(time.Date(2026, 1, 1, 17, 0, 0, 0, time.UTC)) || !s.TurnErrorAfterWork || s.TurnErrorResumes {
		t.Fatalf("usage limit: %q %q %v after work %v resumes %v", s.TurnError, s.TurnErrorKind, s.TurnErrorResetsAt, s.TurnErrorAfterWork, s.TurnErrorResumes)
	}
}

// What an app-server says of a failed turn comes before the rollout read has it; the reset and the work come from the
// rollout when it already has that failure.
func TestReportedTurnFailure(t *testing.T) {
	rollout := turnFailure{text: "You've hit your usage limit.", kind: proto.TurnErrorUsageLimit, resetsAt: claudeLimitReset, afterWork: true}
	var s proto.Session
	reported("You've hit your usage limit.", proto.TurnErrorUsageLimit, rollout).set(&s)
	if s.TurnErrorKind != proto.TurnErrorUsageLimit || s.TurnErrorResetsAt == nil || !s.TurnErrorResetsAt.Equal(claudeLimitReset) || !s.TurnErrorAfterWork {
		t.Fatalf("with the rollout's: %+v", s)
	}
	reported("unexpected status 503", proto.TurnErrorTransient, rollout).set(&s)
	if s.TurnError != "unexpected status 503" || s.TurnErrorResetsAt != nil || s.TurnErrorAfterWork {
		t.Fatalf("another failure: %+v", s)
	}
	reported("You've hit your usage limit.", proto.TurnErrorUsageLimit, turnFailure{}).set(&s)
	if s.TurnErrorResetsAt != nil {
		t.Fatalf("the rollout has not got it yet: %+v", s.TurnErrorResetsAt)
	}
}

// Claude Code says once that it goes on by itself; the wait's wakes write request lines of hundreds of KB, and the
// tail soon starts after the notice. What the last read found stands until a line in the tail takes it back (#1710).
func TestQuickMetaKeepsTheResumeNoticeOutOfTheTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{claudeLimitPrompt, claudeLimitWork, claudeLimitLine, claudeLimitNotice, claudeLimitEnd}
	m := readQuickMetaSince(path, writeLines(t, path, lines), quickMeta{})
	if !m.turnErr.resumes {
		t.Fatalf("the notice: %+v", m.turnErr)
	}
	blob := `{"type":"api-request-blob","content":"` + strings.Repeat("x", quickTail) + `"}`
	wake := `{"type":"user","uuid":"w1","parentUuid":"d1","isSidechain":false,"timestamp":"2026-01-01T12:15:00Z","origin":{"kind":"task-notification"},` +
		`"message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n<tool-use-id>toolu_1</tool-use-id>\n<status>completed</status>\n` +
		`<summary>Background command \"make check\" completed (exit code 0)</summary>\n</task-notification>"}}`
	refused := strings.Replace(claudeLimitLine, `"uuid":"e1","parentUuid":"a1"`, `"uuid":"e2","parentUuid":"w1"`, 1)
	lines = append(lines, blob, wake, refused)
	if m = readQuickMetaSince(path, writeLines(t, path, lines), m); m.turnErr.kind != proto.TurnErrorUsageLimit || !m.turnErr.resumes {
		t.Fatalf("the notice out of the tail: %+v", m.turnErr)
	}
	if fresh := readQuickMetaSince(path, writeLines(t, path, lines), quickMeta{}); fresh.turnErr.resumes {
		t.Fatalf("no earlier read, yet resumes: %+v", fresh.turnErr) // nothing to go by: Persona may continue it
	}
	typed := `{"type":"user","uuid":"u2","parentUuid":"e2","isSidechain":false,"timestamp":"2026-01-01T12:16:00Z","message":{"role":"user","content":"go on"}}`
	again := strings.Replace(claudeLimitLine, `"uuid":"e1","parentUuid":"a1"`, `"uuid":"e3","parentUuid":"u2"`, 1)
	lines = append(lines, typed, again)
	if m = readQuickMetaSince(path, writeLines(t, path, lines), m); m.turnErr.kind != proto.TurnErrorUsageLimit || m.turnErr.resumes {
		t.Fatalf("a message typed cancels it: %+v", m.turnErr)
	}
}
