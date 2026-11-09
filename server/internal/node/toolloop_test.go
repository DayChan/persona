package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestToolLoopIncrementalAndListPush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	n := &Node{paths: map[string]string{"s": path}, metas: map[string]quickMeta{}}
	appendText := func(s string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.WriteString(s); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	lastKey := listKey([]proto.Session{{SessionID: "s"}})
	for i := 0; i < 5; i++ {
		at := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339)
		call := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Read","input":{"file_path":"a"}}]}}`+"\n", at, fmt.Sprint(i))
		result := fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}}`+"\n", at, fmt.Sprint(i))
		appendText(call + result[:len(result)/2])
		if got := n.toolLoopOf("s"); i == 3 && got != nil {
			t.Fatalf("incomplete result counted: %+v", got)
		}
		appendText(result[len(result)/2:])
		got := n.toolLoopOf("s")
		if i < 3 && got != nil {
			t.Fatalf("early candidate: %+v", got)
		}
		if i >= 3 {
			if got == nil || got.Count != i+1 {
				t.Fatalf("candidate %d = %+v", i, got)
			}
			key := listKey([]proto.Session{{SessionID: "s", ToolLoop: got}})
			if key == lastKey {
				t.Fatal("candidate update did not change session list key")
			}
			lastKey = key
		}
	}
	appendText(`{"type":"user","message":{"content":"new message"}}` + "\n")
	if got := n.toolLoopOf("s"); got != nil {
		t.Fatalf("new message did not clear candidate: %+v", got)
	}
	if key := listKey([]proto.Session{{SessionID: "s"}}); key == lastKey {
		t.Fatal("clearing candidate did not change list key")
	}
}

func TestToolLoopPollPush(t *testing.T) {
	n, f, home := herdrNode(t)
	const cwd = "/tmp/tool-loop-poll"
	at := time.Now().UTC().Add(-time.Minute)
	path := writeTranscript(t, home, cwd, sidA, at, `{"type":"user","timestamp":"`+at.Format(time.RFC3339)+`","message":{"role":"user","content":"Check"}}`)
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, "check")}, nil))
	pushes, stop := pollNode(t, n)
	defer stop()
	if s := nextWorkflowPush(t, pushes, 5*time.Second); s.ToolLoop != nil {
		t.Fatalf("unexpected candidate: %+v", s.ToolLoop)
	}
	var body strings.Builder
	for i := 0; i < 4; i++ {
		ts := at.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		fmt.Fprintf(&body, `{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Read","input":{}}]}}`+"\n"+`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}}`+"\n", ts, fmt.Sprint(i), ts, fmt.Sprint(i))
	}
	appendFile(t, path, body.String())
	if s := nextWorkflowPush(t, pushes, 5*time.Second); s.ToolLoop == nil || s.ToolLoop.Count != 4 {
		t.Fatalf("loop was not pushed: %+v", s.ToolLoop)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(original), `"content":"ok"`, `"content":"no"`, 1)
	if len(changed) != len(original) || changed == string(original) {
		t.Fatal("rewrite must change content without changing size")
	}
	rewrite := func(contents []byte, stamp time.Time) {
		t.Helper()
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	rewrite([]byte(changed), time.Now().Add(time.Second))
	if s := nextWorkflowPush(t, pushes, 5*time.Second); s.ToolLoop != nil {
		t.Fatalf("same-size rewrite did not clear loop: %+v", s.ToolLoop)
	}
	rewrite(original, time.Now().Add(2*time.Second))
	if s := nextWorkflowPush(t, pushes, 5*time.Second); s.ToolLoop == nil || s.ToolLoop.Count != 4 {
		t.Fatalf("same-size rewrite did not rebuild loop: %+v", s.ToolLoop)
	}
	appendFile(t, path, `{"type":"user","message":{"content":"new prompt"}}`+"\n")
	if s := nextWorkflowPush(t, pushes, 5*time.Second); s.ToolLoop != nil {
		t.Fatalf("loop was not cleared: %+v", s.ToolLoop)
	}
}

