// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { getToken, handleUnauthorized, setToken } from "../../lib/api";
import type { KBDetail, KBDetailResponse, KBEntry } from "../../lib/kb";
import { setRosterDefault } from "../../lib/manager-choice";
import { EntryDetail } from "./KBDetail";
import backendFixtures from "./fixtures/relationships.json";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Pending = { url: string; init: RequestInit; resolve: (r: Response) => void };
let requests: Pending[], host: HTMLDivElement, root: Root;
const saved = vi.fn(), opened = vi.fn(), unauthorized = vi.fn();
const entry = (kind: string, id = kind === "recordings" ? "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" : kind): KBEntry => ({ kind, id, name: `Example ${kind}`, relevance: 0 });
const response = (e: KBEntry, meta: Record<string, unknown>, links: KBEntry[] = []): KBDetailResponse => ({
  detail: { kind: e.kind, id: e.id, path: "", meta: { ...(["docs", "meetings", "events"].includes(e.kind) ? { title: e.name } : { name: e.name }), tags: ["existing"], pinned: "Existing facts", ...meta }, summary: "Existing profile", links },
});
const render = (e: KBEntry, reloadKey = 0) => act(() => root.render(<StrictMode><EntryDetail kind={e.kind} id={e.id} reloadKey={reloadKey} onBack={() => {}} onToggleSidebar={() => {}} onOpen={opened} onSaved={saved} /></StrictMode>));
const reply = async (r: Pending, data: unknown, status = 200) => act(async () => r.resolve(new Response(JSON.stringify(data), { status })));
const buttons = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const click = (label: string) => act(() => { const b = buttons(label)[0]; expect(b, label).toBeDefined(); b.click(); });
const writes = () => requests.filter((r) => r.init.method === "PUT");
const reads = () => requests.filter((r) => r.init.method !== "PUT");
const dialog = () => document.querySelector('[role="dialog"]');
const project = entry("projects"), person = entry("people"), chat = entry("chats");
const mappings = [
  { a: project, b: person, af: "people", bf: "projects", delta: "remove_people", reverse: false },
  { a: project, b: chat, af: "chats", bf: "projects", delta: "remove_chats", reverse: false },
  { a: chat, b: person, af: "people", bf: "chats", delta: "remove_people", reverse: false },
  { a: person, b: chat, af: "chats", bf: "people", delta: "remove_chats", reverse: false },
  ...["people", "chats", "docs", "meetings", "events", "recordings"].map((kind) => ({
    a: entry(kind), b: project, af: "projects", bf: kind === "people" || kind === "chats" ? kind : "items", delta: "remove_projects", reverse: false,
  })),
  ...["docs", "meetings", "events", "recordings"].map((kind) => ({ a: project, b: entry(kind), af: "items", bf: "projects", delta: "remove_projects", reverse: true })),
];
const member = (e: KBEntry, field: string) => field === "items" ? `${e.kind}/${e.id}` : e.id;
async function load(a = project, b = person, af = "people") {
  render(a);
  await reply(requests.at(-1)!, response(a, { [af]: [member(b, af), "unrelated"] }, [b]));
}
async function verify(a = project, b = person, af = "people", bf = "projects", opts: { partial?: boolean; ignored?: boolean; fail?: boolean } = {}) {
  const pair = reads().slice(-2);
  expect(pair.map((r) => r.url).sort()).toEqual([`/api/v1/kb/${a.kind}/${a.id}`, `/api/v1/kb/${b.kind}/${b.id}`].sort());
  for (const r of pair) {
    const first = r.url === `/api/v1/kb/${a.kind}/${a.id}`;
    const e = first ? a : b, other = first ? b : a, field = first ? af : bf;
    const remains = opts.ignored || (!first && opts.partial);
    await reply(r, opts.fail && !first ? { error: "Synthetic read failure" } : response(e, {
      [field]: ["unrelated", ...(remains ? [member(other, field)] : [])], locked_fields: remains ? ["name"] : ["name", field],
    }, remains ? [other] : []), opts.fail && !first ? 503 : 200);
  }
}

beforeEach(async () => {
  await initI18n("en"); localStorage.clear(); setToken("synthetic-owner-a"); setRosterDefault("assistant");
  saved.mockClear(); opened.mockClear(); unauthorized.mockClear(); handleUnauthorized(unauthorized);
  requests = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init, resolve }))));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); host.remove(); handleUnauthorized(() => {}); vi.unstubAllGlobals(); });

