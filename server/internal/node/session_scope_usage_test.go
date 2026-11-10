package node

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type scopeCountingReader struct {
	io.Reader
	read int
}

func (r *scopeCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestNodeSessionScopeCwdReadsOnlyBoundedHeader(t *testing.T) {
	for _, header := range []string{
		`{"type":"user","cwd":"/work","message":{"content":"first"}}`,
		`{"type":"session_meta","payload":{"cwd":"/work"}}`,
	} {
		r := &scopeCountingReader{Reader: strings.NewReader(header + "\n" + strings.Repeat("tail", 2<<20))}
		if got := scopeTranscriptCwd(r); got != "/work" {
			t.Fatalf("cwd: %q", got)
		}
		if r.read > 4096 {
			t.Fatalf("cwd check read activity tail: %d bytes", r.read)
		}
	}
	for _, body := range []string{
		strings.Repeat(" ", scopeTranscriptHeaderLimit) + "\n" + `{"cwd":"/work"}`,
		`{"type":"user","cwd":"/work"`,
		`{"type":"event_msg","payload":{"cwd":"/work"}}`,
	} {
		r := &scopeCountingReader{Reader: strings.NewReader(body)}
		if got := scopeTranscriptCwd(r); got != "" {
			t.Fatalf("invalid or out-of-bound metadata accepted: %q", got)
		}
		if r.read > scopeTranscriptHeaderLimit {
			t.Fatalf("header bound exceeded: %d bytes", r.read)
		}
	}
}

func TestNodeSessionScopeUsageRejectsOtherCwdsAndSymlinkEscapes(t *testing.T) {
	n := scopedTestNode(t)
	ownClaude := scopeClaudeTranscript(t, n.sessionScope.claude, "aaaaaaaa-0000-0000-0000-000000000011", n.sessionScope.dir)
	scopeClaudeTranscript(t, n.sessionScope.claude, "aaaaaaaa-0000-0000-0000-000000000012", n.sessionScope.dir+"-other")
	externalClaude := scopeClaudeTranscript(t, filepath.Join(n.home, ".claude"), "aaaaaaaa-0000-0000-0000-000000000013", n.sessionScope.dir)
	if err := os.Symlink(externalClaude, filepath.Join(filepath.Dir(ownClaude), filepath.Base(externalClaude))); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	const ownID = "bbbbbbbb-0000-0000-0000-000000000011"
	const otherID = "bbbbbbbb-0000-0000-0000-000000000012"
	const externalID = "bbbbbbbb-0000-0000-0000-000000000013"
	fixture := filepath.Join(n.dir, "fixture")
	writeRollout(t, fixture, ownID, n.sessionScope.dir, now, now)
	writeRollout(t, fixture, otherID, n.sessionScope.dir+"-other", now, now)
	if err := os.Rename(filepath.Join(fixture, ".codex", "sessions"), filepath.Join(n.sessionScope.codex, "sessions")); err != nil {
		t.Fatal(err)
	}
	ownCodex := rolloutPath(n.sessionScope.codex, ownID)
	writeRollout(t, n.home, externalID, n.sessionScope.dir, now, now)
	externalCodex := rolloutPath(filepath.Join(n.home, ".codex"), externalID)
	if err := os.Symlink(externalCodex, filepath.Join(filepath.Dir(ownCodex), filepath.Base(externalCodex))); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range n.usageFiles(now.Add(-time.Hour)) {
		got = append(got, f.path)
	}
	want := []string{ownClaude, ownCodex}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage transcripts escaped scope: got %v, want %v", got, want)
	}
}
