// @vitest-environment jsdom
// useTranslation keeps the translation of a line on screen even when more than TEXTS_KEPT others came back after it:
// the line asks only as it mounts, so a dropped translation would vanish from it for good.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const calls = vi.hoisted(() => ({ texts: [] as string[][] }));
vi.mock("./api", () => ({
  api: vi.fn(async (_path: string, init?: { json?: { texts: string[] } }) => {
    const texts = init?.json?.texts ?? [];
    calls.texts.push(texts);
    return { texts: texts.map((t) => `译：${t}`) };
  }),
  enc: encodeURIComponent,
}));

import { requestTranslation, resetTranslateForTests, TEXTS_KEPT, translateCacheSizesForTests, translationOf, useTranslation } from "./translate";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const shown = "Could we move the design review to Tuesday afternoon?";

function Line({ text }: { text: string }) {
  const tr = useTranslation(text, "zh-Hans", true);
  return <p>{tr ?? ""}</p>;
}

let root: Root | undefined;
let host: HTMLDivElement;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  resetTranslateForTests();
  calls.texts = [];
  host = document.createElement("div");
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  vi.useRealTimers();
});

const settle = () => act(async () => void (await vi.advanceTimersByTimeAsync(350)));
const asked = (text: string) => calls.texts.filter((batch) => batch.includes(text)).length;

/** More than TEXTS_KEPT other translations, each read after the shown one's, so it is the first to go. */
async function overflow(from: number) {
  const msg = (i: number) => `Message number ${i} about the release`;
  for (let i = from; i < from + TEXTS_KEPT; i++) requestTranslation(msg(i), "zh-Hans");
  await settle();
  for (let i = from; i < from + TEXTS_KEPT; i++) translationOf(msg(i), "zh-Hans");
  for (let i = from + TEXTS_KEPT; i < from + TEXTS_KEPT + 30; i++) requestTranslation(msg(i), "zh-Hans");
  await settle();
}

describe("useTranslation", () => {
  it("keeps a shown translation through an overflow and drops it once the line is gone", async () => {
    act(() => root!.render(<Line text={shown} />));
    await settle();
    expect(host.textContent).toBe(`译：${shown}`);
    expect(translateCacheSizesForTests().pinned).toBe(1);

    await overflow(0);
    expect(host.textContent).toBe(`译：${shown}`);
    expect(asked(shown)).toBe(1);
    expect(translateCacheSizesForTests().texts).toBeLessThanOrEqual(TEXTS_KEPT + 1);

    act(() => root!.render(<p />));
    expect(translateCacheSizesForTests().pinned).toBe(0);
    await overflow(TEXTS_KEPT + 30);
    expect(translationOf(shown, "zh-Hans")).toBeUndefined();
    expect(translateCacheSizesForTests().texts).toBe(TEXTS_KEPT);

    act(() => root!.render(<Line text={shown} />)); // shown again: asked again, as on first view
    await settle();
    expect(host.textContent).toBe(`译：${shown}`);
    expect(asked(shown)).toBe(2);
  });

  it("keeps a translation two lines show until both are gone", async () => {
    act(() => root!.render(<><Line text={shown} /><Line text={shown} /></>));
    await settle();
    act(() => root!.render(<Line text={shown} />));
    await overflow(0);
    expect(host.textContent).toBe(`译：${shown}`);
    expect(asked(shown)).toBe(1);
  });
});
