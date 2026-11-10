// @vitest-environment jsdom
// The mark draws one picture, the page theme's, so the browser fetches one file (the hidden twin was fetched too), and the
// app icon offers its small sizes.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { AppMark } from "./AppMark";

it("draws only the theme's picture, smallest sharp size first, and swaps it with the theme", async () => {
  const host = document.createElement("div");
  const root = createRoot(host);
  act(() => root.render(<AppMark size={32} radius={8} />));
  const imgs = () => [...host.querySelectorAll("img")].map((i) => [i.getAttribute("src"), i.getAttribute("srcset"), i.getAttribute("sizes")]);
  expect(imgs()).toEqual([["/app-icon.png", "/app-icon-64.png 64w, /app-icon-128.png 128w, /app-icon.png 256w", "32px"]]);
  document.documentElement.classList.add("dark");
  await act(async () => {}); // the class observer's callback
  expect(imgs()).toEqual([["/app-icon-dark.png", "/app-icon-dark-64.png 64w, /app-icon-dark-128.png 128w, /app-icon-dark.png 256w", "32px"]]);
  document.documentElement.classList.remove("dark");
  act(() => root.unmount());
});
