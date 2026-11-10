package node

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// cancelHub is a hub that never answers a card and notes the cards the node withdraws.
type cancelHub struct {
	mu        sync.Mutex
	asked     []string
	toolIDs   []string
	cancelled []string
}

func (h *cancelHub) counts() (asked, cancelled int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.asked), len(h.cancelled)
}

func (h *cancelHub) toolUseIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.toolIDs...)
}

func withCancelHub(t *testing.T, n *Node) *cancelHub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	h := &cancelHub{}
	hub.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != "interaction.create" {
			return map[string]any{}, nil
		}
		var p struct {
			Interaction proto.Interaction `json:"interaction"`
		}
		_ = json.Unmarshal(params, &p)
		h.mu.Lock()
		h.asked = append(h.asked, p.Interaction.ID)
		h.toolIDs = append(h.toolIDs, p.Interaction.ToolUseID)
		h.mu.Unlock()
		<-ctx.Done() // nobody on the phone answers
		return nil, ctx.Err()
	}
	hub.OnEvent = func(event string, params json.RawMessage) {
		if event != "interaction.cancel" {
			return
		}
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(params, &p)
		h.mu.Lock()
		h.cancelled = append(h.cancelled, p.ID)
		h.mu.Unlock()
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	peer := n.peer
	go func() { _ = peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
	return h
}

// fakeAgent says the claude 4242 started "linux:7" lives until dead is set.
func fakeAgent(t *testing.T) *atomic.Bool {
	t.Helper()
	var dead atomic.Bool
	oldGone, oldWatch := agentGone, agentWatch
	agentGone = func(pid int, start string) bool { return pid != 4242 || start != "linux:7" || dead.Load() }
	agentWatch = 10 * time.Millisecond
	t.Cleanup(func() { agentGone, agentWatch = oldGone, oldWatch })
	return &dead
}

// runHookAsync runs a hook in the background; its result arrives on the channel. The test waits for it before its
// cleanups put the package variables back.
func runHookAsync(t *testing.T, n *Node, in map[string]any) <-chan any {
	t.Helper()
	raw, _ := json.Marshal(in)
	done := make(chan any, 1)
	finished := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(finished)
		out, _ := n.handleHook(ctx, sockRequest{Kind: "hook", Input: raw, Attempt: "a1", AgentPID: 4242, AgentStart: "linux:7"})
		done <- out
	}()
	t.Cleanup(func() { cancel(); <-finished })
	return done
}

// A claude killed while its hook waits on the phone leaves the hook running (Claude Code starts hooks in their own
// process group): the node sees the claude gone and withdraws the card instead of holding it until nodeclient.HookTimeout (#4344).
func TestHookCardEndsWithItsClaude(t *testing.T) {
	question := map[string]any{
		"hook_event_name": "PermissionRequest", "session_id": decidedSession, "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"questions": []any{map[string]any{"question": "Which?", "header": "Pick",
			"options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}}}},
	}
	permission := map[string]any{
		"hook_event_name": "PermissionRequest", "session_id": decidedSession, "tool_name": "Bash",
		"tool_input": map[string]any{"command": "make deploy"},
	}
	for name, in := range map[string]map[string]any{"question": question, "permission": permission} {
		t.Run(name, func(t *testing.T) {
			dead := fakeAgent(t)
			n := codexTestNode(t)
			h := withCancelHub(t, n)
			done := runHookAsync(t, n, in)
			waitFor(t, "the card", func() bool { asked, _ := h.counts(); return asked == 1 })

			// While the claude lives the card stays.
			select {
			case out := <-done:
				t.Fatalf("the hook stopped waiting while its claude lives: %v", out)
			case <-time.After(20 * agentWatch):
			}
			if _, cancelled := h.counts(); cancelled != 0 {
				t.Fatal("withdrawn while its claude lives")
			}

			dead.Store(true)
			select {
			case out := <-done:
				if out != nil {
					t.Fatalf("a gone claude's hook answered: %v", out)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("the hook still waits for the phone after its claude is gone")
			}
			waitFor(t, "the card to be withdrawn", func() bool { _, cancelled := h.counts(); return cancelled == 1 })
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.cancelled[0] != h.asked[0] {
				t.Fatalf("withdrew %s, asked %s", h.cancelled[0], h.asked[0])
			}
		})
	}
}

// watchFor runs watchAgent on req for 30 looks with gone deciding each look; true: it ended the wait on its own.
func watchFor(t *testing.T, req sockRequest, gone func(int, string) bool) bool {
	oldGone, oldWatch := agentGone, agentWatch
	agentGone, agentWatch = gone, 5*time.Millisecond
	defer func() { agentGone, agentWatch = oldGone, oldWatch }()
	ctx, stop := codexTestNode(t).watchAgent(context.Background(), req)
	time.Sleep(30 * agentWatch)
	ended := ctx.Err() != nil // before stop, which ends ctx itself
	stop()
	return ended
}

// One odd look (a /proc read that failed for another reason) does not end the wait; a hook whose claude was not found
// (no pid), or already gone when it asks, is not watched at all.
func TestHookWatchNeedsTwoMissesAndAClaude(t *testing.T) {
	var looks atomic.Int32
	claude := sockRequest{AgentPID: 4242, AgentStart: "linux:7"}
	if watchFor(t, claude, func(int, string) bool { return looks.Add(1)%3 == 2 }) || looks.Load() < 6 {
		t.Fatalf("one odd look in a row ended the wait (%d looks)", looks.Load())
	}
	for _, req := range []sockRequest{{AgentPID: 0}, claude} {
		if watchFor(t, req, func(int, string) bool { return true }) {
			t.Fatalf("pid %d: ended without a claude seen alive", req.AgentPID)
		}
	}
}
