package transcript

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestUserInputPositionsCountsInputsNotTurns(t *testing.T) {
	source := &proto.MessageSource{Kind: proto.SourceConversation}
	text := func(id string, seq int, value string) proto.Item {
		return proto.Item{ID: id, Seq: seq, Role: "user", Kind: proto.KindText, Text: value}
	}
	first := text("same-a#0", 1, "Repeated input")
	queued := text("same-b#0", 2, "Repeated input")
	queued.Queued = true
	auto := text("auto#0", 3, "Scheduled input")
	auto.Source = source
	peer := text("peer#0", 4, "Peer input")
	peer.From = "example-peer"
	wake := text("wake#0", 5, "Wake input")
	wake.Wake = true
	unknown := text("unknown#0", 6, "Unknown source")
	unknown.Source = &proto.MessageSource{Kind: "future-source"}
	protected := text("protected#0", 7, prompts.MessageSourceText("An authored input with context", nil))
	protected.ClientMsgID = "synthetic-send"
	img := proto.Item{ID: "picture#0", Seq: 8, Role: "user", Kind: proto.KindImage}
	imageSibling := img
	imageSibling.ID = "picture#1"
	toolImage := img
	toolImage.ID = "tool-image#0"
	toolImage.Seq = 9
	toolImage.ToolUseID = "tool"
	autoImg := img
	autoImg.ID = "auto#1"
	autoImg.Seq = 3
	inputs := []proto.Item{first, queued, auto, autoImg, peer, wake, unknown, protected, img, imageSibling, toolImage, text("loop#0", 10, "/loop check again"), text("empty#0", 11, " ")}
	want := []proto.UserInputPosition{{ID: first.ID, Seq: 1}, {ID: queued.ID, Seq: 2}, {ID: protected.ID, Seq: 7}, {ID: img.ID, Seq: 8}}
	if got := UserInputPositions(inputs); !reflect.DeepEqual(got, want) {
		t.Fatalf("positions %v, want %v", got, want)
	}
}

func TestUserInputsIncludesColdCompactedHistoryAndRewrite(t *testing.T) {
	old := windowBytes
	windowBytes = 512
	t.Cleanup(func() { windowBytes = old })
	path := filepath.Join(t.TempDir(), "synthetic.jsonl")
	var content strings.Builder
	for i := 1; i <= 36; i++ {
		record := map[string]any{"type": "user", "uuid": fmt.Sprintf("input-%03d", i), "message": map[string]any{"role": "user", "content": fmt.Sprintf("Synthetic input %03d. %s", i, strings.Repeat("fixture ", 20))}}
		b, _ := json.Marshal(record)
		content.Write(b)
		content.WriteByte('\n')
		content.WriteString(fmt.Sprintf(`{"type":"assistant","uuid":"answer-%03d","message":{"role":"assistant","content":[{"type":"text","text":"Synthetic answer"}]}}`+"\n", i))
		if i%6 == 0 {
			content.WriteString(`{"type":"system","subtype":"compact_boundary","content":"Synthetic compaction"}` + "\n")
		}
	}
	if err := os.WriteFile(path, []byte(content.String()), 0600); err != nil {
		t.Fatal(err)
	}
	f := NewFile(path)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	initial, _, _ := f.PageTurns(0, 2, 2000)
	if got := len(UserInputPositions(initial)); got != 2 {
		t.Fatalf("tail inputs = %d", got)
	}
	index, err := f.UserInputs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if index.Total != 36 || len(index.Inputs) != 36 || index.Inputs[0].ID != "input-001#0" {
		t.Fatalf("index %+v", index)
	}
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Synthetic") || strings.Contains(string(raw), "message") {
		t.Fatalf("body leaked: %s", raw)
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","uuid":"replacement","message":{"role":"user","content":"A new synthetic history"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	replaced, err := f.UserInputs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Total != 1 || replaced.History == index.History {
		t.Fatalf("rewrite %+v", replaced)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.UserInputs(ctx); err == nil {
		t.Fatal("cancelled scan returned a total")
	}
}
