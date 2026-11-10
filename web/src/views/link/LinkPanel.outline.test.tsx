// @vitest-environment jsdom
// A document's outline ("Contents" in the panel's header) scrolls to the heading picked (docs/lessons/web.md "Scripted
// scrolling ignores reduced motion"): smoothly, and straight there when the system asks for less motion.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { LinkPanel } from "./LinkPanel";

const media = vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  const state = { reduce: false };
  g.matchMedia = (q: string) => ({ matches: q.includes("reduce") && state.reduce, media: q, addEventListener() {}, removeEventListener() {} });
  return state;
});

const DOC = "https://example.feishu.cn/docx/Outline1";

let host: HTMLDivElement;
let root: Root;
let scrolled: [Element, unknown][] = [];
const own = Object.getOwnPropertyDescriptor(Element.prototype, "scrollIntoView");
const flush = async (n = 8) => {
  for (let i = 0; i < n; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  scrolled = [];
  Element.prototype.scrollIntoView = function (this: Element, o?: unknown) {
    scrolled.push([this, o]);
  };
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    let body: unknown = {};
    if (url.includes("/documents/read?")) body = { title: "Plan", markdown: "# Goals\n\nWhy.\n\n## Steps\n\nHow." };
    else if (url.includes("/documents/comments")) body = { threads: [] };
    return { ok: true, status: 200, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  media.reduce = false;
  vi.unstubAllGlobals();
  if (own) Object.defineProperty(Element.prototype, "scrollIntoView", own);
  else delete (Element.prototype as Partial<Element>).scrollIntoView;
});

describe.each([
  [false, "smooth"],
  [true, "auto"],
] as const)("a document's outline, reduced motion %s", (reduce, behavior) => {
  it(`scrolls to the heading picked (${behavior})`, async () => {
    media.reduce = reduce;
    act(() => root.render(<LinkPanel url={DOC} onClose={() => {}} />));
    await flush();
    const contents = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("link-linkpanel.outline")}"]`)!;
    act(() => contents.click());
    const steps = [...document.querySelectorAll<HTMLButtonElement>(`nav[aria-label="${t("link-linkpanel.outline")}"] button`)].find((b) => b.textContent === "Steps")!;
    scrolled = [];
    act(() => steps.click());
    expect(scrolled).toEqual([[host.querySelector("h2"), { block: "start", behavior }]]);
    expect(host.querySelector("h2")!.textContent).toBe("Steps");
  });
});
