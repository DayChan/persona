import { describe, expect, it } from "vitest";
import {
  BATCH,
  componentFrames,
  ErrorReporter,
  MAX_AGE_MS,
  MAX_QUEUE,
  navigationCrumb,
  osOf,
  requestCrumb,
  setReporterForTest,
  parseStack,
  pathPattern,
  QUEUE_KEY,
  scrubText,
  urlPath,
  type ReporterDeps,
  type SendResult,
} from "./error-reports";
import type { ErrorReport } from "./extension-wire";

const ORIGIN = "https://hub.example";

function memStorage() {
  const m = new Map<string, string>();
  return {
    m,
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
  };
}

function setup(over: Partial<ReporterDeps> = {}, answers: SendResult[] = []) {
  const storage = memStorage();
  const sent: ErrorReport[][] = [];
  const timers: (() => void)[] = [];
  let clock = Date.parse("2026-11-06T09:00:00Z");
  let n = 0;
  const deps: ReporterDeps = {
    storage,
    send: async (reports) => {
      sent.push(reports);
      return answers.shift() ?? { ok: true, enabled: true };
    },
    now: () => clock,
    uuid: () => `00000000-0000-4000-8000-${String(++n).padStart(12, "0")}`,
    origin: ORIGIN,
    platform: { os: "macos", os_version: "10" },
    setTimer: (fn) => timers.push(fn),
    clearTimer: () => {},
    ...over,
  };
  const r = new ErrorReporter(deps);
  return {
    r,
    storage,
    sent,
    timers,
    tick: (ms: number) => (clock += ms),
    runTimers: async () => {
      while (timers.length) timers.shift()!();
      await new Promise((res) => setTimeout(res, 0));
    },
  };
}

const err = (message: string, stack?: string) => {
  const e = new Error(message);
  e.stack = stack ?? `Error: ${message}\n    at f${message} (${ORIGIN}/assets/index-abc.js:1:${message.length})`;
  return e;
};

describe("parseStack", () => {
  it("reads V8 stacks, innermost last, paths only", () => {
    const stack = [
      "TypeError: Cannot read properties of undefined (reading 'x')",
      `    at render (${ORIGIN}/assets/index-abc.js?v=2:10:20)`,
      `    at async load (${ORIGIN}/assets/Chat-def.js:3:4)`,
      "    at https://cdn.example/lib.js:5:6",
      "    at new Thing (<anonymous>)",
    ].join("\n");
    expect(parseStack(stack, ORIGIN)).toEqual([
      { file: "/lib.js", line: 5, column: 6, in_app: false },
      { function: "load", file: "/assets/Chat-def.js", line: 3, column: 4, in_app: true },
      { function: "render", file: "/assets/index-abc.js", line: 10, column: 20, in_app: true },
    ]);
  });
  it("reads Firefox and Safari stacks", () => {
    const stack = [`render@${ORIGIN}/assets/index-abc.js:10:20`, `@${ORIGIN}/assets/index-abc.js#x:1:2`, "global code@https://other.example/a.js:7:8", "[native code]"].join("\n");
    expect(parseStack(stack, ORIGIN)).toEqual([
      { function: "global code", file: "/a.js", line: 7, column: 8, in_app: false },
      { file: "/assets/index-abc.js", line: 1, column: 2, in_app: true },
      { function: "render", file: "/assets/index-abc.js", line: 10, column: 20, in_app: true },
    ]);
  });
  it("gives nothing for no stack", () => {
    expect(parseStack(undefined, ORIGIN)).toEqual([]);
    expect(parseStack("just text", ORIGIN)).toEqual([]);
  });
});

describe("componentFrames", () => {
  it("reads a production component stack, innermost last, keeping components that have no position by name", () => {
    // as React 19's production build writes ErrorInfo.componentStack: minified names, a position only where it has one
    const stack = ["", `    at Bq (${ORIGIN}/assets/App-abc.js:14:28)`, "    at div (<anonymous>)", "    at Xr (<anonymous>)", `    at PageBoundary (${ORIGIN}/assets/App-abc.js:9:20)`].join("\n");
    expect(componentFrames(stack, ORIGIN)).toEqual([
      { function: "PageBoundary", file: "/assets/App-abc.js", line: 9, column: 20, in_app: true },
      { function: "Xr" },
      { function: "div" },
      { function: "Bq", file: "/assets/App-abc.js", line: 14, column: 28, in_app: true },
    ]);
    expect(componentFrames(undefined, ORIGIN)).toEqual([]);
  });
});

