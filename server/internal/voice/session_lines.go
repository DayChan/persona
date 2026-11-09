package voice

import (
	"fmt"
	"github.com/nwverse/persona/server/internal/prompts"
	"slices"
	"strings"
	"time"
)

type line struct {
	id, text, source string
	at               time.Time // a user's line: when they stopped saying it; the model's: when it began
	shown            bool      // a frame of it with words went to the app
	// before is, on a user's line, the model's line it goes before in the app ("" at the end; protocol 2, #5673): its
	// caption came after the model began to answer it.
	before string
}

// show tells the app line l (role's) as it is, once it has words: a line that never had any is never shown, not even
// its end (#5587: a reply that only called a tool came as whitespace, and the app got a final caption of "").
func (c *conversation) show(l *line, role string, final bool) {
	text := strings.TrimSpace(l.text)
	if text == "" && !l.shown {
		return
	}
	l.shown = true
	f := map[string]any{"type": "transcript", "id": l.id, "role": role, "text": text, "final": final}
	if l.before != "" {
		f["before"] = l.before
	}
	c.sendApp(f)
}

// shownLine is a line of the model's the app has, as placeUser goes by it.
type shownLine struct {
	id string
	at time.Time
}

// tailMost is the most of the model's lines since the user's last one that placeUser keeps: a caption is at most
// captionLateMost late, and the model does not begin that many lines in that time.
const tailMost = 8

// heardLine is a line of what was said, with its line's at (zero: not known, a line the app handed back).
type heardLine struct {
	prompts.VoiceLine
	at time.Time
}

// captionLateMost is the longest a user's caption comes after the voice it is of: the measured service sends the whole
// line 1.2–1.4 s after they stop (docs/lessons/voice.md "Timeline"). The app's voice older than that is of something
// before the caption (a line too quiet for the energy rule): the caption is then placed as it came.
const captionLateMost = 3 * time.Second

// captionHeldMost is the longest the caption of a new line of the user's comes after the voice it is of when nothing
// of theirs (a caption, a typed line) came since that voice: when the model's reply only calls a tool (a status), the
// measured service holds the user's caption until the reply that speaks, after the call's result (3.3 and 3.5 s after
// they stopped in 2 of 5 runs, #5673; 4.2 s in #5672). The app's voice older than that is of something else.
const captionHeldMost = 10 * time.Second

// remember adds a finished line to what was said (mu held), in the order it was said.
func (c *conversation) remember(role, text string, at time.Time) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	c.history = place(c.history, heardLine{VoiceLine: prompts.VoiceLine{Role: role, Text: text}, at: at})
	if len(c.history) > historyLines {
		c.history = c.history[len(c.history)-historyLines:]
	}
}

// place adds l to lines in the order they were said. The user's caption comes after they stop, often after the model
// has begun to answer them (docs/lessons/voice.md "Timeline"): a user's line goes before the model's lines at the end
// that began after the user stopped saying it. Never before another line of the user's, nor a line whose time is not
// known.
func place(lines []heardLine, l heardLine) []heardLine {
	i := len(lines)
	if l.Role == "user" && !l.at.IsZero() {
		for i > 0 && lines[i-1].Role == "assistant" && !lines[i-1].at.IsZero() && lines[i-1].at.After(l.at) {
			i--
		}
	}
	return slices.Insert(lines, i, l)
}

func voiceLines(lines []heardLine) []prompts.VoiceLine {
	out := make([]prompts.VoiceLine, len(lines))
	for i, l := range lines {
		out[i] = l.VoiceLine
	}
	return out
}

// spokeAt is when the user stopped saying what a caption arriving at now is of (mu held): the app's audio last had
// their voice in it, unless that is too long ago to be it or there is none (no audio: now). Too long ago is past
// captionLateMost; for the first caption of a line, when nothing of the user's came since that voice (gate.user: it
// has had no caption yet), past captionHeldMost.
func (c *conversation) spokeAt(now time.Time) time.Time {
	v := c.pace.voice
	switch {
	case v.IsZero() || v.After(now):
		return now
	case now.Sub(v) <= captionLateMost:
		return v
	case c.user == nil && v.After(c.gate.user) && now.Sub(v) <= captionHeldMost:
		return v
	}
	return now
}

