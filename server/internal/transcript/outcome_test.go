package transcript

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

// resultLine is a Claude tool_result line: content is the result's JSON content, extra more top-level fields
// (`"toolUseResult":…`), or "".
func resultLine(content string, isErr bool, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"type":"user","uuid":"r1","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":[`+
		`{"type":"tool_result","tool_use_id":"toolu_1","content":%s,"is_error":%t}]}%s}`, content, isErr, extra)
}

func outcomeOf(t *testing.T, line string) *proto.ToolOutcome {
	t.Helper()
	items, _ := parse(t, line)
	it := only(t, items)
	if it.Kind != proto.KindToolResult {
		t.Fatalf("kind %q, want a tool result", it.Kind)
	}
	if it.Outcome == nil {
		t.Fatal("no outcome")
	}
	return it.Outcome
}

// checkHunks fails unless every hunk header of patch counts the lines under it.
func checkHunks(t *testing.T, patch string) {
	t.Helper()
	for hunk := range strings.SplitSeq(strings.TrimPrefix(patch, "@@ -"), "\n@@ -") {
		header, body, _ := strings.Cut(hunk, "\n")
		var oldStart, oldN, newStart, newN int
		if _, err := fmt.Sscanf(header, "%d,%d +%d,%d @@", &oldStart, &oldN, &newStart, &newN); err != nil {
			t.Fatalf("header %q: %v", header, err)
		}
		gotOld, gotNew := 0, 0
		for l := range strings.SplitSeq(body, "\n") {
			switch l[0] {
			case ' ':
				gotOld, gotNew = gotOld+1, gotNew+1
			case '-':
				gotOld++
			case '+':
				gotNew++
			}
		}
		if gotOld != oldN || gotNew != newN {
			t.Fatalf("header %q over %d old and %d new lines", header, gotOld, gotNew)
		}
	}
}

// The status comes from the line's denial kind, the record, then Claude Code's fixed words, in that order.
func TestClaudeOutcomeStatus(t *testing.T) {
	code := func(n int) *int { return &n }
	cases := []struct {
		name    string
		content string
		isErr   bool
		extra   string
		status  string
		exit    *int
	}{
		{"plain", "ok", false, "", proto.OutcomeOK, nil},
		{"no record", "done", false, ``, proto.OutcomeOK, nil},
		{"exit code", "Exit code 2\nmake: *** [all] Error 2", true, `"toolUseResult":"Error: Exit code 2"`, proto.OutcomeFailed, code(2)},
		{"exit code, no record", "Exit code 127\nsh: x: command not found", true, "", proto.OutcomeFailed, code(127)},
		{"exit code without is_error", "Exit code 1 is what grep says", false, "", proto.OutcomeOK, nil},
		{"tool_use_error", "<tool_use_error>File has not been read yet.</tool_use_error>", true, `"toolUseResult":"Error: File has not been read yet."`, proto.OutcomeBlocked, nil},
		{"worktree guard", "This agent is isolated in the worktree /work/x but this command is too complex", true, "", proto.OutcomeBlocked, nil},
		{"user rejected by kind", "anything", true, `"toolUseResult":"User rejected tool use","toolDenialKind":"user-rejected"`, proto.OutcomeDenied, nil},
		{"hook by kind", "Do not write that file.", true, `"toolDenialKind":"permission-rule"`, proto.OutcomeDenied, nil},
		{"auto mode by kind", "blocked", true, `"toolDenialKind":"automode-blocked"`, proto.OutcomeDenied, nil},
		{"aborted by kind", "stopped", true, `"toolDenialKind":"interrupted"`, proto.OutcomeInterrupted, nil},
		{"unknown kind falls back to words", "Exit code 3", true, `"toolDenialKind":"something-new"`, proto.OutcomeFailed, code(3)},
		{"user rejected by words", "The user doesn't want to proceed with this tool use. The tool use was rejected.", true, "", proto.OutcomeDenied, nil},
		{"older rejection", "The user doesn't want to take this action right now. STOP what you are doing.", true, "", proto.OutcomeDenied, nil},
		{"safety check", "Permission for this command was denied by a built-in Claude Code safety check, not by the user.", true, "", proto.OutcomeDenied, nil},
		{"rule", "Permission to use Bash has been denied.", true, "", proto.OutcomeDenied, nil},
		{"headless", "Claude requested permissions to write to /work/x, but you haven't granted it yet.", true, "", proto.OutcomeDenied, nil},
		{"hook", "PreToolUse:Bash hook error: run writes one at a time", true, "", proto.OutcomeDenied, nil},
		{"interrupted words", "[Request interrupted by user for tool use]", true, "", proto.OutcomeInterrupted, nil},
		{"interrupted record", "partial", false, `"toolUseResult":{"stdout":"partial","interrupted":true}`, proto.OutcomeInterrupted, nil},
		{"other error", "File does not exist.", true, "", proto.OutcomeFailed, nil},
		{"timed out", "Command did not complete within its 30s timeout and was moved to the background (ID: b1).", false,
			`"toolUseResult":{"stdout":"","interrupted":false,"backgroundTaskId":"b1","timedOutAfterMs":30000}`, proto.OutcomeTimedOut, nil},
		{"background", "Command running in background with ID: b2.", false, `"toolUseResult":{"backgroundTaskId":"b2"}`, proto.OutcomeBackground, nil},
		{"no match", "", false, `"toolUseResult":{"stdout":"","returnCodeInterpretation":"No matches found"}`, proto.OutcomeNoMatch, nil},
		{"files differ is fine", "1c1", false, `"toolUseResult":{"returnCodeInterpretation":"Files differ"}`, proto.OutcomeOK, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := outcomeOf(t, resultLine(quote(c.content), c.isErr, c.extra))
			if o.Status != c.status || !reflect.DeepEqual(o.ExitCode, c.exit) {
				t.Fatalf("status %q exit %v, want %q %v", o.Status, o.ExitCode, c.status, c.exit)
			}
		})
	}
}

// toolUseResult is a string, an array, a number or null on some lines, and a field can have an unexpected type: the
// line is parsed all the same, and the fields that do decode are kept.
func TestClaudeToolResultTolerant(t *testing.T) {
	for _, record := range []string{`"Error: something"`, `[{"type":"text","text":"x"}]`, `42`, `null`, `true`, `{}`} {
		o := outcomeOf(t, resultLine(`"ok"`, false, `"toolUseResult":`+record))
		if o.Status != proto.OutcomeOK || o.Files != nil || o.Git != nil {
			t.Errorf("%s: %+v", record, o)
		}
	}
	// answers of the wrong type, a type that is a number: the rest still decodes.
	o := outcomeOf(t, resultLine(`"ok"`, false, `"toolUseResult":{"answers":{"q":["a","b"]},"type":5,`+
		`"persistedOutputPath":"/work/out.txt","persistedOutputSize":12,"gitOperation":{"push":{"branch":"main"}}}`))
	if o.Persisted == nil || o.Persisted.Path != "/work/out.txt" || o.Git == nil || o.Git.Push.Branch != "main" {
		t.Fatalf("fields lost past a bad one: %+v", o)
	}
	// An agent's content is blocks, not a file's text: no line count, no file.
	o = outcomeOf(t, resultLine(`"ok"`, false, `"toolUseResult":{"status":"completed","content":[{"type":"text","text":"a\nb"}]}`))
	if o.Files != nil {
		t.Fatalf("files from an agent's result: %+v", o.Files)
	}
	// toolDenialKind that is not a string is no kind: the result stays, its status from the words.
	for _, kind := range []string{`{"x":1}`, `["user-rejected"]`, `5`, `null`, `true`} {
		o := outcomeOf(t, resultLine(`"Exit code 3"`, true, `"toolDenialKind":`+kind))
		if o.Status != proto.OutcomeFailed || o.ExitCode == nil || *o.ExitCode != 3 {
			t.Errorf("denial kind %s: %+v", kind, o)
		}
	}
}

// A line holding several results has no record of which is which: none of them takes it.
func TestClaudeOutcomeSeveralResults(t *testing.T) {
	line := `{"type":"user","uuid":"r1","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"a","content":"x"},{"type":"tool_result","tool_use_id":"b","content":"y"}]},` +
		`"toolUseResult":{"gitOperation":{"push":{"branch":"main"}}},"toolDenialKind":"user-rejected"}`
	items, _ := parse(t, line)
	if len(items) != 2 {
		t.Fatalf("items: %+v", items)
	}
	for _, it := range items {
		if it.Outcome == nil || it.Outcome.Status != proto.OutcomeOK || it.Outcome.Git != nil {
			t.Errorf("%s: %+v", it.ToolUseID, it.Outcome)
		}
	}
}

