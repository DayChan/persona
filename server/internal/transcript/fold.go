package transcript

import (
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// subagentTools are the calls that launch a subagent (Claude Code's Task, renamed Agent).
var subagentTools = map[string]bool{"Task": true, "Agent": true}

// A run of work shorter than this (and lighter than foldMinBytes) stays on the page: opening it would cost a round
// trip for a few hundred bytes.
const (
	foldMinItems             = 3
	foldMinBytes             = 4 * 1024
	foldThinkingPreviewChars = 512
)

// FoldWork replaces each run of the agent's work in a page — tool calls with their results and images, thoughts —
// with one KindFold item that counts it (proto.Fold). The prompts, replies and notices stay as they are. A chat shows
// that work folded into one line, yet a long Codex exchange made the phone's first page 1400 rows and a megabyte of
// it (docs/protocol/protocol.md "Folded work").
//
// A call whose result is not in the page (still running) and a result whose call is not (on an older page) are not
// folded: the app pairs them with what comes later or earlier, and a fold hides them from that.
func FoldWork(items []proto.Item) []proto.Item {
	uses, results, agents := map[string]bool{}, map[string]bool{}, map[string]bool{}
	taskUses := map[string]proto.Item{}
	for _, it := range items {
		switch it.Kind {
		case proto.KindToolUse:
			uses[it.ToolUseID] = true
			taskUses[it.ToolUseID] = it
			if subagentTools[it.ToolName] {
				agents[it.ToolUseID] = true
			}
		case proto.KindToolResult:
			results[it.ToolUseID] = true
		}
	}
	inFold := func(it proto.Item) bool {
		switch it.Kind {
		case proto.KindThinking:
			return true
		case proto.KindToolUse, proto.KindToolResult, proto.KindImage: // a tool's image goes with its call
			// a subagent's call stays on the page: the chat draws it as a card, which never folds (#5602)
			return it.ToolUseID != "" && uses[it.ToolUseID] && results[it.ToolUseID] && !agents[it.ToolUseID]
		}
		return false
	}
	out := make([]proto.Item, 0, len(items))
	for i := 0; i < len(items); {
		if !inFold(items[i]) {
			out = append(out, items[i])
			i++
			continue
		}
		j, size := i, 0
		for j < len(items) && inFold(items[j]) {
			size += len(items[j].Text) + len(items[j].ToolInput)
			j++
		}
		if j-i < foldMinItems && size < foldMinBytes {
			out = append(out, items[i:j]...)
		} else {
			it := fold(items[i:j])
			it.Fold.Tasks = foldTaskSnapshots(items[i:j], taskUses)
			it.Fold.Images = ViewedImages(items[i:j], taskUses)
			it.Fold.SupervisionUser = supervisionUserAction(items[i:j], taskUses)
			out = append(out, it)
		}
		i = j
	}
	return out
}

func fold(run []proto.Item) proto.Item {
	first, last := run[0], run[len(run)-1]
	fo := &proto.Fold{Items: len(run), Until: last.TS, ThroughSeq: last.Seq, ThroughID: last.ID}
	latestThought := -1
	for i, it := range run {
		switch {
		case it.Kind == proto.KindToolUse:
			fo.Tools++
		case it.Kind == proto.KindThinking && strings.TrimSpace(it.Text) != "":
			fo.Thoughts++
			latestThought = i
		}
		if it.TS.After(fo.Until) {
			fo.Until = it.TS
		}
	}
	if latestThought >= 0 {
		fo.LatestThinking = thinkingPreview(run[latestThought])
	}
	return proto.Item{ID: "fold:" + first.ID, Seq: first.Seq, TS: first.TS, Role: "assistant", Kind: proto.KindFold, Fold: fo}
}

func thinkingPreview(it proto.Item) *proto.Item {
	preview := &proto.Item{ID: it.ID, Seq: it.Seq, TS: it.TS, Role: it.Role, Kind: it.Kind, Text: it.Text, FullSize: it.FullSize}
	text := []rune(it.Text)
	if len(text) > foldThinkingPreviewChars {
		preview.Text = string(text[:foldThinkingPreviewChars-1]) + "…"
		preview.FullSize = max(preview.FullSize, len(it.Text))
	}
	return preview
}

// Range returns the items of lines fromSeq through throughSeq, at most about limit of them (whole lines); more says
// the range goes on after the last one. It is how a fold is opened.
func (f *File) Range(fromSeq, throughSeq, limit int) ([]proto.Item, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureAfter(fromSeq - 1)
	i := sort.Search(len(f.items), func(k int) bool { return f.items[k].Seq >= fromSeq })
	end := i
	for end < len(f.items) && f.items[end].Seq <= throughSeq && end-i < limit {
		end++
	}
	for end > i && end < len(f.items) && f.items[end].Seq == f.items[end-1].Seq { // never split a line
		end++
	}
	more := end < len(f.items) && f.items[end].Seq <= throughSeq
	return append([]proto.Item(nil), f.items[i:end]...), more
}
