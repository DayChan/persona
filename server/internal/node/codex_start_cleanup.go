package node

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A start already gave its prompt to Codex as an argument. Leaving its dialog open would let it run that prompt
// later without a task tracking it. Close that start's pane before answering, including after cancellation. Unlike
// closeNewTab, failure is retained and reported: a retry must finish this cleanup before creating another pane.
type codexAbandonedStart struct {
	launch    host.Launcher
	created   *host.Created
	workspace string
}

// closeAbandonedCodex serializes cleanup only when failed panes are pending. Independent starts do not hold
// this lock while creating their tabs or waiting for threads, and can add failures while a cleanup is in flight.
func (n *Node) closeAbandonedCodex(ctx context.Context, key string) error {
	n.mu.Lock()
	pending := len(n.codexAbandoned[key]) > 0
	n.mu.Unlock()
	if !pending {
		return nil
	}
	defer n.typing.lock("codex-cleanup:" + key)()
	for {
		n.mu.Lock()
		starts := slices.Clone(n.codexAbandoned[key])
		n.mu.Unlock()
		if len(starts) == 0 {
			return nil
		}
		var failures []error
		for _, start := range starts {
			if err := closeCodexStart(ctx, start); err != nil {
				failures = append(failures, err)
				continue
			}
			n.mu.Lock()
			n.codexAbandoned[key] = slices.DeleteFunc(n.codexAbandoned[key], func(s codexAbandonedStart) bool {
				return s.created == start.created
			})
			if len(n.codexAbandoned[key]) == 0 {
				delete(n.codexAbandoned, key)
			}
			n.mu.Unlock()
		}
		if len(failures) > 0 {
			return errors.Join(failures...)
		}
		// A concurrent start may have added another failed cleanup while these panes were closing.
	}
}

// closeCodexStart stops one abandoned start independently of other starts in the same directory.
func closeCodexStart(ctx context.Context, start codexAbandonedStart) error {
	org, ok := start.launch.(host.Organizer)
	var err error
	if !ok {
		err = host.ErrUnsupported
	} else {
		space := start.workspace
		if space == "" {
			space = start.created.SpaceID
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = org.Close(cleanup, start.created.Pane, space, start.created.SlotID)
		var he *herdr.Error
		if errors.As(err, &he) {
			switch he.Code {
			case "pane_not_found":
				err = nil
			case "tab_not_found", "workspace_not_found":
				// A pane may have moved out of the missing tab. Only a fresh layout proving it absent clears it.
				if abandonedCodexPaneGone(cleanup, start) {
					err = nil
				}
			}
		}
		cancel()
	}
	if err != nil {
		return &rpc.Error{Code: proto.CodeAgentStartStalled,
			Message: i18n.T(i18n.Default, "node-codex-start.cleanup_failed", map[string]any{"pane": start.created.Pane.String(), "reason": err.Error()})}
	}
	return nil
}

func abandonedCodexPaneGone(ctx context.Context, start codexAbandonedStart) bool {
	h, ok := start.launch.(*herdrHost)
	if !ok {
		return false
	}
	target, err := h.target(ctx, start.created.Pane.ID)
	if err != nil {
		return false
	}
	// Host.Snapshot can substitute a cached named-session layout; only query this pane's actual server.
	layout, err := layoutOf(ctx, target.c, target.session)
	if err != nil || layout == nil {
		return false
	}
	matches := func(a host.Agent) bool { return a.PaneID == start.created.Pane.String() }
	return !slices.ContainsFunc(layout.Panes, matches) && !slices.ContainsFunc(layout.Agents, matches)
}
