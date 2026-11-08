package voice

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The app's rows go in the order they were said (#5673): the measured service sends the user's caption 1.2–1.4 s after
// they stop, after the model began to answer (docs/lessons/voice.md "Timeline"). On protocol 2 the caption's frame says
// which of the model's lines it goes before, and the model's line it came in the middle of goes on in the same line
// where the service ends the model's turns itself. Protocol 1's frames are as they were.

// captionFrames takes the transcript frames queued for the app, as "id:text:final", with "<before" after the id of a
// line placed before another.
func captionFrames(t *testing.T, c *conversation) []string {
	t.Helper()
	var out []string
	for len(c.out) > 0 {
		var f map[string]any
		if err := json.Unmarshal((<-c.out).text, &f); err != nil {
			t.Fatal(err)
		}
		if f["type"] == "transcript" {
			out = append(out, captionFrame(f))
		}
	}
	return out
}

func captionFrame(f map[string]any) string {
	id := f["id"].(string)
	if b, ok := f["before"]; ok {
		id += "<" + b.(string)
	}
	return fmt.Sprintf("%s:%s:%v", id, f["text"], f["final"])
}

// appRows is the order the app puts frames' lines in: a line it has not seen goes before the line its before names,
// when it has that one, else at the end; a line it has seen stays where it is (VoiceConversation.caption).
func appRows(frames []string) []string {
	var rows []string
	for _, f := range frames {
		head, _, _ := strings.Cut(f, ":")
		id, before, _ := strings.Cut(head, "<")
		if slices.Contains(rows, id) {
			continue
		}
		if i := slices.Index(rows, before); before != "" && i >= 0 {
			rows = slices.Insert(rows, i, id)
		} else {
			rows = append(rows, id)
		}
	}
	return rows
}

// lateCaption plays the issue's turn on c: the user stops talking, the model begins to answer (its reply r1, when the
// service names its replies), the user's caption comes, then the rest of the answer and the reply's end.
func lateCaption(c *conversation, signals bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if signals {
		c.handle(0, event{kind: evReply, id: "r1"})
	}
	c.pace.voice = time.Now()
	time.Sleep(2 * time.Millisecond)
	c.handle(0, event{kind: evAssistant, text: "它还停在原地，在屏幕上等你回答部署环境那个问题，还没往下", name: "text"})
	time.Sleep(2 * time.Millisecond)
	c.handle(0, event{kind: evUser, text: "它现在到哪了？"})
	c.handle(0, event{kind: evAssistant, text: "做。", name: "text"})
	if signals {
		c.handle(0, event{kind: evTurnEnd, id: "r1"})
	}
	c.closeUser() // the speaker, after captionQuiet
	c.closeAsst()
}

