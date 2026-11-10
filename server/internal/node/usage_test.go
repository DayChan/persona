package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func claudeReply(ts time.Time, id, model string, in, write1h, read, out int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":%q,"content":[],"usage":{"input_tokens":%d,`+
		`"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d,"cache_creation":{"ephemeral_1h_input_tokens":%d}}}}`+"\n",
		ts.Format(time.RFC3339Nano), id, model, in, write1h, read, out, write1h)
}

// scanFile scans path the way a sweep does, with the stat usageFiles takes of it.
func scanFile(t *testing.T, m *usageMeter, path string, cutoff time.Time) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	m.scan(path, st, cutoff)
}

func TestUsageMeterClaude(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 30, 0, time.UTC)
	cutoff := now.Add(-usageWindow)
	p := filepath.Join(t.TempDir(), "s.jsonl")
	body := claudeReply(now.Add(-30*time.Hour), "old", "claude-opus-5-5", 1, 1, 1, 1) + // before the window
		claudeReply(now, "m1", "claude-opus-5-5", 2, 100, 1000, 50) +
		claudeReply(now, "m1", "claude-opus-5-5", 2, 100, 1000, 50) + // the same reply's next content block
		`{"type":"user","timestamp":"` + now.Format(time.RFC3339) + `","message":{"content":"usage"}}` + "\n" +
		claudeReply(now, "m2", "<synthetic>", 0, 0, 0, 0) +
		claudeReply(now.Add(time.Minute), "m3", "claude-sonnet-5-5", 5, 0, 10, 7) +
		`{"type":"assistant","timestamp":"` // unfinished line: left for the next scan
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newUsageMeter()
	scanFile(t, m, p, cutoff)
	if len(m.buckets) != 2 {
		t.Fatalf("buckets = %d, want 2: %+v", len(m.buckets), m.buckets)
	}
	b := m.buckets[usageKey{now.Unix() / 60 * 60, "claude-opus-5-5", ""}]
	if b == nil || b.Input != 2 || b.CacheWrite != 100 || b.CacheRead != 1000 || b.Output != 50 || b.Requests != 1 {
		t.Fatalf("opus bucket = %+v", b)
	}
	if want := int64(len(body) - len(`{"type":"assistant","timestamp":"`)); m.files[p].off != want {
		t.Fatalf("offset = %d, want %d (before the unfinished line)", m.files[p].off, want)
	}
	// The line is finished later: only it is read.
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(now.Add(time.Minute).Format(time.RFC3339Nano) + `","message":{"id":"m4","model":"claude-sonnet-5-5","usage":{"output_tokens":3}}}` + "\n")
	f.Close()
	m.dirty = map[usageKey]bool{}
	scanFile(t, m, p, cutoff)
	s := m.buckets[usageKey{now.Unix()/60*60 + 60, "claude-sonnet-5-5", ""}]
	if s == nil || s.Output != 10 || s.Requests != 2 || len(m.dirty) != 1 {
		t.Fatalf("sonnet bucket = %+v, dirty %v", s, m.dirty)
	}
	// Reports: everything first, then only what changed, then nothing.
	params, ok := m.report(now)
	if !ok || !params["full"].(bool) {
		t.Fatalf("first report = %v", params)
	}
	if _, ok := m.report(now.Add(time.Second)); ok {
		t.Fatal("a report without changes")
	}
}

