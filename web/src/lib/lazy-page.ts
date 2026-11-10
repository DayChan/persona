// Pages a first screen doesn't need (App.tsx, #1996) are their own chunks, loaded on their first render or ahead of it
// (preloadPages). Ahead of it in two ways, so only what is likely next is fetched: the few marked `idle` (the overlays a
// shortcut opens, small parts drawn inside content; about 29 KB gzip together) once the browser is idle after start
// (#1954, carried over from #1974), and every other page when the pointer, the focus or a finger reaches a link to one
// of its `routes`, so its chunk is on the way before the click. A page opened another way (a menu row, a shortcut) loads
// as it renders. A page whose chunk is in renders at once, without a round through Suspense (React.lazy suspends once
// even for a module that is already loaded); one still on its way shows the Suspense fallback, as with Data Saver, and
// is drawn the moment its chunk is in. A failed load (the hub was redeployed under the page, or it is offline) is not
// kept: the next render, preload or link asks again, and PageBoundary shows it in the meantime.
import { createElement, use, useEffect, useLayoutEffect, useReducer, useState, useSyncExternalStore, type ComponentType } from "react";
import { routeAt, type Route } from "./router";

type Loader = () => Promise<unknown>;

const loaders: Loader[] = [];
const idleLoaders: Loader[] = [];
const routeLoaders = new Map<Route["name"], Loader>();
const still = () => () => {};

export interface LazyPageOptions {
  /** Loaded once the browser is idle after start: an overlay a shortcut opens, a small part drawn inside content. */
  idle?: boolean;
  /** The routes it is the page of: a link to one loads it when the pointer, the focus or a finger reaches it. */
  routes?: Route["name"][];
}

/** A page from a chunk loaded on its first render, or by preloadPages as `opts` says. */
export function lazyPage<P extends object>(load: () => Promise<ComponentType<P>>, opts: LazyPageOptions = {}): ComponentType<P> {
  let page: ComponentType<P> | null = null;
  let loading: Promise<ComponentType<P>> | null = null;
  const once = () =>
    (loading ??= load().then(
      (p) => (page = p),
      (e) => {
        loading = null;
        throw e;
      },
    ));
  loaders.push(once);
  if (opts.idle) idleLoaders.push(once);
  for (const r of opts.routes ?? []) routeLoaders.set(r, once);
  function LazyPage(props: P) {
    // While the chunk is on its way it mounts first and suspends after (before the paint), so the chunk's arrival draws
    // the page as an update of its own, at once. Suspending on the first render would leave that to React's retry,
    // which it holds back until 300 ms after the fallback showed: a tap on a link waited that long for a chunk already
    // in. The server has no second render: there it suspends at once.
    const server = useSyncExternalStore(still, () => false, () => true);
    const [held, hold] = useState(false);
    const [, arrived] = useReducer((n: number) => n + 1, 0);
    const drawn = page !== null;
    useLayoutEffect(() => {
      if (!page) hold(true);
      // The chunk came in after the render that mounted it drew nothing and before its commit (React can yield in
      // between, in a transition or a Suspense retry): nothing else would draw it, and the fallback never showed.
      else if (!drawn) arrived();
    }, []);
    useEffect(() => {
      // not a layout effect: the fallback hiding this page runs those down, and this one has to see the chunk come in
      if (page) return;
      let live = true;
      once().then(() => live && arrived(), () => {});
      return () => {
        live = false;
      };
    }, []);
    if (page) return createElement(page, props);
    if (!held && !server) return null;
    return createElement(use(once()), props);
  }
  return LazyPage;
}

/** Loads every lazy page's chunk now, for a test that draws without waiting (renderToStaticMarkup). */
export function loadPages(): Promise<unknown> {
  return Promise.all(loaders.map((l) => l()));
}

interface IntentTarget {
  addEventListener(type: string, f: (e: Event) => void, opts: AddEventListenerOptions): void;
  removeEventListener(type: string, f: (e: Event) => void, opts: AddEventListenerOptions): void;
}

interface IdleWindow {
  requestIdleCallback?: (run: () => void, opts?: { timeout: number }) => number;
  cancelIdleCallback?: (id: number) => void;
  navigator?: { connection?: { saveData?: boolean } };
  document?: IntentTarget;
}

const INTENT = ["pointerover", "focusin", "touchstart"];
const LISTEN: AddEventListenerOptions = { capture: true, passive: true };
// The address each link was last looked up at: moving over a link again does no work.
const asked = new WeakMap<Element, string>();

function onIntent(e: Event) {
  const a = e.target instanceof Element ? e.target.closest("a[href]") : null;
  if (!(a instanceof HTMLAnchorElement) || asked.get(a) === a.href) return;
  asked.set(a, a.href);
  const r = routeAt(a.href);
  const load = r && routeLoaders.get(r.name);
  if (load) void load().catch(() => {});
}

/**
 * Loads the `idle` pages' chunks once the browser is idle (at most 5 s after start; 2 s where there is no
 * requestIdleCallback, Safari), and the others' as a link to them is reached (one listener on the document). Neither
 * with Data Saver on: those pages then load when opened. Returns the cancel.
 */
export function preloadPages(w: IdleWindow = globalThis as IdleWindow): () => void {
  if (w.navigator?.connection?.saveData) return () => {};
  const doc = w.document;
  for (const type of INTENT) doc?.addEventListener(type, onIntent, LISTEN);
  const unlisten = () => INTENT.forEach((type) => doc?.removeEventListener(type, onIntent, LISTEN));
  const run = () => idleLoaders.forEach((l) => void l().catch(() => {}));
  if (w.requestIdleCallback && w.cancelIdleCallback) {
    const id = w.requestIdleCallback(run, { timeout: 5000 });
    return () => {
      w.cancelIdleCallback!(id);
      unlisten();
    };
  }
  const t = setTimeout(run, 2000);
  return () => {
    clearTimeout(t);
    unlisten();
  };
}
