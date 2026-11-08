// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FileContent } from "../../../lib/files";

vi.mock("../../assistant/TaskPanel", () => ({ AgentPanel: () => null, TaskPanel: () => null }));
vi.mock("../../chat/SubagentPanel", () => ({ SubagentPanel: () => null }));
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const requests = vi.hoisted(() => [] as {
  path: string;
  signal?: AbortSignal;
  resolve: (file: FileContent) => void;
  reject: (error: Error) => void;
}[]);
vi.mock("../../../lib/api", async (original) => ({
  ...(await original<typeof import("../../../lib/api")>()),
  // Deliberately ignore abort: a transport may already have delivered a response when its owner is retired.
  api: (path: string, options?: { signal?: AbortSignal }) => new Promise<FileContent>((resolve, reject) => requests.push({ path, signal: options?.signal, resolve, reject })),
}));
vi.mock("../../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../../ui/atoms")>()), toast: vi.fn() }));
import { useFileDoc, type FileDoc } from "./FileView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let doc: FileDoc;
let root: Root;
let host: HTMLDivElement;
function Harness({ mid = "demo", path = "/work/example.txt", cwd = "/work" }: { mid?: string; path?: string; cwd?: string }) {
  doc = useFileDoc(mid, path, cwd);
  return <pre>{doc.text}</pre>;
}
const page = (over: Partial<FileContent> = {}): FileContent => ({ path: "/work/example.txt", name: "example.txt", size: 12, mime: "text/plain", is_dir: false, text: "one\n", next: 4, revision: "r1", truncated: true, ...over });
const outcome = <T,>(promise: Promise<T>) => promise.then((value) => ({ value, error: undefined }), (error: unknown) => ({ value: undefined, error }));
const flush = async () => { await act(async () => { await Promise.resolve(); }); };
async function resolve(index: number, file = page()) { await act(async () => requests[index].resolve(file)); }
async function open(props = {}) { await act(async () => root.render(<Harness {...props} />)); }
function more() { let pending: Promise<boolean>; act(() => { pending = doc.more(); }); return pending!; }
function all(cap?: number) { let pending: ReturnType<typeof outcome<string>>; act(() => { pending = outcome(doc.all(cap)); }); return pending!; }
function read(cap: number) { let pending: ReturnType<typeof outcome<{ text: string; complete: boolean }>>; act(() => { pending = outcome(doc.read(cap)); }); return pending!; }

