// One main conversation's newest page, for rendering before the hub answers. This is never a source of authority.
import { BASE } from "./base";
import type { Item } from "./types";

const PREFIX = "persona.feedCache.v1:";
export const FEED_CACHE_MAX_BYTES = 256 * 1024;
export const FEED_CACHE_MAX_ITEMS = 200;

export interface FeedCache {
  machineID: string;
  sessionID: string;
  manager?: string;
  items: Item[];
  hasMore: boolean;
  history?: string;
}

// This 64-bit FNV-1a fingerprint avoids copying the token into another storage key; it is not a security
// boundary, since persona.token already stores the credential in the same localStorage. feedCacheScope clears
// all cached feeds when the hub or account identity changes, so cache partitioning needs no cryptographic hash.
function fingerprint(value: string): string {
  let hash = 0xcbf29ce484222325n;
  for (const byte of new TextEncoder().encode(value)) hash = BigInt.asUintN(64, (hash ^ BigInt(byte)) * 0x100000001b3n);
  return hash.toString(16).padStart(16, "0");
}

/** Includes the proxy prefix: two hubs behind the same origin must not share their cached conversation. */
export function feedCacheKey(hub: string, token: string, deviceID = ""): string | null {
  if (!token) return null;
  // A saved device id can be stale after pasting a different jd_ token or pairing against an older hub.
  return `${PREFIX}${fingerprint(hub)}:${fingerprint(JSON.stringify([deviceID, token]))}`;
}

let generation = 0;
let identity = "";
let key: string | null = null;

/** Capture before starting a read. Its in-memory generation also fences logout followed by the same login. */
export function feedCacheScope(): string | null {
  try {
    const token = localStorage.getItem("persona.token") ?? "";
    const deviceID = localStorage.getItem("persona.tokenId") ?? "";
    const hub = location.origin + BASE;
    const now = JSON.stringify([hub, token, deviceID]);
    if (identity !== now) {
      if (identity) clearFeedCache();
      identity = now;
      key = feedCacheKey(hub, token, deviceID);
    }
    return key ? `${key}.${generation}` : null;
  } catch {
    return null;
  }
}

/** Sign-out and credential replacement remove every cached feed of this origin, including other proxy prefixes. */
export function clearFeedCache(): void {
  generation++;
  try {
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const stored = localStorage.key(i);
      if (stored?.startsWith(PREFIX)) localStorage.removeItem(stored);
    }
  } catch { /* Storage can be disabled or full; caching must never prevent signing out. */ }
}

const storageKey = (scope: string) => scope.slice(0, scope.lastIndexOf("."));

export function discardFeedCache(scope: string | null): void {
  if (!scope || scope !== feedCacheScope()) return;
  try { localStorage.removeItem(storageKey(scope)); } catch { /* Best effort. */ }
}

const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const strings = (value: Record<string, unknown>, fields: string[]) => fields.every((field) => value[field] === undefined || typeof value[field] === "string");
const numbers = (value: Record<string, unknown>, fields: string[]) => fields.every((field) => value[field] === undefined || (typeof value[field] === "number" && Number.isFinite(value[field])));
function validItem(value: unknown): value is Item {
  if (!object(value) || typeof value.id !== "string" || !value.id || !Number.isSafeInteger(value.seq)) return false;
  if (!["user", "assistant", "system"].includes(String(value.role))) return false;
  if (!["text", "thinking", "tool_use", "tool_result", "notice", "image", "fold", "recap"].includes(String(value.kind))) return false;
  if (!strings(value, ["text", "ts", "tool_use_id", "tool_name", "from", "client_msg_id"]) || !numbers(value, ["full_size"])) return false;
  if (["is_error", "queued", "wake"].some((field) => value[field] !== undefined && typeof value[field] !== "boolean")) return false;
  if (value.image !== undefined && (!object(value.image) || typeof value.image.mime !== "string"
    || !numbers(value.image, ["size", "width", "height"]) || (value.image.lazy !== undefined && typeof value.image.lazy !== "boolean"))) return false;
  if (value.tool_input !== undefined && !object(value.tool_input)) return false;
  if (value.compact !== undefined && (!object(value.compact) || !strings(value.compact, ["trigger"]) || !numbers(value.compact, ["pre_tokens"])
    || (value.compact.notes !== undefined && (!Array.isArray(value.compact.notes) || !value.compact.notes.every((note) => typeof note === "string"))))) return false;
  if (value.source !== undefined && (!object(value.source) || typeof value.source.kind !== "string"
    || !strings(value.source, ["conversation_id", "title"]))) return false;
  if (value.injected !== undefined && (!object(value.injected) || typeof value.injected.kind !== "string" || typeof value.injected.line !== "string"
    || !strings(value.injected, ["state", "title", "machine", "todo_id", "detail"]))) return false;
  if (value.task !== undefined && (!object(value.task) || typeof value.task.kind !== "string" || typeof value.task.status !== "string"
    || !strings(value.task, ["title", "task_id", "tool_use_id", "output_file", "detail"]) || !numbers(value.task, ["exit_code"]))) return false;
  if (value.fold !== undefined) {
    const fold = value.fold;
    if (!object(fold) || ![fold.items, fold.tools, fold.thoughts, fold.through_seq].every(Number.isFinite)) return false;
    if (!strings(fold, ["until", "through_id"])) return false;
    if (fold.images !== undefined && (!Array.isArray(fold.images) || !fold.images.every((image) => object(image)
      && strings(image, ["item_id", "tool_use_id", "tool", "source", "path", "machine", "mime"]) && numbers(image, ["size", "width", "height"])))) return false;
    if (fold.tasks !== undefined && (!Array.isArray(fold.tasks) || !fold.tasks.every((task) => object(task)
      && strings(task, ["replace", "source_item_id", "source_tool_use_id"]) && numbers(task, ["source_seq"])
      && Array.isArray(task.entries) && task.entries.every((entry) => object(entry) && typeof entry.id === "string"
        && strings(entry, ["subject", "description", "status"]))))) return false;
  }
  return true;
}

