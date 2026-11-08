// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { getToken, handleUnauthorized, setToken } from "../../lib/api";
import { KB_KINDS, type KBDetailResponse } from "../../lib/kb";
import { setRosterDefault } from "../../lib/manager-choice";
import { EntryDetail } from "./KBDetail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Request = { url: string; init: RequestInit; resolve: (value: Response) => void; reject: (error: Error) => void };
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const saved = vi.fn();
const unauthorized = vi.fn();
const detail = (kind = "people", id = "example", pinned?: string): KBDetailResponse => ({
  detail: { kind, id, path: "", meta: { name: "Example entry", pinned, tags: ["existing"], importance: "P1", locked_fields: ["name"] }, summary: "# Example entry\n\nInferred profile." },
});
const render = (kind = "people", id = "example", reloadKey = 0) => act(() => root.render(
  <EntryDetail kind={kind} id={id} reloadKey={reloadKey} onToggleSidebar={() => {}} onBack={() => {}} onOpen={() => {}} onSaved={saved} />,
));
const reply = async (request: Request, value: unknown, status = 200) => act(async () => {
  request.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));
});
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label)!;
const click = (label: string) => act(() => button(label).click());
const text = () => host.querySelector<HTMLTextAreaElement>("textarea")!;
const input = (value: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(text(), value);
  text().dispatchEvent(new Event("input", { bubbles: true }));
});
const writes = () => requests.filter((r) => r.init.method === "PUT");
const submit = () => act(() => host.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
const load = async (kind = "people", id = "example", pinned?: string) => {
  render(kind, id);
  await reply(requests.at(-1)!, detail(kind, id, pinned));
};

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  saved.mockClear();
  unauthorized.mockClear();
  handleUnauthorized(unauthorized);
  requests = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve, reject) => requests.push({ url, init, resolve, reject }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  handleUnauthorized(() => {});
  vi.unstubAllGlobals();
});

