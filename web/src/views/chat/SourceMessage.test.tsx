// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";
import { segments } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { SegmentView } from "./SegmentView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const mounted: { root: ReturnType<typeof createRoot>; host: HTMLDivElement }[] = [];
afterEach(() => { for (const { root, host } of mounted.splice(0)) { act(() => root.unmount()); host.remove(); } });
const render = (input: Item[]) => {
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host); mounted.push({ root, host });
  act(() => root.render(<>{segments(input).map((seg) => <SegmentView key={seg.id} seg={seg} live={false} mid="test" sid="test" expandFold={async () => {}} />)}</>));
  return host;
};
const input = (seq: number, text: string, kind = "conversation"): Item => ({ id: `${seq}#0`, seq, role: "user", kind: "text", text, source: { kind, title: "Build review", conversation_id: "test-source" } });

describe("source messages in the actual transcript renderer", () => {
  it.each(["conversation", "patrol", "followup", "supervision", "future-kind"])("renders %s at the source, not the user", (kind) => {
    const host = render([input(1, "Checks passed", kind)]);
    expect(host.querySelector("[data-user-bubble]")).toBeNull();
    expect(host.querySelector("[data-message-source]")?.getAttribute("data-message-source")).toBe(kind);
    expect(host.textContent).toContain("@Build review");
  });

  it("shows the source once for consecutive messages", () => {
    const host = render([input(1, "First report"), input(2, "Second report")]);
    expect(host.querySelectorAll(".chat-source-label")).toHaveLength(1);
    expect(host.querySelectorAll("[data-message-source]")).toHaveLength(2);
  });

  it("defaults long content to folded, with an accessible reversible toggle", () => {
    const host = render([input(1, "First line\nSecond line\nThird line\nFourth line")]);
    const toggle = host.querySelector("button[aria-expanded]") as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("[data-source-folded]")).not.toBeNull();
    act(() => toggle.click());
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector("[data-source-folded]")).toBeNull();
    act(() => toggle.click());
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps selected excerpts inspectable while hiding transport metadata", () => {
    const value = input(1, `<persona-context kind="message-source" title="">{"kind":"conversation"}</persona-context>\n<persona-context kind="doc" title="Selected excerpt" url="">Quoted acceptance requirement</persona-context>\nPlease check this excerpt`);
    const host = render([value]);
    expect(host.textContent).toContain("Selected excerpt");
    expect(host.textContent).not.toContain('{"kind":"conversation"}');
    const toggle = host.querySelector("button[aria-expanded]") as HTMLButtonElement;
    act(() => toggle.click());
    expect(host.textContent).toContain("Quoted acceptance requirement");
  });

  it("never exposes injected model instructions in the source block", () => {
    const value = input(1, "PRIVATE MODEL INSTRUCTION", "followup");
    value.injected = { kind: "followup", state: "once", line: "A reminder is due", detail: "Check the report" };
    const host = render([value]);
    expect(host.textContent).toContain("Check the report");
    expect(host.textContent).not.toContain("PRIVATE MODEL INSTRUCTION");
  });
});
