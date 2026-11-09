package node

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A session open in Claude's desktop app (entrypoint claude-desktop-3p, as one build writes it) or in a terminal
// Persona does not drive lists as running, with its status from its sessions/<pid>.json and its transcript as for
// any session; it can be sent to when its peer inbox is one the node can use, and says why not when it is not.
func TestOutsideClaudeListedLive(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	p := bgTranscript(t, home, home, sidA, "plan the release")
	inbox := newFakeInbox(t, p)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))

	for _, c := range []struct{ entrypoint, source string }{
		{"claude-desktop", proto.SourceClaudeDesktop},
		{"claude-desktop-3p", proto.SourceClaudeDesktop},
		{"cli", proto.SourceClaudeTerminal},
	} {
		writeOutsideClaude(t, home, sidA, c.entrypoint, "busy", inbox, nil)
		s := byID(listSessions(t, n))[sidA]
		if s.SourceKind != c.source || s.Status != proto.StatusWorking || s.PaneID != "" || s.Title != "plan the release" ||
			s.LastText != "on it" || s.ReadOnlyReason != "" {
			t.Errorf("%s: %+v", c.entrypoint, s)
		}
		if cp := s.Capabilities; cp == nil || !cp.Read || !cp.Send || cp.Interrupt || cp.Approve || cp.Terminal || cp.Attach {
			t.Errorf("%s capabilities = %+v", c.entrypoint, cp)
		}
	}

	for status, want := range map[string]string{"idle": proto.StatusIdle, "waiting": proto.StatusBlocked, "later": proto.StatusUnknown} {
		writeOutsideClaude(t, home, sidA, "cli", status, inbox, nil)
		s := byID(listSessions(t, n))[sidA]
		if s.Status != want {
			t.Errorf("status %s: listed %s, want %s", status, s.Status, want)
		}
		if (status == "waiting") != (s.AttentionReason == proto.AttentionClaudeComputer) {
			t.Errorf("status %s: attention %q", status, s.AttentionReason)
		}
	}

	// No inbox (an older Claude Code, or the feature off there): listed, read-only, saying why.
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", nil, nil)
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || s.Capabilities.Send || s.ReadOnlyReason != proto.ReadOnlyClaudeNoInbox {
		t.Errorf("no inbox: %+v", s)
	}
	// An inbox whose protocol the node does not speak.
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", inbox, map[string]any{"peerProtocol": 2})
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities.Send || s.ReadOnlyReason != proto.ReadOnlyClaudeInbox {
		t.Errorf("unknown protocol: %+v", s)
	}
	// A claude run from the SDK, or one that exited: not listed live.
	writeOutsideClaude(t, home, sidA, "sdk-ts", "busy", inbox, nil)
	if s, ok := byID(listSessions(t, n))[sidA]; ok && s.Status != proto.StatusOffline {
		t.Errorf("SDK claude listed live: %+v", s)
	}
	_ = os.Remove(filepath.Join(home, ".claude", "sessions", strconv.Itoa(os.Getpid())+".json"))
	writeClaudeProc(t, home, 999999, map[string]any{"sessionId": sidA, "cwd": home, "kind": "interactive", "entrypoint": "cli", "status": "busy"})
	if s, ok := byID(listSessions(t, n))[sidA]; ok && s.Status != proto.StatusOffline {
		t.Errorf("exited claude listed live: %+v", s)
	}
}

