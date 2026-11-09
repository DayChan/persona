package transcript

import (
	"context"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// UserInputPositions groups blocks by entry identity, not text or model turn. Genuine queued inputs count;
// system deliveries, peers, loop wakes and tool-result images do not. No message bodies leave this projection.
func UserInputPositions(items []proto.Item) []proto.UserInputPosition {
	type entry struct {
		position          proto.UserInputPosition
		content, excluded bool
	}
	groups := map[string]*entry{}
	var order []*entry
	for _, it := range items {
		if it.Role != "user" || (it.Kind != proto.KindText && it.Kind != proto.KindImage) || it.ToolUseID != "" {
			continue
		}
		id, _, _ := strings.Cut(it.ID, "#")
		g := groups[id]
		if g == nil {
			g = &entry{position: proto.UserInputPosition{ID: it.ID, Seq: it.Seq}}
			groups[id] = g
			order = append(order, g)
		}
		g.content = g.content || it.Kind == proto.KindImage || strings.TrimSpace(it.Text) != ""
		g.excluded = g.excluded || it.From != "" || it.Wake || prompts.ItemSource(it) != nil || strings.HasPrefix(strings.TrimSpace(it.Text), "/loop")
	}
	out := make([]proto.UserInputPosition, 0, len(order))
	for _, g := range order {
		if g.content && !g.excluded {
			out = append(out, g.position)
		}
	}
	return out
}

// UserInputs reads the complete existing history, including a cold file's older windows. A partial scan never
// masquerades as the total. Parsing still uses the ordinary light item cache; images and tool bodies are not returned.
func (f *File) UserInputs(ctx context.Context) (proto.UserInputIndex, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.base > 0 {
		if err := ctx.Err(); err != nil {
			return proto.UserInputIndex{}, err
		}
		if f.loadOlder() == 0 {
			return proto.UserInputIndex{}, errors.New("user input history is incomplete")
		}
	}
	if err := ctx.Err(); err != nil {
		return proto.UserInputIndex{}, err
	}
	positions := UserInputPositions(f.items)
	return proto.UserInputIndex{Total: len(positions), Inputs: positions, History: f.history()}, nil
}
