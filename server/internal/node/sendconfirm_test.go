package node

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// appendTranscript adds lines at the end of id's transcript, as Claude writes them (the guide and confirm tests watch
// it from where it ended before the typing).
func appendTranscript(t *testing.T, home, cwd, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", nonAlnum.ReplaceAllString(cwd, "-"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

// promptLine is a message to the agent as Claude's transcript has it: text, and with image a picture of about that many
// bytes (base64 in the line, as Claude keeps a screenshot).
func promptLine(cwd, text string, image int) string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if image == 0 {
		return `{"type":"user","timestamp":"` + now + `","cwd":"` + cwd + `","message":{"role":"user","content":"` + text + `"}}`
	}
	data := base64.StdEncoding.EncodeToString(make([]byte, image))
	return `{"type":"user","timestamp":"` + now + `","cwd":"` + cwd + `","message":{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}},` +
		`{"type":"text","text":"[Image #1] ` + text + `\n\n<attached_images>\n</attached_images>"}]}}`
}

// confirmPane is a Claude in w1:p1 with an empty box that a send types "fix the flaky test" into. After the n-th
// screen read that follows the prompt (counting from 1), the box shows the prompt (0: never); Enter on it empties the
// box and, when enterTakes, the transcript gets the message (landing, when set, gives its line). It returns how many
// times Enter was pressed.
func confirmPane(t *testing.T, f *fakeHerdr, home, cwd string, showsAt int, enterTakes bool, landing ...string) (enters func() int) {
	t.Helper()
	empty := screenFixture(t, "claude-2.1.312-empty-162.txt")
	emptyANSI := screenFixture(t, "claude-2.1.312-empty-162.ansi")
	typed := screenFixture(t, "claude-2.1.312-typed-162.txt") // the box shows "fix the flaky test"
	typedANSI := screenFixture(t, "claude-2.1.312-typed-162.ansi")
	f.setScreens("w1:p1", empty, emptyANSI)
	line := promptLine(cwd, "fix the flaky test", 0)
	if len(landing) > 0 {
		line = landing[0]
	}
	var mu sync.Mutex
	prompted, reads, ne := false, 0, 0
	f.after(func(c herdrCall) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case c.Method == "agent.prompt":
			prompted = true
		case c.Method == "pane.read" && prompted:
			if reads++; showsAt > 0 && reads == showsAt && ne == 0 {
				f.setScreens("w1:p1", typed, typedANSI)
			}
		case isKeys("enter")(c):
			ne++
			if enterTakes {
				f.setScreens("w1:p1", empty, emptyANSI)
				appendTranscript(t, home, cwd, sidA, line)
			}
		}
	})
	return func() int { mu.Lock(); defer mu.Unlock(); return ne }
}

// #6428: two large screenshots took Claude seconds to take in, so the box showed nothing 2 s after the prompt; the one
// look then saw nothing to press Enter for, the send answered 200, and the text sat in the box for an hour. Now the
// box is looked at until the transcript shows the prompt went in: it shows up later, Enter goes in once, and the send
// succeeds when the transcript has it — here with a picture of 600 KB in the line, more than a quick read of the
// transcript's tail holds.
func TestSendPressesEnterForAPromptThatShowsLate(t *testing.T) {
	n, f, home, cwd := guideNode(t, true)
	withSubmitWindow(t, 10*time.Second, 20*time.Millisecond)
	enters := confirmPane(t, f, home, cwd, 5, true, promptLine(cwd, "fix the flaky test", 600<<10))
	start := time.Now()
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "fix the flaky test"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if enters() != 1 {
		t.Fatalf("Enter pressed %d times, want once", enters())
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("waited %s: the transcript's line with the picture was not seen", took)
	}
}

// Enter does not take the prompt either: it is still in the box when the window ends. The send fails with delivered
// "unknown" (the text is on the computer, not in the conversation) instead of answering success; Enter goes once.
// Meanwhile other lines land in the transcript (another session's message queued, a message of other words): they
// are not this prompt.
func TestSendAPromptStuckInTheBoxIsNotReportedDelivered(t *testing.T) {
	n, f, home, cwd := guideNode(t, true)
	withSubmitWindow(t, 400*time.Millisecond, 20*time.Millisecond)
	enters := confirmPane(t, f, home, cwd, 1, false)
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" {
			appendTranscript(t, home, cwd, sidA,
				`{"type":"queue-operation","operation":"enqueue","timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","content":"<cross-session-message from=\"other\">done</cross-session-message>"}`,
				promptLine(cwd, "run the voice check", 0))
		}
	})
	_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "fix the flaky test"})
	if err == nil {
		t.Fatal("send answered success for a prompt still in the box")
	}
	if _, data := refusalOf(t, err); data["delivered"] != deliveredUnknown || !strings.Contains(err.Error(), "输入框") {
		t.Fatalf("delivered %v: %v", data["delivered"], err)
	}
	if enters() != 1 {
		t.Fatalf("Enter pressed %d times, want once", enters())
	}
}

