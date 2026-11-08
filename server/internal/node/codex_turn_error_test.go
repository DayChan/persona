package node

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A Codex turn the model service failed writes no answer, only task_complete's error (Codex 0.166, measured): the
// pane's row says the turn failed and how, until the next turn starts (#2185).
func TestCodexPaneTurnError(t *testing.T) {
	n, _, _, _ := daemonPane(t)
	failed := append([]string{}, turnAfterTurn[:3]...)
	failed = append(failed, event("task_started"),
		`{"timestamp":"2026-01-01T00:00:02Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2","last_agent_message":null,`+
			`"error":{"message":"unexpected status 503 Service Unavailable: upstream overloaded","codex_error_info":{"http_connection_failed":{"http_status_code":503}}}}}`)
	at := time.Now()
	writeRollout(t, n.home, remoteRaw, n.home, at, at, failed...)
	s := paneRow(t, n, remoteRaw)
	if s.TurnError != "unexpected status 503 Service Unavailable: upstream overloaded" || s.TurnErrorKind != proto.TurnErrorTransient {
		t.Fatalf("failed turn: %q, %q", s.TurnError, s.TurnErrorKind)
	}
	// "continue" went in: a turn runs again, the error is gone. (A growing rollout is re-read at most every
	// metaMinInterval: forget the last read.)
	writeRollout(t, n.home, remoteRaw, n.home, at, at.Add(time.Second), append(failed, event("task_started"))...)
	forgetMetas(n)
	if s := paneRow(t, n, remoteRaw); s.TurnError != "" || s.TurnErrorKind != "" {
		t.Fatalf("next turn: %q, %q", s.TurnError, s.TurnErrorKind)
	}
	// A turn that ended well says nothing.
	writeRollout(t, n.home, remoteRaw, n.home, at, at.Add(2*time.Second), turnAfterTurn[:3]...)
	forgetMetas(n)
	if s := paneRow(t, n, remoteRaw); s.TurnError != "" {
		t.Fatalf("good turn: %q", s.TurnError)
	}
}

// Failed, then "continue" ran and ended well within metaMinInterval, before the rollout is re-read: the throttled read must
// not bring the old error back, which would have the hub continue a turn that went fine (#2185).
func TestCodexPaneStaleTurnError(t *testing.T) {
	n, _, _, _ := daemonPane(t)
	failed := append(append([]string{}, turnAfterTurn[:3]...), event("task_started"),
		`{"timestamp":"2026-01-01T00:00:02Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2",`+
			`"error":{"message":"unexpected status 503","codex_error_info":{"http_connection_failed":{"http_status_code":503}}}}}`)
	at := time.Now()
	writeRollout(t, n.home, remoteRaw, n.home, at, at, failed...)
	if s := paneRow(t, n, remoteRaw); s.TurnError == "" {
		t.Fatal("the failed turn says nothing")
	}
	retried := append(append([]string{}, failed...), event("task_started"),
		`{"timestamp":"2026-01-01T00:00:03Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"a2","content":[{"type":"Text","text":"测试都过了"}]}}}`,
		event("task_complete"))
	writeRollout(t, n.home, remoteRaw, n.home, at, at.Add(time.Second), retried...) // no forgetMetas: still throttled
	if s := paneRow(t, n, remoteRaw); s.TurnError != "" || s.TurnErrorKind != "" {
		t.Fatalf("a stale read brought the old error back: %q, %q", s.TurnError, s.TurnErrorKind)
	}
	// Once re-read, the rollout says the turn went fine.
	forgetMetas(n)
	if s := paneRow(t, n, remoteRaw); s.TurnError != "" {
		t.Fatalf("re-read: %q", s.TurnError)
	}
}

func forgetMetas(n *Node) {
	n.mu.Lock()
	clear(n.metas)
	n.mu.Unlock()
}

