// Code highlighting (highlight.js 11's common languages through lowlight), loaded the first time a reply has a fenced
// block with a language: the main bundle doesn't carry the grammars. Until it is there blocks show plain.
//
// Each code block colors itself (#2465, docs/web/web.md "Markdown cache and on-demand highlighting"): the reply's
// Markdown no longer runs rehype-highlight, so the grammars arriving draw again only the blocks near the viewport, not
// every reply that has code, and a reply's kept tree (markdown-cache.ts) does not depend on them. A block is near when it
// is within one height of its scroll container above or below what shows (one IntersectionObserver per container);
// the rest are colored as they come near. What a block draws is what rehype-highlight 7 draws: one lowlight with the
// common grammars, no guessing of unnamed languages, the `hljs` class first and `hljs-` token classes.
import { createContext, createElement, useContext, useLayoutEffect, useMemo, useState, type ReactNode, type RefObject } from "react";
import { flushSync } from "react-dom";
import type { ElementContent, Root } from "hast";
import { commonLowlight } from "../../lib/lowlight";
import { lazyPlugin } from "./lazy-plugin";

type Plugin = () => (tree: Root) => void;

/** Rollback switch: off highlights through rehype-highlight in every reply's Markdown again, as before #2465. */
export const codeHighlight = { inBlock: true };

/** rehype-highlight, for the switch's off position. */
const plugin = lazyPlugin<Plugin>(() => import("rehype-highlight").then((m) => m.default as unknown as Plugin));

type Lowlight = { highlight: (lang: string, text: string) => Root };

// The lowlight the code views use too (lib/lowlight.ts).
const grammars = lazyPlugin<Lowlight>(commonLowlight);

/** Loads the highlighter (once). */
export const loadHighlight = (): Promise<unknown> => (codeHighlight.inBlock ? grammars.load() : plugin.load());