it.each(mappings)("removes $a.kind → $b.kind through the real detail/API caller with only $delta", async ({ a, b, af, bf, delta, reverse }) => {
  await load(a, b, af);
  click("Remove relationship");
  expect(dialog()?.textContent).toContain(a.name);
  expect(dialog()?.textContent).toContain(b.name);
  expect(dialog()?.textContent).toContain("entire");
  expect(dialog()?.textContent).toContain("Both entries are kept");
  expect(writes()).toHaveLength(0);
  click("Remove");
  expect(writes()).toHaveLength(1);
  const put = writes()[0], target = reverse ? b : a;
  expect(put.url).toBe(`/api/v1/kb/${target.kind}/${target.id}`);
  expect(JSON.parse(put.init.body as string)).toEqual({ [delta]: [reverse ? a.id : b.id] });
  expect(new Headers(put.init.headers).get("Authorization")).toBe("Bearer synthetic-owner-a");
  expect(new Headers(put.init.headers).get("X-Persona-Manager")).toBe("assistant");
  await reply(put, { id: target.id });
  expect(saved).not.toHaveBeenCalled();
  await verify(a, b, af, bf);
  expect(saved).toHaveBeenCalledOnce();
  expect(dialog()).toBeNull();
  expect(host.textContent).toContain("Existing facts");
  expect(host.textContent).toContain("Existing profile");
  expect(host.textContent).toContain("#existing");
  expect(writes()).toHaveLength(1);
});

it.each([
  { a: project, b: person, meta: {} },
  { a: project, b: person, meta: { people: "people" } },
  { a: project, b: person, meta: { people: [person.id, 7] } },
  { a: project, b: person, meta: { people: ["someone-else"] } },
  { a: project, b: entry("projects", "other-project"), meta: { projects: ["other-project"] } },
  { a: entry("docs"), b: chat, meta: { where: chat.id, chats: [chat.id] } },
  { a: entry("meetings"), b: person, meta: { people: [person.id] } },
])("keeps derived, malformed or unsupported $a.kind → $b.kind links navigation-only", async ({ a, b, meta }) => {
  render(a); await reply(requests.at(-1)!, response(a, meta, [b]));
  expect(buttons("Remove relationship")).toHaveLength(0);
  const anchor = [...host.querySelectorAll("a")].find((a) => a.textContent?.includes(b.name))!;
  expect(anchor).toBeDefined();
  act(() => anchor.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 })));
  expect(opened).toHaveBeenCalledWith(b);
  expect(writes()).toHaveLength(0);
});

it("cancels before writing, reopens, and admits only one removal before React rerenders", async () => {
  await load(); click("Remove relationship"); click("Cancel");
  expect(dialog()).toBeNull(); expect(writes()).toHaveLength(0);
  click("Remove relationship");
  act(() => { const b = buttons("Remove")[0]; b.click(); b.click(); });
  expect(writes()).toHaveLength(1);
  expect(buttons("Close").at(-1)!.disabled).toBe(true);
  click("Close");
  expect(dialog()).not.toBeNull();
  await reply(writes()[0], {});
  expect(buttons("Close").at(-1)!.disabled).toBe(true);
  await verify();
  expect(saved).toHaveBeenCalledOnce();
});

it.each([false, true])("refreshes both ends after rejected writes and retries the original intent after the row disappears (read failure: %s)", async (fail) => {
  await load(); click("Remove relationship"); click("Remove");
  await reply(writes()[0], { error: "Synthetic partial failure" }, 503);
  await verify(project, person, "people", "projects", { partial: true, fail });
  expect(saved).not.toHaveBeenCalled();
  expect(host.textContent).not.toContain(person.name);
  expect(dialog()?.textContent).toContain(person.name);
  expect(dialog()?.textContent).toContain("Synthetic partial failure");
  if (fail) {
    expect(dialog()?.textContent).toContain("Some changes may have been saved");
    expect(buttons("Retry removal")).toHaveLength(0);
    click("Retry verification");
    expect(writes()).toHaveLength(1);
    await verify(project, person, "people", "projects", { partial: true });
  }
  click("Retry removal");
  expect(writes()).toHaveLength(2);
  expect(writes()[1].url).toBe(writes()[0].url);
  expect(writes()[1].init.body).toBe(writes()[0].init.body);
  await reply(writes()[1], {}); await verify();
  expect(saved).toHaveBeenCalledOnce();
});

