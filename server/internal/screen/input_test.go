package screen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCheckClaudeFixtures(t *testing.T) {
	for _, c := range []struct {
		name, code, clearable string
	}{
		{"claude-2.1.312-empty-162.ansi", "", ""},   // the grey tip is not a draft
		{"claude-2.1.312-cleared-162.ansi", "", ""}, // after ctrl+k ctrl+u
		{"claude-2.1.312-typed-162.ansi", InputDraft, "fix the flaky test"},
		{"claude-2.1.312-multiline-162.ansi", InputDraft, ""}, // two rows: the clear keys take one
		{"claude-2.1.312-wrapped-162.ansi", InputDraft, ""},
		{"claude-2.1.312-bash-162.ansi", InputDraft, ""}, // bash mode: the text would run as a command
		{"claude-2.1.312-trust-162.ansi", DialogOpen, ""},
		{"claude-2.1.312-model-picker-162.ansi", DialogOpen, ""},
		{"claude-rewind-2.1.304-confirm-apple-restore-120.txt", DialogOpen, ""},
	} {
		code, in := CheckClaude(fixture(t, c.name))
		if code != c.code || in.Clearable != c.clearable {
			t.Errorf("%s: code %q clearable %q (text %q), want %q %q", c.name, code, in.Clearable, in.Text, c.code, c.clearable)
		}
	}
}

// Without colors any text is a draft (a grey suggestion and typed words read the same), but for the exact
// new-session tip row, which is how Claude draws it under NO_COLOR.
func TestAboveInputBox(t *testing.T) {
	rule, named := "────────────────", "──── my-session ─"
	for _, c := range []struct {
		screen string
		want   int // rows above the box
	}{
		{"a\nb\n" + rule + "\n❯ x\n" + rule + "\nfooter", 2},
		{"a\n" + named + "\n❯ x\n" + rule + "\nfooter", 1},
		{"a\x1b[2mb\x1b[0m\n" + rule + "\n❯\n" + rule, 1},
		{"a\nb\nc", 3}, // no box: every row
	} {
		got := AboveInputBox(c.screen)
		if len(got) != c.want || got[0] != "a" && got[0] != "ab" {
			t.Errorf("%q: %q", c.screen, got)
		}
	}
}

func TestPlainTextCannotTellSuggestion(t *testing.T) {
	if code, _ := CheckClaude(fixture(t, "claude-2.1.312-empty-162.txt")); code != "" {
		t.Fatalf("plain tip: %q", code)
	}
	if code, _ := CheckClaude("────────────────────\n❯ fix the build\n────────────────────\n footer"); code != InputDraft {
		t.Fatalf("plain suggestion-or-draft: %q", code)
	}
	if code, in := CheckClaude(fixture(t, "claude-2.1.312-typed-162.txt")); code != InputDraft || in.Text != "fix the flaky test" {
		t.Fatalf("plain typed: %q %q", code, in.Text)
	}
}

func TestSuggestionWithCursor(t *testing.T) {
	rule := "\x1b[38;2;136;136;136m" + "────────────────────" + "\x1b[0m"
	// Claude's own cursor sits inverse on the first grey character.
	grey := rule + "\n❯ \x1b[7mf\x1b[0m\x1b[2mix the build\x1b[0m\n" + rule + "\n  footer"
	if code, _ := CheckClaude(grey); code != "" {
		t.Fatalf("suggestion under the cursor: %q", code)
	}
	typedCursor := rule + "\n❯ hello\x1b[7m \x1b[0m\n" + rule + "\n  footer"
	if code, _ := CheckClaude(typedCursor); code != InputDraft {
		t.Fatalf("typed text with the cursor after it: %q", code)
	}
	emptyCursor := rule + "\n❯ \x1b[7m \x1b[0m\n" + rule + "\n  footer"
	if code, _ := CheckClaude(emptyCursor); code != "" {
		t.Fatalf("empty box with the cursor: %q", code)
	}
}

func TestChoosingIgnoresHistory(t *testing.T) {
	screen := "❯ 1. an old message that starts like an option\n⏺ done\n────────────────────\n❯ \n────────────────────\n  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt"
	if Choosing(screen) {
		t.Fatal("a numbered line above the input box is history, and esc to interrupt is no dialog")
	}
}

func TestSGRColorIsNotDim(t *testing.T) {
	runs := sgrRuns("\x1b[38;2;2;2;2mx\x1b[2my\x1b[22mz")
	if len(runs) != 3 || runs[0].dim || !runs[1].dim || runs[2].dim {
		t.Fatalf("%+v", runs)
	}
}

