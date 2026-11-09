// Pages a first screen doesn't need (App.tsx, #1996) are their own chunks; this loads them in the background once the
// browser is idle after start (#1954, carried over from #1974), so moving to one later is instant instead of a wait on
// the network. A page whose chunk is in renders at once, without a round through Suspense (React.lazy suspends once
// even for a module that is already loaded). A failed load (the hub was redeployed under the page, or it is offline)
// is not kept: the next render or preload asks again, and PageBoundary shows it in the meantime.
import { createElement, use, type ComponentType } from "react";

const loaders: (() => Promise<unknown>)[] = [];

/** A page from a chunk loaded on its first render or by preloadPages. */
export function lazyPage<P extends object>(load: () => Promise<ComponentType<P>>): ComponentType<P> {
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
  function LazyPage(props: P) {
    return createElement(page ?? use(once()), props);
  }
  return LazyPage;
}

/** Loads every lazy page's chunk now, for a test that draws without waiting (renderToStaticMarkup). */
export function loadPages(): Promise<unknown> {
  return Promise.all(loaders.map((l) => l()));
}

interface IdleWindow {
  requestIdleCallback?: (run: () => void, opts?: { timeout: number }) => number;
  cancelIdleCallback?: (id: number) => void;
  navigator?: { connection?: { saveData?: boolean } };
}

/**
 * Loads every lazy page's chunk once the browser is idle (at most 5 s after start; 2 s where there is no
 * requestIdleCallback, Safari). Not with Data Saver on: those pages then load when opened. Returns the cancel.
 */
export function preloadPages(w: IdleWindow = globalThis as IdleWindow): () => void {
  if (w.navigator?.connection?.saveData) return () => {};
  const run = () => loaders.forEach((l) => void l().catch(() => {}));
  if (w.requestIdleCallback && w.cancelIdleCallback) {
    const id = w.requestIdleCallback(run, { timeout: 5000 });
    return () => w.cancelIdleCallback!(id);
  }
  const t = setTimeout(run, 2000);
  return () => clearTimeout(t);
}
