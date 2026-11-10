package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// botmux next to the engine (hostbotmux.go), driven through the node's requests against a fake botmux CLI and a fake
// dashboard.

const (
	bmxCwd = "/tmp/bmxproj"
	bmxApp = "cli_test_app"
)

// fakeBotmux is the botmux CLI (observe, dashboard current) and its dashboard's /api/trigger.
type fakeBotmux struct {
	mu       sync.Mutex
	rows     []map[string]any
	offline  bool  // no daemon answers
	fail     error // observe fails
	observes int
	block    chan struct{} // observe waits on it (nil: answers at once)
	token    string        // the dashboard's current token
	told     string        // the token `dashboard current` prints
	status   int           // the trigger's reply status (0: 200)
	triggers []map[string]any
	srv      *httptest.Server

	herdr *fakeHerdr // botmuxNode's engine, and its home
	home  string
}

func newFakeBotmux(t *testing.T) *fakeBotmux {
	f := &fakeBotmux{token: "tok1", told: "tok1"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/api/trigger" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if c, err := r.Cookie("botmux_dashboard_token"); err != nil || c.Value != f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		f.triggers = append(f.triggers, body)
		if f.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			fmt.Fprint(w, `{"ok":false,"error":"session_not_found"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"action":"delivered"}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBotmux) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch strings.Join(args, " ") {
	case "observe --include-raw":
		f.observes++
		if b := f.block; b != nil { // an observe in flight until the test lets it answer
			f.mu.Unlock()
			<-b
			f.mu.Lock()
		}
		if f.fail != nil {
			return nil, f.fail
		}
		status := "ok"
		if f.offline {
			status = "daemon_offline"
		}
		return json.Marshal(map[string]any{"schemaVersion": 1, "daemons": []any{
			map[string]any{"larkAppId": bmxApp, "probe": map[string]any{"status": status}, "sessions": f.rows},
		}})
	case "dashboard current":
		port := f.srv.URL[strings.LastIndex(f.srv.URL, ":")+1:]
		return []byte("http://192.0.2.1:" + port + "/?t=" + f.told + "\n工作台: http://192.0.2.1:" + port +
			"/workbench?t=" + f.told + "\n⚠️ 安全提示\n"), nil
	}
	return nil, fmt.Errorf("unexpected botmux %v", args)
}

func (f *fakeBotmux) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.triggers
}

// bmxRow is one session as botmux observe reports it.
func bmxRow(id, cli, liveness, turn string, prompt bool, title string) map[string]any {
	return map[string]any{
		"identity":         map[string]any{"sessionId": id, "larkAppId": bmxApp},
		"cli":              map[string]any{"id": cli},
		"backend":          map[string]any{"type": "tmux", "sessionName": "bmx-" + id[:8]},
		"liveness":         liveness,
		"turn":             turn,
		"closed":           liveness == "closed",
		"tuiPromptActive":  prompt,
		"lastActivityAt":   time.Now().Add(-time.Hour).UnixMilli(),
		"workingDirectory": bmxCwd,
		"raw":              map[string]any{"title": title},
	}
}

// bmxNode is a Node whose engine is herdr (not registered, so a call to it shows in the fake herdr's trace) with
// botmux next to it.
func bmxNode(t *testing.T) (*Node, *fakeBotmux, *fakeHerdr, string) {
	t.Helper()
	n, fh, home := herdrNode(t)
	f := newFakeBotmux(t)
	n.hosts = host.NewRegistry(&botmuxHost{run: f.run, client: f.srv.Client()})
	withSubmitGrace(t, time.Millisecond)
	return n, f, fh, home
}

const (
	sidD = "0d000000-0000-4000-8000-00000000000d"
	sidE = "0d000000-0000-4000-8000-00000000000e"
	sidF = "0d000000-0000-4000-8000-00000000000f"
	sidG = "0d000000-0000-4000-8000-000000000010"
)

// Claude Code sessions are listed with botmux's state, in a space per working directory; closed sessions, other CLIs,
// sessions botmux runs in herdr and long-parked ones are not.
func TestBotmuxListsClaudeSessions(t *testing.T) {
	n, f, _, home := bmxNode(t)
	attention := bmxRow(sidC, "claude-code", "alive", "idle", false, "needs a decision")
	attention["raw"] = map[string]any{"title": "needs a decision", "agentAttention": map[string]any{"kind": "blocked"}}
	inHerdr := bmxRow(sidF, "claude-code", "alive", "working", false, "in herdr")
	inHerdr["backend"] = map[string]any{"type": "herdr"}
	oldParked := bmxRow(sidG, "claude-code", "not_running", "unknown", false, "old")
	oldParked["lastActivityAt"] = time.Now().Add(-2 * offlineWindow).UnixMilli()
	f.rows = []map[string]any{
		bmxRow(sidA, "claude-code", "alive", "working", false, "1103 | 功能 | botmux\n接入"),
		bmxRow(sidB, "claude-code", "alive", "idle", true, "asks"),
		attention,
		bmxRow(sidD, "claude-code", "alive", "idle", false, "done"),
		bmxRow(sidE, "claude-code", "not_running", "unknown", false, "parked"),
		bmxRow("0d000000-0000-4000-8000-000000000011", "claude-code", "closed", "unknown", false, "closed"),
		bmxRow("0d000000-0000-4000-8000-000000000012", "traex", "alive", "working", false, "traex"),
		inHerdr, oldParked,
	}
	for _, id := range []string{sidA, sidB, sidC, sidD, sidE} {
		writeTranscript(t, home, bmxCwd, id, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	}
	m := byID(listSessions(t, n))
	want := map[string]string{sidA: proto.StatusWorking, sidB: proto.StatusBlocked, sidC: proto.StatusBlocked,
		sidD: proto.StatusDone, sidE: proto.StatusOffline}
	for id, status := range want {
		s, ok := m[id]
		if !ok {
			t.Fatalf("%s not listed: %v", id, ids(listSessions(t, n)))
		}
		if s.Status != status || s.PaneID != "botmux:"+id || s.SpaceKind != host.Botmux || s.SpaceID != dirSpace+bmxCwd ||
			s.SpaceName != "bmxproj" || s.SlotID != "" {
			t.Errorf("%s: %+v, want status %s", id, s, status)
		}
	}
	if a := m[sidA]; a.AgentName != "1103 | 功能 | botmux 接入" {
		t.Errorf("name %q", a.AgentName)
	}
	for _, id := range []string{sidF, sidG} {
		if s, ok := m[id]; ok && s.PaneID != "" {
			t.Errorf("%s listed with pane %q", id, s.PaneID)
		}
	}
	if len(m) != len(want) {
		t.Errorf("listed %v", ids(listSessions(t, n)))
	}
}

// A session botmux raised attention on says why, and each session links to its Feishu topic when botmux gives one
// (an https link only); a session that is not waiting for the user carries no reason.
func TestBotmuxAttentionReasonAndTopicLink(t *testing.T) {
	n, f, _, home := bmxNode(t)
	const topic = "https://applink.example.com/client/thread/open?chat=c1&thread=t1"
	blocked := bmxRow(sidA, "claude-code", "alive", "idle", false, "stuck")
	blocked["raw"] = map[string]any{"title": "stuck", "feishuThreadLink": topic,
		"agentAttention": map[string]any{"kind": "decision", "reason": "要合并到哪个分支，\n 请在话题里回复。"}}
	working := bmxRow(sidB, "claude-code", "alive", "working", false, "busy")
	working["raw"] = map[string]any{"title": "busy", "feishuThreadLink": "javascript:alert(1)"}
	prompt := bmxRow(sidC, "claude-code", "alive", "idle", true, "asks")
	f.rows = []map[string]any{blocked, working, prompt}
	for _, id := range []string{sidA, sidB, sidC} {
		writeTranscript(t, home, bmxCwd, id, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	}
	m := byID(listSessions(t, n))
	if s := m[sidA]; s.Status != proto.StatusBlocked || s.AttentionReason != "要合并到哪个分支， 请在话题里回复。" || s.ExternalURL != topic {
		t.Errorf("blocked: status %s reason %q url %q", s.Status, s.AttentionReason, s.ExternalURL)
	}
	if s := m[sidB]; s.AttentionReason != "" || s.ExternalURL != "" {
		t.Errorf("working: reason %q url %q", s.AttentionReason, s.ExternalURL)
	}
	if s := m[sidC]; s.Status != proto.StatusBlocked || s.AttentionReason != "" {
		t.Errorf("tui prompt: status %s reason %q", s.Status, s.AttentionReason)
	}
}

// A session's last words are the reply botmux posted in the topic (a card's title without botmux's label): its
// transcript often ends on botmux's sentinel only. A turn under way, a message botmux has not answered yet, a session
// waiting for the user, and one whose transcript has words well after the reply show the transcript's words.
func TestBotmuxLastTextIsTheTopicReply(t *testing.T) {
	n, f, _, home := bmxNode(t)
	replyAt := time.Now().Add(-time.Hour)
	replied := func(id, liveness, turn, state string) map[string]any {
		r := bmxRow(id, "claude-code", liveness, turn, false, "t")
		r["raw"] = map[string]any{"title": "t", "previewBotState": state, "previewBotText": "结论：已修好 …",
			"previewBotFullText": "结论：已修好\n测试 9 个通过", "previewBotAt": replyAt.UnixMilli()}
		return r
	}
	const sidH, sidI = "0d000000-0000-4000-8000-000000000013", "0d000000-0000-4000-8000-000000000014"
	short := replied(sidE, "not_running", "unknown", "replied")
	short["raw"].(map[string]any)["previewBotFullText"] = ""
	blocked := replied(sidF, "alive", "idle", "replied")
	blocked["tuiPromptActive"] = true
	card := replied(sidG, "not_running", "unknown", "replied")
	card["raw"].(map[string]any)["previewBotFullText"] = "[卡片: 部署完成]\n**结论**：三台都在线"
	f.rows = []map[string]any{
		replied(sidA, "alive", "idle", "replied"),
		replied(sidB, "alive", "working", "replied"),
		replied(sidC, "alive", "idle", "waiting"),
		bmxRow(sidD, "claude-code", "alive", "idle", false, "no reply"),
		short, blocked, card,
		replied(sidH, "not_running", "unknown", "replied"),
		replied(sidI, "not_running", "unknown", "replied"),
	}
	text := func(at time.Time, s string) string {
		return `{"type":"assistant","timestamp":"` + at.UTC().Format(time.RFC3339) +
			`","message":{"role":"assistant","content":[{"type":"text","text":"` + s + `"}]}}`
	}
	for _, id := range []string{sidA, sidB, sidC, sidD, sidE, sidF, sidG, sidH, sidI} {
		at := replyAt.Add(time.Minute) // the sentinel closing the turn of the reply
		lines := []string{`{"type":"user","message":{"role":"user","content":"hi"}}`, text(at, "已发到话题"),
			text(at, "BOTMUX_NOTHING_TO_SEND")}
		switch id {
		case sidH: // answered later outside botmux (a resume in a terminal)
			lines = append(lines, text(time.Now().Add(-time.Minute), "在终端里接着做完了"))
		case sidI: // within the slack of the reply: the same turn
			lines = append(lines, text(replyAt.Add(5*time.Minute), "收尾"))
		}
		writeTranscript(t, home, bmxCwd, id, time.Now().Add(-time.Minute), lines...)
	}
	m := byID(listSessions(t, n))
	want := map[string]string{sidA: "结论：已修好 测试 9 个通过", sidB: "已发到话题", sidC: "已发到话题", sidD: "已发到话题",
		sidE: "结论：已修好 …", sidF: "已发到话题", sidG: "部署完成 **结论**：三台都在线", sidH: "在终端里接着做完了",
		sidI: "结论：已修好 测试 9 个通过"}
	for id, text := range want {
		if got := m[id].LastText; got != text {
			t.Errorf("%s: last text %q, want %q", id, got, text)
		}
	}
}

// One observe answers the refreshes of a moment; botmux not running is no sessions, not a failure.
func TestBotmuxObserveIsReusedAndOfflineIsEmpty(t *testing.T) {
	_, f, _, _ := bmxNode(t)
	h := &botmuxHost{run: f.run}
	for range 3 {
		if _, err := h.Agents(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.observes != 1 {
		t.Fatalf("observed %d times", f.observes)
	}
	h.at = time.Now().Add(-botmuxReuse)
	if _, err := h.Agents(context.Background()); err != nil || f.observes != 2 {
		t.Fatalf("after %s: observed %d times, %v", botmuxReuse, f.observes, err)
	}
	f.offline = true
	_, err := (&botmuxHost{run: f.run}).Agents(context.Background())
	if !errors.Is(err, host.ErrNotRunning) {
		t.Fatalf("offline: %v", err)
	}
}

// A botmux from before `observe` prints its usage and exits 0: that is botmux not running here, saying to upgrade, not
// a parse error (#3023). Valid JSON of another schema is still an error.
func TestBotmuxTooOldIsNotRunning(t *testing.T) {
	usage := []byte("\nbotmux v3.13.0 — IM ↔ AI 编程 CLI 桥接\n\n命令:\n  setup  交互式配置\n")
	h := &botmuxHost{run: func(context.Context, ...string) ([]byte, error) { return usage, nil }}
	_, err := h.Agents(context.Background())
	if !errors.Is(err, host.ErrNotRunning) || !strings.Contains(err.Error(), "botmux upgrade") {
		t.Fatalf("old botmux: %v", err)
	}
	h = &botmuxHost{run: func(context.Context, ...string) ([]byte, error) { return []byte(`{"schemaVersion":2}`), nil }}
	_, err = h.Agents(context.Background())
	if err == nil || errors.Is(err, host.ErrNotRunning) || !strings.Contains(err.Error(), "schema 2") {
		t.Fatalf("other schema: %v", err)
	}
}

// A botmux without `observe` is not run again every 5 seconds: its answer holds until the CLI file changes (an
// upgrade) or botmuxTooOldReuse passes (#5337). Without a file to check, it is asked again as before.
func TestBotmuxTooOldIsReused(t *testing.T) {
	usage := []byte("\nbotmux v3.13.0 — IM ↔ AI 编程 CLI 桥接\n\n命令:\n  setup  交互式配置\n")
	bin := filepath.Join(t.TempDir(), "botmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runs := 0
	out := usage
	h := &botmuxHost{bin: bin, run: func(context.Context, ...string) ([]byte, error) { runs++; return out, nil }}
	look := func(want int, why string) {
		t.Helper()
		_, err := h.Agents(context.Background())
		if runs != want {
			t.Fatalf("%s: %d runs, want %d", why, runs, want)
		}
		if !errors.Is(err, host.ErrNotRunning) {
			t.Fatalf("%s: %v", why, err)
		}
	}
	look(1, "first look")
	h.at = time.Now().Add(-botmuxReuse)
	look(1, "after 5 seconds, same CLI")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n# upgraded\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	look(2, "the CLI file changed")
	h.at = time.Now().Add(-botmuxTooOldReuse)
	look(3, "after the upper bound")
	h.at = time.Time{} // a delivered message: look again at once
	look(4, "asked to look again")

	out = []byte(`{"schemaVersion":1,"daemons":[{"probe":{"status":"ok"},"sessions":[]}]}`)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n# observes now\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.at = time.Now().Add(-botmuxReuse)
	if _, err := h.Agents(context.Background()); err != nil || runs != 5 {
		t.Fatalf("upgraded: %v, %d runs", err, runs)
	}
	h.at = time.Now().Add(-botmuxReuse)
	if _, err := h.Agents(context.Background()); err != nil || runs != 6 {
		t.Fatalf("a botmux that observes is asked every 5 seconds: %v, %d runs", err, runs)
	}

	runs, out = 0, usage
	h = &botmuxHost{run: func(context.Context, ...string) ([]byte, error) { runs++; return out, nil }}
	look(1, "no CLI file")
	h.at = time.Now().Add(-botmuxReuse)
	look(2, "no CLI file to check: asked again after 5 seconds")
}

// A message goes to botmux as a turn of the session — a running one and a parked one alike (botmux resumes it; no
// claude --resume in herdr) — with the dashboard's token, asked of botmux again once it was rotated.
func TestBotmuxReplyIsDeliveredByBotmux(t *testing.T) {
	n, f, fh, home := bmxNode(t)
	f.rows = []map[string]any{
		bmxRow(sidA, "claude-code", "alive", "working", false, "a"),
		bmxRow(sidE, "claude-code", "not_running", "unknown", false, "parked"),
	}
	for _, id := range []string{sidA, sidE} {
		writeTranscript(t, home, bmxCwd, id, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	}
	listSessions(t, n)
	if err := n.send(context.Background(), sidA, "继续", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.token, f.told = "tok2", "tok2" // rotated: the cached token is refused once
	f.mu.Unlock()
	if err := n.send(context.Background(), sidE, "接着做", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	got := f.sent()
	if len(got) != 2 {
		t.Fatalf("triggers %v", got)
	}
	for i, want := range []struct{ id, text string }{{sidA, "继续"}, {sidE, "接着做"}} {
		target, _ := got[i]["target"].(map[string]any)
		env, _ := got[i]["envelope"].(map[string]any)
		if got[i]["instruction"] != want.text || target["kind"] != "turn" || target["sessionId"] != want.id ||
			target["botId"] != bmxApp || env["trusted"] != false || got[i]["options"] != nil {
			t.Errorf("trigger %d: %v", i, got[i])
		}
	}
	if trace := fh.calls(); len(trace) != 0 {
		t.Errorf("herdr was asked: %v", trace)
	}
}

// botmux refusing a message delivered nothing: the phone may send it again. Keys and screens are not botmux's to give.
func TestBotmuxRefusalsAndUnsupported(t *testing.T) {
	n, f, _, home := bmxNode(t)
	f.rows = []map[string]any{bmxRow(sidA, "claude-code", "alive", "idle", false, "a")}
	writeTranscript(t, home, bmxCwd, sidA, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	listSessions(t, n)
	f.status = http.StatusNotFound
	if err := n.send(context.Background(), sidA, "x", nil, sendOpts{}); refusalCode(err) != notDelivered {
		t.Fatalf("refused send = %v", err)
	}
	if err := n.keys(context.Background(), sidA, []string{"esc"}, ""); !errors.Is(err, host.ErrUnsupported) {
		t.Fatalf("keys = %v", err)
	}
	if err := n.keys(context.Background(), sidA, []string{"down", "enter"}, "x"); refusalCode(err) != cannotType {
		t.Fatalf("typing = %v", err)
	}
	if _, _, err := n.screen(context.Background(), sidA, 40, "visible", false); !errors.Is(err, host.ErrUnsupported) {
		t.Fatalf("screen = %v", err)
	}
}

// botmux is registered next to the engine's hosts only when it is installed and wanted.
func TestNodeHostsAddBotmuxWhenInstalled(t *testing.T) {
	old := botmuxBin
	t.Cleanup(func() { botmuxBin = old })
	botmuxBin = func() string { return "" }
	if hs := nodeHosts(proto.EngineHerdr, true, nil); len(hs) != 2 || hs[1].Kind() != host.Tmux {
		t.Fatalf("without botmux: %d hosts", len(hs))
	}
	botmuxBin = func() string { return "/opt/botmux/bin/botmux" }
	hs := nodeHosts(proto.EngineHerdrBoth, true, nil)
	if len(hs) != 4 || hs[0].Kind() != host.Herdr || hs[2].Kind() != host.Tmux || hs[3].Kind() != host.Botmux {
		t.Fatalf("with botmux: %v", hs)
	}
	if hs := nodeHosts(proto.EngineHerdrBoth, false, nil); len(hs) != 3 {
		t.Fatalf("botmux not wanted: %v", hs)
	}
	if defaultHostKind(proto.EngineHerdrBoth) != host.Herdr {
		t.Fatal("new sessions must not go to botmux")
	}
	if ref := host.ParsePane("botmux:" + sidA); ref.Kind != host.Botmux || ref.ID != sidA {
		t.Fatalf("pane ref %+v", ref)
	}
}

// An observe that fails (a timeout, a CLI error) is not botmux stopping: the sessions stay as listed, and a reply in
// the gap still goes to botmux.
func TestBotmuxFailedObserveKeepsSessions(t *testing.T) {
	n, f, _, home := bmxNode(t)
	f.rows = []map[string]any{bmxRow(sidA, "claude-code", "alive", "working", false, "a")}
	writeTranscript(t, home, bmxCwd, sidA, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	listSessions(t, n)
	h := n.hosts.Get(host.Botmux).(*botmuxHost)
	f.fail = errors.New("signal: killed")
	h.at = time.Time{}
	if s := byID(listSessions(t, n))[sidA]; s.PaneID != "botmux:"+sidA || s.Status != proto.StatusWorking {
		t.Fatalf("after a failed observe: %+v", s)
	}
	_, err := h.Agents(context.Background())
	if err == nil || errors.Is(err, host.ErrNotRunning) {
		t.Fatalf("a failed observe = %v, want an error that is not ErrNotRunning", err)
	}
}

// A session parked for longer than the transcripts listed offline is not listed, but a reply to it still goes to
// botmux (which resumes it), not to a claude --resume in herdr.
func TestBotmuxReplyToLongParkedSession(t *testing.T) {
	n, f, fh, home := bmxNode(t)
	old := bmxRow(sidE, "claude-code", "not_running", "unknown", false, "old")
	old["lastActivityAt"] = time.Now().Add(-2 * offlineWindow).UnixMilli()
	f.rows = []map[string]any{old}
	writeTranscript(t, home, bmxCwd, sidE, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	if s := byID(listSessions(t, n))[sidE]; s.PaneID != "" {
		t.Fatalf("listed with a pane: %+v", s)
	}
	if err := n.send(context.Background(), sidE, "接着做", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := f.sent(); len(got) != 1 {
		t.Fatalf("triggers %v", got)
	}
	if trace := fh.calls(); len(trace) != 0 {
		t.Errorf("herdr was asked: %v", trace)
	}
}

// A failure after botmux took the message may have delivered it (no "nothing typed" refusal, the phone must not
// resend blindly); a dashboard that moved to another port is asked for again.
func TestBotmuxUncertainDeliveryAndMovedDashboard(t *testing.T) {
	n, f, _, home := bmxNode(t)
	f.rows = []map[string]any{bmxRow(sidA, "claude-code", "alive", "idle", false, "a")}
	writeTranscript(t, home, bmxCwd, sidA, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	listSessions(t, n)
	h := n.hosts.Get(host.Botmux).(*botmuxHost)
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		f.mu.Lock()
		f.status = status
		f.mu.Unlock()
		err := h.Prompt(context.Background(), host.PaneRef{Kind: host.Botmux, ID: sidA}, "x")
		if err == nil || errors.Is(err, host.ErrNotDelivered) != (status == http.StatusServiceUnavailable) {
			t.Errorf("%d: %v", status, err)
		}
	}
	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	h.mu.Lock()
	h.base = "http://127.0.0.1:1" // where the dashboard was: nothing listens there now
	h.mu.Unlock()
	if err := h.Prompt(context.Background(), host.PaneRef{Kind: host.Botmux, ID: sidA}, "y"); err != nil {
		t.Fatalf("after the dashboard moved: %v", err)
	}
}

// The same session in a herdr pane and as botmux's parked row: the pane that runs it wins, whichever comes first.
func TestRunningPaneWinsOverParkedBotmuxRow(t *testing.T) {
	parked := host.Agent{Agent: "claude", AgentSession: &host.AgentSession{Kind: "id", Value: sidA},
		AgentStatus: proto.StatusOffline, PaneID: "botmux:" + sidA}
	running := host.Agent{Agent: "claude", AgentSession: &host.AgentSession{Kind: "id", Value: sidA},
		AgentStatus: proto.StatusWorking, PaneID: "w1:p1"}
	for _, agents := range [][]host.Agent{{parked, running}, {running, parked}} {
		n, _, _ := herdrNode(t)
		n.setAgents(agents)
		if a, _ := n.agent(sidA); a.PaneID != "w1:p1" {
			t.Fatalf("%v: pane %q", agents, a.PaneID)
		}
	}
}

// The resident node (launchd) has only the system's PATH: botmux is found at the launcher its installer writes.
func TestBotmuxFoundOutsidePATH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("PATH", t.TempDir())
	if bin := findBotmux(); bin != "" {
		t.Fatalf("found %q without botmux", bin)
	}
	dir := filepath.Join(home, ".botmux", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "botmux")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bin := findBotmux(); bin != launcher {
		t.Fatalf("findBotmux = %q, want %q", bin, launcher)
	}
}

// botmuxNode is a Node before the hub's word on botmux, as New makes it (no botmux host), with botmux installed: a
// fake CLI that counts its observes and has one running session.
func botmuxNode(t *testing.T) (*Node, *fakeBotmux) {
	t.Helper()
	n, hf, home := herdrNode(t)
	// An engine switch makes its hosts from the environment: the fake herdr, and no herdr-core, not this computer's.
	t.Setenv("HERDR_SOCKET_PATH", hf.sock)
	t.Setenv("HC_SOCKET", filepath.Join(t.TempDir(), "hc.sock"))
	f := newFakeBotmux(t)
	f.herdr, f.home = hf, home
	stubBotmux(t, f)
	// Runs before stubBotmux puts botmuxBin back: an engine switch's list, still being built, reads it (publish).
	t.Cleanup(n.engLists.Wait)
	f.rows = []map[string]any{bmxRow(sidA, "claude-code", "alive", "working", false, "a")}
	writeTranscript(t, home, bmxCwd, sidA, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	return n, f
}

// stubBotmux: botmux is installed, and the botmux host the node makes runs the fake CLI.
func stubBotmux(t *testing.T, f *fakeBotmux) {
	t.Helper()
	oldBin, oldNew := botmuxBin, newBotmux
	t.Cleanup(func() { botmuxBin, newBotmux = oldBin, oldNew })
	botmuxBin = func() string { return "/opt/botmux/bin/botmux" }
	newBotmux = func(string, func(string, ...any)) host.Host { return &botmuxHost{run: f.run, client: f.srv.Client()} }
}

func configureNode(t *testing.T, n *Node, params map[string]any) {
	t.Helper()
	if _, err := request(t, n, "node.configure", params); err != nil {
		t.Fatal(err)
	}
}

// botmuxListed: the session is listed on its botmux pane (its transcript alone lists it offline, without a pane).
func botmuxListed(t *testing.T, n *Node, id string) bool {
	t.Helper()
	s, ok := byID(listSessions(t, n))[id]
	return ok && s.PaneID == "botmux:"+id
}

// A new node does not register botmux, installed or not: it waits for the hub's word (useBotmux).
func TestNewNodeWaitsForTheHubOnBotmux(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", os.Getenv("PATH")) // New puts ~/.local/bin and the like in front
	stubBotmux(t, newFakeBotmux(t))
	n, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	if kinds := hostKinds(n); slices.Contains(kinds, host.Botmux) {
		t.Fatalf("botmux registered before the hub said: %v", kinds)
	}
}

// Turned off for the machine (hub.json machines[].botmux false), botmux is no host: no observe and no sessions, and a
// node.configure that says nothing of botmux (only keep_sessions, only claude_dirs) leaves it off; so does an engine
// switch, before and after the hub's word.
func TestBotmuxOffRunsNoBotmux(t *testing.T) {
	n, f := botmuxNode(t)
	if err := n.useEngine(context.Background(), proto.EngineHerdrBoth); err != nil {
		t.Fatal(err)
	}
	if kinds := hostKinds(n); slices.Contains(kinds, host.Botmux) {
		t.Fatalf("engine.use before the hub's word registered botmux: %v", kinds)
	}
	configureNode(t, n, map[string]any{"botmux": false})
	configureNode(t, n, map[string]any{"keep_sessions": []string{}})
	configureNode(t, n, map[string]any{"claude_dirs": []string{}})
	if err := n.useEngine(context.Background(), proto.EngineHerdr); err != nil {
		t.Fatal(err)
	}
	if kinds := hostKinds(n); slices.Contains(kinds, host.Botmux) {
		t.Fatalf("botmux registered while off: %v", kinds)
	}
	if botmuxListed(t, n, sidA) || f.observes != 0 {
		t.Fatalf("off: observed %d times", f.observes)
	}
}

// Turned on, botmux's sessions are listed (an engine switch keeps them); turned off again they leave the list and
// botmux is not asked any more; on again, they come back.
func TestBotmuxComesAndGoesWithTheHubsWord(t *testing.T) {
	n, f := botmuxNode(t)
	configureNode(t, n, map[string]any{"botmux": true})
	if err := n.useEngine(context.Background(), proto.EngineHerdrBoth); err != nil {
		t.Fatal(err)
	}
	if !botmuxListed(t, n, sidA) {
		t.Fatalf("on: %v", ids(listSessions(t, n)))
	}
	configureNode(t, n, map[string]any{"botmux": false})
	seen := f.observes
	if kinds := hostKinds(n); slices.Contains(kinds, host.Botmux) || botmuxListed(t, n, sidA) || f.observes != seen {
		t.Fatalf("off again: hosts %v, observed %d → %d", kinds, seen, f.observes)
	}
	configureNode(t, n, map[string]any{"botmux": true})
	if !botmuxListed(t, n, sidA) {
		t.Fatalf("on again: %v", ids(listSessions(t, n)))
	}
}

// A hub that lists sessions without having said (older than the setting) gets botmux's sessions as before; one that
// said off first keeps it off.
func TestBotmuxListedWhenTheHubListsWithoutSaying(t *testing.T) {
	n, _ := botmuxNode(t)
	configureNode(t, n, map[string]any{"keep_sessions": []string{}})
	if !botmuxListed(t, n, sidA) {
		t.Fatalf("an older hub: %v", ids(listSessions(t, n)))
	}
	off, f := botmuxNode(t)
	configureNode(t, off, map[string]any{"botmux": false})
	if botmuxListed(t, off, sidA) || f.observes != 0 {
		t.Fatalf("listed after the hub said off: observed %d times", f.observes)
	}
}

// Turned off while a refresh is still asking botmux, the refresh's answer is dropped: the sessions do not come back.
func TestBotmuxOffDuringARefresh(t *testing.T) {
	n, f := botmuxNode(t)
	configureNode(t, n, map[string]any{"botmux": true})
	block := make(chan struct{})
	f.mu.Lock()
	f.block = block
	f.mu.Unlock()
	done := make(chan struct{})
	go func() { _ = n.refreshHosts(context.Background()); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		f.mu.Lock()
		asked := f.observes
		f.mu.Unlock()
		if asked > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the refresh never asked botmux")
		}
	}
	configureNode(t, n, map[string]any{"botmux": false})
	close(block)
	<-done
	if botmuxListed(t, n, sidA) {
		t.Fatal("a refresh from before off brought botmux's session back")
	}
}

// Turned off, the next list the node pushes (its poll, every second) is without botmux's sessions.
func TestBotmuxOffTellsTheHub(t *testing.T) {
	n, _ := botmuxNode(t)
	configureNode(t, n, map[string]any{"botmux": true})
	pushes, _ := pollNode(t, n)
	awaitPush(t, pushes, listsBotmux(true), "the hub was not told the session while on")
	configureNode(t, n, map[string]any{"botmux": false})
	awaitPush(t, pushes, listsBotmux(false), "no session list without botmux's session went to the hub")
}

// Where the engine does not answer (herdr not running), turning botmux off still takes its sessions off the list: a
// refresh nobody answers keeps the agents as they were.
func TestBotmuxOffWithTheEngineDown(t *testing.T) {
	n, _ := botmuxNode(t)
	n.hosts = herdrHosts(&herdr.Client{Socket: filepath.Join(t.TempDir(), "gone.sock")})
	configureNode(t, n, map[string]any{"botmux": true})
	pushes, _ := pollNode(t, n)
	awaitPush(t, pushes, listsBotmux(true), "the hub was not told the session while on")
	configureNode(t, n, map[string]any{"botmux": false})
	awaitPush(t, pushes, listsBotmux(false), "with herdr down, botmux's session stayed on the hub's list")
}

// Where botmux is installed, no list goes to the hub before its word on botmux: one without botmux's sessions would
// take them off the hub's list for those seconds, and their coming back would read as new turns.
func TestNoListGoesBeforeTheHubsWordOnBotmux(t *testing.T) {
	n, f := botmuxNode(t)
	// herdr runs an agent of its own: a list with it would go at the first tick, were lists not held.
	cwd := "/tmp/poll"
	writeTranscript(t, f.home, cwd, sidB, time.Now(),
		`{"type":"user","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp/poll","message":{"role":"user","content":"hello"}}`)
	f.herdr.reply("session.snapshot", snapshot(nil, nil,
		[]any{claudeAgent(sidB, proto.StatusIdle, "w5:p1", "w5:t1", "w5", cwd, "poll")}, nil))
	pushes, _ := pollNode(t, n)
	for quiet := time.After(2500 * time.Millisecond); ; {
		select {
		case p := <-pushes:
			if p.Event == "sessions.changed" || p.Event == "sessions.revs" {
				t.Fatalf("%s went to the hub before its word: %s", p.Event, p.Params)
			}
			continue
		case <-quiet:
		}
		break
	}
	configureNode(t, n, map[string]any{"botmux": true})
	awaitPush(t, pushes, listsBotmux(true), "no list went to the hub after its word")
}

// Where botmux is not installed, lists go to the hub from the start, as before.
func TestListsGoAtOnceWithoutBotmux(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := "/tmp/poll"
	writeTranscript(t, home, cwd, sidA, time.Now(),
		`{"type":"user","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp/poll","message":{"role":"user","content":"hello"}}`)
	f.reply("session.snapshot", snapshot(nil, nil,
		[]any{claudeAgent(sidA, proto.StatusIdle, "w5:p1", "w5:t1", "w5", cwd, "poll")}, nil))
	pushes, _ := pollNode(t, n)
	awaitPush(t, pushes, func(p hubPush) bool {
		return p.Event == "sessions.changed" && strings.Contains(string(p.Params), sidA)
	}, "no list went to the hub")
}

// listsBotmux: a session list that has (or has not) the session on its botmux pane.
func listsBotmux(on bool) func(hubPush) bool {
	return func(p hubPush) bool {
		return p.Event == "sessions.changed" && strings.Contains(string(p.Params), `"botmux:`+sidA+`"`) == on
	}
}

// awaitPush waits up to five seconds for a push that want takes.
func awaitPush(t *testing.T, pushes <-chan hubPush, want func(hubPush) bool, what string) {
	t.Helper()
	for timeout := time.After(5 * time.Second); ; {
		select {
		case p := <-pushes:
			if want(p) {
				return
			}
		case <-timeout:
			t.Fatal(what)
		}
	}
}

// A transcript whose agent only said botmux's sentinel (its answers all went to the topic) is a conversation, not a
// probe: it is listed offline once botmux no longer lists it.
func TestSentinelOnlyTranscriptIsListedOffline(t *testing.T) {
	n, _, _, home := bmxNode(t)
	pad := strings.Repeat("x", 2048) // offline listing skips files under 2 KB
	writeTranscript(t, home, bmxCwd, sidA, time.Now().Add(-time.Minute),
		`{"type":"user","message":{"role":"user","content":"`+pad+`"}}`,
		`{"type":"assistant","timestamp":"`+time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)+
			`","message":{"role":"assistant","content":[{"type":"text","text":"BOTMUX_NOTHING_TO_SEND"}]}}`)
	s, ok := byID(listSessions(t, n))[sidA]
	if !ok || s.Status != proto.StatusOffline || s.LastText != "" {
		t.Fatalf("listed %v: %+v", ok, s)
	}
}

// bmxRollout writes a Codex and compatible variants rollout under root (<home>/.codex, <home>/.trae/cli) whose first prompt is botmux's,
// opening with the botmux session's id (botmuxID "": a prompt typed on the computer).
func bmxRollout(t *testing.T, root, thread, cwd, botmuxID string, written time.Time, events ...string) string {
	t.Helper()
	day := time.Now()
	dir := filepath.Join(root, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := "hello"
	if botmuxID != "" {
		prompt = "<session_id>" + botmuxID + "</session_id>\n<user_message>做一下</user_message>"
	}
	p, _ := json.Marshal(prompt)
	lines := []string{
		`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"` + thread + `","cwd":"` + cwd + `","instructions":"` + strings.Repeat("x", 30<<10) + `"}}`,
		`{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":` + string(p) + `}]}}}`,
	}
	lines = append(lines, events...)
	path := filepath.Join(dir, "rollout-"+day.Format("2006-01-02T15-04-05")+"-"+thread+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	bmxThreadA  = "0c000000-0000-4000-8000-00000000000a"
	bmxThreadB  = "0c000000-0000-4000-8000-00000000000b"
	bmxThreadE  = "0c000000-0000-4000-8000-00000000000e"
	bmxThreadT  = "0c000000-0000-4000-8000-0000000000aa"
	bmxThreadF0 = "0c000000-0000-4000-8000-0000000000f0" // sidA's thread before a fork
	bmxThreadX  = "0c000000-0000-4000-8000-0000000000cc" // a Codex conversation of the computer's, same directory
)

// botmux's Codex and compatible variants sessions are listed as their threads (found by botmux's session id in the rollout's first
// prompt), with botmux's state, the topic's title and the rollout as the conversation. One whose rollout is not found,
// codex-app ones, and threads in another directory are not.
func TestBotmuxListsCodexAndTraexSessions(t *testing.T) {
	n, f, fh, home := bmxNode(t)
	codexRoot, traexRoot := filepath.Join(home, ".codex"), filepath.Join(home, ".trae", "cli")
	now := time.Now()
	bmxRollout(t, codexRoot, bmxThreadF0, bmxCwd, sidA, now.Add(-time.Hour))
	bmxRollout(t, codexRoot, bmxThreadA, bmxCwd, sidA, now.Add(-time.Minute), // the fork botmux runs now
		`{"timestamp":"2026-01-01T00:00:02Z","type":"event_msg","payload":{"type":"task_started"}}`)
	bmxRollout(t, codexRoot, bmxThreadB, bmxCwd, sidB, now.Add(-time.Minute))
	bmxRollout(t, codexRoot, bmxThreadE, bmxCwd, sidE, now.Add(-time.Hour))
	bmxRollout(t, codexRoot, bmxThreadX, bmxCwd, "", now)
	bmxRollout(t, codexRoot, "0c000000-0000-4000-8000-0000000000dd", "/tmp/elsewhere", sidF, now)
	bmxRollout(t, traexRoot, bmxThreadT, bmxCwd, sidD, now.Add(-time.Minute))
	f.rows = []map[string]any{
		bmxRow(sidA, "codex", "alive", "working", false, "codex 任务"),
		bmxRow(sidB, "codex", "alive", "idle", true, "asks"),
		bmxRow(sidE, "codex", "not_running", "unknown", false, "parked"),
		bmxRow(sidD, "traex", "alive", "idle", false, "traex 任务"),
		bmxRow(sidF, "codex", "alive", "working", false, "elsewhere"),
		bmxRow(sidG, "codex-app", "alive", "working", false, "app"),
	}
	list := listSessions(t, n)
	m := byID(list)
	want := map[string]struct{ botmux, agent, status, title string }{
		bmxThreadA: {sidA, "codex", proto.StatusWorking, "codex 任务"},
		bmxThreadB: {sidB, "codex", proto.StatusBlocked, "asks"},
		bmxThreadE: {sidE, "codex", proto.StatusOffline, "parked"},
		bmxThreadT: {sidD, traexAgent, proto.StatusDone, "traex 任务"},
	}
	for id, w := range want {
		s, ok := m[id]
		if !ok {
			t.Fatalf("%s not listed: %v", id, ids(list))
		}
		if s.PaneID != "botmux:"+w.botmux || s.Agent != w.agent || s.Status != w.status || s.Title != w.title ||
			s.SpaceKind != host.Botmux || s.SpaceID != dirSpace+bmxCwd || s.CodexRoute != "" {
			t.Errorf("%s: %+v, want %+v", id, s, w)
		}
	}
	if s := m[bmxThreadA]; s.Rev == 0 {
		t.Errorf("no conversation read for %s: %+v", bmxThreadA, s)
	}
	for _, s := range list {
		if s.PaneID != "" && !slices.Contains([]string{bmxThreadA, bmxThreadB, bmxThreadE, bmxThreadT}, s.SessionID) {
			t.Errorf("listed with a pane: %s %s", s.SessionID, s.PaneID)
		}
		if s.SessionID == sidA || s.SessionID == sidF || s.SessionID == sidG {
			t.Errorf("listed by botmux's id: %+v", s)
		}
	}
	if s, ok := m[bmxThreadF0]; ok && s.PaneID != "" {
		t.Errorf("the thread before the fork has the pane: %+v", s)
	}
	if trace := fh.calls(); len(trace) != 0 {
		t.Errorf("herdr was asked: %v", trace)
	}
}

// A Codex session of botmux's that Codex's history (and the desktop App's list) also has is listed once, as botmux's:
// sendable through botmux, not a read-only history row.
func TestBotmuxCodexSessionWinsOverCodexHistory(t *testing.T) {
	n, f, _, home := bmxNode(t)
	bmxRollout(t, filepath.Join(home, ".codex"), bmxThreadA, bmxCwd, sidA, time.Now().Add(-time.Minute))
	n.codices = newCodexRegistry()
	n.configureCodex(proto.CodexConfig{}) // ~/.codex is the legacy home: its thread ids stay as they are
	if s := byID(listSessions(t, n))[bmxThreadA]; s.SourceKind != "codex_history" {
		t.Fatalf("without botmux: %+v", s) // the history row botmux's session replaces
	}
	f.rows = []map[string]any{bmxRow(sidA, "codex", "alive", "idle", false, "codex 任务")}
	n.hosts.Get(host.Botmux).(*botmuxHost).at = time.Time{}
	var rows []proto.Session
	for _, s := range listSessions(t, n) {
		if s.SessionID == bmxThreadA || s.SourceThreadID == bmxThreadA {
			rows = append(rows, s)
		}
	}
	if len(rows) != 1 || rows[0].PaneID != "botmux:"+sidA || rows[0].SourceKind != "" || rows[0].Status != proto.StatusDone {
		t.Fatalf("rows of the thread: %+v", rows)
	}
}

// A message to a Codex and compatible variants session of botmux's goes to botmux as a turn of its botmux session — never typed into a
// pane, never a resume of the thread elsewhere, no app-server join — and asks no screen first.
func TestBotmuxCodexReplyIsDeliveredByBotmux(t *testing.T) {
	n, f, fh, home := bmxNode(t)
	bmxRollout(t, filepath.Join(home, ".codex"), bmxThreadA, bmxCwd, sidA, time.Now().Add(-time.Minute))
	bmxRollout(t, filepath.Join(home, ".codex"), bmxThreadE, bmxCwd, sidE, time.Now().Add(-time.Hour))
	bmxRollout(t, filepath.Join(home, ".trae", "cli"), bmxThreadT, bmxCwd, sidD, time.Now().Add(-time.Minute))
	f.rows = []map[string]any{
		bmxRow(sidA, "codex", "alive", "working", false, "a"),
		bmxRow(sidE, "codex", "not_running", "unknown", false, "parked"),
		bmxRow(sidD, "traex", "alive", "idle", false, "t"),
	}
	listSessions(t, n)
	for _, id := range []string{bmxThreadA, bmxThreadE, bmxThreadT} {
		if err := n.send(context.Background(), id, "继续 "+id, nil, sendOpts{}); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	got := f.sent()
	if len(got) != 3 {
		t.Fatalf("triggers %v", got)
	}
	for i, want := range []struct{ botmux, thread string }{{sidA, bmxThreadA}, {sidE, bmxThreadE}, {sidD, bmxThreadT}} {
		target, _ := got[i]["target"].(map[string]any)
		if target["sessionId"] != want.botmux || got[i]["instruction"] != "继续 "+want.thread {
			t.Errorf("trigger %d: %v", i, got[i])
		}
	}
	if cards := n.codexCardsNow(context.Background(), ""); len(cards) != 0 {
		t.Errorf("cards of botmux's sessions: %+v", cards)
	}
	if trace := fh.calls(); len(trace) != 0 {
		t.Errorf("herdr was asked: %v", trace)
	}
}

// The same Codex thread in a herdr pane and as botmux's parked row: the pane that runs it keeps it.
func TestRunningCodexPaneWinsOverParkedBotmuxRow(t *testing.T) {
	n, _, home := herdrNode(t)
	bmxRollout(t, filepath.Join(home, ".codex"), bmxThreadA, bmxCwd, sidA, time.Now().Add(-time.Minute))
	parked := host.Agent{Agent: "codex", AgentStatus: proto.StatusOffline, Cwd: bmxCwd, PaneID: "botmux:" + sidA}
	running := host.Agent{Agent: "codex", AgentSession: &host.AgentSession{Kind: "id", Value: bmxThreadA},
		AgentStatus: proto.StatusWorking, Cwd: bmxCwd, PaneID: "w1:p1"}
	for _, agents := range [][]host.Agent{{parked, running}, {running, parked}} {
		n.setAgents(agents)
		if a, _ := n.agent(bmxThreadA); a.PaneID != "w1:p1" {
			t.Fatalf("%v: pane %q", agents, a.PaneID)
		}
	}
}

// Only the head of a rollout is read for botmux's id, and a rollout that has it is not read again.
func TestBotmuxIDsInReadsTheHeadOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	p := bmxRollout(t, root, bmxThreadA, bmxCwd, sidA, time.Now())
	if got := botmuxIDsIn(p); !slices.Equal(got, []string{sidA}) {
		t.Fatalf("ids %v", got)
	}
	tail := `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"<session_id>` + sidB + `</session_id>"}]}}}` + "\n"
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(tail)
	fh.Close()
	if got := botmuxIDsIn(p); !slices.Equal(got, []string{sidA}) {
		t.Fatalf("after a later prompt: %v", got)
	}
	big := bmxRollout(t, filepath.Join(t.TempDir(), ".codex"), bmxThreadB, bmxCwd, "", time.Now(),
		`{"type":"event_msg","payload":{"text":"`+strings.Repeat("y", botmuxHeadMax)+`"}}`, strings.TrimSpace(tail))
	if got := botmuxIDsIn(big); len(got) != 0 {
		t.Fatalf("an id past the head: %v", got)
	}
	escaped := bmxRollout(t, filepath.Join(t.TempDir(), ".codex"), bmxThreadE, bmxCwd, "", time.Now(),
		`{"type":"event_msg","payload":{"text":"<session_id>`+sidE+`</session_id>"}}`)
	if got := botmuxIDsIn(escaped); !slices.Equal(got, []string{sidE}) {
		t.Fatalf("escaped: %v", got)
	}
}

// botmux's own failure notice (a turn it stopped on an error, its reason starting with a warning sign) is no wait for
// the user: the session takes its turn's state and carries the notice as failure_reason, until a message comes in
// after it. An attention an agent raised waits for the user until the agent takes a message after it.
func TestBotmuxFailureNoticeAndStaleAttention(t *testing.T) {
	n, f, _, home := bmxNode(t)
	raisedAt := time.Now().Add(-48 * time.Hour)
	row := func(id, reason string) map[string]any {
		r := bmxRow(id, "claude-code", "alive", "idle", false, "t")
		r["raw"] = map[string]any{"title": "t",
			"agentAttention": map[string]any{"kind": "blocked", "reason": reason, "at": raisedAt.UnixMilli()}}
		return r
	}
	const failure = "⚠️ Claude 本轮执行失败，且当前错误不能安全自动续跑。\n 请检查后再发一条消息继续。"
	const sidH = "0d000000-0000-4000-8000-000000000013"
	f.rows = []map[string]any{
		row(sidA, failure),                // failed, nothing after
		row(sidB, failure),                // failed, then a message went in another way
		row(sidC, "需要你决定用哪个方案"),           // an agent's attention, still waiting
		row(sidD, "需要你决定用哪个方案"),           // an agent's attention, answered another way
		row(sidE, "⚠️ 需要你确认是否删库"),         // an agent's attention with a warning sign: still a wait
		row(sidF, "需要你决定用哪个方案"),           // answered with a long pasted message (a line the tail read skips)
		row(sidG, "需要你决定用哪个方案"),           // only a slash command, a "! cmd" and a teammate's report after it
		row(sidH, "⚠️ Claude 本轮执行失败，请检查"), // a warning sign without botmux's words: an agent's
	}
	prompt := func(at time.Time) string {
		return `{"type":"user","timestamp":"` + at.UTC().Format(time.RFC3339) + `","message":{"role":"user","content":"hi"}}`
	}
	user := func(at time.Time, text string) string {
		b, _ := json.Marshal(text)
		return `{"type":"user","timestamp":"` + at.UTC().Format(time.RFC3339) + `","message":{"role":"user","content":` + string(b) + `}}`
	}
	after := raisedAt.Add(time.Hour)
	for _, id := range []string{sidA, sidB, sidC, sidD, sidE, sidF, sidG, sidH} {
		lines := []string{prompt(raisedAt.Add(-time.Hour))}
		switch id {
		case sidB, sidD:
			lines = append(lines, prompt(after))
		case sidF: // past the head read (32 KB), so only the tail read sees it
			pad := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` +
				strings.Repeat("x", 40<<10) + `"}]}}`
			lines = append(lines, pad, user(after, strings.Repeat("日志 ", 4<<10)))
		case sidG:
			lines = append(lines, user(after, "<command-name>/model</command-name><command-args>opus</command-args>"),
				user(after, "<bash-input>ls</bash-input>"),
				user(after, `<teammate-message teammate_id="tester">测试都过了</teammate-message>`))
		}
		writeTranscript(t, home, bmxCwd, id, time.Now().Add(-time.Minute), lines...)
	}
	m := byID(listSessions(t, n))
	cases := []struct {
		id, status, attention, failure string
	}{
		{sidA, proto.StatusDone, "", "⚠️ Claude 本轮执行失败，且当前错误不能安全自动续跑。 请检查后再发一条消息继续。"},
		{sidB, proto.StatusDone, "", ""},
		{sidC, proto.StatusBlocked, "需要你决定用哪个方案", ""},
		{sidD, proto.StatusDone, "", ""},
		{sidE, proto.StatusBlocked, "⚠️ 需要你确认是否删库", ""},
		{sidF, proto.StatusDone, "", ""},
		{sidG, proto.StatusBlocked, "需要你决定用哪个方案", ""},
		{sidH, proto.StatusBlocked, "⚠️ Claude 本轮执行失败，请检查", ""},
	}
	for _, c := range cases {
		s := m[c.id]
		if s.Status != c.status || s.AttentionReason != c.attention || s.FailureReason != c.failure {
			t.Errorf("%s: status %s attention %q failure %q, want %s %q %q", c.id, s.Status, s.AttentionReason,
				s.FailureReason, c.status, c.attention, c.failure)
		}
	}
}

// One directory with or without its trailing slash is one space; a directory named like its parent, ignoring case,
// carries the parent in its label, whatever else is listed (#1836).
func TestBotmuxSpacesByCleanDirectoryAndDistinctLabels(t *testing.T) {
	if got := botmuxDir("/work/Botmux/"); got != "/work/Botmux" {
		t.Fatalf("botmuxDir = %q", got)
	}
	spaces := []host.Space{
		{ID: dirSpace + "/work/Botmux", Label: "Botmux", Cwd: "/work/Botmux"},
		{ID: dirSpace + "/work/Botmux/botmux", Label: "botmux", Cwd: "/work/Botmux/botmux"},
		{ID: dirSpace + "/work/app", Label: "app", Cwd: "/work/app"},
	}
	botmuxSpaceLabels(spaces)
	var got []string
	for _, sp := range spaces {
		got = append(got, sp.Label)
	}
	if want := []string{"Botmux", "Botmux/botmux", "app"}; !slices.Equal(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	alone := []host.Space{{ID: dirSpace + "/work/Botmux/botmux", Label: "botmux", Cwd: "/work/Botmux/botmux"}}
	if botmuxSpaceLabels(alone); alone[0].Label != "Botmux/botmux" {
		t.Fatalf("alone label = %q, want the same as beside its parent", alone[0].Label)
	}
}

// A botmux session carries its Feishu chat and topic, and the topics it dispatched as botmux's dispatch registry
// names them (orchestrate-dispatch.json, by the dispatched topic's root, orchSessionId the dispatcher).
func TestBotmuxChatTopicAndDispatches(t *testing.T) {
	n, f, _, home := bmxNode(t)
	data := t.TempDir()
	n.hosts = host.NewRegistry(&botmuxHost{run: f.run, client: f.srv.Client(), dataDir: data})
	parent := bmxRow(sidA, "claude-code", "alive", "idle", false, "parent")
	parent["raw"] = map[string]any{"title": "parent", "chatId": "oc_1", "rootMessageId": "om_parent"}
	child := bmxRow(sidB, "claude-code", "alive", "working", false, "child")
	child["raw"] = map[string]any{"title": "child", "chatId": "oc_1", "rootMessageId": "om_child"}
	f.rows = []map[string]any{parent, child}
	if err := os.WriteFile(filepath.Join(data, "orchestrate-dispatch.json"), []byte(`{
		"om_child": {"orchSessionId": "`+sidA+`", "orchRoot": "om_parent", "status": "accepted"},
		"om_other": {"orchSessionId": "`+sidA+`"},
		"om_gone": {"orchSessionId": "someone-else"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sidA, sidB} {
		writeTranscript(t, home, bmxCwd, id, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	}
	m := byID(listSessions(t, n))
	if p := m[sidA]; p.Chat != "oc_1" || p.Topic != "om_parent" || !slices.Equal(p.Dispatched, []string{"om_child", "om_other"}) {
		t.Fatalf("parent = chat %q topic %q dispatched %v", p.Chat, p.Topic, p.Dispatched)
	}
	if c := m[sidB]; c.Chat != "oc_1" || c.Topic != "om_child" || len(c.Dispatched) != 0 {
		t.Fatalf("child = chat %q topic %q dispatched %v", c.Chat, c.Topic, c.Dispatched)
	}
	// A registry botmux wrote half of, or no data directory: nothing dispatched.
	if err := os.WriteFile(filepath.Join(data, "orchestrate-dispatch.json"), []byte(`{"om_child": `), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := botmuxDispatched(data); got != nil {
		t.Fatalf("a broken registry = %v", got)
	}
	if got := botmuxDispatched(""); got != nil {
		t.Fatalf("no data dir = %v", got)
	}
}

// botmux's data directory as botmux resolves it: SESSION_DATA_DIR, the ~/.botmux/.data-dir breadcrumb, ~/.botmux/data.
func TestBotmuxDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SESSION_DATA_DIR", "")
	if got := botmuxDataDir(); got != filepath.Join(home, ".botmux", "data") {
		t.Fatalf("default = %q", got)
	}
	os.MkdirAll(filepath.Join(home, ".botmux"), 0o700)
	os.WriteFile(filepath.Join(home, ".botmux", ".data-dir"), []byte("/srv/botmux-data\n"), 0o600)
	if got := botmuxDataDir(); got != "/srv/botmux-data" {
		t.Fatalf("breadcrumb = %q", got)
	}
	t.Setenv("SESSION_DATA_DIR", "/env/data")
	if got := botmuxDataDir(); got != "/env/data" {
		t.Fatalf("env = %q", got)
	}
}
