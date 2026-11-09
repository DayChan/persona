package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

func workspaceList(t *testing.T, n *Node) []proto.Workspace {
	t.Helper()
	raw, err := n.handle(context.Background(), "workspaces.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	return raw.(proto.WorkspacesResponse).Workspaces
}

func TestWorkspacesListHerdrEmptyAndShellSpaces(t *testing.T) {
	n, f, _ := herdrNode(t)
	f.reply("session.snapshot", snapshot([]any{
		map[string]any{"workspace_id": "empty", "label": "project"},
		map[string]any{"workspace_id": "shell", "label": "project"},
		map[string]any{"workspace_id": "root", "cwd": "/tmp/root", "branch": "feature"},
		map[string]any{"workspace_id": "mixed"},
	}, nil, nil, []any{
		map[string]any{"pane_id": "p1", "workspace_id": "shell", "cwd": "/tmp/shell"},
		map[string]any{"pane_id": "p2", "workspace_id": "root", "cwd": "/tmp/root/child"},
		map[string]any{"pane_id": "p3", "workspace_id": "mixed", "cwd": "/tmp/one"},
		map[string]any{"pane_id": "p4", "workspace_id": "mixed", "cwd": "/tmp/two"},
	}))
	w := workspaceList(t, n)
	if len(w) != 4 || w[0].Cwd != "" || w[1].Cwd != "/tmp/shell" || w[2].Cwd != "/tmp/root" || w[2].Branch != "feature" || w[3].Cwd != "" {
		t.Fatalf("workspaces = %+v", w)
	}
	if w[0].Title != w[1].Title || w[0].SourceID == w[1].SourceID || w[0].Source != host.Herdr || !w[0].Online || !w[0].Available {
		t.Fatalf("workspace identity = %+v", w)
	}
	assertTrace(t, f.trace(), []string{"session.snapshot {}"})
}

func TestWorkspacesListOrcaKeepsWorktreesWithoutTerminals(t *testing.T) {
	n, f, o, procs, _ := orcaNode(t)
	o.SetWorktrees(
		orcarttest.Worktree("empty", "/tmp/empty-tree", "refs/heads/feature-empty"),
		orcarttest.Worktree("unknown-path", "", ""),
	)
	w := workspaceList(t, n)
	if len(w) != 2 || w[0].SourceID != "empty" || w[0].Cwd != "/tmp/empty-tree" || w[0].Branch != "feature-empty" || w[1].Cwd != "" {
		t.Fatalf("workspaces = %+v", w)
	}
	if procs.scans != 0 || len(o.Trace("terminal.list", "terminal.send", "terminal.close")) != 0 || len(f.trace()) != 0 {
		t.Fatal("workspace catalog inspected processes or touched terminals")
	}
	// A catalog error cannot be mistaken for an empty, successful refresh.
	o.Handle("worktree.ps", func(map[string]any) any { return orcarttest.Fail{Code: "method_not_found"} })
	if _, err := n.handle(context.Background(), "workspaces.list", nil); err == nil || !strings.Contains(err.Error(), "orca") {
		t.Fatalf("unsupported catalog = %v", err)
	}
}

func TestWorkspacesCatalogPreservesOrcaHierarchyWithoutAgents(t *testing.T) {
	n, _, o, procs, _ := orcaNode(t)
	root := orcarttest.Worktree("root", "/tmp/root", "")
	root["displayName"] = "Workspace collection"
	child := orcarttest.Worktree("child", "/tmp/elsewhere", "refs/heads/feature")
	child["parentWorktreeId"] = "root"
	grandchild := orcarttest.Worktree("grandchild", "/tmp/third", "")
	grandchild["parentWorktreeId"] = "child"
	o.SetWorktrees(root, child, grandchild)
	w := workspaceList(t, n)
	if len(w) != 3 || w[0].Title != "Workspace collection" || w[0].ParentSourceID != "" ||
		w[1].ParentSourceID != "root" || w[2].ParentSourceID != "child" || w[2].Index != 3 {
		t.Fatalf("hierarchy = %+v", w)
	}
	if procs.scans != 0 || len(o.Trace("terminal.list", "terminal.send", "terminal.close")) != 0 {
		t.Fatal("reading workspace containers inspected or changed agents")
	}
}

func TestWorkspacesCatalogKeepsProjectSeparateFromWorkspaceParents(t *testing.T) {
	n, _, o, procs, _ := orcaNode(t)
	main := orcarttest.Worktree("app::/code/app", "/code/app", "")
	main["repoId"], main["repo"], main["isMainWorktree"] = "app", "App", true
	main["unread"], main["sidebarStatus"], main["workspaceKind"] = true, "working", "git"
	o.Handle("worktree.list", func(map[string]any) any {
		return map[string]any{"worktrees": []any{map[string]any{"id": "app::/code/app", "head": "abcdef1234567890"}}}
	})
	a := orcarttest.Worktree("app::/external/a", "/external/a", "a")
	a["repoId"], a["repo"], a["isMainWorktree"] = "app", "App", false
	b := orcarttest.Worktree("app::/external/b", "/external/b", "b")
	b["repo"], b["isMainWorktree"], b["parentWorktreeId"] = "App", false, "foreign::/folder"
	o.SetWorktrees(main, a, b)
	ws := workspaceList(t, n)
	if len(ws) != 3 {
		t.Fatalf("workspaces = %+v", ws)
	}
	for _, w := range ws {
		if w.ProjectID != "app" || w.ProjectName != "App" || w.ProjectPath != "/code/app" || w.IsMain == nil {
			t.Fatalf("project metadata = %+v", w)
		}
	}
	if !*ws[0].IsMain || *ws[1].IsMain || ws[1].ParentSourceID != "" || ws[2].ParentSourceID != "foreign::/folder" {
		t.Fatalf("project changed workspace lineage: %+v", ws)
	}
	if ws[0].Unread == nil || !*ws[0].Unread || ws[0].SidebarStatus != "working" || ws[0].WorkspaceKind != "git" || ws[0].Head != "abcdef1234567890" {
		t.Fatal("workspace presentation metadata was lost")
	}
	if procs.scans != 0 || len(o.Trace("terminal.list", "terminal.send", "terminal.close")) != 0 {
		t.Fatal("project inventory touched agent processes or terminals")
	}
}

func TestWorkspacesListHerdrCoreEmptySpaces(t *testing.T) {
	n, f, hc, _ := hcNode(t)
	hc.doc = map[string]any{"spaces": []any{
		map[string]any{"id": "empty", "name": "project", "folder": "/tmp/empty-space"},
		map[string]any{"id": "unknown", "name": "project"},
	}}
	w := workspaceList(t, n)
	if len(w) != 2 || w[0].Cwd != "/tmp/empty-space" || w[1].Cwd != "" || w[0].Source != host.HerdrCore {
		t.Fatalf("workspaces = %+v", w)
	}
	if len(f.trace()) != 0 {
		t.Fatalf("classic herdr called: %v", f.trace())
	}
	hc.handle("doc.get", func(map[string]any) any { return hcFail{Kind: "unavailable"} })
	if _, err := n.handle(context.Background(), "workspaces.list", nil); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("failed document = %v", err)
	}
}

