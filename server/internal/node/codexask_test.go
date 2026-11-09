package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Codex question and approval cards against a fake herdr: what is listed, and the keys and text an answer turns into
// for each state the TUI can be in (docs/integrations/codex.md "Questions and approvals").

const (
	cxID   = "01a19756-bcef-7f23-91dd-a562c8d078e4"
	cxPane = "w1:p1"

	cxMeta          = `{"timestamp":"2026-11-01T13:40:00.000Z","type":"session_meta","payload":{"id":"` + cxID + `","cwd":"/private/tmp/project","cli_version":"0.163.0"}}`
	cxAsyncCallLine = `{"timestamp":"2026-11-01T13:40:29.600Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input_async","arguments":"{\"questions\":[{\"title\":\"Which color should the banner be?\",\"options\":[\"Red\",\"Green\",\"Blue\"]}]}","call_id":"call_a1"}}`
	// the async item Codex writes once it accepted the call: it carries the questions (cxAsyncCall is both lines)
	cxAsyncItemLine = `{"timestamp":"2026-11-01T13:40:29.650Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"call_a1","content":[{"type":"Text","text":"Which color should the banner be?\n- Red\n- Green\n- Blue"}],"phase":"final_answer","delivery":"async","questions":[{"title":"Which color should the banner be?","options":["Red","Green","Blue"]}]}}}`
	cxAsyncCall     = cxAsyncCallLine + "\n" + cxAsyncItemLine
	cxAsyncOut      = `{"timestamp":"2026-11-01T13:40:29.700Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_a1","output":"{\"accepted\":true}"}}`
	cxSyncCall      = `{"timestamp":"2026-11-01T13:49:35.000Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","arguments":"{\"questions\":[{\"header\":\"Color\",\"id\":\"color\",\"question\":\"Which color should the banner be?\",\"options\":[{\"label\":\"Red\",\"description\":\"warm\"},{\"label\":\"Blue\",\"description\":\"calm\"}]},{\"header\":\"Size\",\"id\":\"size\",\"question\":\"How tall should it be?\",\"options\":[{\"label\":\"Small\",\"description\":\"one line\"},{\"label\":\"Large\",\"description\":\"two lines\"}]}]}","call_id":"call_s1"}}`
	cxSyncOut       = `{"timestamp":"2026-11-01T13:49:55.788Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_s1","output":"{\"answers\":{}}"}}`

	cxEmpty = "• Done.\n\n› Ask Codex to do anything\n\n  gpt-6 low · /private/tmp/project\n"
)

// cxToday moves the fixtures' timestamps (2026-11-01 13:40 on) to an hour ago, keeping their order: async questions
// older than codexAskMaxAge are dropped, and with the fixed day every test that lists one failed from 2026-11-02
// 01:40 UTC on.
func cxToday(lines string) string {
	shift := time.Since(time.Date(2026, 11, 1, 13, 40, 0, 0, time.UTC)) - time.Hour
	return cxStamp.ReplaceAllStringFunc(lines, func(m string) string {
		at, err := time.Parse(time.RFC3339Nano, m[len(`"timestamp":"`):len(m)-1])
		if err != nil {
			return m
		}
		return `"timestamp":"` + at.Add(shift).UTC().Format("2006-01-02T15:04:05.000Z") + `"`
	})
}

var cxStamp = regexp.MustCompile(`"timestamp":"[^"]+"`)

func cxBox(text string) string {
	return "• Done.\n\n› " + text + "\n\n  gpt-6 low · /private/tmp/project\n"
}

