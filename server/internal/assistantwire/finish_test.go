package assistantwire

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The finished push's text and whether a turn finished normally (#2396): a quiet task's conversation pushes nothing
// on a normal finish, but an error, a cut-off answer or a stuck turn is still told.
func TestFinishedTextAndNormalFinish(t *testing.T) {
	for _, c := range []struct {
		name   string
		s      proto.Session
		text   string
		normal bool
	}{
		{"last words", proto.Session{LastText: "没有新邮件。"}, "没有新邮件。", true},
		{"no words", proto.Session{}, "", true},
		{"done work line", proto.Session{LastText: "好了", Work: &proto.WorkState{Phase: proto.WorkDone, Line: "合并了 PR"}}, "合并了 PR", true},
		{"stuck", proto.Session{LastText: "等部署", Work: &proto.WorkState{Phase: proto.WorkBlocked, Line: "在等部署跑完"}}, "在等部署跑完", false},
		{"model error", proto.Session{LastText: "没有新邮件。", TurnError: "unexpected status 503"}, "没有新邮件。", false},
		{"cut off", proto.Session{LastText: "API Error: 529 overloaded"}, "API Error: 529 overloaded", false},
		{"working line is not the text", proto.Session{LastText: "查完了", Work: &proto.WorkState{Phase: proto.WorkWorking, Line: "在查"}}, "查完了", true},
	} {
		if got := FinishedText(c.s); got != c.text {
			t.Errorf("%s: text %q, want %q", c.name, got, c.text)
		}
		if got := NormalFinish(c.s); got != c.normal {
			t.Errorf("%s: normal %v, want %v", c.name, got, c.normal)
		}
	}
}

// A Claude Code or headless Codex conversation that is not the main one may go unpushed: a runtime's failed turn is
// not on the session, and the main conversation's turns are the user's. A Codex turn the model service cut off
// carries TurnError (#2571) and is pushed as ever.
func TestQuietTaskFinish(t *testing.T) {
	ok := proto.Session{SessionID: "own-q", LastText: "没有新邮件。"}
	codex, runtime := ok, ok
	codex.Agent, runtime.Agent = "codex", "dsh"
	failed := ok
	failed.TurnError = "unexpected status 503"
	codexFailed := codex
	codexFailed.TurnError, codexFailed.TurnErrorKind = "unexpected status 503 Service Unavailable", proto.TurnErrorTransient
	for _, c := range []struct {
		name string
		s    proto.Session
		main string
		want bool
	}{
		{"claude, normal finish", ok, "main-1", true},
		{"no main conversation yet", ok, "", true},
		{"headless codex, normal finish", codex, "main-1", true},
		{"headless codex, failed turn", codexFailed, "main-1", false},
		{"headless codex, made the main conversation", codex, "own-q", false},
		{"a runtime", runtime, "main-1", false},
		{"made the main conversation", ok, "own-q", false},
		{"model error", failed, "main-1", false},
	} {
		if got := QuietTaskFinish(c.s, c.main); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Claude Code's own lines for a refused request are cut-offs at the start of an answer, and only there; a reply
// that only starts like one is a reply (#1710).
func TestCutOffRefusals(t *testing.T) {
	for text, want := range map[string]bool{
		"You've hit your session limit · resets 4:20pm (UTC)":                      true,
		"You’ve hit your weekly limit · resets Sep 28, 4pm (UTC)":                  true,
		"Please run /login · API Error: 403 forbidden":                             true,
		"Login expired · Please run /login":                                        true,
		"You've hit your target of 90% coverage; next I'll look at the rate limit": false,
		"Fixed it. If you see \"Please run /login\", sign in again.":               false,
	} {
		if got := CutOff(text); got != want {
			t.Errorf("CutOff(%q) = %v, want %v", text, got, want)
		}
	}
}