func TestClaudeOutcomeEdit(t *testing.T) {
	record := `{"filePath":"/work/a.go","oldString":"x","newString":"y","originalFile":"package a\n","userModified":false,` +
		`"replaceAll":false,"structuredPatch":[{"oldStart":3,"oldLines":3,"newStart":3,"newLines":4,"lines":[" a","-b","+c","+d"," e"]},` +
		`{"oldStart":20,"oldLines":2,"newStart":21,"newLines":1,"lines":[" f","-g","\\ No newline at end of file"]}]}`
	o := outcomeOf(t, resultLine(`"The file /work/a.go has been updated successfully."`, false, `"toolUseResult":`+record))
	want := "@@ -3,3 +3,4 @@\n a\n-b\n+c\n+d\n e\n@@ -20,2 +21,1 @@\n f\n-g\n\\ No newline at end of file"
	if o.Patch != want || o.PatchCut {
		t.Fatalf("patch %q (cut %v), want %q", o.Patch, o.PatchCut, want)
	}
	if !reflect.DeepEqual(o.Files, []proto.OutcomeFile{{Path: "/work/a.go", Adds: 2, Dels: 2}}) {
		t.Fatalf("files %+v", o.Files)
	}
	if strings.Contains(fmt.Sprint(o), "package a") {
		t.Fatal("the original file was copied")
	}
}