// codexNode is a node on a fake herdr with one live Codex session whose rollout holds lines.
func codexNode(t *testing.T, status string, lines ...string) (*Node, *fakeHerdr, string) {
	t.Helper()
	oldStep, oldPoll, oldConfirm := codexStep, codexPoll, codexConfirm
	codexStep, codexPoll, codexConfirm = 5*time.Millisecond, 10*time.Millisecond, time.Second
	t.Cleanup(func() { codexStep, codexPoll, codexConfirm = oldStep, oldPoll, oldConfirm })
	n, f, home := herdrNode(t)
	day := time.Now()
	dir := filepath.Join(home, ".codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-01T21-40-06-"+cxID+".jsonl")
	if err := os.WriteFile(path, []byte(cxToday(strings.Join(append([]string{cxMeta}, lines...), "\n"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.agents[cxID] = host.Agent{Agent: "codex", PaneID: cxPane, AgentStatus: status, Cwd: "/private/tmp/project"}
	return n, f, path
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func cxUserLine(text string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `{"timestamp":"2026-11-01T13:41:00.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"` + r.Replace(text) + `"}]}}}`
}

func onlyCard(t *testing.T, n *Node) codexCard {
	t.Helper()
	cards := n.codexCardsNow(context.Background(), "")
	if len(cards) != 1 {
		t.Fatalf("cards %+v", cards)
	}
	return cards[0]
}

func refusalCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func unpaste(s string) string { return strings.TrimSuffix(strings.TrimPrefix(s, pasteStart), pasteEnd) }

// codexTUI plays the TUI's box: a paste shows in it, enter submits it (written to the rollout as the user's message).
func codexTUI(t *testing.T, f *fakeHerdr, path string) *[]string {
	var mu sync.Mutex
	var sent []string
	typed := ""
	f.after(func(c herdrCall) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case c.Method == "pane.send_text":
			typed = unpaste(str(c.Params["text"]))
			f.setScreen(cxPane, cxBox(typed))
		case isKeys("enter")(c) && typed != "":
			appendLine(t, path, cxUserLine(typed))
			sent = append(sent, typed)
			typed = ""
			f.setScreen(cxPane, cxEmpty)
		}
	})
	return &sent
}

func TestCodexCardsListed(t *testing.T) {
	n, f, _ := codexNode(t, proto.StatusBlocked, cxAsyncCall, cxAsyncOut)
	f.setScreen(cxPane, screenFixture(t, "codex-0.163.0-exec-approval-120.txt"))
	cards := n.codexCardsNow(context.Background(), "")
	if len(cards) != 2 {
		t.Fatalf("cards %+v", cards)
	}
	q, a := cards[0].ix, cards[1].ix
	if q.Kind != proto.IxQuestion || q.Agent != "codex" || q.ToolName != "request_user_input_async" || len(q.Questions) != 1 ||
		q.Questions[0].Question != "Which color should the banner be?" || q.Questions[0].Options[2].Label != "Blue" {
		t.Errorf("question card %+v", q)
	}
	if a.Kind != proto.IxPermission || a.ToolName != "Bash" || a.Summary != "curl -sI https://example.com | head -1" ||
		!strings.HasPrefix(a.Reason, "Allow this command") || a.AlwaysLabel != "以后不再问以 curl -sI https://example.com 开头的命令" {
		t.Errorf("approval card %+v", a)
	}
	// Not blocked: the screen is not read for an approval.
	n.agents[cxID] = host.Agent{Agent: "codex", PaneID: cxPane, AgentStatus: proto.StatusIdle}
	f.reset()
	if cards := n.codexCardsNow(context.Background(), ""); len(cards) != 1 || len(f.trace("pane.read")) != 0 {
		t.Errorf("idle: cards %d, reads %v", len(cards), f.trace("pane.read"))
	}
}

func TestCodexAnswerAsync(t *testing.T) {
	n, f, path := codexNode(t, proto.StatusIdle, cxAsyncCall, cxAsyncOut)
	f.setScreen(cxPane, cxEmpty)
	sent := codexTUI(t, f, path)
	card := onlyCard(t, n)
	err := n.codexAnswer(context.Background(), cxID, card.ix.ID, proto.Resolution{Answers: map[string]string{"Which color should the banner be?": "Green"}})
	if err != nil {
		t.Fatal(err)
	}
	want := transcript.CodexReplyMessage([]transcript.CodexReply{{QuestionItemID: transcript.QuestionItemID("call_a1", 0), Question: "Which color should the banner be?", Answer: "Green"}})
	if len(*sent) != 1 || (*sent)[0] != want {
		t.Errorf("sent %q, want %q", *sent, want)
	}
	assertTrace(t, f.trace("pane.send_text", "pane.send_keys"), []string{
		call("pane.send_text", map[string]any{"pane_id": cxPane, "text": pasteStart + want + pasteEnd}),
		call("pane.send_keys", map[string]any{"pane_id": cxPane, "keys": []string{"enter"}}),
	})
	if cards := n.codexCardsNow(context.Background(), ""); len(cards) != 0 {
		t.Errorf("still listed: %+v", cards)
	}
}

// The states the computer may be in when the answer comes: a draft refuses; the pager and the question pane step aside.
func TestCodexAnswerAsyncStates(t *testing.T) {
	answer := proto.Resolution{Answers: map[string]string{"Which color should the banner be?": "Red"}}
	t.Run("draft", func(t *testing.T) {
		n, f, _ := codexNode(t, proto.StatusIdle, cxAsyncCall)
		f.setScreen(cxPane, cxBox("half typed"))
		err := n.codexAnswer(context.Background(), cxID, onlyCard(t, n).ix.ID, answer)
		if refusalCode(err) != screen.InputDraft || len(f.trace("pane.send_text", "pane.send_keys")) != 0 {
			t.Errorf("err %v, typed %v", err, f.trace("pane.send_text", "pane.send_keys"))
		}
	})
	t.Run("approval", func(t *testing.T) {
		n, f, _ := codexNode(t, proto.StatusIdle, cxAsyncCall)
		f.setScreen(cxPane, screenFixture(t, "codex-0.163.0-exec-approval-120.txt"))
		err := n.codexAnswer(context.Background(), cxID, onlyCard(t, n).ix.ID, answer)
		if refusalCode(err) != screen.DialogOpen || len(f.trace("pane.send_text", "pane.send_keys")) != 0 {
			t.Errorf("err %v, typed %v", err, f.trace("pane.send_text", "pane.send_keys"))
		}
	})
	for _, c := range []struct{ name, fixture, key string }{
		{"question pane", "codex-0.163.0-async-expanded-120.txt", "shift+right"},
		{"transcript pager", "codex-0.153.4-transcript-pending-approval-120.txt", "q"},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, f, path := codexNode(t, proto.StatusIdle, cxAsyncCall)
			f.screenSteps(cxPane, isKeys(c.key), screenFixture(t, c.fixture), cxEmpty)
			sent := codexTUI(t, f, path)
			if err := n.codexAnswer(context.Background(), cxID, onlyCard(t, n).ix.ID, answer); err != nil {
				t.Fatal(err)
			}
			keys := f.trace("pane.send_keys")
			if len(keys) != 2 || keys[0] != call("pane.send_keys", map[string]any{"pane_id": cxPane, "keys": []string{c.key}}) || len(*sent) != 1 {
				t.Errorf("keys %v, sent %v", keys, *sent)
			}
		})
	}
	t.Run("answered at the computer", func(t *testing.T) {
		n, f, path := codexNode(t, proto.StatusIdle, cxAsyncCall)
		f.setScreen(cxPane, cxEmpty)
		id := onlyCard(t, n).ix.ID
		appendLine(t, path, cxUserLine("green please"))
		err := n.codexAnswer(context.Background(), cxID, id, answer)
		if refusalCode(err) != screen.PromptChanged || len(f.trace("pane.send_text", "pane.send_keys")) != 0 {
			t.Errorf("err %v, typed %v", err, f.trace("pane.send_text", "pane.send_keys"))
		}
	})
}

func TestCodexAnswerApproval(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		name  string
		r     proto.Resolution
		keys  string
		typed string
	}{
		{"allow", proto.Resolution{Allow: &yes}, "1", ""},
		{"always", proto.Resolution{Allow: &yes, Always: true}, "2", ""},
		{"deny", proto.Resolution{Allow: &no}, "3", ""},
		{"deny with a reason", proto.Resolution{Allow: &no, Message: "use wget instead"}, "3", "use wget instead"},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, f, path := codexNode(t, proto.StatusBlocked)
			f.screenSteps(cxPane, isKeys(c.keys), screenFixture(t, "codex-0.163.0-exec-approval-120.txt"), cxEmpty)
			sent := codexTUI(t, f, path)
			card := onlyCard(t, n)
			if err := n.codexAnswer(context.Background(), cxID, card.ix.ID, c.r); err != nil {
				t.Fatal(err)
			}
			if keys := f.trace("pane.send_keys"); len(keys) == 0 || keys[0] != call("pane.send_keys", map[string]any{"pane_id": cxPane, "keys": []string{c.keys}}) {
				t.Errorf("keys %v", keys)
			}
			if c.typed == "" && len(*sent) != 0 || c.typed != "" && (len(*sent) != 1 || (*sent)[0] != c.typed) {
				t.Errorf("sent %q", *sent)
			}
		})
	}
	t.Run("changed", func(t *testing.T) {
		n, f, _ := codexNode(t, proto.StatusBlocked)
		f.setScreen(cxPane, screenFixture(t, "codex-0.163.0-exec-approval-120.txt"))
		id := onlyCard(t, n).ix.ID
		f.setScreen(cxPane, screenFixture(t, "codex-0.153.4-exec-approval-120.txt")) // another command's approval
		err := n.codexAnswer(context.Background(), cxID, id, proto.Resolution{Allow: &yes})
		if refusalCode(err) != screen.PromptChanged || len(f.trace("pane.send_keys")) != 0 {
			t.Errorf("err %v, keys %v", err, f.trace("pane.send_keys"))
		}
	})
}