// userText adds to the user's line; spoke is when they stopped saying text (zero for no text).
//
// The measured service sends the caption 1.2–1.4 s after they stop, often after the model began to answer them
// (docs/lessons/voice.md "Timeline"). On protocol 2 such a line goes before the model's lines that began after spoke
// (placeUser: the frame's before), and the model's line still open is not ended by it where the service ends the
// model's turns itself (turnsEnd): the rest of the answer stays in the same line (#5673). Without that the caption is
// the only end the model's line has, and it still ends it. Protocol 1's frames are as they always were.
func (c *conversation) userText(text string, replace, final bool, spoke time.Time) {
	if c.user == nil {
		if strings.TrimSpace(text) == "" { // no words: no line (#5587)
			return
		}
		before := ""
		if c.version >= 2 {
			before = c.placeUser(spoke)
		}
		if before == "" || !c.turnsEnd {
			c.closeAsst()
		}
		c.userSeq++
		c.user = &line{id: fmt.Sprintf("u%d", c.userSeq), before: before}
	}
	if replace {
		c.user.text = text
	} else {
		c.user.text += text
	}
	if strings.TrimSpace(text) != "" && !spoke.IsZero() {
		c.user.at = spoke
	}
	c.show(c.user, "user", final)
	if final {
		c.remember("user", c.user.text, c.user.at)
		c.user = nil
	}
}

func (c *conversation) closeUser() {
	if c.user != nil {
		c.userText("", false, true, time.Time{})
	}
}

// placeUser is the model's line a new line of the user's, of what they stopped saying at spoke, goes before in the app
// (mu held; "" for at the end): the first of the model's lines after the user's last one from which all began after
// spoke, as place puts it in what was said. Those stay after the new line, the others are before it now: a later
// caption never goes before this one.
func (c *conversation) placeUser(spoke time.Time) string {
	i := len(c.tail)
	for i > 0 && !spoke.IsZero() && c.tail[i-1].at.After(spoke) {
		i--
	}
	before := ""
	if i < len(c.tail) {
		before = c.tail[i].id
	}
	c.tail = slices.Clone(c.tail[i:])
	return before
}

// asstText: source "transcript" or "text" for a service that sends the model's words both ways (only one is shown).
func (c *conversation) asstText(text string, replace, final bool, source string) {
	if source == "transcript" {
		c.sawTranscript = true
	} else if source == "text" && c.sawTranscript {
		return
	}
	if c.asst == nil {
		if strings.TrimSpace(text) == "" { // no words: no line (a reply that only calls a tool may begin with whitespace, #5587)
			return
		}
		c.closeUser()
		c.asstSeq++
		c.asst = &line{id: fmt.Sprintf("a%d", c.asstSeq), source: source, at: time.Now()}
		if c.version >= 2 { // it has words: it is shown below
			if c.tail = append(c.tail, shownLine{id: c.asst.id, at: c.asst.at}); len(c.tail) > tailMost {
				c.tail = c.tail[len(c.tail)-tailMost:]
			}
		}
	}
	if source == "transcript" && c.asst.source == "text" {
		c.asst.text, c.asst.source = "", source
	}
	if replace {
		c.asst.text = text
	} else {
		c.asst.text += text
	}
	c.show(c.asst, "assistant", final)
	if final {
		c.remember("assistant", c.asst.text, c.asst.at)
		c.asst = nil
	}
}

func (c *conversation) closeAsst() {
	if c.asst != nil {
		c.asstText("", false, true, c.asst.source)
	}
}

// recent is the conversation's last n lines in the order they were said, the user's line still open (its caption so
// far) among them.
func (c *conversation) recent(n int) []prompts.VoiceLine {
	lines := slices.Clone(c.history)
	if c.user != nil && strings.TrimSpace(c.user.text) != "" {
		lines = place(lines, heardLine{VoiceLine: prompts.VoiceLine{Role: "user", Text: strings.TrimSpace(c.user.text)}, at: c.user.at})
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return clipHistory(voiceLines(lines))
}

func (c *conversation) lastUserLine() string {
	if c.user != nil && strings.TrimSpace(c.user.text) != "" {
		return strings.TrimSpace(c.user.text)
	}
	for i := len(c.history) - 1; i >= 0; i-- {
		if c.history[i].Role == "user" {
			return c.history[i].Text
		}
	}
	return ""
}

func clipHistory(lines []prompts.VoiceLine) []prompts.VoiceLine {
	out := make([]prompts.VoiceLine, len(lines))
	for i, l := range lines {
		out[i] = prompts.VoiceLine{Role: l.Role, Text: clipRunes(l.Text, 200)}
	}
	return out
}