beforeEach(() => {
  requests.length = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

describe("useFileDoc revision paging", () => {
  it("rejects a different revision without appending it or marking the prefix complete", async () => {
    await open();
    await resolve(0, page({ text: "OLD\n" }));
    const pending = more();
    expect(requests[1].path).toContain("offset=4");
    await resolve(1, page({ text: "NEW\n", next: 0, revision: "r2", truncated: false }));
    expect(await pending).toBe(false);
    expect(doc.text).toBe("OLD\n");
    expect(doc.file?.revision).toBe("r1");
    expect(doc.changed).toBe(true);
    expect(doc.next).toBe(4);
    expect((await all()).error).toBeInstanceOf(Error);
    expect(await more()).toBe(false);
    expect(requests).toHaveLength(2);
  });

  it.each([
    ["r1", "r1", "r1"],
    [undefined, undefined, undefined],
    ["r1", undefined, undefined],
    [undefined, "r1", undefined],
  ])("accepts compatible revisions %s / %s with aggregate revision %s", async (first, second, revision) => {
    await open();
    await resolve(0, page({ revision: first }));
    const pending = all();
    await resolve(1, page({ text: "two\n", next: 0, truncated: false, revision: second }));
    expect(await pending).toEqual({ value: "one\ntwo\n", error: undefined });
    expect(doc.file?.revision).toBe(revision);
    expect(doc.next).toBe(0);
    expect(doc.changed).toBe(false);
  });

  it.each([["r1", undefined, "r2"], [undefined, "r1", "r2"]])("compares known revisions even across unversioned pages (%s / %s / %s)", async (first, second, third) => {
    await open();
    await resolve(0, page({ revision: first }));
    const pending = all();
    await resolve(1, page({ text: "two\n", next: 8, revision: second }));
    await resolve(2, page({ text: "changed\n", next: 0, truncated: false, revision: third }));
    expect((await pending).error).toBeInstanceOf(Error);
    expect(doc.text).toBe("one\ntwo\n");
    expect(doc.file?.revision).toBeUndefined();
    expect(doc.changed).toBe(true);
  });

  it("keeps loaded pages after a failure and retries the same offset", async () => {
    await open();
    await resolve(0);
    const failed = all();
    await act(async () => requests[1].reject(new Error("synthetic page failure")));
    expect((await failed).error).toBeInstanceOf(Error);
    expect(doc.text).toBe("one\n");
    expect(doc.next).toBe(4);
    expect(doc.loadingMore).toBe(false);
    expect(doc.changed).toBe(false);
    const retry = all();
    expect(requests[2].path).toBe(requests[1].path);
    await resolve(2, page({ text: "two\n", next: 0, truncated: false }));
    expect(await retry).toEqual({ value: "one\ntwo\n", error: undefined });
    expect(doc.pageError).toBe("");
  });

  it("waits for an admitted manual page without duplicating it and rejects its failure", async () => {
    await open();
    await resolve(0);
    const manual = more();
    const complete = all();
    expect(await more()).toBe(false);
    expect(requests).toHaveLength(2);
    await act(async () => requests[1].reject(new Error("synthetic concurrent failure")));
    expect(await manual).toBe(false);
    expect((await complete).error).toBeInstanceOf(Error);
  });

  it("shares successful pages between simultaneous complete reads", async () => {
    await open();
    await resolve(0);
    const a = all();
    const b = all();
    await resolve(1, page({ text: "two\n", next: 8 }));
    await resolve(2, page({ text: "end\n", next: 0, truncated: false }));
    expect((await a).value).toBe("one\ntwo\nend\n");
    expect((await b).value).toBe("one\ntwo\nend\n");
    expect(requests).toHaveLength(3);
  });

  it("rejects both complete readers when their shared page changes revision", async () => {
    await open();
    await resolve(0);
    const a = all();
    const b = all();
    await resolve(1, page({ text: "changed\n", next: 0, revision: "r2", truncated: false }));
    expect((await a).error).toBeInstanceOf(Error);
    expect((await b).error).toBeInstanceOf(Error);
    expect(doc.text).toBe("one\n");
    expect(requests).toHaveLength(2);
  });

  it("does not return a capped prefix as a complete read", async () => {
    await open();
    await resolve(0);
    expect((await all(4)).error).toBeInstanceOf(Error);
    expect(doc.next).toBe(4);
    expect(requests).toHaveLength(1);
  });

  it("allows an explicitly incomplete bounded preview and can continue it to completion", async () => {
    await open();
    await resolve(0);
    const preview = read(8);
    await resolve(1, page({ text: "two\n", next: 8 }));
    expect((await preview).value).toEqual({ text: "one\ntwo\n", complete: false });
    expect((await all(8)).error).toBeInstanceOf(Error);
    expect(requests).toHaveLength(2);
    const rest = read(12);
    await resolve(2, page({ text: "end\n", next: 0, truncated: false }));
    expect((await rest).value).toEqual({ text: "one\ntwo\nend\n", complete: true });
  });

  it.each(["failure", "revision"])("rejects a bounded preview on %s rather than returning a prefix", async (reason) => {
    await open();
    await resolve(0);
    const preview = read(8);
    if (reason === "failure") await act(async () => requests[1].reject(new Error("synthetic bounded failure")));
    else await resolve(1, page({ text: "changed\n", next: 8, revision: "r2" }));
    expect((await preview).error).toBeInstanceOf(Error);
    expect(doc.text).toBe("one\n");
  });

  it.each([
    { size: 1 }, { size: 0, offset: 4 }, { size: 0, next: 4 }, { size: 0, truncated: true },
    { size: 0, is_dir: true }, { size: 0, stream: true }, { size: 0, mime: "application/octet-stream" },
  ])("does not infer complete text from arbitrary missing content: %j", async (over) => {
    await open();
    await resolve(0, page({ text: undefined, next: 0, truncated: false, ...over }));
    expect((await all()).error).toBeInstanceOf(Error);
    expect((await read(64)).error).toBeInstanceOf(Error);
    expect(requests).toHaveLength(1);
  });

  it.each(["text/plain; charset=utf-8", "application/json"])("recognizes a complete zero-byte %s response with omitted text", async (mime) => {
    await open();
    await resolve(0, page({ text: undefined, size: 0, mime, offset: 0, next: 0, truncated: false }));
    expect((await all()).value).toBe("");
    expect((await read(64)).value).toEqual({ text: "", complete: true });
  });

  it("does not accept a non-advancing page as end of file", async () => {
    await open();
    await resolve(0);
    const pending = all();
    await resolve(1, page({ text: "duplicate\n", next: 4 }));
    expect((await pending).error).toBeInstanceOf(Error);
    expect(doc.text).toBe("one\n");
    expect(doc.next).toBe(4);
  });

  it("does not return an old node's terminal truncated response as a complete file", async () => {
    await open();
    await resolve(0, page({ next: 0, revision: undefined }));
    expect((await all()).error).toBeInstanceOf(Error);
  });
});

describe("useFileDoc read ownership", () => {
  it.each(["path", "mid", "cwd"])("retires initial and continuation reads when %s changes", async (key) => {
    await open();
    await resolve(0);
    const staleMore = doc.more;
    const pending = all();
    await open({ [key]: key === "path" ? "/work/other.txt" : "other" });
    await resolve(2, page({ text: "current\n", revision: "current" }));
    await resolve(1, page({ text: "stale\n", next: 0, truncated: false }));
    expect((await pending).error).toMatchObject({ name: "AbortError" });
    expect(await staleMore()).toBe(false);
    expect(doc.text).toBe("current\n");
    expect(doc.file?.revision).toBe("current");
    expect(doc.changed).toBe(false);
    expect(requests).toHaveLength(3);
  });

  it("fences an initial response after a path change", async () => {
    await open();
    await open({ path: "/work/other.txt" });
    await resolve(1, page({ text: "current\n" }));
    await resolve(0, page({ text: "stale\n" }));
    expect(doc.text).toBe("current\n");
  });

  it("rejects a retired bounded preview instead of returning its accepted prefix", async () => {
    await open();
    await resolve(0);
    const pending = read(8);
    await open({ path: "/work/other.txt" });
    await resolve(2, page({ text: "current\n", next: 0, truncated: false }));
    await resolve(1, page({ text: "stale\n", next: 8, revision: "stale" }));
    expect((await pending).error).toMatchObject({ name: "AbortError" });
    expect(doc.text).toBe("current\n");
    expect(doc.changed).toBe(false);
  });

  it("keeps a late old-path failure out of the current document", async () => {
    await open();
    await resolve(0);
    const pending = all();
    await open({ path: "/work/other.txt" });
    await resolve(2, page({ text: "current\n", next: 0, truncated: false }));
    await act(async () => requests[1].reject(new Error("stale synthetic failure")));
    expect((await pending).error).toMatchObject({ name: "AbortError" });
    expect(doc.text).toBe("current\n");
    expect(doc.error).toBe("");
    expect(doc.pageError).toBe("");
    expect(doc.loadingMore).toBe(false);
  });

  it("recovers an initial read failure by reloading", async () => {
    await open();
    await act(async () => requests[0].reject(new Error("synthetic initial failure")));
    expect(doc.error).toBe("synthetic initial failure");
    expect((await all()).error).toBeInstanceOf(Error);
    act(() => doc.reload());
    await resolve(1, page({ text: "recovered\n", next: 0, truncated: false }));
    expect(doc.error).toBe("");
    expect((await all()).value).toBe("recovered\n");
  });

  it("invalidates a read synchronously on reload and ignores its late mismatch", async () => {
    await open();
    await resolve(0);
    const pending = all();
    act(() => doc.reload());
    await resolve(2, page({ text: "fresh\n", next: 0, truncated: false, revision: "fresh" }));
    await resolve(1, page({ text: "stale\n", next: 0, truncated: false, revision: "stale" }));
    expect((await pending).error).toMatchObject({ name: "AbortError" });
    expect(doc.text).toBe("fresh\n");
    expect(doc.changed).toBe(false);
  });

  it("recovers from a revision change by starting at offset zero", async () => {
    await open();
    await resolve(0);
    const failed = all();
    await resolve(1, page({ text: "new tail\n", next: 0, truncated: false, revision: "r2" }));
    expect((await failed).error).toBeInstanceOf(Error);
    act(() => doc.reload());
    expect(requests[2].path).not.toContain("offset=");
    await resolve(2, page({ text: "new whole\n", next: 0, truncated: false, revision: "r2" }));
    expect((await all()).value).toBe("new whole\n");
    expect(doc.changed).toBe(false);
  });

  it("rejects a complete read retired by unmount", async () => {
    await open();
    await resolve(0);
    const pending = all();
    act(() => root.render(null));
    await resolve(1, page({ next: 0, truncated: false }));
    expect((await pending).error).toMatchObject({ name: "AbortError" });
  });

  it("ignores the first StrictMode setup's late response", async () => {
    await act(async () => root.render(<StrictMode><Harness /></StrictMode>));
    expect(requests).toHaveLength(2);
    await resolve(1, page({ text: "current\n" }));
    await resolve(0, page({ text: "stale\n" }));
    await flush();
    expect(doc.text).toBe("current\n");
  });
});
