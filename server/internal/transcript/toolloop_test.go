package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func loopMessage(d *LoopDetector, n int, tool string, input any, result string, at time.Time) {
	call, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": at, "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": fmt.Sprint(n), "name": tool, "input": input}}}})
	out, _ := json.Marshal(map[string]any{"type": "user", "timestamp": at, "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprint(n), "content": result}}}})
	d.Line(call)
	d.Line(out)
}

func TestToolLoopPatterns(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		tools   []string
		results []string
		step    time.Duration
		count   int
	}{
		{"same below threshold", []string{"Read", "Read", "Read"}, nil, time.Second, 0},
		{"same", []string{"Read", "Read", "Read", "Read"}, nil, time.Second, 4},
		{"two-cycle", []string{"Read", "Edit", "Read", "Edit", "Read", "Edit"}, nil, time.Second, 6},
		{"three-cycle", []string{"Read", "Edit", "Grep", "Read", "Edit", "Grep", "Read", "Edit", "Grep"}, nil, time.Second, 9},
		{"different results", []string{"Read", "Read", "Read", "Read"}, []string{"a", "b", "a", "b"}, time.Second, 0},
		{"slow polling", []string{"Read", "Read", "Read", "Read"}, nil, 30 * time.Second, 0},
		{"wait tool", []string{"Monitor", "Monitor", "Monitor", "Monitor"}, nil, time.Second, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d LoopDetector
			for i, tool := range tc.tools {
				result := "ok"
				if tc.results != nil {
					result = tc.results[i]
				}
				loopMessage(&d, i, tool, map[string]any{"file_path": "file.go"}, result, start.Add(time.Duration(i)*tc.step))
			}
			got := d.Candidate()
			if tc.count == 0 {
				if got != nil {
					t.Fatalf("unexpected loop: %+v", got)
				}
				return
			}
			if got == nil || got.Count != tc.count || !got.Since.Equal(start) {
				t.Fatalf("candidate = %+v, want count %d since %v", got, tc.count, start)
			}
		})
	}
}

func TestToolLoopCeilingAndReset(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var d LoopDetector
	for i := 0; i < 120; i++ {
		loopMessage(&d, i, "Read", map[string]any{"file_path": "a"}, "same", start.Add(time.Duration(i)*time.Second))
	}
	if got := d.Candidate(); got == nil || got.Count != 120 || !got.Since.Equal(start) {
		t.Fatalf("candidate = %+v", got)
	}
	copy := d.Clone()
	copy.Line([]byte(`{"type":"user","message":{"content":"new prompt"}}`))
	if copy.Candidate() != nil || d.Candidate() == nil {
		t.Fatal("new prompt must reset only its detector")
	}
	d.Line([]byte(`{"type":"system","subtype":"turn_duration"}`))
	if d.Candidate() != nil {
		t.Fatal("turn end must reset")
	}
}

func TestToolLoopPollingAndPrivateData(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, command := range []string{"gh pr checks 42", "gh run view 1", "tail -n 10 server.log", "sleep 1", "while true; do sleep 1; done", "while true; do gh pr checks 42; done", "while true; do tail -n 10 server.log; done", "while true; do gh pr checks 42; sleep 1; done", "gh pr checks 42 && sleep 1", "gh pr checks 42 || sleep 1", `sleep "1"`, `tail -n 10 '/var/log/application log'`, "journalctl -f", "docker logs job", "gh api repos/demo/repo/check-runs"} {
		var d LoopDetector
		for i := 0; i < 5; i++ {
			loopMessage(&d, i, "Bash", map[string]any{"command": command}, "same", start.Add(time.Duration(i)*time.Second))
		}
		if got := d.Candidate(); got != nil {
			t.Fatalf("polling %q: %+v", command, got)
		}
	}
	var d LoopDetector
	for i := 0; i < 4; i++ {
		loopMessage(&d, i, "Read", map[string]any{"file_path": "private-input"}, "private-output", start.Add(time.Duration(i)*time.Second))
	}
	b, _ := json.Marshal(d.Candidate())
	if string(b) != `{"tool":"Read","count":4,"since":"2026-01-01T00:00:00Z"}` {
		t.Fatalf("summary = %s", b)
	}
}

