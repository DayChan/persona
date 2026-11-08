// Dev server previews (docs/web/preview.md, #150): the logic of the side panel that shows a session's dev server, which a
// headless browser on that computer draws (the node's preview.go). The pictures and messages it sends, the input
// the panel sends back, the servers it offers, and a picked element as a comment for the agent. Drawn by
// views/session/PreviewPanel.tsx.
import { BASE } from "./base";
import { enc, getToken } from "./api";
import { MAX_CODE_LINES, truncateLines, type ElementSpot } from "./comments";
import type { DraftQuote } from "./drafts";
import type { Port } from "./types";

/** The hub feature that says it has previews. */
export const PREVIEW_FEATURE = "preview";

/** A launch.json entry as `…/preview/servers` returns it. */
export interface PreviewServer {
  name: string;
  port?: number;
  url?: string;
  command?: string[];
  env?: Record<string, string>;
  cwd?: string;
  listening: boolean;
  title?: string;
  problem?: "no_name" | "no_port" | "not_local" | "no_run" | "bad_env" | string;
}

export interface PreviewServers {
  file?: string;
  servers: PreviewServer[];
  error?: string;
  /** The browser previews open in on that computer; absent when it has none. */
  browser?: string;
}

/** One server the panel offers: a launch.json entry, or a port an agent of the session listens on. */
export interface PreviewOption {
  key: string;
  name: string;
  port: number;
  /** Where it opens: a path on its server. */
  path: string;
  listening: boolean;
  title?: string;
  /** The entry's command: it can be started from the panel. */
  command?: string[];
  env?: Record<string, string>;
  cwd?: string;
  from: "launch" | "agent";
}

/** The servers to offer: the usable launch.json entries in the file's order, then the agents' ports none of them has. */
export function previewOptions(res: PreviewServers | null, ports: readonly Port[] = []): PreviewOption[] {
  const out: PreviewOption[] = [];
  for (const s of res?.servers ?? []) {
    if (s.problem || !s.port) continue;
    let path = "/";
    if (s.url) {
      try {
        const u = new URL(s.url);
        path = u.pathname + u.search + u.hash || "/";
      } catch {
        // the node checked it; a path it could not read opens at /
      }
    }
    // an agent's port is listening even when the probe missed it (still compiling)
    const listening = s.listening || ports.some((p) => p.port === s.port);
    out.push({ key: `launch:${s.name}`, name: s.name, port: s.port, path, listening, title: s.title, command: s.command, env: s.env, cwd: s.cwd, from: "launch" });
  }
  for (const p of ports) {
    if (out.some((o) => o.port === p.port)) continue;
    out.push({ key: `port:${p.port}`, name: p.title || `:${p.port}`, port: p.port, path: "/", listening: true, title: p.title, from: "agent" });
  }
  return out;
}

/** The entries launch.json has that cannot be used, for the panel to say why. */
export function previewProblems(res: PreviewServers | null): PreviewServer[] {
  return (res?.servers ?? []).filter((s) => s.problem);
}

