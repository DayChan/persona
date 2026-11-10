// A model error's text-derived fix ("Fix it", Persona's model settings) belongs to Persona's own conversations only: an
// agent session's Claude Code error keeps the plain card (and "Retry" when it can help).
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { parseAPIError } from "../../lib/api-errors";
import type { ChatLine } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { ChatLineView } from "./Lines";

function line(text: string): ChatLine {
  const item = { id: "e1", seq: 1, role: "assistant", kind: "text", text } as Item;
  return { kind: "apiError", id: "err-e1", item, info: parseAPIError(text)!, count: 1 };
}
const render = (text: string, assistant: boolean) =>
  renderToStaticMarkup(<ChatLineView line={line(text)} mid="m" sid="s" expandFold={async () => {}} onRetry={() => {}} assistant={assistant} />);

describe("a model error's fix", () => {
  it("offers 去修 in a Persona conversation", () => {
    const html = render("Invalid API key · Fix external API key", true);
    expect(html).toContain("去修");
  });

  it("does not point an agent session at Persona's settings", () => {
    const html = render("Invalid API key · Fix external API key", false);
    expect(html).not.toContain("去修");
  });

  it("keeps 重试 on an agent session's passing error", () => {
    const html = render("API Error: 529 overloaded", false);
    expect(html).toContain("重试");
    expect(html).not.toContain("去修");
  });

  it("recognizes safeguards as requiring action and offers editing only on a supported agent chat", () => {
    const text = "API Error: Opus 5.5's safeguards flagged this message. Details: [frontier_llm]";
    const info = parseAPIError(text)!;
    expect(info.code).toBe("safeguards");
    expect(info.transient).toBe(false);
    const html = renderToStaticMarkup(<ChatLineView line={line(text)} mid="m" sid="s" expandFold={async () => {}} onRetry={() => {}} onEditLastMessage={() => {}} />);
    expect(html).toContain("编辑上一条消息");
    expect(html).toContain("模型的安全检查拦截了这条消息");
    expect(html).not.toContain(">重试</button>");
    expect(render(text, false)).not.toContain("编辑上一条消息");
    const main = renderToStaticMarkup(<ChatLineView line={line(text)} mid="m" sid="s" expandFold={async () => {}} assistant onEditLastMessage={() => {}} />);
    expect(main).not.toContain("编辑上一条消息");
  });
});
