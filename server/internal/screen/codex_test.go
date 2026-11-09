package screen

import (
	"strings"
	"testing"
)

// Each recorded Codex screen, colored and plain, reads as the view it is.
func TestReadCodexFixtures(t *testing.T) {
	for _, c := range []struct {
		name, view, draft string
	}{
		{"codex-0.153.4-empty-120", CodexComposer, ""},
		{"codex-0.163.0-empty-120", CodexComposer, ""},
		{"codex-0.153.4-draft-120", CodexComposer, "half typed draft"},
		{"codex-0.153.4-slash-popup-120", CodexComposer, "/plan"},
		{"codex-0.163.0-slash-popup-120", CodexComposer, "/plan"},
		{"codex-0.163.0-plan-answered-120", CodexComposer, ""},
		{"codex-0.163.0-async-collapsed-120", CodexComposer, ""},
		{"codex-0.163.0-async-answered-midturn-120", CodexComposer, ""},
		{"codex-0.153.4-exec-approval-120", CodexApproval, ""},
		{"codex-0.163.0-exec-approval-120", CodexApproval, ""},
		{"codex-0.153.4-plan-question-120", CodexQuestion, ""},
		{"codex-0.153.4-plan-question-notes-120", CodexQuestion, ""},
		{"codex-0.163.0-plan-question-120", CodexQuestion, ""},
		{"codex-0.163.0-plan-question-2-120", CodexQuestion, ""},
		{"codex-0.163.0-async-expanded-120", CodexAsyncPane, ""},
		{"codex-0.153.4-transcript-pending-approval-120", CodexTranscript, ""},
	} {
		for _, ext := range []string{".ansi", ".txt"} {
			s := ReadCodex(fixture(t, c.name+ext))
			if s.View != c.view {
				t.Errorf("%s%s: view %q, want %q", c.name, ext, s.View, c.view)
			}
			if s.Draft != c.draft {
				t.Errorf("%s%s: draft %q, want %q", c.name, ext, s.Draft, c.draft)
			}
		}
	}
}

func TestReadCodexApproval(t *testing.T) {
	for _, name := range []string{"codex-0.153.4-exec-approval-120", "codex-0.163.0-exec-approval-120"} {
		a := ReadCodex(fixture(t, name+".txt")).Approval
		if a == nil {
			t.Fatalf("%s: no approval", name)
		}
		if a.Title != "Would you like to run the following command?" || a.Command != "curl -sI https://example.com | head -1" {
			t.Errorf("%s: title %q command %q", name, a.Title, a.Command)
		}
		if !strings.HasPrefix(a.Reason, "Allow this command to access ") || a.Detail != "Environment: local" {
			t.Errorf("%s: reason %q detail %q", name, a.Reason, a.Detail)
		}
		if len(a.Options) != 3 || a.Options[0].Label != "Yes, proceed" || !a.Options[0].Selected ||
			a.Options[1].Label != "Yes, and don't ask again for commands that start with `curl -sI https://example.com`" {
			t.Fatalf("%s: options %+v", name, a.Options)
		}
		if a.Role(RoleAllow).Key != "1" || a.Role(RoleAlways).Key != "2" || a.Role(RoleCancel).Key != "3" ||
			a.Role(RoleSession) != nil || a.Role(RoleDecline) != nil {
			t.Errorf("%s: roles %+v", name, a.Options)
		}
		// The same dialog in color has the same revision: the card is read plain, the answer checked either way.
		if c := ReadCodex(fixture(t, name+".ansi")).Approval; c == nil || c.Rev != a.Rev || a.Rev == "" {
			t.Errorf("%s: revision differs between reads", name)
		}
	}
}

