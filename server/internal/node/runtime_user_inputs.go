package node

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Runtime sessions use their existing item paging, without requiring #2071 or a new runtime SDK method.
// The server reduces a complete, coherent read to positions; never return a partial list as the total.
func (n *Node) runtimeUserInputs(ctx context.Context, sid string, read func(any) (json.RawMessage, error)) (proto.UserInputIndex, error) {
	var all []proto.Item
	history := ""
	before := 0
	for {
		if err := ctx.Err(); err != nil {
			return proto.UserInputIndex{}, err
		}
		params := map[string]any{"session_id": sid, "limit": 500, "lazy_images": true}
		if before > 0 {
			params["before_seq"] = before
		}
		raw, err := read(params)
		if err != nil {
			return proto.UserInputIndex{}, err
		}
		raw = n.echoedRaw(sid, raw, "items")
		var page struct {
			Items   []proto.Item `json:"items"`
			HasMore bool         `json:"has_more"`
			History string       `json:"history"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return proto.UserInputIndex{}, err
		}
		if history != "" && page.History != "" && history != page.History {
			return proto.UserInputIndex{}, errors.New("user input history changed")
		}
		if page.History != "" {
			history = page.History
		}
		all = append(all, page.Items...)
		if !page.HasMore {
			break
		}
		next := before
		for _, it := range page.Items {
			if next == 0 || it.Seq < next {
				next = it.Seq
			}
		}
		if next <= 0 || (before > 0 && next >= before) {
			return proto.UserInputIndex{}, errors.New("user input history made no progress")
		}
		before = next
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	positions := transcript.UserInputPositions(all)
	return proto.UserInputIndex{Total: len(positions), Inputs: positions, History: history}, nil
}