func TestToolLoopHeadlessSession(t *testing.T) {
	n, _, home := herdrNode(t)
	const cwd = "/tmp/loop-headless"
	at := time.Now().UTC().Add(-time.Minute)
	path := writeTranscript(t, home, cwd, sidA, at, `{"type":"user","timestamp":"`+at.Format(time.RFC3339)+`","message":{"role":"user","content":"Check"}}`)
	var body strings.Builder
	for i := 0; i < 4; i++ {
		ts := at.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		fmt.Fprintf(&body, `{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Read","input":{}}]}}`+"\n"+`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}}`+"\n", ts, fmt.Sprint(i), ts, fmt.Sprint(i))
	}
	appendFile(t, path, body.String())
	p := &assistantProc{id: sidA, cwd: cwd, agent: "claude", busy: true}
	n.mu.Lock()
	n.paths[sidA] = path
	n.procs[sidA] = p
	n.mu.Unlock()
	row := func() proto.Session {
		t.Helper()
		for _, s := range n.sessionList(context.Background(), false) {
			if s.SessionID == sidA {
				return s
			}
		}
		t.Fatal("headless session missing")
		return proto.Session{}
	}
	s := row()
	if s.Agent != "" || s.Status != proto.StatusWorking || s.ToolLoop == nil || s.ToolLoop.Count != 4 {
		t.Fatalf("working headless session: agent=%q status=%q loop=%+v", s.Agent, s.Status, s.ToolLoop)
	}
	p.mu.Lock()
	p.busy = false
	p.mu.Unlock()
	if s := row(); s.Status != proto.StatusDone || s.ToolLoop != nil {
		t.Fatalf("finished headless session: status=%q loop=%+v", s.Status, s.ToolLoop)
	}
}

type toolLoopStaleInfo struct {
	os.FileInfo
	size int64
}

func (s toolLoopStaleInfo) Size() int64 { return s.size }

func TestToolLoopRetriesAfterReadFailure(t *testing.T) {
	for _, mode := range []string{"short read", "open failure"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			n := &Node{paths: map[string]string{"s": path}, metas: map[string]quickMeta{}}
			var records [4]string
			for i := range records {
				records[i] = fmt.Sprintf(`{"type":"assistant","timestamp":"2026-01-01T00:00:0%dZ","message":{"content":[{"type":"tool_use","id":"%d","name":"Read","input":{}}]}}`+"\n"+`{"type":"user","timestamp":"2026-01-01T00:00:0%dZ","message":{"content":[{"type":"tool_result","tool_use_id":"%d","content":"ok"}]}}`+"\n", i, i, i, i)
			}
			appendFile(t, path, strings.Join(records[:3], ""))
			if got := n.toolLoopOf("s"); got != nil {
				t.Fatalf("early candidate: %+v", got)
			}
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate a stale Stat or an open failure without sleeping or a race.
			readPath := path
			if mode == "open failure" {
				readPath += ".missing"
			}
			m := readToolLoop(readPath, toolLoopStaleInfo{FileInfo: st, size: st.Size() + 100}, n.metas[path])
			n.metas[path] = m
			appendFile(t, path, records[3])
			if got := n.toolLoopOf("s"); got == nil || got.Count != 4 {
				t.Fatalf("read failure lost the preceding calls: %+v", got)
			}
		})
	}
}

func TestToolLoopLargeResultsIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	n := &Node{paths: map[string]string{"s": path}, metas: map[string]quickMeta{}}
	for i := 0; i < 4; i++ {
		at := fmt.Sprintf("2026-01-01T00:00:0%dZ", i)
		call := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Read","input":{}}]}}`+"\n", at, fmt.Sprint(i))
		result := fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`+"\n", at, fmt.Sprint(i), strings.Repeat("x", quickTail+1))
		appendFile(t, path, call+result[:len(result)/2])
		if got := n.toolLoopOf("s"); got != nil {
			t.Fatalf("incomplete result counted: %+v", got)
		}
		appendFile(t, path, result[len(result)/2:])
		got := n.toolLoopOf("s")
		if i == 3 && (got == nil || got.Count != 4) {
			t.Fatalf("large results missed: %+v", got)
		}
	}
}

func TestToolLoopLargeTailAndRewrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	var records strings.Builder
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&records, `{"type":"assistant","timestamp":"2026-01-01T00:00:0%dZ","message":{"content":[{"type":"tool_use","id":"%d","name":"Read","input":{}}]}}`+"\n"+`{"type":"user","timestamp":"2026-01-01T00:00:0%dZ","message":{"content":[{"type":"tool_result","tool_use_id":"%d","content":%q}]}}`+"\n", i, i, i, i, strings.Repeat("x", quickTail+1))
	}
	body := records.String()
	// The bounded tail starts in an unrelated long line, before the four pairs.
	original := strings.Repeat("z", toolLoopReadLimit) + "\n" + body
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	n := &Node{paths: map[string]string{"s": path}, metas: map[string]quickMeta{}}
	if got := n.toolLoopOf("s"); got == nil || got.Count != 4 {
		t.Fatalf("tail missed large results: %+v", got)
	}
	changed := strings.Replace(original, `"content":"xxx`, `"content":"yxx`, 1)
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if got := n.toolLoopOf("s"); got != nil {
		t.Fatalf("same-size rewrite retained candidate: %+v", got)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if got := n.toolLoopOf("s"); got == nil || got.Count != 4 {
		t.Fatalf("truncation did not rebuild candidate: %+v", got)
	}
}

func TestToolLoopSkipsOverlongPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", toolLoopReadLimit+1)), 0600); err != nil {
		t.Fatal(err)
	}
	n := &Node{paths: map[string]string{"s": path}, metas: map[string]quickMeta{}}
	if got := n.toolLoopOf("s"); got != nil {
		t.Fatalf("unexpected candidate: %+v", got)
	}
	// This apparent call is still part of the skipped line and must not count.
	appendFile(t, path, `{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","message":{"content":[{"type":"tool_use","id":"0","name":"Read","input":{}}]}}`+"\n")
	n.toolLoopOf("s")
	var body strings.Builder
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&body, `{"type":"user","timestamp":"2026-01-01T00:00:0%dZ","message":{"content":[{"type":"tool_result","tool_use_id":"%d","content":"ok"}]}}`+"\n", i, i)
		if i < 3 {
			fmt.Fprintf(&body, `{"type":"assistant","timestamp":"2026-01-01T00:00:0%dZ","message":{"content":[{"type":"tool_use","id":"%d","name":"Read","input":{}}]}}`+"\n", i+1, i+1)
		}
	}
	appendFile(t, path, body.String())
	if got := n.toolLoopOf("s"); got != nil {
		t.Fatalf("partial line counted as a call: %+v", got)
	}
	appendFile(t, path, `{"type":"assistant","timestamp":"2026-01-01T00:00:04Z","message":{"content":[{"type":"tool_use","id":"4","name":"Read","input":{}}]}}`+"\n"+`{"type":"user","timestamp":"2026-01-01T00:00:04Z","message":{"content":[{"type":"tool_result","tool_use_id":"4","content":"ok"}]}}`+"\n")
	if got := n.toolLoopOf("s"); got == nil || got.Count != 4 {
		t.Fatalf("did not recover after overlong line: %+v", got)
	}
}

func TestToolLoopLargeGapDoesNotReuseOldCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	n := &Node{paths: map[string]string{"s": path}, metas: map[string]quickMeta{}}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		fmt.Fprintf(f, `{"type":"assistant","timestamp":"2026-01-01T00:00:0%dZ","message":{"content":[{"type":"tool_use","id":"%d","name":"Read","input":{}}]}}`+"\n"+`{"type":"user","timestamp":"2026-01-01T00:00:0%dZ","message":{"content":[{"type":"tool_result","tool_use_id":"%d","content":"ok"}]}}`+"\n", i, i, i, i)
	}
	if got := n.toolLoopOf("s"); got == nil {
		t.Fatal("candidate missing")
	}
	fmt.Fprintln(f, strings.Repeat("x", toolLoopReadLimit+1))
	f.Close()
	if got := n.toolLoopOf("s"); got != nil {
		t.Fatalf("old candidate after large gap: %+v", got)
	}
}
