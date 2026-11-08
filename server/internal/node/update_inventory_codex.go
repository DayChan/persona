package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nwverse/persona/server/internal/proto"
)

// updateCodexObservation is raw, freshly read evidence. It includes ephemeral
// background threads and subagents that a conversation display intentionally omits.
// It does not assert completeness for the machine or any other app-server.
type updateCodexObservation struct {
	Instance   string              `json:"instance"`
	Generation string              `json:"generation"`
	Revision   string              `json:"revision"`
	Threads    []updateCodexThread `json:"threads"`
}

type updateCodexThread struct {
	ID                  string `json:"id"`
	Status              string `json:"status"`
	PendingInteractions int    `json:"pending_interactions"`
	Ephemeral           bool   `json:"ephemeral"`
	ParentThreadID      string `json:"parent_thread_id"`
	BackgroundTerminals int    `json:"background_terminals"`
	BackgroundRevision  string `json:"background_revision"`
}

// observeUpdateThreads reads without resuming/joining a thread, canceling asks or
// replacing the normal display cache. An unread loaded thread is unknown, never
// an idle copy from a previous refresh. The caller owns the total read deadline.
func (c *codexRemote) observeUpdateThreads(ctx context.Context) (updateCodexObservation, error) {
	out := updateCodexObservation{Instance: c.spec.ID, Generation: c.generation, Threads: []updateCodexThread{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	if !ready || c.spec.ID == "" || c.generation == "" {
		return out, fmt.Errorf("app-server source identity is not ready")
	}
	ids, err := c.loadedForUpdate(ctx)
	if err != nil {
		return out, fmt.Errorf("read complete loaded thread inventory: %w", err)
	}
	slices.Sort(ids)
	for i, id := range ids {
		if !validID(id) || i > 0 && id == ids[i-1] {
			return out, fmt.Errorf("invalid or duplicate loaded thread identity")
		}
		var response struct {
			Thread remoteThreadData `json:"thread"`
		}
		if err := c.wire.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true}, &response); err != nil {
			return out, fmt.Errorf("loaded thread activity unavailable: %w", err)
		}
		d := response.Thread
		if d.ID != id {
			return out, fmt.Errorf("loaded thread read identity conflicts")
		}
		status := proto.StatusUnknown
		switch d.Status.Type {
		case "idle":
			status = proto.StatusIdle
		case "active":
			status = proto.StatusWorking
		}
		for _, turn := range d.Turns {
			if turn.Status == "inProgress" {
				status = proto.StatusWorking
			}
		}
		// Unknown active flags cannot be interpreted as idleness.
		if len(d.Status.ActiveFlags) != 0 {
			status = proto.StatusBlocked
		}
		// Keep activity and identity only. Conversation previews, prompts, titles
		// and transcript paths do not belong in an update receipt.
		background, revision, err := observeUpdateBackground(ctx, c.wire, id)
		if err != nil {
			return out, err
		}
		out.Threads = append(out.Threads, updateCodexThread{ID: id, Status: status,
			Ephemeral: d.Ephemeral, ParentThreadID: d.ParentThreadID,
			BackgroundTerminals: background, BackgroundRevision: revision})
	}
	finalIDs, err := c.loadedForUpdate(ctx)
	if err != nil {
		return out, fmt.Errorf("recheck loaded thread inventory: %w", err)
	}
	slices.Sort(finalIDs)
	if !slices.Equal(ids, finalIDs) {
		return out, fmt.Errorf("loaded thread inventory changed while being read")
	}
	c.mu.Lock()
	ready = c.ready
	for i := range out.Threads {
		for _, ask := range c.asks {
			if ask.thread == out.Threads[i].ID {
				out.Threads[i].PendingInteractions++
			}
		}
	}
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !ready {
		return out, fmt.Errorf("app-server disconnected during activity read")
	}
	b, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(b)
	out.Revision = hex.EncodeToString(digest[:])
	return out, nil
}

// Both a Node-owned stdio connection and a shared app-server use this same
// read-only RPC. A nil cache or omitted data is never an empty terminal list.
func observeUpdateBackground(ctx context.Context, wire *codexConn, thread string) (int, string, error) {
	var response struct {
		Data *[]json.RawMessage `json:"data"`
	}
	if err := wire.call(ctx, "thread/backgroundTerminals/list", map[string]any{"threadId": thread}, &response); err != nil {
		return 0, "", fmt.Errorf("fresh background terminal inventory unavailable: %w", err)
	}
	if response.Data == nil {
		return 0, "", fmt.Errorf("explicit background terminal inventory unavailable")
	}
	for _, raw := range *response.Data {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil || item == nil {
			return 0, "", fmt.Errorf("invalid background terminal inventory item")
		}
	}
	data, err := json.Marshal(*response.Data)
	if err != nil {
		return 0, "", err
	}
	digest := sha256.Sum256(data)
	return len(*response.Data), hex.EncodeToString(digest[:]), nil
}

// The update contract requires an explicit list on every page, including an
// empty final page. Display APIs may remain tolerant of older servers.
func (c *codexRemote) loadedForUpdate(ctx context.Context) ([]string, error) {
	ids := []string{}
	cursor := ""
	seen := map[string]bool{}
	for pageNumber := 0; pageNumber < 1000; pageNumber++ {
		var page struct {
			Data       *[]string `json:"data"`
			NextCursor *string   `json:"nextCursor"`
		}
		args := map[string]any{"limit": 100}
		if cursor != "" {
			args["cursor"] = cursor
		}
		if err := c.wire.call(ctx, "thread/loaded/list", args, &page); err != nil {
			return nil, err
		}
		if page.Data == nil {
			return nil, fmt.Errorf("explicit loaded thread list unavailable")
		}
		ids = append(ids, (*page.Data)...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return ids, nil
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, fmt.Errorf("loaded thread page cursor repeated")
		}
		seen[cursor] = true
	}
	return nil, fmt.Errorf("loaded thread page budget exceeded")
}
