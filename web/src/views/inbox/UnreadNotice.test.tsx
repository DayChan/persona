import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { UnreadNotice } from "./UnreadNotice";
import { TriageBanner } from "./TriageBanner";
import type { InboxTriage } from "../../lib/inbox";

const draw = (triage: InboxTriage | null | undefined) => renderToStaticMarkup(<UnreadNotice triage={triage} />);

// GET /api/v1/inbox `triage` shaped like the hub's (server/internal/assistant/inbox_admit.go): triage ok, nothing waiting,
// two sources not read this round, one read fine. The hub always sends `read_reason` with `read_error`; the source
// without one exercises the client's fallback wording (older hubs, a blank reason).
const hub: InboxTriage = JSON.parse(`{"state":"ok","waiting":0,"sources":[
  {"source":"ext-chat","name":"示例聊天","state":"ok","waiting":0,"read_error":"timeout","read_reason":"这一轮没读到：超时了"},
  {"source":"ext-tracker","name":"","state":"ok","waiting":0,"read_error":"rate_limited"},
  {"source":"github","name":"GitHub","state":"ok","waiting":0}
]}`);

describe("这一轮没读到的来源", () => {
  it("点名每个没读到的来源，用 hub 的原因（没有就用兜底）；读到的不提", () => {
    const html = draw(hub);
    expect(html).toContain('role="status"');
    expect(html).toContain("示例聊天：这一轮没读到：超时了");
    expect(html).toContain("ext-tracker：这一轮没读到，列表可能不全");
    expect(html).not.toContain("GitHub");
    expect(html).toContain("bg-field");
    expect(html).not.toContain("bg-orange-tint");
  });
  it("和等分拣的横幅分开：分拣 ok 时横幅不出，这条照样出", () => {
    expect(renderToStaticMarkup(<TriageBanner triage={hub} />)).toBe("");
    expect(draw(hub)).not.toBe("");
  });
  it("下一轮读到了、老 hub、没有来源：什么都不画", () => {
    expect(draw({ state: "ok", waiting: 0, sources: [{ source: "ext-chat", name: "示例聊天", state: "ok", waiting: 0 }] })).toBe("");
    expect(draw({ state: "ok", waiting: 0 })).toBe("");
    expect(draw(null)).toBe("");
    expect(draw({ state: "ok", waiting: 0, sources: [{ source: "x", state: "ok", waiting: 0, read_error: "  " }] })).toBe("");
  });
});
