// Word-by-word fade-in for text that is still arriving (ported from cindy's rehypeStreamWordFade, #1963). Words,
// inline code (one segment each) and list markers share one timeline per message: a new segment starts 16 ms after the
// previous one, 4 ms once the queue is 96 ms behind, and never waits more than 160 ms. Segments keep a stable key
// across re-parses (matched by content), so a word that has already faded in never plays again; a remounted node
// resumes its animation with a negative delay. Code blocks and formulas appear at once.
//
// While streaming, settled segments keep their span (only the animation class goes, when the chunk is parsed again:
// nothing is written to the page as each word ends, FadeSpan.tsx); when the stream ends the message
// is rendered as plain Markdown again, without any of these spans. A block that fades in once (`plain`) writes its
// settled segments as plain text instead, so taking over a timeline whose words are mostly in (shownBefore: the stream
// it ends, or an earlier mount of the same block) costs about what plain Markdown does.
import type { Element, ElementContent, Root } from "hast";

type Kind = "text" | "inline-code";

interface Segment {
  kind: Kind;
  content: string;
}

interface KeyedSegment extends Segment {
  key: string;
}

export interface WordFadeTimeline {
  /** When each key starts (performance.now() ms). */
  startAt: Map<string, number>;
  /** Keys whose fade is over: rendered without the animation class. */
  settled: Set<string>;
  /** The earliest the next new segment may start; shared by every chunk of one message. */
  next: number;
  /** The clock (tests). */
  now?: () => number;
}

export interface WordFadeState {
  nextId: number;
  /** The last parse's segments in document order: what the next parse matches its keys against. */
  previous: KeyedSegment[];
  /** Keeps the keys of a message's chunks apart. */
  prefix: string;
  timeline: WordFadeTimeline;
  /** Each chunk's own matching state (by its start in the text), over the shared timeline. */
  chunks: Map<string, WordFadeState>;
}

/** The fade's length: --stream-fade in stream.css. */
export const FADE_MS = 150;
export const STEP_MS = 16;
/** Once a new segment would wait this long, the step shrinks to BACKLOG_STEP_MS. */
export const BACKLOG_MS = 96;
export const BACKLOG_STEP_MS = 4;
/** No segment waits longer than this: past it a burst fades in together instead of sitting invisible. */
export const MAX_WAIT_MS = 160;

const clock = (t: WordFadeTimeline) => (t.now ?? (() => performance.now()))();

export function createWordFadeState(prefix = "", timeline: WordFadeTimeline = { startAt: new Map(), settled: new Set(), next: 0 }): WordFadeState {
  return { nextId: 0, previous: [], prefix, timeline, chunks: new Map() };
}

/** The matching state of one chunk of a message (`start`: where the chunk starts in the text). */
export function chunkState(owner: WordFadeState, start: string, wholeDocument = false): WordFadeState {
  let s = owner.chunks.get(start);
  if (!s) {
    s = createWordFadeState(`${owner.prefix}c${start}-`, owner.timeline);
    owner.chunks.set(start, s);
  }
  // Back to one chunk (a second heading needs the whole document's context): it takes over what the others had
  // matched, so their words don't fade in again.
  if (wholeDocument && owner.chunks.size > 1) {
    s.previous = [...owner.chunks.entries()].sort(([a], [b]) => Number(a) - Number(b)).flatMap(([, c]) => c.previous);
    for (const k of owner.chunks.keys()) if (k !== start) owner.chunks.delete(k);
  }
  return s;
}

/**
 * Puts a segment on the timeline (when its node mounts) and writes its remaining delay into --wf-delay. A key seen
 * before keeps its start, so a remount resumes instead of replaying. Returns the delay.
 */
export function scheduleSegment(state: WordFadeState, key: string, el: HTMLElement): number {
  const t = state.timeline;
  const now = clock(t);
  let start = t.startAt.get(key);
  if (start === undefined) {
    start = Math.min(Math.max(t.next, now), now + MAX_WAIT_MS);
    t.startAt.set(key, start);
    t.next = start + (start - now < BACKLOG_MS ? STEP_MS : BACKLOG_STEP_MS);
  }
  const delay = Math.round(start - now);
  el.style.setProperty("--wf-delay", `${delay}ms`);
  return delay;
}

/** When the last scheduled segment will have faded in (ms from now; 0 when nothing is left). */
export function remainingMs(state: WordFadeState): number {
  let last = -Infinity;
  for (const s of state.timeline.startAt.values()) last = Math.max(last, s);
  return last === -Infinity ? 0 : Math.max(0, last + FADE_MS - clock(state.timeline));
}