func TestLateCaptionGoesBeforeTheAnswer(t *testing.T) {
	c := captionConversation(t)
	c.version = 2
	lateCaption(c, true)
	got := captionFrames(t, c)
	answer := "它还停在原地，在屏幕上等你回答部署环境那个问题，还没往下"
	want := []string{"a1:" + answer + ":false", "u1<a1:它现在到哪了？:false", "a1:" + answer + "做。:false",
		"a1:" + answer + "做。:true", "u1<a1:它现在到哪了？:true"}
	if !slices.Equal(got, want) {
		t.Fatalf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rows := appRows(got); !slices.Equal(rows, []string{"u1", "a1"}) {
		t.Fatalf("the app's rows: %v", rows)
	}
	c.mu.Lock()
	history := lineTexts(voiceLines(c.history))
	c.mu.Unlock()
	if !slices.Equal(history, []string{"user:它现在到哪了？", "assistant:" + answer + "做。"}) {
		t.Fatalf("history: %v", history)
	}
}

// Protocol 1 (Android, #1931): the frames as they always were, no before, the caption ending the model's line. What
// was said is still in the order it was said (#5560).
func TestLateCaptionProtocolOneUnchanged(t *testing.T) {
	c := captionConversation(t)
	c.version = 1
	lateCaption(c, true)
	got := captionFrames(t, c)
	answer := "它还停在原地，在屏幕上等你回答部署环境那个问题，还没往下"
	want := []string{"a1:" + answer + ":false", "a1:" + answer + ":true", "u1:它现在到哪了？:false", "u1:它现在到哪了？:true",
		"a2:做。:false", "a2:做。:true"}
	if !slices.Equal(got, want) {
		t.Fatalf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	c.mu.Lock()
	history := lineTexts(voiceLines(c.history))
	c.mu.Unlock()
	if !slices.Equal(history, []string{"user:它现在到哪了？", "assistant:" + answer, "assistant:做。"}) {
		t.Fatalf("history: %v", history)
	}
}

// A service that does not say when the model's turn is over (a standard upstream without response.event): the caption
// is the only end the model's line has, so it still ends it (else the next answer would run on in it), and the line
// still goes before it. The next turn's answer is a line of its own.
func TestLateCaptionWithoutTurnEnds(t *testing.T) {
	c := captionConversation(t)
	c.version = 2
	lateCaption(c, false)
	c.turn("明天呢？", "明天是周六。")
	c.mu.Lock()
	c.closeUser()
	c.closeAsst()
	c.mu.Unlock()
	got := captionFrames(t, c)
	answer := "它还停在原地，在屏幕上等你回答部署环境那个问题，还没往下"
	want := []string{"a1:" + answer + ":false", "a1:" + answer + ":true", "u1<a1:它现在到哪了？:false", "u1<a1:它现在到哪了？:true",
		"a2:做。:false", "a2:做。:true", "a3:明天是周六。:false", "a3:明天是周六。:true", "u2<a3:明天呢？:false", "u2<a3:明天呢？:true"}
	if !slices.Equal(got, want) {
		t.Fatalf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rows := appRows(got); !slices.Equal(rows, []string{"u1", "a1", "a2", "u2", "a3"}) {
		t.Fatalf("the app's rows: %v", rows)
	}
}

// A caption is of the app's voice that last had the user in it: within captionLateMost always (it may be more of a
// line already open); past that, for a line's first caption when nothing of the user's came since that voice (the
// measured service holds the caption behind a reply that only calls a tool, #5673), within captionHeldMost. Else (a
// voice that had its caption, one too old: of something too quiet for the energy rule) the caption is of now.
func TestSpokeAt(t *testing.T) {
	c := captionConversation(t)
	now := time.Now()
	if got := c.spokeAt(now); !got.Equal(now) {
		t.Fatalf("no voice: spoke at %v, want now", got)
	}
	c.pace.voice = now.Add(-1300 * time.Millisecond)
	c.gate.user = now.Add(-time.Second) // a caption since: it may be the start of this one
	if got := c.spokeAt(now); !got.Equal(c.pace.voice) {
		t.Fatalf("the voice the caption is of: spoke at %v", got)
	}
	c.pace.voice = now.Add(-captionLateMost - time.Second)
	if got := c.spokeAt(now); !got.Equal(now) {
		t.Fatalf("an old voice that had its caption: spoke at %v, want now", got)
	}
	c.gate.user = c.pace.voice.Add(-time.Second)
	if got := c.spokeAt(now); !got.Equal(c.pace.voice) {
		t.Fatalf("an old voice with no caption since (held behind a tool call): spoke at %v", got)
	}
	c.user = &line{id: "u1", text: "已经开了"}
	if got := c.spokeAt(now); !got.Equal(now) {
		t.Fatalf("more of a line already open: spoke at %v, want now", got)
	}
	c.user = nil
	c.pace.voice, c.gate.user = now.Add(-captionHeldMost-time.Second), time.Time{}
	if got := c.spokeAt(now); !got.Equal(now) {
		t.Fatalf("a voice older than captionHeldMost: spoke at %v, want now", got)
	}
}

// A caption held behind a reply that only called a tool (a status) comes 3.5 s after the user stopped, just after the
// reply that speaks began: it still goes before that reply, which stays one line (the issue's run on the measured
// service, 2 of 5).
func TestCaptionHeldBehindAToolCall(t *testing.T) {
	c := captionConversation(t)
	c.version = 2
	c.mu.Lock()
	c.pace.voice = time.Now().Add(-3500 * time.Millisecond)
	c.handle(0, event{kind: evReply, id: "r1"}) // a reply with no words: it only called a tool
	c.handle(0, event{kind: evAssistant, text: " ", name: "text"})
	c.handle(0, event{kind: evTurnEnd, id: "r1"})
	c.handle(0, event{kind: evReply, id: "r2"})
	c.handle(0, event{kind: evAssistant, text: "它停下来了，正等你回答部署到哪里，还没开始部署", name: "text"})
	c.handle(0, event{kind: evUser, text: "它现在到哪了？"})
	c.handle(0, event{kind: evAssistant, text: "。", name: "text"})
	c.handle(0, event{kind: evTurnEnd, id: "r2"})
	c.closeUser()
	c.mu.Unlock()
	got := captionFrames(t, c)
	want := []string{"a1:它停下来了，正等你回答部署到哪里，还没开始部署:false", "u1<a1:它现在到哪了？:false",
		"a1:它停下来了，正等你回答部署到哪里，还没开始部署。:false", "a1:它停下来了，正等你回答部署到哪里，还没开始部署。:true",
		"u1<a1:它现在到哪了？:true"}
	if !slices.Equal(got, want) {
		t.Fatalf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A caption of what the user said after the model's line began (they talked over it, or typed) goes at the end and
// ends that line, as before.
func TestCaptionAfterTheAnswerBeganGoesAtTheEnd(t *testing.T) {
	c := captionConversation(t)
	c.version = 2
	c.mu.Lock()
	c.handle(0, event{kind: evReply, id: "r1"})
	c.handle(0, event{kind: evAssistant, text: "我先说一段很长的话", name: "text"})
	time.Sleep(2 * time.Millisecond)
	c.pace.voice = time.Now() // the user talks while it is said
	c.handle(0, event{kind: evUser, text: "等一下"})
	c.closeUser()
	c.handle(0, event{kind: evReply, id: "r2"})
	c.handle(0, event{kind: evAssistant, text: "好，你说。", name: "text"})
	c.handle(0, event{kind: evTurnEnd, id: "r2"})
	c.userText("我打字说的", true, true, time.Now()) // a text frame
	c.mu.Unlock()
	got := captionFrames(t, c)
	want := []string{"a1:我先说一段很长的话:false", "a1:我先说一段很长的话:true", "u1:等一下:false", "u1:等一下:true",
		"a2:好，你说。:false", "a2:好，你说。:true", "u2:我打字说的:true"}
	if !slices.Equal(got, want) {
		t.Fatalf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The model began two lines before the caption came: the caption goes before the first of them. A later caption goes
// before the lines that began after its voice, never before an earlier caption of the user's.
func TestCaptionGoesBeforeEveryLineThatBeganAfterIt(t *testing.T) {
	c := captionConversation(t)
	c.version = 2
	c.mu.Lock()
	stopped := time.Now()
	c.pace.voice = stopped
	time.Sleep(2 * time.Millisecond)
	c.handle(0, event{kind: evReply, id: "r1"})
	c.handle(0, event{kind: evAssistant, text: "我看看。", name: "text"})
	c.handle(0, event{kind: evTurnEnd, id: "r1"})
	time.Sleep(2 * time.Millisecond)
	between := time.Now()
	time.Sleep(2 * time.Millisecond)
	c.handle(0, event{kind: evReply, id: "r2"})
	c.handle(0, event{kind: evAssistant, text: "还在跑测试", name: "text"})
	c.handle(0, event{kind: evUser, text: "它到哪了？"}) // of the voice that stopped before both
	c.closeUser()
	c.pace.voice = between // said after the first line began, before the second
	c.handle(0, event{kind: evUser, text: "快点"})
	c.closeUser()
	c.pace.voice = stopped // a caption of the oldest voice: still after the user's caption before it
	c.handle(0, event{kind: evUser, text: "还有"})
	c.closeUser()
	c.handle(0, event{kind: evAssistant, text: "，快了。", name: "text"})
	c.handle(0, event{kind: evTurnEnd, id: "r2"})
	c.mu.Unlock()
	got := captionFrames(t, c)
	want := []string{"a1:我看看。:false", "a1:我看看。:true", "a2:还在跑测试:false", "u1<a1:它到哪了？:false", "u1<a1:它到哪了？:true",
		"u2<a2:快点:false", "u2<a2:快点:true", "u3<a2:还有:false", "u3<a2:还有:true", "a2:还在跑测试，快了。:false",
		"a2:还在跑测试，快了。:true"}
	if !slices.Equal(got, want) {
		t.Fatalf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rows := appRows(got); !slices.Equal(rows, []string{"u1", "a1", "u2", "u3", "a2"}) {
		t.Fatalf("the app's rows: %v", rows)
	}
	c.mu.Lock()
	history := lineTexts(voiceLines(c.history))
	c.mu.Unlock()
	if !slices.Equal(history, []string{"user:它到哪了？", "assistant:我看看。", "user:快点", "user:还有", "assistant:还在跑测试，快了。"}) {
		t.Fatalf("history: %v", history)
	}
}

// The issue's turn through the socket, on the measured service's frames (protocol 2, bound to an agent): the app's
// voice stops, the model's reply begins, the caption comes, the rest of the reply and its end.
func TestLateCaptionOnTheSocket(t *testing.T) {
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
	reply := func(e map[string]any) {
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": e})
	}

	a.speak(400*time.Millisecond, true)
	a.speak(100*time.Millisecond, false)
	time.Sleep(50 * time.Millisecond)
	reply(map[string]any{"type": "response.created"})
	reply(map[string]any{"type": "response.output_text.delta", "delta": "它还停在原地，还没往下"})
	up.send(c, map[string]any{"type": "session.input_transcript.delta", "delta": "它现在到哪了？"})
	reply(map[string]any{"type": "response.output_text.delta", "delta": "做。"})
	reply(map[string]any{"type": "response.completed"})
	var got []string
	a.until(func(f map[string]any) bool {
		if f["type"] != "transcript" {
			return false
		}
		got = append(got, captionFrame(f))
		return f["role"] == "assistant" && f["final"] == true
	})
	want := []string{"a1:它还停在原地，还没往下:false", "u1<a1:它现在到哪了？:false", "a1:它还停在原地，还没往下做。:false",
		"a1:它还停在原地，还没往下做。:true"}
	if !slices.Equal(got, want) {
		t.Fatalf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
