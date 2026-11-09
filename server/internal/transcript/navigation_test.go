package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func navigationFixture(t *testing.T, questions int) *File {
	t.Helper()
	var content strings.Builder
	appendLine := func(kind, id string, message any, extra map[string]any) {
		v := map[string]any{"type": kind, "uuid": id, "timestamp": "2026-11-09T10:00:00Z"}
		if message != nil {
			v["message"] = map[string]any{"role": kind, "content": message}
		}
		for k, value := range extra {
			v[k] = value
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		content.Write(b)
		content.WriteByte('\n')
	}
	supervision := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{{ID: "task-demo", Title: "Test a change", Kind: "review"}}})
	for q := 1; q <= questions; q++ {
		blocks := []map[string]any{{"type": "text", "text": fmt.Sprintf("Question %d", q)}}
		if q%10 == 0 {
			blocks = append([]map[string]any{{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "c3ludGhldGlj"}}}, blocks...)
		}
		appendLine("user", fmt.Sprintf("user-%d", q), blocks, nil)
		appendLine("assistant", fmt.Sprintf("answer-%d", q), []map[string]any{{"type": "text", "text": "An answer"}}, nil)
		appendLine("user", fmt.Sprintf("supervision-%d", q), supervision, nil)
		if q%5 == 0 {
			appendLine("system", fmt.Sprintf("compact-%d", q), nil, map[string]any{"subtype": "compact_boundary", "compactMetadata": map[string]any{"trigger": "auto", "preTokens": 170000}})
			appendLine("user", fmt.Sprintf("summary-%d", q), "A synthetic continuation summary", map[string]any{"isCompactSummary": true})
		}
	}
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewFile(path)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNavigationPagesGenuineInputsIndependentlyOfTranscriptPages(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	for _, window := range []int64{1 << 20, 4096} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			windowBytes = window
			f := navigationFixture(t, 80)
			latest, err := f.Navigation(0, 0, "")
			if err != nil || len(latest.Messages) == 0 || len(latest.Messages) > 40 || !latest.HasMore {
				t.Fatalf("latest: %+v err=%v", latest, err)
			}
			if window == 1<<20 && len(latest.Messages) != 40 {
				t.Fatalf("default page has %d messages, want 40", len(latest.Messages))
			}
			seen := map[string]bool{}
			page, before := latest, 0
			for pages := 0; ; pages++ {
				if pages >= 100 {
					t.Fatal("navigation did not terminate")
				}
				for i, mark := range page.Messages {
					if seen[mark.ID] || !strings.HasPrefix(mark.Text, "Question ") || (i > 0 && mark.Seq <= page.Messages[i-1].Seq) {
						t.Fatalf("invalid or repeated mark %+v", mark)
					}
					if mark.Answer != "An answer" {
						t.Fatalf("wrong brief answer for %+v", mark)
					}
					seen[mark.ID] = true
					if mark.Text == "Question 80" && !mark.HasImage {
						t.Fatal("text/image siblings lost image metadata")
					}
				}
				if !page.HasMore {
					break
				}
				if page.NextBeforeSeq <= 0 || (before > 0 && page.NextBeforeSeq >= before) {
					t.Fatalf("cursor did not advance: before=%d next=%d", before, page.NextBeforeSeq)
				}
				before = page.NextBeforeSeq
				page, err = f.Navigation(before, 40, page.History)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(seen) != 80 {
				t.Fatalf("collected %d real questions, want 80", len(seen))
			}
		})
	}
}

func TestNavigationMessageExcludesAutomaticInputsAndBoundsPreview(t *testing.T) {
	base := proto.Item{ID: "user#text", Seq: 7, Role: "user", Kind: proto.KindText, Text: "<persona-context title=\"private context\">Not for the preview</persona-context>\n" + strings.Repeat("é", 200)}
	mark, ok := navigationMessage([]proto.Item{base})
	if !ok || utf8.RuneCountInString(mark.Text) != NavigationPreviewRunes || !utf8.ValidString(mark.Text) || strings.Contains(mark.Text, "context") {
		t.Fatalf("preview=%+v ok=%v", mark, ok)
	}
	for _, patch := range []func(*proto.Item){
		func(it *proto.Item) { it.Injected = &proto.Injected{Kind: proto.InjectedSystem, State: "supervise"} },
		func(it *proto.Item) { it.From = "other-session" },
		func(it *proto.Item) { it.Wake = true },
		func(it *proto.Item) { it.Text = "/loop watch a test" },
		func(it *proto.Item) { it.Kind = proto.KindToolResult; it.ToolUseID = "tool-demo" },
	} {
		it := base
		patch(&it)
		if mark, ok := navigationMessage([]proto.Item{it}); ok {
			t.Fatalf("automatic input became navigation mark %+v", mark)
		}
	}
	photo := proto.Item{ID: "user#image", Seq: base.Seq, Role: "user", Kind: proto.KindImage}
	base.Queued = true
	mark, ok = navigationMessage([]proto.Item{photo, base})
	if !ok || mark.ID != base.ID || !mark.HasImage {
		t.Fatalf("queued text/image user input missing: %+v", mark)
	}
}