func TestUsageMeterCodex(t *testing.T) {
	now := time.Now().UTC()
	line := func(kind, payload string) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":%q,"payload":%s}`+"\n", now.Format(time.RFC3339Nano), kind, payload)
	}
	count := func(total, in, cached, out int) string {
		return line("event_msg", fmt.Sprintf(`{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},`+
			`"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}}`, total, in, cached, out))
	}
	body := line("turn_context", `{"model":"gpt-6-codex"}`) + count(1000, 900, 800, 100) +
		count(1000, 900, 800, 100) + // repeated with a rate limit update
		line("event_msg", `{"type":"token_count","info":null}`) + count(1500, 450, 400, 50)
	p := filepath.Join(t.TempDir(), "rollout-2026-10-29T10-00-00-0199a213-81c0-7800-8aa1-bbab2a035a53.jsonl")
	os.WriteFile(p, []byte(body), 0o600)
	m := newUsageMeter()
	scanFile(t, m, p, now.Add(-time.Hour))
	b := m.buckets[usageKey{now.Unix() / 60 * 60, "gpt-6-codex", ""}]
	if b == nil || b.Requests != 2 || b.Input != 150 || b.CacheRead != 1200 || b.Output != 150 {
		t.Fatalf("codex bucket = %+v", b)
	}
}

func TestUsageStartOffset(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.Add(-time.Hour)
	var sb strings.Builder
	pad := strings.Repeat("x", 4000)
	for i := 0; sb.Len() < 12<<20; i++ {
		fmt.Fprintf(&sb, `{"type":"user","timestamp":%q,"pad":%q}`+"\n", now.Add(-5*time.Hour).Format(time.RFC3339Nano), pad)
	}
	old := sb.Len()
	sb.WriteString(claudeReply(now, "m1", "claude-opus-5-5", 1, 0, 0, 9))
	p := filepath.Join(t.TempDir(), "big.jsonl")
	os.WriteFile(p, []byte(sb.String()), 0o600)
	off := startOffset(p, int64(sb.Len()), cutoff)
	if off == 0 || off > int64(old) {
		t.Fatalf("start offset = %d, want inside the old part (%d bytes)", off, old)
	}
	m := newUsageMeter()
	scanFile(t, m, p, cutoff)
	if b := m.buckets[usageKey{now.Unix() / 60 * 60, "claude-opus-5-5", ""}]; b == nil || b.Output != 9 {
		t.Fatalf("bucket = %+v", b)
	}
}

func TestEachLineSkipsHugeLines(t *testing.T) {
	huge := strings.Repeat("y", usageMaxLine+10)
	var got []string
	n := eachLine(strings.NewReader("a\n"+huge+"\nb\npartial"), func(l []byte) { got = append(got, string(l)) })
	if strings.Join(got, ",") != "a,b" || n != int64(len("a\n"+huge+"\nb\n")) {
		t.Fatalf("lines = %v, consumed %d", got, n)
	}
}

// A fork copies the replies it starts from into the new file (Claude Code's --fork-session, Codex's fork): they are
// counted once; the fork's own replies are counted, under the fork's launcher.
func TestUsageMeterCountsAForkOnce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	dir := t.TempDir()
	orig := filepath.Join(dir, "proj", "s-orig.jsonl")
	fork := filepath.Join(dir, "proj", "s-fork.jsonl")
	sub := filepath.Join(dir, "proj", "s-orig", "subagents", "agent-a1.jsonl")
	for _, p := range []string{orig, sub} {
		os.MkdirAll(filepath.Dir(p), 0o700)
	}
	shared := claudeReply(now, "m1", "claude-opus-5-5", 10, 0, 100, 5) + claudeReply(now, "m2", "claude-opus-5-5", 10, 0, 100, 5)
	os.WriteFile(orig, []byte(shared), 0o600)
	os.WriteFile(fork, []byte(shared+claudeReply(now, "m3", "claude-opus-5-5", 10, 0, 100, 5)), 0o600)
	os.WriteFile(sub, []byte(claudeReply(now, "a1", "claude-haiku-5-5", 1, 0, 0, 1)), 0o600)
	m := newUsageMeter()
	m.launcherOf = func(s string) string { return map[string]string{"s-orig": "claude", "s-fork": "sonnetcc"}[s] }
	for _, p := range []string{orig, fork, sub} {
		scanFile(t, m, p, now.Add(-time.Hour))
	}
	minute := now.Unix() / 60 * 60
	if b := m.buckets[usageKey{minute, "claude-opus-5-5", "claude"}]; b == nil || b.Requests != 2 || b.Output != 10 {
		t.Fatalf("the original's bucket = %+v", b)
	}
	if b := m.buckets[usageKey{minute, "claude-opus-5-5", "sonnetcc"}]; b == nil || b.Requests != 1 || b.Output != 5 {
		t.Fatalf("the fork's bucket = %+v (its copies of m1, m2 are not counted again)", b)
	}
	if b := m.buckets[usageKey{minute, "claude-haiku-5-5", "claude"}]; b == nil || b.Requests != 1 {
		t.Fatalf("the subagent's bucket = %+v (its session's launcher)", b)
	}
	// Rewritten from the start (the file shrank): read again, nothing counted twice.
	os.WriteFile(orig, []byte(claudeReply(now, "m1", "claude-opus-5-5", 10, 0, 100, 5)), 0o600)
	scanFile(t, m, orig, now.Add(-time.Hour))
	if b := m.buckets[usageKey{minute, "claude-opus-5-5", "claude"}]; b.Requests != 2 {
		t.Fatalf("after the rewrite = %+v", b)
	}
	// The counted replies leave with the window.
	m.prune(now.Add(time.Hour))
	if len(m.counted) != 0 {
		t.Fatalf("counted after the window = %v", m.counted)
	}
	params, _ := m.report(now)
	if params["launchers"] != true {
		t.Fatalf("report = %v: it says its buckets name their launcher", params)
	}
}

