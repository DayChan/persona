// @vitest-environment jsdom
// An MCP form or link card (docs/protocol/protocol.md "MCP elicitation") driven through the UI: a link is accepted only after it
// was opened from the card, and opens with noopener / noreferrer; a form sends what was typed (numbers as numbers),
// says what is missing, notes that a secret is not kept, and keeps a draft when the card is drawn anew; a schema outside
// the subset has no Submit; a session that can't approve can't open, submit or decline. The hub is faked (apiOp).
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
import { dropElicitationDraft } from "../../lib/elicitation";
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const click = async (el: Element | undefined) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};
function type(input: HTMLInputElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    set.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const noNavigation = (e: Event) => e.preventDefault(); // jsdom does not open tabs

const session = (s: Partial<Session> = {}) => {
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s", ...s } as Session] }, interactions: [], answered: {} });
};

const LINK = "https://auth.example.com/authorize?state=1";
const urlIx: Interaction = {
  id: "ix_url", machine_id: "m", session_id: "s", kind: "elicitation", mcp_server_name: "plugin_persona",
  elicitation: { mode: "url", message: "登录一下", url: LINK },
};
const formIx = (schema: Record<string, unknown>): Interaction => ({
  id: "ix_form", machine_id: "m", session_id: "s", kind: "elicitation", mcp_server_name: "example-mcp",
  elicitation: { mode: "form", message: "请填写", requested_schema: schema },
});
const schema = {
  type: "object",
  required: ["env", "count"],
  properties: {
    env: { type: "string", title: "环境" },
    count: { type: "integer", title: "数量", minimum: 1 },
    offset: { type: "integer", title: "偏移" },
    token: { type: "string", title: "令牌", format: "password" },
  },
};

const render = (ix: Interaction) => act(() => root.render(<InteractionCard ix={ix} />));
const input = (label: string) => {
  const l = [...host.querySelectorAll("label")].find((x) => x.textContent?.startsWith(label));
  return document.getElementById(l!.htmlFor) as HTMLInputElement;
};

beforeEach(() => {
  calls.length = 0;
  session();
  document.addEventListener("click", noNavigation);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.removeEventListener("click", noNavigation);
  dropElicitationDraft("ix_url");
  dropElicitationDraft("ix_form");
});

describe("MCP 链接卡片", () => {
  it("打开链接之前不能继续；链接不带 opener 和 referrer；服务器名同安卓", async () => {
    render(urlIx);
    expect(host.textContent).toContain("MCP 服务器：Persona");
    expect(button("已打开，继续")!.disabled).toBe(true);
    const link = host.querySelector<HTMLAnchorElement>("a[target=_blank]")!;
    expect(link.getAttribute("href")).toBe(LINK);
    expect(link.rel.split(" ").sort()).toEqual(["noopener", "noreferrer"]);
    await click(link);
    expect(button("已打开，继续")!.disabled).toBe(false);
    await click(button("已打开，继续"));
    expect(calls).toEqual([{ path: "/interactions/ix_url/resolve", body: { action: "accept" } }]);
  });

  it("重新画出来的卡片记得已经打开过", async () => {
    render(urlIx);
    await click(host.querySelector("a[target=_blank]")!);
    act(() => root.unmount());
    root = createRoot(host);
    render(urlIx);
    expect(button("已打开，继续")!.disabled).toBe(false);
  });

  it("拒绝、取消直接发出", async () => {
    render(urlIx);
    await click(button("拒绝"));
    render({ ...urlIx, id: "ix_url2" });
    await click(button("取消"));
    expect(calls.map((c) => c.body)).toEqual([{ action: "decline" }, { action: "cancel" }]);
  });

  it("不能审批的会话：链接、提交、拒绝都点不了", () => {
    session({ source_kind: "runtime", capabilities: { approve: false } } as Partial<Session>);
    render(urlIx);
    expect(host.querySelector("a[target=_blank]")).toBeNull();
    expect(host.querySelector("fieldset")!.disabled).toBe(true);
    for (const b of host.querySelectorAll("button")) expect(b.matches(":disabled")).toBe(true);
  });
});

describe("MCP 表单卡片", () => {
  it("没填的必填项直接说出来，填好后发出数字和文字", async () => {
    render(formIx(schema));
    expect(button("提交")!.disabled).toBe(true);
    expect([...host.querySelectorAll("[role=alert]")].map((e) => e.textContent)).toEqual(["请填写这一项", "请填写这一项"]);
    expect(host.textContent).toContain("此项不会保存，离开后需重新填写");
    expect(input("数量").inputMode).toBe("numeric");
    expect(input("偏移").inputMode).toBe(""); // may be negative: the phone keypad has no minus
    type(input("环境"), "prod");
    type(input("数量"), "3");
    type(input("偏移"), "-2");
    expect(button("提交")!.disabled).toBe(false);
    await click(button("提交"));
    expect(calls).toEqual([{ path: "/interactions/ix_form/resolve", body: { action: "accept", content: { env: "prod", count: 3, offset: -2 } } }]);
  });

  it("重新画出来的卡片留着填过的，不留密码", () => {
    render(formIx(schema));
    type(input("环境"), "prod");
    type(input("令牌"), "secret");
    act(() => root.unmount());
    root = createRoot(host);
    render(formIx(schema));
    expect(input("环境").value).toBe("prod");
    expect(input("令牌").value).toBe("");
  });

  it("子集之外的表单没有提交，叫你到终端处理", () => {
    render(formIx({ type: "object", properties: { x: { type: "object" } } }));
    expect(button("提交")).toBeUndefined();
    expect(button("拒绝")).toBeTruthy();
    expect(host.textContent).toContain("终端");
  });
});

function expectSecretInput(el: HTMLInputElement) {
  expect(el.type).toBe("password");
  expect(el.autocomplete).toBe("one-time-code");
  expect(el.getAttribute("autocapitalize")).toBe("off");
  expect(el.getAttribute("autocorrect")).toBe("off");
  expect(el.getAttribute("spellcheck")).toBe("false");
  expect(el.getAttribute("data-1p-ignore")).toBe("true");
  expect(el.getAttribute("data-lpignore")).toBe("true");
  expect(el.getAttribute("data-bwignore")).toBe("true");
}

function expectOrdinaryInput(el: HTMLInputElement) {
  expect(el.type).toBe("text");
  expect(el.autocomplete).toBe("off");
  for (const attr of ["autocapitalize", "autocorrect", "spellcheck", "data-1p-ignore", "data-lpignore", "data-bwignore"]) {
    expect(el.hasAttribute(attr), attr).toBe(false);
  }
}

describe("secret input browser hints and removal", () => {
  it("clears the terminal password DOM value before removal without changing ordinary inputs", () => {
    const ix: Interaction = { id: "ix_screen", machine_id: "m", session_id: "s", kind: "screen", secret: true };
    render(ix);
    const secret = host.querySelector("input")!;
    expectSecretInput(secret);
    type(secret, "synthetic-terminal-secret");
    expect(secret.value).toBe("synthetic-terminal-secret");
    act(() => root.render(null));
    expect(secret.value).toBe("");
    expect(secret.isConnected).toBe(false);

    render({ ...ix, secret: false });
    const ordinary = host.querySelector("input")!;
    expectOrdinaryInput(ordinary);
    type(ordinary, "public text");
    act(() => root.render(null));
    expect(ordinary.value).toBe("public text");
  });

  it("clears MCP secret fields on removal while leaving non-secret field behavior unchanged", () => {
    render(formIx({
      type: "object",
      properties: {
        code: { type: "string", title: "Code", format: "password" },
        hidden: { type: "string", title: "Hidden", writeOnly: true },
        name: { type: "string", title: "Name" },
      },
    }));
    const secrets = [input("Code"), input("Hidden")];
    for (const el of secrets) {
      expectSecretInput(el);
      type(el, "synthetic-mcp-secret");
    }
    const ordinary = input("Name");
    expectOrdinaryInput(ordinary);
    type(ordinary, "public name");
    act(() => root.render(null));
    for (const el of secrets) {
      expect(el.value).toBe("");
      expect(el.isConnected).toBe(false);
    }
    expect(ordinary.value).toBe("public name");
  });
});