type workspaceHost struct {
	bareHost
	spaces []host.Space
	err    error
}

func (h *workspaceHost) Snapshot(context.Context) (*host.Layout, error) {
	return &host.Layout{Spaces: h.spaces}, h.err
}

func TestWorkspacesListKeepsSameNamedSourcesAndReportsFailure(t *testing.T) {
	a := &workspaceHost{bareHost: bareHost{kind: host.Orca}, spaces: []host.Space{{ID: "same", Label: "project"}}}
	b := &workspaceHost{bareHost: bareHost{kind: host.HerdrCore}, spaces: []host.Space{{ID: "same", Label: "project"}}}
	n := &Node{hosts: host.NewRegistry(a, b), state: newState()}
	w := workspaceList(t, n)
	if len(w) != 2 || w[0].Source == w[1].Source || w[0].SourceID != w[1].SourceID || w[0].Cwd != "" {
		t.Fatalf("workspaces = %+v", w)
	}
	b.err = errors.New("doc.get: unavailable")
	partial, err := n.listWorkspaces(context.Background())
	if err != nil || len(partial.Workspaces) != 1 || partial.Workspaces[0].Source != host.Orca ||
		len(partial.Errors) != 1 || partial.Errors[0].Source != host.HerdrCore || partial.Errors[0].Error != b.err.Error() {
		t.Fatalf("partial catalog = %+v, %v", partial, err)
	}
	a.err = host.ErrUnsupported
	if _, err := n.listWorkspaces(context.Background()); !errors.Is(err, host.ErrUnsupported) || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("all hosts failed = %v", err)
	}
	n.hosts = host.NewRegistry()
	if _, err := n.listWorkspaces(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("missing host = %v", err)
	}
}