// Claude Code records no hunks for a file Write creates: its lines are what was added.
func TestClaudeOutcomeWriteCreate(t *testing.T) {
	for content, adds := range map[string]int{"a\nb\nc\n": 3, "a\nb\nc": 3, "": 0, "\n": 1, "a\\nb\n": 1, "x\u000ay": 2} {
		record := `{"type":"create","filePath":"/work/new.txt","content":` + quote(content) + `,"structuredPatch":[],"originalFile":null}`
		o := outcomeOf(t, resultLine(`"File created successfully at: /work/new.txt"`, false, `"toolUseResult":`+record))
		if !reflect.DeepEqual(o.Files, []proto.OutcomeFile{{Path: "/work/new.txt", Adds: adds, Created: true}}) || o.Patch != "" {
			t.Errorf("%q: files %+v patch %q, want %d adds", content, o.Files, o.Patch, adds)
		}
	}
	// \u000a written as an escape counts too.
	var n lineCount
	if err := json.Unmarshal([]byte(`"a\u000Ab\u000a"`), &n); err != nil || n != 2 {
		t.Fatalf("lines %d (%v), want 2", n, err)
	}
}

func TestClaudePatchBounded(t *testing.T) {
	var hunks []claudeHunk
	for i := range 40 {
		var lines []string
		for j := range 20 {
			lines = append(lines, fmt.Sprintf("+line %d of hunk %d %s", j, i, strings.Repeat("x", 20)))
		}
		lines = append(lines, "-gone")
		hunks = append(hunks, claudeHunk{OldStart: i*100 + 1, OldLines: 1, NewStart: i*100 + 1, NewLines: 20, Lines: lines})
	}
	patch, cut, adds, dels := claudePatch(hunks)
	if !cut || len(patch) > outcomePatchLen || len(patch) < outcomePatchLen-200 {
		t.Fatalf("cut %v, %d bytes, want cut near %d", cut, len(patch), outcomePatchLen)
	}
	if adds != 800 || dels != 40 {
		t.Fatalf("adds %d dels %d, want the whole change counted (800, 40)", adds, dels)
	}
	checkHunks(t, patch)
	// One big hunk is cut inside: its header counts the lines kept.
	var big []string
	for j := range 1000 {
		big = append(big, fmt.Sprintf(" context %d", j), fmt.Sprintf("+added %d", j))
	}
	patch, cut, adds, _ = claudePatch([]claudeHunk{{OldStart: 5, OldLines: 1000, NewStart: 5, NewLines: 2000, Lines: big}})
	if !cut || adds != 1000 || !strings.HasPrefix(patch, "@@ -5,") || strings.HasPrefix(patch, "@@ -5,1000 ") {
		t.Fatalf("cut %v adds %d, patch %.40q", cut, adds, patch)
	}
	checkHunks(t, patch)
	// A single line bigger than the whole budget leaves nothing, and says it was cut.
	patch, cut, adds, _ = claudePatch([]claudeHunk{{OldStart: 1, NewStart: 1, NewLines: 1, Lines: []string{"+" + strings.Repeat("y", outcomePatchLen)}}})
	if patch != "" || !cut || adds != 1 {
		t.Fatalf("patch %d bytes, cut %v, adds %d", len(patch), cut, adds)
	}
}

