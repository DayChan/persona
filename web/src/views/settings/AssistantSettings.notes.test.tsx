// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }, { id: "work", name: "Work" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({ useStore: () => [], refreshAssistant: vi.fn(() => Promise.resolve()) }));
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { handleUnauthorized, setToken } from "../../lib/api";
import { toast } from "../../ui/atoms";
import { AssistantSettings } from "./AssistantSettings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Write = { manager: string; token: string; notes: string; signal?: AbortSignal | null; succeed: () => void; fail: (status?: number) => void };
let root: Root;
let host: HTMLDivElement;
let writes: Write[];
let notes: Record<string, string | undefined>;
let fallback: string;
let unauthorized = vi.fn<() => void>();
const key = (manager = "assistant", token = "Bearer example-owner") => `${token}/${manager}`;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const box = () => host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${t("settings-assistantsettings.about_me")}"]`)!;
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t(label))!;
async function type(value: string) {
  act(() => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), value); box().dispatchEvent(new Event("input", { bubbles: true })); });
  await flush();
}
async function save() { act(() => button("common.save").click()); await flush(); }
async function render(strict = false) { await act(async () => root.render(strict ? <StrictMode><AssistantSettings /></StrictMode> : <AssistantSettings />)); await flush(); }
async function leave() { await act(async () => root.render(null)); await flush(); }
async function choose(manager: string) {
  act(() => { const select = host.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-assistantsettings.configuration_target")}"]`)!; select.value = manager; select.dispatchEvent(new Event("change", { bubbles: true })); });
  await flush();
}
async function complete(index: number, fail = false) { act(() => fail ? writes[index].fail() : writes[index].succeed()); await flush(); }
beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=assistant");
  resetManagerChoice(); setRosterDefault("assistant"); setToken("example-owner");
  notes = { [key()]: "Initial instructions", [key("work")]: "Work instructions" }; fallback = "Hub fallback instructions"; writes = [];
  vi.mocked(toast).mockClear();
  unauthorized = vi.fn<() => void>(); handleUnauthorized(unauthorized);
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  // Exercise the real API's dispatch-time credential lookup and explicit token override.
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    const headers = new Headers(init?.headers), manager = headers.get("X-Persona-Manager")!, token = headers.get("Authorization")!;
    if (path.endsWith("/assistant")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        return new Promise<Response>((resolve, reject) => {
          const signal = init.signal;
          const abort = () => reject(new DOMException("Aborted", "AbortError"));
          if (signal?.aborted) { abort(); return; }
          signal?.addEventListener("abort", abort, { once: true });
          const finish = (response: Response) => { signal?.removeEventListener("abort", abort); resolve(response); };
          writes.push({ manager, token, notes: body.notes, signal,
            succeed: () => { if (signal?.aborted) return; notes[key(manager, token)] = body.notes; finish(json({ manager, notes: body.notes })); },
            fail: (status = 500) => finish(json({ error: "Synthetic save failure" }, status)),
          });
        });
      }
      return Promise.resolve(json({ manager, notes: notes[key(manager, token)], notes_effective: notes[key(manager, token)] ?? fallback, default_permission_mode: "default" }));
    }
    if (path.endsWith("/tasks")) return Promise.resolve(json({ tasks: [] }));
    if (path.endsWith("/memory")) return Promise.resolve(json({ files: [] }));
    throw new Error(`Unexpected path: ${path}`);
  }));
});
afterEach(async () => {
  await leave();
  // Settle cleanup writes before retiring this test's credential and synthetic transport.
  for (let i = 0; i < writes.length; i++) await complete(i);
  act(() => root.unmount()); host.remove(); setToken(""); handleUnauthorized(() => {}); vi.unstubAllGlobals();
});

it("orders a pending manual save before the newer leave-page save at the authoritative store", async () => {
  await render(); await type("First saved instructions"); await save();
  expect(writes.map((w) => w.notes)).toEqual(["First saved instructions"]);
  expect(box().disabled).toBe(false);
  await type("Latest edited instructions"); await leave();
  // B cannot reach the hub until A settles, so even an adversarial hub cannot apply A last.
  expect(writes.map((w) => w.notes)).toEqual(["First saved instructions"]);
  await complete(0);
  expect(writes.map((w) => w.notes)).toEqual(["First saved instructions", "Latest edited instructions"]);
  await complete(1);
  expect(notes[key()]).toBe("Latest edited instructions");
  await render(); expect(box().value).toBe("Latest edited instructions");
});

it("keeps the latest queued draft across a same-owner remount and orders another leave after it", async () => {
  await render(); await type("Manual snapshot"); await save();
  await type("First page draft"); await leave();
  await render(); expect(box().value).toBe("First page draft");
  await type("Second page draft"); await leave();
  expect(writes).toHaveLength(1);
  await complete(0); expect(writes[1].notes).toBe("First page draft");
  await complete(1); expect(writes[2].notes).toBe("Second page draft");
  await complete(2); expect(notes[key()]).toBe("Second page draft");
  await render(); expect(box().value).toBe("Second page draft");
});

it("acknowledges only the saved snapshot while keeping newer edits dirty and saveable", async () => {
  await render(); await type("Saved snapshot"); await save(); await type("Still dirty");
  await complete(0);
  expect(box().value).toBe("Still dirty"); expect(button("common.save").disabled).toBe(false);
  expect(toast).toHaveBeenCalledWith(t("settings-assistantsettings.saved_new_chats"), "ok");
  await save(); await complete(1);
  expect(notes[key()]).toBe("Still dirty"); expect(button("common.save").disabled).toBe(true);
  await leave(); expect(writes).toHaveLength(2);
});

it("reverts later edits to the acknowledged snapshot", async () => {
  await render(); await type("Saved snapshot"); await save(); await type("Later edit"); await complete(0);
  act(() => button("settings-assistantsettings.revert").click()); await flush();
  expect(box().value).toBe("Saved snapshot"); expect(button("common.save").disabled).toBe(true);
});

it("keeps a rejected snapshot dirty, reports the error, and permits successful retry", async () => {
  await render(); await type("Retry instructions"); await save(); await complete(0, true);
  expect(notes[key()]).toBe("Initial instructions"); expect(box().value).toBe("Retry instructions");
  expect(button("common.save").disabled).toBe(false);
  expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic save failure"), "error");
  await save(); await complete(1);
  expect(notes[key()]).toBe("Retry instructions"); expect(button("common.save").disabled).toBe(true);
});

it("continues with the newer leave-page snapshot after the manual write rejects", async () => {
  await render(); await type("Rejected snapshot"); await save(); await type("Newer instructions"); await leave();
  expect(writes).toHaveLength(1); await complete(0, true);
  expect(writes[1].notes).toBe("Newer instructions"); await complete(1);
  expect(notes[key()]).toBe("Newer instructions");
  expect(toast).toHaveBeenCalledWith(t("settings-assistantsettings.about_saved"), "ok");
});

it.each([false, true])("deduplicates an unchanged pending snapshot on leave (StrictMode=%s)", async (strict) => {
  await render(strict); await type("One snapshot"); await save(); await leave();
  expect(writes).toHaveLength(1); await complete(0); expect(writes).toHaveLength(1);
  expect(notes[key()]).toBe("One snapshot");
});

it("leaves unchanged fallback notes alone, then saves an intentional empty override", async () => {
  notes[key()] = undefined;
  await render(); expect(box().value).toBe(fallback);
  expect(host.textContent).toContain(t("settings-assistantsettings.from_hub"));
  await leave(); expect(writes).toHaveLength(0);
  await render(); await type(""); await save(); await complete(0);
  expect(notes[key()]).toBe(""); expect(host.textContent).not.toContain(t("settings-assistantsettings.from_hub"));
  await leave(); await render(); expect(box().value).toBe(""); expect(button("common.save").disabled).toBe(true);
});

it("automatically saves an ordinary edited draft on leave", async () => {
  await render(); await type("Leave-page instructions"); await leave();
  expect(writes.map((w) => w.notes)).toEqual(["Leave-page instructions"]); await complete(0);
  expect(notes[key()]).toBe("Leave-page instructions");
  expect(toast).toHaveBeenCalledWith(t("settings-assistantsettings.about_saved"), "ok");
});

it("keeps manager queues independent and prevents retired errors from reaching another editor", async () => {
  await render(); await type("Personal saved snapshot"); await save(); await type("Personal final draft");
  await choose("work"); expect(box().value).toBe("Work instructions");
  await type("Work saved snapshot"); await save();
  expect(writes.map((w) => [w.manager, w.notes])).toEqual([["assistant", "Personal saved snapshot"], ["work", "Work saved snapshot"]]);
  await complete(0, true); expect(writes[2].manager).toBe("assistant");
  await complete(2, true); expect(box().value).toBe("Work saved snapshot");
  expect(toast).not.toHaveBeenCalled();
  await complete(1); expect(notes[key("work")]).toBe("Work saved snapshot");
});

it.each([false, true])("isolates a replacement credential from queued writes and late results (failure=%s)", async (fail) => {
  await render(); await type("Old saved snapshot"); await save(); await type("Old queued draft"); await leave();
  setToken("replacement-owner"); notes[key("assistant", "Bearer replacement-owner")] = "Replacement instructions";
  await render(); expect(box().value).toBe("Replacement instructions");
  await type("Replacement draft"); await save();
  expect(writes.map((w) => w.token)).toEqual(["Bearer example-owner", "Bearer replacement-owner"]);
  await complete(0, fail);
  expect(writes).toHaveLength(2); expect(box().value).toBe("Replacement draft"); expect(toast).not.toHaveBeenCalled();
  await complete(1); expect(notes[key("assistant", "Bearer replacement-owner")]).toBe("Replacement draft");
  expect(writes.every((w) => w.token !== "Bearer replacement-owner" || w.notes === "Replacement draft")).toBe(true);
});

it("does not save an old editor's dirty text with a token replaced before cleanup", async () => {
  await render(); await type("Old unsent draft"); act(() => setToken("replacement-owner")); await leave();
  expect(writes).toHaveLength(0);
});

it("fences queued work across logout followed by the same token", async () => {
  await render(); await type("Old saved snapshot"); await save(); await type("Old queued draft"); await leave();
  setToken(""); setToken("example-owner"); await render();
  expect(box().value).toBe("Initial instructions");
  await type("New login draft"); await save(); await complete(0);
  expect(writes).toHaveLength(2); expect(box().value).toBe("New login draft"); expect(toast).not.toHaveBeenCalled();
  await complete(1); expect(notes[key()]).toBe("New login draft");
});

it("restores text edited back to the original baseline while an older save is pending", async () => {
  await render(); await type("Pending change"); await save(); await type("Initial instructions"); await leave();
  expect(writes).toHaveLength(1); await complete(0);
  expect(writes[1].notes).toBe("Initial instructions"); await complete(1);
  expect(notes[key()]).toBe("Initial instructions");
});

it.each([false, true])("keeps a remounted editor's later draft dirty after inherited work settles (failure=%s)", async (fail) => {
  await render(); await type("Manual snapshot"); await save(); await type("Inherited draft"); await leave();
  await render(); expect(box().value).toBe("Inherited draft"); await type("New editor draft");
  await complete(0); await complete(1, fail);
  expect(box().value).toBe("New editor draft"); expect(button("common.save").disabled).toBe(false);
  expect(toast).not.toHaveBeenCalled();
  await save(); await complete(2); expect(notes[key()]).toBe("New editor draft");
});

it("reports failure of the unchanged inherited draft and allows retry after remount", async () => {
  await render(); await type("Manual snapshot"); await save(); await type("Inherited draft"); await leave();
  await render(); await complete(0); await complete(1, true);
  expect(box().value).toBe("Inherited draft"); expect(button("common.save").disabled).toBe(false);
  expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic save failure"), "error");
  await save(); await complete(2); expect(notes[key()]).toBe("Inherited draft");
});


it("reloads a surviving editor on a cross-tab credential replacement without transferring its draft", async () => {
  await render(); await type("Retired owner draft");
  const retiredBox = box();
  notes[key("assistant", "Bearer replacement-owner")] = "Replacement instructions";
  act(() => {
    localStorage.setItem("persona.token", "replacement-owner");
    window.dispatchEvent(new StorageEvent("storage", { key: "persona.token", oldValue: "example-owner", newValue: "replacement-owner", storageArea: localStorage }));
  });
  await flush();
  expect(box()).not.toBe(retiredBox); expect(box().value).toBe("Replacement instructions");
  expect(writes).toHaveLength(0);
  await type("Replacement draft"); await save();
  expect(writes.map((w) => [w.token, w.notes])).toEqual([["Bearer replacement-owner", "Replacement draft"]]);
  await complete(0, true); expect(button("common.save").disabled).toBe(false);
  await save(); await complete(1); expect(notes[key("assistant", "Bearer replacement-owner")]).toBe("Replacement draft");
});

it("preserves unauthorized recovery for a current credential's notes save", async () => {
  await render(); await type("Current owner draft"); await save();
  act(() => writes[0].fail(401)); await flush();
  expect(unauthorized).toHaveBeenCalledTimes(1);
  expect(button("common.save").disabled).toBe(false);
});

it("ignores an old credential's late 401 after a mounted replacement and saves the new draft", async () => {
  await render(); await type("Old pending snapshot"); await save(); await type("Old queued draft");
  notes[key("assistant", "Bearer replacement-owner")] = "Replacement instructions";
  act(() => setToken("replacement-owner")); await flush();
  expect(box().value).toBe("Replacement instructions");
  await type("Replacement draft"); await save();
  act(() => writes[0].fail(401)); await flush();
  expect(unauthorized).not.toHaveBeenCalled(); expect(toast).not.toHaveBeenCalled();
  expect(box().value).toBe("Replacement draft");
  expect(writes.map((w) => [w.token, w.notes])).toEqual([["Bearer example-owner", "Old pending snapshot"], ["Bearer replacement-owner", "Replacement draft"]]);
  await complete(1); expect(notes[key("assistant", "Bearer replacement-owner")]).toBe("Replacement draft");
});