func TestWorkspacesListEmptyHostWithFailedSource(t *testing.T) {
	empty := &workspaceHost{bareHost: bareHost{kind: host.Herdr}}
	failed := &workspaceHost{bareHost: bareHost{kind: host.Botmux}, err: errors.New("botmux observe: timeout")}
	n := &Node{hosts: host.NewRegistry(empty, failed), state: newState()}
	raw, err := n.handle(context.Background(), "workspaces.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := raw.(proto.WorkspacesResponse)
	if got.Workspaces == nil || len(got.Workspaces) != 0 || len(got.Errors) != 1 ||
		got.Errors[0].Source != host.Botmux || got.Errors[0].Error != failed.err.Error() {
		t.Fatalf("empty successful source with failure = %+v", got)
	}
	failed.err = nil
	got, err = n.listWorkspaces(context.Background())
	if err != nil || len(got.Errors) != 0 {
		t.Fatalf("recovered source = %+v, %v", got, err)
	}
}

// An engine that is not running on this computer (Orca not started, botmux without a daemon or too old) has no
// workspaces: not a source error for the board, and not a failure even when nothing else runs (#3023).
func TestWorkspacesListSkipsHostsNotRunning(t *testing.T) {
	herdr := &workspaceHost{bareHost: bareHost{kind: host.Herdr}, spaces: []host.Space{{ID: "w1", Label: "project"}}}
	orca := &workspaceHost{bareHost: bareHost{kind: host.Orca},
		err: host.Mark(errors.New("orca is not running: open orca-runtime.json: no such file or directory"), host.ErrNotRunning)}
	botmux := &workspaceHost{bareHost: bareHost{kind: host.Botmux},
		err: host.Mark(errors.New("botmux is too old to list its sessions"), host.ErrNotRunning)}
	n := &Node{hosts: host.NewRegistry(herdr, orca, botmux), state: newState()}
	got, err := n.listWorkspaces(context.Background())
	if err != nil || len(got.Workspaces) != 1 || got.Workspaces[0].Source != host.Herdr || len(got.Errors) != 0 {
		t.Fatalf("with engines not running = %+v, %v", got, err)
	}
	n.hosts = host.NewRegistry(orca, botmux)
	got, err = n.listWorkspaces(context.Background())
	if err != nil || got.Workspaces == nil || len(got.Workspaces) != 0 || len(got.Errors) != 0 {
		t.Fatalf("nothing running = %+v, %v", got, err)
	}
	// A real failure next to one not running is still reported, and alone it is still an error.
	botmux.err = errors.New("botmux observe: signal: killed")
	got, err = n.listWorkspaces(context.Background())
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("real failure = %+v, %v", got, err)
	}
}

