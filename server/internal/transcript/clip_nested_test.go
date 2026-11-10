package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// clipSlack is what a clipped input may carry over maxInputLen: JSON escapes in the kept text.
const clipSlack = 2048

// pushFilesInput is an MCP push_files call that writes two 500 KB files, both inside files[].
func pushFilesInput() string {
	file := strings.Repeat("func main() { fmt.Println(\"hello\") }\n", 500_000/38)
	in, _ := json.Marshal(map[string]any{
		"owner": "someone", "repo": "project", "branch": "main", "message": "Add two files",
		"files": []map[string]string{{"path": "a/one.go", "content": file}, {"path": "b/two.go", "content": file}},
	})
	return string(in)
}

// toolUses runs Claude or Codex lines through ParseLine and returns their tool calls as a page carries them.
func toolUses(t *testing.T, meta *Meta, lines ...string) []proto.Item {
	t.Helper()
	var uses []proto.Item
	for i, l := range lines {
		for _, it := range ParseLine(i+1, []byte(l), meta) {
			if it.Kind == proto.KindToolUse {
				paged, _ := light(it, false)
				uses = append(uses, paged)
			}
		}
	}
	return uses
}

// clippedInput checks a clipped tool input's size and decodes it.
func clippedInput(t *testing.T, it proto.Item) map[string]any {
	t.Helper()
	if len(it.ToolInput) > maxInputLen+clipSlack {
		t.Errorf("%s input is %d bytes, want about %d", it.ToolName, len(it.ToolInput), maxInputLen)
	}
	var in map[string]any
	if err := json.Unmarshal(it.ToolInput, &in); err != nil {
		t.Fatalf("%s input is not JSON: %v", it.ToolName, err)
	}
	return in
}

// Strings deep inside a tool input are clipped too: before, only top-level ones were, and push_files' files went out
// whole (about 1 MB) in every page and push.
func TestNestedToolInputClipped(t *testing.T) {
	args := pushFilesInput()
	claude := toolUses(t, &Meta{}, `{"type":"assistant","uuid":"a1","message":{"id":"m1","role":"assistant","content":[`+
		`{"type":"tool_use","id":"toolu_1","name":"mcp__github__push_files","input":`+args+`}]}}`)
	codex := toolUses(t, &Meta{Codex: true}, `{"timestamp":"2026-10-26T05:06:05.000Z","type":"event_msg","payload":{"type":"item_completed",`+
		`"item":{"type":"McpToolCall","id":"c1","server":"github","tool":"push_files","arguments":`+args+`,`+
		`"status":"completed","result":{"content":[{"type":"text","text":"ok"}]}}}}`)
	if len(claude) != 1 || len(codex) != 1 {
		t.Fatalf("want one tool call each, got %d and %d", len(claude), len(codex))
	}
	for _, u := range []proto.Item{claude[0], codex[0]} {
		in := clippedInput(t, u)
		files, _ := in["files"].([]any)
		if len(files) != 2 || in["message"] != "Add two files" {
			t.Fatalf("%s lost its shape: %v", u.ToolName, in["message"])
		}
		for i, want := range []string{"a/one.go", "b/two.go"} {
			f, _ := files[i].(map[string]any)
			content, _ := f["content"].(string)
			if f["path"] != want || !strings.HasPrefix(content, "func main()") || !strings.Contains(content, "已截断") {
				t.Errorf("%s files[%d]: path %v, content %d bytes ending %q", u.ToolName, i, f["path"], len(content), content[max(len(content)-40, 0):])
			}
		}
	}
}

// A MultiEdit keeps its edits array the apps draw: the big edit is cut, the small one and the flags stay whole.
func TestMultiEditInputClipped(t *testing.T) {
	big := strings.Repeat("汉字 line\n", 4_000) // 48 KB
	in, _ := json.Marshal(map[string]any{"file_path": "/r/x.go", "edits": []map[string]any{
		{"old_string": big, "new_string": big + "更多"},
		{"old_string": "a := 1", "new_string": "a := 2", "replace_all": true},
	}})
	uses := toolUses(t, &Meta{}, `{"type":"assistant","uuid":"a1","message":{"id":"m1","role":"assistant","content":[`+
		`{"type":"tool_use","id":"toolu_1","name":"MultiEdit","input":`+string(in)+`}]}}`)
	if len(uses) != 1 {
		t.Fatalf("want one tool call, got %d", len(uses))
	}
	out := clippedInput(t, uses[0])
	edits, _ := out["edits"].([]any)
	if out["file_path"] != "/r/x.go" || len(edits) != 2 {
		t.Fatalf("MultiEdit lost its shape: %v", out["file_path"])
	}
	first, _ := edits[0].(map[string]any)
	for _, k := range []string{"old_string", "new_string"} {
		if s, _ := first[k].(string); !strings.HasPrefix(s, "汉字 line") || !strings.Contains(s, "已截断") || strings.ContainsRune(s, '�') {
			t.Errorf("edits[0].%s is %d bytes, not cut cleanly", k, len(s))
		}
	}
	if second, _ := edits[1].(map[string]any); second["new_string"] != "a := 2" || second["replace_all"] != true {
		t.Errorf("edits[1] = %v", second)
	}
}

