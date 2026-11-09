package transcript

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Fixtures are real transcripts / rollouts run through scripts/diag/redact_transcript.py (testdata/README.md). Each one has
// a golden file with what the parser makes of it; `go test ./internal/transcript -run Fixture -update` rewrites them,
// and the diff is the review.

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

type fixture struct {
	name string
	path string
	sub  bool // a subagent's transcript (every line a sidechain)
}

func fixtures(t *testing.T) []fixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	var out []fixture
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		out = append(out, fixture{name: name, path: p, sub: strings.Contains(name, "subagent")})
	}
	return out
}

func (fx fixture) open(path string) *File {
	if fx.sub {
		return NewSubagentFile(path)
	}
	return NewFile(path)
}

// golden is what a fixture's golden file holds: every item, and the session-level facts the apps show.
type golden struct {
	Items []proto.Item `json:"items"`
	Meta  goldenMeta   `json:"meta"`
}

type goldenMeta struct {
	Title          string                 `json:"title,omitempty"`
	FirstUser      string                 `json:"first_user,omitempty"`
	LastText       string                 `json:"last_text,omitempty"`
	Model          string                 `json:"model,omitempty"`
	Cwd            string                 `json:"cwd,omitempty"`
	PermissionMode string                 `json:"permission_mode,omitempty"`
	Effort         string                 `json:"effort,omitempty"`
	Ultracode      *bool                  `json:"ultracode,omitempty"`
	Turns          int                    `json:"turns"`
	Compactions    int                    `json:"compactions"`
	ContextTokens  int                    `json:"context_tokens"`
	OutputTokens   int                    `json:"output_tokens"`
	Queue          []string               `json:"queue,omitempty"`
	Background     []proto.BackgroundTask `json:"background,omitempty"`
	TurnOpen       bool                   `json:"turn_open,omitempty"`
	LastTurnWake   bool                   `json:"last_turn_wake,omitempty"`
	TurnError      string                 `json:"turn_error,omitempty"`
	TurnErrorKind  string                 `json:"turn_error_kind,omitempty"`
	TurnErrorCode  string                 `json:"turn_error_code,omitempty"`
	ModelFallback  *proto.ModelRefusal    `json:"model_fallback,omitempty"`
	TurnResetsAt   *time.Time             `json:"turn_error_resets_at,omitempty"`
	TurnResumes    bool                   `json:"turn_error_resumes,omitempty"`
	TurnAfterWork  bool                   `json:"turn_error_after_work,omitempty"`
	LastActivity   time.Time              `json:"last_activity"`
}

func snapshot(f *File) golden {
	m := f.Meta()
	items, _, _ := f.Page(0, 1<<30)
	var resets *time.Time
	if !m.TurnErrorResetsAt.IsZero() {
		resets = &m.TurnErrorResetsAt
	}
	return golden{Items: items, Meta: goldenMeta{
		Title: m.Title, FirstUser: m.FirstUser, LastText: m.LastText, Model: m.Model, Cwd: m.Cwd,
		PermissionMode: m.PermissionMode, Effort: m.Effort, Ultracode: m.Ultracode, Turns: m.Turns, Compactions: m.Compactions,
		ContextTokens: m.ContextTokens, OutputTokens: m.OutputTokens, Queue: m.Queue, Background: m.Background,
		TurnOpen: m.TurnOpen, LastTurnWake: m.LastTurnWake, TurnError: m.TurnError, TurnErrorKind: m.TurnErrorKind, TurnErrorCode: m.TurnErrorCode, ModelFallback: m.ModelFallback,
		TurnResetsAt: resets, TurnResumes: m.TurnErrorResumes, TurnAfterWork: m.TurnErrorAfterWork, LastActivity: m.LastActivity,
	}}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestFixtureGolden(t *testing.T) {
	for _, fx := range fixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			f := fx.open(fx.path)
			if _, err := f.Refresh(); err != nil {
				t.Fatal(err)
			}
			got := marshal(t, snapshot(f))
			goldenPath := filepath.Join("testdata", fx.name+".golden.json")
			if *update {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from %s; run with -update and review the diff", fx.name, goldenPath)
			}
		})
	}
}

// Reading a transcript as it is written (any split, mid-line included) must give exactly what reading it whole gives.
func TestFixtureIncrementalEqualsCold(t *testing.T) {
	for _, fx := range fixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			data, err := os.ReadFile(fx.path)
			if err != nil {
				t.Fatal(err)
			}
			cold := fx.open(fx.path)
			if _, err := cold.Refresh(); err != nil {
				t.Fatal(err)
			}
			want := marshal(t, snapshot(cold))
			for _, step := range []int{1 << 30, 4096, 977, 61} {
				p := filepath.Join(t.TempDir(), "t.jsonl")
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				f := fx.open(p)
				var streamed []proto.Item
				for off := 0; off < len(data); off += step {
					end := min(off+step, len(data))
					fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = fh.Write(data[off:end])
					fh.Close()
					fresh, err := f.Refresh()
					if err != nil {
						t.Fatal(err)
					}
					streamed = append(streamed, fresh...)
				}
				if got := marshal(t, snapshot(f)); !bytes.Equal(got, want) {
					t.Fatalf("step %d: incremental read differs from a cold read", step)
				}
				all, _, _ := f.Page(0, 1<<30)
				if !bytes.Equal(marshal(t, proto.WithoutImageData(streamed)), marshal(t, all)) {
					t.Fatalf("step %d: the items Refresh returned are not the items the file holds", step)
				}
			}
		})
	}
}

