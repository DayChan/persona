// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { handleUnauthorized, setToken } from "../../lib/api";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { SentSettings } from "./SentSettings";

const row = (id = 7, state: unknown = { can: true, exists: false }) => ({
  id, at: "2026-11-01T12:00:00Z", connector: "github", action: "github_comment",
  target: { ref: "sample/project#42" }, text: "Synthetic comment", summary: "A synthetic issue comment",
  origin: {}, let_through: { kind: "card" }, ...(state === null ? {} : { chat_rule: state }),
});
const rule = { id: "opaque:rule/42", connector: "github", chat: "sample/project#42", at: "2026-11-01T12:00:00Z", row: 999, summary: "Retained rule" };
type Call = { path: string; init: RequestInit; answer: (body: unknown, status?: number) => void };
let calls: Call[], root: Root, host: HTMLDivElement;
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.body.querySelectorAll("button")].find(b => b.textContent?.trim() === label)!;
const click = (label: string) => act(async () => { expect(button(label), label).toBeTruthy(); button(label).click(); });
const answer = (call: Call, body: unknown, status = 200) => act(async () => call.answer(body, status));
const reads = (path: string) => calls.filter(c => c.path === path && (!c.init.method || c.init.method === "GET"));
const mutations = () => calls.filter(c => ["POST", "DELETE"].includes(c.init.method ?? ""));
const ledger = "/connectors/sent", rules = `${ledger}/chat-rules`;
async function loaded(items: unknown[] = [row()], existing: unknown[] = []) {
  await act(async () => root.render(<SentSettings />));
  await answer(reads(ledger).at(-1)!, { items, next: 7 });
  expect(reads(rules)).toHaveLength(1);
  await answer(reads(rules)[0], { rules: existing, most: 500 });
}
async function openRow() { await act(async () => host.querySelector<HTMLButtonElement>("[data-sent] button")!.click()); }
beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await initI18n("en"); resetManagerChoice(); setToken("synthetic-a"); calls = [];
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>(resolve => {
    calls.push({ path: url.split("/api/v1")[1], init, answer: (body, status = 200) => resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })) });
  })));
});
afterEach(async () => { act(() => root.unmount()); host.remove(); document.body.innerHTML = ""; handleUnauthorized(() => {}); vi.unstubAllGlobals(); resetManagerChoice(); await initI18n("zh-Hans"); });

it("confirms a narrow destination and posts only the authoritative row, once, then verifies both resources", async () => {
  await loaded(); await openRow(); await click("Don't ask again…");
  expect(text()).toContain("GitHub"); expect(text()).toContain("sample/project#42"); expect(text()).toContain("comments");
  await act(async () => { button("Allow for this chat").click(); button("Allow for this chat").click(); });
  expect(mutations()).toHaveLength(1); expect(JSON.parse(mutations()[0].init.body as string)).toEqual({ row: 7 });
  await answer(mutations()[0], { rule, created: true });
  expect(reads(ledger)).toHaveLength(2); expect(reads(rules)).toHaveLength(2);
  await answer(reads(rules)[1], { rules: [rule], most: 500 });
  await answer(reads(ledger)[1], { items: [row(7, { can: true, exists: true, id: rule.id })], next: null });
  expect(text()).toContain("Allowed for this chat"); expect(text()).toContain("1 / 500");
});

it("retains a failed durable revoke and exact opaque id when GET omits it, then retries successfully", async () => {
  await loaded([], [rule]); await click("Revoke…"); await click("Revoke rule");
  const path = `${rules}/${encodeURIComponent(rule.id)}`;
  expect(mutations()[0].path).toBe(path);
  await answer(mutations()[0], { code: "store_failed", error: "synthetic storage error" }, 500);
  expect(text()).toContain("may return after the hub restarts");
  await answer(reads(rules).at(-1)!, { rules: [], most: 500 }); await answer(reads(ledger).at(-1)!, { items: [], next: null });
  expect(text()).toContain(rule.chat); await click("Retry revoke");
  expect(mutations()).toHaveLength(2); expect(mutations()[1].path).toBe(path);
  await answer(mutations()[1], { ok: true }); await answer(reads(rules).at(-1)!, { rules: [], most: 500 }); await answer(reads(ledger).at(-1)!, { items: [], next: null });
  expect(text()).not.toContain("Retry revoke"); expect(text()).not.toContain(rule.chat);
});

