// @vitest-environment jsdom
// A row that asks first (`confirm`): the first choice turns it into the question, with what it means under it
// (`confirmDetail`, a node menu's Git ▸ Push, #1883); only the second choice runs it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Menu } from "./atoms";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("a row that asks first", () => {
  it("shows the question and what it means, and runs only on the second choice", () => {
    const push = vi.fn();
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() =>
      root!.render(<Menu anchor={null} onClose={() => {}} items={[{ key: "push", label: "Push", confirm: "Push now", confirmDetail: "main goes to its upstream branch. Never forced.", onSelect: push }]} />),
    );
    const row = () => document.querySelector("[role=menuitem]") as HTMLButtonElement;
    expect(row().textContent).toContain("Push");
    expect(row().textContent).not.toContain("Never forced");
    act(() => row().click());
    expect(push).not.toHaveBeenCalled();
    expect(row().textContent).toContain("Push now");
    expect(row().textContent).toContain("main goes to its upstream branch. Never forced.");
    expect(document.getElementById(row().getAttribute("aria-describedby")!)?.textContent).toBe("main goes to its upstream branch. Never forced.");
    act(() => row().click());
    expect(push).toHaveBeenCalledTimes(1);
  });
});
