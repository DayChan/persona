package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// Agent credentials are refused in every Claude Code config directory and Codex home the node knows, whether asked
// directly or through a link, while the rest of those directories stays readable (#1959).
func TestPathGuardAgentCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n := &Node{home: home, cdirs: newClaudeDirs(), codices: newCodexRegistry()}
	n.cdirs.known = []string{"~/.claude-work"}
	n.cdirs.found = []string{filepath.Join(home, "profiles/claude-team")}
	n.codices.config = proto.CodexConfig{Homes: []string{"~/codex-work"}}
	write := func(rel string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"token":"fixture-secret"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	refused := []string{
		".claude/.credentials.json",
		".claude/settings.json",
		".claude/settings.local.json",
		".claude-work/.credentials.json",
		".claude-work/settings.json",
		"profiles/claude-team/.credentials.json",
		".codex/auth.json",
		"codex-work/auth.json",
	}
	readable := []string{
		".claude/projects/p/s.jsonl",
		".claude-work/projects/p/s.jsonl",
		"codex-work/sessions/2026/rollout.jsonl",
		"code/repo/settings.json", // the same name outside an agent directory
		"code/repo/auth.json",
	}
	for _, rel := range append(append([]string{}, refused...), readable...) {
		write(rel)
	}
	if err := os.Symlink(filepath.Join(home, ".claude-work"), filepath.Join(home, "work-link")); err != nil {
		t.Skip("symlinks:", err)
	}
	for _, rel := range append(refused, "work-link/.credentials.json", "work-link/settings.json") {
		if _, err := n.readFile("~/"+rel, "", 0, false); !errors.Is(err, errOffLimits) {
			t.Errorf("read %s: %v, want off limits", rel, err)
		}
		if _, _, _, err := n.openRegular("~/"+rel, ""); !errors.Is(err, errOffLimits) {
			t.Errorf("download %s: %v, want off limits", rel, err)
		}
	}
	for _, rel := range readable {
		if fc, err := n.readFile("~/"+rel, "", 0, false); err != nil || fc.Text == "" {
			t.Errorf("read %s: %+v %v, want its text", rel, fc, err)
		}
	}
	stat := n.statPaths([]string{"~/.claude-work/.credentials.json", "~/.claude-work/projects/p/s.jsonl"}, "")
	if stat[0].Kind != "missing" || stat[1].Kind != "file" {
		t.Errorf("stat: %+v", stat)
	}
}