// While Claude works, the prompt goes into its queue: the transcript's enqueue of it confirms it at once, with no
// Enter and without waiting out the window.
func TestSendConfirmedByClaudesQueue(t *testing.T) {
	n, f, home, cwd := guideNode(t, true)
	withSubmitWindow(t, 10*time.Second, 20*time.Millisecond)
	enters := confirmPane(t, f, home, cwd, 0, false)
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" {
			appendTranscript(t, home, cwd, sidA,
				`{"type":"queue-operation","operation":"enqueue","timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","sessionId":"`+sidA+`","content":"fix the flaky test"}`)
		}
	})
	start := time.Now()
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "fix the flaky test"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("waited %s for a prompt the queue had", took)
	}
	if enters() != 0 {
		t.Fatalf("Enter pressed %d times on a queued prompt", enters())
	}
}

// Nothing in the transcript can confirm a slash command (Claude runs it itself), nor anything when there is no
// transcript: one look after the grace, as before, then success.
func TestSendWithNothingToConfirmTakesOneLook(t *testing.T) {
	for _, c := range []struct {
		name, text string
		transcript bool
	}{
		{"slash command", "/compact", true},
		{"no transcript", "fix the flaky test", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, f, home, cwd := guideNode(t, c.transcript)
			withSubmitWindow(t, 10*time.Second, 20*time.Millisecond)
			enters := confirmPane(t, f, home, cwd, 0, false)
			start := time.Now()
			if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": c.text}); err != nil {
				t.Fatalf("send: %v", err)
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("waited %s with nothing to confirm", took)
			}
			if enters() != 0 {
				t.Fatalf("Enter pressed %d times", enters())
			}
		})
	}
}

// No screen to look at after the prompt (botmux has none; here a read that cannot be made): one try, as before, not a
// wait for the window.
func TestEnsureSubmittedWithoutAScreenTakesOneLook(t *testing.T) {
	withSubmitGrace(t, 10*time.Millisecond)
	withSubmitWindow(t, 10*time.Second, 20*time.Millisecond)
	rule := strings.Repeat("─", 30)
	longMenu := rule + "\r\n❯ /re\r\n" + rule + strings.Repeat("\r\n  /review   review a pull request", 40)
	f := &fakePane{screens: []string{longMenu}, cutOver: 10, plainCutOver: 16} // no box in any rows that fit
	n := &Node{hosts: herdrHosts(f.serve(t))}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := n.ensureSubmitted(context.Background(), "w1:p1", "fix the flaky test", time.Now(), &promptWatch{path: p, off: 3, want: echoWords("fix the flaky test")}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("waited %s with no screen", took)
	}
}