// An app-server's turn/completed with status failed carries the error; the user's Esc (interrupted) is none, and the
// next turn/started clears it (#2185).
func TestCodexRemoteTurnFailed(t *testing.T) {
	n, f, c := effortPane(t)
	joinPane(t, n)
	turn := func(id string) {
		f.push(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": id, "status": "inProgress"}}})
		waitFor(t, "turn "+id+" runs", func() bool { return threadOf(c, remoteRaw).turn == id })
	}
	end := func(id, status string, err any) {
		f.push(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": remoteRaw,
			"turn": map[string]any{"id": id, "status": status, "error": err}}})
		waitFor(t, "turn "+id+" ends", func() bool { return threadOf(c, remoteRaw).turn == "" })
	}

	turn("t1")
	end("t1", "failed", map[string]any{"message": "unexpected status 503", "codexErrorInfo": map[string]any{"httpConnectionFailed": map[string]any{"httpStatusCode": 503}}})
	if th := threadOf(c, remoteRaw); th.failure != "unexpected status 503" || th.failureKind != proto.TurnErrorTransient {
		t.Fatalf("failed: %q, %q", th.failure, th.failureKind)
	}
	turn("t2")
	if th := threadOf(c, remoteRaw); th.failure != "" {
		t.Fatalf("a new turn keeps the old failure: %q", th.failure)
	}
	end("t2", "failed", map[string]any{"message": "You've hit your usage limit.", "codexErrorInfo": "usageLimitExceeded"})
	if th := threadOf(c, remoteRaw); th.failureKind != proto.TurnErrorUsageLimit {
		t.Fatalf("usage limit: %q, %q", th.failure, th.failureKind)
	}
	turn("t3")
	end("t3", "failed", nil)
	if th := threadOf(c, remoteRaw); th.failure == "" {
		t.Fatal("failed with no error said nothing")
	}
	turn("t4")
	end("t4", "interrupted", map[string]any{"message": "interrupted"})
	if th := threadOf(c, remoteRaw); th.failure != "" {
		t.Fatalf("the user's Esc is a failure: %q", th.failure)
	}
	turn("t5")
	end("t5", "completed", nil)
	if th := threadOf(c, remoteRaw); th.failure != "" {
		t.Fatalf("completed: %q", th.failure)
	}
}

// A turn that began before Persona joined (a resume started with its message): its turn/started never came, so its
// turn/completed ends it all the same, and clears the reply's live text on the hub, or every page opened later shows
// the reply twice (#5598).
func TestCodexRemoteTurnBegunBeforeJoining(t *testing.T) {
	n, f, c := effortPane(t)
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	var mu sync.Mutex
	var deltas []string
	hub.OnEvent = func(event string, params json.RawMessage) {
		var p struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		if event == "session.delta" && json.Unmarshal(params, &p) == nil && p.SessionID == remoteRaw {
			mu.Lock()
			deltas = append(deltas, p.Text)
			mu.Unlock()
		}
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
	last := func() (string, int) {
		mu.Lock()
		defer mu.Unlock()
		if len(deltas) == 0 {
			return "", 0
		}
		return deltas[len(deltas)-1], len(deltas)
	}

	joinPane(t, n)
	if th := threadOf(c, remoteRaw); th.turn != "" {
		t.Fatalf("a turn known before any began: %q", th.turn)
	}
	f.push(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": remoteRaw,
		"item": map[string]any{"id": "m1", "type": "agentMessage", "text": "done"}}})
	waitFor(t, "the reply's live text", func() bool { text, _ := last(); return text == "done" })
	_, before := last()
	f.push(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": remoteRaw,
		"turn": map[string]any{"id": "t-before", "status": "completed"}}})
	waitFor(t, "the live text cleared", func() bool { text, n := last(); return n > before && text == "" })
	if th := threadOf(c, remoteRaw); th.status != proto.StatusDone {
		t.Errorf("status after the turn = %q", th.status)
	}

	// Its end arriving again does not end anything again: a failure it left stays.
	if th := threadOf(c, remoteRaw); th.ended != "t-before" {
		t.Fatalf("ended = %q", th.ended)
	}
	f.push(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": "t-fail", "status": "inProgress"}}})
	waitFor(t, "turn t-fail runs", func() bool { return threadOf(c, remoteRaw).turn == "t-fail" })
	f.push(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": remoteRaw,
		"turn": map[string]any{"id": "t-fail", "status": "failed", "error": map[string]any{"message": "boom"}}}})
	waitFor(t, "t-fail ends", func() bool { return threadOf(c, remoteRaw).failure == "boom" })
	f.push(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": "t-fail", "status": "interrupted"}}})
	f.push(map[string]any{"method": "thread/settings/updated", "params": map[string]any{"threadId": remoteRaw, "threadSettings": map[string]any{"model": "sync-1"}}})
	waitFor(t, "events read", func() bool { return threadOf(c, remoteRaw).model == "sync-1" })
	if th := threadOf(c, remoteRaw); th.failure != "boom" {
		t.Errorf("its end again cleared the failure: %q", th.failure)
	}

	// A known turn still ends only on its own turn/completed.
	f.push(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": "t1", "status": "inProgress"}}})
	waitFor(t, "turn t1 runs", func() bool { return threadOf(c, remoteRaw).turn == "t1" })
	f.push(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": "t0"}}})
	f.push(map[string]any{"method": "thread/settings/updated", "params": map[string]any{"threadId": remoteRaw, "threadSettings": map[string]any{"model": "sync-marker"}}})
	waitFor(t, "events read", func() bool { return threadOf(c, remoteRaw).model == "sync-marker" })
	if th := threadOf(c, remoteRaw); th.turn != "t1" || th.status != proto.StatusWorking {
		t.Fatalf("another turn's end ended t1: turn %q status %q", th.turn, th.status)
	}
}
