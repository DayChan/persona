// @vitest-environment jsdom
// An image in the file panel (#2982): "View larger" opens the chat's ImageViewer with the file's name on top and "Download" / "Copy",
// and once a HEIC is shown as the machine's PNG preview, "Download" saves that PNG under a .png name.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../../../i18n";
import type { FileContent } from "../../../lib/files";
import { ImageView, viewerImage } from "./FileView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const file = (name: string, mime: string): FileContent => ({ path: `/w/${name}`, name, size: 2048, mime, truncated: false, is_dir: false });
const byLabel = (label: string) =>
  [...document.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === label || b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

let host: HTMLDivElement;
let root: Root;
let saved: { name: string }[] = [];

beforeEach(() => {
  saved = [];
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("isSecureContext", true);
  vi.stubGlobal("ClipboardItem", class {});
  Object.defineProperty(navigator, "clipboard", { value: { write: async () => {} }, configurable: true });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(new Blob([String(input)], { type: "image/png" }))));
  URL.createObjectURL = () => "blob:x";
  URL.revokeObjectURL = () => {};
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    saved.push({ name: this.download });
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function openLarge() {
  const view = byLabel(t("session-files-fileview.view_large"));
  expect(view, "view large").toBeTruthy();
  await act(async () => view!.click());
  await flush();
  const dialog = document.querySelector('[role="dialog"]');
  expect(dialog, "viewer").toBeTruthy();
  return dialog!;
}

describe("an image in the file panel opens the chat's viewer", () => {
  it("with the file's name on top and 下载 / 拷贝, saving under the file's name", async () => {
    act(() => root.render(<ImageView file={file("shot.png", "image/png")} src="/orig/shot.png" />));
    const dialog = await openLarge();
    expect(dialog.getAttribute("aria-label")).toContain("shot.png");
    expect(byLabel(t("chat-imageviewer.copy")), "copy").toBeTruthy();
    const download = byLabel(t("chat-imageviewer.download"));
    expect(download, "download").toBeTruthy();
    await act(async () => download!.click());
    await flush();
    expect(saved.map((s) => s.name)).toEqual(["shot.png"]);
  });

  it("on a HEIC the browser cannot draw, keeps the name on top but saves the PNG preview it shows as .png", async () => {
    act(() => root.render(<ImageView file={file("IMG_1234.heic", "image/heic")} src="/orig/IMG_1234.heic" fallback="/thumb/IMG_1234.png" />));
    // the browser cannot decode the original: the preview the machine drew takes its place
    await act(async () => document.querySelector("img")!.dispatchEvent(new Event("error")));
    expect(document.querySelector("img")!.getAttribute("src")).toBe("/thumb/IMG_1234.png");
    const dialog = await openLarge();
    expect(dialog.getAttribute("aria-label")).toContain("IMG_1234.heic");
    await act(async () => byLabel(t("chat-imageviewer.download"))!.click());
    await flush();
    expect(saved.map((s) => s.name)).toEqual(["IMG_1234.png"]);
  });
});

describe("viewerImage", () => {
  it("names and types what is shown", () => {
    const heic = file("IMG_1234.heic", "image/heic");
    expect(viewerImage(heic, "/o", false)).toMatchObject({ src: "/o", label: "IMG_1234.heic", name: "IMG_1234.heic", mime: "image/heic" });
    expect(viewerImage(heic, "/t", true)).toMatchObject({ src: "/t", label: "IMG_1234.heic", name: "IMG_1234.png", mime: "image/png" });
    expect(viewerImage(file("noext", "image/heic"), "/t", true).name).toBe("noext.png");
  });
});
