// @vitest-environment jsdom
// Rename and Rename space submit on Enter: a fast second Enter while the first request is out must not send it again
// (a session rename reaches the agent's pane as a typed /rename prompt, so a second POST types it twice).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import type { Session } from "../../lib/types";
import { RenameDialog, RenameSpaceDialog } from "./SessionDialogs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Request = { url: string; init: RequestInit; resolve: (value: Response) => void };
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const closed = vi.fn();
const session = { machine_id: "m", session_id: "s", title: "Old name", space_id: "sp", space_name: "Old space", status: "idle" } as Session;

const posts = (path: string) => requests.filter((r) => r.init.method === "POST" && r.url.endsWith(path));
const reply = (r: Request, status: number, value: unknown) => act(async () => {
  r.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));
});
const type = (value: string) => act(() => {
  const input = document.querySelector<HTMLInputElement>("[role=dialog] input")!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
});
// Enter in a field submits its form; each press is its own browser task, so each gets its own act
const enter = () => act(() => document.querySelector<HTMLFormElement>("[role=dialog] form")!.requestSubmit());

beforeEach(async () => {
  await initI18n("en");
  setToken("synthetic-owner-a");
  closed.mockClear();
  requests = [];
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init, resolve }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("RenameDialog", () => {
  it("sends one rename for a fast double Enter", async () => {
    await act(async () => root.render(<RenameDialog session={session} open onClose={closed} />));
    await type("New name");
    await enter();
    await enter();
    expect(posts("/sessions/m/s/rename")).toHaveLength(1);
    expect(JSON.parse(String(posts("/sessions/m/s/rename")[0].init.body))).toEqual({ title: "New name" });
    await reply(posts("/sessions/m/s/rename")[0], 200, {});
    expect(closed).toHaveBeenCalledTimes(1);
  });

  it("sends again once a failed rename has come back", async () => {
    await act(async () => root.render(<RenameDialog session={session} open onClose={closed} />));
    await type("New name");
    await enter();
    await reply(posts("/sessions/m/s/rename")[0], 500, { error: "offline" });
    await enter();
    expect(posts("/sessions/m/s/rename")).toHaveLength(2);
    expect(closed).not.toHaveBeenCalled();
  });
});

describe("RenameSpaceDialog", () => {
  it("sends one rename for a fast double Enter", async () => {
    await act(async () => root.render(<RenameSpaceDialog session={session} open onClose={closed} />));
    await type("New space");
    await enter();
    await enter();
    expect(posts("/machines/m/spaces/sp/rename")).toHaveLength(1);
    expect(JSON.parse(String(posts("/machines/m/spaces/sp/rename")[0].init.body))).toEqual({ name: "New space" });
    await reply(posts("/machines/m/spaces/sp/rename")[0], 200, {});
    expect(closed).toHaveBeenCalledTimes(1);
  });
});