// A narrow pane wraps the title, the command and an option; the reading joins them back.
func TestReadCodexApprovalWrapped(t *testing.T) {
	screen := strings.Join([]string{
		"• Running npm install left-pad",
		"",
		"",
		"  Would you like to run the following",
		"  command?",
		"",
		"  Reason: Install the package the",
		"  user asked for",
		"",
		"  $ npm install left-pad --save-exact",
		"  --no-audit",
		"",
		"› 1. Yes, proceed (y)",
		"  2. Yes, and don't ask again for this",
		"     command in this session (a)",
		"  3. No, continue without running it (d)",
		"  4. No, and tell Codex what to do",
		"     differently (esc)",
		"",
		"  Press enter to confirm or esc to",
		"  cancel",
	}, "\n")
	a := ReadCodex(screen).Approval
	if a == nil {
		t.Fatal("no approval")
	}
	if a.Title != "Would you like to run the following command?" || a.Reason != "Install the package the user asked for" ||
		a.Command != "npm install left-pad --save-exact--no-audit" {
		t.Errorf("title %q reason %q command %q", a.Title, a.Reason, a.Command)
	}
	if len(a.Options) != 4 || a.Role(RoleSession).Key != "2" || a.Role(RoleDecline).Key != "3" || a.Role(RoleCancel).Key != "4" ||
		a.Options[1].Label != "Yes, and don't ask again for this command in this session" {
		t.Errorf("options %+v", a.Options)
	}
}

func TestReadCodexQuestion(t *testing.T) {
	for _, name := range []string{"codex-0.153.4-plan-question-120", "codex-0.163.0-plan-question-120"} {
		q := ReadCodex(fixture(t, name+".ansi")).Question
		if q == nil || q.Index != 1 || q.Total != 2 || q.Question != "Which color should the banner be?" {
			t.Fatalf("%s: %+v", name, q)
		}
		if len(q.Options) != 3 || q.Options[0].Label != "Red" || q.Options[0].Description != "warm" || !q.Options[0].Selected ||
			q.Other() == nil || q.Other().Key != "3" || q.Notes {
			t.Errorf("%s: options %+v notes %v", name, q.Options, q.Notes)
		}
	}
	q := ReadCodex(fixture(t, "codex-0.163.0-plan-question-2-120.txt")).Question
	if q == nil || q.Index != 2 || q.Question != "How tall should it be?" || q.Options[1].Label != "Large" {
		t.Errorf("second question: %+v", q)
	}
	q = ReadCodex(fixture(t, "codex-0.153.4-plan-question-notes-120.txt")).Question
	if q == nil || !q.Notes || q.NotesText != "" || q.Selected().Key != "3" {
		t.Errorf("notes open: %+v", q)
	}
}

// A message the user sent that starts with a number is history, not a picker: the box under it still reads.
func TestReadCodexHistoryNumbered(t *testing.T) {
	screen := "› 1. fix the login page\n\n• Done.\n\n› Ask Codex to do anything\n\n  gpt-6 low · /private/tmp/project\n"
	if s := ReadCodex(screen); s.View != CodexComposer || s.Draft != "" {
		t.Errorf("%+v", s)
	}
	picker := "› Ask Codex to do anything\n\n  Select model\n› 1. gpt-6 (current)\n  2. gpt-6-mini\n\n  Press enter to select\n"
	if s := ReadCodex(picker); s.View != CodexDialog {
		t.Errorf("picker: %+v", s)
	}
}

// Compact TUI layouts put the status directly below the composer, without a blank separating row.
func TestReadCodexComposerWithAdjacentFooter(t *testing.T) {
	footer := "  GPT-6.1-Sol xhigh · ~/project · Review project · Main [default]    Goal achieved (30m)\n" +
		"  ← for agents · ? for shortcuts                              ⚠ 2 warnings · f2 to view\n"
	for _, tc := range []struct{ name, composer, want string }{
		{"empty", "› Ask Codex to do anything\n", ""},
		{"typed", "› check the build\n", "check the build"},
		{"multiline", "› check the build\n  and keep my changes\n", "check the build\nand keep my changes"},
		{"footer-like draft", "› keep this text\n  GPT-6.1-Sol xhigh · /work/example\n  ← for agents · ? for shortcuts\n  and this final line\n", "keep this text\nGPT-6.1-Sol xhigh · /work/example\n← for agents · ? for shortcuts\nand this final line"},
		{"colored empty", "› \x1b[2mAsk Codex to do anything\x1b[0m\n", ""},
		{"colored real placeholder", "› \x1b[37mAsk Codex to do anything\x1b[0m\n", CodexPlaceholder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ReadCodex("• Done.\n\n" + tc.composer + footer)
			if s.View != CodexComposer || s.Draft != tc.want {
				t.Fatalf("view %q draft %q, want composer draft %q", s.View, s.Draft, tc.want)
			}
		})
	}
}
