// @vitest-environment jsdom
// Tab in an open menu (#2434): the menu closes and the key keeps its default, so the browser moves the focus on from the
// menu's button. The menu keeps the DOM focus on its button and draws the highlight, so without this Tab moved the focus
// to a control behind a menu that stayed open.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { Menu } from "./atoms";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;
let button: HTMLButtonElement;

beforeEach(() => {
  window.matchMedia = ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  // jsdom has no CSS.escape; the menu's outside-press check calls it
  const css = ((globalThis as { CSS?: Partial<typeof CSS> }).CSS ??= {});
  css.escape ??= (s: string) => s.replace(/[^\w-]/g, (c) => `\\${c}`);
  button = document.createElement("button");
  host = document.createElement("div");
  document.body.append(button, host);
  root = createRoot(host);
  button.focus();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  button.remove();
});

function Open() {
  const [open, setOpen] = useState(true);
  return open ? <Menu anchor={button} onClose={() => setOpen(false)} items={[{ key: "copy", label: "Copy", onSelect: () => {} }, { key: "close", label: "Close", confirm: "Close now", onSelect: () => {} }]} /> : null;
}

const key = (k: string, shiftKey = false) => {
  const e = new KeyboardEvent("keydown", { key: k, shiftKey, bubbles: true, cancelable: true });
  act(() => void (document.activeElement ?? document.body).dispatchEvent(e));
  return e;
};

describe("Tab in an open menu", () => {
  it.each([false, true])("closes it and lets the focus move on from its button (Shift: %s)", (shift) => {
    act(() => root.render(<Open />));
    key("ArrowDown");
    const tab = key("Tab", shift);
    expect(document.querySelector("[data-menu]")).toBeNull();
    expect(tab.defaultPrevented).toBe(false);
    expect(document.activeElement).toBe(button);
  });

  it("gives the focus back to the button first when the pointer left it on a row asking to confirm", () => {
    act(() => root.render(<Open />));
    const row = [...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((b) => b.textContent === "Close")!;
    row.focus();
    act(() => row.click());
    expect(row.textContent).toBe("Close now");
    const tab = key("Tab");
    expect(document.querySelector("[data-menu]")).toBeNull();
    expect(tab.defaultPrevented).toBe(false);
    expect(document.activeElement).toBe(button);
  });
});
