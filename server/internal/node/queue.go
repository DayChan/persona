package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// editQueue takes the queued input back out of claude (the TUI's "press up to edit queued messages" pulls every
// queued message into the input box), clears the input, and re-queues the ones listed in keep, in order.
// It returns the messages that were queued so the phone can put the edited one in its composer.
func (n *Node) editQueue(ctx context.Context, id string, keep []int) ([]string, error) {
	pane, err := n.paneFor(ctx, id)
	if err != nil {
		return nil, err
	}
	p := n.transcriptPath(id)
	if p == "" {
		return nil, fmt.Errorf("transcript for session %s not found", id)
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	queue := n.meta(p, st).queue
	if len(queue) == 0 {
		return nil, fmt.Errorf("nothing is queued")
	}
	if err := n.sendKeys(ctx, pane, "up"); err != nil {
		return nil, err
	}
	time.Sleep(300 * time.Millisecond)
	// ctrl+u only clears the current line: walk back through every line of the pulled-back text.
	lines := 0
	for _, q := range queue {
		lines += strings.Count(q, "\n") + 1
	}
	for i := 0; i < lines; i++ {
		_ = n.sendKeys(ctx, pane, "ctrl+u", "backspace")
	}
	_ = n.sendKeys(ctx, pane, "ctrl+u")
	for _, k := range keep {
		if k >= 0 && k < len(queue) {
			if err := n.prompt(ctx, pane, queue[k]); err != nil {
				return queue, fmt.Errorf("re-queue: %w", err)
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	return queue, nil
}

// sendNowHint is what Claude Code (2.1.31x) shows under a queued message: its own "send now" (#6088).
const sendNowHint = "ctrl+x ctrl+s to send now"

// offersSendNow: the screen shows a queued message with Claude Code's send-now beside it.
func offersSendNow(screen string) bool {
	return strings.Contains(strings.Join(strings.Fields(screen), " "), sendNowHint)
}

// sendNowWait is how long the node looks for the send-now hint to go once the key is pressed.
var sendNowWait = 3 * time.Second

// sendQueuedNow gives the agent what waits in Claude Code's queue now (#6088), with Claude Code's own send-now (ctrl+x
// ctrl+s): a command the agent runs moves to the background and goes on, a reply it is writing is asked again with the
// message (measured on 2.1.319; Esc would stop the command). Pressed only while the screen offers it: that is the queue
// right now (the transcript says a message was taken about a second late, and a key pressed then would hit a turn that
// already has it), and an older Claude without it offers nothing.
func (n *Node) sendQueuedNow(ctx context.Context, id string) error {
	if a, ok := n.agent(id); ok && a.Agent != "claude" {
		return refusal("send_now_unsupported", "only Claude Code can send queued messages now", nil)
	}
	pane, err := n.paneFor(ctx, id)
	if err != nil {
		return err
	}
	screen, err := n.readPane(ctx, pane, "visible", 80)
	if err != nil {
		return err
	}
	if !offersSendNow(screen) {
		if p := n.transcriptPath(id); p != "" {
			if st, err := os.Stat(p); err == nil && len(n.meta(p, st).queue) > 0 {
				return refusal("send_now_unsupported", "this Claude Code shows no send-now for its queued messages (update it)", nil)
			}
		}
		return refusal("nothing_queued", "nothing is queued for the agent any more", nil)
	}
	if err := n.sendKeys(ctx, pane, "ctrl+x", "ctrl+s"); err != nil {
		return err
	}
	for deadline := time.Now().Add(sendNowWait); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		if s, err := n.readPane(ctx, pane, "visible", 80); err == nil && !offersSendNow(s) {
			return nil
		}
	}
	// the key went in and the queue is still shown: perhaps it was taken, perhaps not (not "none", as a refusal says)
	return &rpc.Error{Code: "send_now_in_doubt", Message: "the queued messages are still shown as queued", Data: json.RawMessage(`{"delivered":"unknown"}`)}
}