describe("scrubbing", () => {
  it("cuts URLs to their path", () => {
    expect(urlPath("https://hub.example:8443/api/v1/x?token=abc#f")).toBe("/api/v1/x");
    expect(urlPath("/assets/a.js?v=1")).toBe("/assets/a.js");
    expect(urlPath("https://hub.example")).toBe("/");
  });
  it("takes hosts, queries and e-mail addresses out of text", () => {
    expect(scrubText("Failed to fetch https://hub.example/api/v1/files?path=/secret&token=t0k now")).toBe("Failed to fetch /api/v1/files now");
    expect(scrubText("GET /api/v1/x?token=t0k failed")).toBe("GET /api/v1/x failed");
    expect(scrubText("no user me@example.com")).toBe("no user <email>");
    expect(scrubText("plain message")).toBe("plain message");
  });
  it("makes API paths a pattern without ids", () => {
    expect(pathPattern("/sessions/my-box/0b5c-11/items/abc/full")).toBe("/sessions/:id/:id/items/:id/full");
    expect(pathPattern("/machines/box1/panes?x=1")).toBe("/machines/:id/panes");
    expect(pathPattern("/api/v1/todos/123")).toBe("/todos/:id");
  });
  it("tells the system coarsely", () => {
    expect(osOf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit")).toEqual({ os: "macos", os_version: "10" });
    expect(osOf("Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X)")).toEqual({ os: "ios", os_version: "18" });
    expect(osOf("Mozilla/5.0 (Linux; Android 16; Pixel)")).toEqual({ os: "android", os_version: "16" });
    expect(osOf("Mozilla/5.0 (Windows NT 10.0; Win64; x64)")).toEqual({ os: "windows", os_version: "10" });
    expect(osOf("Mozilla/5.0 (X11; Linux x86_64)")).toEqual({ os: "linux" });
    expect(osOf("", "macOS")).toEqual({ os: "macos", os_version: undefined });
  });
});

describe("ErrorReporter", () => {
  it("builds a web exception report", () => {
    const { r } = setup();
    r.breadcrumb("navigation", "settings");
    r.capture(err("boom"), { mechanism: "onerror" });
    const [rep] = r.pending();
    expect(rep).toMatchObject({ source: "web", kind: "exception", level: "error", os: "macos", message: "Error: boom", time: "2026-11-06T09:00:00.000Z", tags: { mechanism: "onerror" } });
    expect(rep.exceptions?.[0]).toMatchObject({ type: "Error", value: "boom", frames: [{ file: "/assets/index-abc.js", in_app: true }] });
    expect(rep.breadcrumbs).toEqual([{ time: "2026-11-06T09:00:00.000Z", category: "navigation", message: "settings", level: "info" }]);
    expect(rep.release).toBeUndefined();
  });
  it("keeps the last 30 breadcrumbs", () => {
    const { r } = setup();
    for (let i = 0; i < 40; i++) r.breadcrumb("navigation", `p${i}`);
    r.capture(err("x"));
    const crumbs = r.pending()[0].breadcrumbs!;
    expect(crumbs).toHaveLength(30);
    expect(crumbs[0].message).toBe("p10");
  });
  it("folds the same error within 5 seconds", () => {
    const { r, tick } = setup();
    r.capture(err("same"));
    r.capture(err("same"));
    expect(r.pending()).toHaveLength(1);
    tick(6_000);
    r.capture(err("same"));
    expect(r.pending()).toHaveLength(2);
  });
  it("keeps at most 50, and none older than 7 days", () => {
    const { r, storage, tick } = setup();
    for (let i = 0; i < MAX_QUEUE + 5; i++) r.capture(err(`e${i}`));
    expect(r.pending()).toHaveLength(MAX_QUEUE);
    expect(r.pending()[0].message).toBe("Error: e5");
    expect(JSON.parse(storage.m.get(QUEUE_KEY)!)).toHaveLength(MAX_QUEUE);
    // a later page load drops what is too old
    tick(MAX_AGE_MS + 1);
    const later = new ErrorReporter({ storage, send: async () => ({ ok: true, enabled: true }), now: () => Date.parse("2026-11-06T09:00:00Z") + MAX_AGE_MS + 1, uuid: () => "u", origin: ORIGIN, platform: {}, setTimer: () => {}, clearTimer: () => {} });
    expect(later.pending()).toHaveLength(0);
  });
  it("sends nothing without the feature, and drops what it kept", async () => {
    const { r, sent, storage, runTimers } = setup();
    r.capture(err("before hello"));
    expect(storage.m.has(QUEUE_KEY)).toBe(true);
    await runTimers();
    expect(sent).toHaveLength(0);
    r.setFeatures(["terminal_stream"]);
    expect(r.pending()).toHaveLength(0);
    expect(storage.m.has(QUEUE_KEY)).toBe(false);
    r.capture(err("after"));
    await runTimers();
    expect(r.pending()).toHaveLength(0);
    expect(sent).toHaveLength(0);
  });
  it("sends in batches of 20 once the feature is on, and marks reports of an earlier load", async () => {
    const first = setup();
    for (let i = 0; i < 25; i++) first.r.capture(err(`e${i}`));
    // the next page load finds them
    const { r, sent, storage, runTimers } = setup({ storage: first.storage });
    r.setFeatures(["error_reports"]);
    await runTimers();
    expect(sent.map((b) => b.length)).toEqual([BATCH, 5]);
    expect(sent[0].every((rep) => rep.previous_launch)).toBe(true);
    expect(r.pending()).toHaveLength(0);
    expect(storage.m.has(QUEUE_KEY)).toBe(false);
  });
  it("keeps reports on a network failure or 5xx, drops them on a 4xx", async () => {
    const { r, sent, runTimers } = setup({}, [{ ok: false, status: 0 }, { ok: false, status: 503 }, { ok: false, status: 400 }]);
    r.setFeatures(["error_reports"]);
    r.capture(err("a"));
    await runTimers();
    expect(r.pending()).toHaveLength(1);
    await r.flush();
    expect(r.pending()).toHaveLength(1);
    await r.flush();
    expect(r.pending()).toHaveLength(0);
    expect(sent).toHaveLength(3);
  });
  it("stops when the hub says the feature is off", async () => {
    const { r, runTimers } = setup({}, [{ ok: true, enabled: false }]);
    r.setFeatures(["error_reports"]);
    r.capture(err("a"));
    await runTimers();
    expect(r.pending()).toHaveLength(0);
    r.capture(err("b"));
    expect(r.pending()).toHaveLength(0);
  });
  it("never throws, and does not report its own failures", async () => {
    const throwing = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("full"); }, removeItem: () => { throw new Error("x"); } };
    const { r, sent, runTimers } = setup({ storage: throwing, send: async () => { throw new Error("send failed"); } });
    r.setFeatures(["error_reports"]);
    expect(() => r.capture(err("a"))).not.toThrow();
    expect(() => r.capture({ get message(): string { throw new Error("getter"); } })).not.toThrow();
    expect(() => r.capture("a string")).not.toThrow();
    await runTimers();
    expect(sent).toHaveLength(0);
    // the error whose message throws is left out; the send that threw keeps the rest for later
    expect(r.pending().map((p) => p.message)).toEqual(["Error: a", "Error: a string"]);
  });
});

describe("the page's breadcrumbs", () => {
  it("note failed requests as method, path pattern and status, never their own", () => {
    const { r } = setup();
    setReporterForTest(r);
    navigationCrumb("session");
    requestCrumb("get", "/machines/box1/files?path=/home/me", 502);
    requestCrumb("POST", "/todos", 0);
    requestCrumb("POST", "/error-reports", 503);
    r.capture(err("x"));
    setReporterForTest(null);
    expect(r.pending()[0].breadcrumbs!.map((b) => [b.category, b.message, b.level])).toEqual([
      ["navigation", "session", "info"],
      ["http", "GET /machines/:id/files 502", "error"],
      ["http", "POST /todos network", "error"],
    ]);
  });
});
