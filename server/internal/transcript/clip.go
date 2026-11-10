package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// snippetLine is the first line of s, at most n runes.
func snippetLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

func clip(s string) string { return clipTo(s, maxTextLen) }

// clipTo cuts s to at most n bytes on a UTF-8 boundary and notes the original size.
func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut, note := clipParts(s, n)
	return s[:cut] + note
}

// clipParts is where clipTo cuts s (len(s) > n) and the note it adds.
func clipParts(s string, n int) (cut int, note string) {
	cut = n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return cut, fmt.Sprintf("\n…（已截断，共 %d 字节）", len(s))
}

// light is the copy of an item that pages and pushes carry; cut says something was left out (fetch it with Full).
// Long text ends in a line that says it was cut, so apps that do not know FullSize still show it truncated, not whole.
// Only a tool's output has a "View full text" button in the app, so only its note says to tap for the whole.
func light(it proto.Item, keepImage bool) (_ proto.Item, cut bool) {
	limit, note := pageTextLen, "\n…（已截断，共 %d 字节）"
	if it.Kind == proto.KindToolResult {
		limit, note = pageOutputLen, "\n…（已截断，点开看全文，共 %d 字节）"
	}
	if len(it.Text) > limit && it.Role == "user" && it.Kind == proto.KindText {
		if t, ok := lightContexts(it.Text, limit); ok { // what the user wrote stays whole, the blocks' bodies are cut
			it.FullSize, it.Text = len(it.Text), t
			cut = true
		}
	}
	if len(it.Text) > limit {
		end := pageCut(it.Text, limit)
		it.FullSize = len(it.Text)
		it.Text = it.Text[:end] + fmt.Sprintf(note, len(it.Text))
		cut = true
	}
	if it.Image != nil && it.Image.Data != "" && !keepImage {
		it.Image = it.Image.Placeholder()
		cut = true
	}
	return it, cut
}

// pageCut is where light cuts text longer than limit: s[:pageCut] is what the page keeps. It ends on a character, at
// most limit bytes in, and on a whole line (before its newline) when one ends in the last 512 bytes of that. An
// outcome's tail picks up from here (outputTail).
func pageCut(s string, limit int) int {
	end := limit
	for end > 0 && (s[end]&0xC0) == 0x80 {
		end--
	}
	if nl := strings.LastIndexByte(s[:end], '\n'); nl > end-512 { // end on a whole line when one is near
		end = nl
	}
	return end
}

const (
	minInputShare = 256 // a long string in a tool input is never cut shorter than this
	clipNoteRoom  = 48  // what clipTo's note adds to a cut string, as JSON
)

// clipJSON shortens long strings anywhere in a tool input object: a Write's content, but also push_files'
// files[].content, MultiEdit's edits[].new_string or an MCP call's nested blocks, which went out whole in every page and
// push. The shape stays, so the apps' renderers still find their fields. A value that is not an object is left as it is:
// a Claude tool_use's input and an MCP call's arguments are always objects.
func clipJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) <= maxInputLen {
		return raw
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	b, _ := json.Marshal(mapStrings(m, func(s string) string { return clipTo(s, maxInputLen/2) }))
	if len(b) <= maxInputLen {
		return b
	}
	// Still too long (many medium strings): the strings over minInputShare share evenly what the rest leaves. Shorter
	// strings, keys and other values are never cut, and a share is never under minInputShare, so an input made mostly of
	// those (thousands of short array items) or of a great many long strings still goes over maxInputLen.
	long, kept := 0, 0
	rest, _ := json.Marshal(mapStrings(m, func(s string) string {
		if len(s) <= minInputShare {
			return s
		}
		long, kept = long+1, kept+min(len(s), maxInputLen/2)
		return ""
	}))
	if long == 0 {
		return b // nothing left that may be cut
	}
	// JSON escapes make a kept byte cost more than one: scale the share by what the first pass's strings cost.
	share := (maxInputLen - len(rest)) * kept / (len(b) - len(rest)) / long
	share = min(max(share-clipNoteRoom, minInputShare), maxInputLen/2)
	b, _ = json.Marshal(mapStrings(m, func(s string) string { return clipIfShorter(s, share) }))
	return b
}

// clipIfShorter is clipTo(s, n) if that is shorter as JSON, which is what a tool input carries, and s if not. A string
// more than clipNoteRoom over n always is: every byte cut costs at least one byte of JSON and the note at most
// clipNoteRoom. An unescaped ASCII string stays whole up to the note's JSON content length beyond n (37 bytes with
// a three-digit original size). Escapes change that threshold: json.Marshal writes < > & as six bytes each, so an
// HTML tail of the same raw length may cost more than the note.
func clipIfShorter(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if len(s) > n+clipNoteRoom {
		return clipTo(s, n)
	}
	cut, note := clipParts(s, n)
	// The cut is on a UTF-8 boundary, so JSON encodes the shared prefix identically in both strings, even next to
	// an escape. That prefix and the surrounding quotes cancel: only the dropped tail and note need marshalling.
	if jsonLen(s[cut:]) > jsonLen(note) {
		return s[:cut] + note
	}
	return s
}

// jsonLen is the length of s written as a JSON string.
func jsonLen(s string) int {
	b, _ := json.Marshal(s)
	return len(b)
}

// mapStrings is a copy of v, a decoded JSON value, with f applied to every string in it (object keys are kept).
func mapStrings(v any, f func(string) string) any {
	switch v := v.(type) {
	case string:
		return f(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = mapStrings(e, f)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = mapStrings(e, f)
		}
		return out
	}
	return v
}
