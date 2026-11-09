// In-conversation search (docs/web/web.md "In-conversation search", Android ChatSearch.kt, parity N26): literal, case-insensitive
// matching over what is loaded of a chat (words, thoughts, tool names, tool input and output, system events), newest
// first, at most SEARCH_LIMIT hits; and where a hit sits in the chat's lines, for going to it.
import type { Item } from "./types";
import { parseUserText, sortItems, type ChatLine, type Step } from "./turns";
import type { WorkNode } from "./work-groups";

export type SearchCategory = "text" | "thinking" | "tool_name" | "tool_input" | "tool_output" | "event";

/** One match: the item, what of it matched, the text around the first match. */
export interface ChatSearchHit {
  itemId: string;
  category: SearchCategory;
  snippet: string;
  /** [start, end) offsets of every match in the snippet (UTF-16, as the DOM counts). */
  ranges: [number, number][];
}

export interface ChatSearchResults {
  hits: ChatSearchHit[];
  /** More matched than SEARCH_LIMIT. */
  truncated: boolean;
}

export const SEARCH_LIMIT = 100;
/** How much of the text shows before and after the first match. */
const BEFORE = 45;
const AFTER = 110;

/** Every string in a tool's input, in order (keys are not searched). */
function strings(v: unknown): string[] {
  if (typeof v === "string") return [v];
  if (Array.isArray(v)) return v.flatMap(strings);
  if (v && typeof v === "object") return Object.values(v).flatMap(strings);
  return [];
}

/** What of an item is searched, and as what. */
export function searchFields(item: Item): [SearchCategory, string][] {
  const out: [SearchCategory, string][] = [];
  const add = (c: SearchCategory, text?: string) => text?.trim() && out.push([c, text]);
  switch (item.kind) {
    case "text":
      // your messages: their words, not the tags the contexts and files travel in
      add("text", item.role === "user" ? parseUserText(item.text ?? "").text : item.text);
      break;
    case "thinking":
      add("thinking", item.text);
      break;
    case "tool_use":
      add("tool_input", item.text);
      add("tool_name", item.tool_name);
      add("tool_input", strings(item.tool_input).join("\n"));
      break;
    case "tool_result":
      add("tool_output", item.text);
      break;
    case "notice":
    case "recap":
      add("event", item.text);
      break;
    default:
      return out;
  }
  if (item.task) add("event", [item.task.title, item.task.detail, item.task.status].filter(Boolean).join("\n"));
  return out;
}

/** Case-insensitive indexOf that keeps the text's own offsets (a lowercase form of another length matches exactly). */
function finder(needle: string) {
  const low = needle.toLowerCase();
  return (text: string, from = 0): number => {
    const t = text.toLowerCase();
    return t.length === text.length && low.length === needle.length ? t.indexOf(low, from) : text.indexOf(needle, from);
  };
}

const isLow = (s: string, i: number) => {
  const c = s.charCodeAt(i);
  return c >= 0xdc00 && c <= 0xdfff;
};

export function searchChat(items: Item[], query: string): ChatSearchResults {
  const needle = query.trim();
  if (!needle) return { hits: [], truncated: false };
  const find = finder(needle);
  const seen = new Set<string>();
  const hits: ChatSearchHit[] = [];
  for (const item of sortItems(items).reverse()) {
    if (seen.has(item.id)) continue;
    seen.add(item.id);
    for (const [category, text] of searchFields(item)) {
      const first = find(text);
      if (first < 0) continue;
      if (hits.length === SEARCH_LIMIT) return { hits, truncated: true };
      let start = Math.max(0, first - BEFORE);
      let end = Math.min(text.length, first + needle.length + AFTER);
      if (start > 0 && isLow(text, start)) start--;
      if (end < text.length && isLow(text, end)) end++;
      const snippet = (start > 0 ? "…" : "") + text.slice(start, end).replace(/\s*\n\s*/g, " ") + (end < text.length ? "…" : "");
      const ranges: [number, number][] = [];
      for (let at = find(snippet); at >= 0; at = find(snippet, at + needle.length)) ranges.push([at, at + needle.length]);
      hits.push({ itemId: item.id, category, snippet, ranges });
    }
  }
  return { hits, truncated: false };
}

/** The hits by category, the categories in the order they first come. */
export function groupHits(hits: ChatSearchHit[]): [SearchCategory, ChatSearchHit[]][] {
  const groups = new Map<SearchCategory, ChatSearchHit[]>();
  for (const h of hits) groups.set(h.category, [...(groups.get(h.category) ?? []), h]);
  return [...groups];
}

/** The snippet cut at its matches: [text, matched] pieces. */
export function snippetParts(hit: ChatSearchHit): [string, boolean][] {
  const out: [string, boolean][] = [];
  let at = 0;
  for (const [a, b] of hit.ranges) {
    if (a > at) out.push([hit.snippet.slice(at, a), false]);
    out.push([hit.snippet.slice(a, b), true]);
    at = b;
  }
  if (at < hit.snippet.length) out.push([hit.snippet.slice(at), false]);
  return out;
}

