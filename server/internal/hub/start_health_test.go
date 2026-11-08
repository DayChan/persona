package hub

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// startMachine is a machine "box" whose node answers session.start with answer (and anything else with nothing).
func startMachine(t *testing.T, h *Hub, answer func(p map[string]any) (any, error)) *Machine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		if method == "session.start" {
			return answer(p)
		}
		return map[string]any{}, nil
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: "box", Name: "示例电脑", Kind: "local"})
	m.peer, m.online = hubSide, true
	registerMachine(h, m)
	return m
}

// An app's start the node refuses because the new terminal tab's shell has no such launcher (#1642): the app gets the
// hub's sentence for it (409 launcher_missing, naming the computer and the shells, the shell's line as the detail),
// and the manager records the launcher as one that computer's terminal cannot run. A start with it that works clears it.
func TestAppStartThatTheTerminalCannotRunCountsForLauncherHealth(t *testing.T) {
	h := newTestHub(t)
	h.store = newMemStore()
	var mu sync.Mutex
	fail := true
	startMachine(t, h, func(p map[string]any) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		if !fail {
			return map[string]any{"session": proto.Session{SessionID: "new-1", Cwd: "/code"}}, nil
		}
		data, _ := json.Marshal(map[string]string{"delivered": "none", "launcher": "work", "missing": "work", "kind": "not_found",
			"where": "terminal", "pane_shell": "bash", "launcher_shell": "zsh", "rc_file": "~/.zshrc", "line": "bash: work: command not found"})
		return nil, &rpc.Error{Code: proto.CodeLauncherMissing, Message: `the new tab's shell (bash) has no command work: it printed "bash: work: command not found"`, Data: data}
	})
	connectAssistant(t, h)
	mux := h.routes()
	code, out := call(t, mux, "POST", "/api/v1/sessions", h.cfg.Token, `{"machine_id":"box","cwd":"/code","launcher":"work","prompt":"x"}`)
	text, _ := out["error"].(string)
	if code != 409 || out["code"] != proto.CodeLauncherMissing || out["action"] != "connect_model" || out["where"] != "terminal" ||
		!strings.Contains(text, "示例电脑 上新开的终端标签页（bash）里没有「work」") || !strings.Contains(text, "zsh（~/.zshrc）") ||
		!strings.Contains(out["detail"].(string), "bash: work: command not found") {
		t.Fatalf("start: %d %v", code, out)
	}
	health := func() map[string]any {
		_, res := call(t, mux, "GET", "/api/v1/launchers/health?machine=box", h.cfg.Token, "")
		recs, _ := res["records"].([]any)
		for _, r := range recs {
			if rec, _ := r.(map[string]any); rec["launcher"] == "work" {
				return rec
			}
		}
		return nil
	}
	eventually(t, 10*time.Second, "the failed start recorded", func() bool {
		r := health()
		return r != nil && r["kind"] == "not_in_terminal" && r["state"] == "blocked" && r["source"] == "start"
	})
	mu.Lock()
	fail = false
	mu.Unlock()
	if code, out := call(t, mux, "POST", "/api/v1/sessions", h.cfg.Token, `{"machine_id":"box","cwd":"/code","launcher":"work","prompt":"x"}`); code != 200 {
		t.Fatalf("start again: %d %v", code, out)
	}
	eventually(t, 10*time.Second, "the start that worked clears it", func() bool { r := health(); return r != nil && r["state"] == "ok" })
}

// A row keeps a coded refusal's start_error whole (the manager's sentence, its remedy last); any other start_error is
// clipped like the rest of a row's error lines.
func TestRowKeepsACodedStartErrorWhole(t *testing.T) {
	long := strings.Repeat("x", 300)
	if got := runtimeSummary(proto.Session{StartError: long, StartErrorCode: proto.CodeLauncherMissing}).StartError; got != long {
		t.Errorf("coded: %d runes kept", len([]rune(got)))
	}
	if got := runtimeSummary(proto.Session{StartError: long}).StartError; len([]rune(got)) != 180 {
		t.Errorf("plain: %d runes kept", len([]rune(got)))
	}
}

// A row says when a subscription's usage limit resets and whether the agent goes on by itself (#1710).
func TestRowKeepsTheUsageLimitReset(t *testing.T) {
	reset := time.Date(2026, 9, 25, 8, 20, 0, 0, time.UTC)
	row := runtimeSummary(proto.Session{TurnError: "You've hit your session limit", TurnErrorKind: proto.TurnErrorUsageLimit,
		TurnErrorResetsAt: &reset, TurnErrorResumes: true,
		Work: &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, ResetsAt: &reset}})
	if row.TurnErrorResetsAt == nil || !row.TurnErrorResetsAt.Equal(reset) || !row.TurnErrorResumes {
		t.Errorf("turn error: %+v %v", row.TurnErrorResetsAt, row.TurnErrorResumes)
	}
	if row.Work == nil || row.Work.ResetsAt == nil || !row.Work.ResetsAt.Equal(reset) {
		t.Errorf("work: %+v", row.Work)
	}
}