// cxOverlay is request_user_input's second question, the cursor on option sel, its notes field open with notes.
func cxOverlay(sel int, notes *string) string {
	rows := []string{"Small              one line", "Large              two lines", "None of the above  Optionally, add details in notes (tab)"}
	out := []string{"", "  Question 2/2 (1 unanswered)", "  How tall should it be?", ""}
	for i, r := range rows {
		mark := "   "
		if i+1 == sel {
			mark = "  ›"
		}
		out = append(out, mark+" "+string(rune('1'+i))+". "+r)
	}
	out = append(out, "")
	if notes != nil {
		text := *notes
		if text == "" {
			text = "Add notes"
		}
		out = append(out, "  › "+text, "", "  tab or esc to clear notes | enter to submit all")
	} else {
		out = append(out, "  tab to add notes | enter to submit all | ←/→ to navigate questions | esc to interrupt")
	}
	return strings.Join(out, "\n") + "\n"
}

func TestCodexAnswerOverlay(t *testing.T) {
	n, f, path := codexNode(t, proto.StatusBlocked, cxSyncCall)
	open, typed := "", "Medium"
	steps := []struct {
		after  func(herdrCall) bool
		screen string
	}{
		{isKeys("2"), cxOverlay(1, nil)},
		{isKeys("down", "down", "tab"), cxOverlay(3, &open)},
		{func(c herdrCall) bool { return c.Method == "pane.send_text" }, cxOverlay(3, &typed)},
		{isKeys("enter"), cxEmpty},
	}
	f.setScreen(cxPane, screenFixture(t, "codex-0.163.0-plan-question-120.txt"))
	var mu sync.Mutex
	at := 0
	f.after(func(c herdrCall) {
		mu.Lock()
		defer mu.Unlock()
		if at < len(steps) && steps[at].after(c) {
			f.setScreen(cxPane, steps[at].screen)
			if at == len(steps)-1 {
				appendLine(t, path, cxToday(cxSyncOut))
			}
			at++
		}
	})
	card := onlyCard(t, n)
	if card.ix.Questions[0].Header != "Color" || card.ix.Questions[1].Options[1].Description != "two lines" {
		t.Errorf("card %+v", card.ix)
	}
	err := n.codexAnswer(context.Background(), cxID, card.ix.ID, proto.Resolution{Answers: map[string]string{
		"Which color should the banner be?": "Blue", "How tall should it be?": "Medium",
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace("pane.send_text", "pane.send_keys"), []string{
		call("pane.send_keys", map[string]any{"pane_id": cxPane, "keys": []string{"2"}}),
		call("pane.send_keys", map[string]any{"pane_id": cxPane, "keys": []string{"down", "down", "tab"}}),
		call("pane.send_text", map[string]any{"pane_id": cxPane, "text": pasteStart + "Medium" + pasteEnd}),
		call("pane.send_keys", map[string]any{"pane_id": cxPane, "keys": []string{"enter"}}),
	})
}

func TestCodexAnswerOverlayBusy(t *testing.T) {
	// Someone is typing notes into the question at the computer: nothing is pressed.
	n, f, _ := codexNode(t, proto.StatusBlocked, cxSyncCall)
	notes := strings.Replace(screenFixture(t, "codex-0.153.4-plan-question-notes-120.txt"), "› Add notes", "› Purp", 1)
	f.setScreen(cxPane, notes)
	err := n.codexAnswer(context.Background(), cxID, onlyCard(t, n).ix.ID, proto.Resolution{Answers: map[string]string{
		"Which color should the banner be?": "Red", "How tall should it be?": "Small",
	}})
	if refusalCode(err) != screen.DialogOpen || len(f.trace("pane.send_text", "pane.send_keys")) != 0 {
		t.Errorf("err %v, typed %v", err, f.trace("pane.send_text", "pane.send_keys"))
	}
}

func TestCodexSend(t *testing.T) {
	compactFooter := "  GPT-6.1-Sol xhigh · ~/project · Main [default]    Goal achieved (30m)\n" +
		"  ← for agents · ? for shortcuts\n"
	t.Run("compact empty composer", func(t *testing.T) {
		n, f, path := codexNode(t, proto.StatusBlocked)
		f.setScreen(cxPane, "• Done.\n\n› Ask Codex to do anything\n"+compactFooter)
		f.reply("agent.prompt", herdrFail{Code: "agent_blocked", Message: "agent is blocked and requires interactive input"})
		sent := codexTUI(t, f, path)
		if _, err := request(t, n, "session.send", map[string]any{"session_id": cxID, "text": "check the current status"}); err != nil {
			t.Fatal(err)
		}
		if len(*sent) != 1 || (*sent)[0] != "check the current status" {
			t.Errorf("sent %q", *sent)
		}
	})
	t.Run("compact genuine draft", func(t *testing.T) {
		n, f, _ := codexNode(t, proto.StatusIdle)
		f.setScreen(cxPane, "• Done.\n\n› half typed\n  keep this line too\n"+compactFooter)
		_, err := request(t, n, "session.send", map[string]any{"session_id": cxID, "text": "hello"})
		if refusalCode(err) != screen.InputDraft || len(f.trace("agent.prompt", "pane.send_text", "pane.send_keys")) != 0 {
			t.Errorf("err %v, calls %v", err, f.trace("agent.prompt", "pane.send_text", "pane.send_keys"))
		}
	})
	t.Run("draft", func(t *testing.T) {
		n, f, _ := codexNode(t, proto.StatusIdle)
		f.setScreen(cxPane, cxBox("half typed"))
		_, err := request(t, n, "session.send", map[string]any{"session_id": cxID, "text": "hello"})
		if refusalCode(err) != screen.InputDraft || len(f.trace("agent.prompt", "pane.send_text")) != 0 {
			t.Errorf("err %v, calls %v", err, f.trace("agent.prompt", "pane.send_text"))
		}
	})
	// herdr will not prompt a Codex with an async question open; its empty box takes the message typed.
	t.Run("blocked", func(t *testing.T) {
		n, f, path := codexNode(t, proto.StatusBlocked)
		f.setScreen(cxPane, cxEmpty)
		f.reply("agent.prompt", herdrFail{Code: "agent_blocked", Message: "agent is blocked and requires interactive input"})
		sent := codexTUI(t, f, path)
		if _, err := request(t, n, "session.send", map[string]any{"session_id": cxID, "text": "hello there"}); err != nil {
			t.Fatal(err)
		}
		if len(*sent) != 1 || (*sent)[0] != "hello there" {
			t.Errorf("sent %q", *sent)
		}
	})
}

// Someone moved on to the second question at the computer: the answer starts by going back to the first.
func TestCodexAnswerOverlayFromLaterQuestion(t *testing.T) {
	n, f, path := codexNode(t, proto.StatusBlocked, cxSyncCall)
	q1 := screenFixture(t, "codex-0.163.0-plan-question-120.txt")
	f.screenSteps(cxPane, func(c herdrCall) bool { return c.Method == "pane.send_keys" }, cxOverlay(1, nil), q1, cxOverlay(1, nil), cxEmpty)
	f.after(func(c herdrCall) {
		if isKeys("1")(c) {
			appendLine(t, path, cxToday(cxSyncOut))
		}
	})
	card := onlyCard(t, n)
	err := n.codexAnswer(context.Background(), cxID, card.ix.ID, proto.Resolution{Answers: map[string]string{
		"Which color should the banner be?": "Red", "How tall should it be?": "Small",
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace("pane.send_keys"), []string{
		call("pane.send_keys", map[string]any{"pane_id": cxPane, "keys": []string{"ctrl+p"}}),
		call("pane.send_keys", map[string]any{"pane_id": cxPane, "keys": []string{"1"}}),
		call("pane.send_keys", map[string]any{"pane_id": cxPane, "keys": []string{"1"}}),
	})
}

// A thread Persona joined on its app-server gets its approvals and overlay questions from there (codex_remote_asks.go):
// the TUI is not read for those. Its async questions are messages, not requests, and stay cards of the rollout.
func TestCodexCardsOfJoinedThreads(t *testing.T) {
	n, f, _ := codexNode(t, proto.StatusBlocked, cxAsyncCall, cxSyncCall)
	f.setScreen(cxPane, screenFixture(t, "codex-0.163.0-exec-approval-120.txt"))
	if cards := n.codexCardsNow(context.Background(), ""); len(cards) != 2 {
		t.Fatalf("to begin with: %+v", cards) // the async and the overlay question (an overlay hides any approval)
	}
	n.codices = newCodexRegistry()
	home := canonicalCodexHome(filepath.Join(n.home, ".codex"), n.home)
	n.codices.config.LegacyHome = home // the default home keeps the thread ids as they are
	if id := n.registerCodex(home, cxID, ""); id != cxID {
		t.Fatalf("registered as %q", id)
	}
	remote := &codexRemote{n: n, spec: proto.CodexServer{ID: "daemon", Home: home}, ready: true,
		threads: map[string]remoteThread{cxID: {id: cxID, publicID: cxID, joined: true}}}
	n.codices.remotes = map[string]*codexRemote{"daemon": remote}
	f.reset()
	cards := n.codexCardsNow(context.Background(), "")
	if len(cards) != 1 || cards[0].ask == nil || !cards[0].ask.Async {
		t.Errorf("joined: %+v", cards)
	}
	if len(f.trace("pane.read")) != 0 {
		t.Errorf("a joined thread's screen was read: %v", f.trace("pane.read"))
	}
	if route, _ := n.codexRoute(cxID); route != codexRouteAppServer {
		t.Errorf("route %q", route)
	}
	remote.threads[cxID] = remoteThread{id: cxID, publicID: cxID}
	if cards := n.codexCardsNow(context.Background(), ""); len(cards) != 2 {
		t.Errorf("seen on an app-server but not joined: %+v", cards)
	}
	if route, note := n.codexRoute(cxID); route != codexRouteTerminal || note == "" {
		t.Errorf("route %q %q", route, note)
	}
}

// A card handed back to the computer stays off the phone while Codex asks it, and the same approval asked again
// later is a new card (#3766: the hand-back used to keep it off the phone until the node restarted).
func TestCodexDismissedApprovalComesBack(t *testing.T) {
	old := codexHold
	codexHold = 150 * time.Millisecond
	t.Cleanup(func() { codexHold = old })
	n, f, _ := codexNode(t, proto.StatusBlocked)
	approval := screenFixture(t, "codex-0.163.0-exec-approval-120.txt")
	f.setScreen(cxPane, approval)
	id := onlyCard(t, n).ix.ID
	n.syncCodexCards(context.Background())
	if n.cx.sent != id {
		t.Fatalf("listed %q, want %q", n.cx.sent, id)
	}
	if err := n.codexAnswer(context.Background(), cxID, id, proto.Resolution{Dismiss: true}); err != nil {
		t.Fatal(err)
	}

	// Still asked on the computer for well past codexHold: each sync sees it again, so it stays the computer's.
	for end := time.Now().Add(4 * codexHold); time.Now().Before(end); time.Sleep(codexHold / 5) {
		n.syncCodexCards(context.Background())
		if n.cx.sent != "" {
			t.Fatalf("still asked on the computer but listed: %q", n.cx.sent)
		}
	}

	// Answered at the computer (one screen read without it), then the same approval asked again at once: a new card.
	f.setScreen(cxPane, cxEmpty)
	n.syncCodexCards(context.Background())
	f.setScreen(cxPane, approval)
	n.syncCodexCards(context.Background())
	if n.cx.sent != id {
		t.Fatalf("the same approval asked again: listed %q, want %q", n.cx.sent, id)
	}
}

// A screen read that fails says nothing about the dialog: a handed-back card is not forgotten for it.
func TestCodexDismissedApprovalKeptOverAFailedRead(t *testing.T) {
	n, f, _ := codexNode(t, proto.StatusBlocked)
	f.setScreen(cxPane, screenFixture(t, "codex-0.163.0-exec-approval-120.txt"))
	id := onlyCard(t, n).ix.ID
	if err := n.codexAnswer(context.Background(), cxID, id, proto.Resolution{Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	n.agents[cxID] = host.Agent{Agent: "codex", PaneID: cxPane, AgentStatus: proto.StatusWorking, Cwd: "/private/tmp/project"}
	n.syncCodexCards(context.Background()) // not blocked: the screen is not read
	n.agents[cxID] = host.Agent{Agent: "codex", PaneID: cxPane, AgentStatus: proto.StatusBlocked, Cwd: "/private/tmp/project"}
	n.syncCodexCards(context.Background())
	if n.cx.sent != "" {
		t.Fatalf("listed again without the dialog having gone: %q", n.cx.sent)
	}
}

// An approval answered on the phone and asked again right after its dialog left the screen is listed at once, not
// after codexHold (#4585): one screen read with no approval on it ends the hold. A question card keeps its hold.
func TestCodexAnsweredApprovalAskedAgainIsListedAtOnce(t *testing.T) {
	yes := true
	n, f, path := codexNode(t, proto.StatusBlocked)
	approval := screenFixture(t, "codex-0.163.0-exec-approval-120.txt")
	f.screenSteps(cxPane, isKeys("1"), approval, cxEmpty)
	_ = codexTUI(t, f, path)
	id := onlyCard(t, n).ix.ID
	if err := n.codexAnswer(context.Background(), cxID, id, proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	n.syncCodexCards(context.Background()) // the dialog is gone: a screen read without it
	if n.cx.sent != "" {
		t.Fatalf("listed with no dialog: %q", n.cx.sent)
	}
	f.setScreen(cxPane, approval) // Codex asks the same approval again
	n.syncCodexCards(context.Background())
	if n.cx.sent != id {
		t.Fatalf("the same approval asked again: listed %q, want %q at once", n.cx.sent, id)
	}

	// Held but never seen gone (the screen not read: the session not blocked), it stays held.
	n.cx.hold(cxID, id)
	n.agents[cxID] = host.Agent{Agent: "codex", PaneID: cxPane, AgentStatus: proto.StatusWorking, Cwd: "/private/tmp/project"}
	n.syncCodexCards(context.Background())
	n.agents[cxID] = host.Agent{Agent: "codex", PaneID: cxPane, AgentStatus: proto.StatusBlocked, Cwd: "/private/tmp/project"}
	n.syncCodexCards(context.Background())
	if n.cx.sent != "" {
		t.Fatalf("listed during its hold without the dialog having gone: %q", n.cx.sent)
	}
}
