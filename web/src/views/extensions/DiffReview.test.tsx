// @vitest-environment jsdom
// UI version 5's code review drawn from the component showcase page code-review-v5: words that changed, a range of lines picked
// for a comment, drafts and suggestions, hidden lines asked for as lines, viewed marks, generated files folded, the
// pending review's bar — and what each sends.
import { act as flush } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { SHOWCASE } from "../../showcase/snapshots";
import type { UIAction, UIActionResult } from "../../lib/extension-ui";
import { INERT, ViewSections } from "./ViewBody";
import { ReviewBar, ReviewProvider, ReviewToolbar } from "./DiffReview";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

type Sent = [string, Record<string, string> | undefined];
let host: HTMLDivElement | null = null;
let root: Root | null = null;
beforeEach(() => localStorage.clear());
afterEach(() => {
  // Unmounted inside act, so React has no work left for after the environment is torn down.
  flush(() => root?.unmount());
  root = null;
  host?.remove();
  host = null;
});

function mount(sent: Sent[], answer: (a: UIAction, v?: Record<string, string>) => UIActionResult | null = () => ({})) {
  host = document.createElement("div");
  document.body.append(host);
  const r = createRoot(host);
  root = r;
  const view = SHOWCASE["code-review-v5"];
  const act = (a: UIAction, v?: Record<string, string>) => sent.push([a.id, { ...v, ...a.values }]);
  const fetch = async (a: UIAction, v?: Record<string, string>) => {
    sent.push([a.id, v]);
    return answer(a, v);
  };
  flush(() =>
    r.render(
      <ReviewProvider view={view} page="p" fetch={fetch}>
        <ReviewToolbar view={view} onHelp={() => {}} />
        <ViewSections view={view} {...INERT} onAct={act} />
        <ReviewBar view={view} act={act} busy="" />
      </ReviewProvider>,
    ),
  );
  return host;
}