// A watch counts only lines written after it began whose words start as the prompt's: a line of the transcript from
// before, another message, a task's notification are not it; a long prompt's line with its notes and chips is, and so
// is any message with a picture for a prompt of pictures only.
func TestPromptWatchKnowsItsPrompt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	before := promptLine("/x", "fix the flaky test", 0) + "\n"
	if err := os.WriteFile(p, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	add := func(lines ...string) {
		t.Helper()
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_, _ = f.WriteString(strings.Join(lines, "\n") + "\n")
	}
	long := strings.Repeat("修一下那个不稳定的测试，然后把文档也改了 ", 10)
	w := &promptWatch{path: p, off: int64(len(before)), want: echoWords(long + "\n\n<attached_images>\n/tmp/a.png\n</attached_images>")}
	if w.took() {
		t.Fatal("the line from before counted")
	}
	add(promptLine("/x", "something else", 0),
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-01T00:00:00Z","content":"<task-notification>\n<task-id>x</task-id>\n</task-notification>"}`)
	if w.took() {
		t.Fatal("another message counted")
	}
	line := promptLine("/x", long, 200<<10)
	write := func(s string) {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(s)
		f.Close()
	}
	write(line[:len(line)/2]) // written in two goes: half a line is not one
	if w.took() {
		t.Fatal("half a line counted")
	}
	write(line[len(line)/2:] + "\n")
	if !w.took() || !w.took() {
		t.Fatal("the prompt's line, notes and chips and all, not seen (or not kept)")
	}
	pictures := &promptWatch{path: p, off: int64(len(before)), want: echoWords("<attached_images>\n/tmp/a.png\n</attached_images>")}
	if !pictures.took() {
		t.Fatal("a prompt of pictures only: the message with a picture not seen")
	}
}

// A prompt of pictures only is not confirmed by a picture a tool's result carries (Claude reading a screenshot), nor
// by a queued paste's placeholder; and a transcript that shrank (written anew) confirms nothing more: its lines may be
// from before the typing.
func TestPromptWatchIgnoresToolPicturesAndRewrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	before := promptLine("/x", "fix the flaky test", 0) + "\n"
	if err := os.WriteFile(p, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(make([]byte, 64))
	_, _ = f.WriteString(`{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]}]}}` + "\n" +
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-01T00:00:01Z","content":"[Pasted text #1 +40 lines]"}` + "\n")
	f.Close()
	pictures := &promptWatch{path: p, off: int64(len(before)), want: echoWords("<attached_images>\n/tmp/a.png\n</attached_images>")}
	if pictures.took() {
		t.Fatal("a tool result's picture or a paste counted for a prompt of pictures")
	}

	w := &promptWatch{path: p, off: int64(len(before)), want: echoWords("fix the flaky test")}
	if err := os.WriteFile(p, []byte(before[:len(before)/2]), 0o600); err != nil { // written anew, shorter
		t.Fatal(err)
	}
	if w.took() || w.usable() {
		t.Fatal("a transcript written anew still confirms")
	}
	if err := os.WriteFile(p, []byte(before+before), 0o600); err != nil { // the old prompt's line, again
		t.Fatal(err)
	}
	if w.took() {
		t.Fatal("lines read again after the rewrite")
	}
}

// Only a text Claude may collapse into a `[Pasted text #n …]` placeholder takes one in the box for itself: another's
// paste over a short prompt gets no Enter.
func TestLongPaste(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{"fix the flaky test", false},
		{"fix the flaky test\n\n<attached_images>\n/a.png\n/b.png\n</attached_images>", false},
		{"fix the flaky test\n\n<attached_images>\n/a.png\npath: /a.png\n</attached_images>", true}, // its path: lines stay in the paste
		{"<attached_images>\n/a.png\npath: /a.png\n</attached_images>", false},                      // pictures only: nothing to tell by
		{"one\ntwo\nthree", true},
		{strings.Repeat("长", 800), true},
	} {
		if got := longPaste(c.text); got != c.want {
			t.Errorf("longPaste(%q) = %v", c.text, got)
		}
	}
}

// #1707: the transcript has the prompt right after it is typed, as it usually does within a second. The send answers then,
// not after the whole submitGrace; the one screen read for a guide still happens.
func TestSendAnswersOnceTheTranscriptTookThePrompt(t *testing.T) {
	n, f, home, cwd := guideNode(t, true)
	withSubmitGrace(t, 3*time.Second)
	withSubmitWindow(t, 10*time.Second, 20*time.Millisecond)
	enters := confirmPane(t, f, home, cwd, 0, false)
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" {
			appendTranscript(t, home, cwd, sidA, promptLine(cwd, "fix the flaky test", 0))
		}
	})
	start := time.Now()
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "fix the flaky test"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("send answered after %s: it waited out the grace with the prompt already in the transcript", took)
	}
	if enters() != 0 {
		t.Fatalf("Enter pressed %d times for a prompt the transcript had", enters())
	}
	reads := 0
	for _, c := range f.calls() {
		if c.Method == "pane.read" {
			reads++
		}
	}
	if reads == 0 {
		t.Fatal("no screen read after the prompt: a guide would go unseen")
	}
}

// A prompt the transcript does not get still waits the whole grace before its box is looked at: the early end is only
// for one the transcript has.
func TestSendWithoutTheTranscriptWaitsTheGrace(t *testing.T) {
	n, f, home, cwd := guideNode(t, true)
	withSubmitGrace(t, 600*time.Millisecond)
	withSubmitWindow(t, 900*time.Millisecond, 20*time.Millisecond)
	confirmPane(t, f, home, cwd, 0, false)
	var typed, firstRead time.Time
	var mu sync.Mutex
	f.after(func(c herdrCall) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case c.Method == "agent.prompt":
			typed = time.Now()
		case c.Method == "pane.read" && !typed.IsZero() && firstRead.IsZero():
			firstRead = time.Now()
		}
	})
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "fix the flaky test"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if firstRead.IsZero() || firstRead.Sub(typed) < 550*time.Millisecond {
		t.Fatalf("box first read %s after typing, want the whole grace", firstRead.Sub(typed))
	}
}