func TestNavigationBriefAnswerDoesNotCrossAnInjectedTurn(t *testing.T) {
	item := func(seq int, role, text string) proto.Item {
		return proto.Item{ID: fmt.Sprintf("item-%d", seq), Seq: seq, Role: role, Kind: proto.KindText, Text: text}
	}
	supervision := item(4, "user", "Automatic supervision")
	supervision.Injected = &proto.Injected{Kind: proto.InjectedSystem, State: "supervise"}
	f := &File{lines: 8, items: []proto.Item{
		item(1, "user", "Question 1"), item(2, "assistant", "Earlier narration"), item(3, "assistant", "The real answer"),
		supervision, item(5, "assistant", "Not the user's answer"), item(6, "user", "Question 2"),
		item(7, "assistant", strings.Repeat("é", 200)), item(8, "user", "Question 3"),
	}}
	page, err := f.Navigation(0, 40, "")
	if err != nil || len(page.Messages) != 3 {
		t.Fatalf("index=%+v err=%v", page, err)
	}
	if page.Messages[0].Answer != "The real answer" || utf8.RuneCountInString(page.Messages[1].Answer) != 160 || page.Messages[2].Answer != "" {
		t.Fatalf("incorrect answer attribution: %+v", page.Messages)
	}
}

func TestNavigationRejectsARealTranscriptRewrite(t *testing.T) {
	f := navigationFixture(t, 3)
	before, err := f.Navigation(0, 40, "")
	if err != nil {
		t.Fatal(err)
	}
	replacement := f.Path + ".replacement"
	line := `{"type":"user","uuid":"replacement-user","message":{"role":"user","content":"A different conversation history"}}` + "\n"
	if err := os.WriteFile(replacement, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, f.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Navigation(before.NextBeforeSeq, 40, before.History); !errors.Is(err, ErrNavigationHistory) {
		t.Fatalf("old cursor accepted after rewrite: %v", err)
	}
	if _, _, _, _, err := f.Around(before.Messages[0].Seq, 80, before.History); !errors.Is(err, ErrNavigationHistory) {
		t.Fatalf("old target accepted after rewrite: %v", err)
	}
	after, err := f.Navigation(0, 40, "")
	if err != nil || after.History == before.History || len(after.Messages) != 1 || after.Messages[0].ID == before.Messages[0].ID {
		t.Fatalf("fresh index after rewrite=%+v err=%v", after, err)
	}
}

func TestAroundLoadsOnlyTheSelectedWindowAndDetectsStaleTargets(t *testing.T) {
	f := navigationFixture(t, 80)
	index, err := f.Navigation(0, 100, "")
	if err != nil || len(index.Messages) != 80 {
		t.Fatalf("index=%+v err=%v", index, err)
	}
	target := index.Messages[24]
	items, older, newer, span, err := f.Around(target.Seq, 40, index.History)
	if err != nil || !older || !newer || len(items) > 42 || span.To >= f.LastSeq() {
		t.Fatalf("around: len=%d older=%v newer=%v span=%+v err=%v", len(items), older, newer, span, err)
	}
	if items[0].Seq <= span.From || items[len(items)-1].Seq > span.To {
		t.Fatalf("invalid coverage span %+v", span)
	}
	found := false
	for _, item := range items {
		found = found || item.ID == target.ID
	}
	if !found {
		t.Fatal("bounded window omitted the selected target")
	}
	after, more, afterSpan := f.After(span.To, 40)
	if !more || len(after) == 0 || after[0].Seq <= span.To || afterSpan.From != span.To {
		t.Fatalf("forward window cannot continue: n=%d more=%v span=%+v", len(after), more, afterSpan)
	}
	_, _, _, _, err = f.Around(target.Seq, 40, "old-history")
	if !errors.Is(err, ErrNavigationHistory) {
		t.Fatalf("stale around err=%v", err)
	}
	_, err = f.Navigation(0, 40, "old-history")
	if !errors.Is(err, ErrNavigationHistory) {
		t.Fatalf("stale index err=%v", err)
	}
	_, _, _, _, err = f.Around(f.LastSeq()+100, 40, "")
	if !errors.Is(err, ErrNavigationTarget) {
		t.Fatalf("missing target err=%v", err)
	}
	minPage, _ := f.Navigation(0, 1, "")
	if len(minPage.Messages) != 20 {
		t.Fatalf("minimum page size=%d", len(minPage.Messages))
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	again, _, _, _, _ := f.Around(target.Seq, 40, index.History)
	againIDs := make([]string, len(again))
	for i, item := range again {
		againIDs[i] = item.ID
	}
	if !reflect.DeepEqual(ids, againIDs) {
		t.Fatal("window identity changed without a history change")
	}
}
