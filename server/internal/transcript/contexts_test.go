package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The shape hub wrote Feishu /agent prompts in before the instructions moved into a block (placeholder values).
const legacyLarkPrompt = "示例用户在飞书「发布群」里用 /agent 叫你。这不是在电脑上开的会话，你只知道下面给出的上下文。\n\n" +
	"## 请求\n\n看下构建为什么挂了\n\n## 上下文\n\n" +
	"<persona-context kind=\"context\" title=\"请求的位置\">\n位置：群聊「发布群」\n</persona-context>\n" +
	"<persona-context kind=\"context\" title=\"项目 · 发布\">\n</persona-context>\n\n" +
	"知识库里这个会话的 id：lark-f1（kb_get / kb_search 查更多）。\n\n\n" +
	"## 怎么做\n\n- 能做的直接做掉。\n- 做完用 lark_agent_reply 回复一次。\n"

func outside(s string) string { return strings.TrimSpace(contextBlockRe.ReplaceAllString(s, "")) }

func TestLegacyLarkAgentPromptShowsOnlyTheRequest(t *testing.T) {
	got := legacyLarkAgent(legacyLarkPrompt)
	if outside(got) != "看下构建为什么挂了" {
		t.Fatalf("outside the blocks: %q\n%s", outside(got), got)
	}
	blocks := contextBlockRe.FindAllStringSubmatch(got, -1)
	if len(blocks) != 3 || !strings.Contains(blocks[0][1], `kind="instructions"`) {
		t.Fatalf("blocks: %q", blocks)
	}
	for _, want := range []string{"用 /agent 叫你", "## 怎么做", "lark_agent_reply", "知识库里这个会话的 id"} {
		if !strings.Contains(blocks[0][2], want) {
			t.Fatalf("instructions miss %q: %q", want, blocks[0][2])
		}
	}
	if legacyLarkAgent(got) != got {
		t.Fatal("rewriting twice changed it")
	}
}

func TestLegacyLarkAgentPromptWithoutARequest(t *testing.T) {
	old := strings.Replace(legacyLarkPrompt, "看下构建为什么挂了", "（没写具体要求：看上下文和引用的消息，判断他想让你做什么。）", 1)
	got := legacyLarkAgent(old)
	if outside(got) != "/agent" || !strings.Contains(got, "（没写具体要求") {
		t.Fatalf("got %s", got)
	}
}

func TestOtherTextIsNotRewritten(t *testing.T) {
	for _, s := range []string{"hello", "## 请求\n\nx\n\n## 上下文\n\ny\n\n## 怎么做\n\nz", "<persona-context kind=\"context\" title=\"t\" url=\"\">\nx\n</persona-context>\n看看"} {
		if got := legacyLarkAgent(s); got != s {
			t.Fatalf("%q became %q", s, got)
		}
	}
}

func TestLegacyLarkAgentPromptInATranscript(t *testing.T) {
	content, _ := json.Marshal(legacyLarkPrompt)
	items, meta := parse(t, `{"type":"user","uuid":"u1","timestamp":"2026-10-24T10:00:00Z","message":{"role":"user","content":`+string(content)+`}}`)
	it := only(t, items)
	if it.Kind != proto.KindText || outside(it.Text) != "看下构建为什么挂了" {
		t.Fatalf("item %+v", it)
	}
	if meta.FirstUser == "" {
		t.Fatal("no first user text")
	}
}

func TestLightKeepsTheRequestAndEveryTitle(t *testing.T) {
	long := strings.Repeat("一行聊天记录\n", 6000) // ~114 KB
	text := "<persona-context kind=\"instructions\" title=\"给 Agent 的说明\">\n说明\n</persona-context>\n" +
		"<persona-context kind=\"context\" title=\"前面的聊天\">\n" + long + "</persona-context>\n" +
		"<persona-context kind=\"doc\" title=\"文档 · 手册\" url=\"https://example.com/d\">\n" + long + "</persona-context>\n" +
		"看下构建为什么挂了"
	it, cut := light(proto.Item{Role: "user", Kind: proto.KindText, Text: text}, false)
	if !cut || it.FullSize != len(text) || len(it.Text) > pageTextLen {
		t.Fatalf("cut %v, full %d, len %d", cut, it.FullSize, len(it.Text))
	}
	if outside(it.Text) != "看下构建为什么挂了" {
		t.Fatalf("outside the blocks: %q", outside(it.Text))
	}
	blocks := contextBlockRe.FindAllStringSubmatch(it.Text, -1)
	if len(blocks) != 3 || strings.TrimSpace(blocks[0][2]) != "说明" || !strings.Contains(blocks[1][2], "（已截断") || !strings.Contains(blocks[2][1], `url="https://example.com/d"`) {
		t.Fatalf("blocks: %d %q", len(blocks), it.Text[:200])
	}
	// Not a user message: a plain cut, as before.
	it, _ = light(proto.Item{Role: "assistant", Kind: proto.KindText, Text: text}, false)
	if strings.HasSuffix(it.Text, "看下构建为什么挂了") {
		t.Fatal("an answer was cut like a message with context")
	}
}