it("retries reads only after an accepted mutation and failed readback", async () => {
  await load(); click("Remove relationship"); click("Remove");
  await reply(writes()[0], {}); await verify(project, person, "people", "projects", { fail: true });
  expect(dialog()?.textContent).toContain("The hub accepted the request");
  expect(dialog()?.textContent).toContain("Synthetic read failure");
  expect(buttons("Retry removal")).toHaveLength(0); expect(saved).not.toHaveBeenCalled();
  click("Retry verification");
  expect(writes()).toHaveLength(1); await verify();
  expect(saved).toHaveBeenCalledOnce(); expect(writes()).toHaveLength(1);
});

it.each(["ignored", "reverse-remains", "locks-missing", "record-missing", "wrong-entry"])("never reports success for an accepted but unconfirmed removal: %s", async (mode) => {
  await load(); click("Remove relationship"); click("Remove"); await reply(writes()[0], {});
  if (mode === "ignored" || mode === "reverse-remains") {
    await verify(project, person, "people", "projects", { ignored: mode === "ignored", partial: mode === "reverse-remains" });
  } else {
    const pair = reads().slice(-2);
    await reply(pair[0], response(project, { people: [], locked_fields: ["people"] }));
    await reply(pair[1], mode === "record-missing" ? { error: "not found" } : response(mode === "wrong-entry" ? entry("people", "wrong") : person, { projects: [] }), mode === "record-missing" ? 404 : 200);
  }
  expect(saved).not.toHaveBeenCalled(); expect(dialog()).not.toBeNull();
  expect(buttons("Retry removal")).toHaveLength(0);
  click("Retry verification"); expect(writes()).toHaveLength(1);
  await verify(); expect(saved).toHaveBeenCalledOnce();
});

it("fences older same-entry GETs after authoritative verification", async () => {
  await load(); render(project, 1); const earlier = reads().at(-1)!;
  click("Remove relationship"); click("Remove"); await reply(writes()[0], {}); await verify();
  await reply(earlier, response(project, { people: [person.id] }, [person]));
  expect(buttons("Remove relationship")).toHaveLength(0);
  expect(saved).toHaveBeenCalledOnce();
});

it.each(["route", "manager", "credential"].flatMap((change) => ["confirm", "write", "read"].map((phase) => ({ change, phase }))))("retires $phase callbacks on $change replacement without redirecting requests", async ({ change, phase }) => {
  await load(); click("Remove relationship");
  if (phase !== "confirm") click("Remove");
  if (phase === "read") await reply(writes()[0], {});
  const old = phase === "read" ? reads().slice(-2) : phase === "write" ? [writes()[0]] : [];
  const oldButton = buttons("Remove")[0];
  if (change === "route") render(chat);
  else act(() => change === "manager" ? setRosterDefault("example-manager") : setToken("synthetic-owner-b"));
  expect(dialog()).toBeNull();
  if (oldButton) act(() => oldButton.click());
  const count = requests.length;
  for (const r of old) await reply(r, r.init.method === "PUT" ? {} : response(r.url.endsWith("/people") ? person : project, { people: [], projects: [], locked_fields: ["people", "projects"] }));
  expect(requests).toHaveLength(count); expect(saved).not.toHaveBeenCalled();
  expect(writes()).toHaveLength(phase === "confirm" ? 0 : 1);
  for (const r of old) {
    expect(new Headers(r.init.headers).get("Authorization")).toBe("Bearer synthetic-owner-a");
    expect(new Headers(r.init.headers).get("X-Persona-Manager")).toBe("assistant");
  }
});

it("rejects a stale rendered action when storage changed before React was notified", async () => {
  await load(); click("Remove relationship");
  localStorage.setItem("persona.token", "synthetic-owner-b"); click("Remove");
  expect(writes()).toHaveLength(0);
});

it.each(["write", "read"])("uses normal sign-out on a current owner's %s 401", async (phase) => {
  await load(); handleUnauthorized(() => { unauthorized(); setToken(""); });
  click("Remove relationship"); click("Remove");
  if (phase === "read") await reply(writes()[0], {});
  await reply(phase === "write" ? writes()[0] : reads().at(-1)!, { error: "invalid token" }, 401);
  expect(unauthorized).toHaveBeenCalledOnce(); expect(getToken()).toBe("");
  expect(dialog()).toBeNull(); expect(writes()).toHaveLength(1); expect(saved).not.toHaveBeenCalled();
});

