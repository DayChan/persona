package node

import (
	"context"
	"errors"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
	"strconv"
)

func (n *Node) observeUpdateAssistant(ctx context.Context, p *assistantProc) ([]nodeclient.UpdateActivity, string, error) {
	if p.cx == nil {
		// The Claude stream has no read-only barrier proving a complete current
		// background list. A previous event is not fresh idle evidence.
		return nil, "", errors.New("live assistant lacks synchronous background inventory authority")
	}
	c := p.cx
	c.mu.Lock()
	id, closed := c.threadID, c.closed
	c.mu.Unlock()
	if id == "" || closed {
		return nil, "", errors.New("assistant app-server is starting or disconnected")
	}
	key := "owned:" + strconv.Itoa(p.cmd.Process.Pid)
	reader := &codexRemote{spec: proto.CodexServer{ID: key}, generation: updateDigest([]any{p.cmd.Process.Pid, p.born}), wire: c, ready: true}
	observation, err := reader.observeUpdateThreads(ctx)
	if err != nil {
		return nil, "", err
	}
	found := false
	for _, row := range observation.Threads {
		found = found || row.ID == id
	}
	if !found {
		return nil, "", errors.New("owned main thread is absent from its live app-server inventory")
	}
	c.mu.Lock()
	pending, turn, nowID, ended := len(c.asks), c.turnID, c.threadID, c.closed
	c.mu.Unlock()
	if nowID != id || ended || !p.alive() {
		return nil, "", errors.New("owned assistant source changed during read")
	}
	p.mu.Lock()
	busy, compacting := p.busy, p.compacting
	p.mu.Unlock()
	rows := []nodeclient.UpdateActivity{}
	for _, row := range observation.Threads {
		activity := updateCodexActivity(key, row, observation.Revision)
		if row.ID == id {
			activity.PendingInteractions += pending
			activity.Compacting = compacting
			if busy || turn != "" {
				activity.Status = proto.StatusWorking
			}
		}
		rows = append(rows, activity)
	}
	revision := updateDigest(rows)
	if revision == "" {
		return nil, "", errors.New("owned assistant revision unavailable")
	}
	return rows, revision, nil
}
