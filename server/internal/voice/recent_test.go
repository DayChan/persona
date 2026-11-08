package voice

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/prompts"
)

// What a handover is sent with of the conversation, and what a new session is told was said, go in the order it was
// said (#5506): the measured service sends the user's caption 1.2–1.4 s after they stop, after the model began to
// answer (docs/lessons/voice.md "Timeline").

// captionConversation is a conversation that takes upstream events with no upstream or app behind it.
func captionConversation(t *testing.T) *conversation {
	return &conversation{ctx: t.Context(), out: make(chan outFrame, 256), delegations: map[string]int{}, pending: map[string]*time.Timer{}}
}

// turn plays one turn the way the measured service sends it: the user stops talking (the app's voice), the model
// begins to answer, and only then the user's caption comes.
func (c *conversation) turn(user, answer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pace.voice = time.Now()
	time.Sleep(2 * time.Millisecond)
	c.handle(0, event{kind: evAssistant, text: answer, name: "transcript"})
	time.Sleep(2 * time.Millisecond)
	c.handle(0, event{kind: evUser, text: user})
}

func lineTexts(lines []prompts.VoiceLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Role+":"+l.Text)
	}
	return out
}

// Alternating turns: every caption comes after the model's answer to it, and is put back before it, in what a
// handover is sent with (the last caption still open) and in what a new session is told (the finished lines). The app
// gets its frames as they came.
func TestRecentInTheOrderSaid(t *testing.T) {
	c := captionConversation(t)
	c.turn("你好，现在几点了？", "现在下午三点。")
	c.turn("明天呢？", "明天是周六。")
	c.turn("帮我把 hello.txt 删掉", "好，这就交给它去删 hello.txt。")
	c.mu.Lock()
	got := lineTexts(c.recent(recentLines))
	history := lineTexts(voiceLines(c.history))
	c.mu.Unlock()
	want := []string{"user:你好，现在几点了？", "assistant:现在下午三点。", "user:明天呢？", "assistant:明天是周六。",
		"user:帮我把 hello.txt 删掉", "assistant:好，这就交给它去删 hello.txt。"}
	if !slices.Equal(got, want) {
		t.Fatalf("recent:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !slices.Equal(history, slices.Delete(slices.Clone(want), 4, 5)) { // the last caption is still open: not in history yet
		t.Fatalf("history:\n%s", strings.Join(history, "\n"))
	}
	var ids []string
	for len(c.out) > 0 {
		var f map[string]any
		_ = json.Unmarshal((<-c.out).text, &f)
		if f["final"] == true {
			ids = append(ids, f["id"].(string))
		}
	}
	if !slices.Equal(ids, []string{"a1", "u1", "a2", "u2", "a3"}) {
		t.Fatalf("the app's final lines: %v", ids)
	}
}

// The user's caption is placed by when they stopped talking: a line of the model's that began before that (it spoke
// while the user still talked, or the user typed) stays before it; nor does a caption go before a line handed back by
// the app (no time), or a caption of the user's before it. The app's voice too old to be of the caption (a line too
// quiet for the energy rule) leaves it where it came (TestSpokeAt).
func TestPlaceKeepsWhatCameBefore(t *testing.T) {
	t0 := time.Now()
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	line := func(role, text string, at time.Time) heardLine {
		return heardLine{VoiceLine: prompts.VoiceLine{Role: role, Text: text}, at: at}
	}
	lines := []heardLine{line("user", "app", time.Time{}), line("assistant", "handed back", time.Time{})}
	lines = place(lines, line("assistant", "before", at(0)))
	lines = place(lines, line("assistant", "after", at(900)))
	lines = place(lines, line("user", "said", at(500)))
	lines = place(lines, line("user", "typed", at(1000)))
	lines = place(lines, line("assistant", "later", at(1100)))
	lines = place(lines, line("user", "old", at(400)))
	if got := lineTexts(voiceLines(lines)); !slices.Equal(got, []string{"user:app", "assistant:handed back", "assistant:before",
		"user:said", "assistant:after", "user:typed", "user:old", "assistant:later"}) {
		t.Fatalf("placed: %v", got)
	}
}

// A handover whose gate is open already (the user spoke before) waits for the caption still on its way: the app's
// voice came after the last caption. It goes once that caption is in, or after gateWait without it. whenSpoken (a
// stop, an answer to a card) does not wait for it.
func TestWhenSaidWaitsForTheCaptionOnItsWay(t *testing.T) {
	setVar(t, &gateWait, 600*time.Millisecond)
	c := captionConversation(t)
	now := time.Now()
	c.gate.user = now.Add(-2 * time.Second) // the earlier line's caption: the gate is open
	c.pace.voice = now                      // the user said something since

	spoken := make(chan bool, 2)
	c.whenSpoken("test", time.Time{}, func(s bool) { spoken <- s })
	select {
	case s := <-spoken:
		if !s {
			t.Fatal("whenSpoken: gate closed")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("whenSpoken waited for the caption")
	}

	start := time.Now()
	c.whenSaid("test", time.Time{}, "", func(s bool) { spoken <- s })
	select {
	case <-spoken:
		t.Fatal("whenSaid went before the caption came")
	case <-time.After(250 * time.Millisecond):
	}
	c.mu.Lock()
	c.gate.user = time.Now() // the caption
	c.mu.Unlock()
	select {
	case s := <-spoken:
		if took := time.Since(start); !s || took >= gateWait {
			t.Fatalf("spoken %v after %v", s, took)
		}
	case <-time.After(time.Second):
		t.Fatal("whenSaid never went")
	}

	// No caption ever comes (a cough, a backchannel the service does not caption): it goes after gateWait, the gate open.
	c.mu.Lock()
	c.pace.voice = time.Now()
	c.mu.Unlock()
	start = time.Now()
	c.whenSaid("test", time.Time{}, "", func(s bool) { spoken <- s })
	select {
	case s := <-spoken:
		if took := time.Since(start); !s || took < gateWait-50*time.Millisecond {
			t.Fatalf("spoken %v after %v", s, took)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("whenSaid never went")
	}
}

// #5790: the model's reply that handed this over only called the tool (it was over without any audio). The measured
// service holds the user's caption until the call's result, so a handover the user's voice opened goes at once instead
// of waiting gateWait for it. A reply that spoke still waits for the caption on its way, and a silent reply does not
// open a gate that is closed.
func TestWhenSaidGoesAtOnceAfterASilentReply(t *testing.T) {
	setVar(t, &gateWait, 600*time.Millisecond)
	c := captionConversation(t)
	c.pl = &Plugin{logger: log.New(io.Discard, "", 0)}
	reply := func(id string, voiced bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.handle(0, event{kind: evReply, id: id})
		if voiced {
			c.handle(0, event{kind: evAudio, audio: make([]byte, 320)})
		}
		c.handle(0, event{kind: evTurnEnd, id: id})
	}
	when := func(open bool, id string) (bool, time.Duration) {
		c.mu.Lock()
		now := time.Now()
		c.gate = speechGate{}
		if open {
			c.gate.user = now.Add(-2 * time.Second) // an earlier caption: the gate is open
		}
		c.pace.voice = now // the user said something since: its caption is due
		spoken := make(chan bool, 1)
		c.whenSaid("test", time.Time{}, id, func(s bool) { spoken <- s })
		c.mu.Unlock()
		start := time.Now()
		select {
		case s := <-spoken:
			return s, time.Since(start)
		case <-time.After(2 * time.Second):
			t.Fatal("whenSaid never went")
			return false, 0
		}
	}

	reply("r1", false)
	if s, took := when(true, "r1"); !s || took >= gateWait/2 {
		t.Fatalf("after a silent reply: spoken %v after %v", s, took)
	}
	reply("r2", true)
	if s, took := when(true, "r2"); !s || took < gateWait-50*time.Millisecond {
		t.Fatalf("after a reply that spoke: spoken %v after %v", s, took)
	}
	if s, took := when(false, "r1"); s || took < gateWait-50*time.Millisecond {
		t.Fatalf("closed gate after a silent reply: spoken %v after %v", s, took)
	}
}

// The issue's call end to end (protocol 2, bound to an agent): the user asked the time, then asks for a file. The
// model answers and calls delegate_task before the caption of that request comes (0.3 s later): the gate is open (the
// first question), and the message waits for the caption. It goes with both turns, each question before its answer.
func TestHandoverHasTheRequestThatTriggeredIt(t *testing.T) {
	hub := agentHub(t)
	var mu sync.Mutex
	var conn *websocket.Conn
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] == "session.start" {
			mu.Lock()
			conn = c
			mu.Unlock()
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		}
	})
	pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(true)}}}, hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	mu.Lock()
	c := conn
	mu.Unlock()
	say := func(typ, text string) { up.send(c, map[string]any{"type": typ, "delta": text}) }

	a.speak(400*time.Millisecond, true)
	a.speak(100*time.Millisecond, false)
	time.Sleep(50 * time.Millisecond)
	say("session.output_transcript.delta", "现在下午三点。")
	say("session.input_transcript.delta", "你好，现在几点了？")
	a.until(func(f map[string]any) bool { return f["type"] == "transcript" && f["role"] == "user" })

	a.speak(400*time.Millisecond, true)
	a.speak(100*time.Millisecond, false)
	time.Sleep(50 * time.Millisecond)
	say("session.output_transcript.delta", "好，这就交给它去建 hello.txt。")
	up.send(c, map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "dlg1", "target": "client"}})
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "dlg1", "event": map[string]any{
		"type": "function_call", "call_id": "c1", "name": "delegate_task", "arguments": `{"task":"在当前目录建 hello.txt，内容写 hi"}`,
	}})
	time.Sleep(300 * time.Millisecond)
	say("session.input_transcript.delta", "帮我在当前目录建一个文件 hello.txt，内容写 hi")

	m := hub.waitSent(1)
	want := strings.Join([]string{"我：你好，现在几点了？", "语音助手：现在下午三点。", "我：帮我在当前目录建一个文件 hello.txt，内容写 hi",
		"语音助手：好，这就交给它去建 hello.txt。"}, "\n")
	if !strings.HasPrefix(m.Text, "在当前目录建 hello.txt，内容写 hi\n") || !strings.Contains(m.Text, want) {
		t.Fatalf("sent:\n%s\nwant it to have:\n%s", m.Text, want)
	}
	t.Log(m.Text)
}

