package node

import (
	"context"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// Historical display status never supplies activity evidence. Retained task or
// child metadata can, however, expose work absent from a live parent's list and
// must remain a blocker until its own authoritative completion is available.
func (n *Node) observeUpdateRetained(ctx context.Context, target nodeclient.UpdateInventoryTarget) (string, error) {
	n.state.mu.Lock()
	states := map[string]sessionState{}
	for id, state := range n.state.Sessions {
		states[id] = state
	}
	loading := n.state.loading != nil
	n.state.mu.Unlock()
	if loading {
		return "", errors.New("retained session state is loading")
	}
	children := map[string]any{}
	for id, state := range states {
		if len(state.Tasks) > 0 || state.Compaction != nil && state.Compaction.State != "done" && state.Compaction.State != "failed" {
			return "", fmt.Errorf("retained background or compaction completion is unresolved: session=%s", id)
		}
		if !identity.IsAssistantDirOf(state.Cwd, target.Manager) {
			continue
		}
		if _, codex := n.codexSource(id); codex || launcherAgentType(state.Launcher) == "codex" {
			continue
		}
		directory := n.subagentDir(id)
		if directory == "" {
			return "", errors.New("retained assistant child inventory path unavailable")
		}
		observed, err := observeUpdateSubagentTree(ctx, directory)
		if err != nil {
			return "", err
		}
		for _, child := range observed {
			if child.Running {
				return "", fmt.Errorf("retained subagent completion is unresolved: session=%s child=%s", id, child.ID)
			}
		}
		children[id] = observed
	}
	return updateDigest(struct {
		States   map[string]sessionState
		Children map[string]any
	}{states, children}), ctx.Err()
}
