// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { handleUnauthorized, setToken } from "../../lib/api";
import { chooseManager, MANAGER_HEADER, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { AllowedComputerApps } from "./AllowedComputerApps";
import { ComputerSection } from "./ComputerSection";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type App = { bundle_id: string; name: string; at?: string };
type Request = {
  path: string;
  method: string;
  headers: Headers;
  signal?: AbortSignal | null;
  answer: (body: unknown, status?: number, proof?: string | null) => void;
  reject: (error: Error) => void;
};
const apps: App[] = [
  { name: "Example Editor", bundle_id: "org.example.editor", at: "2026-01-02T12:34:00Z" },
  { name: "Design / Notes + Draft", bundle_id: "name:design / notes + draft%?#" },
  { name: "Whole screen", bundle_id: "screen" },
];
let requests: Request[], root: Root, host: HTMLDivElement;
const unauthorized = vi.fn();
const flush = async () => {
  await act(async () => {
    for (let i = 0; i < 8; i++) await Promise.resolve();
  });
};
const buttons = (label: string, scope: ParentNode = document) =>
  [...scope.querySelectorAll<HTMLButtonElement>("button")].filter((button) => button.textContent === label);
const button = (label: string, scope: ParentNode = document) => {
  const found = buttons(label, scope);
  expect(found).toHaveLength(1);
  return found[0]!;
};
const click = (label: string, scope: ParentNode = document) => act(() => button(label, scope).click());
const list = () => host.querySelector('[data-testid="allowed-computer-apps"]')!;
const dialog = () => document.querySelector('[role="dialog"]')!;
const take = (method = "GET") => {
  const index = requests.findIndex((request) => request.method === method);
  expect(index).toBeGreaterThanOrEqual(0);
  return requests.splice(index, 1)[0]!;
};
const render = (mid = "example-machine", manager?: string) => act(() => root.render(<AllowedComputerApps mid={mid} manager={manager} />));
const load = async () => {
  render();
  take().answer({ apps });
  await flush();
};
const choose = (index = 0) => act(() => buttons("Revoke", list())[index]!.click());

beforeEach(async () => {
  await initI18n("en");
  resetManagerChoice();
  setRosterDefault("example-manager");
  setToken("synthetic-token-a");
  requests = [];
  unauthorized.mockReset();
  handleUnauthorized(unauthorized);
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal(
    "fetch",
    vi.fn(
      (input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((resolve, reject) => {
          requests.push({
            path: String(input),
            method: init?.method ?? "GET",
            headers: new Headers(init?.headers),
            signal: init?.signal,
            answer: (body, status = 200, proof = new Headers(init?.headers).get(MANAGER_HEADER)) => {
              // The real hub owns this acknowledgement; integration controls also exercise a legacy receiver.
              const data =
                body && typeof body === "object" && "apps" in body
                  ? { manager: new Headers(init?.headers).get(MANAGER_HEADER), ...body }
                  : body;
              const headers = new Headers({ "Content-Type": "application/json" });
              if (proof) headers.set("X-Persona-Computer-Apps-Owner", proof);
              resolve(new Response(JSON.stringify(data), { status, headers }));
            },
            reject,
          });
        }),
    ),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  handleUnauthorized(() => {});
  vi.unstubAllGlobals();
});

it.each([
  { enabled: false, online: true },
  { enabled: false, online: false },
  { enabled: true, online: false },
])("loads and revokes persisted approvals with enabled=$enabled, online=$online", async ({ enabled, online }) => {
  act(() => root.render(<ComputerSection mid="example-machine" computer={{ enabled }} online={online} manager="example-owner" />));
  expect(list().textContent).toContain("Loading");
  const read = take();
  expect(read.path).toBe("/api/v1/machines/example-machine/computer/apps?expected_manager=example-owner");
  expect(read.headers.get(MANAGER_HEADER)).toBe("example-owner");
  expect(requests).toHaveLength(0);
  read.answer({ apps });
  await flush();
  expect(list().textContent).toContain("Example Editor");
  expect(list().textContent).toContain("org.example.editor");
  expect(list().querySelector("time")?.dateTime).toBe(apps[0]!.at);
  choose();
  click("Revoke", dialog());
  take("DELETE").answer({ apps: apps.slice(1) });
  await flush();
  expect(list().textContent).not.toContain("org.example.editor");
  expect(list().textContent).toContain("Persona will ask again");
  expect(requests).toHaveLength(0);
});

it.each(apps)("confirms and encodes the exact returned identifier $bundle_id", async (app) => {
  render("example machine/one");
  take().answer({ apps: [app] });
  await flush();
  choose();
  expect(dialog().textContent).toContain(app.name);
  expect(dialog().textContent).toContain(app.bundle_id);
  expect(requests).toHaveLength(0);
  click("Cancel", dialog());
  expect(document.querySelector('[role="dialog"]')).toBeNull();
  expect(requests).toHaveLength(0);
  choose();
  click("Revoke", dialog());
  const write = take("DELETE");
  expect(write.path).toBe(
    `/api/v1/machines/example%20machine%2Fone/computer/apps/revoke/${encodeURIComponent(app.bundle_id)}?expected_manager=example-manager`,
  );
  expect(write.headers.get("Authorization")).toBe("Bearer synthetic-token-a");
  expect(write.headers.get(MANAGER_HEADER)).toBe("example-manager");
  write.answer({ apps: [] });
  await flush();
  expect(list().textContent).toContain("No remembered app approvals");
});

it.each([403, 404, 500, 0])("explains list failure %s and retries without claiming empty", async (status) => {
  render();
  const read = take();
  if (status) read.answer({ error: "Synthetic read failure" }, status);
  else read.reject(new TypeError("Synthetic network failure"));
  await flush();
  expect(list().querySelector('[role="alert"]')).not.toBeNull();
  expect(list().textContent).not.toContain("No remembered");
  if (status === 403) expect(list().textContent).toContain("do not have permission");
  if (status === 404) expect(list().textContent).toContain("does not support");
  click("Retry", list());
  take().answer({ apps: [] });
  await flush();
  expect(list().querySelector('[role="alert"]')).toBeNull();
  expect(list().textContent).toContain("No remembered");
});

it.each([403, 404, 500, 0, 200])(
  "retains the row on failed or malformed revoke %s, then retries authoritative replacement",
  async (status) => {
    await load();
    choose();
    click("Revoke", dialog());
    expect(list().textContent).toContain("org.example.editor");
    const write = take("DELETE");
    if (status) write.answer(status === 200 ? {} : { error: "Synthetic write failure" }, status);
    else write.reject(new TypeError("Synthetic network failure"));
    await flush();
    expect(list().textContent).toContain("org.example.editor");
    expect(list().textContent).not.toContain("Saved approval removed");
    expect(dialog().querySelector('[role="alert"]')).not.toBeNull();
    click("Retry", dialog());
    const replacement = { name: "Another app", bundle_id: "org.example.another" };
    take("DELETE").answer({ apps: [replacement] });
    await flush();
    expect(list().textContent).toContain(replacement.bundle_id);
    expect(list().textContent).not.toContain("org.example.editor");
  },
);

it("retires displayed approval authority during refresh, so its read cannot race a revoke", async () => {
  await load();
  const oldRevoke = buttons("Revoke", list())[0]!;
  click("Refresh", list());
  const read = take();
  expect(oldRevoke.disabled).toBe(true);
  act(() => oldRevoke.click());
  expect(requests).toHaveLength(0);
  read.answer({ apps });
  await flush();
  choose();
  click("Revoke", dialog());
  take("DELETE").answer({ apps: apps.slice(1) });
  await flush();
  expect(list().textContent).not.toContain("org.example.editor");
});

it.each(["machine", "manager", "credential"])("ignores a stale GET during the new %s owner's revoke", async (change) => {
  render();
  const old = take();
  act(() => {
    if (change === "machine") render("example-next-machine");
    else if (change === "manager") setRosterDefault("example-next-manager");
    else setToken("synthetic-token-b");
  });
  expect(old.signal?.aborted).toBe(true);
  take().answer({ apps: [apps[2]] });
  await flush();
  choose();
  click("Revoke", dialog());
  const write = take("DELETE");
  old.answer({ apps });
  await flush();
  expect(list().textContent).not.toContain("org.example.editor");
  expect(button("Cancel", dialog()).disabled).toBe(true);
  write.answer({ apps: [] });
  await flush();
  expect(list().textContent).toContain("No remembered");
});

it("admits one confirmation/write synchronously and cancellation retires the old confirm callback", async () => {
  await load();
  const open = buttons("Revoke", list())[0]!;
  act(() => {
    open.click();
    open.click();
  });
  const confirm = button("Revoke", dialog());
  act(() => {
    button("Cancel", dialog()).click();
    confirm.click();
  });
  expect(requests).toHaveLength(0);
  choose();
  const submit = button("Revoke", dialog());
  act(() => {
    submit.click();
    submit.click();
  });
  expect(requests.filter((request) => request.method === "DELETE")).toHaveLength(1);
  expect(button("Cancel", dialog()).disabled).toBe(true);
  take("DELETE").answer({ apps: [] });
  await flush();
  expect(document.querySelector('[role="dialog"]')).toBeNull();
});

it.each(["machine", "explicit-manager", "default-manager", "credential", "storage", "same-token"])(
  "retires old confirmations, writes and finalizers on %s change",
  async (change) => {
    const explicit = change === "explicit-manager";
    render("example-machine", explicit ? "example-owner-a" : undefined);
    take().answer({ apps });
    await flush();
    choose();
    const oldConfirm = button("Revoke", dialog());
    click("Revoke", dialog());
    const staleWrite = take("DELETE");
    act(() => {
      if (change === "machine") render("example-next-machine");
      else if (explicit) render("example-machine", "example-owner-b");
      else if (change === "default-manager") setRosterDefault("example-next-manager");
      else if (change === "storage") {
        localStorage.setItem("persona.token", "synthetic-token-b");
        window.dispatchEvent(new StorageEvent("storage", { key: "persona.token" }));
      } else {
        setToken("synthetic-token-b");
        if (change === "same-token") setToken("synthetic-token-a");
      }
    });
    expect(staleWrite.signal?.aborted).toBe(true);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    const freshRead = take();
    expect(freshRead.headers.get("Authorization")).toBe(
      change === "credential" || change === "storage" ? "Bearer synthetic-token-b" : "Bearer synthetic-token-a",
    );
    expect(freshRead.headers.get(MANAGER_HEADER)).toBe(
      explicit ? "example-owner-b" : change === "default-manager" ? "example-next-manager" : "example-manager",
    );
    if (change === "machine") expect(freshRead.path).toContain("example-next-machine");
    freshRead.answer({ apps: [apps[2]] });
    await flush();
    choose();
    click("Revoke", dialog());
    const freshWrite = take("DELETE");
    staleWrite.answer({ error: "Retired synthetic failure" }, 500);
    act(() => oldConfirm.click());
    await flush();
    expect(dialog().textContent).not.toContain("Retired");
    expect(button("Cancel", dialog()).disabled).toBe(true);
    expect(list().textContent).not.toContain("org.example.editor");
    expect(requests).toHaveLength(0);
    freshWrite.answer({ apps: [] });
    await flush();
    expect(list().textContent).toContain("No remembered");
  },
);

it("isolates an old 401 while preserving current-owner unauthorized recovery", async () => {
  render();
  const old = take();
  act(() => setToken("synthetic-token-b"));
  const current = take();
  old.answer({ error: "Expired synthetic token" }, 401);
  await flush();
  expect(unauthorized).not.toHaveBeenCalled();
  current.answer({ apps });
  await flush();
  choose();
  click("Revoke", dialog());
  take("DELETE").answer({ error: "Expired synthetic token" }, 401);
  await flush();
  expect(unauthorized).toHaveBeenCalledOnce();
});

it("waits for the actual default and ignores the assistant-page choice", async () => {
  resetManagerChoice();
  render();
  expect(requests).toHaveLength(0);
  act(() => setRosterDefault("example-actual-default"));
  const read = take();
  expect(read.headers.get(MANAGER_HEADER)).toBe("example-actual-default");
  read.answer({ apps });
  await flush();
  act(() => chooseManager("example-chat-manager"));
  choose();
  click("Revoke", dialog());
  const write = take("DELETE");
  expect(write.headers.get(MANAGER_HEADER)).toBe("example-actual-default");
  write.answer({ apps: [] });
  await flush();
});

it("keeps only the current StrictMode read and aborts its own pending work on unmount", async () => {
  act(() =>
    root.render(
      <StrictMode>
        <AllowedComputerApps mid="example-machine" />
      </StrictMode>,
    ),
  );
  const old = take(),
    current = take();
  expect(old.signal?.aborted).toBe(true);
  current.answer({ apps: [apps[2]] });
  old.answer({ apps });
  await flush();
  expect(list().textContent).not.toContain("org.example.editor");
  choose();
  click("Revoke", dialog());
  const write = take("DELETE");
  act(() => root.render(null));
  expect(write.signal?.aborted).toBe(true);
  write.answer({ apps: [] });
  await flush();
  expect(host.textContent).toBe("");
});

it.each(["machine", "manager", "credential"])("retires an unsubmitted confirmation on %s change", async (change) => {
  await load();
  choose();
  const oldConfirm = button("Revoke", dialog());
  act(() => {
    if (change === "machine") render("example-next-machine");
    else if (change === "manager") setRosterDefault("example-next-manager");
    else setToken("synthetic-token-b");
  });
  const fresh = take();
  expect(document.querySelector('[role="dialog"]')).toBeNull();
  act(() => oldConfirm.click());
  expect(requests).toHaveLength(0);
  fresh.answer({ apps: [apps[2]] });
  await flush();
  choose();
  click("Revoke", dialog());
  take("DELETE").answer({ apps: [] });
  await flush();
  expect(list().textContent).toContain("No remembered");
});

it.each(["machine", "manager", "credential"])("drops a late successful revoke after %s change", async (change) => {
  await load();
  choose();
  click("Revoke", dialog());
  const oldWrite = take("DELETE");
  act(() => {
    if (change === "machine") render("example-next-machine");
    else if (change === "manager") setRosterDefault("example-next-manager");
    else setToken("synthetic-token-b");
  });
  take().answer({ apps: [apps[2]] });
  await flush();
  choose();
  click("Revoke", dialog());
  const newWrite = take("DELETE");
  oldWrite.answer({ apps: [] });
  await flush();
  expect(list().textContent).toContain("Whole screen");
  expect(list().textContent).not.toContain("Saved approval removed");
  expect(button("Cancel", dialog()).disabled).toBe(true);
  newWrite.answer({ apps: [] });
  await flush();
  expect(list().textContent).toContain("No remembered");
});

it("uses the authoritative list even when it retains the requested app", async () => {
  await load();
  choose();
  click("Revoke", dialog());
  take("DELETE").answer({ apps });
  await flush();
  expect(list().textContent).toContain("org.example.editor");
  expect(list().textContent).not.toContain("Saved approval removed");
});

it("reads and revokes a legacy receiver's list when the hub acknowledges owner enforcement", async () => {
  render();
  take().answer({ apps, manager: undefined });
  await flush();
  choose();
  click("Revoke", dialog());
  take("DELETE").answer({ apps: [], manager: undefined });
  await flush();
  expect(list().textContent).toContain("Saved approval removed");
});

it("keeps legacy reads visible but disables mutation without a hub acknowledgement", async () => {
  render();
  take().answer({ apps, manager: undefined }, 200, null);
  await flush();
  expect(list().textContent).toContain("org.example.editor");
  expect(list().textContent).toContain("update the hub");
  expect(buttons("Revoke", list()).every((row) => row.disabled)).toBe(true);
  click("Retry", list());
  take().answer({ apps });
  await flush();
  expect(buttons("Revoke", list())[0]!.disabled).toBe(false);
});

it.each(["failure", "missing-proof"])("retires previous mutation authority after a refresh %s", async (mode) => {
  await load();
  const oldRevoke = buttons("Revoke", list())[0]!;
  click("Refresh", list());
  const read = take();
  if (mode === "failure") read.answer({ error: "Synthetic refresh failure" }, 500);
  else read.answer({ apps }, 200, null);
  await flush();
  expect(list().textContent).toContain("org.example.editor");
  expect(buttons("Revoke", list()).every((row) => row.disabled)).toBe(true);
  act(() => oldRevoke.click());
  expect(requests).toHaveLength(0);
});

it("rejects a mismatched response owner before displaying its rows", async () => {
  render();
  take().answer({ apps, manager: "example-wrong-owner" });
  await flush();
  expect(list().querySelector("li")).toBeNull();
  expect(list().querySelector('[role="alert"]')).not.toBeNull();
});

it("retains approvals when the authoritative route rejects a stale owner before deletion", async () => {
  await load();
  choose();
  expect(dialog().textContent).toContain("If app approval is enabled");
  expect(dialog().textContent).toContain("does not change that setting");
  click("Revoke", dialog());
  const write = take("DELETE");
  expect(new URL(write.path, "http://example.test").searchParams.get("expected_manager")).toBe("example-manager");
  write.answer({ code: "machine_owner_changed", error: "Synthetic owner changed" }, 409);
  await flush();
  expect(list().textContent).toContain("org.example.editor");
  expect(list().textContent).not.toContain("Saved approval removed");
  expect(dialog().textContent).toContain("Refresh the machine page");
});