// The timelines of what was on screen lately, by its text. A reply is drawn again from scratch when it moves in the
// chat (the stream ends and its item takes its place; an earlier answer of the segment the next reply goes into): the
// new mount takes over the old timeline, so the words already in don't fade again.
// One entry per timeline: a stream moves its entry along as it grows.
const shown = new Map<string, WordFadeState>();
const shownAs = new WeakMap<WordFadeState, string>();
const SHOWN_MAX = 64;

/** Remembers the timeline `text` is drawn on (a streaming reply as it grows, a block fading in once). */
export function rememberShown(text: string, state: WordFadeState): void {
  const key = text.trimEnd();
  if (!key || shown.get(key) === state) return;
  const prev = shownAs.get(state);
  if (prev !== undefined && shown.get(prev) === state) shown.delete(prev);
  shown.delete(key);
  shown.set(key, state);
  shownAs.set(state, key);
  if (shown.size > SHOWN_MAX) shown.delete(shown.keys().next().value!);
}

/**
 * The timeline of what `text` was drawn as last (the longest remembered text it starts with), and whether all of it is
 * in already (the same text, nothing left fading): then it is drawn plain at once.
 */
export function shownBefore(text: string): { state: WordFadeState; done: boolean } | null {
  const whole = text.trimEnd();
  let best: string | null = null;
  // at least half of it: a short reply that happens to start another one is not where that one was drawn
  for (const k of shown.keys()) if (k.length * 2 >= whole.length && text.startsWith(k) && (best === null || k.length > best.length)) best = k;
  if (best === null) return null;
  const state = shown.get(best)!;
  return { state, done: best === whole && remainingMs(state) === 0 };
}

/** Tests: forget every remembered timeline. */
export function forgetShown(): void {
  shown.clear();
}

const SKIP = new Set(["pre", "script", "style", "textarea"]);
const isKatex = (n: Element) => {
  const cls = n.properties?.className;
  return Array.isArray(cls) && cls.some((c) => typeof c === "string" && c.startsWith("katex"));
};

const segmenter = typeof Intl !== "undefined" && typeof Intl.Segmenter === "function" ? new Intl.Segmenter(undefined, { granularity: "word" }) : null;

// A growing tail is split again at every update; its earlier text nodes are the same strings, so they are cached.
const splitCache = new Map<string, string[]>();
const SPLIT_CACHE_MAX = 500;

/** Words, with the whitespace after each word kept on it (whitespace at the start stays on its own). */
export function splitWords(text: string): string[] {
  const hit = splitCache.get(text);
  if (hit) {
    splitCache.delete(text);
    splitCache.set(text, hit);
    return hit;
  }
  const out: string[] = [];
  const parts = segmenter ? Array.from(segmenter.segment(text), (s) => s.segment) : text.split(/(\s+)/).filter(Boolean);
  for (const s of parts) {
    if (!s.trim() && out.length) out[out.length - 1] += s;
    else out.push(s);
  }
  if (text.length < 20_000) {
    splitCache.set(text, out);
    if (splitCache.size > SPLIT_CACHE_MAX) splitCache.delete(splitCache.keys().next().value!);
  }
  return out;
}

interface TextSlot {
  kind: "text";
  parent: Root | Element;
  index: number;
  words: string[];
  first: number; // its first segment's index
}
interface CodeSlot {
  kind: "inline-code";
  parent: Root | Element;
  index: number;
  node: Element;
  first: number;
}
interface Marker {
  node: Element;
  first: number; // the item's first segment; == end: nothing in it yet
  end: number;
}
interface Collected {
  slots: (TextSlot | CodeSlot)[];
  segments: Segment[];
  markers: Marker[];
}

function textOf(n: Element): string {
  return n.children.map((c) => (c.type === "text" ? c.value : c.type === "element" ? textOf(c) : "")).join("");
}

function collect(node: Root | Element, out: Collected) {
  node.children.forEach((c, i) => {
    if (c.type === "element") {
      if (SKIP.has(c.tagName) || isKatex(c)) return;
      if (c.tagName === "code") {
        out.slots.push({ kind: "inline-code", parent: node, index: i, node: c, first: out.segments.length });
        out.segments.push({ kind: "inline-code", content: textOf(c) });
        return;
      }
      if (c.tagName === "li") {
        const m: Marker = { node: c, first: out.segments.length, end: 0 };
        out.markers.push(m);
        collect(c, out);
        m.end = out.segments.length;
        return;
      }
      collect(c, out);
      return;
    }
    if (c.type !== "text") return;
    const words = splitWords(c.value);
    if (!words.length || (words.length === 1 && !words[0].trim())) return; // newlines between blocks
    out.slots.push({ kind: "text", parent: node, index: i, words, first: out.segments.length });
    for (const w of words) if (w.trim()) out.segments.push({ kind: "text", content: w });
  });
}

/**
 * Stable keys: the same position with the same content (or text that grew: the old word is a prefix of the new one)
 * keeps its key; otherwise the last unused old segment with this content (text that moved); otherwise a new key.
 */