func TestClaudeOutcomeReadGitAnswersPersisted(t *testing.T) {
	o := outcomeOf(t, resultLine(`"695\tx\n696\ty"`, false,
		`"toolUseResult":{"type":"text","file":{"filePath":"/work/a.go","content":"x\ny","numLines":2,"startLine":695,"totalLines":700}}`))
	if !reflect.DeepEqual(o.Read, &proto.OutcomeRead{Start: 695, Lines: 2, Total: 700}) {
		t.Fatalf("read %+v", o.Read)
	}
	o = outcomeOf(t, resultLine(`"Wasted call — file unchanged since your last Read. Refer to that earlier tool_result instead."`, false, ""))
	if !reflect.DeepEqual(o.Read, &proto.OutcomeRead{Unchanged: true}) {
		t.Fatalf("unchanged by words: %+v", o.Read)
	}
	o = outcomeOf(t, resultLine(`"ok"`, false, `"toolUseResult":{"type":"file_unchanged","file":{"filePath":"/work/a.go"}}`))
	if !reflect.DeepEqual(o.Read, &proto.OutcomeRead{Unchanged: true}) {
		t.Fatalf("unchanged by record: %+v", o.Read)
	}
	o = outcomeOf(t, resultLine(`"[main 4f1c2ab] fix\nhttps://github.com/example/project/pull/12"`, false,
		`"toolUseResult":{"stdout":"x","gitOperation":{"commit":{"sha":"4f1c2ab","branch":"main","kind":"committed"},`+
			`"push":{"branch":"main"},"pr":{"number":12,"url":"https://github.com/example/project/pull/12","action":"created"},`+
			`"branch":{"ref":"origin/main","action":"merged"},"future":{"x":1}}}`))
	want := &proto.OutcomeGit{Commit: &proto.OutcomeCommit{SHA: "4f1c2ab", Branch: "main", Kind: "committed"}, Push: &proto.OutcomePush{Branch: "main"},
		PR: &proto.OutcomePR{Number: 12, URL: "https://github.com/example/project/pull/12", Action: "created"}, Branch: &proto.OutcomeBranch{Ref: "origin/main", Action: "merged"}}
	if !reflect.DeepEqual(o.Git, want) {
		t.Fatalf("git %+v", o.Git)
	}
	o = outcomeOf(t, resultLine(`"ok"`, false, `"toolUseResult":{"gitOperation":{"commit":{"sha":""}}}`))
	if o.Git != nil {
		t.Fatalf("an empty operation: %+v", o.Git)
	}
	long := strings.Repeat("答", 600) // 1800 bytes
	o = outcomeOf(t, resultLine(`"answered"`, false, `"toolUseResult":{"questions":[],"answers":{"Which?":"A","Long?":`+quote(long)+`}}`))
	if len(o.Answers) != 2 || o.Answers["Which?"] != "A" || len(o.Answers["Long?"]) > outcomeAnswerLen ||
		!utf8.ValidString(o.Answers["Long?"]) || !strings.HasSuffix(o.Answers["Long?"], "…") {
		t.Fatalf("answers %q", o.Answers)
	}
	many := map[string]string{}
	for i := range 40 {
		many[fmt.Sprintf("q%02d", i)] = "a"
	}
	if got := boundedAnswers(many); len(got) != outcomeAnswers || got["q00"] != "a" || got["q39"] != "" {
		t.Fatalf("bounded: %d answers", len(got))
	}
	o = outcomeOf(t, resultLine(`"<persisted-output>\nOutput too large (40.4KB). Full output saved to: /work/tool-results/b1.txt\n\nPreview (first 2KB):\nx"`, false,
		`"toolUseResult":{"stdout":"","persistedOutputPath":"/work/tool-results/b1.txt","persistedOutputSize":41420}`))
	if !reflect.DeepEqual(o.Persisted, &proto.OutcomePersisted{Path: "/work/tool-results/b1.txt", Size: 41420}) {
		t.Fatalf("persisted %+v", o.Persisted)
	}
	o = outcomeOf(t, resultLine(`"<persisted-output>\nOutput too large (40.4KB). Full output saved to: /work/tool-results/b2.txt\n\nPreview (first 2KB):\nx"`, false, ""))
	if !reflect.DeepEqual(o.Persisted, &proto.OutcomePersisted{Path: "/work/tool-results/b2.txt"}) {
		t.Fatalf("persisted by words %+v", o.Persisted)
	}
	o = outcomeOf(t, resultLine(`"fetched"`, false, `"toolUseResult":{"bytes":10,"code":200,"durationMs":1980.6,"url":"https://example.com/"}`))
	if o.DurationMS == nil || *o.DurationMS != 1980 {
		t.Fatalf("duration %v", o.DurationMS)
	}
}