func TestWorkspaceResolveCanonicalDirectory(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "project")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: home, state: newState()}
	w, err := n.resolveWorkspace("~/alias")
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(real)
	if w.Cwd != canonical || w.ID != canonical || w.SourceID != canonical || w.Source != "directory" || w.Title != "project" || !w.Available {
		t.Fatalf("resolved = %+v", w)
	}
	filename := filepath.Join(home, "file")
	if err := os.WriteFile(filename, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	cloud := filepath.Join(home, "Library", "Mobile Documents", "project")
	if err := os.MkdirAll(cloud, 0o700); err != nil {
		t.Fatal(err)
	}
	cloudAlias := filepath.Join(home, "cloud-alias")
	if err := os.Symlink(cloud, cloudAlias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "project", "~other/project", filename, filepath.Join(home, "missing"), cloud, cloudAlias} {
		if _, err := n.resolveWorkspace(path); err == nil {
			t.Errorf("accepted invalid directory %q", path)
		}
	}
	raw, err := n.handle(context.Background(), "workspace.resolve", []byte(`{"path":"~/alias"}`))
	if err != nil || raw.(proto.Workspace).Cwd != canonical {
		t.Fatalf("resolve RPC = %+v, %v", raw, err)
	}
}

func TestWorkspaceSessionCanonicalDirectory(t *testing.T) {
	n, f, home := herdrNode(t)
	real := filepath.Join(home, "project")
	alias := filepath.Join(home, "alias")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(map[string]any{"type": "user", "cwd": alias,
		"message": map[string]any{"role": "user", "content": "project"}})
	writeTranscript(t, home, alias, sidA, time.Now(), string(line), `{"type":"custom-title","customTitle":"project"}`)
	writeTranscript(t, home, alias, sidB, time.Now(), string(line), `{"type":"custom-title","customTitle":"offline project"}`)
	n.setKept([]string{sidB})
	a := claudeAgent(sidA, proto.StatusIdle, "p1", "t1", "w1", alias, "project")
	f.reply("session.snapshot", snapshot([]any{map[string]any{"workspace_id": "w1", "cwd": real}}, nil, []any{a}, []any{a}))
	sessions := byID(listSessions(t, n))
	canonical, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sidA, sidB} {
		s := sessions[id]
		if s.Cwd != alias || s.WorkspaceCwd != canonical {
			t.Errorf("session %s lost original cwd or physical identity: cwd=%q workspace_cwd=%q", id, s.Cwd, s.WorkspaceCwd)
		}
	}
	w := workspaceList(t, n)
	if len(w) != 1 || w[0].Cwd != sessions[sidA].WorkspaceCwd {
		t.Fatalf("workspace and session physical paths differ: %+v / %+v", w, sessions[sidA])
	}
	resolved, err := n.resolveWorkspace(alias)
	if err != nil || resolved.Cwd != sessions[sidA].WorkspaceCwd {
		t.Fatalf("directory binding and session physical paths differ: %+v / %v", resolved, err)
	}
}

func TestWorkspaceSessionDirectoryCacheRefreshes(t *testing.T) {
	home := t.TempDir()
	first, second, alias := filepath.Join(home, "first"), filepath.Join(home, "second"), filepath.Join(home, "alias")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: home}
	wantFirst, _ := filepath.EvalSymlinks(first)
	wantSecond, _ := filepath.EvalSymlinks(second)
	if got := n.sessionWorkspaceCwd(alias); got != wantFirst {
		t.Fatalf("initial directory = %q, want %q", got, wantFirst)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	if got := n.sessionWorkspaceCwd(alias); got != wantFirst {
		t.Fatalf("repeated inventory did not reuse directory cache: %q", got)
	}
	n.workspaceDirs.Lock()
	entry := n.workspaceDirs.entries[alias]
	entry.at = time.Now().Add(-workspaceDirectoryCacheTTL)
	n.workspaceDirs.entries[alias] = entry
	n.workspaceDirs.Unlock()
	if got := n.sessionWorkspaceCwd(alias); got != wantSecond {
		t.Fatalf("expired identity = %q, want %q", got, wantSecond)
	}
	for _, path := range []string{"", "relative", filepath.Join(home, "missing"), filepath.Join(home, "Library", "Mobile Documents")} {
		if got := n.sessionWorkspaceCwd(path); got != "" {
			t.Errorf("unverified directory %q has physical identity %q", path, got)
		}
	}
}
