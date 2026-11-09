package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// shortTitleWait makes a kept title look for its transcript every few milliseconds, for a second at most.
func shortTitleWait(t *testing.T) {
	t.Helper()
	retry, wait := titleRetry, titleWait
	titleRetry, titleWait = 5*time.Millisecond, time.Second
	t.Cleanup(func() { titleRetry, titleWait = retry, wait })
}

// writeAssistantTranscript writes the transcript Claude starts with its first turn.
func writeAssistantTranscript(t *testing.T, home, id string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-home-me--persona-assistant")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"user","sessionId":"`+id+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// customTitles are the titles the custom-title lines of a transcript give, in order.
func customTitles(t *testing.T, p string) []string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		var l struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal([]byte(line), &l) == nil && l.Type == "custom-title" {
			out = append(out, l.CustomTitle)
		}
	}
	return out
}

func keptTitles(n *Node) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.titles)
}

// The Feishu /agent names its conversation right after starting it, before Claude has written the transcript: the
// title waits for it, the newest of two renames wins, and a rename once the transcript is there is written at once
// (#5866).
func TestRenameRightAfterAnAssistantStart(t *testing.T) {
	shortTitleWait(t)
	n, home := testNode(t)
	const id = "0e000000-0000-4000-8000-0000000000c1"
	n.procs[id] = &assistantProc{id: id, agent: "claude", exited: make(chan struct{})}
	first, second := "first title", "second title"
	for _, title := range []*string{&first, &second} {
		if err := n.renameSession(context.Background(), id, nil, title); err != nil {
			t.Fatalf("rename before the transcript: %v", err)
		}
	}
	p := writeAssistantTranscript(t, home, id)
	waitFor(t, "the kept title", func() bool { return len(customTitles(t, p)) > 0 })
	third := "third title"
	if err := n.renameSession(context.Background(), id, nil, &third); err != nil {
		t.Fatalf("rename once the transcript is there: %v", err)
	}
	if got, want := customTitles(t, p), []string{second, third}; !slices.Equal(got, want) {
		t.Fatalf("custom titles %q, want %q", got, want)
	}
	if k := keptTitles(n); k != 0 {
		t.Fatalf("%d title(s) still kept", k)
	}
}

// A conversation whose process ends before it wrote a transcript drops the title it was given: nothing is written
// into a transcript that appears later.
func TestRenameBeforeATranscriptThatNeverComes(t *testing.T) {
	shortTitleWait(t)
	n, home := testNode(t)
	const id = "0e000000-0000-4000-8000-0000000000c2"
	proc := &assistantProc{id: id, agent: "claude", exited: make(chan struct{})}
	n.procs[id] = proc
	title := "never written"
	if err := n.renameSession(context.Background(), id, nil, &title); err != nil {
		t.Fatalf("rename before the transcript: %v", err)
	}
	close(proc.exited)
	waitFor(t, "the kept title to be dropped", func() bool { return keptTitles(n) == 0 })
	p := writeAssistantTranscript(t, home, id)
	time.Sleep(30 * time.Millisecond)
	if got := customTitles(t, p); len(got) != 0 {
		t.Fatalf("custom titles %q written after the process ended", got)
	}
}

// A session this node neither runs nor has a transcript of is still not found.
func TestRenameUnknownSession(t *testing.T) {
	n, _ := testNode(t)
	title := "anything"
	err := n.renameSession(context.Background(), "0e000000-0000-4000-8000-0000000000c3", nil, &title)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
	if k := keptTitles(n); k != 0 {
		t.Fatalf("%d title(s) kept for an unknown session", k)
	}
}

// Codex keeps its own title: never append Claude's custom-title record to a Codex rollout.
func TestRenameCodexUsesItsAppServer(t *testing.T) {
	n, f, _ := effortPane(t)
	title := "  Review  the build "
	if err := n.renameSession(context.Background(), remoteRaw, nil, &title); err != nil {
		t.Fatal(err)
	}
	got := f.sent("thread/name/set")
	if len(got) != 1 || got[0]["threadId"] != remoteRaw || got[0]["name"] != "Review the build" || len(got[0]) != 2 {
		t.Fatalf("rename params %v", got)
	}
	f.mu.Lock()
	f.reject = "thread/name/set"
	f.mu.Unlock()
	if err := n.renameSession(context.Background(), remoteRaw, nil, &title); err == nil {
		t.Fatal("a refused rename reported success")
	}
}
