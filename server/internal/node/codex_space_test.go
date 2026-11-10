package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise the complete list: an app-server root and its child are discovered
// after terminal sessions, but must still acquire the matching workspace.
func TestCodexSessionListMatchesHerdrSpace(t *testing.T) {
	n := remoteTestNode(t)
	repo := filepath.Join(t.TempDir(), "project")
	writeGitFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	n.layouts = map[string]layout{host.HerdrCore: {spaces: map[string]host.Space{
		"space": {ID: "space", Label: "Project", Cwd: repo, Number: 2},
	}}}
	root, child := "0e000000-0000-4000-8000-000000000091", "0e000000-0000-4000-8000-000000000092"
	home := n.codexHome()
	for _, id := range []string{root, child} {
		payload := map[string]any{"id": id, "originator": "codex-tui", "cwd": repo, "source": "cli"}
		if id == child {
			payload["parent_thread_id"] = root
			payload["cwd"] = filepath.Join(t.TempDir(), "child-worktree")
		}
		data, err := json.Marshal(map[string]any{"type": "session_meta", "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), id+".jsonl")
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		n.registerCodex(home, id, path)
	}
	_, remote := connectRemoteFixture(t, n, home, "fixture", root, child)
	remote.mu.Lock()
	r := remote.threads[root]
	r.cwd = repo
	remote.threads[root] = r
	c := remote.threads[child]
	c.parent = root
	remote.threads[child] = c
	remote.mu.Unlock()
	matched := 0
	for _, s := range n.sessionList(context.Background(), false) {
		if s.SourceThreadID != root && s.SourceThreadID != child {
			continue
		}
		matched++
		if s.SpaceID != "space" || s.SpaceKind != host.HerdrCore || s.SpaceName != "Project" || s.SpaceIndex != 2 {
			t.Errorf("thread %s has space %q (%q, %q); want Herdr space", s.SourceThreadID, s.SpaceID, s.SpaceKind, s.SpaceName)
		}
		if s.PaneID != "" || s.SlotID != "" || s.Capabilities.Terminal {
			t.Errorf("workspace association invented terminal placement: %+v", s)
		}
	}
	if matched != 2 {
		t.Fatalf("got %d fixture threads, want 2", matched)
	}
}

func TestCodexSpaceAssociationBoundaries(t *testing.T) {
	base := t.TempDir()
	repo, other, tree := filepath.Join(base, "project"), filepath.Join(base, "other", "project"), filepath.Join(base, "tree")
	notes := filepath.Join(base, "notes")
	if err := os.MkdirAll(filepath.Join(notes, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	root := proto.Session{SessionID: "root", Agent: "codex", SourceKind: "codex_app_server", Cwd: repo, RepoRoot: repo}
	space := host.Space{ID: "workspace", Label: "Project", Cwd: repo}
	for _, tc := range []struct {
		name   string
		row    proto.Session
		spaces []host.Space
		peers  []proto.Session
		want   string
	}{
		{name: "exact directory", row: root, spaces: []host.Space{space}, want: space.ID},
		{name: "non-git exact directory", row: proto.Session{Agent: "codex", SourceKind: "codex_app_server", Cwd: notes},
			spaces: []host.Space{{ID: "notes", Cwd: notes}}, want: "notes"},
		{name: "non-git subdirectory never guesses", row: proto.Session{Agent: "codex", SourceKind: "codex_app_server", Cwd: filepath.Join(notes, "sub")},
			spaces: []host.Space{{ID: "notes", Cwd: notes}}},
		{name: "git subdirectory shares verified root", row: func() proto.Session { s := root; s.Cwd = filepath.Join(repo, "src"); return s }(), spaces: []host.Space{space}, peers: []proto.Session{
			{SpaceKind: host.HerdrCore, SpaceID: space.ID, PaneID: "pane", Cwd: repo, RepoRoot: repo},
		}, want: space.ID},
		{name: "same name different path", row: root, spaces: []host.Space{{ID: "other", Cwd: other}}},
		{name: "ambiguous directory", row: root, spaces: []host.Space{space, {ID: "second", Cwd: repo}}},
		{name: "explicit placement", row: func() proto.Session { s := root; s.SpaceID = "explicit"; return s }(), spaces: []host.Space{space}, want: "explicit"},
		{name: "explicit pane without space", row: func() proto.Session { s := root; s.PaneID = "pane"; return s }(), spaces: []host.Space{space}},
		{name: "orphan child never guesses", row: func() proto.Session { s := root; s.ParentSessionID = "missing"; return s }(), spaces: []host.Space{space}},
		{name: "assistant never guesses", row: func() proto.Session { s := root; s.Assistant = true; return s }(), spaces: []host.Space{space}},
		{name: "unrelated runtime", row: func() proto.Session { s := root; s.SourceKind = "runtime"; return s }(), spaces: []host.Space{space}},
		{name: "synthetic directory space", row: root, spaces: []host.Space{{ID: dirSpace + repo, Cwd: repo}}},
		{name: "unique repository across worktrees", row: func() proto.Session { s := root; s.Cwd = tree; return s }(), spaces: []host.Space{space}, peers: []proto.Session{
			{SpaceKind: host.HerdrCore, SpaceID: space.ID, PaneID: "pane", Cwd: repo, RepoRoot: repo},
		}, want: space.ID},
		{name: "ambiguous repository", row: func() proto.Session { s := root; s.Cwd = tree; return s }(), spaces: []host.Space{space, {ID: "second", Cwd: other}}, peers: []proto.Session{
			{SpaceKind: host.HerdrCore, SpaceID: space.ID, PaneID: "pane", Cwd: repo, RepoRoot: repo},
			{SpaceKind: host.HerdrCore, SpaceID: "second", PaneID: "other-pane", Cwd: other, RepoRoot: repo},
		}},
		{name: "worktree directory beats shared repository", row: root, spaces: []host.Space{space, {ID: "second", Cwd: tree}}, peers: []proto.Session{
			{SpaceKind: host.HerdrCore, SpaceID: "second", PaneID: "other-pane", Cwd: tree, RepoRoot: repo},
		}, want: space.ID},
		{name: "terminal current directory", row: root, spaces: []host.Space{{ID: space.ID, Cwd: other}}, peers: []proto.Session{
			{SpaceKind: host.HerdrCore, SpaceID: space.ID, PaneID: "pane", Cwd: repo, RepoRoot: repo},
		}, want: space.ID},
		{name: "no path", row: proto.Session{Agent: "codex", SourceKind: "codex_history"}, spaces: []host.Space{space}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := testNode(t)
			spaces := map[string]host.Space{}
			for _, s := range tc.spaces {
				spaces[s.ID] = s
			}
			n.layouts = map[string]layout{host.HerdrCore: {spaces: spaces}}
			for range 10 {
				rows := append([]proto.Session{tc.row}, tc.peers...)
				n.placeUnassignedCodex(rows)
				if rows[0].SpaceID != tc.want {
					t.Fatalf("space=%q, want %q", rows[0].SpaceID, tc.want)
				}
				if rows[0].PaneID != tc.row.PaneID || rows[0].Capabilities != tc.row.Capabilities || rows[0].WorkspaceID != tc.row.WorkspaceID {
					t.Fatal("association changed terminal routing")
				}
			}
		})
	}
}
