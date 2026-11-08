package node

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

const assistantIdleTimeout = 20 * time.Minute

// A process with background tasks or monitors running is kept: they are its children, and their notifications are
// what wakes the conversation up again. Past this long without a turn it is let go anyway (a forgotten `tail -f`).
const assistantTaskMaxIdle = 12 * time.Hour

// assistantInterrupt stops the current turn. claude -p has no interrupt over stdin, so the process is killed;
// the next message resumes the conversation from its transcript. Codex has turn/interrupt: the process stays.
func (n *Node) assistantInterrupt(id string) error {
	if n.runtimeAssistant(id) {
		return n.assistantInterruptRuntime(context.Background(), id)
	}
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if p != nil && p.agent == "codex" && p.alive() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := p.cx.interrupt(ctx); err == nil {
			return nil
		}
	}
	n.stopAssistant(id)
	n.rememberTasks(id, nil) // stopped on purpose, background tasks included
	return nil
}

func (n *Node) stopAssistant(id string) {
	n.mu.Lock()
	p := n.procs[id]
	delete(n.procs, id)
	n.mu.Unlock()
	if p != nil {
		p.stop()
	}
}

func (p *assistantProc) stop() {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = osproc.StopGroup(p.cmd.Process.Pid, false)
		go func() {
			select {
			case <-p.exited:
			case <-time.After(5 * time.Second):
				_ = osproc.StopGroup(p.cmd.Process.Pid, true)
			}
		}()
	}
}

// reapable: idle long enough to end; p.mu is held. Background tasks and monitors are children of the process
// (killed with it) and wake it when they report, so a process waiting on them stays (2026-10-30: a 23-minute
// background command was killed at minute 20 and the conversation never heard back).
func (p *assistantProc) reapable(now time.Time) bool {
	if p.compacting {
		return now.Sub(p.lastActive) > 10*time.Minute // a compaction that never reported back
	}
	if p.busy {
		return false
	}
	if len(p.tasks) > 0 {
		return now.Sub(p.lastActive) > assistantTaskMaxIdle
	}
	return now.Sub(p.lastActive) > assistantIdleTimeout
}

// idleCompactable: about to be let go with a context worth compacting first (assistantIdleCompact); p.mu is held.
func (p *assistantProc) idleCompactable() bool {
	threshold := p.compactIdleAt
	if threshold == 0 {
		threshold = assistantIdleCompact
	}
	return p.agent == "claude" && !p.compacting && !p.compacted && p.context >= threshold
}

// compactIdle sends /compact to an idle process. It is not a turn: the conversation stays as it was in the app (the
// transcript shows a notice), and the process is let go when idle again.
func (n *Node) compactIdle(p *assistantProc) {
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "/compact"}})
	p.mu.Lock()
	tokens := p.context
	p.compacting, p.compacted = true, true
	p.lastActive = time.Now()
	p.mu.Unlock()
	if err := p.write(line); err != nil {
		n.logger.Printf("assistant %s: compact: %v", p.id[:8], err)
		n.stopAssistant(p.id)
		return
	}
	n.logger.Printf("assistant %s: idle with %d tokens in context, compacting before letting it go", p.id[:8], tokens)
}

// reapAssistants ends processes idle for a while; their conversations resume on the next message.
func (n *Node) reapAssistants(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			n.mu.Lock()
			ids := make([]string, 0, len(n.procs))
			for id := range n.procs {
				ids = append(ids, id)
			}
			spare := n.spare
			n.spare = nil
			n.mu.Unlock()
			if spare != nil {
				spare.stop()
			}
			if n.keeper != nil { // a resident node: the next connection takes them over (assistant_keep.go)
				n.handOverAssistants()
				ids = nil
			}
			for _, id := range ids {
				n.stopAssistant(id)
			}
			return
		case <-t.C:
		}
		n.mu.Lock()
		var stale *assistantProc
		if n.spare != nil && time.Since(n.spare.born) > assistantSpareMaxAge {
			stale, n.spare = n.spare, nil
		}
		var idle []string
		var compact []*assistantProc
		var timedOut []string
		for id, p := range n.procs {
			p.mu.Lock()
			if p.manual && time.Since(p.lastActive) > assistantCompactTimeout {
				p.manual, p.compacting = false, false
				timedOut = append(timedOut, id)
			}
			if p.reapable(time.Now()) {
				if p.idleCompactable() {
					compact = append(compact, p)
				} else {
					idle = append(idle, id)
				}
			}
			p.mu.Unlock()
		}
		n.mu.Unlock()
		for _, id := range timedOut {
			n.compactionEnded(id, "压缩超过 10 分钟没有结果")
		}
		n.sweepCompactions()
		for _, p := range compact {
			n.compactIdle(p)
		}
		for _, id := range idle {
			n.stopAssistant(id)
			n.rememberTasks(id, nil) // let go on purpose: nothing to tell it after a restart
		}
		if stale != nil {
			stale.stop()
		}
	}
}