func TestLightKeepsMessageSourceMetadata(t *testing.T) {
	for _, metadata := range []string{
		`{"kind":"conversation","conversation_id":"source","title":"` + strings.Repeat("s", 360) + `"}`,
		`{"author":"user"}`,
	} {
		block := "<persona-context kind=\"message-source\" title=\"\">\n" + metadata + "\n</persona-context>"
		text := block + "\n<persona-context kind=\"context\" title=\"Evidence\">\n" + strings.Repeat("e", 4000) + "\n</persona-context>\n" + strings.Repeat("x", 23800)
		it, cut := light(proto.Item{Role: "user", Kind: proto.KindText, Text: text}, false)
		if !cut || !strings.HasPrefix(it.Text, block) || !strings.HasSuffix(it.Text, strings.Repeat("x", 23800)) {
			t.Fatal("context clipping changed source JSON, user-authored sentinel, or input")
		}
		blocks := contextBlockRe.FindAllStringSubmatch(it.Text, -1)
		if len(blocks) != 2 || strings.TrimSpace(blocks[0][2]) != metadata || !strings.Contains(blocks[1][2], "…") {
			t.Fatal("source metadata participated in context water-fill")
		}
	}
}

func TestStripInjected(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"现在部署 hub", "现在部署 hub"},
		{`<persona-context kind="context" title="快照">` + "\n机器：a\n</persona-context>\n现在把 hub 部署到最新 master", "现在把 hub 部署到最新 master"},
		{"<persona-context>\n快照\n</persona-context>\n" + `<persona-context kind="doc" title="文档">正文</persona-context>` + "\n看看这个", "看看这个"},
		{"问题\n<persona-view-errors>\n坏了\n</persona-view-errors>", "问题"},
		{"<system-reminder>别管</system-reminder>好的", "好的"},
		{`前面<pasted_content id="ab12">贴的</pasted_content id="ab12">`, "前面贴的"},
		{`<persona-context kind="context" title="快照">` + "\n很长的快照被截断", ""}, // clipped inside the block
		{`<persona-context kind="context">只有块</persona-context>`, ""},
		{"a < b", "a < b"},
		// The new name (#3182): with attributes, bare, clipped, and mixed with the old one.
		{`<persona-context kind="context" title="快照">` + "\n机器：a\n</persona-context>\n现在把 hub 部署到最新 master", "现在把 hub 部署到最新 master"},
		{"<persona-context>\n快照\n</persona-context>\n" + `<persona-context kind="doc" title="文档">正文</persona-context>` + "\n看看这个", "看看这个"},
		{`<persona-context kind="context" title="快照">` + "\n很长的快照被截断", ""},
		{"问题\n<persona-context>\n被截断", "问题"},
		// Clipped inside the opening tag, before or after its name ended (titles made by clipping first).
		{`<persona-context kind="context" title="接上…`, ""},
		{`<persona-context kind="context" title="接上…`, ""},
		{"看看<persona-con…", "看看"},
		{"看看<persona-co...", "看看"},
		{"<system-rem", ""},
		{"x <p", "x <p"},
		{"a <b…", "a <b…"},
	} {
		if got := StripInjected(c.in); got != c.want {
			t.Errorf("StripInjected(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTitle(t *testing.T) {
	handoff := `<persona-context kind="context" title="接上旧主对话" url="">` + "\n旧对话的摘要\n</persona-context>"
	for _, c := range []struct{ in, want string }{
		{"修一下登录", "修一下登录"},
		{handoff + "\n接着做部署", "接着做部署"},
		{handoff, "接上旧主对话"}, // only a block: its title
		{strings.Replace(handoff, `title="接上旧主对话"`, `title="Another conversation"`, 1), "Another conversation"},
		{`<persona-context kind="context" title="来自对话「A &amp; B」">` + "\n摘要被截", "来自对话「A & B」"},
		// The title in the screenshot: the first message clipped at 40 characters inside the opening tag.
		{`<persona-context kind="context" title="接上…`, ""},
		{"<persona-context>\n快照\n</persona-context>", ""},
	} {
		if got := Title(c.in); got != c.want {
			t.Errorf("Title(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