function validFeed(value: unknown): value is FeedCache {
  return object(value) && typeof value.machineID === "string" && !!value.machineID && typeof value.sessionID === "string" && !!value.sessionID
    && (value.manager === undefined || typeof value.manager === "string") && (value.history === undefined || typeof value.history === "string")
    && typeof value.hasMore === "boolean" && Array.isArray(value.items) && value.items.length <= FEED_CACHE_MAX_ITEMS && value.items.every(validItem);
}

// Keep lazy image metadata, including dimensions, so the normal image component can load bytes from the hub.
function withoutImageBodies(this: Record<string, unknown>, name: string, value: unknown): unknown {
  if (name === "image" && object(value)) {
    const { data: _data, ...metadata } = value;
    return { ...metadata, lazy: true };
  }
  if (name === "data" && (this.type === "base64" || String(this.mime ?? this.media_type ?? "").startsWith("image/"))) return undefined;
  if (typeof value === "string") return value.replace(/data:image\/[^\s;,]+;base64,[^\s"'<>)]*/gi, "[image omitted]");
  return value;
}

export function readFeedCache(scope: string | null = feedCacheScope()): FeedCache | null {
  if (!scope || scope !== feedCacheScope()) return null;
  try {
    const raw = localStorage.getItem(storageKey(scope));
    if (!raw) return null;
    if (new TextEncoder().encode(raw).length > FEED_CACHE_MAX_BYTES) return null;
    const parsed: unknown = JSON.parse(raw);
    if (!validFeed(parsed)) return null;
    // A record with embedded image bytes was not written by this version. Never hydrate an unsanitized record.
    if (JSON.stringify(parsed, withoutImageBodies) !== JSON.stringify(parsed) || containsCredential(raw)) {
      discardFeedCache(scope);
      return null;
    }
    return parsed;
  } catch {
    return null;
  }
}

function containsCredential(raw: string): boolean {
  const token = localStorage.getItem("persona.token") ?? "";
  return !!token && [token, encodeURIComponent(token), JSON.stringify(token).slice(1, -1)].some((value) => raw.includes(value));
}

/** Only the newest two turns, matching GET items?turns=2. Older pages and expanded folds belong only in memory. */
export function writeFeedCache(scope: string | null, value: FeedCache): boolean {
  if (!scope || scope !== feedCacheScope()) return false;
  try {
    let start = value.items.length, turns = 0;
    while (start > 0 && turns < 2) {
      const item = value.items[--start];
      if (item.role === "user" && item.kind === "text" && !item.queued) turns++;
    }
    while (start > 0 && value.items[start].seq === value.items[start - 1].seq) start--;
    const page: FeedCache = {
      machineID: value.machineID, sessionID: value.sessionID, manager: value.manager,
      items: value.items.slice(start), hasMore: value.hasMore || start > 0, history: value.history,
    };
    const raw = JSON.stringify(page, withoutImageBodies);
    if (!validFeed(page) || new TextEncoder().encode(raw).length > FEED_CACHE_MAX_BYTES || containsCredential(raw)) {
      discardFeedCache(scope);
      return false;
    }
    localStorage.setItem(storageKey(scope), raw);
    return true;
  } catch {
    discardFeedCache(scope);
    return false;
  }
}
