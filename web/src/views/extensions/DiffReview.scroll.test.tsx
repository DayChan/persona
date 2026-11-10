// @vitest-environment jsdom
// A page of diffs scrolls from script (docs/lessons/web.md "Scripted scrolling ignores reduced motion"): the keys n / p,
// j / k and ] / [ and the toolbar's open-threads button scroll smoothly, and go straight there when the system asks for
// less motion.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readView } from "../../lib/extension-ui";
import { t } from "../../i18n";
import { ReviewKeys, ReviewProvider, ReviewToolbar } from "./DiffReview";

const media = vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  const state = { reduce: false };
  g.matchMedia = (q: string) => ({ matches: q.includes("reduce") && state.reduce, media: q, addEventListener() {}, removeEventListener() {} });
  return state;
});

// one file with an open thread, so the toolbar has its button
const VIEW = readView({
  sections: [{ items: [{ type: "diff", path: "a.go", threads: [{ id: "t1", side: "new", line: 3, comments: [{ author: "sam", body: "why?" }] }] }] }],
});

let host: HTMLDivElement;
let root: Root;
let scrolled: [Element, unknown][] = [];
const own = Object.getOwnPropertyDescriptor(Element.prototype, "scrollIntoView");

beforeEach(() => {
  scrolled = [];
  Element.prototype.scrollIntoView = function (this: Element, o?: unknown) {
    scrolled.push([this, o]);
  };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root.render(
      <ReviewProvider view={VIEW} page="p" fetch={async () => null}>
        <ReviewToolbar view={VIEW} onHelp={() => {}} />
        <ReviewKeys onHelp={() => {}} />
      </ReviewProvider>,
    ),
  );
});

afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  media.reduce = false;
  if (own) Object.defineProperty(Element.prototype, "scrollIntoView", own);
  else delete (Element.prototype as Partial<Element>).scrollIntoView;
});

/** An element of the page whose top is `top` px down the screen (jsdom lays nothing out). */
function at(attr: string, top: number): Element {
  const el = document.createElement("div");
  el.setAttribute(attr, "x");
  el.getBoundingClientRect = () => ({ top, bottom: top + 20, left: 0, right: 0, width: 0, height: 20, x: 0, y: top, toJSON: () => ({}) });
  document.body.append(el);
  return el;
}

const press = (key: string) => act(() => void document.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true })));
const openThreads = () => document.querySelector<HTMLButtonElement>(`button[title="${t("extensions-review.next_open")}"]`)!;

const MOTION = [
  [false, "smooth"],
  [true, "auto"],
] as const;

describe.each(MOTION)("a page of diffs, reduced motion %s", (reduce, behavior) => {
  beforeEach(() => {
    media.reduce = reduce;
  });

  it.each([
    ["n", "data-diff-path", 1, "start"],
    ["p", "data-diff-path", 0, "start"],
    ["j", "data-change", 1, "center"],
    ["k", "data-change", 0, "center"],
    ["]", "data-unresolved", 1, "center"],
    ["[", "data-unresolved", 0, "center"],
  ] as const)(`key %s scrolls to the %s element %i (${behavior})`, (key, attr, which, block) => {
    const els = [at(attr, 0), at(attr, 400)];
    press(key);
    expect(scrolled).toEqual([[els[which], { behavior, block }]]);
  });

  it(`the open-threads button scrolls to the next open thread (${behavior})`, () => {
    at("data-unresolved", 0);
    const next = at("data-unresolved", 400);
    act(() => openThreads().click());
    expect(scrolled).toEqual([[next, { behavior, block: "center" }]]);
  });

  it(`past the last one it goes back to the first (${behavior})`, () => {
    const first = at("data-unresolved", 0);
    at("data-unresolved", 40);
    act(() => openThreads().click());
    expect(scrolled).toEqual([[first, { behavior, block: "center" }]]);
  });
});