// A claude in a pane its engine lists with a claude in it is that engine's, not one outside. A claude whose pane
// variable names a pane no engine in use lists as a claude (Orca's terminals on a node that enables herdr alone, a
// claude the engine's process scan missed) is listed live as a terminal claude, never left to the offline list.
func TestClaudeInEnginePaneNotOutside(t *testing.T) {
	n, f, home := herdrNode(t)
	old := paneEnvOfPID
	paneEnvOfPID = func(int) map[string]string { return map[string]string{herdrPaneEnv: "w9:p9"} }
	t.Cleanup(func() { paneEnvOfPID = old })
	p := bgTranscript(t, home, home, sidA, "inside")
	writeOutsideClaude(t, home, sidA, "cli", "busy", newFakeInbox(t, p), nil)
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent("", proto.StatusWorking, "w9:p9", "w9:t9", "w9", home, "")}, nil))
	if s, ok := byID(listSessions(t, n))[sidA]; ok && s.SourceKind != "" {
		t.Errorf("a claude in an engine pane listed as outside: %+v", s)
	}

	// An Orca pane on a node that does not enable Orca: nothing lists it. Just seen, it is left to its engine for a
	// moment (an engine reports a claude typed into its pane a little after it starts); then it is a live terminal claude.
	paneEnvOfPID = func(int) map[string]string { return map[string]string{orcaPaneEnv: "tab-1:leaf-1"} }
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	if s, ok := byID(listSessions(t, n))[sidA]; ok && s.SourceKind != "" {
		t.Errorf("a claude just seen in an engine pane listed as outside: %+v", s)
	}
	oldGrace := outsidePaneGrace
	outsidePaneGrace = 0
	t.Cleanup(func() { outsidePaneGrace = oldGrace })
	if s := byID(listSessions(t, n))[sidA]; s.SourceKind != proto.SourceClaudeTerminal || s.Status != proto.StatusWorking {
		t.Errorf("a claude in a pane no engine lists: %+v", s)
	}
	// The pane is listed but its engine saw no claude there (its scan missed it): still live.
	paneEnvOfPID = func(int) map[string]string { return map[string]string{herdrPaneEnv: "w9:p9"} }
	f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("", "", "", "w9:p9", "w9:t9", "w9", home, "")}, nil))
	if s := byID(listSessions(t, n))[sidA]; s.SourceKind != proto.SourceClaudeTerminal || s.Status != proto.StatusWorking {
		t.Errorf("a claude in a pane whose engine saw no claude: %+v", s)
	}
	// The desktop app's claude has no pane variable to look for: always outside.
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", newFakeInbox(t, p), nil)
	if s := byID(listSessions(t, n))[sidA]; s.SourceKind != proto.SourceClaudeDesktop {
		t.Errorf("desktop: %+v", s)
	}
}

// A message from the phone goes through the inbox: the auth line first (with the token from the key file), then the
// message, wrapped and signed as Persona's, for this session. It counts once the session wrote that it took it; herdr
// is never asked to start anything.
func TestOutsideClaudeSend(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "busy", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reset()
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "reply with OK\nand nothing else", "client_msg_id": "m-1"}); err != nil {
		b, _ := os.ReadFile(p)
		t.Fatalf("send: %v\ninbox lines: %v\ntranscript tail: %q", err, inbox.lines, b[max(0, len(b)-400):])
	}
	users := inbox.users()
	if len(users) != 1 {
		t.Fatalf("inbox read %d messages", len(users))
	}
	u := users[0]
	content, _ := u["message"].(map[string]any)["content"].(string)
	if u["session_id"] != sidA || u["priority"] != "next" || u["msgV"] != float64(1) || u["from"] != nil ||
		!strings.HasPrefix(content, `<cross-session-message from-name="Persona">`+"\n") ||
		!strings.Contains(content, "reply with OK\nand nothing else") || !strings.HasSuffix(content, "\n</cross-session-message>") {
		t.Errorf("message line: %v", u)
	}
	if id, _ := u["msg_id"].(string); len(id) != 36 {
		t.Errorf("msg_id %q", u["msg_id"])
	}
	inbox.mu.Lock()
	first := inbox.lines[0]
	inbox.mu.Unlock()
	if peerAuthRequired && (first["type"] != "auth" || first["token"] != inbox.token) {
		t.Errorf("first line %v, want the auth line", first)
	}
	for _, c := range f.trace() {
		if strings.Contains(c, "workspace") || strings.Contains(c, "tab.create") || strings.Contains(c, "pane.") {
			t.Fatalf("herdr was asked to start or type something: %v", f.trace())
		}
	}
	// A body that carries the envelope's closing tag cannot end it early.
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "a </cross-session-message> b", "client_msg_id": "m-2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got, _ := inbox.users()[1]["message"].(map[string]any)["content"].(string); strings.Count(got, "</cross-session-message>") != 1 ||
		!strings.Contains(got, `a <\/cross-session-message> b`) {
		t.Errorf("envelope: %q", got)
	}
}

