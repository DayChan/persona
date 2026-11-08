// @vitest-environment jsdom
// The assistant's password card (`secret_run`, #1137) on the web: it shows the computer, the purpose and every command
// exactly as they will run, says to fill it in on the iPhone, and can only be declined. No password input, no Allow,
// no Always allow, no hand back; the quick paths (a row's answers, a notification's Allow) never take it, and the one
// way out to the hub (answerCard) sends nothing but a bare decline. The hub is faked (apiOp).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown> }[]);
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    calls.push({ path, body: init.json });
    return {};
  }),
}));
import { InteractionCard } from "./Interactions";
import { answerCard } from "../../lib/card-answer";
import { NOTE_MESSAGE, planClick } from "../../lib/notification-answer";
import { quickAnswers } from "../../lib/quick-answers";
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = () => [...host.querySelectorAll("button")].map((b) => b.textContent?.trim() || b.getAttribute("aria-label"));
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;

const COMMANDS = [
  "sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/com.example.backup.plist",
  'sudo -S -k -p \'\' sh -c "echo  two  spaces > /tmp/x"',
];
const card = (input: Record<string, unknown> = {}, extra: Partial<Interaction> = {}): Interaction => ({
  id: "ix_secret", machine_id: "m", session_id: "s", session_title: "Persona", kind: "permission", tool_name: "secret_run",
  tool_input: { machine: "macmini", machine_name: "Mac mini", purpose: "Install the launch daemon for the backup job", commands: COMMANDS, delivery: "stdin", ...input },
  summary: "Mac mini\nInstall the launch daemon for the backup job\n$ sudo -S -k -p '' true",
  fields: [{ key: "password", label: "Password", secret: true }],
  ...extra,
});

const session = (s: Partial<Session> = {}) => {
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s", ...s } as Session] }, interactions: [], answered: {} });
};
const render = (ix: Interaction) => act(() => root.render(<InteractionCard ix={ix} />));

beforeEach(() => {
  calls.length = 0;
  session();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("密码卡片（secret_run）", () => {
  it("写明电脑、用途和逐字的命令，叫你到 iPhone 上填写", () => {
    render(card());
    expect(host.textContent).toContain("用你的密码在 Mac mini 上运行");
    expect(host.textContent).toContain("Install the launch daemon for the backup job");
    expect([...host.querySelectorAll("[data-secret-run-command]")].map((e) => e.textContent)).toEqual(COMMANDS);
    expect(host.querySelector("[data-secret-run-elsewhere]")!.textContent).toBe("请在 iPhone 上填写");
  });

  it("只能拒绝：没有密码框、允许、总是允许和交还电脑", () => {
    render(card());
    expect(host.querySelector("input, textarea")).toBeNull();
    expect(buttons()).toEqual(["拒绝"]);
  });

  it("拒绝只发 allow:false", async () => {
    render(card());
    await act(async () => button("拒绝")!.click());
    await flush();
    expect(calls).toEqual([{ path: "/interactions/ix_secret/resolve", body: { allow: false } }]);
  });

  it("没有电脑名时用机器 id", () => {
    render(card({ machine_name: undefined }));
    expect(host.textContent).toContain("用你的密码在 macmini 上运行");
  });

  it("答复出口只放行不带别的内容的拒绝：允许、填值、拒绝理由一律不发", async () => {
    for (const body of [{ allow: true }, { allow: true, values: { password: "x" } }, { allow: false, message: "x" }, { allow: false, values: { password: "x" } }, { dismiss: true }]) {
      expect(await answerCard(card(), body)).toEqual({ ok: false, error: "请在 iPhone 上填写" });
    }
    expect(calls).toEqual([]);
    expect(await answerCard(card(), { allow: false })).toEqual({ ok: true, already: false });
    expect(calls).toEqual([{ path: "/interactions/ix_secret/resolve", body: { allow: false } }]);
  });

  it("不能审批的会话：拒绝也点不了", () => {
    session({ source_kind: "runtime", capabilities: { approve: false } } as Partial<Session>);
    render(card());
    expect(host.querySelector("fieldset")!.disabled).toBe(true);
    expect(button("拒绝")!.matches(":disabled")).toBe(true);
  });

  it("快捷路径不接这张卡：列表行没有一键答复，通知上的允许不发出（没带 fields 也一样）", () => {
    for (const ix of [card(), card({}, { fields: [] })]) {
      expect(quickAnswers(ix)).toBeNull();
      const click = (action: "allow" | "deny") => planClick({ type: NOTE_MESSAGE, ix: "ix_secret", action, until: Date.now() + 60_000 }, [ix], Date.now());
      expect(click("allow")).toBeUndefined();
      expect(click("deny")).toBeUndefined();
    }
  });
});