it.each(["write", "read"])("ignores a retired generation's %s 401 even when its token returns", async (phase) => {
  await load(); click("Remove relationship"); click("Remove");
  if (phase === "read") await reply(writes()[0], {});
  const old = phase === "write" ? writes()[0] : reads().at(-1)!;
  act(() => setToken("synthetic-owner-b")); act(() => setToken("synthetic-owner-a"));
  await reply(reads().at(-1)!, response(project, { people: [person.id] }, [person]));
  click("Remove relationship");
  const count = requests.length;
  await reply(old, { error: "invalid token" }, 401);
  expect(unauthorized).not.toHaveBeenCalled(); expect(getToken()).toBe("synthetic-owner-a");
  expect(requests).toHaveLength(count); expect(buttons("Remove")[0].disabled).toBe(false);
  expect(dialog()?.textContent).not.toContain("invalid token"); expect(saved).not.toHaveBeenCalled();
});


const backend = backendFixtures as unknown as {
  item: Record<"beforeProject" | "beforeItem" | "afterProject" | "afterItem", KBDetail>;
  attribution: Record<"beforeProject" | "beforePerson" | "partialProject" | "partialPerson" | "afterProject" | "afterPerson", KBDetail>;
};

it.each(["project", "item"])("uses backend raw, request and canonical identities when removing from the %s", async (direction) => {
  const fixture = backend.item;
  const source = direction === "project" ? fixture.beforeProject : fixture.beforeItem;
  render(entry(source.kind, direction === "project" ? "project" : "d/1"));
  await reply(reads().at(-1)!, { detail: source });
  click("Remove relationship"); click("Remove");
  expect(writes()[0].url).toBe("/api/v1/kb/docs/d%2F1");
  expect(JSON.parse(writes()[0].init.body as string)).toEqual({ remove_projects: ["project"] });
  await reply(writes()[0], { id: "d/1" });
  const pair = reads().slice(-2);
  expect(pair.map((r) => r.url).sort()).toEqual(["/api/v1/kb/docs/d%2F1", "/api/v1/kb/projects/project"]);
  for (const r of pair) await reply(r, { detail: r.url.includes("/docs/") ? fixture.afterItem : fixture.afterProject });
  expect(saved).toHaveBeenCalledOnce(); expect(dialog()).toBeNull();
});

it.each(["Close", "Escape", "backdrop"])("keeps an attribution-failure correction accessible after %s and retries the original delta", async (dismiss) => {
  const fixture = backend.attribution;
  render(entry("projects", "project")); await reply(reads().at(-1)!, { detail: fixture.beforeProject });
  click("Remove relationship"); click("Remove");
  const original = writes()[0];
  await reply(original, { error: "Synthetic attribution cleanup failure" }, 400);
  for (const r of reads().slice(-2)) await reply(r, { detail: r.url.includes("/people/") ? fixture.partialPerson : fixture.partialProject });
  expect(saved).not.toHaveBeenCalled();
  expect(buttons("Retry removal")).toHaveLength(1);
  const retry = buttons("Retry removal")[0];
  act(() => {
    if (dismiss === "Escape") document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    else if (dismiss === "backdrop") dialog()!.parentElement!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    else buttons("Close")[0].click();
    retry.click(); // A retained DOM callback must not dispatch after dismissal, even before React commits it.
  });
  expect(writes()).toHaveLength(1);
  expect(dialog()).toBeNull();
  expect(host.textContent).toContain("Example project"); expect(host.textContent).toContain("Example person");
  expect(buttons("Review correction")).toHaveLength(1);
  click("Review correction"); click("Retry removal");
  expect(writes()).toHaveLength(2);
  expect(writes()[1].url).toBe(original.url); expect(writes()[1].init.body).toBe(original.init.body);
  await reply(writes()[1], { id: "project" });
  for (const r of reads().slice(-2)) await reply(r, { detail: r.url.includes("/people/") ? fixture.afterPerson : fixture.afterProject });
  expect(saved).toHaveBeenCalledOnce(); expect(buttons("Review correction")).toHaveLength(0);
});

