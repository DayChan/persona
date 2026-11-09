package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

const workspaceDirectoryCacheTTL = 30 * time.Second

type workspaceDirectoryEntry struct {
	cwd string
	at  time.Time
}

type workspaceDirectoryCache struct {
	sync.Mutex
	entries map[string]workspaceDirectoryEntry
}

// sessionWorkspaceCwd verifies physical directory identity without changing the
// runtime's original cwd. Cache both successful and failed lookups: offline
// transcripts and live sessions are listed every second, often sharing a cwd.
func (n *Node) sessionWorkspaceCwd(cwd string) string {
	path := n.resolvePath(cwd, "")
	if path == "" || inICloud(path) {
		return ""
	}
	c := &n.workspaceDirs
	c.Lock()
	defer c.Unlock()
	if entry, ok := c.entries[path]; ok && time.Since(entry.at) < workspaceDirectoryCacheTTL {
		return entry.cwd
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || inICloud(real) {
		real = ""
	} else if st, err := os.Stat(real); err != nil || !st.IsDir() {
		real = ""
	}
	// The catalog of recent session directories is bounded; discard old entries
	// if a long-lived node has visited more paths than the recent inventory holds.
	if c.entries == nil || len(c.entries) >= 512 {
		c.entries = map[string]workspaceDirectoryEntry{}
	}
	c.entries[path] = workspaceDirectoryEntry{cwd: real, at: time.Now()}
	return real
}

// fillSpaceDirectories uses actual pane directories only when they agree. A
// workspace may contain panes opened in unrelated folders, so no root is guessed.
func fillSpaceDirectories(spaces []host.Space, panes []host.Agent) {
	dirs := map[string]string{}
	conflict := map[string]bool{}
	for _, p := range panes {
		if p.Cwd == "" {
			continue
		}
		if old := dirs[p.WorkspaceID]; old != "" && old != p.Cwd {
			conflict[p.WorkspaceID] = true
		}
		dirs[p.WorkspaceID] = p.Cwd
	}
	for i := range spaces {
		if spaces[i].Cwd == "" && !conflict[spaces[i].ID] {
			spaces[i].Cwd = dirs[spaces[i].ID]
		}
	}
}

func (n *Node) listWorkspaces(ctx context.Context) (proto.WorkspacesResponse, error) {
	out := proto.WorkspacesResponse{Workspaces: []proto.Workspace{}}
	hs := n.hosts.All()
	if len(hs) == 0 {
		return out, fmt.Errorf("unsupported: this node has no workspace host")
	}
	var failures []error
	succeeded := 0
	for _, h := range hs {
		var spaces []host.Space
		var err error
		// Classic herdr's launcher listing predates snapshot support. Prefer the
		// full layout for its pane directories, keeping workspace.list as fallback.
		if classic, ok := h.(*herdrHost); ok {
			if l, e := h.Snapshot(ctx); e == nil && l != nil {
				spaces = l.Spaces
			} else {
				spaces, err = classic.Spaces(ctx)
			}
		} else if lister, ok := h.(host.SpaceLister); ok {
			spaces, err = lister.Spaces(ctx)
		} else {
			var l *host.Layout
			l, err = h.Snapshot(ctx)
			if err == nil && l != nil {
				spaces = l.Spaces
			} else if err == nil {
				err = fmt.Errorf("unsupported: host returned no workspace layout")
			}
		}
		if errors.Is(err, host.ErrNotRunning) {
			// Not running on this computer (Orca not started, no botmux daemon): it has no workspaces, as in the agents
			// list. Not a source error the board should show.
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s workspaces: %w", h.Kind(), err))
			out.Errors = append(out.Errors, proto.WorkspaceSourceError{Source: h.Kind(), Error: err.Error()})
			continue
		}
		succeeded++
		seen := map[string]bool{}
		for _, s := range spaces {
			if s.ID == "" || seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			cwd := s.Cwd
			if path := n.resolvePath(cwd, ""); path != "" && !inICloud(path) {
				if real, err := filepath.EvalSymlinks(path); err == nil && !inICloud(real) {
					cwd = real
				}
			}
			out.Workspaces = append(out.Workspaces, proto.Workspace{ID: s.ID, Source: h.Kind(), SourceID: s.ID,
				Title: firstOf(s.DisplayName, s.Label), Cwd: cwd, Branch: s.Branch, Online: true, Available: true,
				ParentSourceID: s.ParentID, Index: s.Number,
				ProjectID: s.ProjectID, ProjectName: s.ProjectName, ProjectPath: s.ProjectPath, IsMain: s.IsMain,
				Unread: s.Unread, SidebarStatus: s.SidebarStatus, WorkspaceKind: s.WorkspaceKind, Head: s.Head})
		}
	}
	if succeeded == 0 && len(failures) > 0 {
		// Every source that runs here failed: an error, not an empty catalog.
		return proto.WorkspacesResponse{}, errors.Join(failures...)
	}
	return out, nil
}

// resolveWorkspace validates a directory binding on the machine that owns it.
// It performs no host, terminal, agent, or Git operations.
func (n *Node) resolveWorkspace(asked string) (proto.Workspace, error) {
	path := n.resolvePath(asked, "")
	if path == "" {
		return proto.Workspace{}, fmt.Errorf("workspace path must be an absolute directory or ~/ path")
	}
	if inICloud(path) {
		return proto.Workspace{}, fmt.Errorf("iCloud Drive files are not readable remotely")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return proto.Workspace{}, fmt.Errorf("resolve workspace directory: %w", err)
	}
	if inICloud(real) {
		return proto.Workspace{}, fmt.Errorf("iCloud Drive files are not readable remotely")
	}
	st, err := os.Stat(real)
	if err != nil {
		return proto.Workspace{}, fmt.Errorf("stat workspace directory: %w", err)
	}
	if !st.IsDir() {
		return proto.Workspace{}, fmt.Errorf("workspace path is not a directory")
	}
	return proto.Workspace{ID: real, Source: "directory", SourceID: real, Title: filepath.Base(real),
		Cwd: real, Online: true, Available: true}, nil
}
