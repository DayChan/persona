// @vitest-environment jsdom
// Machines › Add computer (docs/assistant/onboarding.md 7.2 #11): the name and system go to pair-codes' add, the system's instructions show,
// the dialog waits for the computer and then asks whether it is yours on the same screen.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// api() reads the token from localStorage, which this jsdom has none of
vi.hoisted(() => {
  const kept = new Map<string, string>();
  const g = globalThis as unknown as { localStorage: Pick<Storage, "getItem" | "setItem" | "removeItem"> };
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});
import { getState } from "../../lib/store";
import { AddComputer } from "./AddComputer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const PAIR = "persona://link?url=https%3A%2F%2Fhub.example.com&token=jp_x&machine=home";
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

let posts: { path: string; body: unknown }[] = [];
let refuse = "";
let machine: Record<string, unknown> | null = null;
let reads = 0; // GET /machines
let visibility: DocumentVisibilityState = "visible";

function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const path = String(input).split("?")[0]!.replace(/^.*\/api\/v1/, "");
  if (init?.method === "POST") posts.push({ path, body: init.body ? JSON.parse(String(init.body)) : undefined });
  if (path === "/auth/pair-codes") {
    if (refuse) return Promise.resolve(json({ error: "refused", code: refuse }, 409));
    machine = { id: "home", name: "家里的服务器", online: false, trust: "pending", error_code: "never_connected" };
    return Promise.resolve(
      json({
        token: "jp_x",
        expires_at: new Date(Date.now() + 600_000).toISOString(),
        pair_url: PAIR,
        machine: "home",
        name: "家里的服务器",
        trust: "pending",
        added: true,
        command: `persona-link pair '${PAIR}'`,
        install: { linux: `curl -fsSL https://example.com/install.sh | sh -s -- --node --pair '${PAIR}'` },
      }),
    );
  }
  if (path === "/machines") return (reads++, Promise.resolve(json({ machines: machine ? [machine] : [] })));
  if (path === "/machines/home/confirm") {
    machine = { ...machine, trust: undefined };
    return Promise.resolve(json({ ok: true }));
  }
  if (/^\/machines\/[abc]\/reject$/.test(path)) return Promise.resolve(json({ ok: true })); // the ones in the way
  if (path === "/machines/home/reject") {
    machine = null;
    return Promise.resolve(json({ ok: true }));
  }
  return Promise.resolve(json({ error: "not found" }, 404));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = async (label: string) => {
  const b = button(label);
  if (!b) throw new Error(`no button ${label}: ${text()}`);
  await act(async () => b.click());
  await flush();
};
const type = async (value: string) => {
  const input = document.querySelector("input")!;
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    set.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
// The dialog polls GET /machines every 3 s while it waits.
const poll = async () => {
  await act(async () => { vi.advanceTimersByTime(3100); });
  await flush();
};
const show = async (v: DocumentVisibilityState) => {
  visibility = v;
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  await flush();
};

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  posts = [];
  refuse = "";
  machine = null;
  reads = 0;
  visibility = "visible";
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
  localStorage.removeItem("persona.addComputer.os.home");
  vi.stubGlobal("fetch", vi.fn(hub));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  Reflect.deleteProperty(document, "visibilityState");
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function startLinux() {
  await act(async () => root.render(<AddComputer open onClose={() => {}} />));
  await flush();
  await click("Linux");
  await type("  家里的服务器 ");
  await click("下一步");
}

describe("AddComputer", () => {
  it("adds a Linux computer, shows its one line, and confirms it once it is online", async () => {
    await startLinux();
    expect(posts[0]).toEqual({ path: "/auth/pair-codes", body: { add: { name: "家里的服务器", os: "linux" } } });
    expect(text()).toContain("curl -fsSL https://example.com/install.sh");
    expect(text()).toContain("等「家里的服务器」连上来");

    await poll(); // still offline
    expect(text()).toContain("等「家里的服务器」连上来");

    machine = { ...machine, online: true, os: "linux", hostname: "home" };
    await poll();
    expect(text()).toContain("有一台电脑想加入：家里的服务器，是你的吗？");

    await click("是我的电脑");
    expect(posts.at(-1)).toEqual({ path: "/machines/home/confirm", body: undefined });
    expect(text()).toContain("已添加：家里的服务器");
  });

  it("stops asking for the computer while the tab is hidden and asks once right away on coming back", async () => {
    await startLinux();
    expect(reads).toBe(0); // the code was just made: the first read waits one period
    await show("hidden");
    await poll();
    await poll();
    await poll();
    expect(reads).toBe(0);
    machine = { ...machine, online: true };
    await show("visible");
    expect(reads).toBe(1);
    expect(text()).toContain("有一台电脑想加入：家里的服务器，是你的吗？");
  });

  it("removes it when it is not yours", async () => {
    await startLinux();
    machine = { ...machine, online: true };
    await poll();
    await click("不是，删掉");
    expect(posts.at(-1)?.path).toBe("/machines/home/reject");
    expect(text()).toContain("已删掉「家里的服务器」");
  });

  it("confirming one that is not online: remove it, or a new link (first choice only if it never connected)", async () => {
    const saved = { machines: getState().machines, features: getState().features };
    const asleep = { id: "home", name: "家里的服务器", online: false, trust: "pending" };
    Object.assign(getState(), { features: ["machines_add", "machine_trust"], machines: [asleep] });
    try {
      await act(async () => root.render(<AddComputer open onClose={() => {}} confirm={{ id: "home", name: "家里的服务器" }} />));
      await flush();
      expect(document.querySelector('[data-stage="offline"]')).not.toBeNull();
      // only asleep: a new link (it revokes the old pairing) is not the first choice
      expect(button("重新出链接")?.className).toBe(button("删掉")?.className);
      Object.assign(getState(), { machines: [{ ...asleep, error_code: "never_connected" }] });
      await act(async () => root.render(<AddComputer open onClose={() => {}} confirm={{ id: "home", name: "家里的服务器" }} />));
      await flush();
      expect(button("重新出链接")?.className).not.toBe(button("删掉")?.className);
      await click("删掉");
      expect(posts.at(-1)?.path).toBe("/machines/home/reject");
      expect(text()).toContain("已删掉「家里的服务器」");
    } finally {
      Object.assign(getState(), saved);
    }
  });

  it("gives a Mac the pairing link and Persona Link's install guide, not a command", async () => {
    await act(async () => root.render(<AddComputer open onClose={() => {}} />));
    await flush();
    await type("我的 MacBook");
    await click("下一步");
    expect(posts[0]?.body).toEqual({ add: { name: "我的 MacBook", os: "mac" } });
    expect(button("拷贝配对链接")).toBeTruthy();
    expect(document.querySelector<HTMLAnchorElement>('a[href$="persona-link-install.md"]')).toBeTruthy();
    expect(text()).toContain("要自己从源码编译");
    expect(text()).not.toContain("curl");
  });

  it("says in words why the hub refused", async () => {
    refuse = "tls_required";
    await act(async () => root.render(<AddComputer open onClose={() => {}} />));
    await flush();
    await type("我的 MacBook");
    await click("下一步");
    expect(text()).toContain("hub 还没打开安全连接");
    expect(button("下一步")).toBeTruthy(); // still on the form, to try again
  });

  it("says the computers in the way never connected (not 等你确认) and removes one in place", async () => {
    refuse = "too_many_pending";
    const waiting = ["a", "b", "c"].map((id) => ({ id, name: `电脑 ${id}`, online: false, trust: "pending" as const, error_code: "never_connected" }));
    Object.assign(getState(), { machines: waiting });
    try {
      await act(async () => root.render(<AddComputer open onClose={() => {}} />));
      await flush();
      await type("我的 MacBook");
      await click("下一步");
      const box = document.querySelector("[data-too-many]");
      expect(box?.textContent).toContain("都还没连上来过");
      expect(box?.textContent).not.toContain("等你确认");
      expect(box?.textContent).toContain("电脑 a");
      const remove = [...box!.querySelectorAll("button")].find((b) => b.textContent?.trim() === "删掉")!;
      await act(async () => remove.click());
      await flush();
      expect(posts.at(-1)?.path).toBe("/machines/a/reject");
      expect(document.querySelector("[data-too-many]")).toBeNull(); // back to the form, "Next" tries again
      expect(document.querySelector('[role="alert"]')).toBeNull();
      expect(button("下一步")).toBeTruthy();
    } finally {
      Object.assign(getState(), { machines: [] });
    }
  });

  it("asks a new code for a computer added before instead of adding one", async () => {
    await act(async () => root.render(<AddComputer open onClose={() => {}} again={{ id: "home", name: "家里的服务器" }} />));
    await flush();
    expect(posts[0]).toEqual({ path: "/auth/pair-codes", body: { machine: "home" } });
  });

  it("gives a Linux computer added before its one line again, from the system remembered when it was added", async () => {
    await startLinux();
    act(() => root.unmount());
    root = createRoot(host);
    posts = [];
    await act(async () => root.render(<AddComputer open onClose={() => {}} again={{ id: "home", name: "家里的服务器" }} />));
    await flush();
    expect(posts[0]).toEqual({ path: "/auth/pair-codes", body: { machine: "home" } });
    expect(text()).toContain("curl -fsSL https://example.com/install.sh");
  });

  it("lets the user pick the system of a computer added elsewhere before", async () => {
    await act(async () => root.render(<AddComputer open onClose={() => {}} again={{ id: "home", name: "家里的服务器" }} />));
    await flush();
    expect(button("拷贝配对链接")).toBeTruthy(); // nothing known: Mac
    await click("Linux");
    expect(text()).toContain("curl -fsSL https://example.com/install.sh");
    await click("Windows");
    expect(text()).toContain("persona-link.exe install");
  });

  it("drops a reply to a request made before the dialog closed", async () => {
    let release: (r: Response) => void = () => {};
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
        String(input).includes("/auth/pair-codes") && posts.length === 0
          ? (posts.push({ path: "/auth/pair-codes", body: "late" }), new Promise<Response>((r) => (release = r)))
          : hub(input, init),
      ),
    );
    await act(async () => root.render(<AddComputer open onClose={() => {}} again={{ id: "old", name: "旧电脑" }} />));
    await flush();
    await act(async () => root.render(<AddComputer open={false} onClose={() => {}} />));
    await act(async () => root.render(<AddComputer open onClose={() => {}} />));
    await flush();
    release(json({ token: "jp_old", expires_at: new Date(Date.now() + 600_000).toISOString(), pair_url: "persona://old", machine: "old", name: "旧电脑" }));
    await flush();
    expect(button("下一步")).toBeTruthy(); // still the new dialog's form
    expect(text()).not.toContain("旧电脑");
  });
});