// A tool's whole output is still not copied, now that part of its record is read (TestToolUseResultNotCopied measures
// the allocations): nothing of stdout reaches the outcome.
func TestClaudeOutcomeCopiesNoOutput(t *testing.T) {
	big := strings.Repeat("secret-ish output\n", 1000)
	o := outcomeOf(t, resultLine(`"short"`, false, `"toolUseResult":{"stdout":`+quote(big)+`,"stderr":"","interrupted":false}`))
	if b, _ := json.Marshal(o); strings.Contains(string(b), "secret-ish") {
		t.Fatalf("outcome carries the record's output: %s", b)
	}
}

func TestOutputTail(t *testing.T) {
	if got := outputTail(strings.Repeat("x\n", pageOutputLen/2)); got != "" {
		t.Fatalf("a page carries %d bytes whole, tail %q", pageOutputLen, got)
	}
	var b strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&b, "line %04d\n", i)
	}
	b.WriteString("FAIL: the cause\n\n\n")
	tail := outputTail(b.String())
	lines := strings.Split(tail, "\n")
	if len(lines) != outcomeTailLines || lines[len(lines)-1] != "FAIL: the cause" || lines[0] != "line 1971" {
		t.Fatalf("tail: %d lines, %q … %q", len(lines), lines[0], lines[len(lines)-1])
	}
	// Long lines: at most outcomeTailLen bytes, from a whole line.
	b.Reset()
	for i := range 100 {
		fmt.Fprintf(&b, "%03d %s\n", i, strings.Repeat("y", 300))
	}
	tail = outputTail(b.String())
	if len(tail) > outcomeTailLen || !strings.HasPrefix(tail, "09") || strings.Count(tail, "\n") != 5 {
		t.Fatalf("tail of long lines: %d bytes, %q…", len(tail), tail[:min(len(tail), 10)])
	}
	// Only just over the page: the tail starts where the page's head ends, neither repeating nor skipping a line.
	text := strings.Repeat("head\n", pageOutputLen/5) + "after the head 1\nafter the head 2"
	if tail = outputTail(text); tail != "after the head 1\nafter the head 2" {
		t.Fatalf("tail %q", tail)
	}
	// One long line (minified JSON): the last bytes, from a character.
	one := strings.Repeat("汉", 3000)
	tail = outputTail(one)
	if !utf8.ValidString(tail) || len(tail) > outcomeTailLen || len(tail) < outcomeTailLen-3 {
		t.Fatalf("tail of one line: %d bytes, valid %v", len(tail), utf8.ValidString(tail))
	}
}