export function assignKeys(segments: Segment[], state: WordFadeState): string[] {
  const unused = new Set(state.previous.keys());
  const byContent = new Map<string, number[]>();
  const id = (s: Segment) => `${s.kind}\0${s.content}`;
  for (let i = state.previous.length - 1; i >= 0; i--) {
    const k = id(state.previous[i]);
    const list = byContent.get(k);
    if (list) list.push(i);
    else byContent.set(k, [i]);
  }
  const keys = segments.map((s, i) => {
    const p = state.previous[i];
    if (p && unused.has(i) && p.kind === s.kind && (p.content === s.content || (s.kind === "text" && p.content && s.content.startsWith(p.content)))) {
      unused.delete(i);
      return p.key;
    }
    const candidates = byContent.get(id(s));
    let j = candidates?.pop();
    while (j !== undefined && !unused.has(j)) j = candidates?.pop();
    if (j !== undefined) {
      unused.delete(j);
      return state.previous[j].key;
    }
    return `wf-${state.prefix}${state.nextId++}`;
  });
  state.previous = keys.map((key, i) => ({ ...segments[i], key }));
  return keys;
}

// The first render's delay, before the ref puts the segment on the shared timeline: where it already is, or an
// estimate from its place in this chunk (so it doesn't flash in before the ref corrects it).
function delayOf(key: string, state: WordFadeState, now: number, index: number): number {
  const start = state.timeline.startAt.get(key);
  if (start !== undefined) return Math.round(start - now);
  const raw = index * STEP_MS;
  return raw <= BACKLOG_MS ? raw : Math.min(MAX_WAIT_MS, BACKLOG_MS + (index - Math.floor(BACKLOG_MS / STEP_MS)) * BACKLOG_STEP_MS);
}

// No style here: FadeSpan drops --wf-delay on a keyed span (its ref owns it), and parsing a style for every word is
// most of what turning the tree into React elements costs.
function fadeNode(children: ElementContent[], key: string, state: WordFadeState): ElementContent {
  return {
    type: "element",
    tagName: "span",
    properties: { ...(state.timeline.settled.has(key) ? {} : { className: ["stream-word"] }), dataWfKey: key },
    children,
  };
}

function slotNodes(slot: TextSlot | CodeSlot, keys: string[], state: WordFadeState, plain: boolean): ElementContent[] {
  const settled = (key: string) => plain && state.timeline.settled.has(key);
  if (slot.kind === "inline-code") return [settled(keys[slot.first]) ? slot.node : fadeNode([slot.node], keys[slot.first], state)];
  const nodes: ElementContent[] = [];
  // settled words (with `plain`) and the whitespace between them run together into one text node
  let run = "";
  const flush = () => {
    if (run) nodes.push({ type: "text", value: run });
    run = "";
  };
  let n = slot.first;
  for (const w of slot.words) {
    if (!w.trim()) {
      run += w;
      continue;
    }
    if (settled(keys[n])) run += w;
    else {
      flush();
      nodes.push(fadeNode([{ type: "text", value: w }], keys[n], state));
    }
    n++;
  }
  flush();
  return nodes;
}

/**
 * The rehype plugin: wraps each word / inline code in a span.stream-word and marks list items whose dot fades in.
 * `plain`: segments already faded in stay plain text (a block fading in once; while streaming they keep their span).
 */
export function rehypeStreamWordFade(state: WordFadeState, plain = false) {
  return (tree: Root) => {
    const c: Collected = { slots: [], segments: [], markers: [] };
    collect(tree, c);
    if (!c.segments.length && !c.markers.length) return;
    const keys = assignKeys(c.segments, state);
    const now = clock(state.timeline);
    // A span remounted mid-fade may never get its animationend: once its time is up it counts as settled.
    for (const k of keys) {
      const start = state.timeline.startAt.get(k);
      if (start !== undefined && now - start >= FADE_MS) state.timeline.settled.add(k);
    }
    const nodes = c.slots.map((s) => slotNodes(s, keys, state, plain));
    // back to front: splicing a slot doesn't move the ones before it
    for (let i = c.slots.length - 1; i >= 0; i--) c.slots[i].parent.children.splice(c.slots[i].index, 1, ...nodes[i]);
    // A list item's dot fades in with its first word (at once while the item is still empty).
    for (const m of c.markers) {
      const key = m.end > m.first ? keys[m.first] : undefined;
      if (key && state.timeline.settled.has(key)) continue;
      m.node.properties = {
        ...m.node.properties,
        dataStreamMarker: true,
        ...(key ? { dataWfKey: key } : {}),
        style: `--wf-delay:${key ? delayOf(key, state, now, m.first) : 0}ms`,
      };
    }
  };
}