func TestUsageMeterCountsACodexForkOnce(t *testing.T) {
	now := time.Now().UTC()
	line := func(kind, payload string) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":%q,"payload":%s}`+"\n", now.Format(time.RFC3339Nano), kind, payload)
	}
	count := func(total, in, cached, out int) string {
		return line("event_msg", fmt.Sprintf(`{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},`+
			`"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}}`, total, in, cached, out))
	}
	dir := t.TempDir()
	a := filepath.Join(dir, "rollout-2026-10-29T10-00-00-0199a213-81c0-7800-8aa1-bbab2a035a53.jsonl")
	b := filepath.Join(dir, "rollout-2026-10-29T11-00-00-0199a213-81c0-7800-8aa1-ccccccccccccc.jsonl")
	history := line("turn_context", `{"model":"gpt-6-codex"}`) + count(1000, 900, 800, 100)
	os.WriteFile(a, []byte(history), 0o600)
	os.WriteFile(b, []byte(history+count(1600, 500, 400, 100)), 0o600)
	m := newUsageMeter()
	sessions := map[string]bool{}
	m.launcherOf = func(s string) string { sessions[s] = true; return "codex" }
	scanFile(t, m, a, now.Add(-time.Hour))
	scanFile(t, m, b, now.Add(-time.Hour))
	got := m.buckets[usageKey{now.Unix() / 60 * 60, "gpt-6-codex", "codex"}]
	if got == nil || got.Requests != 2 || got.Output != 200 {
		t.Fatalf("bucket = %+v, want the shared request once and the fork's own", got)
	}
	if !sessions["0199a213-81c0-7800-8aa1-bbab2a035a53"] {
		t.Fatalf("sessions asked = %v, want the rollout's thread id", sessions)
	}
}

func TestUsageSession(t *testing.T) {
	for path, want := range map[string]string{
		"/h/.claude/projects/-p/0f1e.jsonl":                                                                    "0f1e",
		"/h/.claude/projects/-p/0f1e/subagents/agent-a1.jsonl":                                                 "0f1e",
		"/h/.codex/sessions/2026/10/29/rollout-2026-10-29T10-00-00-0199a213-81c0-7800-8aa1-bbab2a035a53.jsonl": "0199a213-81c0-7800-8aa1-bbab2a035a53",
	} {
		if got := usageSession(path); got != want {
			t.Errorf("usageSession(%s) = %q, want %q", path, got, want)
		}
	}
}
