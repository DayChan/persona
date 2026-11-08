// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import { handleUnauthorized, pastHub, setToken } from "../lib/api";
import { resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import { getState } from "../lib/store";
import type { Todo } from "../lib/todos";
import { TodosView } from "./Todos";

type Request = { path: string; init: RequestInit; resolve: (response: Response) => void; reject: (error: unknown) => void };
const checks: Request[] = [];
const traffic: { path: string; init: RequestInit }[] = [];
let root: Root;
let host: HTMLDivElement;
let ignoreAbort = false;
let currentTodo: Todo;
const previous = { ...getState() };
const todo = (patch: Partial<Todo> = {}): Todo => ({
  id: "task-example", title: "Synthetic task", origin: "conversation-example", stage: "review",
  created_at: "2026-11-01T08:00:00Z", updated_at: "2026-11-01T09:00:00Z", ...patch,
});
const section = () => host.querySelector<HTMLElement>('[data-testid="task-completion-check"]')!;
const button = () => section().querySelector("button")!;
const result = () => host.querySelector('[data-testid="completion-check-result"]');
const flush = async () => { await act(async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); }); };
async function render(value = currentTodo) {
  currentTodo = value;
  Object.assign(getState(), { todos: [value], todosLoaded: true, todosError: null, sessions: {}, sessionSummaries: [], machines: [], interactions: [], features: [] });
  await act(async () => root.render(<StrictMode><TodosView selectedId={value.id} /></StrictMode>));
  await flush();
}
async function click() { await act(async () => button().click()); await flush(); }
async function answer(body: unknown, status = 200, index = checks.length - 1) {
  await act(async () => checks[index].resolve(Response.json(body, { status })));
  await flush();
}
const completed = { verdict: "done", reason: "Synthetic work is ready.", would_close: true, skip: "", replies: 1, signals: { merged_pr: true } };

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear();
  setToken("synthetic-token-a");
  resetManagerChoice();
  setRosterDefault("assistant");
  handleUnauthorized(() => {});
  await initI18n("en");
  checks.length = 0; traffic.length = 0; ignoreAbort = false; currentTodo = todo();
  vi.stubGlobal("fetch", vi.fn((input: unknown, init: RequestInit = {}) => {
    const path = String(input).replace(/^.*\/api\/v1/, "");
    traffic.push({ path, init });
    if (path.endsWith("/done-check")) return new Promise<Response>((resolve, reject) => {
      checks.push({ path, init, resolve, reject });
      if (!ignoreAbort) {
        const aborted = () => reject(new DOMException("Synthetic request aborted", "AbortError"));
        if (init.signal?.aborted) aborted();
        else init.signal?.addEventListener("abort", aborted, { once: true });
      }
    });
    return Promise.resolve(Response.json(path === "/todos" ? { todos: [currentTodo] } : { decisions: [] }));
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove(); Object.assign(getState(), previous);
  handleUnauthorized(() => {}); vi.useRealTimers(); vi.unstubAllGlobals();
});

describe("task detail completion check with the real REST client", () => {
  it("requires an explicit click, admits one synchronous POST and never changes task or agents", async () => {
    await render();
    expect(checks).toHaveLength(0);
    await act(async () => { button().click(); button().click(); button().click(); });
    expect(checks).toHaveLength(1);
    expect(checks[0].init.method).toBe("POST");
    expect(checks[0].path).toBe("/todos/task-example/done-check");
    expect(new Headers(checks[0].init.headers).get("Authorization")).toBe("Bearer synthetic-token-a");
    expect(button().disabled).toBe(true);
    expect(section().textContent).toContain("minute and a half");
    await answer(completed);
    expect(result()?.textContent).toContain("Work appears complete");
    expect(result()?.textContent).toContain("has not marked the task complete");
    expect(result()?.querySelector("time")?.dateTime).toMatch(/^20/);
    expect(section().textContent).not.toMatch(/merged_pr|signals|would_close/);
    expect(getState().todos[0].done_at).toBeUndefined();
    expect(traffic.filter(({ init }) => init.method && init.method !== "GET")).toHaveLength(1);
    await click(); expect(checks).toHaveLength(2);
    expect(result()).toBeNull();
    await answer({ verdict: "not_done", reason: "Tests remain.", would_close: false });
    expect(result()?.textContent).toContain("Tests remain.");
  });

  it.each([
    ["done", "Work appears complete"], ["not_done", "Work is unfinished"], ["waiting", "Waiting on something"],
    ["needs_user", "Your decision is needed"], ["unsure", "Not enough evidence"], ["undelivered", "Work has not been delivered"],
    ["future_verdict", "No clear assessment"], ["__proto__", "No clear assessment"],
  ])("renders %s as an assessment without changing the task", async (verdict, label) => {
    await render(); await click(); await answer({ ...completed, verdict, reason: "Synthetic explanation." });
    expect(result()?.textContent).toContain(label);
    expect(result()?.textContent).toContain("Synthetic explanation.");
    expect([...host.querySelectorAll("button")].some(x => x.textContent?.trim() === t("todos.complete"))).toBe(true);
    if (verdict !== "done") expect(result()?.textContent).not.toContain("work qualifies");
  });

  it.each([
    ["", "qualifies for automatic completion"], ["not_a_task", "manually created to-dos"],
    ["done", "already complete"], ["reopened", "new agent work after reopening"],
    ["future_skip", "unrecognized exclusion"], ["constructor", "unrecognized exclusion"],
  ])("explains skip %s independently of the verdict", async (skip, expected) => {
    await render(todo({ reopened_at: skip === "reopened" ? "2026-11-02T08:00:00Z" : undefined }));
    await click(); await answer({ ...completed, skip });
    expect(result()?.textContent).toContain(expected);
    expect(getState().todos[0].done_at).toBeUndefined();
  });

  it.each([
    ["live", "Live and tested"], ["none", "No deployment involved"], ["pr", "Stopped at a PR"],
    ["merged", "Merged, not live"], ["off", "Live, off by default"], ["future_state", "Not established"], ["__proto__", "Not established"],
  ])("shows diagnostic delivery %s and its blocker", async (state, label) => {
    await render(); await click(); await answer({ ...completed, delivery: { state, gate: "Synthetic review gate", unblock: "other" } });
    expect(result()?.textContent).toContain(`Delivery: ${label}`);
    expect(result()?.textContent).toContain("Blocked on: Synthetic review gate");
    expect(result()?.textContent).toContain("waiting on others");
  });

  it.each([["user", "needs your OK"], ["agent", "the agent can go on"], ["future_owner", "not established"], ["toString", "not established"]])("explains blocker owner %s", async (unblock, text) => {
    await render(); await click(); await answer({ delivery: { state: "pr", unblock } });
    expect(result()?.textContent).toContain(text);
  });

  it.each([{}, null, [], "", { verdict: 12, reason: {}, delivery: [] }, { delivery: {}, would_close: "true" }])("tolerates empty or malformed success %j without completion claims", async (body) => {
    await render(); await click(); await answer(body);
    expect(result()?.textContent).toContain("No clear assessment");
    expect(result()?.textContent).toContain("no explanation");
    expect(result()?.textContent).not.toContain("qualifies for automatic completion");
  });

  it("keeps supervised review, stage and ordinary controls separate from a done assessment", async () => {
    await render(todo({ supervision: { supervisor: "conversation-example", state: "pending_review", done_when: ["Synthetic criterion"] } }));
    const before = host.querySelector('[data-testid="review-checklist"]')?.textContent;
    const stage = [...host.querySelectorAll('[data-stage]')].map(x => [x.getAttribute('data-stage'), x.getAttribute('data-state')]);
    expect(stage).toHaveLength(6);
    await click(); await answer(completed);
    expect(section().textContent).toContain("normal supervision and review");
    expect(host.querySelector('[data-testid="review-checklist"]')?.textContent).toBe(before);
    expect([...host.querySelectorAll('[data-stage]')].map(x => [x.getAttribute('data-stage'), x.getAttribute('data-state')])).toEqual(stage);
    expect(host.textContent).toContain("Hand to agent");
    expect(getState().todos[0].supervision?.state).toBe("pending_review");
  });

  it("omits the diagnostic on a manual to-do while keeping handover and completion working", async () => {
    await render(todo({ origin: "" }));
    expect(section()).toBeNull();
    const handover = [...host.querySelectorAll("button")].find(x => x.textContent?.trim() === "Hand to agent")!;
    await act(async () => handover.click()); await flush();
    expect(document.querySelector('[role="dialog"]')).toBeTruthy();
    expect(checks).toHaveLength(0);
  });

  it("keeps completed-task reopening a deliberate ordinary action", async () => {
    await render(todo({ done_at: "2026-11-02T08:00:00Z" }));
    await click(); await answer({ ...completed, skip: "done", would_close: false });
    expect(result()?.textContent).toContain("already complete");
    expect(traffic.filter(x => x.init.method === "PATCH")).toHaveLength(0);
    const reopen = [...host.querySelectorAll("button")].find(x => x.textContent?.trim() === "Reopen")!;
    await act(async () => reopen.click()); await flush();
    expect(traffic.some(x => x.path === "/todos/task-example" && x.init.method === "PATCH" && x.init.body === '{"done":false}')).toBe(true);
  });

  it("waits beyond the GET default, times out past the hub limit, and retries only on click", async () => {
    vi.useFakeTimers(); await render(); await click();
    await act(async () => { await vi.advanceTimersByTimeAsync(90_000); });
    expect(button().disabled).toBe(true); expect(checks).toHaveLength(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(pastHub(90_000) - 90_000); }); await flush();
    expect(checks[0].init.signal?.aborted).toBe(true);
    expect(section().textContent).toContain("took too long");
    expect(button().disabled).toBe(false);
    await act(async () => { await vi.advanceTimersByTimeAsync(180_000); }); expect(checks).toHaveLength(1);
    await click(); await answer(completed); expect(result()).toBeTruthy();
  });

  it("accepts a slow answer after ninety seconds before the client deadline", async () => {
    vi.useFakeTimers(); await render(); await click();
    await act(async () => { await vi.advanceTimersByTimeAsync(92_000); });
    expect(button().disabled).toBe(true);
    await answer(completed);
    expect(result()?.textContent).toContain("Work appears complete");
    expect(button().disabled).toBe(false);
  });

  it("keeps the ordinary complete action independent of a negative assessment", async () => {
    await render(); await click(); await answer({ verdict: "not_done", reason: "Synthetic remaining work." });
    const complete = [...host.querySelectorAll("button")].find(x => x.textContent?.trim() === t("todos.complete"))!;
    await act(async () => complete.click()); await flush();
    expect(traffic.some(x => x.path === "/todos/task-example" && x.init.method === "PATCH" && x.init.body === '{"done":true}')).toBe(true);
    expect(checks).toHaveLength(1);
  });

  it("labels an assessment with its check time while task evidence changes during the request", async () => {
    await render(); await click();
    await render(todo({ title: "Updated synthetic task", notes: "New evidence during the check.", updated_at: "2026-11-03T08:00:00Z" }));
    await answer(completed);
    expect(result()?.querySelector("time")).toBeTruthy();
    expect(section().textContent).toContain("evidence may change");
    expect(getState().todos[0].title).toBe("Updated synthetic task");
    expect(getState().todos[0].done_at).toBeUndefined();
  });

  it.each([
    [403, { error: "read-only role cannot judge todos" }, "full access"],
    [404, { code: "not_found", error: "Not Found" }, "does not support completion checks"],
    [404, { error: "todo not found" }, "task could not be found"],
    [404, { code: "not_found", error: "Synthetic task does not exist" }, "task could not be found"],
    [502, { error: "Synthetic model failure" }, "judgment service"],
    [503, { error: "Synthetic service failure" }, "task service is unavailable"],
    [500, { error: "Synthetic error" }, "Could not check completion"],
  ])("shows useful HTTP %i failure and supports deliberate retry", async (status, body, expected) => {
    await render(); await click(); await answer(body, status);
    expect(section().querySelector('[role="alert"]')?.textContent).toContain(expected);
    expect(result()).toBeNull(); expect(button().disabled).toBe(false);
    await click(); await answer(completed); expect(result()?.textContent).toContain("Work appears complete");
  });

  it("recognizes a non-JSON route 404 and network loss without exposing server/debug details", async () => {
    await render(); await click();
    await act(async () => checks[0].resolve(new Response("404 page not found", { status: 404 }))); await flush();
    expect(section().textContent).toContain("does not support completion checks");
    await click(); await act(async () => checks[1].reject(new TypeError("Synthetic network loss"))); await flush();
    expect(section().textContent).toContain("Check your connection");
    expect(section().textContent).not.toContain("Synthetic network loss");
  });

  it.each(["success", "error"])("retires a previous task's late %s and finally while a new task is busy", async outcome => {
    ignoreAbort = true; await render(); await click();
    await render(todo({ id: "task-other", title: "Another synthetic task" }));
    expect(checks[0].init.signal?.aborted).toBe(true); expect(result()).toBeNull();
    await click();
    await answer(outcome === "success" ? completed : { error: "Old failure" }, outcome === "success" ? 200 : 502, 0);
    expect(result()).toBeNull(); expect(section().querySelector('[role="alert"]')).toBeNull(); expect(button().disabled).toBe(true);
    await answer({ verdict: "waiting", reason: "Current task waiting." }, 200, 1);
    expect(result()?.textContent).toContain("Current task waiting.");
  });

  it.each(["success", "error"])("retires a previous manager's late %s with the same task id", async outcome => {
    ignoreAbort = true; await render(todo({ manager: "example-manager-a" })); await click();
    expect(new Headers(checks[0].init.headers).get("X-Persona-Manager")).toBe("example-manager-a");
    await render(todo({ manager: "example-manager-b" })); await click();
    expect(checks[0].init.signal?.aborted).toBe(true);
    expect(new Headers(checks[1].init.headers).get("X-Persona-Manager")).toBe("example-manager-b");
    await answer(outcome === "success" ? completed : { error: "Old failure" }, outcome === "success" ? 200 : 503, 0);
    expect(button().disabled).toBe(true); expect(result()).toBeNull(); expect(section().querySelector('[role="alert"]')).toBeNull();
    await answer({ verdict: "unsure", reason: "Current manager." }, 200, 1);
    expect(result()?.textContent).toContain("Current manager.");
  });

  it("retires an implicit default manager and bounds the new manager request", async () => {
    ignoreAbort = true; await render(); await click();
    await act(async () => setRosterDefault("example-new-default")); await flush();
    expect(checks[0].init.signal?.aborted).toBe(true); expect(button().disabled).toBe(false);
    await click(); await answer(completed, 200, 0); expect(result()).toBeNull(); expect(button().disabled).toBe(true);
    await answer({ verdict: "waiting" }, 200, 1); expect(button().disabled).toBe(false);
  });

  it("keeps the request usable when the roster confirms the same default manager", async () => {
    resetManagerChoice(); await render(); await click();
    await act(async () => setRosterDefault("assistant")); await flush();
    expect(checks[0].init.signal?.aborted).toBe(false);
    await answer(completed); expect(result()).toBeTruthy();
    await click(); expect(checks).toHaveLength(2);
  });

  it.each(["success", "error", "401"])("fences an old login's late %s, including finally and unauthorized handling", async outcome => {
    const unauthorized = vi.fn(); handleUnauthorized(unauthorized);
    ignoreAbort = true; await render(); await click();
    await act(async () => setToken("synthetic-token-b")); await flush();
    expect(checks[0].init.signal?.aborted).toBe(true); expect(button().disabled).toBe(false); expect(result()).toBeNull();
    await click(); expect(new Headers(checks[1].init.headers).get("Authorization")).toBe("Bearer synthetic-token-b");
    await answer(outcome === "success" ? completed : { error: "Old credential error" }, outcome === "success" ? 200 : outcome === "401" ? 401 : 502, 0);
    expect(unauthorized).not.toHaveBeenCalled(); expect(button().disabled).toBe(true); expect(result()).toBeNull();
    expect(section().querySelector('[role="alert"]')).toBeNull();
    await answer({ verdict: "needs_user", reason: "Current login." }, 200, 1);
    expect(result()?.textContent).toContain("Current login.");
  });

  it("clears completed results on cross-tab credential replacement and same-token re-pairing", async () => {
    await render(); await click(); await answer(completed);
    await act(async () => {
      localStorage.setItem("persona.token", "synthetic-token-b"); window.dispatchEvent(new StorageEvent("storage", { key: "persona.token" }));
    }); await flush(); expect(result()).toBeNull();
    await click(); await answer(completed);
    await act(async () => { setToken(""); setToken("synthetic-token-b"); }); await flush();
    expect(result()).toBeNull(); expect(button().disabled).toBe(false);
  });

  it("aborts a pending check on a cross-tab login replacement and fences its late 401", async () => {
    const unauthorized = vi.fn(); handleUnauthorized(unauthorized);
    ignoreAbort = true; await render(); await click();
    await act(async () => {
      localStorage.setItem("persona.token", "synthetic-token-b"); window.dispatchEvent(new StorageEvent("storage", { key: "persona.token" }));
    }); await flush();
    expect(checks[0].init.signal?.aborted).toBe(true);
    await click(); await answer({ error: "invalid token" }, 401, 0);
    expect(unauthorized).not.toHaveBeenCalled(); expect(button().disabled).toBe(true);
    await answer(completed, 200, 1); expect(result()).toBeTruthy();
  });

  it("uses the existing current-owner unauthorized callback without a token bypass", async () => {
    const unauthorized = vi.fn(); handleUnauthorized(unauthorized);
    await render(); await click(); await answer({ error: "invalid token" }, 401);
    expect(unauthorized).toHaveBeenCalledTimes(1);
    expect(section().textContent).toContain("Sign in again"); expect(result()).toBeNull();
  });

  it("aborts an admitted request when the detail unmounts", async () => {
    await render(); await click();
    await act(async () => root.render(<div />)); await flush();
    expect(checks[0].init.signal?.aborted).toBe(true);
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it.each(["en", "zh-Hans", "ja"] as const)("localizes check, assessment and failures in %s", async lang => {
    await initI18n(lang); await render(); expect(button().textContent).toContain(t("todos.check_action"));
    await click(); await answer({ verdict: "needs_user", reason: "Synthetic explanation." });
    expect(result()?.textContent).toContain(t("todos.check_needs_user"));
    await click(); await answer({ error: "Synthetic service unavailable" }, 503);
    expect(section().textContent).toContain(t("todos.check_unavailable"));
  });
});
