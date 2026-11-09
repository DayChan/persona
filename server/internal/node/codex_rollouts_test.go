package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rolloutID reads the same id as rolloutIDRe, for any name.
func TestRolloutID(t *testing.T) {
	for _, p := range []string{
		"/h/.codex/sessions/2026/01/02/rollout-2026-01-02T03-04-05-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl",
		"/h/.codex/archived_sessions/rollout-2026-01-02T03-04-05-0199A1B2-C3D4-7E5F-8A9B-0C1D2E3F4A5B.jsonl",
		`C:\Users\u\.codex\sessions\2026\01\02\rollout-2026-01-02T03-04-05-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl`,
		"0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl",
		"x0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl",
		"/h/rollout-2026-01-02T03-04-05-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl.tmp",
		"/h/rollout-2026-01-02T03-04-05-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5.jsonl",
		"/h/rollout-2026-01-02T03-04-05-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5g.jsonl",
		"/h/rollout-2026-01-02T03-04-05-0199a1b2_c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl",
		"/h/rollout-2026-01-02T03-04-05-0199a1b2-c3d4-7e5f-8a9b0-c1d2e3f4a5b.jsonl",
		"/h/rollout-2026-01-02T03-04-05-plain.jsonl",
		".jsonl",
		"",
	} {
		want := ""
		if m := rolloutIDRe.FindStringSubmatch(p); m != nil {
			want = m[1]
		}
		if got := rolloutID(p); got != want {
			t.Errorf("rolloutID(%q) = %q, want %q", p, got, want)
		}
	}
}

func rolloutMetaCount(home string) int {
	rolloutCaches.Lock()
	defer rolloutCaches.Unlock()
	return len(cacheFor(home).metas)
}

// The session_meta cache keeps only rollouts still on disk: once the older days are listed again, deleted threads
// (recent, or older ones resumed lately) leave it, while a rollout outside the window read on demand stays.
func TestRolloutsMetasPruned(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	older := now.AddDate(0, 0, -40)
	const (
		recentKept    = "a1111111-1111-1111-1111-111111111111"
		recentGone    = "a2222222-2222-2222-2222-222222222222"
		resumedKept   = "b1111111-1111-1111-1111-111111111111"
		resumedGone   = "b2222222-2222-2222-2222-222222222222"
		outsideWindow = "c1111111-1111-1111-1111-111111111111"
	)
	writeRollout(t, home, recentKept, "/w", now, now)
	writeRollout(t, home, recentGone, "/w", now, now)
	writeRollout(t, home, resumedKept, "/w", older, now)
	writeRollout(t, home, resumedGone, "/w", older, now)
	writeRollout(t, home, outsideWindow, "/w", older.AddDate(0, 0, -1), older)

	if got := len(rollouts(codex)); got != 4 {
		t.Fatalf("rollouts in the window: %d, want 4", got)
	}
	p := rolloutPath(codex, outsideWindow)
	if p == "" || metaOf(codex, p).cwd != "/w" {
		t.Fatalf("rollout outside the window: %q", p)
	}
	if got := rolloutMetaCount(codex); got != 5 {
		t.Fatalf("metas before: %d, want 5", got)
	}

	for _, id := range []string{recentGone, resumedGone} {
		if err := os.Remove(rolloutPath(codex, id)); err != nil {
			t.Fatal(err)
		}
	}
	expireRollouts(codex, true)
	got := rollouts(codex)
	if len(got) != 2 || got[0].id == got[1].id {
		t.Fatalf("rollouts after deleting two: %+v", got)
	}
	for _, r := range got {
		if r.id != recentKept && r.id != resumedKept {
			t.Fatalf("unexpected rollout %q", r.id)
		}
	}
	// The two kept rollouts and the one outside the window, still on disk.
	if n := rolloutMetaCount(codex); n != 3 {
		t.Fatalf("metas after the older days are listed again: %d, want 3", n)
	}
}