it.each([
  [404, {}, "does not support"], [405, {}, "does not support"],
  [403, {}, "full access"], [503, { code: "no_database" }, "no database"],
  [500, {}, "could not be loaded"],
])("keeps ledger and filters when the independent rule endpoint returns %s", async (status, body, expected) => {
  await act(async () => root.render(<SentSettings />));
  await answer(reads(ledger)[0], { items: [row()], next: 7 });
  await answer(reads(rules)[0], body, status);
  expect(text()).toContain("Synthetic comment"); expect(text()).toContain(expected);
  await click("Load earlier");
  expect(calls.at(-1)!.path).toBe(`${ledger}?before=7`);
  await answer(calls.at(-1)!, { items: [row(6)], next: null });
  await click("GitHub"); expect(calls.at(-1)!.path).toBe(`${ledger}?connector=github`);
  expect(reads(rules)).toHaveLength(1);
  await click("Refresh rules"); await answer(reads(rules).at(-1)!, { rules: [rule], most: 500 });
  expect(text()).toContain("Retained rule");
});

it.each([
  [null, "did not provide rule eligibility"],
  [{ can: false, exists: false, reason: "mail" }, "Mail needs confirmation"],
  [{ can: false, exists: false, reason: "recorded" }, "reported by an extension"],
  [{ can: false, exists: false, reason: "reply" }, "Replies still need confirmation"],
  [{ can: false, exists: false, reason: "review" }, "Reviews still need confirmation"],
  [{ can: false, exists: false, reason: "target" }, "does not identify one destination"],
  [{ can: false, exists: false, reason: "route" }, "This action cannot be allowed"],
  [{ can: false, exists: false, reason: "command" }, "supported chat gate"],
  [{ can: false, exists: false, reason: "future" }, "does not offer a rule"],
  [{ can: true, exists: true, id: rule.id }, "Allowed for this chat"],
])("uses authoritative row eligibility %j", async (state, expected) => {
  await loaded([row(7, state)]); await openRow();
  expect(text()).toContain(expected); expect(button("Don't ask again…")).toBeUndefined(); expect(mutations()).toHaveLength(0);
});

it.each([
  [404, { code: "not_found" }, "no longer available"],
  [409, { code: "no_chat_rule", reason: "reply" }, "Replies still need confirmation"],
  [409, { code: "too_many_rules" }, "limit has been reached"],
  [503, { code: "no_database" }, "no database"],
  [403, {}, "full access"], [404, {}, "does not support"], [405, {}, "does not support"],
])("reports add failure %s without claiming success", async (status, body, expected) => {
  await loaded(); await openRow(); await click("Don't ask again…"); await click("Allow for this chat");
  await answer(mutations()[0], body, status); expect(text()).toContain(expected);
  await answer(reads(rules).at(-1)!, { rules: [], most: 500 }); await answer(reads(ledger).at(-1)!, { items: [row()], next: null });
  expect(text()).not.toContain("Allowed for this chat"); expect(text()).toContain(expected);
});

it("cancels admission, retries a failed add, and holds admission through authoritative verification", async () => {
  await loaded(); await openRow(); await click("Don't ask again…"); await click("Cancel"); expect(mutations()).toHaveLength(0);
  await click("Don't ask again…"); await click("Allow for this chat"); await answer(mutations()[0], {}, 500);
  await answer(reads(rules).at(-1)!, { rules: [], most: 500 }); await answer(reads(ledger).at(-1)!, { items: [row()], next: null });
  expect(text()).toContain("could not confirm"); await click("Allow for this chat");
  expect(mutations()).toHaveLength(2); await answer(mutations()[1], { created: true, rule });
  expect(button("Don't ask again…").disabled).toBe(true);
  await answer(reads(rules).at(-1)!, { rules: [rule], most: 500 }); await answer(reads(ledger).at(-1)!, { items: [row(7, { can: true, exists: true, id: rule.id })], next: null });
  expect(mutations()).toHaveLength(2);
});

