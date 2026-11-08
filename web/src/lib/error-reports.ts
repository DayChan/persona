// The page's own errors, sent to the hub's error reports (docs/protocol/protocol.md "Error reports", docs/web/web.md "Error reports").
// Captured: uncaught errors, unhandled rejections and what the page boundaries catch. Sent only while the hub's hello
// `features` has `error_reports`; kept in localStorage until a 2xx (at most 50, at most 7 days); dropped as soon as a
// hello says the feature is off. Nothing the user wrote goes in: an error's name, message and stack (with URLs cut to
// their path), and breadcrumbs of code-chosen text (a route's name, a failed request's method, path pattern and status).
// Reporting never throws and never reports itself.

import type { ErrorBreadcrumb, ErrorException, ErrorFrame, ErrorLevel, ErrorReport } from "./extension-wire";

export const ERROR_REPORTS_FEATURE = "error_reports";
export const QUEUE_KEY = "persona.errorReports";
export const MAX_QUEUE = 50;
export const MAX_AGE_MS = 7 * 24 * 3600_000;
export const BATCH = 20;
export const MAX_BREADCRUMBS = 30;
export const FLUSH_DELAY_MS = 2_000;
export const DEDUPE_MS = 5_000;
const MAX_MESSAGE = 1024;
const MAX_FRAMES = 50;

// ── scrubbing ──────────────────────────────────────────