/** The command line an entry runs, as the confirmation shows it (each word quoted as the node quotes it). */
export function commandLine(o: Pick<PreviewOption, "command" | "env">): string {
  const q = (s: string) => (/^[A-Za-z0-9_./-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`);
  const env = Object.keys(o.env ?? {})
    .sort()
    .map((k) => q(`${k}=${o.env![k]}`));
  return [...(env.length ? ["env", ...env] : []), ...(o.command ?? []).map(q)].join(" ");
}

/** The example the empty panel shows: what to put in .claude/launch.json. */
export const LAUNCH_EXAMPLE = `{
  "version": "0.0.1",
  "configurations": [
    {
      "name": "web",
      "runtimeExecutable": "npm",
      "runtimeArgs": ["run", "dev"],
      "port": 5173
    }
  ]
}`;

// ── the socket ────────────────────────────────────────

export interface PreviewSize {
  w: number;
  h: number;
  dpr: number;
  mobile: boolean;
}

/** A phone's viewport (CSS pixels): what "Phone" shows, scaled to the panel. */
export const PHONE_SIZE = { w: 390, h: 844 };

/** The preview's WebSocket address. A browser cannot set headers on one, so the token goes in the query, as the
 *  other sockets do. */
export function previewSocketURL(loc: { protocol: string; host: string }, mid: string, port: number, path: string, size: PreviewSize, token = getToken()): string {
  const scheme = loc.protocol === "https:" ? "wss" : "ws";
  const q = new URLSearchParams({ port: String(port), path: path || "/", w: String(Math.round(size.w)), h: String(Math.round(size.h)), dpr: String(size.dpr) });
  if (size.mobile) q.set("mobile", "1");
  if (token) q.set("token", token);
  return `${scheme}://${loc.host}${BASE}/api/v1/machines/${enc(mid)}/preview/connect?${q}`;
}

/** A binary message from the node: a picture of the page, or a screenshot. */
export type PreviewBinary = { kind: "frame"; seq: number; w: number; h: number; data: Uint8Array } | { kind: "shot"; id: number; data: Uint8Array };

export function parseBinary(buf: ArrayBuffer): PreviewBinary | null {
  const v = new DataView(buf);
  if (buf.byteLength < 5) return null;
  const tag = String.fromCharCode(v.getUint8(0));
  if (tag === "f" && buf.byteLength >= 9) return { kind: "frame", seq: v.getUint32(1), w: v.getUint16(5), h: v.getUint16(7), data: new Uint8Array(buf, 9) };
  if (tag === "s") return { kind: "shot", id: v.getUint32(1), data: new Uint8Array(buf, 5) };
  return null;
}

/** What the node says, as JSON. */
export type PreviewMessage =
  | { t: "ready"; browser: string; w: number; h: number }
  | { t: "page"; url: string; title: string; loading: boolean; back: boolean; forward: boolean }
  | { t: "external"; url: string }
  | { t: "failed"; url: string; error: string }
  | { t: "dialog"; kind: "alert" | "confirm" | "prompt" | "beforeunload"; message: string; default?: string }
  | { t: "dialog_closed" }
  | { t: "pick"; on: boolean }
  | ({ t: "picked" } & PickedElement)
  | { t: "pick_failed" }
  | { t: "shot_failed"; id: number; error: string }
  | { t: "copied"; text: string }
  | { t: "notice"; code: string }
  | { t: "crashed" }
  | { t: "closed"; code: string }
  | { t: "error"; code: string; error: string };

/** The element picked in the page, as the node describes it (preview_pick.js). */
export interface PickedElement {
  selector: string;
  tag: string;
  text: string;
  html: string;
  role?: string;
  label?: string;
  component?: string;
  source?: { file: string; line: number } | null;
  rect?: { x: number; y: number; w: number; h: number };
  url: string;
  title?: string;
}

// ── input ─────────────────────────────────────────────

/** The modifier keys as the browser on the computer counts them (CDP: Alt 1, Ctrl 2, Meta 4, Shift 8). */
export function modsOf(e: { altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }): number {
  return (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
}

/** The point under the pointer in the page's CSS pixels, from where it is on the picture (drawn at `rect`). */
export function toPage(x: number, y: number, rect: { left: number; top: number; width: number; height: number }, page: { w: number; h: number }) {
  const px = rect.width > 0 ? ((x - rect.left) * page.w) / rect.width : 0;
  const py = rect.height > 0 ? ((y - rect.top) * page.h) / rect.height : 0;
  return { x: Math.max(0, Math.min(page.w, Math.round(px))), y: Math.max(0, Math.min(page.h, Math.round(py))) };
}

/** The mouse button by its DOM number, as CDP names it. */
export function buttonName(b: number): "left" | "middle" | "right" | "back" | "forward" | "none" {
  return (["left", "middle", "right", "back", "forward"] as const)[b] ?? "none";
}

const isMac = () => typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

/** The page's own editing command a shortcut stands for (⌘A / Ctrl+A select all…), or null: a headless browser has no
 *  menu to take them from, and copying must reach this computer's clipboard. */
export function editCommand(e: { key: string; altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }, mac = isMac()): string | null {
  const mod = mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  if (!mod || e.altKey) return null;
  switch (e.key.toLowerCase()) {
    case "a":
      return e.shiftKey ? null : "selectAll";
    case "c":
      return e.shiftKey ? null : "copy";
    case "x":
      return e.shiftKey ? null : "cut";
    case "z":
      return e.shiftKey ? "redo" : "undo";
    case "y":
      return mac || e.shiftKey ? null : "redo";
  }
  return null;
}

/** A key pressed in the preview as the message for it; `text` when it types a character. Null for what is not sent
 *  as a key: an input method at work (its text comes when it is done), a modifier alone. */
export function keyMessage(
  e: { key: string; code: string; keyCode: number; location: number; repeat: boolean; isComposing: boolean; altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean },
  type: "down" | "up",
): Record<string, unknown> | null {
  if (e.isComposing || e.key === "Process" || e.key === "Dead" || e.key === "Unidentified") return null;
  const msg: Record<string, unknown> = { t: "key", type, key: e.key, code: e.code, key_code: e.keyCode, mods: modsOf(e), location: e.location };
  if (e.repeat) msg.repeat = true;
  if (type === "down") {
    const printable = [...e.key].length === 1 && !e.ctrlKey && !e.metaKey;
    if (printable) msg.text = e.key;
    else if (e.key === "Enter") msg.text = "\r";
  }
  return msg;
}

// ── addresses ─────────────────────────────────────────

/** What the address field shows for the page's URL: its path when it is on the server being previewed, else the
 *  whole address. */
export function addressOf(url: string, port: number): string {
  try {
    const u = new URL(url);
    if (u.port === String(port) && /^(localhost|127\.0\.0\.1|\[::1\])$/.test(u.hostname)) return u.pathname + u.search + u.hash;
    return url;
  } catch {
    return url;
  }
}

/** An address the page wanted to open elsewhere, when it may be opened in a new tab here (http / https only). */
export function safeExternal(url: string): string | null {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : null;
  } catch {
    return null;
  }
}

/** The address a localhost link of a reply stands for on the session's computer: its port and path; null for one that
 *  is not a port of that computer (docs/operations/ports.md "Recognizing links"). */
export function localhostLink(url: string): { port: number; path: string } | null {
  let u: URL;
  try {
    u = new URL(/^[a-z][a-z0-9+.-]*:\/\//i.test(url) ? url : `http://${url}`);
  } catch {
    return null;
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") return null;
  const host = u.hostname.toLowerCase();
  if (!(host === "localhost" || host.endsWith(".localhost") || host === "127.0.0.1" || host === "0.0.0.0" || host === "[::1]")) return null;
  const port = Number(u.port);
  if (!port) return null;
  return { port, path: u.pathname + u.search + u.hash || "/" };
}

// ── a picked element as a comment ─────────────────────

/** The element the user picked as a comment's quote: its HTML is the code (cut to MAX_CODE_LINES lines). */
export function elementQuote(el: PickedElement, at: { mid: string; sid: string; cwd: string; port: number }): DraftQuote {
  const lines = prettyHTML(el.html).split("\n");
  const cut = truncateLines(lines, MAX_CODE_LINES);
  const spot: ElementSpot = { url: el.url, selector: el.selector || el.tag, omitted: cut.omitted, mid: at.mid, sid: at.sid, cwd: at.cwd, port: at.port };
  if (el.component) spot.component = el.component;
  if (el.source?.file) spot.source = el.source.line ? `${relativeSource(el.source.file, at.cwd)}:${el.source.line}` : relativeSource(el.source.file, at.cwd);
  const text = el.text.trim();
  if (text) spot.text = text.length > 120 ? `${text.slice(0, 119)}…` : text;
  return { text: cut.lines.join("\n"), comment: { body: "", element: spot } };
}

/** A source path a dev build gave, relative to the session's folder when inside it (webpack's and vite's are absolute). */
function relativeSource(file: string, cwd: string): string {
  const f = file.replace(/^webpack:\/\/\/?(?:\.\/)?/, "").replace(/^file:\/\//, "");
  const root = cwd.replace(/\/+$/, "");
  return root && f.startsWith(root + "/") ? f.slice(root.length + 1) : f;
}

/** outerHTML with a line per tag when it came as one long line (most frameworks write it so), so 60 lines say more. */
export function prettyHTML(html: string): string {
  if (html.includes("\n") || html.length < 120) return html;
  return html.replace(/>\s*</g, ">\n<");
}
