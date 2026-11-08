import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
const reports = vi.hoisted(() => ({ list: [] as { error: unknown; boundary: string; chunk: boolean }[] }));
vi.mock("../lib/error-reports", () => ({
  reportBoundary: (error: unknown, boundary: string, chunk: boolean) => void reports.list.push({ error, boundary, chunk }),
}));
import { isChunkError, PageBoundary, reloadsFor } from "./PageBoundary";

afterEach(() => {
  reports.list = [];
  vi.unstubAllGlobals();
});

describe("PageBoundary", () => {
  it("knows a chunk that would not load from other errors", () => {
    expect(isChunkError(new TypeError("Failed to fetch dynamically imported module: /assets/Settings-abc.js"))).toBe(true);
    expect(isChunkError(new TypeError("Importing a module script failed."))).toBe(true);
    expect(isChunkError(new Error("Cannot read properties of undefined"))).toBe(false);
    // an API call that failed while the page drew is not a missing chunk: no reload for it
    expect(isChunkError(new TypeError("Failed to fetch"))).toBe(false);
  });
  it("reloads once for a chunk error, not again within a minute", () => {
    const e = new TypeError("Failed to fetch dynamically imported module");
    expect(reloadsFor(e, null, 1_000_000)).toBe(true);
    expect(reloadsFor(e, 990_000, 1_000_000)).toBe(false);
    expect(reloadsFor(e, 900_000, 1_000_000)).toBe(true);
    expect(reloadsFor(new Error("boom"), null, 1_000_000)).toBe(false);
  });
  it("draws its page while nothing failed, and a way out once it does", () => {
    expect(renderToStaticMarkup(<PageBoundary page="x"><p>页</p></PageBoundary>)).toBe("<p>页</p>");
    const failed = PageBoundary.getDerivedStateFromError(new TypeError("Failed to fetch dynamically imported module"));
    const b = new PageBoundary({ page: "x", children: null });
    b.state = failed;
    const html = renderToStaticMarkup(<>{b.render()}</>);
    expect(html).toContain("这一页没能加载");
    expect(html).toContain("重新加载");
  });
  it("reports a render crash as fatal and draws the same way out", () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { reload });
    const e = new Error("boom");
    const b = new PageBoundary({ page: "x", children: null });
    b.componentDidCatch(e, { componentStack: "" });
    expect(reports.list).toEqual([{ error: e, boundary: "page_boundary", chunk: false }]);
    expect(reload).not.toHaveBeenCalled();
    b.state = PageBoundary.getDerivedStateFromError(e);
    expect(renderToStaticMarkup(<>{b.render()}</>)).toContain("重新加载");
  });
  it("only reloads for the first chunk failure, and reports one that comes back after it", () => {
    const reload = vi.fn();
    const stored = new Map<string, string>();
    vi.stubGlobal("location", { reload });
    vi.stubGlobal("sessionStorage", { getItem: (k: string) => stored.get(k) ?? null, setItem: (k: string, v: string) => void stored.set(k, v) });
    const e = new TypeError("Failed to fetch dynamically imported module");
    const b = new PageBoundary({ page: "x", children: null });
    b.componentDidCatch(e, { componentStack: "" });
    expect(reload).toHaveBeenCalledTimes(1);
    expect(reports.list).toEqual([]);
    b.componentDidCatch(e, { componentStack: "" });
    expect(reload).toHaveBeenCalledTimes(1);
    expect(reports.list).toEqual([{ error: e, boundary: "page_boundary", chunk: true }]);
  });
});