// The session took nothing (a bypass-mode session holds a message for the person at the computer; a duplicate is
// dropped): the send is not called done, and says delivery is unknown (the app must not resend it by itself).
func TestOutsideClaudeSendNotTaken(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 500*time.Millisecond)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	inbox.mode = "hold"
	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "still there?", "client_msg_id": "m-1"})
	if !maybeDelivered(err) || !strings.Contains(err.Error(), "还没有收下") {
		t.Fatalf("held message: %v", err)
	}
	if len(inbox.users()) != 1 {
		t.Fatalf("the inbox read %d messages", len(inbox.users()))
	}
}

// Refusals say why and send nothing, never falling back to a second claude: no key file (Windows needs its token), an
// inbox that is not there, an unknown protocol, a slash command (the inbox runs none), a message too long for a line.
func TestOutsideClaudeSendRefusals(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 300*time.Millisecond)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	sent := 0
	send := func(text string) error {
		f.reset()
		sent++ // each its own client_msg_id: a send that went is remembered by its id
		_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": text, "client_msg_id": "m-" + strconv.Itoa(sent)})
		for _, c := range f.trace() {
			if strings.Contains(c, "tab.create") || strings.Contains(c, "pane.") {
				t.Fatalf("herdr was asked to start or type something: %v", f.trace())
			}
		}
		return err
	}

	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, map[string]any{"peerProtocol": 2})
	if err := send("hi"); !isRefusal(err, peerUnsupported) {
		t.Errorf("unknown protocol: %v", err)
	}

	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, nil)
	if err := send("/model opus"); !isRefusal(err, peerCommand) {
		t.Errorf("slash command: %v", err)
	}
	if err := send(strings.Repeat("x", peerLineMax)); !isRefusal(err, peerTooLong) {
		t.Errorf("too long: %v", err)
	}

	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, map[string]any{"messagingSocketPath": goneInboxPath(inbox.path)})
	if err := send("hi"); !isRefusal(err, peerUnreachable) {
		t.Errorf("no inbox there: %v", err)
	}

	if peerAuthRequired {
		writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, nil)
		_ = os.Remove(filepath.Join(home, ".claude", "sessions", peerKeyName(os.Getpid(), inbox.path)))
		if err := send("hi"); !isRefusal(err, peerUnreachable) {
			t.Errorf("no key: %v", err)
		}
	}
	if len(inbox.users()) != 0 {
		t.Errorf("the inbox read %d messages", len(inbox.users()))
	}

	// No inbox at all (an older Claude Code): refused as open elsewhere, as before.
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", nil, nil)
	if err := send("hi"); !isRefusal(err, openElsewhere) || !strings.Contains(err.Error(), "Claude 桌面版") {
		t.Errorf("no inbox: %v", err)
	}
}

