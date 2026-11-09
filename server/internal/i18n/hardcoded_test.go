package i18n

import (
	"encoding/json"
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Text the hub writes for people (API errors, statuses, pushes, notify, server-rendered pages) comes from the
// catalogs, not from Chinese written into the code (docs/protocol/i18n.md). This is a ratchet while the strings move: per
// file, the number of string literals with CJK text may only go down (hardcoded-baseline.json; a file at zero leaves
// it), the same as the web's src/i18n/hardcoded-baseline.json. Comments and tests don't count; a line that has to
// keep its Chinese (it is matched by its original, it goes to a model, it is stored) says `i18n-ignore` in a comment
// on that line or the line above.
const hardcodedBaseline = "hardcoded-baseline.json"

// hardcodedDirs are the packages (with their subpackages) whose text can reach a client, relative to server/.
// Left out on purpose: cmd/ and link/ (command-line output), node/ and transcript/ (node-side text the hub rewrites
// by its original, hub/transcript_text.go), kb/ (stored knowledge keeps its language), prompts/ (text for models),
// lcuconfirm/ (card labels matched back by their original), office/ (document content).
var hardcodedDirs = []string{
	"internal/ai",
	"internal/assistant",
	"internal/assistantwire",
	"internal/channel",
	"internal/connector",
	"internal/devices",
	"internal/extension",
	"internal/extmgr",
	"internal/feishubot",
	"internal/hub",
	"internal/linktray", // the Windows tray: everything it shows goes through link-tray.json
	"internal/mail",
	"internal/quota",
	"internal/update",
	"internal/voice",
	"internal/workspaceboard",
	"plugins",
}

// hardcodedSkip are files in those packages that only write for a terminal or must keep the original, with why.
var hardcodedSkip = map[string]string{
	"internal/hub/doctor.go":             "personad hub doctor's terminal output",
	"internal/hub/setup.go":              "personad hub setup's terminal output",
	"internal/hub/setup_service.go":      "personad hub setup's terminal output",
	"internal/hub/setup_manager.go":      "personad hub setup's terminal output",
	"internal/hub/transcript_text.go":    "node's original text, matched to rewrite it",
	"internal/extmgr/extension_build.go": "personad extension build's terminal output",
}

// hardcodedSkipDir: conformance kits are test harnesses that happen to live outside _test.go files.
func hardcodedSkipDir(rel string) bool {
	return filepath.Base(rel) == "conformance" || filepath.Base(rel) == "testdata"
}

var cjk = regexp.MustCompile(`[　-〿぀-ヿ㐀-䶿一-鿿！-｠]`)

type hardcodedHit struct {
	line int
	text string
}

// hardcodedIn lists src's string literals with CJK text, less those marked i18n-ignore.
func hardcodedIn(name string, src []byte) []hardcodedHit {
	fset := token.NewFileSet()
	f := fset.AddFile(name, -1, len(src))
	var s scanner.Scanner
	s.Init(f, src, nil, scanner.ScanComments)
	ignored := map[int]bool{}
	var hits []hardcodedHit
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		switch tok {
		case token.COMMENT:
			if strings.Contains(lit, "i18n-ignore") {
				// The comment's last line covers itself and the next one.
				end := f.Line(pos) + strings.Count(lit, "\n")
				ignored[end], ignored[end+1] = true, true
			}
		case token.STRING:
			decoded, err := strconv.Unquote(lit)
			if err == nil && cjk.MatchString(decoded) {
				text := lit
				if r := []rune(text); len(r) > 60 {
					text = string(r[:60]) + "…"
				}
				hits = append(hits, hardcodedHit{f.Line(pos), text})
			}
		}
	}
	// A trailing // i18n-ignore comes after the literal on its line, so filter once every comment is known.
	return slices.DeleteFunc(hits, func(h hardcodedHit) bool { return ignored[h.line] })
}

