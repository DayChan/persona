package node

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestRuntimeUserInputsDrainsPagesAndDeduplicates(t *testing.T) {
	n := &Node{}
	calls := []int{}
	index, err := n.runtimeUserInputs(context.Background(), "synthetic", func(params any) (json.RawMessage, error) {
		p := params.(map[string]any)
		before, _ := p["before_seq"].(int)
		calls = append(calls, before)
		if p["lazy_images"] != true || p["limit"] != 500 {
			t.Fatalf("params %v", p)
		}
		items := []proto.Item{{ID: "input-b#0", Seq: 20, Role: "user", Kind: proto.KindText, Text: "Synthetic"}, {ID: "input-b#1", Seq: 20, Role: "user", Kind: proto.KindImage}}
		more := true
		if before == 20 {
			items = []proto.Item{{ID: "input-a#0", Seq: 5, Role: "user", Kind: proto.KindText, Text: "Synthetic", Queued: true}}
			more = false
		}
		out, e := json.Marshal(map[string]any{"items": items, "has_more": more, "history": "fixture-history"})
		return out, e
	})
	if err != nil {
		t.Fatal(err)
	}
	if index.Total != 2 || index.Inputs[0].Seq != 5 || !reflect.DeepEqual(calls, []int{0, 20}) {
		t.Fatalf("index %+v calls %v", index, calls)
	}
}

func TestRuntimeUserInputsRefusesPartialOrRewrittenTotals(t *testing.T) {
	for _, scenario := range []string{"empty", "stuck", "rewrite"} {
		t.Run(scenario, func(t *testing.T) {
			n := &Node{}
			calls := 0
			_, err := n.runtimeUserInputs(context.Background(), "synthetic", func(any) (json.RawMessage, error) {
				calls++
				history := "first"
				if scenario == "rewrite" && calls > 1 {
					history = "second"
				}
				items := []proto.Item{{ID: "input#0", Seq: 20, Role: "user", Kind: proto.KindText, Text: "Synthetic"}}
				if scenario == "empty" {
					items = nil
				}
				return json.Marshal(map[string]any{"items": items, "has_more": true, "history": history})
			})
			if err == nil {
				t.Fatal("incomplete index reported a total")
			}
		})
	}
}