/** A URL (or a path) cut to its path: no scheme, host, query or fragment. Anything that isn't one stays as it is. */
export function urlPath(raw: string): string {
  const s = raw.trim();
  const m = /^[a-z][a-z0-9+.-]*:\/\/[^/?#\s]*([^?#\s]*)/i.exec(s);
  if (m) return m[1] || "/";
  return s.replace(/[?#].*$/s, "");
}

/** First-pass scrub of free text (an error message): URLs become their path, queries and fragments go, so do e-mail
 *  addresses. The hub scrubs again. */
export function scrubText(text: string): string {
  return text
    .replace(/\b[a-z][a-z0-9+.-]*:\/\/[^\s'"<>()]+/gi, (u) => urlPath(u))
    .replace(/(\/[^\s'"<>()?#]*)[?#][^\s'"<>()]*/g, "$1")
    .replace(/[\w.+-]+@[\w-]+(?:\.[\w-]+)+/g, "<email>");
}

const truncate = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + "…" : s);

// The words of the hub's API paths; any other segment (a machine, session, item id, a name) becomes `:id`.
const API_WORDS = new Set(
  ("accounts agents archive assistant attach auth auto-upgrade before bind binding blob blocked botmux bots browser bulk catalog " +
    "channels chats check claude-dirs close code comments computer config connectors contributions conversations convert create " +
    "current data days decisions deploy diff dirs discover dismiss doc doctor documents done draft enabled engine error-reports " +
    "events extensions file files find fix flags fork fresh fs full git health herdr history hub image import inbox info install " +
    "interactions interrupt items kb kb-search lark launchers log login logout looks machines mail managers markets memory messages " +
    "metrics missing model-config modes moments move mutes notify oauth owner pair pair-codes panes people personas pinned pins posts " +
    "prefs prepare presets preview providers purposes queue quick quota raw reactions read reconnect refresh reject rename replies " +
    "reply resolve restore retry rewind route run runs screen secrets seen send sessions settings show skills spaces split start " +
    "status step suggestion tabs task-plans tasks thread thumb today todos token tokens translate trash tree triage ui unarchive " +
    "update upgrade upgrades usage windows workspace-board workspaces worktree").split(" "),
);

/** An API path as a pattern: no query, every segment that isn't one of the API's own words `:id`. */
export function pathPattern(path: string): string {
  const p = urlPath(path).replace(/^\/api\/v1(?=\/|$)/, "");
  return p.split("/").map((seg) => (seg === "" || API_WORDS.has(seg) ? seg : ":id")).join("/") || "/";
}

// ── stacks ─────────────────────────────────────────────

// V8: "    at fn (url:1:2)", "    at url:1:2", "    at async fn (url:1:2)"
const V8 = /^\s*at (?:(.+?) \()?(.+?):(\d+):(\d+)\)?\s*$/;
// Firefox / Safari: "fn@url:1:2", "@url:1:2"
const GECKO = /^\s*(.*?)@(.+?):(\d+):(\d+)\s*$/;

function frameFile(url: string, origin: string): { file: string; inApp: boolean } {
  let sameOrigin = false;
  try {
    const u = new URL(url, origin || undefined);
    sameOrigin = !!origin && u.origin === origin;
  } catch {
    // not a URL: "<anonymous>", "native"
  }
  const file = urlPath(url);
  return { file, inApp: sameOrigin && /\/(?:assets|src)\//.test(file) };
}

/** An Error.stack as frames, innermost last (the order of ErrorException.frames). */
export function parseStack(stack: string | undefined, origin = pageOrigin()): ErrorFrame[] {
  if (!stack) return [];
  const frames: ErrorFrame[] = [];
  for (const line of stack.split("\n")) {
    const m = V8.exec(line) ?? (line.includes("@") ? GECKO.exec(line) : null);
    if (!m) continue;
    const [, fn, url, ln, col] = m;
    if (url.startsWith("eval at ")) continue;
    const { file, inApp } = frameFile(url, origin);
    const frame: ErrorFrame = { file, line: Number(ln), column: Number(col), in_app: inApp };
    const name = (fn ?? "").replace(/^async /, "").trim();
    if (name) frame.function = truncate(name, 200);
    frames.push(frame);
    if (frames.length >= MAX_FRAMES) break;
  }
  return frames.reverse();
}

/** A React component stack (ErrorInfo.componentStack) as frames, innermost last: one per component, with its file and
 *  position where the stack has them; a component or host element React gives no position (`at div (<anonymous>)`)
 *  keeps its name. */
export function componentFrames(stack: string | null | undefined, origin = pageOrigin()): ErrorFrame[] {
  if (!stack) return [];
  const frames: ErrorFrame[] = [];
  for (const line of stack.split("\n")) {
    if (!line.trim()) continue;
    const [frame] = parseStack(line, origin);
    const name = frame ? undefined : /^\s*(?:at )?([^\s@(]+)/.exec(line)?.[1];
    if (frame) frames.push(frame);
    else if (name) frames.push({ function: truncate(name, 200) });
    if (frames.length >= MAX_FRAMES) break;
  }
  return frames.reverse();
}

function pageOrigin(): string {
  try {
    return typeof location === "undefined" ? "" : location.origin;
  } catch {
    return "";
  }
}

// ── the platform ───────────────────────────────────────

/** The system coarsely: macos, windows, linux, ios, android, and its major version when the user agent says it. */
export function osOf(ua: string, platform?: string): { os?: string; os_version?: string } {
  const p = (platform ?? "").toLowerCase();
  let m: RegExpExecArray | null;
  if ((m = /Android (\d+)/.exec(ua)) || p === "android") return { os: "android", os_version: m?.[1] };
  if ((m = /(?:iPhone|iPad|iPod).*? OS (\d+)/.exec(ua)) || p === "ios") return { os: "ios", os_version: m?.[1] };
  if ((m = /Windows NT (\d+)/.exec(ua)) || p === "windows") return { os: "windows", os_version: m?.[1] };
  if ((m = /Mac OS X (\d+)/.exec(ua)) || p === "macos") return { os: "macos", os_version: m?.[1] };
  if (/Linux|X11|CrOS/.test(ua) || p === "linux" || p === "chrome os") return { os: "linux" };
  return {};
}

// ── the reporter ───────────────────────────────────────

export interface Stored {
  report: ErrorReport;
  /** It was kept by an earlier page load (sent with previous_launch). */
  earlier?: boolean;
}

/** What the POST answered: ok (2xx, with whether the feature is on), or a status (0: network). */
export type SendResult = { ok: true; enabled: boolean } | { ok: false; status: number };

export interface ReporterDeps {
  storage: Pick<Storage, "getItem" | "setItem" | "removeItem"> | null;
  send: (reports: ErrorReport[]) => Promise<SendResult>;
  now: () => number;
  uuid: () => string;
  origin: string;
  platform: { os?: string; os_version?: string };
  setTimer: (fn: () => void, ms: number) => unknown;
  clearTimer: (t: unknown) => void;
}

type Gate = "unknown" | "on" | "off";

export class ErrorReporter {
  private deps: ReporterDeps;
  private gate: Gate = "unknown";
  private queue: Stored[] = [];
  private crumbs: ErrorBreadcrumb[] = [];
  private recent = new Map<string, number>();
  private busy = false;
  private sending = false;
  private timer: unknown = null;

  constructor(deps: ReporterDeps) {
    this.deps = deps;
    this.queue = this.load().map((s) => ({ ...s, earlier: true }));
    this.save();
  }

  /** The hub's features from a hello: on sends what is kept, off drops it and keeps nothing more. */
  setFeatures(features: readonly string[]) {
    this.guard(() => {
      if (features.includes(ERROR_REPORTS_FEATURE)) {
        this.gate = "on";
        this.schedule(0);
      } else {
        this.off();
      }
    });
  }

  breadcrumb(category: string, message: string, level: "info" | "warning" | "error" = "info") {
    this.guard(() => {
      this.crumbs.push({ time: new Date(this.deps.now()).toISOString(), category, message: truncate(message, 200), level });
      if (this.crumbs.length > MAX_BREADCRUMBS) this.crumbs.splice(0, this.crumbs.length - MAX_BREADCRUMBS);
    });
  }

  /** Reports an error (anything thrown). Never throws; an error while reporting is not reported. `componentStack`:
   *  where in the component tree a boundary caught it (React's ErrorInfo). */
  capture(error: unknown, opts: { level?: ErrorLevel; mechanism?: string; componentStack?: string | null } = {}) {
    if (this.busy || this.gate === "off") return;
    this.guard(() => {
      const report = this.build(error, opts.level ?? "error", opts.mechanism, opts.componentStack);
      if (!report) return;
      const key = `${report.message}|${topFrame(report)}`;
      const now = this.deps.now();
      for (const [k, at] of this.recent) if (now - at > DEDUPE_MS) this.recent.delete(k);
      if (this.recent.has(key)) return;
      this.recent.set(key, now);
      this.queue.push({ report });
      this.trim();
      this.save();
      this.schedule(FLUSH_DELAY_MS);
    });
  }

  /** Sends what is kept, BATCH at a time, while the feature is on. */
  async flush(): Promise<void> {
    if (this.sending || this.gate !== "on") return;
    this.sending = true;
    try {
      while (this.gate === "on") {
        this.trim();
        const batch = this.queue.slice(0, BATCH);
        if (!batch.length) break;
        let res: SendResult;
        try {
          res = await this.deps.send(batch.map((s) => (s.earlier ? { ...s.report, previous_launch: true } : s.report)));
        } catch {
          res = { ok: false, status: 0 };
        }
        if (res.ok && !res.enabled) {
          this.off();
          break;
        }
        // 2xx: taken (or dropped by the hub, not to be sent again); 4xx other than 408 / 429: never takes them
        const done = res.ok || (res.status >= 400 && res.status < 500 && res.status !== 408 && res.status !== 429);
        if (!done) break; // network or 5xx: kept for the next hello or report
        const sent = new Set(batch.map((s) => s.report.id));
        this.queue = this.queue.filter((s) => !sent.has(s.report.id));
        this.save();
      }
    } finally {
      this.sending = false;
    }
  }

  /** For tests. */
  pending(): ErrorReport[] {
    return this.queue.map((s) => s.report);
  }

  private off() {
    this.gate = "off";
    this.queue = [];
    if (this.timer !== null) this.deps.clearTimer(this.timer);
    this.timer = null;
    try {
      this.deps.storage?.removeItem(QUEUE_KEY);
    } catch {
      // no storage
    }
  }

  private schedule(ms: number) {
    if (this.gate !== "on") return;
    if (this.timer !== null) this.deps.clearTimer(this.timer);
    this.timer = this.deps.setTimer(() => {
      this.timer = null;
      void this.flush().catch(() => {});
    }, ms);
  }

  private guard(fn: () => void) {
    if (this.busy) return;
    this.busy = true;
    try {
      fn();
    } catch {
      // reporting must never become an error of its own
    } finally {
      this.busy = false;
    }
  }

  private build(error: unknown, level: ErrorLevel, mechanism?: string, componentStack?: string | null): ErrorReport | null {
    let type: string;
    let value: string;
    let frames: ErrorFrame[] = [];
    if (error instanceof Error || (error && typeof error === "object" && "message" in error)) {
      const e = error as Error;
      type = typeof e.name === "string" && e.name ? e.name : "Error";
      value = typeof e.message === "string" ? e.message : String(e.message);
      frames = parseStack(typeof e.stack === "string" ? e.stack : undefined, this.deps.origin);
    } else {
      type = mechanism === "unhandledrejection" ? "UnhandledRejection" : "Error";
      value = typeof error === "string" ? error : error === undefined ? "undefined" : safeString(error);
    }
    type = truncate(scrubText(type), 200);
    value = truncate(scrubText(value), MAX_MESSAGE);
    const exception: ErrorException = { type, value };
    if (frames.length) exception.frames = frames;
    const exceptions = [exception];
    // where a boundary caught it, as the outer exception: the error stays the innermost cause the hub fingerprints by
    const components = componentFrames(componentStack, this.deps.origin);
    if (components.length) exceptions.unshift({ type: "ComponentStack", value: `caught by ${mechanism ?? "a boundary"}`, frames: components });
    const report: ErrorReport = {
      id: this.deps.uuid(),
      source: "web",
      kind: "exception",
      level,
      time: new Date(this.deps.now()).toISOString(),
      ...this.deps.platform,
      message: truncate(`${type}: ${value}`, MAX_MESSAGE),
      exceptions,
    };
    if (this.crumbs.length) report.breadcrumbs = this.crumbs.slice();
    if (mechanism) report.tags = { mechanism };
    return report;
  }

  private trim() {
    const cutoff = this.deps.now() - MAX_AGE_MS;
    this.queue = this.queue.filter((s) => Date.parse(s.report.time) >= cutoff);
    if (this.queue.length > MAX_QUEUE) this.queue = this.queue.slice(this.queue.length - MAX_QUEUE);
  }

  private load(): Stored[] {
    try {
      const raw = this.deps.storage?.getItem(QUEUE_KEY);
      const list: unknown = raw ? JSON.parse(raw) : [];
      if (!Array.isArray(list)) return [];
      const cutoff = this.deps.now() - MAX_AGE_MS;
      return list
        .filter((s): s is Stored => !!s && typeof s === "object" && !!(s as Stored).report?.id && typeof (s as Stored).report.time === "string")
        .filter((s) => Date.parse(s.report.time) >= cutoff)
        .slice(-MAX_QUEUE);
    } catch {
      return [];
    }
  }

  private save() {
    try {
      if (!this.deps.storage) return;
      if (this.queue.length) this.deps.storage.setItem(QUEUE_KEY, JSON.stringify(this.queue));
      else this.deps.storage.removeItem(QUEUE_KEY);
    } catch {
      // full or no storage: what is in memory is still sent
    }
  }
}

function topFrame(r: ErrorReport): string {
  const f = r.exceptions?.[0]?.frames?.at(-1);
  return f ? `${f.file}:${f.line}:${f.column}` : "";
}

function safeString(v: unknown): string {
  try {
    return String(v);
  } catch {
    return "?";
  }
}

// ── the page's reporter ────────────────────────────────

let reporter: ErrorReporter | null = null;

/** Breadcrumb: the page shown (a route's name, never its ids or query). */
export function navigationCrumb(name: string) {
  reporter?.breadcrumb("navigation", name);
}

/** Breadcrumb: a request to the hub failed (status 0: it did not get there). Its own reports are left out. */
export function requestCrumb(method: string, path: string, status: number) {
  if (!reporter) return;
  const pattern = pathPattern(path);
  if (pattern === "/error-reports") return;
  reporter.breadcrumb("http", `${method.toUpperCase()} ${pattern} ${status || "network"}`, status >= 500 || status === 0 ? "error" : "warning");
}

/** What an error boundary caught, with its component stack. A chunk that would not load (a redeployed hub) is a
 *  warning, a render crash fatal. */
export function reportBoundary(error: unknown, boundary: string, chunk: boolean, componentStack?: string | null) {
  reporter?.capture(error, { level: chunk ? "warning" : "fatal", mechanism: boundary, componentStack });
}

export function reportError(error: unknown, mechanism?: string) {
  reporter?.capture(error, { mechanism });
}

/** Starts reporting for this page: the window's error events, and the hub's features from the store. */
export function startErrorReports(opts: {
  send: ReporterDeps["send"];
  subscribe: (l: () => void) => unknown;
  features: () => { online: boolean; features: readonly string[] };
  uuid: () => string;
}) {
  if (reporter || typeof window === "undefined") return;
  let storage: ReporterDeps["storage"] = null;
  try {
    storage = window.localStorage;
  } catch {
    // storage blocked: keep in memory only
  }
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } };
  reporter = new ErrorReporter({
    storage,
    send: opts.send,
    now: Date.now,
    uuid: opts.uuid,
    origin: location.origin,
    platform: osOf(nav.userAgent ?? "", nav.userAgentData?.platform),
    setTimer: (fn, ms) => setTimeout(fn, ms),
    clearTimer: (t) => clearTimeout(t as ReturnType<typeof setTimeout>),
  });
  window.addEventListener("error", (e: ErrorEvent) => {
    // "Script error." with no error: another origin's script, nothing to say
    if (!e.error && /^Script error\.?$/.test(e.message ?? "")) return;
    reportError(e.error ?? { name: "Error", message: e.message ?? "", stack: e.filename ? `@${e.filename}:${e.lineno}:${e.colno}` : undefined }, "onerror");
  });
  window.addEventListener("unhandledrejection", (e: PromiseRejectionEvent) => {
    const r = e.reason;
    if (r instanceof DOMException && r.name === "AbortError") return;
    reportError(r, "unhandledrejection");
  });
  // The features a hello brings (each hello sets a new array): on or off from there on.
  let seen: readonly string[] | null = null;
  const check = () => {
    const { online, features } = opts.features();
    if (!online || features === seen) return;
    seen = features;
    reporter?.setFeatures(features);
  };
  opts.subscribe(check);
  check();
}

/** For tests: the page's reporter. */
export function setReporterForTest(r: ErrorReporter | null) {
  reporter = r;
}