func TestHardcodedIn(t *testing.T) {
	src := []byte("package x\n" +
		"var a = \"中文\"\n" +
		"var b = \"中文\" // i18n-ignore: matched\n" +
		"// i18n-ignore: stored\n" +
		"var c = `中文`\n" +
		"var d = \"plain\" // 注释\n" +
		"var e = \"かな\"\n" +
		"var f = \"\\u4e2d\\u6587\"\n" +
		"var g = `plain\n中文`\n" +
		"/* i18n-ignore: original label */\n" +
		"var h = \"中文\"\n")
	got := hardcodedIn("x.go", src)
	var lines []int
	for _, h := range got {
		lines = append(lines, h.line)
	}
	if !slices.Equal(lines, []int{2, 7, 8, 9}) {
		t.Fatalf("hits on lines %v, want [2 7 8 9]", lines)
	}
}

// updateHardcodedBaseline also guards the write path: update mode is not an escape hatch for new literals.
func updateHardcodedBaseline(name string, counts, baseline map[string]int) error {
	for rel, n := range counts {
		if n > baseline[rel] {
			return fmt.Errorf("refusing to raise %s from %d to %d", rel, baseline[rel], n)
		}
	}
	raw, err := json.MarshalIndent(counts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(name, append(raw, '\n'), 0o644)
}

func TestUpdateHardcodedBaseline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		counts  map[string]int
		blocked bool
	}{
		{"unchanged", map[string]int{"existing.go": 2}, false},
		{"lower", map[string]int{"existing.go": 1}, false},
		{"removed", map[string]int{}, false},
		{"increase", map[string]int{"existing.go": 3}, true},
		{"new file", map[string]int{"existing.go": 2, "new.go": 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "baseline.json")
			original := []byte("{\"existing.go\":2}\n")
			if err := os.WriteFile(name, original, 0o644); err != nil {
				t.Fatal(err)
			}
			err := updateHardcodedBaseline(name, tc.counts, map[string]int{"existing.go": 2})
			if (err != nil) != tc.blocked {
				t.Fatalf("error = %v, blocked = %v", err, tc.blocked)
			}
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if tc.blocked {
				if string(raw) != string(original) {
					t.Fatal("blocked update changed the file")
				}
				return
			}
			var got map[string]int
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.counts) {
				t.Fatalf("written counts = %v", got)
			}
			for rel, n := range tc.counts {
				if got[rel] != n {
					t.Fatalf("written counts = %v", got)
				}
			}
		})
	}
}

func TestHardcodedChineseBaseline(t *testing.T) {
	root := filepath.Join("..", "..")
	counts := map[string]int{}
	hits := map[string][]hardcodedHit{}
	for _, dir := range hardcodedDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if hardcodedSkipDir(rel) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") ||
				strings.HasSuffix(rel, "cli.go") || hardcodedSkip[rel] != "" {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if h := hardcodedIn(rel, src); len(h) > 0 {
				counts[rel], hits[rel] = len(h), h
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
	}
	for rel := range hardcodedSkip {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("hardcodedSkip lists %s, which is gone: drop it", rel)
		}
	}
	raw, err := os.ReadFile(hardcodedBaseline)
	if err != nil {
		t.Fatal(err)
	}
	var baseline map[string]int
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	var grew, stale []string
	for rel, n := range counts {
		if n > baseline[rel] {
			var b strings.Builder
			fmt.Fprintf(&b, "%s: %d (baseline %d)", rel, n, baseline[rel])
			for _, h := range hits[rel] {
				fmt.Fprintf(&b, "\n  %s:%d %s", rel, h.line, h.text)
			}
			grew = append(grew, b.String())
		}
	}
	for rel, n := range baseline {
		if counts[rel] < n {
			stale = append(stale, fmt.Sprintf("%s: %d (baseline %d)", rel, counts[rel], n))
		}
	}
	slices.Sort(grew)
	slices.Sort(stale)
	if len(grew) > 0 {
		t.Errorf("more Chinese written into the code: move it into locales/ with i18n.T / i18n.Err "+
			"(or mark text that must stay with // i18n-ignore: <why>)\n%s", strings.Join(grew, "\n"))
		return // Update mode must not bless an increase either.
	}
	if os.Getenv("UPDATE_I18N_BASELINE") != "" {
		if err := updateHardcodedBaseline(hardcodedBaseline, counts, baseline); err != nil {
			t.Fatal(err)
		}
		return
	}
	if len(stale) > 0 {
		t.Errorf("fewer hard-coded strings now: run UPDATE_I18N_BASELINE=1 go test ./internal/i18n/ -run TestHardcodedChineseBaseline\n%s",
			strings.Join(stale, "\n"))
	}
}