const click = (el: Element | null | undefined) => {
  if (!el) throw new Error("not found");
  flush(() => (el as HTMLElement).click());
};
const mouse = (el: Element | null | undefined, kind: "mousedown" | "mouseup") => {
  if (!el) throw new Error("not found");
  flush(() => el.dispatchEvent(new MouseEvent(kind, { bubbles: true, button: 0 })));
};
const button = (root: Element, text: RegExp) => [...root.querySelectorAll("button")].find((b) => text.test(b.textContent ?? ""));
const diff = (root: Element, path: string) => root.querySelector(`[data-diff-path="${path}"]`)!;
const num = (root: Element, path: string, side: string, n: number) => diff(root, path).querySelector(`[data-ln="${side}"][data-n="${n}"]`);
const type = (el: HTMLTextAreaElement, text: string) =>
  flush(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

// the code views and the extension page are their own chunks (lib/lazy-page.ts, #1794): in before anything is drawn
beforeAll(() => loadPages(), 60_000);

describe("code review (UI version 5)", () => {
  it("marks the words that changed in a pair of lines", () => {
    const root = mount([]);
    const marked = [...diff(root, "src/upload/client.go").querySelectorAll(".bg-diff-add-word, .bg-diff-del-word")].map((e) => e.textContent);
    expect(marked.join("")).toContain("fmt.Errorf");
    expect(marked).not.toContain("return");
  });

  it("comments on a range dragged over, as a draft once a review is under way", () => {
    const sent: Sent[] = [];
    const root = mount(sent);
    mouse(num(root, "src/upload/client.go", "new", 23), "mousedown");
    flush(() => num(root, "src/upload/client.go", "new", 23)!.dispatchEvent(new MouseEvent("mouseout", { bubbles: true, relatedTarget: num(root, "src/upload/client.go", "new", 25) })));
    flush(() => document.dispatchEvent(new MouseEvent("mouseup")));
    type(diff(root, "src/upload/client.go").querySelector("textarea")!, "why three?");
    // two drafts already: adding to the review comes first
    click(button(diff(root, "src/upload/client.go"), /^(加入评审|Add to review)$/));
    expect(sent.at(-1)).toEqual(["draft_comment", { path: "src/upload/client.go", side: "new", line: "25", start_line: "23", body: "why three?" }]);
  });

  it("starts a suggestion with the lines picked", () => {
    const root = mount([]);
    click(num(root, "src/upload/client.go", "new", 21));
    click(button(diff(root, "src/upload/client.go"), /^(建议修改|Suggest a change)$/));
    expect(diff(root, "src/upload/client.go").querySelector("textarea")!.value).toBe("```suggestion\n\tif err != nil {\n```\n");
  });

  it("draws a suggestion in a comment as a small diff of its lines", () => {
    const root = mount([]);
    const card = diff(root, "src/upload/retry.go");
    click(card.querySelector("button[aria-expanded]")); // viewed: folded at first
    expect(card.textContent).toContain("建议的修改");
    expect(card.textContent).toContain("and returns f's last error");
  });

  it("asks for hidden lines as lines and shows them without a new page", async () => {
    const sent: Sent[] = [];
    const lines = Array.from({ length: 80 }, (_, i) => `line ${i + 1}`);
    const root = mount(sent, (a, v) => (a.id === "context" ? { lines: { path: v!.path, side: "new", start: 1, lines, total: 80 } } : {}));
    const d = diff(root, "src/upload/client.go");
    const bar = d.querySelector('[aria-label="展开全部"], [aria-label="Show all"]');
    await flush(async () => (bar as HTMLElement).click());
    expect(sent.at(-1)).toEqual(["context", { path: "src/upload/client.go", side: "new", from: "1", to: "19" }]);
    expect(d.textContent).toContain("line 19");
    expect(d.textContent).not.toContain("line 20 "); // a hunk's own line
  });

  it("marks a file viewed with the extension, and folds it", () => {
    const sent: Sent[] = [];
    const root = mount(sent);
    const d = diff(root, "src/upload/client.go");
    expect(d.querySelector('[role="checkbox"] > span')!.classList.contains("border-line-strong")).toBe(true);
    click(d.querySelector('[role="checkbox"]'));
    expect(sent.at(-1)).toEqual(["mark_viewed", { path: "src/upload/client.go", viewed: "true" }]);
    expect(d.querySelector('[role="checkbox"]')!.getAttribute("aria-checked")).toBe("true");
    const viewedMark = d.querySelector('[role="checkbox"] > span')!;
    expect(viewedMark.classList.contains("bg-green-tint")).toBe(true);
    expect(viewedMark.classList.contains("text-green")).toBe(true);
    const treeMark = root.querySelector('[role="tree"] button[title="src/upload/client.go"] > span[aria-label]')!;
    expect(treeMark.classList.contains("bg-green-tint")).toBe(true);
    expect(treeMark.classList.contains("text-green")).toBe(true);
    expect(d.querySelector(".code-hl")).toBeNull();
    expect(root.textContent).toContain("已看 2 / 5");
  });

  it("folds generated files and does not load them until asked", () => {
    const sent: Sent[] = [];
    const root = mount(sent);
    const d = diff(root, "go.sum");
    expect(d.textContent).toContain("生成的文件");
    expect(sent.some(([id, v]) => id === "load_diff" && v?.path === "go.sum")).toBe(false);
  });

  it("resolves a thread, and says drafts are drafts", () => {
    const sent: Sent[] = [];
    const root = mount(sent);
    click(button(diff(root, "src/upload/client.go"), /^(解决|Resolve)$/));
    expect(sent.at(-1)).toEqual(["resolve", { thread: "t3", resolved: "true" }]);
    const retry = diff(root, "src/upload/retry.go");
    click(retry.querySelector("button[aria-expanded]"));
    expect(retry.querySelector("[data-thread=t2]")!.textContent).toContain("草稿");
    expect(retry.querySelector("[data-thread=t2]")!.hasAttribute("data-unresolved")).toBe(false);
  });

  it("sends the pending review with its summary", () => {
    const sent: Sent[] = [];
    const root = mount(sent);
    expect(root.textContent).toContain("评审草稿 · 2 条");
    click(button(root, /^(完成评审|Finish review)$/));
    type([...root.querySelectorAll("textarea")].at(-1)!, "LGTM");
    click(button(root, /^批准$/));
    expect(sent.at(-1)).toEqual(["submit_review", { body: "LGTM", event: "approve" }]);
  });

  it("ignores changes that are only whitespace when asked", () => {
    const root = mount([]);
    click(button(root, /忽略空白|Ignore whitespace/));
    expect(localStorage.getItem("persona.diff.whitespace")).toBe("1");
  });
});
