import { storedCredentials, storeCredentials, CLIENT_HEADERS, OP_HEADERS, compatibleEnvelope, requestForHub } from "./compatibility";
// REST client for the hub. The page is served by the hub itself (same origin), so every path is relative.

import { MANAGER_HEADER, managerHeaderFor } from "./manager-choice";
import { BASE } from "./base";
import { requestCrumb } from "./error-reports";
import { currentLanguage, t } from "../i18n";
import type { Item } from "./types";
import { clearFeedCache } from "./feed-cache";

let ownerToken: string | undefined;
let ownerGeneration = 0;
const credentialListeners = new Set<(owner: string) => void>();
/** Opaque in-memory auth partition; never expose credentials in tree keys or persisted drafts. */
export function credentialOwner(): string {
  const token = getToken();
  if (token !== ownerToken) {
    ownerToken = token;
    ownerGeneration++;
    credentialListeners.forEach((listener) => listener(`credential-${ownerGeneration}`));
  }
  return `credential-${ownerGeneration}`;
}

/** Retire work when pairing or sign-out replaces its credential, including a return to the same token. */
export function onCredentialChange(listener: (owner: string) => void): () => void {
  credentialListeners.add(listener);
  return () => { credentialListeners.delete(listener); };
}

/** React editors follow the shared credential subscription and other tabs' storage changes. */
export function subscribeCredentialOwner(listener: () => void): () => void {
  const stop = onCredentialChange(listener);
  const storage = () => { credentialOwner(); };
  window.addEventListener("storage", storage);
  return () => { stop(); window.removeEventListener("storage", storage); };
}

export function getToken(): string {
  return storedCredentials().token;
}

export function setToken(token: string, tokenId?: string) {
  if (token !== getToken() || !token) clearFeedCache();
  storeCredentials(token, tokenId);
  credentialOwner();
}

export class ApiError extends Error {
  status: number;
  body: Record<string, unknown>;
  isJson: boolean;
  /** The hub logs a failed request under this id (X-Request-Id): for a report, not for the message itself. */
  requestId?: string;
  constructor(status: number, message: string, body: Record<string, unknown>, isJson = true, requestId?: string) {
    super(message);
    this.status = status;
    this.body = body;
    this.isJson = isJson;
    this.requestId = requestId;
  }
  get code(): string | undefined {
    return typeof this.body.code === "string" ? this.body.code : undefined;
  }
}

let onUnauthorized: () => void = () => {};
export function handleUnauthorized(fn: () => void) {
  onUnauthorized = fn;
}

/**
 * Who this page is to the hub: the platform and version its socket's client_hello names, and every request's
 * X-Persona-Client. The hub answers a request with what this page's socket declared (MCP forms and links: only to a
 * request whose platform and version match a connection that declared them, docs/protocol/protocol.md "MCP elicitation").
 */
export const CLIENT_PLATFORM = "web";
export const CLIENT_VERSION = "1";
/** What this page can render beyond the basics (client_hello `features`). */
/** `managers`: the hub pushes {"type":"managers"} when its roster changes (Settings › Managers, lib/managers.ts). */
export const CLIENT_FEATURES = ["elicitation", "extension_operations", "extension_auth", "managers", "extension_ui_stream_v1", "persona_contracts_v1"];

// A read the hub has not answered by then fails instead of loading forever (#4492): a restarting hub, a stuck node or a
// half-dead network. Longer than the hub's own cut-offs for asking a computer (30–40 s), so when the hub has an error
// that says which computer, that one arrives first. Writes have none: one that took effect must not read as failed.
export const READ_TIMEOUT_MS = 45_000;

/** The `timeout` for a read the hub itself gives `hubMs` or longer (a node that starts a CLI, a gateway): a few seconds
 *  past the hub's own limit, so the hub's error (which computer, which launcher) or a late answer comes first (#5035). */
export const pastHub = (hubMs: number): number => hubMs + 5_000;