it("does not resurrect a revoked rule from older reads or ledger pages", async () => {
  await loaded([row(7, { can: true, exists: true, id: rule.id })], [rule]);
  await click("Load earlier"); const oldPage = calls.at(-1)!;
  await click("Refresh rules"); const stale = reads(rules).at(-1)!;
  await click("Revoke…"); await click("Cancel"); expect(mutations()).toHaveLength(0);
  await click("Revoke…"); await click("Revoke rule");
  await answer(mutations()[0], { ok: true });
  await answer(reads(rules).at(-1)!, { rules: [], most: 500 }); await answer(reads(ledger).at(-1)!, { items: [row()], next: null });
  await answer(stale, { rules: [rule], most: 500 }); await answer(oldPage, { items: [row(6, { can: true, exists: true, id: rule.id })], next: null });
  expect(text()).toContain("No chat approval rules"); expect(text()).not.toContain("Retained rule"); expect(host.querySelector('[data-sent="6"]')).toBeNull();
});

it.each(["credential", "same-token", "manager", "manager-return", "default-manager", "navigation"])("retires old reads/actions/finally on %s changes", async mode => {
  await loaded([], [rule]); await click("Revoke…"); await click("Revoke rule"); const old = mutations()[0];
  await act(async () => {
    if (mode === "credential") setToken("synthetic-b");
    if (mode === "same-token") { setToken("synthetic-b"); setToken("synthetic-a"); }
    if (mode === "manager") chooseManager("synthetic-manager");
    if (mode === "manager-return") { chooseManager("synthetic-manager"); chooseManager(null); }
    if (mode === "default-manager") setRosterDefault("synthetic-manager");
    if (mode === "navigation") root.render(<p>Another page</p>);
  });
  const count = calls.length;
  await answer(old, { code: "store_failed" }, 500);
  expect(calls).toHaveLength(count); expect(text()).not.toContain("Retry revoke");
  if (mode !== "navigation") {
    await answer(reads(ledger).at(-1)!, { items: [], next: null }); await answer(reads(rules).at(-1)!, { rules: [], most: 500 });
    expect(text()).toContain("No chat approval rules");
  }
});

it("keeps current 401 logout but fences an old generation's 401", async () => {
  const unauthorized = vi.fn(); handleUnauthorized(unauthorized);
  await loaded([], [rule]); await click("Revoke…"); await click("Revoke rule"); const old = mutations()[0];
  expect(new Headers(old.init.headers).get("Authorization")).toBe("Bearer synthetic-a");
  await act(async () => setToken("synthetic-b"));
  await answer(old, {}, 401); expect(unauthorized).not.toHaveBeenCalled();
  await answer(reads(ledger).at(-1)!, {}, 401); expect(unauthorized).toHaveBeenCalledOnce();
  expect(new Headers(reads(ledger).at(-1)!.init.headers).get("Authorization")).toBe("Bearer synthetic-b");
});

it("uses the settings route's default manager contract, not the assistant switcher", async () => {
  setRosterDefault("synthetic-default"); chooseManager("synthetic-other"); await loaded();
  for (const c of calls.filter(c => c.path.startsWith(ledger))) expect({ path: c.path, manager: new Headers(c.init.headers).get("X-Persona-Manager") }).toEqual({ path: c.path, manager: null });
});

it("reports failed verification without claiming an authoritative result, and recovers with refresh", async () => {
  await loaded([], [rule]); await click("Revoke…"); await click("Revoke rule"); await answer(mutations()[0], { ok: true });
  await answer(reads(rules).at(-1)!, {}, 500); await answer(reads(ledger).at(-1)!, {}, 500);
  expect(text()).toContain("latest state could not be read");
  await click("Refresh rules"); await answer(reads(rules).at(-1)!, { rules: [], most: 500 }); await answer(reads(ledger).at(-1)!, { items: [], next: null });
  expect(mutations()).toHaveLength(1); expect(text()).not.toContain("latest state could not be read");
  expect(text()).toContain("No chat approval rules");
});