func TestToolLoopOutputPolling(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		tool  string
		input any
	}{
		{"write_stdin", map[string]any{"session_id": 1, "chars": ""}},
		{"functions.write_stdin", map[string]any{"session_id": 1}},
		{"Read", map[string]any{"file_path": "/logs/worker.log", "offset": 100, "limit": 20}},
		{"Bash", map[string]any{"command": "gh api repos/example/project/actions/runs/1"}},
	} {
		t.Run(tc.tool+fmt.Sprint(tc.input), func(t *testing.T) {
			var d LoopDetector
			for i := 0; i < 5; i++ {
				loopMessage(&d, i, tc.tool, tc.input, "same", start.Add(time.Duration(i)*time.Second))
			}
			if got := d.Candidate(); got != nil {
				t.Fatalf("polling produced candidate: %+v", got)
			}
		})
	}
}

func TestToolLoopOrdinaryCallsAreNotPolling(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		tool  string
		input any
	}{
		{"Edit", map[string]any{"file_path": "app.log", "old_string": "wait for it", "new_string": "while true"}},
		{"Write", map[string]any{"file_path": "/var/log/service.log", "content": "wait while processing"}},
		{"Read", map[string]any{"file_path": "/logs/example.log"}},
		{"Read", map[string]any{"file_path": "main.go", "offset": 100, "limit": 20}},
		{"write_stdin", map[string]any{"session_id": 1, "chars": "submit"}},
		{"write_stdin", map[string]any{}},
		{"Bash", map[string]any{"command": "gh api -X POST repos/example/project/actions/runs/1/rerun"}},
		{"Grep", map[string]any{"pattern": "sleep while wait", "path": "server.log"}},
		{"Bash", map[string]any{"command": "echo wait while sleep app.log"}},
		{"Bash", map[string]any{"command": "tail -n 10 main.go"}},
		{"Bash", map[string]any{"command": "go test ./...", "description": "wait for CI and inspect server.log"}},
		{"Bash", map[string]any{"command": "deploy.sh; sleep 1"}},
		{"Bash", map[string]any{"command": "while true; do deploy.sh; sleep 1; done"}},
		{"Bash", map[string]any{"command": "tail -n 10 server.log; deploy.sh"}},
		{"Bash", map[string]any{"command": "gh pr checks 42 && deploy.sh"}},
		{"Bash", map[string]any{"command": "sleep 1 > output.txt"}},
		{"Bash", map[string]any{"command": "sleep $(deploy.sh)"}},
		{"Bash", map[string]any{"command": `echo 'not a command; sleep 1'`}},
		{"Bash", map[string]any{"command": `sleep "$(deploy.sh)"`}},
	} {
		t.Run(tc.tool+fmt.Sprint(tc.input), func(t *testing.T) {
			var d LoopDetector
			for i := 0; i < 4; i++ {
				loopMessage(&d, i, tc.tool, tc.input, "same", start.Add(time.Duration(i)*time.Second))
			}
			if got := d.Candidate(); got == nil || got.Count != 4 {
				t.Fatalf("ordinary call classified as polling: %+v", got)
			}
		})
	}
}

func TestToolLoopChangedInputAndQueuedPrompt(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var d LoopDetector
	for i := 0; i < 4; i++ {
		loopMessage(&d, i, "Read", map[string]any{"path": "one"}, "ok", start.Add(time.Duration(i)*time.Second))
	}
	if d.Candidate() == nil {
		t.Fatal("candidate missing")
	}
	line := `{"type":"assistant","timestamp":"2026-01-01T00:00:04Z","message":{"content":[{"type":"tool_use","id":"next","name":"Read","input":{"path":"two"}}]}}`
	d.Line([]byte(line))
	if d.Candidate() != nil {
		t.Fatal("a different call must clear the previous candidate immediately")
	}
	d.Line([]byte(`{"type":"user","timestamp":"2026-01-01T00:00:04Z","message":{"content":[{"type":"tool_result","tool_use_id":"next","content":"ok"}]}}`))
	for i := 5; i < 9; i++ {
		loopMessage(&d, i, "Read", map[string]any{"path": "one"}, "ok", start.Add(time.Duration(i)*time.Second))
	}
	if d.Candidate() == nil {
		t.Fatal("new candidate missing")
	}
	d.Line([]byte(`{"type":"attachment","attachment":{"type":"queued_command","commandMode":"prompt","origin":{"kind":"human"}}}`))
	if d.Candidate() != nil || len(d.pending) != 0 {
		t.Fatal("queued user message must reset detection")
	}
	if loopHash([]byte(`{"number":9007199254740992}`)) == loopHash([]byte(`{"number":9007199254740993}`)) {
		t.Fatal("large JSON numbers lost precision")
	}
}

