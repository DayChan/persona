// @vitest-environment jsdom
// UI version 6's controls on an extension's page (docs/extensions/extensions.md "UI version 6"): a change shows at once, one request per
// control is on its way at a time (a change meanwhile goes after it, the latest only), the extension's answer says
// what the control is now, a failure puts back the value it last drew, and a redraw meanwhile keeps the user's value.
// The hub is faked: every draw and action waits until the test answers it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Asked = { kind: "draw" | "act"; action?: string; values?: Record<string, string>; answer: (r: unknown) => void };
const hub = vi.hoisted(() => ({ asked: [] as Asked[], listeners: new Map<string, Set<(m: Record<string, unknown>) => void>>(), toasts: [] as [string, string][] }));

vi.mock("../../lib/extension-ui", async (original) => {
  const real = await original<typeof import("../../lib/extension-ui")>();
  return {
    ...real,
    renderUI: () => new Promise((resolve) => hub.asked.push({ kind: "draw", answer: (v) => resolve(real.readView(v)) })),
    actUI: (_e: string, _u: string, action: string, opts: { values?: Record<string, string> } = {}) =>
      new Promise((resolve, reject) =>
        hub.asked.push({
          kind: "act",
          action,
          values: opts.values,
          answer: (r) => {
            if (r instanceof Error) return reject(r);
            const o = (r ?? {}) as { view?: unknown; items?: unknown[] };
            resolve({ ...o, view: o.view ? real.readView(o.view) : undefined, items: o.items ? real.readView({ sections: [{ items: o.items }] }).sections[0].items : undefined });
          },
        }),
      ),
  };
});
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: (text: string, kind: string) => hub.toasts.push([kind, text]) }));
import { ExtensionUI } from "./ExtensionView";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

const lamp = (on: boolean, confirm?: string) => ({
  type: "row",
  key: "lamp",
  title: "主灯",
  control: { kind: "switch", name: "lamp", on, action: { id: "toggle", values: { entity: "light.main" }, ...(confirm ? { confirm } : {}) } },
});
const page = (on: boolean, confirm?: string) => ({ sections: [{ items: [lamp(on, confirm)] }] });

let host: HTMLDivElement;
let root: Root;
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const answer = async (a: Asked | undefined, r: unknown) => {
  expect(a).toBeTruthy();
  await act(async () => a!.answer(r));
  await settle();
};
const sw = () => host.querySelector<HTMLButtonElement>('[role="switch"]')!;
const flip = async () => {
  await act(async () => sw().click());
  await settle();
};
const acts = () => hub.asked.filter((a) => a.kind === "act");

beforeEach(async () => {
  hub.asked.length = 0;
  hub.toasts.length = 0;
  hub.listeners.clear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

const open = async (view: unknown) => {
  await act(async () => root.render(<ExtensionUI extension="demo" ui="home" history={false} />));
  await answer(hub.asked.find((a) => a.kind === "draw"), view);
};

describe("UI 6：行内控件", () => {
  it("shows the change at once and sends the new value with the action's own", async () => {
    await open(page(false));
    expect(sw().getAttribute("aria-checked")).toBe("false");
    await flip();
    expect(sw().getAttribute("aria-checked")).toBe("true");
    expect(acts()).toHaveLength(1);
    expect(acts()[0]).toMatchObject({ action: "toggle", values: { entity: "light.main", lamp: "true" } });
    // the extension answers with the row as it is now: that is shown
    await answer(acts()[0], { items: [lamp(true)] });
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });

  it("puts back the value the extension drew when the action fails, and says why", async () => {
    await open(page(false));
    await flip();
    expect(sw().getAttribute("aria-checked")).toBe("true");
    await answer(acts()[0], new Error("没有响应"));
    expect(sw().getAttribute("aria-checked")).toBe("false");
    expect(hub.toasts.some(([kind]) => kind === "error")).toBe(true);
  });

  it("has one request on its way per control: a change meanwhile goes after it, the latest only", async () => {
    await open(page(false));
    await flip(); // on
    await flip(); // off
    await flip(); // on again
    expect(acts()).toHaveLength(1);
    expect(sw().getAttribute("aria-checked")).toBe("true");
    await answer(acts()[0], {});
    // the first answered (empty: the page is drawn again) and the latest change goes now
    expect(acts()).toHaveLength(2);
    expect(acts()[1].values).toMatchObject({ lamp: "true" });
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });

  it("keeps the user's value through a redraw that comes meanwhile", async () => {
    await open(page(false));
    await flip();
    await act(async () => hub.listeners.get("extension_ui")?.forEach((l) => l({ type: "extension_ui", extension: "demo", invalidate: true })));
    await settle();
    const redraw = hub.asked.filter((a) => a.kind === "draw")[1];
    await answer(redraw, page(false)); // drawn before the extension saw the change
    expect(sw().getAttribute("aria-checked")).toBe("true");
    await answer(acts()[0], { items: [lamp(true)] });
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });

  it("an empty answer draws the page again, and the user's value shows until it does", async () => {
    await open(page(false));
    await flip();
    await answer(acts()[0], {});
    expect(sw().getAttribute("aria-checked")).toBe("true");
    const redraw = hub.asked.filter((a) => a.kind === "draw")[1];
    await answer(redraw, page(true));
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });

  it("asks first when the action says so; cancelled, nothing is sent or changed", async () => {
    await open(page(false, "打开主灯？"));
    await flip();
    expect(acts()).toHaveLength(0);
    expect(document.body.textContent).toContain("打开主灯？");
    const cancel = [...document.querySelectorAll("button")].find((b) => b.textContent === "取消");
    await act(async () => cancel!.click());
    await settle();
    expect(acts()).toHaveLength(0);
    expect(sw().getAttribute("aria-checked")).toBe("false");
  });
});