/** The hub did not answer a read within its time (`api`'s `timeout`). */
export class HubTimeoutError extends Error {
  constructor() {
    super(t("lib-api.hub_no_response"));
    this.name = "HubTimeoutError";
  }
}

type ApiOptions = RequestInit & { json?: unknown; token?: string; tokenOwner?: string; onResponse?: (headers: Headers) => void };

/** `tokenOwner`: explicit credentials opt into 401 recovery only while both the token and credential generation are current.
 *  `timeout`: ms before a read gives up with HubTimeoutError (default READ_TIMEOUT_MS for GET, none otherwise; 0: none). */
export async function api<T>(path: string, init: ApiOptions & { timeout?: number } = {}): Promise<T> {
  const { timeout, ...rest } = init;
  const limit = timeout ?? ((init.method ?? "GET").toUpperCase() === "GET" ? READ_TIMEOUT_MS : 0);
  if (!limit) return request<T>(path, rest);
  // The caller's own signal still aborts as before (an AbortError); only the time running out becomes HubTimeoutError.
  const controller = new AbortController();
  const outer = init.signal;
  const forward = () => controller.abort(outer?.reason);
  if (outer?.aborted) forward();
  else outer?.addEventListener("abort", forward, { once: true });
  let late = false;
  const timer = setTimeout(() => {
    late = true;
    controller.abort();
  }, limit);
  try {
    return await request<T>(path, { ...rest, signal: controller.signal });
  } catch (e) {
    throw late && isAbort(e) ? new HubTimeoutError() : e;
  } finally {
    clearTimeout(timer);
    outer?.removeEventListener("abort", forward);
  }
}

async function request<T>(path: string, init: ApiOptions): Promise<T> {
  const { onResponse, ...fetchOptions } = init;
  const owner = init.token === undefined ? credentialOwner() : init.tokenOwner;
  const headers = new Headers(init.headers);
  headers.set("Authorization", `Bearer ${init.token ?? getToken()}`);
  // the hub writes its error and status text in the interface language (docs/protocol/i18n.md "Protocol")
  if (!headers.has("Accept-Language")) headers.set("Accept-Language", currentLanguage());
  CLIENT_HEADERS.forEach(name => headers.set(name, `${CLIENT_PLATFORM}/${CLIENT_VERSION}`));
  const op = OP_HEADERS.map(name => headers.get(name)).find(Boolean);
  if (op) OP_HEADERS.forEach(name => headers.set(name, op));
  // the manager the assistant page shows, on the assistant's requests (lib/manager-choice.ts); none for the default
  const manager = managerHeaderFor(path);
  if (manager && !headers.has(MANAGER_HEADER)) headers.set(MANAGER_HEADER, manager);
  let body = init.body;
  if (init.json !== undefined) {
    headers.set("Content-Type", "application/json");
    body = JSON.stringify(requestForHub(path, init.json));
  }
  const method = init.method ?? "GET";
  let res: Response;
  try {
    res = await fetch(`${BASE}/api/v1${path}`, { ...fetchOptions, headers, body });
  } catch (e) {
    // a breadcrumb for an error report: the method, the path's pattern (no ids, no query) and that it did not get there
    if (!isAbort(e)) requestCrumb(method, path, 0);
    throw e;
  }
  const text = await res.text();
  let data: Record<string, unknown> = {};
  let isJson = false;
  try {
    data = text ? JSON.parse(text) : {};
    isJson = typeof data === "object" && data !== null && !Array.isArray(data);
  } catch {
    // a proxy's HTML error page says nothing a person can use: the status stands in for it
    data = { error: /^\s*</.test(text) ? "" : text };
    isJson = false;
  }
  if (!res.ok) {
    requestCrumb(method, path, res.status);
    if (res.status === 401 && owner !== undefined && owner === credentialOwner() && (init.token === undefined || init.token === getToken())) onUnauthorized();
    const msg = typeof data.error === "string" && data.error ? data.error : `HTTP ${res.status}`;
    throw new ApiError(res.status, msg, data, isJson, res.headers?.get("X-Request-Id") ?? undefined);
  }
  onResponse?.(res.headers);
  return compatibleEnvelope(data) as T;
}

