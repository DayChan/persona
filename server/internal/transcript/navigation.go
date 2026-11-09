package transcript

import (
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

var ErrNavigationHistory = errors.New("transcript history changed")
var ErrNavigationTarget = errors.New("navigation target is not in the transcript")

const NavigationPreviewRunes = 160

// History is the identity of the currently parsed transcript tree.
func (f *File) History() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.history()
}

// Navigation returns user inputs independently of the loaded chat page. Sparse
// histories have a parsing budget and an explicit advancing cursor, even if a
// budgeted page contains no real inputs. File order and seq are authoritative.
func (f *File) Navigation(beforeSeq, limit int, history string) (proto.SessionNavigation, error) {
	return f.NavigationTagged(beforeSeq, limit, history, nil)
}

// NavigationTagged applies trusted caller provenance before classifying raw
// transcript lines. The node's echo ledger attaches these IDs after parsing.
func (f *File) NavigationTagged(beforeSeq, limit int, history string, tag func([]proto.Item) []proto.Item) (proto.SessionNavigation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 40
	}
	limit = min(max(limit, 20), 100)
	out := proto.SessionNavigation{Messages: []proto.NavigationMessage{}}
	spent := int64(0)
	for {
		end := f.before(beforeSeq)
		start := end
		out.Messages = out.Messages[:0]
		answer := ""
		for start > 0 && len(out.Messages) < limit {
			last := start
			seq := f.items[start-1].Seq
			for start > 0 && f.items[start-1].Seq == seq {
				start--
			}
			line := f.items[start:last]
			if tag != nil {
				line = tag(line)
			}
			if mark, ok := navigationMessage(line); ok {
				mark.Answer = answer
				out.Messages = append(out.Messages, mark)
			}
			for i := len(line) - 1; i >= 0; i-- {
				it := line[i]
				if it.Wake || (it.Role == "user" && it.Kind == proto.KindText) || (it.Role == "user" && it.Kind == proto.KindImage && it.ToolUseID == "") {
					answer = "" // do not attribute another input's or supervision's answer to this question
					break
				}
				if answer == "" && it.Role == "assistant" && it.Kind == proto.KindText {
					answer = navigationPreview(StripInjected(it.Text))
				}
			}
		}
		if start == 0 && f.base > 0 && (spent < pageBudget() || end == 0) {
			if got := f.loadOlder(); got > 0 {
				spent += got
				continue
			}
		}
		out.HasMore = start > 0 || f.base > 0
		if start < end && out.HasMore {
			out.NextBeforeSeq = f.items[start].Seq
		} else if out.HasMore {
			out.NextBeforeSeq = f.base + 1
		}
		out.History = f.history()
		if history != "" && history != out.History {
			return proto.SessionNavigation{}, ErrNavigationHistory
		}
		for left, right := 0, len(out.Messages)-1; left < right; left, right = left+1, right-1 {
			out.Messages[left], out.Messages[right] = out.Messages[right], out.Messages[left]
		}
		return out, nil
	}
}

func navigationMessage(line []proto.Item) (proto.NavigationMessage, bool) {
	var mark proto.NavigationMessage
	var words []string
	for _, it := range line {
		if it.Role != "user" || (it.Kind != proto.KindText && it.Kind != proto.KindImage) {
			continue
		}
		if it.From != "" || it.Wake {
			return proto.NavigationMessage{}, false
		}
		if it.ToolUseID != "" {
			continue
		}
		if it.Injected != nil || (it.ClientMsgID == "" && prompts.ParseInjected(it.Text) != nil) || strings.HasPrefix(strings.TrimSpace(it.Text), "/loop") {
			return proto.NavigationMessage{}, false
		}
		if mark.ID == "" || (it.Kind == proto.KindText && len(words) == 0) {
			mark.ID, mark.Seq, mark.TS = it.ID, it.Seq, it.TS
		}
		if it.Kind == proto.KindImage {
			mark.HasImage = true
		} else {
			words = append(words, StripInjected(it.Text))
		}
	}
	if mark.ID == "" {
		return mark, false
	}
	text := strings.Join(words, " ")
	// Attached-image paths are transport metadata, not the user's preview.
	if at := strings.Index(text, "<attached_images>"); at >= 0 {
		if end := strings.Index(text[at:], "</attached_images>"); end >= 0 {
			text = text[:at] + text[at+end+len("</attached_images>"):]
			mark.HasImage = true
		}
	}
	mark.Text = navigationPreview(text)
	return mark, true
}

func navigationPreview(text string) string {
	text = strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")
	runes := []rune(text)
	if len(runes) > NavigationPreviewRunes {
		text = string(runes[:NavigationPreviewRunes])
	}
	return text
}

// Around returns a bounded, contiguous window containing seq. Its span covers
// only that window, not the omitted prefix or suffix; clients must not merge it
// across a gap and advance a live catch-up cursor over unseen history.
func (f *File) Around(seq, limit int, history string) ([]proto.Item, bool, bool, Span, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if seq <= 0 {
		return nil, false, false, Span{}, ErrNavigationTarget
	}
	if limit <= 0 {
		limit = 80
	}
	limit = min(max(limit, 20), 500)
	f.ensureAfter(seq - 1)
	f.ensureItems(seq+1, limit/2)
	sp := Span{History: f.history()}
	if history != "" && history != sp.History {
		return nil, false, false, sp, ErrNavigationHistory
	}
	i := sort.Search(len(f.items), func(i int) bool { return f.items[i].Seq >= seq })
	if i == len(f.items) || f.items[i].Seq != seq {
		return nil, false, false, sp, ErrNavigationTarget
	}
	start := max(0, i-limit/2)
	end := min(len(f.items), start+limit)
	for start > 0 && f.items[start-1].Seq == f.items[start].Seq {
		start--
	}
	for end < len(f.items) && f.items[end-1].Seq == f.items[end].Seq {
		end++
	}
	sp.From = f.items[start].Seq - 1
	sp.To = f.lines
	if end < len(f.items) {
		sp.To = f.items[end-1].Seq
	}
	return append([]proto.Item(nil), f.items[start:end]...), start > 0 || f.base > 0, end < len(f.items), sp, nil
}