// Paging back from the end, by rows or by exchanges, visits every item exactly once and never splits a line.
func TestFixturePagingCoversEverything(t *testing.T) {
	for _, fx := range fixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			f := fx.open(fx.path)
			if _, err := f.Refresh(); err != nil {
				t.Fatal(err)
			}
			all, _, _ := f.Page(0, 1<<30)
			walk := func(name string, page func(before int) ([]proto.Item, bool)) {
				var got []proto.Item
				before := 0
				for i := 0; ; i++ {
					items, more := page(before)
					if len(items) > 0 && before > 0 && items[len(items)-1].Seq >= before {
						t.Fatalf("%s: page before %d reaches seq %d", name, before, items[len(items)-1].Seq)
					}
					got = append(append([]proto.Item(nil), items...), got...)
					if !more || len(items) == 0 {
						break
					}
					before = items[0].Seq
					if i > 10000 {
						t.Fatalf("%s: does not end", name)
					}
				}
				if !bytes.Equal(marshal(t, got), marshal(t, all)) {
					t.Fatalf("%s: pages hold %d items, the file %d", name, len(got), len(all))
				}
			}
			walk("Page", func(before int) ([]proto.Item, bool) { items, more, _ := f.Page(before, 3); return items, more })
			walk("PageTurns", func(before int) ([]proto.Item, bool) {
				items, more, _ := f.PageTurns(before, 1, 2000)
				return items, more
			})
			// Forward, the way catch-up and background sync go: resume after the last seq received.
			for _, limit := range []int{1, 3} {
				var got []proto.Item
				after := 0
				for {
					items, more, span := f.After(after, limit)
					if span.From != after || span.To < after {
						t.Fatalf("After(%d) span %+v", after, span)
					}
					got = append(got, items...)
					if len(items) > 0 {
						after = items[len(items)-1].Seq
					}
					if !more {
						break
					}
				}
				if !bytes.Equal(marshal(t, got), marshal(t, all)) {
					t.Fatalf("After by %d: %d items, the file %d", limit, len(got), len(all))
				}
			}
		})
	}
}

// Pages carry light items (long output cut, images without data); Full gives back exactly what parsing the line gives.
func TestFixtureFullRestoresWhatPagesLeaveOut(t *testing.T) {
	cuts := 0
	for _, fx := range fixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			data, err := os.ReadFile(fx.path)
			if err != nil {
				t.Fatal(err)
			}
			whole := map[string]proto.Item{}
			meta := Meta{Sidechain: fx.sub}
			for i, line := range bytes.Split(data, []byte("\n")) {
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				for _, it := range ParseLine(i+1, line, &meta) {
					whole[it.ID] = it
				}
			}
			f := fx.open(fx.path)
			if _, err := f.Refresh(); err != nil {
				t.Fatal(err)
			}
			all, _, _ := f.Page(0, 1<<30)
			withImages := f.WithImages(all)
			for i, it := range all {
				want := whole[it.ID]
				if it.FullSize == 0 && (it.Image == nil || !it.Image.Lazy) {
					if it.Text != want.Text {
						t.Fatalf("%s: uncut item differs", it.ID)
					}
					continue
				}
				cuts++
				if it.FullSize > 0 && (it.FullSize != len(want.Text) || !strings.Contains(it.Text, "已截断")) {
					t.Fatalf("%s: cut text says full_size %d (whole %d): %q", it.ID, it.FullSize, len(want.Text), it.Text[max(len(it.Text)-80, 0):])
				}
				full, err := f.Full(it.ID)
				if err != nil || full.Text != want.Text || full.FullSize != 0 || !sameImage(full.Image, want.Image) {
					t.Fatalf("%s: Full does not give back the parsed item", it.ID)
				}
				if !sameImage(withImages[i].Image, want.Image) || withImages[i].Text != it.Text {
					t.Fatalf("%s: WithImages does not restore the image (or touched the text)", it.ID)
				}
			}
		})
	}
	if cuts == 0 {
		t.Fatal("no fixture has a long output or an image: the test checks nothing")
	}
}

func sameImage(a, b *proto.Image) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Mime == b.Mime && a.Data == b.Data
}