it("names the Lark destination and limits confirmation to messages, using only the row ID", async () => {
  await loaded([{ ...row(), connector: "lark", action: "POST /conversations/synthetic-chat/messages", target: { conversation: "synthetic-chat", command: "synthetic-gate" } }]);
  await openRow(); await click("Don't ask again…");
  expect(text()).toContain("messages through"); expect(text()).toContain("synthetic-chat"); expect(text()).toContain("Other actions still ask");
  await click("Allow for this chat"); expect(JSON.parse(mutations()[0].init.body as string)).toEqual({ row: 7 });
});

it("retires initial reads on a cross-tab credential change and aborts the old rule request", async () => {
  await act(async () => root.render(<SentSettings />)); const oldLedger = reads(ledger)[0], oldRules = reads(rules)[0];
  await act(async () => { localStorage.setItem("persona.token", "synthetic-b"); window.dispatchEvent(new StorageEvent("storage", { key: "persona.token" })); });
  expect(oldRules.init.signal?.aborted).toBe(true);
  await answer(reads(ledger).at(-1)!, { items: [], next: null }); await answer(reads(rules).at(-1)!, { rules: [], most: 500 });
  await answer(oldLedger, { items: [row()], next: null }); await answer(oldRules, { rules: [rule], most: 500 });
  expect(text()).not.toContain("Synthetic comment"); expect(text()).not.toContain(rule.chat);
});

it("closing a row retires its unsent confirmation", async () => {
  await loaded(); await openRow(); await click("Don't ask again…");
  const retired = button("Allow for this chat"); await click("Done");
  await act(async () => retired.click()); expect(mutations()).toHaveLength(0);
  await openRow(); expect(button("Allow for this chat")).toBeUndefined();
});

it.each([403, 404, 503, 500])("keeps revoke context after failure %s and refresh, without claiming durable removal", async status => {
  await loaded([], [rule]); await click("Revoke…"); await click("Revoke rule");
  await answer(mutations()[0], status === 404 ? { code: "not_found" } : status === 503 ? { code: "no_database" } : {}, status);
  await answer(reads(rules).at(-1)!, { rules: [rule], most: 500 }); await answer(reads(ledger).at(-1)!, { items: [], next: null });
  expect(text()).toContain(rule.chat); expect(button("Retry revoke")).toBeTruthy();
});

it("survives StrictMode effect cleanup and still admits exactly one confirmed action", async () => {
  const { StrictMode } = await import("react");
  await act(async () => root.render(<StrictMode><SentSettings /></StrictMode>));
  await answer(reads(ledger).at(-1)!, { items: [row()], next: null }); await answer(reads(rules).at(-1)!, { rules: [], most: 500 });
  await openRow(); await click("Don't ask again…");
  await act(async () => { button("Allow for this chat").click(); button("Allow for this chat").click(); });
  expect(mutations()).toHaveLength(1);
  await answer(mutations()[0], { created: true, rule });
  await answer(reads(ledger).at(-1)!, { items: [row(7, { can: true, exists: true, id: rule.id })], next: null }); await answer(reads(rules).at(-1)!, { rules: [rule], most: 500 });
  expect(text()).toContain("Allowed for this chat");
});

it("does not attach an old row's late add failure to a reopened selection", async () => {
  await loaded(); await openRow(); await click("Don't ask again…"); await click("Allow for this chat"); const old = mutations()[0];
  await click("Done"); await openRow(); await answer(old, { code: "not_found" }, 404);
  expect(text()).not.toContain("no longer available");
  await answer(reads(rules).at(-1)!, { rules: [], most: 500 }); await answer(reads(ledger).at(-1)!, { items: [row()], next: null });
  expect(button("Don't ask again…").disabled).toBe(false);
});