/** A fenced block naming a language (```ts): only those are highlighted (no guessing). */
export const hasLanguageFence = (md: string) => /^[ \t>]*(?:`{3,}|~{3,})[ \t]*[\w+#.-]/m.test(md);

/** The rehype-highlight plugin once loaded, with the switch off; asks for it when `wanted`. */
export const useHighlight = plugin.use;

/** Asks for the grammars (when the page is idle) for a reply whose blocks highlight themselves. */
export const useAskHighlight = grammars.ask;

/** Whether the blocks of the Markdown around them are highlighted (its text names a language: hasLanguageFence). */
export const HighlightWanted = createContext(false);

/** What a highlighted <code> takes: rehype-highlight's classes, and its tokens unless lowlight had none. */
export type Highlighted = { className: string; children?: ReactNode[] };

// rehype-highlight 7's language(): no-highlight wins, then the first lang- / language- class.
function languageOf(classes: string[]): string | false | undefined {
  let name: string | undefined;
  for (const value of classes) {
    if (value === "no-highlight" || value === "nohighlight") return false;
    if (!name && value.slice(0, 5) === "lang-") name = value.slice(5);
    if (!name && value.slice(0, 9) === "language-") name = value.slice(9);
  }
  return name;
}

/** Whether a block with these classes is highlighted at all (a named language, not no-highlight). */
const wantsLanguage = (className: string | undefined) => !!className && !!languageOf(className.split(" "));

function toReact(nodes: ElementContent[]): ReactNode[] {
  return nodes.map((n, i) => {
    if (n.type === "text") return n.value;
    if (n.type !== "element") return null;
    const cls = n.properties?.className;
    return createElement(n.tagName, { key: i, className: Array.isArray(cls) ? cls.join(" ") : (cls as string | undefined) }, ...toReact(n.children));
  });
}

/** What rehype-highlight 7 makes of a <code className={className}> holding `text` inside a <pre>. Null: left as it is. */
export function highlightCode(lowlight: Lowlight, className: string | undefined, text: string): Highlighted | null {
  if (!className) return null;
  const classes = className.split(" ");
  const lang = languageOf(classes);
  if (!lang) return null;
  const out = (classes.includes("hljs") ? classes : ["hljs", ...classes]).join(" ");
  let tree: Root;
  try {
    tree = lowlight.highlight(lang, text);
  } catch (e) {
    // a language it has no grammar for: the class, the text plain
    if (e instanceof Error && /Unknown language/.test(e.message)) return { className: out };
    throw e;
  }
  return tree.children.length ? { className: out, children: toReact(tree.children as ElementContent[]) } : { className: out };
}

// ── Blocks near the viewport ──────────────────────────────

type Entry = { el: HTMLElement; show: () => void; near?: boolean; root?: Element | null; observer?: Observer };
type Observer = { io: IntersectionObserver; n: number; root: Element | null };

const noObserver = () => typeof IntersectionObserver === "undefined";
/** One height of the scroll container above and below what shows. */
const MARGIN = "100% 0px";

const watched = new Map<Element, Entry>();
const observers = new Map<Element | null, Observer>();
let pending: Entry[] = [];
let frame = false;

/** The boxes that scroll Markdown, marked so a block finds its box with `closest()`: reading each ancestor's computed
 *  style made the browser work out the styles of a whole long chat as it opened (#2905). The chat (`data-chat-scroll`),
 *  a folded message or recap (`data-fold-scroll`, views/chat/fold-scroll.tsx), and `data-scroll-box` on the other boxes
 *  in and over the chat that scroll Markdown (an open quote, a plan, the cards and notes over the composer, the recaps'
 *  dialog). A marked box always scrolls vertically, and a box that scrolls between a block and a marked one is marked
 *  too: the block would take the outer one. A block with no marked box (a file, the Inbox) walks up as before. */
const SCROLL_BOXES = "[data-chat-scroll],[data-fold-scroll],[data-scroll-box]";

/** An exchange of the chat, not drawn while it is far off screen (content-visibility, views/chat/SegmentView.tsx). */
const EXCHANGE = "section[data-segment]";

/** The nearest ancestor that scrolls vertically (null: the page): a marked one, else the first whose computed style
 *  scrolls, remembered per ancestor so a page's blocks walk up once. An ancestor that sets only `overflow-x` computes
 *  `overflow-y: auto` as well and is taken too: its blocks are then all near and colored at once, which is correct,
 *  just not lazy. None of the containers that hold Markdown does that. */
const rootOf = new WeakMap<Element, Element | null>();
function scrollRoot(el: Element): Element | null {
  const marked = el.parentElement?.closest(SCROLL_BOXES);
  if (marked) return marked;
  const path: Element[] = [];
  let root: Element | null = null;
  for (let p = el.parentElement; p && p !== document.body && p !== document.documentElement; p = p.parentElement) {
    const known = rootOf.get(p);
    if (known !== undefined && (known === null || known.isConnected)) {
      root = known;
      break;
    }
    const o = getComputedStyle(p).overflowY;
    if (o === "auto" || o === "scroll") {
      root = p;
      break;
    }
    path.push(p);
  }
  for (const p of path) rootOf.set(p, root);
  return root;
}

function observerFor(root: Element | null): Observer {
  let o = observers.get(root);
  if (!o) {
    o = { io: new IntersectionObserver(onIntersect, { root, rootMargin: MARGIN }), n: 0, root };
    observers.set(root, o);
  }
  return o;
}

function unwatch(e: Entry) {
  if (watched.get(e.el) !== e) return;
  watched.delete(e.el);
  const o = e.observer;
  if (!o) return;
  o.io.unobserve(e.el);
  if (--o.n === 0) {
    o.io.disconnect();
    observers.delete(o.root);
  }
}

/** Draws these blocks colored; `now`: before this frame is painted (they mounted with the grammars already in). Else in
 *  an ordinary update, not a transition: React draws every pending transition in one render, so the blocks in view
 *  waited for KaTeX to draw every reply with a formula as a long chat opened (#2905). They are a few blocks. */
function show(entries: Entry[], now = false) {
  if (!entries.length) return;
  entries.forEach(unwatch);
  if (now) flushSync(() => entries.forEach((e) => e.show()));
  else entries.forEach((e) => e.show());
}

function onIntersect(items: IntersectionObserverEntry[]) {
  const near: Entry[] = [];
  for (const it of items) {
    const e = watched.get(it.target);
    if (!e) continue;
    e.near = it.isIntersecting;
    if (e.near && grammars.loaded()) near.push(e);
  }
  show(near);
}

// The grammars arrived: only the blocks known to be near draw again; the others when they come near.
grammars.subscribe(() => show([...watched.values()].filter((e) => e.near)));

/** What one look at the blocks of a frame has read: each scroll box's edges, whether each exchange is near. */
type Looked = { boxes: Map<Element | null, { top: number; bottom: number }>; exchanges: Map<Element, boolean> };

/** Whether a box at `r` is within one height of `root`'s view above or below it. */
function within(r: DOMRect, root: Element | null, seen: Looked): boolean {
  if (!r.width && !r.height) return false; // not laid out (a hidden panel)
  let box = seen.boxes.get(root);
  if (!box) {
    const b = root ? root.getBoundingClientRect() : { top: 0, bottom: innerHeight };
    box = { top: b.top, bottom: b.bottom };
    seen.boxes.set(root, box);
  }
  const h = box.bottom - box.top;
  return r.bottom >= box.top - h && r.top <= box.bottom + h;
}

// A block's own position in an exchange the chat skips drawing makes the browser lay that exchange out (#2905); the
// exchange's box is there without it. A block the chat scrolls is not near when its exchange is not, so only the blocks
// of near exchanges are read, and the others are left to the observer. A block in a box of its own (an open quote, a
// fold) is near by where it is in that box, wherever the exchange is: it is read as before.
function isNear(e: Entry, seen: Looked): boolean {
  const root = e.root ?? null;
  const ex = e.el.closest(EXCHANGE);
  if (ex && scrollRoot(ex) === root) {
    let near = seen.exchanges.get(ex);
    if (near === undefined) seen.exchanges.set(ex, (near = within(ex.getBoundingClientRect(), root, seen)));
    if (!near) return false;
  }
  return within(e.el.getBoundingClientRect(), root, seen);
}

// Blocks mounted in the last commit, looked at once in the next frame before it is painted: by then the chat has moved
// to where it shows (its own layout effects), and their reads come together (styles, then positions, then the writes).
function checkPending() {
  frame = false;
  const batch = pending.filter((e) => watched.get(e.el) === e);
  pending = [];
  for (const e of batch) e.root = scrollRoot(e.el);
  const seen: Looked = { boxes: new Map(), exchanges: new Map() };
  const near = grammars.loaded() ? batch.filter((e) => isNear(e, seen)) : [];
  show(near, true);
  for (const e of batch) {
    if (watched.get(e.el) !== e) continue;
    const o = observerFor(e.root ?? null);
    o.n++;
    e.observer = o;
    o.io.observe(e.el);
  }
}

function watch(el: HTMLElement, onShow: () => void): () => void {
  const e: Entry = { el, show: onShow };
  watched.set(el, e);
  if (noObserver()) {
    // no way to tell what is near (tests): every block is; grammars that came in since the render are taken up now
    e.near = true;
    if (grammars.loaded()) {
      unwatch(e);
      onShow();
    }
  } else {
    pending.push(e);
    if (!frame) {
      frame = true;
      requestAnimationFrame(checkPending);
    }
  }
  return () => unwatch(e);
}

/**
 * A code block's highlighting: rehype-highlight's classes and tokens for its <code> (`className`, `text`) once the
 * grammars are in and the block (`pre`) is near the viewport; null until then, and for a block not to highlight.
 */
export function useBlockHighlight(pre: RefObject<HTMLElement | null>, className: string | undefined, text: string): Highlighted | null {
  const wanted = useContext(HighlightWanted) && wantsLanguage(className);
  // without an observer (tests, rendering to a string) a block mounted with the grammars in is colored at once
  const [shown, setShown] = useState(() => noObserver() && !!grammars.loaded());
  useLayoutEffect(() => {
    const el = pre.current;
    if (!wanted || shown || !el) return;
    return watch(el, () => setShown(true));
  }, [pre, wanted, shown]);
  const lowlight = shown ? grammars.loaded() : null;
  return useMemo(() => (wanted && lowlight ? highlightCode(lowlight, className, text) : null), [wanted, lowlight, className, text]);
}
