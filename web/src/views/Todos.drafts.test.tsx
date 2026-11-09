// @vitest-environment jsdom
// Exercise the mounted detail, shared refresh paths and REST client against a synthetic authoritative hub.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({
    matches: false, media, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});
const { getState, refresh, refreshTodos } = await import("../lib/store");
const { getToken, setToken } = await import("../lib/api");
const { TodosView } = await import("./Todos");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const fixture: Todo = {
  id: "todo-example", title: "Release checklist", notes: "Saved requirements",
  created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
};
type Field = "title" | "notes";
type Write = { path: string; body: Partial<Record<Field, string>> };
const before = { ...getState() };
const originalToken = getToken();
let root: Root, host: HTMLDivElement;
let server: Todo[];
let writes: Write[];
let readNext: (() => Promise<Response>) | undefined;
let patchNext: ((write: Write) => Promise<Response>) | undefined;
const json = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((yes) => { resolve = yes; });
  return { promise, resolve };
}
const field = (name: Field) => host.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[data-testid=todos-detail-column] ${name === "title" ? "input" : "textarea"}`)!;
async function edit(name: Field, value: string) {
  const element = field(name);
  const proto = name === "title" ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(element, value);
    element.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const blur = async (name: Field) => { await act(async () => field(name).dispatchEvent(new FocusEvent("focusout", { bubbles: true }))); };
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const reload = async (full = false) => { await act(async () => { await (full ? refresh() : refreshTodos()); }); };
const render = async (id = fixture.id) => { await act(async () => root.render(<TodosView selectedId={id} />)); };
function commit(write: Write) {
  const todo = server.find((todo) => write.path === `/todos/${todo.id}`)!;
  Object.assign(todo, write.body);
  return json({ todo: { ...todo } });
}
beforeEach(async () => {
  server = [{ ...fixture }, { ...fixture, id: "todo-other", title: "Review examples", notes: "Other requirements" }];
  writes = [];
  readNext = undefined;
  patchNext = undefined;
  setToken("synthetic-draft-owner-a");
  Object.assign(getState(), { todos: server.map((todo) => ({ ...todo })), todosLoaded: true, todosError: null, todosSupported: true, machines: [], sessions: {}, interactions: [], answered: {} });
  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit = {}) => {
    const path = String(url).replace("/api/v1", "");
    if (init.method === "PATCH") {
      const write = { path, body: JSON.parse(String(init.body)) };
      writes.push(write);
      const handler = patchNext;
      patchNext = undefined;
      return handler ? handler(write) : commit(write);
    }
    if (path === "/todos") {
      const handler = readNext;
      readNext = undefined;
      return handler ? handler() : json({ todos: server.map((todo) => ({ ...todo })) });
    }
    return json({});
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await render();
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  Object.assign(getState(), before);
  setToken(originalToken);
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

it.each([false, true])("retains both focused drafts on unchanged and status-only reads (full refresh: %s)", async (full) => {
  await edit("title", "Local checklist");
  await edit("notes", "Keep this requirement");
  field("notes").focus();
  const pending = deferred<Response>();
  readNext = () => pending.promise;
  let loading!: Promise<void>;
  await act(async () => { loading = full ? refresh() : refreshTodos(); });
  expect(field("notes").value).toBe("Keep this requirement");
  await act(async () => { pending.resolve(json({ todos: server.map((todo) => ({ ...todo })) })); await loading; });
  server[0].stage = "review";
  await reload(full);
  expect(field("title").value).toBe("Local checklist");
  expect(field("notes").value).toBe("Keep this requirement");
  expect(document.activeElement).toBe(field("notes"));
  expect(writes).toEqual([]);
});

it.each<Field>(["title", "notes"])("accepts remote changes only for clean fields while %s is dirty", async (dirty) => {
  const clean = dirty === "title" ? "notes" : "title";
  server[0].title = "Remote checklist";
  server[0].notes = "Remote requirements";
  await reload();
  expect(field("title").value).toBe(server[0].title);
  expect(field("notes").value).toBe(server[0].notes);
  await edit(dirty, "Local draft");
  server[0][dirty] = "Remote conflict";
  server[0][clean] = "Clean remote update";
  await reload();
  expect(field(dirty).value).toBe("Local draft");
  expect(field(clean).value).toBe("Clean remote update");
  await blur(dirty);
  expect(writes).toEqual([{ path: "/todos/todo-example", body: { [dirty]: "Local draft" } }]);
  expect(server[0][dirty]).toBe("Local draft");
  server[0][dirty] = "Later clean update";
  await reload();
  expect(field(dirty).value).toBe("Later clean update");
});

it.each<Field>(["title", "notes"])("preserves the other draft during %s PATCH and follow-up GET", async (saving) => {
  const other = saving === "title" ? "notes" : "title";
  const patch = deferred<Response>(), read = deferred<Response>();
  patchNext = () => patch.promise;
  readNext = () => read.promise;
  await edit(saving, "  Saved snapshot  ");
  await blur(saving);
  await edit(other, "Draft during PATCH");
  await act(async () => patch.resolve(commit(writes[0])));
  await flush();
  await edit(other, "Draft during GET");
  await act(async () => read.resolve(json({ todos: server.map((todo) => ({ ...todo })) })));
  await flush();
  expect(field(saving).value).toBe("Saved snapshot");
  expect(field(other).value).toBe("Draft during GET");
  expect(writes).toEqual([{ path: "/todos/todo-example", body: { [saving]: "Saved snapshot" } }]);
  await blur(other);
  expect(server[0][other]).toBe("Draft during GET");
});

it.each<Field>(["title", "notes"])("acknowledges only the %s snapshot and admits one in-flight save", async (name) => {
  const patch = deferred<Response>(), read = deferred<Response>();
  patchNext = () => patch.promise;
  readNext = () => read.promise;
  await edit(name, "First snapshot");
  await act(async () => {
    field(name).dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    field(name).dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
  });
  expect(writes).toHaveLength(1);
  expect(field(name).disabled).toBe(false);
  await edit(name, "Later local revision");
  await act(async () => patch.resolve(commit(writes[0])));
  await flush();
  await edit(name, "First snapshot"); // An edit away and back is still a later revision.
  server[0][name] = "Remote after snapshot";
  await act(async () => read.resolve(json({ todos: server.map((todo) => ({ ...todo })) })));
  await flush();
  expect(field(name).value).toBe("First snapshot");
  await blur(name);
  expect(writes).toHaveLength(2);
  expect(server[0][name]).toBe("First snapshot");
});

it.each<Field>(["title", "notes"])("retains failed %s edits through refresh and retries on blur", async (name) => {
  patchNext = async () => json({ error: "Temporary synthetic failure" }, 503);
  await edit(name, "Retry this edit");
  await blur(name);
  await flush();
  await reload();
  expect(field(name).value).toBe("Retry this edit");
  await blur(name);
  expect(writes).toHaveLength(2);
  expect(server[0][name]).toBe("Retry this edit");
  expect(field(name).getAttribute("aria-busy")).toBe("false");
});

it("clears blank notes, rejects a blank title and normalizes a valid title on blur or Enter", async () => {
  await edit("notes", " \n ");
  await blur("notes");
  expect(writes).toEqual([{ path: "/todos/todo-example", body: { notes: "" } }]);
  expect(field("notes").value).toBe("");
  expect(server[0].notes).toBe("");
  await edit("title", "   ");
  await blur("title");
  expect(field("title").value).toBe(fixture.title);
  expect(writes).toHaveLength(1);
  await edit("title", "  Trimmed checklist  ");
  field("title").focus();
  await act(async () => field("title").dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
  expect(server[0].title).toBe("Trimmed checklist");
  expect(field("title").value).toBe("Trimmed checklist");
});

it.each(["success", "failure"])("retires old callbacks on task switch after late %s", async (result) => {
  const patch = deferred<Response>();
  patchNext = () => patch.promise;
  await edit("notes", "Old task snapshot");
  await blur("notes");
  await render("todo-other");
  expect(field("title").value).toBe("Review examples");
  await edit("notes", "New task draft");
  const reads = vi.mocked(fetch).mock.calls.length;
  await act(async () => patch.resolve(result === "success" ? commit(writes[0]) : json({ error: "Old task failed" }, 503)));
  await flush();
  expect(field("notes").value).toBe("New task draft");
  expect(vi.mocked(fetch).mock.calls).toHaveLength(reads);
  await blur("notes");
  expect(writes[1]).toEqual({ path: "/todos/todo-other", body: { notes: "New task draft" } });
  await render();
  expect(field("notes").value).not.toBe("New task draft");
});

it("retires pending saves and resets drafts when a new owner reuses the same task id", async () => {
  const patch = deferred<Response>();
  patchNext = () => patch.promise;
  await edit("title", "Old owner snapshot");
  await blur("title");
  await edit("notes", "Old owner unsaved notes");
  await act(async () => setToken("synthetic-draft-owner-b"));
  server[0] = { ...fixture, title: "New owner checklist", notes: "New owner notes" };
  await reload();
  expect(field("title").value).toBe("New owner checklist");
  expect(field("notes").value).toBe("New owner notes");
  await edit("notes", "New owner draft");
  const reads = vi.mocked(fetch).mock.calls.length;
  await act(async () => patch.resolve(json({ todo: { ...fixture, title: "Old owner snapshot" } })));
  await flush();
  expect(vi.mocked(fetch).mock.calls).toHaveLength(reads);
  expect(field("notes").value).toBe("New owner draft");
  await blur("notes");
  expect(server[0].notes).toBe("New owner draft");
});

it.each<Field>(["title", "notes"])("saves a later %s blur after the admitted snapshot settles", async (name) => {
  const patch = deferred<Response>();
  patchNext = () => patch.promise;
  await edit(name, "First snapshot");
  await blur(name);
  await edit(name, "Second snapshot");
  await blur(name);
  expect(writes).toHaveLength(1);
  await act(async () => patch.resolve(commit(writes[0])));
  await flush();
  expect(writes).toEqual([
    { path: "/todos/todo-example", body: { [name]: "First snapshot" } },
    { path: "/todos/todo-example", body: { [name]: "Second snapshot" } },
  ]);
  expect(server[0][name]).toBe("Second snapshot");
  expect(field(name).value).toBe("Second snapshot");
});

it("keeps simultaneous field saves independent when one fails and is retried", async () => {
  const title = deferred<Response>(), notes = deferred<Response>();
  patchNext = () => title.promise;
  await edit("title", "Saved title");
  await blur("title");
  patchNext = () => notes.promise;
  await edit("notes", "Retry these notes");
  await blur("notes");
  await act(async () => notes.resolve(json({ error: "Temporary synthetic failure" }, 503)));
  await act(async () => title.resolve(commit(writes[0])));
  await flush();
  expect(field("title").value).toBe("Saved title");
  expect(field("notes").value).toBe("Retry these notes");
  await blur("notes");
  expect(server[0].notes).toBe("Retry these notes");
  expect(writes).toHaveLength(3);
});

it.each(["task", "owner"])("retires the old save while its follow-up GET is pending across a %s switch", async (scope) => {
  const read = deferred<Response>();
  readNext = () => read.promise;
  await edit("title", "Old saved title");
  await blur("title");
  await flush();
  const oldResponse = server.map((todo) => ({ ...todo }));
  if (scope === "task") {
    await render("todo-other");
  } else {
    await act(async () => setToken("synthetic-draft-owner-b"));
    server[0] = { ...fixture, title: "New owner title", notes: "New owner notes" };
    await reload();
  }
  await edit("notes", "New detail draft");
  await act(async () => read.resolve(json({ todos: oldResponse })));
  await flush();
  expect(field("notes").value).toBe("New detail draft");
  expect(field("title").value).toBe(scope === "task" ? "Review examples" : "New owner title");
  expect(field("notes").getAttribute("aria-busy")).toBe("false");
  await blur("notes");
  expect(writes[1]).toEqual({ path: scope === "task" ? "/todos/todo-other" : "/todos/todo-example", body: { notes: "New detail draft" } });
});

it("retains a later blurred revision after failure without automatically retrying it", async () => {
  const patch = deferred<Response>();
  patchNext = () => patch.promise;
  await edit("notes", "First snapshot");
  await blur("notes");
  await edit("notes", "Later revision");
  await blur("notes");
  await act(async () => patch.resolve(json({ error: "Temporary synthetic failure" }, 503)));
  await flush();
  expect(writes).toHaveLength(1);
  await reload();
  expect(field("notes").value).toBe("Later revision");
  await blur("notes");
  expect(server[0].notes).toBe("Later revision");
  expect(writes).toHaveLength(2);
});