it("fences a retained submit synchronously when confirmation is cancelled", async () => {
  await load(); click("Remove relationship"); const remove = buttons("Remove")[0];
  act(() => { buttons("Cancel")[0].click(); remove.click(); });
  expect(writes()).toHaveLength(0); expect(dialog()).toBeNull();
});

it("keeps an accepted but still extension-owned removal unconfirmed and read-only", async () => {
  const fixture = backend.attribution;
  render(entry("projects", "project")); await reply(reads().at(-1)!, { detail: fixture.beforeProject });
  click("Remove relationship"); click("Remove"); await reply(writes()[0], { id: "project" });
  for (const r of reads().slice(-2)) await reply(r, { detail: r.url.includes("/people/") ? fixture.partialPerson : fixture.partialProject });
  expect(saved).not.toHaveBeenCalled(); expect(buttons("Retry removal")).toHaveLength(0);
  click("Close"); click("Review correction"); click("Retry verification");
  expect(writes()).toHaveLength(1);
  for (const r of reads().slice(-2)) await reply(r, { detail: r.url.includes("/people/") ? fixture.afterPerson : fixture.afterProject });
  expect(saved).toHaveBeenCalledOnce();
});

it.each(["Close", "Escape", "backdrop"])("keeps the sole retry for a partial reverse edge after %s", async (dismiss) => {
  await load(); click("Remove relationship"); click("Remove");
  const original = writes()[0];
  await reply(original, { error: "Synthetic reverse write failure" }, 400);
  await verify(project, person, "people", "projects", { partial: true });
  expect(host.textContent).not.toContain(person.name);
  act(() => {
    if (dismiss === "Escape") document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    else if (dismiss === "backdrop") dialog()!.parentElement!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    else buttons("Close")[0].click();
  });
  expect(dialog()).toBeNull(); expect(host.textContent).toContain(person.name);
  expect(buttons("Remove relationship")).toHaveLength(0);
  click("Review correction"); click("Retry removal");
  expect(writes()[1].url).toBe(original.url); expect(writes()[1].init.body).toBe(original.init.body);
  await reply(writes()[1], {}); await verify();
  expect(saved).toHaveBeenCalledOnce();
});

it.each(["chats", "people"].flatMap((kind) => ["project", "entity"].map((direction) => ({ kind, direction }))))("preserves raw $kind IDs and canonical readback when correcting from $direction", async ({ kind, direction }) => {
  const fixture = (backendFixtures as unknown as Record<string, Record<string, KBDetail>>)[kind];
  const source = direction === "project" ? fixture.beforeProject : fixture.beforeEntity;
  const raw = `${kind}/2`;
  render(entry(source.kind, direction === "project" ? "project" : raw));
  await reply(reads().at(-1)!, { detail: source });
  click("Remove relationship"); click("Remove");
  const expectedURL = direction === "project" ? "/api/v1/kb/projects/project" : `/api/v1/kb/${kind}/${encodeURIComponent(raw)}`;
  expect(writes()[0].url).toBe(expectedURL);
  expect(JSON.parse(writes()[0].init.body as string)).toEqual(direction === "project" ? { [`remove_${kind}`]: [raw] } : { remove_projects: ["project"] });
  await reply(writes()[0], {});
  for (const r of reads().slice(-2)) await reply(r, { detail: r.url.includes(`/${kind}/`) ? fixture.afterEntity : fixture.afterProject });
  expect(saved).toHaveBeenCalledOnce();
});

it("places a retained correction before the remaining related lists", async () => {
  render(project); await reply(reads().at(-1)!, response(project, { people: [person.id], chats: [chat.id] }, [person, chat]));
  // Related chats come first; choose the person's action in the second group.
  act(() => buttons("Remove relationship")[1].click()); click("Remove");
  await reply(writes()[0], { error: "Synthetic reverse write failure" }, 400);
  for (const r of reads().slice(-2)) await reply(r, { detail: r.url.includes("/people/") ? response(person, { projects: [project.id] }).detail : response(project, { people: [], chats: [chat.id], locked_fields: ["people"] }, [chat]).detail });
  click("Close");
  const pending = buttons("Review correction")[0].closest('[role="status"]')!;
  const remaining = host.querySelector(`a[href="/kb/chats/${chat.id}"]`)!.closest("section")!;
  expect(pending.compareDocumentPosition(remaining) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(writes()).toHaveLength(1);
});
