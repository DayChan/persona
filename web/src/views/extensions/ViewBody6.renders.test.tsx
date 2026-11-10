// @vitest-environment jsdom
// A carousel measures its edges on every scroll event and every resize report (#2998); with the edges unchanged it does not
// draw itself and its tiles again (snap scrolling and smooth paging fire a scroll event per frame). When an edge does
// change, it draws once and shows or hides the ‹ › buttons.
import { act, Profiler } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readView } from "../../lib/extension-ui";
import { t } from "../../i18n";
import { INERT, ViewSections } from "./ViewBody";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>([["persona.token", "tok"]]);
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// the carousel's resize callbacks, called by hand
let resized: (() => void)[] = [];
let host: HTMLDivElement;
let root: Root;
let commits = 0;

const view = readView({
  sections: [{ items: [{ type: "carousel", key: "c", title: "Scenes", tiles: Array.from({ length: 12 }, (_, i) => ({ key: `t${i}`, title: `Tile ${i}`, action: { id: `a${i}` } })) }] }],
});

beforeEach(() => {
  resized = [];
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(private cb: ResizeObserverCallback) {}
      observe() {
        resized.push(() => this.cb([], this as unknown as ResizeObserver));
      }
      unobserve() {}
      disconnect() {}
    },
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  commits = 0;
  act(() =>
    root.render(
      <Profiler id="view" onRender={() => void commits++}>
        <ViewSections view={view} {...INERT} />
      </Profiler>,
    ),
  );
  commits = 0;
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const list = () => host.querySelector<HTMLElement>('[role="list"]')!;
const geometry = (el: HTMLElement, g: { scrollLeft: number; clientWidth: number; scrollWidth: number }) => {
  for (const [k, v] of Object.entries(g)) Object.defineProperty(el, k, { configurable: true, get: () => v });
};
const scroll = () => act(() => void list().dispatchEvent(new Event("scroll")));
const resize = () => {
  for (const r of resized) act(r);
};
const back = () => host.querySelector(`button[aria-label="${t("extensions-viewbody.scroll_back")}"]`);
const on = () => host.querySelector(`button[aria-label="${t("extensions-viewbody.scroll_on")}"]`);

describe("a carousel's scroll", () => {
  it("draws nothing again while its edges stay the same", () => {
    expect(host.querySelectorAll('[role="listitem"]')).toHaveLength(12);
    for (let i = 0; i < 10; i++) scroll();
    expect(commits).toBe(0);
    expect(resized).toHaveLength(1);
    resize();
    expect(commits).toBe(0);
    expect(back()).toBeNull();
    expect(on()).toBeNull();
  });

  it("draws once when an edge changes and shows the buttons for the side with more", () => {
    // reports that change nothing leave no work behind for the next change
    resize();
    scroll();
    expect(commits).toBe(0);
    geometry(list(), { scrollLeft: 0, clientWidth: 300, scrollWidth: 1000 });
    scroll();
    expect(commits).toBe(1);
    expect(back()).toBeNull();
    expect(on()).not.toBeNull();
    for (let i = 0; i < 10; i++) scroll();
    expect(commits).toBe(1);

    geometry(list(), { scrollLeft: 350, clientWidth: 300, scrollWidth: 1000 });
    scroll();
    expect(commits).toBe(2);
    expect(back()).not.toBeNull();
    expect(on()).not.toBeNull();

    geometry(list(), { scrollLeft: 700, clientWidth: 300, scrollWidth: 1000 });
    scroll();
    expect(commits).toBe(3);
    expect(back()).not.toBeNull();
    expect(on()).toBeNull();

    // the content grows: the resize report brings the › back
    geometry(list(), { scrollLeft: 700, clientWidth: 300, scrollWidth: 1400 });
    resize();
    expect(commits).toBe(4);
    expect(on()).not.toBeNull();
  });
});
