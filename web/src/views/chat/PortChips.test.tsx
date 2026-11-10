// @vitest-environment jsdom
// The ports an agent's answer links, as chips under it (docs/operations/ports.md "Recognizing links in replies"): a
// port an agent lists is live with its title, any other is asked about once, a stopped one is dimmed, and a click
// shows the page in the preview panel. The hub's probe is faked (api).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const request = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: (...args: unknown[]) => request(...args) }));
import { closePanel, getPanel } from "../../lib/panel";
import { resetPortPresence } from "../../lib/port-presence";
import { getState } from "../../lib/store";
import type { Machine, Session } from "../../lib/types";
import { t } from "../../i18n";
import { mightLinkPort, ReplyPorts } from "./PortChips";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const chips = () => [...host.querySelectorAll<HTMLButtonElement>("[data-testid=port-chips] button")];
const chip = (port: number) => host.querySelector<HTMLButtonElement>(`button[data-port="${port}"]`);

const agent = { machine_id: "m1", session_id: "s1", cwd: "/w/site", ports: [{ port: 5173, title: "Weather · landing" }] } as Session;
const studio = { id: "m1", name: "Studio", hostname: "studio.local", online: true } as Machine;
// i18n-ignore: an agent's reply as it is written
const ANSWER = "落地页起好了：http://localhost:5173/ 。API 文档在 http://studio:8000/docs，之前那版（http://127.0.0.1:4000/preview）已经停掉了。";

async function render(text = ANSWER, sid = "s1", cwd: string | null = "/w/site") {
  await act(async () => root.render(<ReplyPorts text={text} mid="m1" sid={sid} cwd={cwd ?? undefined} />));
  await flush();
}

describe("port chips under an agent's answer", () => {
  beforeEach(() => {
    Object.assign(getState(), { features: ["preview"], sessions: { m1: [agent, { ...agent, session_id: "a1", assistant: true, ports: [] }] }, machines: [studio] });
    request.mockReset();
    request.mockImplementation(async (path: string) => {
      if (path === "/machines/m1/ports/8000") return { port: 8000, listening: true, http: true, title: "API docs" };
      if (path === "/machines/m1/ports/4000") return { port: 4000, listening: false };
      throw new Error(`unexpected ${path}`);
    });
    resetPortPresence();
    closePanel();
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    Object.assign(getState(), { features: [], sessions: {}, machines: [] });
  });

  it("names each port with its page, asks only about the ports no agent lists, and dims a stopped one", async () => {
    await render();
    expect(chips().map((b) => b.dataset.port)).toEqual(["5173", "8000", "4000"]);
    expect(chip(5173)?.textContent).toBe(":5173Weather · landing");
    expect(chip(8000)?.textContent).toBe(":8000/docsAPI docs");
    expect(chip(4000)?.textContent).toBe(`:4000/preview${t("chat-portchips.stopped")}`);
    expect(chip(4000)?.className).toContain("opacity-60");
    expect(chip(5173)?.className).not.toContain("opacity-60");
    expect(chip(5173)?.title).toBe(`${t("chat-portchips.open_hint", { machine: "Studio" })}\nhttp://localhost:5173/`);
    expect(chip(4000)?.title).toContain(t("chat-portchips.gone_hint"));
    expect(request.mock.calls.map((c) => c[0]).sort()).toEqual(["/machines/m1/ports/4000", "/machines/m1/ports/8000"]);
  });

  it("asks once while the answer is fresh, however often it is drawn again", async () => {
    await render();
    await render(ANSWER + " ");
    expect(request).toHaveBeenCalledTimes(2);
  });

  it("opens the page in the preview panel of that session, at its path", async () => {
    await render();
    await act(async () => chip(4000)!.click());
    expect(getPanel()).toMatchObject({ kind: "preview", mid: "m1", sid: "s1", cwd: "/w/site", port: 4000, path: "/preview" });
  });

  it("draws nothing for an assistant conversation, a session without a folder, or a hub without previews", async () => {
    await render(ANSWER, "a1");
    expect(chips()).toHaveLength(0);
    await render(ANSWER, "s1", null);
    expect(chips()).toHaveLength(0);
    Object.assign(getState(), { features: ["sessions_delta"] });
    await render();
    expect(chips()).toHaveLength(0);
    expect(request).not.toHaveBeenCalled();
  });

  it("looks no further at an answer with no host and port in it", async () => {
    // i18n-ignore: an agent's reply as it is written
    for (const text of ["改好了，11:08 合入。", "See https://example.com/a and /w/site/index.html"]) expect(mightLinkPort(text)).toBe(false);
    for (const text of ["http://studio:8000/", "localhost:3000", "http://[::1]:4173", "https://example.com:8443/"]) expect(mightLinkPort(text)).toBe(true);
    await render("Done at 11:08, see https://example.com/a");
    expect(host.querySelector("[data-testid=port-chips]")).toBeNull();
  });

  it("leaves out this hub's own pages, which open as hub pages", async () => {
    await render(`Its chat: ${location.origin}/s/m1/s9 and the page http://localhost:5173/`);
    expect(chips().map((b) => b.dataset.port)).toEqual(["5173"]);
  });
});
