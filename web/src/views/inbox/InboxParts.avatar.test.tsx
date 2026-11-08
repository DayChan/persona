// @vitest-environment jsdom
// Feishu items stored before the extension rewrote media paths keep the gateway's /agent/v1/media/ avatar: the browser
// got the web app's HTML for it and showed a broken image (#1353).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import { ItemAvatar, Monogram } from "./InboxParts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const larkItem = (avatar: string) => ({ id: "lark:chat:1", source: "lark", title: "Review group", avatar, lark: { feed_id: "chat", is_group: true } }) as unknown as InboxItem;

describe("inbox avatars", () => {
  it("loads a gateway avatar path through the hub", () => {
    act(() => root.render(<ItemAvatar it={larkItem("/agent/v1/media/default-avatar_1")} />));
    const src = host.querySelector("img:not([class*='size-'])")?.getAttribute("src") ?? "";
    expect(src).toContain("/api/v1/lark/media/default-avatar_1");
    expect(src).not.toContain("/agent/v1/");
  });

  it("shows the letter when the picture does not load, and tries a new one", () => {
    act(() => root.render(<Monogram name="Review group" src="/api/v1/lark/media/gone" />));
    const img = host.querySelector("img");
    expect(img).not.toBeNull();
    act(() => img!.dispatchEvent(new Event("error")));
    expect(host.querySelector("img")).toBeNull();
    expect(host.textContent).toBe("R");
    act(() => root.render(<Monogram name="Review group" src="/api/v1/lark/media/other" />));
    expect(host.querySelector("img")).not.toBeNull();
  });
});
