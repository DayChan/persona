// @vitest-environment jsdom
// Carried over from #1974 (by @tzh476): the idle pages preload when the browser is idle, the others as a link to them
// is reached, a failed load is asked again, and a chunk that comes in draws its page at once.
import { afterEach, describe, expect, it, vi } from "vitest";
import { startTransition, Suspense, type ComponentType } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { lazyPage, preloadPages } from "./lazy-page";

// Waits a few turns of the event loop for `el` to show `until`, and returns what it shows.
async function turns(el: Element, until: string) {
  for (let i = 0; i < 20 && el.innerHTML !== until; i++) await new Promise((r) => setTimeout(r, 0));
  return el.innerHTML;
}

describe("lazyPage", () => {
  afterEach(() => vi.useRealTimers());

  it("preloads only the idle pages once the browser is idle, and asks again for a chunk that failed", async () => {
    vi.useFakeTimers();
    const Page = () => <p>page</p>;
    const load = vi.fn<() => Promise<typeof Page>>().mockRejectedValueOnce(new TypeError("Failed to fetch dynamically imported module")).mockResolvedValue(Page);
    const other = vi.fn(() => Promise.resolve(Page));
    const Lazy = lazyPage(load, { idle: true });
    lazyPage(other, { routes: ["routines"] });
    preloadPages({}); // no requestIdleCallback (Safari): a timer
    expect(load).not.toHaveBeenCalled();
    await vi.runAllTimersAsync();
    expect(load).toHaveBeenCalledTimes(1);
    preloadPages({});
    await vi.runAllTimersAsync();
    expect(load).toHaveBeenCalledTimes(2); // the failed one again
    preloadPages({});
    await vi.runAllTimersAsync();
    expect(load).toHaveBeenCalledTimes(2); // the loaded one not
    expect(other).not.toHaveBeenCalled(); // a page with a route waits for a link to it
    // loaded: drawn at once, not through the fallback
    expect(renderToStaticMarkup(<Suspense fallback={<p>loading</p>}><Lazy /></Suspense>)).toBe("<p>page</p>");
  });

  it("loads a route's page once as the pointer, the focus or a finger reaches a link to it, and not with Data Saver", () => {
    const settings = vi.fn(() => Promise.resolve(() => null));
    const kb = vi.fn(() => Promise.resolve(() => null));
    lazyPage(settings, { routes: ["settings"] });
    lazyPage(kb, { routes: ["kb"] });
    document.body.innerHTML = `<a href="/settings"><span>Settings</span></a><a href="/persona">Persona</a><a href="https://example.com/settings">out</a><a href="/kb/notes">kb</a>`;
    const [link, main, away, notes] = document.querySelectorAll("a");
    const fire = (el: Element, type: string) => el.dispatchEvent(new Event(type, { bubbles: true }));
    const saver = preloadPages({ document, navigator: { connection: { saveData: true } } });
    fire(link.firstElementChild!, "pointerover");
    expect(settings).not.toHaveBeenCalled();
    saver();
    const stop = preloadPages({ document });
    fire(main, "pointerover"); // a page in the main bundle
    fire(away, "focusin"); // another site
    expect(settings).not.toHaveBeenCalled();
    fire(link.firstElementChild!, "pointerover");
    fire(link, "focusin");
    fire(link, "touchstart");
    expect(settings).toHaveBeenCalledTimes(1);
    stop();
    fire(notes, "pointerover"); // the listener is gone with the cancel
    expect(kb).not.toHaveBeenCalled();
  });

  it("waits for idle where there is requestIdleCallback, can be cancelled, and skips Data Saver", () => {
    const idle = vi.fn((run: () => void) => (void run, 7));
    const cancel = vi.fn();
    preloadPages({ requestIdleCallback: idle, cancelIdleCallback: cancel })();
    expect(idle).toHaveBeenCalledWith(expect.any(Function), { timeout: 5000 });
    expect(cancel).toHaveBeenCalledWith(7);
    idle.mockClear();
    preloadPages({ requestIdleCallback: idle, cancelIdleCallback: cancel, navigator: { connection: { saveData: true } } });
    expect(idle).not.toHaveBeenCalled();
  });

  it("shows the fallback while the chunk is on its way, and the page as soon as it is in", async () => {
    let arrive!: (p: ComponentType) => void;
    const Lazy = lazyPage(() => new Promise<ComponentType>((r) => (arrive = r)));
    const tree = <Suspense fallback={<p>loading</p>}><Lazy /></Suspense>;
    expect(renderToStaticMarkup(tree)).toBe("<p>loading</p>");
    const el = document.createElement("div");
    const root = createRoot(el);
    root.render(tree);
    expect(await turns(el, "<p>loading</p>")).toBe("<p>loading</p>");
    arrive(() => <p>page</p>);
    expect(await turns(el, "<p>page</p>")).toBe("<p>page</p>"); // not 300 ms later, when React would reveal a retry
    root.unmount();
  });

  it("draws a page whose chunk came in after the render that mounted it and before its commit", async () => {
    let arrive!: (p: ComponentType) => void;
    const Lazy = lazyPage(() => new Promise<ComponentType>((r) => (arrive = r)), { idle: true });
    preloadPages({ requestIdleCallback: (run) => (run(), 1), cancelIdleCallback: () => {} }); // on its way
    // A slow sibling makes React yield in the middle of a transition (Markdown, highlight and lazy-plugin mount in
    // one), after LazyPage drew nothing: the chunk comes in then, before the commit.
    let slow = true;
    const Slow = () => {
      if (slow) {
        slow = false;
        arrive(() => <p>page</p>);
        for (const end = performance.now() + 30; performance.now() < end; );
      }
      return <i />;
    };
    const el = document.createElement("div");
    const root = createRoot(el);
    startTransition(() => root.render(<Suspense fallback={<p>loading</p>}><Lazy /><Slow /></Suspense>));
    expect(await turns(el, "<p>page</p><i></i>")).toBe("<p>page</p><i></i>");
    root.unmount();
  });
});