// A reply that only calls a tool may come with whitespace for its words (#5587, end to end: 2 of 6 of a call's model
// captions were ""): no line is opened for it, and the app never gets a caption without words, its end neither. A line
// with words is shown and ended as before.
func TestNoCaptionWithoutWords(t *testing.T) {
	hub := agentHub(t)
	var mu sync.Mutex
	var conn *websocket.Conn
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] == "session.start" {
			mu.Lock()
			conn = c
			mu.Unlock()
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		}
	})
	pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(true)}}}, hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	mu.Lock()
	c := conn
	mu.Unlock()
	var got []string
	until := func(match func(map[string]any) bool) {
		t.Helper()
		a.until(func(f map[string]any) bool {
			if f["type"] == "transcript" {
				if strings.TrimSpace(f["text"].(string)) == "" {
					t.Errorf("a caption without words: %v", f)
				}
				got = append(got, fmt.Sprintf("%s:%s:%v", f["id"], f["text"], f["final"]))
			}
			return match(f)
		})
	}
	caption := func(role string, final bool) func(map[string]any) bool {
		return func(f map[string]any) bool {
			return f["type"] == "transcript" && f["role"] == role && f["final"] == final
		}
	}
	// The model's reply: whitespace for words (both ways a service sends them), then a status call; the user's caption
	// begins with whitespace too.
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": " "}})
	up.send(c, map[string]any{"type": "session.output_transcript.delta", "delta": "\n"})
	up.send(c, map[string]any{"type": "session.input_transcript.delta", "delta": "  "})
	up.send(c, map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "dlg1", "target": "client"}})
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "dlg1", "event": map[string]any{
		"type": "function_call", "call_id": "c1", "name": "delegate_task", "arguments": `{"task":"状态：到哪了"}`,
	}})
	up.send(c, map[string]any{"type": "session.input_transcript.delta", "delta": "它到哪了？"})
	until(caption("user", false))
	up.send(c, map[string]any{"type": "session.output_transcript.delta", "delta": "还在跑测试。"})
	until(caption("assistant", false))
	up.send(c, map[string]any{"type": "session.input_transcript.delta", "delta": "好的"})
	until(caption("user", false))
	want := []string{"u1:它到哪了？:false", "u1:它到哪了？:true", "a1:还在跑测试。:false", "a1:还在跑测试。:true", "u2:好的:false"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("captions:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