// Many medium strings, none long enough for the first pass, share the budget evenly; short ones stay whole.
func TestManyMediumStringsShareTheBudget(t *testing.T) {
	blocks := make([]map[string]string, 40)
	for i := range blocks {
		blocks[i] = map[string]string{"type": "paragraph", "text": strings.Repeat("Some notes on the plan. ", 125)} // 3 KB
	}
	in, _ := json.Marshal(map[string]any{"page_id": "p1", "children": blocks})
	out := clipJSON(in)
	if len(in) < 100_000 || len(out) > maxInputLen+clipSlack {
		t.Fatalf("input %d bytes, clipped %d, want about %d", len(in), len(out), maxInputLen)
	}
	var got struct {
		PageID   string              `json:"page_id"`
		Children []map[string]string `json:"children"`
	}
	if err := json.Unmarshal(out, &got); err != nil || got.PageID != "p1" || len(got.Children) != 40 {
		t.Fatalf("lost its shape: %v %+v", err, got.PageID)
	}
	for i, b := range got.Children {
		if b["type"] != "paragraph" || len(b["text"]) < minInputShare || !strings.Contains(b["text"], "已截断") {
			t.Fatalf("children[%d] = %q, %d bytes of text", i, b["type"], len(b["text"]))
		}
	}
	// A value that is not an object is left as it is.
	arr := json.RawMessage(`["` + strings.Repeat("x", maxInputLen*2) + `"]`)
	if string(clipJSON(arr)) != string(arr) {
		t.Error("a top-level array was changed")
	}
}

// A string only a little over the share stays whole: cut, with its note, it would be longer than it was.
func TestStringsJustOverTheShareStayWhole(t *testing.T) {
	items := make([]map[string]string, 200)
	for i := range items {
		items[i] = map[string]string{"text": strings.Repeat("y", minInputShare+30)}
	}
	in, _ := json.Marshal(map[string]any{"items": items})
	out := clipJSON(in)
	if len(out) > len(in) {
		t.Errorf("clipping made the input longer: %d bytes from %d", len(out), len(in))
	}
	var got struct {
		Items []map[string]string `json:"items"`
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Items) != len(items) {
		t.Fatalf("lost its shape: %v", err)
	}
	for i, it := range got.Items {
		if it["text"] != items[i]["text"] {
			t.Fatalf("items[%d] was cut to %d bytes from %d", i, len(it["text"]), len(items[i]["text"]))
		}
	}
}

// Whether a cut pays is decided by the JSON it leaves, not by raw bytes: json.Marshal writes < > & as six bytes each, so the
// tail of an HTML string a little over the share costs more than the note that replaces it, and that string is cut.
func TestEscapedStringsJustOverTheShareAreCut(t *testing.T) {
	html := strings.Repeat(`<a href="x">&lt;</a>`, 15)[:minInputShare+30] // 286 raw bytes, about 670 as JSON
	items := make([]map[string]string, 200)
	for i := range items {
		items[i] = map[string]string{"text": html}
	}
	in, _ := json.Marshal(map[string]any{"items": items})
	out := clipJSON(in)
	var got struct {
		Items []map[string]string `json:"items"`
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Items) != len(items) {
		t.Fatalf("lost its shape: %v", err)
	}
	for i, it := range got.Items {
		if !strings.HasPrefix(it["text"], html[:minInputShare]) || !strings.Contains(it["text"], "已截断") {
			t.Fatalf("items[%d] = %d bytes, want the first %d bytes and a note", i, len(it["text"]), minInputShare)
		}
	}
	if len(out) >= len(in) {
		t.Errorf("clipped input is %d bytes, not shorter than the %d it came from", len(out), len(in))
	}
}

// BenchmarkClipJSONNested clips the push_files input; bytes/item is what every page and push carries for it.
func BenchmarkClipJSONNested(b *testing.B) {
	raw := json.RawMessage(pushFilesInput())
	var out json.RawMessage
	b.ReportAllocs()
	for b.Loop() {
		out = clipJSON(raw)
	}
	b.ReportMetric(float64(len(raw)), "raw-bytes/item")
	b.ReportMetric(float64(len(out)), "bytes/item")
}

// Near the minimum share, the note may cost more than the removed tail. Measure that decision across many strings.
func BenchmarkClipJSONNearShare(b *testing.B) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"ASCII", strings.Repeat("y", minInputShare+30)},
		{"HTML", strings.Repeat(`<a href="x">&lt;</a>`, 15)[:minInputShare+30]},
	} {
		b.Run(tc.name, func(b *testing.B) {
			items := make([]map[string]string, 20_000)
			for i := range items {
				items[i] = map[string]string{"text": tc.text}
			}
			raw, _ := json.Marshal(map[string]any{"items": items})
			var out json.RawMessage
			b.ReportAllocs()
			for b.Loop() {
				out = clipJSON(raw)
			}
			b.ReportMetric(float64(len(out)), "bytes/item")
		})
	}
}