// The light copy of a cut result carries the tail: what a page sends.
func TestOutputTailReachesPages(t *testing.T) {
	text := strings.Repeat("compiling\n", 1000) + "error: the cause"
	o := outcomeOf(t, resultLine(quote("Exit code 1\n"+text), true, `"toolUseResult":"Error: Exit code 1"`))
	it := proto.Item{Kind: proto.KindToolResult, Role: "user", Text: text, Outcome: o}
	page, cut := light(it, false)
	if !cut || page.Outcome == nil || !strings.HasSuffix(page.Outcome.Tail, "error: the cause") || strings.Contains(page.Text, "the cause") {
		t.Fatalf("cut %v, tail %q", cut, page.Outcome.Tail)
	}
}

// An output a little over the page loses nothing: the page's head and the tail together are the whole output, wherever
// the head ends (after a whole line, or mid-line when no line ends near the cut).
func TestOutputTailContinuesThePage(t *testing.T) {
	failing := "Exit code 1\n" + strings.Repeat("=== RUN   TestStore/case_with_a_long_name\n", 97) +
		"    store_test.go:88: want 3 rows, got 2 (the cause)\n--- FAIL: TestStore (0.01s)\nFAIL\nFAIL\tpkg/store\t0.012s"
	if at := strings.Index(failing, "store_test.go"); at > pageOutputLen || at+50 < pageOutputLen {
		t.Fatalf("the cause's line starts at %d, not across the cut", at)
	}
	cases := map[string]string{
		"a line across the cut":      failing,
		"one line from before it":    strings.Repeat("a", 3000) + "\n" + strings.Repeat("b", 2000),
		"characters across the cut":  strings.Repeat("汉", 1500) + "\nend",
		"a newline right at the cut": strings.Repeat("x", pageOutputLen) + "\nafter",
		"trailing blank lines":       strings.Repeat("line\n", 830) + "last\n\n\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			page, cut := light(proto.Item{Kind: proto.KindToolResult, Role: "user", Text: text}, false)
			head := page.Text[:max(strings.LastIndex(page.Text, "\n…"), 0)] // the page without its note
			rest := strings.TrimRight(strings.TrimPrefix(text[len(head):], "\n"), " \t\r\n")
			if tail := outputTail(text); !cut || !strings.HasPrefix(text, head) || tail != rest {
				t.Fatalf("page ends %q, tail %q, want %q", head[max(len(head)-20, 0):], tail, rest)
			}
		})
	}
}