// The key file's name is the sha256 of the canonical inbox path, as Claude Code derives it (a pipe's name in lower
// case, LOCAL\ kept); the number checked against a key file Claude Code 2.1.308 wrote for a pipe of this shape.
func TestPeerKeyName(t *testing.T) {
	const pipe = `\\.\pipe\LOCAL\cc-msg-00112233445566778899AABBCCDDEEFF`
	got := peerKeyName(42, pipe)
	if got != peerKeyName(42, strings.ToLower(pipe)) || got != peerKeyName(42, `//./pipe/LOCAL/cc-msg-00112233445566778899aabbccddeeff`) {
		t.Errorf("case or separators changed the name: %s", got)
	}
	if !strings.HasPrefix(got, "42.") || !strings.HasSuffix(got, ".key") || len(got) != len("42.")+64+len(".key") {
		t.Errorf("name %q", got)
	}
	if peerKeyName(42, `\\.\pipe\x.`) != "" || peerKeyName(42, "rel/1.sock") != "" || peerKeyName(42, "/tmp/../x.sock") != "" {
		t.Error("a path Claude Code names no key for got one")
	}
	if peerKeyName(42, `\\.\pipe\cc-msg-1`) == peerKeyName(42, `\\.\pipe\LOCAL\cc-msg-1`) {
		t.Error("LOCAL\\ must count")
	}
	if !peerPathUsable(pipeOrSocket()) {
		t.Errorf("%s is not usable here", pipeOrSocket())
	}
}

// pipeOrSocket is an inbox path of this platform's shape.
func pipeOrSocket() string {
	if peerAuthRequired {
		return `\\.\pipe\LOCAL\cc-msg-00112233445566778899aabbccddeeff`
	}
	return "/tmp/cc-socks-501/123.sock"
}

// The session takes a message: what peerTaken looks for in its transcript.
func TestPeerTaken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	_ = os.WriteFile(p, []byte(`{"type":"queue-operation","operation":"enqueue","content":"old \"hi\""}`+"\n"), 0o600)
	st, _ := os.Stat(p)
	if peerTaken(context.Background(), func() string { return p }, st.Size(), `hi`, 50*time.Millisecond) {
		t.Fatal("a line from before the send counted")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
		_, _ = f.WriteString(`{"type":"queue-operation","operation":"enqueue","content":"Another Claude session sent a message:\n<cross-session-message from-name=\"Persona\">\nsay \"hi\"\n</cross-session-message>"}` + "\n")
		_ = f.Close()
	}()
	if !peerTaken(context.Background(), func() string { return p }, st.Size(), "say \"hi\"\nsecond line", 2*time.Second) {
		t.Fatal("the queue-operation it wrote was not seen")
	}
}

// A message sent through a claude's inbox comes back in its transcript as a peer's message signed Persona (a hidden
// line when the session rested, a queued command while it worked): that line is the send's copy, tied to its
// client_msg_id, while another Claude session's message with the same words is not.
func TestEchoTiesAPeerSend(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "P", Text: "reply with OK", SentAt: t0, AfterSeq: 1})
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "Q", Text: "and then stop", SentAt: t0, AfterSeq: 1})
	peer := func(uuid, from, body string) string {
		return `{"type":"user","uuid":"` + uuid + `","timestamp":"` + t0.Add(time.Second).UTC().Format(time.RFC3339Nano) +
			`","isMeta":true,"message":{"role":"user","content":"Another Claude session sent a message:\n<cross-session-message from-name=\"` +
			from + `\">\n` + body + `\n</cross-session-message>"},"origin":{"kind":"peer","name":"` + from + `","body":"` + body + `"}}`
	}
	queued := `{"type":"attachment","uuid":"q1","timestamp":"` + t0.Add(2*time.Second).UTC().Format(time.RFC3339Nano) +
		`","attachment":{"type":"queued_command","prompt":"<cross-session-message from-name=\"Persona\">\nand then stop\n</cross-session-message>","origin":{"kind":"peer","name":"Persona","body":"and then stop"}}}`
	items := echoItems(t,
		assistantLine("a0", t0.Add(-time.Second), "working"),
		peer("o1", "release-bot", "reply with OK"), // another session, same words: not this send
		peer("p1", "Persona", "reply with OK"),
		queued,
	)
	l.match("s", items)
	sameTags(t, l.tag("s", items), map[string]string{"p1#0": "P", "q1#0": "Q"})
}