/** The items a step draws (a tool call: its call, result and pictures). */
export function stepItemIds(step: Step): string[] {
  return step.kind === "tool" ? [step.use.id, ...(step.result ? [step.result.id] : []), ...step.images.map((i) => i.id)] : [step.item.id];
}

/** The items under work nodes, nested groups included. */
export function nodeItemIds(nodes: WorkNode[]): string[] {
  return nodes.flatMap((n) => (n.kind === "group" ? nodeItemIds(n.children) : stepItemIds(n.step)));
}

export function lineItemIds(line: ChatLine): string[] {
  switch (line.kind) {
    case "supervision":
      return line.lines.flatMap(lineItemIds);
    case "segment": {
      const s = line.seg;
      return [...(s.user ? [s.user.id, ...(s.user.rawInput ?? s.user.images).map((i) => i.id)] : []), ...s.steps.flatMap(stepItemIds), ...(s.answer ? [s.answer.id] : []), ...s.folds.map((f) => f.id)];
    }
    case "recaps":
      return line.items.map((i) => i.id);
    default:
      return [line.item.id];
  }
}

/** The line a hit is on: the one that draws it (a wake's runs are part of their exchange's line, #3783), else the line
 *  of the nearest item before it that a line draws (another block of your message, a notice folded into a compaction). */
export function lineOfItem(lines: ChatLine[], items: Item[], itemId: string): string | undefined {
  const owner = new Map<string, string>();
  const claim = (l: ChatLine) => lineItemIds(l).forEach((id) => owner.has(id) || owner.set(id, l.id));
  lines.forEach(claim);
  const direct = owner.get(itemId);
  if (direct) return direct;
  let last: string | undefined;
  for (const it of sortItems(items)) {
    if (it.id === itemId) return last;
    last = owner.get(it.id) ?? last;
  }
  return undefined;
}

/** How many pages one "Load earlier and keep searching" loads (Android: 20). */
export const SEARCH_PAGES = 20;

/** How a run of loading earlier history ended: `start` reached the start of the chat; the others say why it stopped. */
export type SearchLoadOutcome = "start" | "stopped" | "busy" | "load_failed" | "no_progress" | "page_cap";

/** What of the transcript the run looks at between pages. */
export interface SearchLoadState {
  items: Item[];
  hasMore: boolean;
  loading: boolean;
  loadingEarlier: boolean;
  error: string;
}

/** Pages back through the history for the search (Android loadEarlierForSearch): one page at a time, `frame` after
 *  each so it lands, until the start, `stopped()`, a page that brought nothing or failed, or `pages` pages. */
export async function loadEarlierForSearch(o: {
  get: () => SearchLoadState;
  load: () => Promise<void>;
  frame: () => Promise<void>;
  stopped: () => boolean;
  onPage: (n: number) => void;
  pages?: number;
}): Promise<SearchLoadOutcome> {
  const pages = o.pages ?? SEARCH_PAGES;
  for (let page = 0; page < pages; page++) {
    if (o.stopped()) return "stopped";
    const now = o.get();
    if (!now.hasMore) return "start";
    if (now.loading || now.loadingEarlier) return "busy";
    const firstSeq = now.items[0]?.seq ?? Infinity;
    await o.load();
    await o.frame();
    await o.frame();
    const after = o.get();
    // nothing older came: failed if an error stands (the same text as an earlier failure counts), else just nothing
    if ((after.items[0]?.seq ?? Infinity) >= firstSeq) return after.error ? "load_failed" : "no_progress";
    o.onPage(page + 1);
    if (!after.hasMore) return "start";
  }
  return "page_cap";
}

/** Where something is while the rows around it open or close: its y in the scrolled content, and what was found. */
export interface Located<T> {
  y: number;
  value: T;
}

/** Calls `locate` every frame until what it finds holds its y for `stillFrames` frames (the rows above it have stopped
 *  opening or closing), or `giveUpMs` passes. Gives what was found last; `missing` if nothing ever was; `cancelled` once
 *  `cancelled()` (another go started). `locate` may move the view each frame (the way back keeps its line in place). */
export async function untilStill<T>(o: {
  locate: (waited: number) => Located<T> | null;
  frame: () => Promise<void>;
  now: () => number;
  cancelled: () => boolean;
  giveUpMs: number;
  stillFrames?: number;
}): Promise<{ value: T } | "missing" | "cancelled"> {
  const need = o.stillFrames ?? 3;
  const started = o.now();
  let last: Located<T> | null = null;
  let still = 0;
  for (;;) {
    await o.frame();
    if (o.cancelled()) return "cancelled";
    const waited = o.now() - started;
    const at = o.locate(waited);
    if (at) {
      still = last && Math.abs(at.y - last.y) < 0.5 ? still + 1 : 0;
      last = at;
      if (still >= need) return { value: at.value };
    }
    if (waited >= o.giveUpMs) return last ? { value: last.value } : "missing";
  }
}
