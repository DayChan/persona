package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// benchTranscript writes a Claude transcript of about size bytes shaped like a real session's: each line carries the
// envelope Claude Code writes (session, cwd, branch, usage), turns are a user prompt, assistant thinking and text, a
// tool call and its result, with a title line now and then that yields no item. It ends in a partial line, as a live
// session's file often does.
func benchTranscript(b *testing.B, size int) string {
	b.Helper()
	const env = `"parentUuid":"00000000-0000-4000-8000-000000000000","isSidechain":false,"userType":"external",` +
		`"cwd":"/Users/someone/projects/persona","sessionId":"11111111-2222-4333-8444-555555555555","version":"2.1.0",` +
		`"gitBranch":"perf/transcript-read","timestamp":"2026-11-05T01:00:00.000Z"`
	const usage = `"usage":{"input_tokens":3,"cache_creation_input_tokens":1520,"cache_read_input_tokens":48211,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":1520},"output_tokens":412,"service_tier":"standard"}`
	output := strings.Repeat("ok  \tgithub.com/nwverse/persona/server/internal/x\t0.012s\n", 80)
	thinking := strings.Repeat("Look at the package first, then run go test and read what fails. ", 12)
	var sb strings.Builder
	for i := 0; sb.Len() < size; i++ {
		fmt.Fprintf(&sb, `{%s,"type":"user","uuid":"u%d","message":{"role":"user","content":"run the tests for package %d"}}`+"\n", env, i, i)
		fmt.Fprintf(&sb, `{%s,"type":"assistant","uuid":"a%d","message":{"id":"m%d","type":"message","role":"assistant","model":"claude-test","content":[{"type":"thinking","thinking":%q,"signature":"c2lnbmF0dXJl"}],"stop_reason":null,%s},"requestId":"req_%d"}`+"\n", env, i, i, thinking, usage, i)
		fmt.Fprintf(&sb, `{%s,"type":"assistant","uuid":"b%d","message":{"id":"m%d","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"Running the tests now."}],"stop_reason":null,%s},"requestId":"req_%d"}`+"\n", env, i, i, usage, i)
		fmt.Fprintf(&sb, `{%s,"type":"assistant","uuid":"c%d","message":{"id":"m%d","type":"message","role":"assistant","model":"claude-test","content":[{"type":"tool_use","id":"toolu_%d","name":"Bash","input":{"command":"go test ./internal/x/...","description":"Run the tests"}}],"stop_reason":"tool_use",%s},"requestId":"req_%d"}`+"\n", env, i, i, i, usage, i)
		fmt.Fprintf(&sb, `{%s,"type":"user","uuid":"r%d","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%d","content":%q,"is_error":false}]},"toolUseResult":{"stdout":%q,"stderr":"","interrupted":false}}`+"\n", env, i, i, output, output)
		if i%4 == 0 {
			fmt.Fprintf(&sb, `{"type":"ai-title","sessionId":"11111111-2222-4333-8444-555555555555","aiTitle":"Run the tests %d"}`+"\n", i)
		}
	}
	sb.WriteString(`{"type":"user","uuid":"tail","message":{"role":"us`)
	path := filepath.Join(b.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	return path
}

// BenchmarkRefreshCold reads a file below the windowed threshold whole, as opening a session does.
func BenchmarkRefreshCold(b *testing.B) {
	path := benchTranscript(b, 4<<20)
	b.ReportAllocs()
	for b.Loop() {
		if items, err := NewFile(path).Refresh(); err != nil || len(items) == 0 {
			b.Fatal(len(items), err)
		}
	}
}

// A tool's own copy of its output (toolUseResult) is skipped, not copied: a line carrying 1 MB there and a short
// tool_result allocates about 2.6 KB measured, where copying the field took over 1 MB.
func TestToolUseResultNotCopied(t *testing.T) {
	line := []byte(`{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result",` +
		`"tool_use_id":"toolu_1","content":"ok"}]},"toolUseResult":{"stdout":"` + strings.Repeat("x", 1<<20) + `"}}`)
	if items := parseLine(1, line, &Meta{}); len(items) != 1 {
		t.Fatalf("items = %+v, want the tool result", items)
	}
	const runs = 20
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		parseLine(1, line, &Meta{})
	}
	runtime.ReadMemStats(&after)
	if per := (after.TotalAlloc - before.TotalAlloc) / runs; per > 2718 {
		t.Fatalf("parsing the line allocated %d bytes, want at most 2718", per)
	}
}

// The light copy a node keeps of a long result (File.items) holds its page and its outcome, not the output: an outcome's
// tail and saved-output path are copied out, never slices that keep the whole output alive.
func TestLightResultRetainsNoOutput(t *testing.T) {
	out := strings.Repeat("compiling a package\n", 1<<20/20) + "error: the cause" // about 1 MB
	lines := []struct {
		line  string
		codex bool
	}{
		{resultLine(quote("Exit code 1\n"+out), true, `"toolUseResult":"Error: Exit code 1"`), false},
		{resultLine(quote("<persisted-output>\nOutput too large. Full output saved to: /work/tool-results/b1.txt\n\n"+out), false, ""), false},
		{`{"type":"user","uuid":"b1","message":{"role":"user","content":` + quote("<bash-stdout>"+out+"</bash-stdout><bash-stderr></bash-stderr>") + `}}`, false},
		{`{"timestamp":"2026-10-26T05:06:06.000Z","type":"event_msg","payload":{"type":"item_completed","item":` +
			`{"type":"CommandExecution","id":"e1","command":["/bin/zsh","-lc","make"],"status":"failed","aggregated_output":` +
			quote(out) + `,"exit_code":1}}}`, true},
	}
	parse := func(kept *[]proto.Item) {
		for i, l := range lines {
			for _, it := range ParseLine(i+1, []byte(l.line), &Meta{Codex: l.codex}) {
				k, _ := light(it, false)
				*kept = append(*kept, k)
			}
		}
	}
	parse(new([]proto.Item)) // compiles the patterns parsing needs, kept for good
	const runs = 4
	kept := make([]proto.Item, 0, runs*2*len(lines))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		parse(&kept)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	results, persisted := 0, 0
	for _, it := range kept {
		if it.Kind == proto.KindToolResult && it.Outcome != nil && it.Outcome.Tail != "" {
			results++
			if it.Outcome.Persisted != nil {
				persisted++
			}
		}
	}
	if results != runs*len(lines) || persisted != runs {
		t.Fatalf("%d results with a tail and %d with a saved output, want %d and %d", results, persisted, runs*len(lines), runs)
	}
	// A page is about 4 KB and a tail at most 2 KB; a kept output would be 1 MB.
	if per := (int64(after.HeapAlloc) - int64(before.HeapAlloc)) / int64(results); per > 64<<10 {
		t.Fatalf("each kept result holds %d bytes, want at most %d", per, 64<<10)
	}
	runtime.KeepAlive(kept)
	runtime.KeepAlive(lines) // the input too: freed by the second GC, it would offset what is kept
}