describe("confirmed facts in Knowledge detail", () => {
  it("shows existing multiline facts separately from the inferred profile", async () => {
    await load("people", "example", "Technical lead.\nOwns the example project.");
    const section = [...host.querySelectorAll("section")].find((s) => s.textContent?.includes("Confirmed facts"));
    expect(section?.textContent).toContain("Technical lead.\nOwns the example project.");
    expect(section?.textContent).not.toContain("Inferred profile.");
    expect(host.textContent).toContain("Inferred profile.");
    expect(button("Edit")).toBeDefined();
  });

  it("offers to add facts when the server has none", async () => {
    await load();
    expect(host.textContent).toContain("No confirmed facts yet.");
    click("Add");
    expect(text().value).toBe("");
    expect(button("Save").disabled).toBe(true);
  });

  it.each(KB_KINDS)("creates, edits and clears facts for %s with field-only bodies and authoritative readback", async (kind) => {
    const id = kind === "recordings" ? "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" : "example";
    await load(kind, id);
    let previous = "";
    for (const next of ["  Confirmed lead.\n\nScope: example only.  ", "Corrected role.\nSecond line.", ""]) {
      click(previous ? "Edit" : "Add");
      expect(text().value).toBe(previous);
      expect(button("Save").disabled).toBe(true);
      input(next);
      expect(button("Save").disabled).toBe(false);
      click("Save");
      const put = writes().at(-1)!;
      expect(put.url).toBe(`/api/v1/kb/${kind}/${id}`);
      expect(JSON.parse(put.init.body as string)).toEqual({ pinned: next });
      expect(new Headers(put.init.headers).get("Authorization")).toBe("Bearer synthetic-owner-a");
      expect(new Headers(put.init.headers).get("X-Persona-Manager")).toBe("assistant");
      await reply(put, { id });
      expect(host.textContent).toContain("Saved. Loading the latest entry");
      expect(text().disabled).toBe(true);
      const read = requests.at(-1)!;
      expect(read.init.method).toBeUndefined();
      expect(read.url).toBe(put.url);
      // Display what the hub read back, even if another writer changed facts after our PUT.
      const authoritative = next ? `${next}\nRead back.` : "";
      await reply(read, detail(kind, id, authoritative));
      expect(text()).toBeNull();
      expect(host.textContent).toContain(authoritative || "No confirmed facts yet.");
      expect(host.textContent).toContain("Inferred profile.");
      expect(host.textContent).toContain("#existing");
      previous = authoritative;
    }
    expect(saved).toHaveBeenCalledTimes(3);
    expect(writes()).toHaveLength(3);
  });

  it("cancels without writing, and reopening discards the cancelled draft", async () => {
    await load("people", "example", "Original");
    click("Edit");
    input("Unsent correction");
    click("Cancel");
    expect(writes()).toHaveLength(0);
    expect(host.textContent).toContain("Original");
    click("Edit");
    expect(text().value).toBe("Original");
    input("Changed");
    input("Original");
    expect(button("Save").disabled).toBe(true);
    submit();
    expect(writes()).toHaveLength(0);
  });

  it("keeps the existing tag editor separate from confirmed facts", async () => {
    await load("people", "example", "Confirmed role");
    click("Tag");
    const dialog = document.querySelector('[role="dialog"]')!;
    const tags = dialog.querySelector<HTMLInputElement>('input[aria-label="Tags"]')!;
    expect(tags.value).toBe("existing");
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(tags, "updated");
      tags.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...dialog.querySelectorAll("button")].find((b) => b.textContent === "Save")!.click());
    expect(JSON.parse(writes()[0].init.body as string)).toEqual({ importance: "P1", relevance: 0, tags: ["updated"] });
    await reply(writes()[0], { id: "example" });
    expect(host.textContent).toContain("Confirmed role");
    expect(host.textContent).toContain("Inferred profile.");
  });

  it("keeps the draft and an actionable error when a write fails, then retries once", async () => {
    await load();
    click("Add");
    input("Keep this\nexact draft.");
    submit();
    await reply(writes()[0], { error: "Synthetic write failure" }, 503);
    expect(text().value).toBe("Keep this\nexact draft.");
    expect(text().disabled).toBe(false);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("Your draft is kept. Retry saving:");
    expect(host.textContent).toContain("Synthetic write failure");
    click("Retry saving");
    expect(writes()).toHaveLength(2);
    expect(writes()[1].init.body).toBe(writes()[0].init.body);
    await reply(writes()[1], { id: "example" });
    await reply(requests.at(-1)!, detail("people", "example", "Keep this\nexact draft."));
    expect(text()).toBeNull();
    expect(saved).toHaveBeenCalledOnce();
  });

  it("admits one submission synchronously and blocks further writes through readback", async () => {
    await load();
    click("Add");
    input("Confirmed");
    act(() => {
      const form = host.querySelector("form")!;
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(writes()).toHaveLength(1);
    expect(button("Cancel").disabled).toBe(true);
    await reply(writes()[0], { id: "example" });
    submit();
    expect(writes()).toHaveLength(1);
    await reply(requests.at(-1)!, detail("people", "example", "Confirmed"));
    expect(saved).toHaveBeenCalledOnce();
  });

  it("retries only the read after a successful PUT and failed GET", async () => {
    await load();
    click("Add");
    input("Saved correction");
    submit();
    await reply(writes()[0], { id: "example" });
    await reply(requests.at(-1)!, { error: "Synthetic read failure" }, 503);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("Your confirmed facts were saved");
    expect(host.textContent).toContain("Synthetic read failure");
    expect(text().value).toBe("Saved correction");
    expect(button("Save")).toBeUndefined();
    submit();
    expect(writes()).toHaveLength(1);
    click("Retry refresh");
    await reply(requests.at(-1)!, detail("people", "example", "Authoritative correction"));
    expect(writes()).toHaveLength(1);
    expect(host.textContent).toContain("Authoritative correction");
    expect(host.textContent).not.toContain("Saved correction");
    expect(saved).toHaveBeenCalledOnce();
  });

  it.each(["id", "kind"])("discards old detail immediately when the requested %s changes", async (change) => {
    await load("people", "example", "Old facts");
    click("Edit");
    input("Old draft");
    const kind = change === "kind" ? "projects" : "people";
    const id = change === "id" ? "another" : "example";
    render(kind, id);
    expect(text()).toBeNull();
    expect(host.textContent).not.toContain("Old facts");
    expect(button("Tag").disabled).toBe(true);
    await reply(requests.at(-1)!, detail(kind, id, "New facts"));
    click("Edit");
    expect(text().value).toBe("New facts");
    input("New draft");
    submit();
    expect(writes()).toHaveLength(1);
    expect(writes()[0].url).toBe(`/api/v1/kb/${kind}/${id}`);
    expect(writes()[0].init.body).toBe(JSON.stringify({ pinned: "New draft" }));
  });

  it.each(["success", "failure"])("ignores late write %s after navigation and reopening", async (result) => {
    await load("people", "example", "Original");
    click("Edit"); input("Old draft"); submit();
    const old = writes()[0];
    render("docs", "another");
    render();
    await reply(requests.at(-1)!, detail("people", "example", "Current facts"));
    click("Edit"); input("Current draft"); submit();
    const count = requests.length;
    await reply(old, result === "success" ? { id: "example" } : { error: "Old write failed" }, result === "success" ? 200 : 503);
    expect(requests).toHaveLength(count);
    expect(text().value).toBe("Current draft");
    expect(text().disabled).toBe(true);
    expect(host.textContent).not.toContain("Old write failed");
    expect(saved).not.toHaveBeenCalled();
  });

  it.each(["credential", "manager"].flatMap((change) => ["success", "failure"].map((result) => ({ change, result }))))("remounts same-entry drafts on $change change and ignores old $result", async ({ change, result }) => {
    await load("people", "example", "Old owner facts");
    click("Edit"); input("Old owner draft"); submit();
    const old = writes()[0];
    act(() => change === "credential" ? setToken("synthetic-owner-b") : setRosterDefault("example-manager"));
    expect(text()).toBeNull();
    expect(host.textContent).not.toContain("Old owner facts");
    const read = requests.at(-1)!;
    expect(new Headers(read.init.headers).get("Authorization")).toBe(`Bearer synthetic-owner-${change === "credential" ? "b" : "a"}`);
    expect(new Headers(read.init.headers).get("X-Persona-Manager")).toBe(change === "manager" ? "example-manager" : "assistant");
    await reply(read, detail("people", "example", "Current owner facts"));
    click("Edit"); input("Current owner draft"); submit();
    const count = requests.length;
    await reply(old, result === "success" ? { id: "example" } : { error: "Old owner failure" }, result === "success" ? 200 : 401);
    expect(text().value).toBe("Current owner draft");
    expect(text().disabled).toBe(true);
    expect(requests).toHaveLength(count);
    expect(host.textContent).not.toContain("Old owner failure");
    expect(saved).not.toHaveBeenCalled();
  });

  it("fences a token replaced directly before a rendered handler can submit", async () => {
    await load();
    click("Add"); input("Stale draft");
    localStorage.setItem("persona.token", "synthetic-owner-b");
    submit();
    expect(writes()).toHaveLength(0);
  });

  it("observes token replacement from another tab and does not revive an old draft when credentials return", async () => {
    await load("people", "example", "Original");
    click("Edit"); input("Unsent owner draft");
    act(() => {
      localStorage.setItem("persona.token", "synthetic-owner-b");
      window.dispatchEvent(new StorageEvent("storage", { key: "persona.token" }));
    });
    expect(text()).toBeNull();
    act(() => setToken("synthetic-owner-a"));
    await reply(requests.at(-1)!, detail("people", "example", "Fresh after replacement"));
    click("Edit");
    expect(text().value).toBe("Fresh after replacement");
    expect(writes()).toHaveLength(0);
  });

  it("keeps server-canonicalized recording IDs bound to the requested route", async () => {
    const requested = "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA";
    render("recordings", requested);
    await reply(requests.at(-1)!, detail("recordings", requested.toLowerCase(), "Original"));
    click("Edit"); input("Correction"); submit();
    expect(writes()[0].url).toBe(`/api/v1/kb/recordings/${requested}`);
    await reply(writes()[0], { id: requested.toLowerCase() });
    await reply(requests.at(-1)!, detail("recordings", requested.toLowerCase(), "Correction"));
    expect(text()).toBeNull();
    expect(host.textContent).toContain("Correction");
  });

  it("ignores late reads, including a successful readback from a retired owner", async () => {
    render();
    const stale = requests[0];
    render("docs", "another");
    await reply(requests.at(-1)!, detail("docs", "another", "Current"));
    await reply(stale, detail("people", "example", "Stale"));
    expect(host.textContent).not.toContain("Stale");
    click("Edit"); input("Written"); submit();
    await reply(writes()[0], { id: "another" });
    const readback = requests.at(-1)!;
    act(() => setToken("synthetic-owner-b"));
    await reply(requests.at(-1)!, detail("docs", "another", "New owner"));
    await reply(readback, detail("docs", "another", "Retired owner"));
    expect(host.textContent).toContain("New owner");
    expect(host.textContent).not.toContain("Retired owner");
    expect(saved).not.toHaveBeenCalled();
  });

  it("does not let an older same-entry reread replace a newer successful readback", async () => {
    await load();
    render("people", "example", 1);
    const earlier = requests.at(-1)!;
    click("Add"); input("Current"); submit();
    await reply(writes()[0], { id: "example" });
    await reply(requests.at(-1)!, detail("people", "example", "Current"));
    await reply(earlier, detail("people", "example", "Outdated"));
    expect(host.textContent).toContain("Current");
    expect(host.textContent).not.toContain("Outdated");
  });

  it("uses normal sign-out for a current owner's detail 401", async () => {
    handleUnauthorized(() => { unauthorized(); setToken(""); });
    render();
    await reply(requests[0], { error: "invalid token" }, 401);
    expect(unauthorized).toHaveBeenCalledOnce();
    expect(getToken()).toBe("");
    expect(host.textContent).not.toContain("invalid token");
    expect(text()).toBeNull();
  });

  it("uses normal sign-out for a current owner's facts PUT 401", async () => {
    await load();
    handleUnauthorized(() => { unauthorized(); setToken(""); });
    click("Add"); input("Current draft"); submit();
    await reply(writes()[0], { error: "invalid token" }, 401);
    expect(unauthorized).toHaveBeenCalledOnce();
    expect(getToken()).toBe("");
    expect(text()).toBeNull();
    expect(button("Retry saving")).toBeUndefined();
    expect(writes()).toHaveLength(1);
    expect(saved).not.toHaveBeenCalled();
  });

  it("signs out on a current readback 401 without repeating the successful mutation", async () => {
    await load();
    handleUnauthorized(() => { unauthorized(); setToken(""); });
    click("Add"); input("Accepted correction"); submit();
    await reply(writes()[0], { id: "example" });
    await reply(requests.at(-1)!, { error: "invalid token" }, 401);
    expect(unauthorized).toHaveBeenCalledOnce();
    expect(getToken()).toBe("");
    expect(writes()).toHaveLength(1);
    expect(saved).not.toHaveBeenCalled();
  });

  it.each(["GET", "PUT"])("ignores an old generation's %s 401 even after the same token returns", async (method) => {
    let old: Request;
    if (method === "PUT") {
      await load();
      click("Add"); input("Old draft"); submit();
      old = writes()[0];
    } else {
      render();
      old = requests[0];
    }
    act(() => setToken("synthetic-owner-b"));
    act(() => setToken("synthetic-owner-a"));
    await reply(requests.at(-1)!, detail("people", "example", "Current generation"));
    click("Edit"); input("Current generation draft");
    const count = requests.length;
    await reply(old, { error: "invalid token" }, 401);
    expect(unauthorized).not.toHaveBeenCalled();
    expect(getToken()).toBe("synthetic-owner-a");
    expect(requests).toHaveLength(count);
    expect(text().value).toBe("Current generation draft");
    expect(text().disabled).toBe(false);
    expect(host.textContent).not.toContain("invalid token");
  });
});