func TestToolLoopConcurrentResultsAndPolling(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		gap  time.Duration
		want bool
	}{{"fast out of order", time.Second, true}, {"slow results", 30 * time.Second, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var d LoopDetector
			for i := 0; i < 4; i++ {
				line, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": start.Add(time.Duration(i) * time.Second), "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": fmt.Sprint(i), "name": "Read", "input": map[string]any{"path": "a"}}}}})
				d.Line(line)
			}
			for i := 3; i >= 0; i-- {
				line, _ := json.Marshal(map[string]any{"type": "user", "timestamp": start.Add(4*time.Second + time.Duration(3-i)*tc.gap), "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprint(i), "content": "same"}}}})
				d.Line(line)
			}
			if got := d.Candidate(); (got != nil) != tc.want {
				t.Fatalf("candidate = %+v, want present %v", got, tc.want)
			}
		})
	}
}

func TestToolLoopMissingResultTimeIsNotEvidence(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var d LoopDetector
	for i := 0; i < 4; i++ {
		call, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": start.Add(time.Duration(i) * time.Second), "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": fmt.Sprint(i), "name": "Read", "input": map[string]any{}}}}})
		result, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprint(i), "content": "ok"}}}})
		d.Line(call)
		d.Line(result)
	}
	if got := d.Candidate(); got != nil {
		t.Fatalf("missing result time: %+v", got)
	}
}

func TestToolLoopRotationSinceStaysStable(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, names := range [][]string{{"Read", "Grep"}, {"Read", "Grep", "Edit"}} {
		var d LoopDetector
		for i := 0; i < 120; i++ {
			loopMessage(&d, i, names[i%len(names)], map[string]any{"path": "a"}, "ok", start.Add(time.Duration(i)*time.Second))
			if i+1 >= 3*len(names) {
				if got := d.Candidate(); got == nil || got.Count != i+1 || got.Tool != names[0] || !got.Since.Equal(start) {
					t.Fatalf("rotation %v step %d: %+v", names, i, got)
				}
			}
		}
	}
}

func TestToolLoopHashesWholeInputAndOutput(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, diff := range []string{"input", "output"} {
		t.Run(diff, func(t *testing.T) {
			var d LoopDetector
			for i := 0; i < 4; i++ {
				input := strings.Repeat("a", maxInputLen+100)
				output := strings.Repeat("b", maxTextLen+100)
				if i%2 == 1 {
					if diff == "input" {
						input += "x"
					} else {
						output += "x"
					}
				}
				loopMessage(&d, i, "Read", map[string]any{"path": input}, output, start.Add(time.Duration(i)*time.Second))
			}
			if got := d.Candidate(); got != nil {
				t.Fatalf("different content after clipping boundary matched: %+v", got)
			}
		})
	}
}

func TestToolLoopResponseItems(t *testing.T) {
	var d LoopDetector
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		call, _ := json.Marshal(map[string]any{"type": "response_item", "timestamp": at, "payload": map[string]any{"type": "function_call", "name": "exec_command", "call_id": fmt.Sprint(i), "arguments": `{"cmd":"go test"}`}})
		out, _ := json.Marshal(map[string]any{"type": "response_item", "timestamp": at, "payload": map[string]any{"type": "function_call_output", "call_id": fmt.Sprint(i), "output": []any{map[string]any{"type": "input_text", "text": "same"}}}})
		d.Line(call)
		d.Line(out)
	}
	if got := d.Candidate(); got == nil || got.Count != 4 {
		t.Fatalf("candidate = %+v", got)
	}
	d.Line([]byte(`{"type":"event_msg","payload":{"type":"task_started"}}`))
	if d.Candidate() != nil {
		t.Fatal("new turn must reset")
	}
}