func TestCodexOutcome(t *testing.T) {
	enc := quote
	exec := func(fields string) string {
		return `{"timestamp":"2026-10-26T05:06:06.000Z","type":"event_msg","payload":{"type":"item_completed","item":` +
			`{"type":"CommandExecution","id":"e1","command":["/bin/zsh","-lc","go test ./..."],` + fields + `}}}`
	}
	result := func(line string) proto.Item {
		t.Helper()
		items := ParseLine(1, []byte(line), &Meta{Codex: true})
		if len(items) != 2 || items[1].Outcome == nil {
			t.Fatalf("items %+v", items)
		}
		return items[1]
	}
	code := func(n int) *int { return &n }
	ms := func(n int64) *int64 { return &n }
	cases := []struct {
		name, fields string
		want         proto.ToolOutcome
		text         string
	}{
		{"ok", `"status":"completed","aggregated_output":"ok","exit_code":0,"duration":{"secs":1,"nanos":500000000}`,
			proto.ToolOutcome{Status: proto.OutcomeOK, ExitCode: code(0), DurationMS: ms(1500)}, "ok"},
		{"failed", `"status":"failed","aggregated_output":"FAIL","exit_code":2,"duration":{"secs":0,"nanos":7084}`,
			proto.ToolOutcome{Status: proto.OutcomeFailed, ExitCode: code(2), DurationMS: ms(0)}, "FAIL\n(exit code 2)"},
		{"declined", `"status":"declined","aggregated_output":""`, proto.ToolOutcome{Status: proto.OutcomeDenied}, ""},
		{"no duration", `"status":"completed","aggregated_output":"x","exit_code":0`, proto.ToolOutcome{Status: proto.OutcomeOK, ExitCode: code(0)}, "x"},
		{"duration of another shape", `"status":"completed","aggregated_output":"x","exit_code":0,"duration":1500`,
			proto.ToolOutcome{Status: proto.OutcomeOK, ExitCode: code(0)}, "x"},
		{"duration as a string", `"status":"completed","aggregated_output":"x","exit_code":0,"duration":"1.5s"`,
			proto.ToolOutcome{Status: proto.OutcomeOK, ExitCode: code(0)}, "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := result(exec(c.fields))
			if !reflect.DeepEqual(*it.Outcome, c.want) || it.Text != c.text {
				t.Fatalf("outcome %+v text %q, want %+v %q", *it.Outcome, it.Text, c.want, c.text)
			}
		})
	}
	// A long failing build: the exit code stays at the end of the text, and the tail has it.
	out := strings.Repeat("building\n", 1000) + "error: the cause"
	it := result(exec(`"status":"failed","aggregated_output":` + enc(out) + `,"exit_code":1`))
	if !strings.HasSuffix(it.Outcome.Tail, "error: the cause\n(exit code 1)") {
		t.Fatalf("tail %q", it.Outcome.Tail)
	}
	// Other items say ok or failed too.
	mcp := `{"timestamp":"2026-10-26T05:06:06.000Z","type":"event_msg","payload":{"type":"item_completed","item":` +
		`{"type":"McpToolCall","id":"m1","server":"s","tool":"t","arguments":{},"status":"failed","error":{"message":"boom"}}}}`
	if it := result(mcp); it.Outcome.Status != proto.OutcomeFailed {
		t.Fatalf("mcp %+v", it.Outcome)
	}
}

// "! cmd" typed in Claude Code: its output is a Bash result too, failed when it wrote to stderr (no exit code is
// recorded).
func TestBangCommandOutcome(t *testing.T) {
	meta := &Meta{}
	ParseLine(1, []byte(`{"type":"user","uuid":"b1","message":{"role":"user","content":"<bash-input>ls nope</bash-input>"}}`), meta)
	for stderr, status := range map[string]string{"": proto.OutcomeOK, "ls: nope: No such file or directory": proto.OutcomeFailed} {
		out := ParseLine(2, []byte(`{"type":"user","uuid":"b2","message":{"role":"user","content":"<bash-stdout>a</bash-stdout><bash-stderr>`+
			stderr+`</bash-stderr>"}}`), meta)
		if len(out) != 2 || out[1].Outcome == nil || out[1].Outcome.Status != status || out[1].Outcome.ExitCode != nil {
			t.Fatalf("stderr %q: %+v", stderr, out)
		}
	}
}
