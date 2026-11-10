package node

import (
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

type codexSpace struct {
	kind   string
	space  host.Space
	places []gitPlace
}

func unplacedCodexRoot(s proto.Session) bool {
	// A thread that is not running (history, or one whose terminal's Codex exited) stays out of workspaces.
	return s.Agent == "codex" && strings.HasPrefix(s.SourceKind, "codex_") && s.Status != proto.StatusOffline &&
		!s.Assistant && !s.IsCodexSubagent() && s.SpaceID == "" && s.WorkspaceID == "" && s.PaneID == ""
}

func spacePath(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(path)
}

// App-server discovery supplies threads, not terminal placement. Only associate
// roots with a unique workspace; never manufacture a pane, tab or capability.
// Explicit host placement wins and children inherit in resolveCodexHierarchy.
func (n *Node) placeUnassignedCodex(sessions []proto.Session) bool {
	needed := false
	for _, s := range sessions {
		if unplacedCodexRoot(s) {
			needed = true
			break
		}
	}
	if !needed {
		return false
	}
	spaces := map[[2]string]*codexSpace{}
	n.mu.Lock()
	for kind, l := range n.layouts {
		if kind != host.Herdr && kind != host.HerdrCore {
			continue
		}
		for _, w := range l.spaces {
			if w.ID != "" && !strings.HasPrefix(w.ID, dirSpace) {
				spaces[[2]string{kind, w.ID}] = &codexSpace{kind: kind, space: w}
			}
		}
	}
	n.mu.Unlock()
	for _, c := range spaces {
		if cwd := spacePath(c.space.Cwd); cwd != "" {
			p := gitPlaces.lookup(cwd)
			// Use the workspace's directory, not its entire worktree, for
			// containment: two spaces can intentionally divide one repository.
			c.places = append(c.places, gitPlace{root: spacePath(p.root), worktree: cwd})
		}
	}
	// A terminal's current directory can be more recent than its space's
	// configured folder. Only explicit, still-present spaces supply evidence.
	for _, s := range sessions {
		c := spaces[[2]string{s.SpaceKind, s.SpaceID}]
		if c == nil || s.PaneID == "" || s.IsCodexSubagent() {
			continue
		}
		if cwd := spacePath(s.Cwd); cwd != "" {
			c.places = append(c.places, gitPlace{root: spacePath(s.RepoRoot), worktree: cwd})
		}
	}
	changed := false
	for i := range sessions {
		s := &sessions[i]
		if !unplacedCodexRoot(*s) {
			continue
		}
		cwd, repo := spacePath(s.Cwd), spacePath(s.RepoRoot)
		var best *codexSpace
		bestScore, bestDepth, tied := 0, 0, false
		for _, c := range spaces {
			score, depth := 0, 0
			for _, p := range c.places {
				candidate, length := 0, 0
				if repo != "" && p.root == repo {
					candidate = 1
				}
				inside := cwd != "" && p.worktree != "" && (cwd == p.worktree || strings.HasPrefix(cwd, p.worktree+string(filepath.Separator)))
				if inside && (cwd == p.worktree || repo != "" && repo == p.root) {
					candidate, length = 2, len(p.worktree)
				}
				if candidate > score || candidate == score && length > depth {
					score, depth = candidate, length
				}
			}
			if score == 0 {
				continue
			}
			if score > bestScore || score == bestScore && depth > bestDepth {
				best, bestScore, bestDepth, tied = c, score, depth, false
			} else if score == bestScore && depth == bestDepth {
				tied = true
			}
		}
		if best != nil && !tied {
			s.SpaceKind, s.SpaceID = best.kind, best.space.ID
			s.SpaceName, s.SpaceIndex, s.ParentSpaceID = best.space.Label, best.space.Number, best.space.ParentID
			changed = true
		}
	}
	return changed
}
