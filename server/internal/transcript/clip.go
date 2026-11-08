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
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n…（已截断，共 %d 字节）", len(s))
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
		end := limit
		for end > 0 && (it.Text[end]&0xC0) == 0x80 {
			end--
		}
		if nl := strings.LastIndexByte(it.Text[:end], '\n'); nl > end-512 { // end on a whole line when one is near
			end = nl
		}
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

// clipJSON shortens very long string fields (e.g. Write content) inside tool inputs.
func clipJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) <= maxInputLen {
		return raw
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	for k, v := range m {
		if s, ok := v.(string); ok && len(s) > maxInputLen/2 {
			m[k] = clipTo(s, maxInputLen/2)
		}
	}
	b, _ := json.Marshal(m)
	return b
}
