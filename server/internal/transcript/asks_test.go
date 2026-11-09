package transcript

import (
	"regexp"
	"strings"
	"testing"
)

func TestAskOf(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"closing question", "改好了，测试都过了。\n\n要不要我现在推到 beta？", "要不要我现在推到 beta？"},
		{"question then how to answer", "两个方案：\n\n1. 改 hub\n2. 改 node\n\n你选哪个？回复 1 或 2 即可。", "你选哪个？"},
		{"markdown and closers", "**要我顺便把 PR 也提了吗？**", "要我顺便把 PR 也提了吗？"},
		{"english", "Done. The build is green.\n\nShould I merge it now?", "Should I merge it now?"},
		{"saying without question mark", "已经写好草稿。你定一下用哪个方案，我再继续。", "你定一下用哪个方案，我再继续。"},
		{"let me know", "Both options are in the doc. Let me know which one you prefer.", "Let me know which one you prefer."},
		{"japanese", "修正しました。マージしてもいいですか", "マージしてもいいですか"},
		{"report only", "修好了，测试都过了。PR #12 已经提了。", ""},
		{"question answered by itself", "为什么会挂？因为缓存没清。\n\n清掉以后测试都过了。\n\n已经提交。", ""},
		{"question in code", "改完了。\n\n```go\nfmt.Println(\"ok?\")\n```\n\n都提交了。", ""},
		{"empty", "  \n", ""},
		{"emoji after question", "要发布吗？🚀", "要发布吗？"},
		{"tell me when", "权限开好以后跟我说一声，我把两个分支推上去。等的这段时间，我继续提 issue。", "权限开好以后跟我说一声，我把两个分支推上去。"},
		{"reply with a number", "8 款都在上面。\n\n你回我编号就行，也可以混搭。选定后，我按这套风格统一改一遍。", "选定后，我按这套风格统一改一遍。"},
		{"nothing to decide", "都合进去了，测试也过了。要不要再看一眼也行。没有需要你拍板的问题。", ""},
		{"optional chaining", "Fixed the `user?.name` crash, tests pass.", ""},
		{"query string", "PR: https://github.com/o/r/pull/9?w=1 is up.", ""},
		{"we need", "需要我们在下个版本再处理。", ""},
		{"logs tell us", "日志告诉我们是超时。", ""},
		{"if you want", "如果你想看细节，日志在 /tmp/x。", ""},
		{"whichever", "不管哪一个都能跑。", ""},
		{"particle without mark", "两处都改好了。要我顺便提 PR 吗", "要我顺便提 PR 吗"},
		{"why, a statement", "这就是之前失败的原因，也说明了为什么。", ""},
		{"still running, a statement", "CI 还在跑呢。", ""},
		{"code kept in the question", "Should I merge `feat/x` into `release-1.2`?", "Should I merge feat/x into release-1.2?"},
		{"question before nothing else", "要不要推到 beta？其他没有需要你决定的。", "要不要推到 beta？"},
		{"is-it statement", "我检查了是否有遗漏，没有。", ""},
	} {
		if got := AskOf(c.text); got != c.want {
			t.Errorf("%s: AskOf = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAskOfClips(t *testing.T) {
	long := ""
	for range 200 {
		long += "长"
	}
	got := []rune(AskOf(long + "吗？"))
	if len(got) != askMost+1 || got[askMost] != '…' {
		t.Fatalf("not clipped to %d runes: %d", askMost, len(got))
	}
}

// The sayings are matched on every session of every list a manager publishes, and most replies hold none of them: each
// pattern keeps its gate (lazyre.NewGated), which turns such a reply away before the regexp engine walks every
// alternative at every position (BenchmarkAskOf, a reply that asks nothing: about 72 to 5 microseconds). A
// saying that may start with anything (a class, a repeat, (?i)) would drop the gate. The code-block and 吗 patterns
// are gated too: most replies hold neither, and a gated replacement gives such a text back without copying it.
func TestAskPatternsGated(t *testing.T) {
	for name, re := range map[string]interface{ Gated() bool }{"askPhrases": askPhrases, "askNone": askNone, "askInline": askInline, "askFence": askFence,
		"askParticle": askParticle} {
		if !re.Gated() {
			t.Errorf("%s lost its gate: every match must start with a literal", name)
		}
	}
}

// cutListMark is the list-mark regexp it replaced, written out.
func TestCutListMarkMatchesRegexp(t *testing.T) {
	re := regexp.MustCompile(`^\s*(?:[-*+>#]+|\d+[.)、])\s*`)
	for _, line := range []string{"", "   ", "plain", "  plain ", "- item", "\t* item", "+item", "> quote", ">> - nested",
		"## Heading", "-", "1. one", "12) twelve", "3、三", "3。三", "42 is", "4.5 GB", "7.", " \r\f9.\tx", "— dash", "一、列表"} {
		if got, want := cutListMark(line), re.ReplaceAllString(line, ""); got != want {
			t.Errorf("cutListMark(%q) = %q, want %q", line, got, want)
		}
	}
}

// A reply without code blocks or markup is split into sentences without copying it, its lines or a list of all its
// sentences, and its sentences, which start with a capital but hold none of the sayings' words, are not lowered to be
// matched (MayMatchLowered): it allocates nothing. It was 16: two copies for each regexp and replacer pass, and the
// list's growth; then 3, a strings.ToLower of each sentence in the tail.
func TestAskOfAllocations(t *testing.T) {
	text := strings.Repeat("The tests pass and the branch is pushed. ", 6)
	if n := testing.AllocsPerRun(100, func() { AskOf(text) }); n > 0 {
		t.Fatalf("AskOf allocates %v times for a reply that asks nothing", n)
	}
}

// BenchmarkAskOf is a reply that reports and asks nothing, the usual last words of a session in a list.
func BenchmarkAskOf(b *testing.B) {
	text := strings.Repeat("The tests pass and the branch is pushed. ", 6)
	b.ReportAllocs()
	for b.Loop() {
		if AskOf(text) != "" {
			b.Fatal("asked")
		}
	}
}