// A file opened by its end (window.go) gives exactly what a full read gives: the first page, every page back to the
// first line, every after_seq page, full texts of items not parsed yet, Meta, and the lines appended after.
func TestFixtureWindowedEqualsFull(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	windowed := 0
	for _, fx := range fixtures(t) {
		data, err := os.ReadFile(fx.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []int64{int64(len(data)) / 12, int64(len(data)) / 6, int64(len(data)) / 3} {
			t.Run(fmt.Sprintf("%s/%d", fx.name, w), func(t *testing.T) {
				// Most of the file, opened cold; then the rest appended.
				cut := bytes.LastIndexByte(data[:len(data)*3/4], '\n') + 1
				p := filepath.Join(t.TempDir(), "t.jsonl")
				if err := os.WriteFile(p, data[:cut], 0o600); err != nil {
					t.Fatal(err)
				}
				windowBytes = 1 << 40
				full := fx.open(p)
				if _, err := full.Refresh(); err != nil {
					t.Fatal(err)
				}
				windowBytes = w
				f := fx.open(p)
				fresh, sp, err := f.RefreshSpan()
				if err != nil {
					t.Fatal(err)
				}
				if f.base > 0 {
					windowed++
				}
				all, _, _ := full.Page(0, 1<<30)
				var tail []proto.Item
				for _, it := range all {
					if it.Seq > sp.From {
						tail = append(tail, it)
					}
				}
				if !bytes.Equal(marshal(t, proto.WithoutImageData(fresh)), marshal(t, tail)) {
					t.Fatalf("the cold read's items are not the file's after line %d", sp.From)
				}
				same := func(what string, a, b any) {
					t.Helper()
					if !bytes.Equal(marshal(t, a), marshal(t, b)) {
						t.Fatalf("%s differs between a windowed and a full read", what)
					}
				}
				wi, wm, ws := f.PageTurns(0, 2, 2000)
				fi, fm, fs := full.PageTurns(0, 2, 2000)
				if msg := pageSuffix(wi, wm, ws, fi, fm, fs); msg != "" {
					t.Fatalf("the first page: %s", msg)
				}
				if f.base > 0 && len(all) > 0 { // an item before the window, whole
					a, errA := f.Full(all[0].ID)
					b, errB := full.Full(all[0].ID)
					same("Full of the first item", []any{a, errA == nil}, []any{b, errB == nil})
				}
				g := fx.open(p) // again cold, paged back to the first line
				if _, err := g.Refresh(); err != nil {
					t.Fatal(err)
				}
				var got []proto.Item
				before := 0
				for {
					items, more, _ := g.Page(before, 3)
					got = append(append([]proto.Item(nil), items...), got...)
					if !more || len(items) == 0 {
						break
					}
					before = items[0].Seq
				}
				same("paging back", got, all)
				h := fx.open(p) // again cold, read forward from the first line
				if _, err := h.Refresh(); err != nil {
					t.Fatal(err)
				}
				got = nil
				for after := 0; ; {
					items, more, _ := h.After(after, 3)
					got = append(got, items...)
					if len(items) > 0 {
						after = items[len(items)-1].Seq
					}
					if !more {
						break
					}
				}
				same("after_seq", got, all)
				// The rest of the file arrives; Meta of the whole.
				fh, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
				_, _ = fh.Write(data[cut:])
				fh.Close()
				for _, x := range []*File{f, full} {
					if _, err := x.Refresh(); err != nil {
						t.Fatal(err)
					}
				}
				same("the whole file and its Meta", snapshot(f), snapshot(full))
			})
		}
	}
	if windowed == 0 {
		t.Fatal("no fixture was opened by its end: the test checks nothing")
	}
}

// pageSuffix checks a page read by the end against the same page read whole: the same, or (its parse budget spent)
// the end of it, saying more may be older — the budget can run out on lines that turn out to hold nothing, and then
// the next page comes back empty. "" when it is.
func pageSuffix(wi []proto.Item, wm bool, ws Span, fi []proto.Item, fm bool, fs Span) string {
	a, _ := json.Marshal(wi)
	if len(wi) == len(fi) {
		b, _ := json.Marshal(fi)
		if !bytes.Equal(a, b) || ws.To != fs.To || ws.History != fs.History || (fm && !wm) || (wm == fm && ws != fs) {
			return fmt.Sprintf("differs (more %v %+v vs %v %+v)", wm, ws, fm, fs)
		}
		return ""
	}
	if len(wi) == 0 || len(wi) > len(fi) || !wm || ws.To != fs.To || ws.History != fs.History {
		return fmt.Sprintf("%d items (more %v, %+v) for %d (%+v)", len(wi), wm, ws, len(fi), fs)
	}
	b, _ := json.Marshal(fi[len(fi)-len(wi):])
	if !bytes.Equal(a, b) {
		return "not the end of the whole page"
	}
	return ""
}
