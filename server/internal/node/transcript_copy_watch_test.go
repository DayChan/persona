package node

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A session whose transcript was copied to a new project directory when its working directory moved, and that goes on
// in the copy while the old one stays on disk (#1423): a node that opened the old copy (a hook named it, before the
// move) and only streams it, with no page read, follows the copy the session list reads, so the list's rev and the
// items it pushes come from the same file.
func TestAWatchedSessionFollowsItsNewestCopy(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n, home := testNode(t)
	const id = "cccccccc-0000-0000-0000-000000001423"
	turn := func(cwd, uuid, text string) string {
		return `{"type":"user","uuid":"` + uuid + `u","timestamp":"2026-01-02T03:04:05Z","cwd":"` + cwd + `","message":{"role":"user","content":"` + text + `"}}` + "\n" +
			`{"type":"assistant","uuid":"` + uuid + `a","timestamp":"2026-01-02T03:04:06Z","cwd":"` + cwd + `","message":{"role":"assistant","content":[{"type":"text","text":"` + text + ` done"}]}}` + "\n"
	}
	projects := filepath.Join(home, ".claude", "projects")
	old := filepath.Join(projects, "-home-user--oldname-assistant", id+".jsonl")
	cur := filepath.Join(projects, "-home-user--persona-assistant", id+".jsonl")
	for _, p := range []string{old, cur} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	before := turn("/home/user/.oldname/assistant", "1", "Plan a weekend trip")
	if err := os.WriteFile(old, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	n.rememberTranscript(hookInput{SessionID: id, TranscriptPath: old})
	hub := withHub(t, n)
	if err := n.watch(id, 0); err != nil {
		t.Fatal(err)
	}
	defer n.unwatch(id)

	// Moved: the copy goes on in the new directory, the old one stays as it was.
	if err := os.WriteFile(cur, []byte(before+turn("/home/user/.persona/assistant", "2", "Book the museum")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	n.pathsAt[id] = time.Now().Add(-time.Minute) // the hook's word is old: the node looks again
	n.mu.Unlock()

	waitFor(t, "the new copy's reply", func() bool {
		return slices.ContainsFunc(hub.got(), func(it proto.Item) bool { return it.Text == "Book the museum done" })
	})
	listed := n.transcriptPath(id) // what the session list's rev and updated_at read
	n.mu.Lock()
	streamed := n.files[id].Where()
	n.mu.Unlock()
	if listed != cur || streamed != cur {
		t.Fatalf("the list reads %s, the stream %s; want both %s", listed, streamed, cur)
	}
}
