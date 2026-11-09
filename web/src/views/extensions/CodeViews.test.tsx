// @vitest-environment jsdom
// UI version 4's code hosting components drawn from the component showcase pages (code-review, code-browse, code-overview), and
// what pressing them sends: the action with the values it carries and the component's own.
import { act as flush } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { SHOWCASE } from "../../showcase/snapshots";
import type { UIAction } from "../../lib/extension-ui";
import { INERT, ViewSections } from "./ViewBody";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

let host: HTMLDivElement | null = null;
let root: Root | null = null;
afterEach(() => {
  // Unmounted inside act, so React has no work left for after the environment is torn down.
  flush(() => root?.unmount());
  root = null;
  host?.remove();
  host = null;
});

function mount(name: string, onAct: (a: UIAction, v?: Record<string, string>) => void) {
  host = document.createElement("div");
  document.body.append(host);
  const r = createRoot(host);
  root = r;
  flush(() => r.render(<ViewSections view={SHOWCASE[name]} {...INERT} onAct={onAct} />));
  return host;
}

const click = (el: Element | null | undefined) => {
  if (!el) throw new Error("not found");
  flush(() => (el as HTMLElement).click());
};
const byText = (root: Element, sel: string, text: string) => [...root.querySelectorAll(sel)].find((e) => e.textContent?.includes(text));
const type = (el: HTMLTextAreaElement, text: string) =>
  flush(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

// the code views and the extension page are their own chunks (lib/lazy-page.ts, #1794): in before anything is drawn
beforeAll(() => loadPages(), 60_000);

describe("代码托管组件", () => {
  it.each(["code-review", "code-browse", "code-overview"])("draws %s", (name) => {
    const html = renderToStaticMarkup(<ViewSections view={SHOWCASE[name]} {...INERT} />);
    expect(html).not.toContain("undefined");
    if (name === "code-review") {
      expect(html).toContain("src/upload/client.go");
      expect(html).toContain("Should this back off");
      expect(html).toContain("time.Sleep");
    }
    if (name === "code-browse") expect(html).toContain("Retry calls f until");
    if (name === "code-overview") {
      expect(html).toContain("<table");
      expect(html).toContain("请求修改");
    }
  });

  it("opens a file from the tree with its path", () => {
    const sent: [string, Record<string, string> | undefined][] = [];
    const root = mount("code-review", (a, v) => sent.push([a.id, v]));
    click(byText(root, "[role=tree] button", "client.go"));
    expect(sent).toEqual([["open_file", { path: "src/upload/client.go" }]]);
  });

  it("asks for a directory still to load", () => {
    const sent: [string, Record<string, string> | undefined][] = [];
    const root = mount("code-browse", (a, v) => sent.push([a.id, v]));
    click(byText(root, "[role=tree] button", "docs"));
    expect(sent).toEqual([["open_dir", { path: "docs" }]]);
    click(byText(root, "nav button", "src"));
    expect(sent[1][0]).toBe("open_dir");
  });

  it("comments on a line, replies to a thread, shows hidden lines, loads a diff", () => {
    const sent: [string, Record<string, string> | undefined][] = [];
    const root = mount("code-review", (a, v) => sent.push([a.id, v]));
    click(root.querySelector('[aria-label="评论第 22 行"], [aria-label="Comment on line 22"]'));
    type(root.querySelector("textarea")!, "nit");
    click([...root.querySelectorAll("button")].find((b) => /^(评论|Comment)$/.test(b.textContent ?? "")));
    expect(sent.at(-1)?.[0]).toBe("inline_comment");
    expect(sent.at(-1)?.[1]).toMatchObject({ side: "new", body: "nit" });

    click([...root.querySelectorAll("button")].find((b) => /^(回复|Reply)…$/.test(b.textContent ?? "")));
    type(root.querySelector("textarea")!, "ok");
    click([...root.querySelectorAll("button")].filter((b) => /^(评论|Comment)$/.test(b.textContent ?? "")).at(-1));
    expect(sent.at(-1)).toEqual(["reply", { thread: "t1", body: "ok" }]);

    click(root.querySelector('[aria-label="展开上面的行"], [aria-label="Show lines above"]'));
    expect(sent.at(-1)?.[0]).toBe("expand");
    expect(sent.at(-1)?.[1]).toMatchObject({ path: "src/upload/client.go", direction: "up" });

    click(byText(root, "button", "加载改动") ?? byText(root, "button", "Load diff"));
    expect(sent.at(-1)).toEqual(["load_diff", { path: "docs/upload.md" }]);
  });

  it("finds in a file, goes to a line, wraps, and links the lines picked", () => {
    const view = structuredClone(SHOWCASE["code-browse"]);
    const code = view.sections.flatMap((x) => x.items).find((it) => it.type === "code")!;
    code.line_url = "https://example.com/blob/main/retry.go#L{start}-L{end}";
    host = document.createElement("div");
    document.body.append(host);
    const r = createRoot(host);
    root = r;
    flush(() => r.render(<ViewSections view={view} {...INERT} onAct={() => {}} />));
    const card = host.querySelector(".code-hl")!.parentElement!;
    click(card.querySelector('[aria-label="查找"], [aria-label="Find"]'));
    const input = card.querySelector("input")!;
    flush(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "retry");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(card.querySelectorAll("mark").length).toBeGreaterThan(0);
    click(card.querySelector('[aria-label="长行换行"], [aria-label="Wrap long lines"]'));
    expect(localStorage.getItem("persona.code.wrap")).toBe("1");
    const nums = [...card.querySelectorAll<HTMLButtonElement>("button[aria-label^='选中第'], button[aria-label^='Select line']")];
    click(nums[1]);
    flush(() => nums[3].dispatchEvent(new MouseEvent("click", { bubbles: true, shiftKey: true })));
    expect(byText(card, "button", "复制链接") ?? byText(card, "button", "Copy link")).toBeTruthy();
  });

  it("sends what is typed with the composer's button", () => {
    const sent: [string, Record<string, string> | undefined][] = [];
    const root = mount("code-overview", (a, v) => sent.push([a.id, v]));
    type(root.querySelector("textarea")!, "LGTM");
    click(byText(root, "button", "批准"));
    expect(sent).toEqual([["approve", { body: "LGTM" }]]);
  });
});
