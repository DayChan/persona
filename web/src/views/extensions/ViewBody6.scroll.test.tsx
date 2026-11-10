// @vitest-environment jsdom
// A carousel's ‹ › arrows scroll it by a page from script (docs/lessons/web.md "Scripted scrolling ignores reduced
// motion"): smoothly, and straight there when the system asks for less motion.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readView } from "../../lib/extension-ui";
import { t } from "../../i18n";
import { INERT, ViewSections } from "./ViewBody";

const media = vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  const state = { reduce: false };
  g.matchMedia = (q: string) => ({ matches: q.includes("reduce") && state.reduce, media: q, addEventListener() {}, removeEventListener() {} });
  return state;
});

const VIEW = readView({ sections: [{ items: [{ type: "carousel", key: "c", title: "Rooms", tiles: [{ title: "Hall" }, { title: "Kitchen" }, { title: "Study" }] }] }] });

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  media.reduce = false;
});

/** The carousel drawn, scrolled part way along a row wider than its box (jsdom lays nothing out), so both arrows show. */
function carousel() {
  act(() => root.render(<ViewSections view={VIEW} {...INERT} />));
  const box = host.querySelector<HTMLElement>('[role="list"]')!;
  Object.defineProperties(box, { clientWidth: { value: 300 }, scrollWidth: { value: 1000 }, scrollLeft: { value: 100 } });
  const scrollBy = vi.fn();
  box.scrollBy = scrollBy;
  act(() => void box.dispatchEvent(new Event("scroll")));
  const arrow = (label: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${t(label)}"]`)!;
  return { scrollBy, back: arrow("extensions-viewbody.scroll_back"), on: arrow("extensions-viewbody.scroll_on") };
}

describe.each([
  [false, "smooth"],
  [true, "auto"],
] as const)("a carousel's arrows, reduced motion %s", (reduce, behavior) => {
  it(`scroll by most of a page either way (${behavior})`, () => {
    media.reduce = reduce;
    const { scrollBy, back, on } = carousel();
    act(() => on.click());
    act(() => back.click());
    expect(scrollBy.mock.calls).toEqual([[{ left: 240, behavior }], [{ left: -240, behavior }]]);
  });
});
