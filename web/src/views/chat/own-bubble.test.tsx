import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { isOwnMessage, segments } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { SegmentView } from "./SegmentView";

// Only the messages you typed (or dictated, or a quick command expanded) are the pale blue bubble (#2643); Persona's
// own messages and the ones another session or conversation sent keep their look.
const u = (seq: number, text: string, extra: Partial<Item> = {}): Item => ({ id: `u${seq}#0`, seq, role: "user", kind: "text", text, ...extra });
const notice = "🔔 任务「跑测试」做完了（电脑）\n（这是 Persona 的任务通知，不是用户发的消息。……）\n\nAgent 最后说的：\n全过了。";
const followup = "⏰ 早报（定时任务）\n\n看看今天的日程。";
const relay = '<persona-context kind="context" title="来自对话「写周报」" url="">\n这条消息不是用户本人发的，是 Persona 的另一段对话（c1）用 conversation_send 发来的，第 1 跳（最多 3 跳）。\n按里面说的做。要把结果告诉那段对话就用 conversation_send 发回 c1；只是确认收到就不要回，免得两段对话来回发。\n</persona-context>\n周报写好了';

const cases: [string, Item, boolean][] = [
  ["typed in the composer", u(1, "看一下 CI", { client_msg_id: "c1" }), true],
  ["dictated", u(1, "帮我看看明天的日程", { client_msg_id: "c2" }), true],
  ["a quick command expanded", u(1, "提交并推送当前改动，写清楚 commit 信息"), true],
  ["typed in the terminal", u(1, "继续"), true],
  ["another session's (cross-session)", u(1, "build passed", { from: "build-bot" }), false],
  ["another conversation's (conversation_send)", u(1, relay), false],
  ["a task notice", u(1, notice), false],
  ["a scheduled run (⏰)", u(1, followup), false],
  ["one the hub marks injected", u(1, "x", { injected: { kind: "system", state: "lost_tasks", line: "node 重启过" } }), false],
];

const render = (it: Item) => {
  const seg = segments([it])[0];
  return renderToStaticMarkup(<SegmentView seg={seg} live={false} mid="m" sid="s" expandFold={async () => {}} />);
};

describe("the user's own bubble", () => {
  it.each(cases)("%s", (_, item, own) => {
    expect(isOwnMessage(segments([item])[0].user!)).toBe(own);
    const html = render(item);
    if (own) expect(html).toContain("bg-user-bubble");
    else expect(html).not.toContain("bg-user-bubble");
  });

  it("a relayed message is a source block, never a user bubble", () => {
    const html = render(u(1, relay));
    expect(html).toContain('data-message-source="conversation"');
    expect(html).not.toContain("data-user-bubble");
    expect(html).not.toContain("bg-neutral-bubble");
  });
});