export const enc = encodeURIComponent;

export function sessionPath(mid: string, sid: string) {
  return `/sessions/${enc(mid)}/${enc(sid)}`;
}

export function attachSession(mid: string, sid: string): Promise<unknown> {
  return api(`${sessionPath(mid, sid)}/attach`, { method: "POST" });
}

// An <img> can't send a header: the hub also takes the token as a query parameter (it never logs queries).
export function imageURL(mid: string, sid: string, itemID: string) {
  return `${BASE}/api/v1${sessionPath(mid, sid)}/items/${enc(itemID)}/image?token=${enc(getToken())}`;
}

/** An item whole, when the page carries it cut (Item.full_size): `GET …/items/{id}/full`. */
export function fullItem(mid: string, sid: string, itemID: string): Promise<Item> {
  return api<Item>(`${sessionPath(mid, sid)}/items/${enc(itemID)}/full`);
}

/** A machine file's bytes by Range (docs/protocol/protocol.md "File preview"): `<video>` / `<audio>` seek through it, a viewer
 * fetches it whole. `inline` lets the browser show it in place (images, media, PDF only; anything else downloads). */
export function fileDownloadURL(mid: string, path: string, cwd?: string, opts: { inline?: boolean } = {}) {
  return `${BASE}/api/v1/machines/${enc(mid)}/file/download?path=${enc(path)}&cwd=${enc(cwd ?? "")}${opts.inline ? "&inline=1" : ""}&token=${enc(getToken())}`;
}

// Fresh legacy URLs must miss the browser cache after this client upgrades. A page-load identity also retries
// after the node upgrades later; the same URL remains reusable while this page is open.
const thumbnailCacheEpoch = `v2-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;

/** A machine file's thumbnail, at most `size` px on its long side; 404 `no_thumbnail` when the machine can't draw it. */
export function fileThumbURL(mid: string, path: string, cwd?: string, size = 256) {
  return `${BASE}/api/v1/machines/${enc(mid)}/file/thumb?path=${enc(path)}&cwd=${enc(cwd ?? "")}&size=${size}&token=${enc(getToken())}&thumb=${thumbnailCacheEpoch}`;
}

export function isAbort(e: unknown) {
  return e instanceof DOMException && e.name === "AbortError";
}

/** The hub answered for a computer that did not (docs/protocol/protocol.md "Error body"): what became of the computer and
 *  what to do next, by code, not "the hub can't respond". `delivered: none` marks the plainly offline one, where nothing
 *  went out. The hub's own words stay on the ApiError (`message`, `body.detail`). */
function computerFailureText(e: ApiError): string | undefined {
  if (e.code === "node_unreachable") return t(e.body.delivered === "none" ? "lib-api.computer_offline" : "lib-api.computer_dropped");
  if (e.code === "timeout") return t("lib-api.computer_timeout");
  return undefined;
}

export function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    const computer = computerFailureText(e);
    if (computer) return computer;
    // Keep both the status and the original reason for diagnostics and existing error-specific explanations.
    const status = `HTTP ${e.status}`;
    const detail = e.message && e.message !== status ? `${status}: ${e.message}` : status;
    if (e.status === 404 || e.code === "not_found" || /^not[ _]found$/i.test(e.message.trim())) {
      return t("lib-api.not_found", { detail });
    }
    if (e.status === 502 || e.status === 503 || e.status === 504) return t("lib-api.hub_unavailable", { detail });
    if (e.status >= 500 && e.status < 600) return t("lib-api.server_error", { detail });
    return e.message;
  }
  if (e instanceof TypeError) return t("lib-api.cant_reach_hub");
  return e instanceof Error ? e.message : String(e);
}
