// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { MessageBar } from "./InboxParts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Inbox thread actions are hover-only with a mouse; on touch the ⋯ is what reaches them.
it("folds its buttons out with the ⋯, and back after one is used", () => {
  const host = document.createElement("div");
  const root = createRoot(host);
  act(() => root.render(<MessageBar className="top-2"><button data-action /></MessageBar>));
  const bar = host.firstElementChild!;
  const more = bar.querySelector<HTMLElement>("button[aria-expanded]")!;
  const folded = () => host.querySelector("[data-action]")!.parentElement!.className.includes("[@media(hover:none)]:hidden");
  expect(bar.className).toContain("[@media(hover:none)]:flex");
  expect(folded()).toBe(true);
  act(() => more.click());
  expect(more.getAttribute("aria-expanded")).toBe("true");
  expect(folded()).toBe(false);
  act(() => host.querySelector<HTMLElement>("[data-action]")!.click());
  expect(folded()).toBe(true);
  act(() => root.unmount());
});