func TestSuggestion(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"claude-2.1.312-suggestion-171.ansi", "add all three to the README"},
		{"claude-2.1.312-suggestion-typed-171.ansi", ""}, // typed over it: a draft, no suggestion
		{"claude-2.1.312-empty-162.ansi", ""},            // the new-session tip is not one
		{"claude-2.1.312-cleared-162.ansi", ""},
		{"claude-2.1.312-typed-162.ansi", ""},
		{"claude-2.1.312-suggestion-171.txt", ""}, // without colors it cannot be told from a draft
	} {
		if got := ClaudeInput(fixture(t, c.name)).Suggestion; got != c.want {
			t.Errorf("%s: suggestion %q, want %q", c.name, got, c.want)
		}
	}
	if code, in := CheckClaude(fixture(t, "claude-2.1.312-suggestion-171.ansi")); code != "" || in.Draft {
		t.Fatalf("a suggestion is not a draft: %q", code)
	}
	rule := "\x1b[0m\x1b[38;2;136;136;136m" + "────────────────────" + "\x1b[0m"
	// Under Claude's cursor, wrapped onto a second row.
	wrapped := rule + "\n❯\u00a0\x1b[7mr\x1b[0m\x1b[2mun the whole test suite again and then\x1b[0m\n  \x1b[2mcommit the fix\x1b[0m\n" + rule + "\n  footer"
	if in := ClaudeInput(wrapped); in.Draft || in.Suggestion != "run the whole test suite again and then commit the fix" {
		t.Fatalf("wrapped: %+v", in)
	}
}

// The box's styles for a log line: SGR sequences as written, any other escape (a link's address, a title) as ⎋, and
// counts for the characters, never the characters.
func TestBoxStyleHasNoText(t *testing.T) {
	rule := "\x1b[38;2;136;136;136m" + "────────────────────" + "\x1b[0m"
	row := "❯ \x1b[7m先\x1b[0m\x1b[2m让它待命\x1b[0m\x1b]8;;https://example.com/secret?token=abc\x1b\\link\x1b]8;;\x1b\\"
	got := BoxStyle(rule + "\r\n" + row + "\r\n" + rule + "\r\n  footer")
	if got != "2·[7m·1·[0m·[2m·4·[0m·⎋·4·⎋" {
		t.Fatalf("style %q", got)
	}
	for _, leak := range []string{"secret", "token", "先", "让", "link"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%q in %q", leak, got)
		}
	}
}

// colorlessByRegexp is the regexp form colorless had, kept as the reference its scanner must agree with.
func colorlessByRegexp(ansi string) bool {
	for _, m := range regexp.MustCompile(`\x1b\[([0-9;:]*)m`).FindAllStringSubmatch(ansi, -1) {
		for _, f := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' }) {
			c := atoi(f)
			if c == 2 || (c >= 30 && c <= 38) || (c >= 40 && c <= 48) || (c >= 90 && c <= 97) || (c >= 100 && c <= 107) {
				return false
			}
		}
	}
	return true
}

func TestColorless(t *testing.T) {
	for _, c := range []struct {
		ansi string
		want bool
	}{
		{"plain text", true},
		{"", true},
		{"\x1b[0m", true},
		{"\x1b[m", true},
		{"\x1b[;m", true},
		{"\x1b[1m", true}, // bold is not a color
		{"\x1b[1;4;7m", true},
		{"\x1b[39m\x1b[49m", true}, // default foreground and background
		{"\x1b[2K\x1b[3;5H", true}, // cursor and erase sequences do not end in m
		{"\x1b]8;;https://example.com/a\x07link\x1b]8;;\x07", true},
		{"\x1b[2m", false}, // dim
		{"\x1b[1;2m", false},
		{"\x1b[38;5;245m", false},
		{"\x1b[38:2::1:2:3m", false}, // colon sub-parameters
		{"\x1b[;31m", false},
		{"\x1b[031m", false},
		{"\x1b[48;5;0m", false},
		{"\x1b[97m", false},
		{"\x1b[100m", false},
		{"\x1b[107m", false},
		{"\x1b[108m", true},
		{"\x1b[49;98m", true},
		{"\x1b[3\x1b[31m", false}, // a cut sequence does not hide the next one
		{"\x1b[?25l\x1b[31m", false},
		{"\x1b[31", true}, // cut before its final byte
		{"\x1b[", true},
		{"\x1b", true},
		{"x\x1b[0m\x1b[1mbold\x1b[0m\x1b[34mblue", false},
	} {
		if got := colorless(c.ansi); got != c.want {
			t.Errorf("colorless(%q) = %v, want %v", c.ansi, got, c.want)
		}
		if ref := colorlessByRegexp(c.ansi); ref != c.want {
			t.Errorf("reference colorless(%q) = %v, want %v", c.ansi, ref, c.want)
		}
	}
	// Every recorded screen is drawn in color, and the scanner agrees with the regexp on each.
	names, err := filepath.Glob(filepath.Join("testdata", "*.ansi"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no recorded screens: %v", err)
	}
	for _, name := range names {
		ansi := fixture(t, filepath.Base(name))
		if got, ref := colorless(ansi), colorlessByRegexp(ansi); got || got != ref {
			t.Errorf("%s: colorless %v, regexp %v, want false", name, got, ref)
		}
		// Without its colors the same screen is colorless.
		plain := regexp.MustCompile(`\x1b\[[0-9;:]*m`).ReplaceAllString(ansi, "\x1b[0m")
		if got, ref := colorless(plain), colorlessByRegexp(plain); !got || !ref {
			t.Errorf("%s without colors: colorless %v, regexp %v, want true", name, got, ref)
		}
	}
}

// colorless runs on every Codex screen read and in Claude's input checks: it must not allocate.
func TestColorlessAllocatesNothing(t *testing.T) {
	s := fixture(t, "claude-2.1.312-typed-162.ansi")
	if n := testing.AllocsPerRun(50, func() { colorless(s) }); n != 0 {
		t.Fatalf("colorless allocates %v times per call, want 0", n)
	}
}
