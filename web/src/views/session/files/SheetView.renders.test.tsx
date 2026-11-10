// @vitest-environment jsdom
// The spreadsheet grid draws only the cells on screen. It used to keep the raw scroll offset in state, so every scrolled
// pixel redrew some 750 cells although the rows and columns on screen had not changed; and a drag selection redrew on
// every pointer move inside one cell. It now keeps only the drawn window, moved in steps, and the focused cell.
import { act, Profiler } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Sheet } from "../../../lib/sheets";
import { SheetView } from "./SheetView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ROWS = 2000;
const COLS = 30;
const sheet: Sheet = {
  name: "Data",
  rows: Array.from({ length: ROWS }, (_, r) => Array.from({ length: COLS }, (_, c) => `r${r}c${c}`)),
  cols: COLS,
  merges: [],
  widths: [],
  cut: false,
};

// the grid's geometry, which jsdom does not lay out: a 800 × 600 scroller at the page's top left corner
const HEAD = 24; // the column letters' row
const RH = 26; // px per row; row 0 is frozen under the letters
const RN = 48; // the row numbers' column for 2000 rows

let host: HTMLDivElement;
let root: Root;
let commits = 0;
let frames: FrameRequestCallback[] = [];

const sized = (name: "clientHeight" | "clientWidth", px: number) =>
  Object.defineProperty(HTMLElement.prototype, name, {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("role") === "grid" ? px : 0;
    },
  });

beforeEach(() => {
  localStorage.clear();
  frames = [];
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => frames.push(cb));
  vi.stubGlobal("cancelAnimationFrame", () => {});
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  sized("clientHeight", 600);
  sized("clientWidth", 800);
  HTMLElement.prototype.setPointerCapture = () => {};
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Profiler id="sheet" onRender={() => commits++}><SheetView sheets={[sheet]} /></Profiler>));
  commits = 0;
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  // the stubs live on HTMLElement.prototype; jsdom's own clientHeight / clientWidth are on Element.prototype
  for (const name of ["clientHeight", "clientWidth", "setPointerCapture"]) Reflect.deleteProperty(HTMLElement.prototype, name);
});

const grid = () => host.querySelector<HTMLElement>('[role="grid"]')!;
const selection = () => host.querySelector(".font-mono")?.textContent; // the selection's name in the bar above the grid
const flushFrames = () =>
  act(() => {
    for (const cb of frames.splice(0)) cb(0);
  });
const scrollTo = (top: number, left = 0) => {
  const el = grid();
  Object.defineProperty(el, "scrollTop", { configurable: true, writable: true, value: top });
  Object.defineProperty(el, "scrollLeft", { configurable: true, writable: true, value: left });
  el.dispatchEvent(new Event("scroll"));
  flushFrames();
};
// a pointer over body row r (r >= 1, the grid scrolled to the top) and column 0, dy px below that row's top edge
const pointer = (type: string, r: number, dy = 5, dx = 10) =>
  act(() => {
    grid().firstElementChild!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0, clientX: RN + dx, clientY: HEAD + RH + (r - 1) * RH + dy }));
  });

describe("SheetView grid redraws", () => {
  it("does not redraw for every scrolled pixel", () => {
    scrollTo(3000);
    commits = 0;
    for (let px = 1; px <= 60; px++) scrollTo(3000 + px);
    expect(commits).toBeLessThanOrEqual(3);

    scrollTo(0, 400);
    commits = 0;
    for (let px = 1; px <= 30; px++) scrollTo(0, 400 + px);
    expect(commits).toBeLessThanOrEqual(3);
  });

  it("draws the rows and columns a long scroll brings on screen", () => {
    expect(host.textContent).not.toContain("r200c0");
    scrollTo(5000);
    expect(commits).toBeGreaterThan(0);
    expect(host.textContent).toContain("r200c0");

    expect(host.textContent).not.toContain("r200c25");
    commits = 0;
    scrollTo(5000, 1600);
    expect(commits).toBeGreaterThan(0);
    expect(host.textContent).toContain("r200c25");
  });

  it("does not redraw while a drag stays in one cell", () => {
    pointer("pointerdown", 1);
    expect(selection()).toBe("A2");
    commits = 0;
    for (let i = 0; i < 10; i++) pointer("pointermove", 1, 6 + i, 11 + i);
    expect(commits).toBe(0);

    pointer("pointermove", 2);
    expect(commits).toBe(1);
    expect(selection()).toBe("A2:A3");
  });
});
