// @vitest-environment jsdom
// Code highlighting stays off the first draw: a reply with code draws its blocks plain, the highlighter is asked for
// once the page is idle, and the same blocks near the viewport take its colors up when it arrives, without drawing the
// replies again (lazy-plugin.ts, highlight.ts, #2465).
import { act, Profiler, startTransition, Suspense, use, useState } from "react";
import { flushSync } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Markdown } from "../Markdown";
import { codeHighlight, loadHighlight } from "./highlight";
import { lazyPlugin } from "./lazy-plugin";
// highlight.js loaded while the file is collected, outside the test timeout: the lazy import below then finds it loaded
// instead of loading it inside the test, which the full parallel suite took past 5 s (#4955). What is checked is when
// Markdown asks for it, not how long the download takes.
import "rehype-highlight";
import "lowlight";

// How often a reply's Markdown draws (it asks for KaTeX once a draw).
const markdownDraws = vi.hoisted(() => ({ n: 0 }));
vi.mock("./math", async (orig) => {
  const real = await orig<typeof import("./math")>();
  return { ...real, useMath: (wanted: boolean) => (markdownDraws.n++, real.useMath(wanted)) };
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("a lazy plugin", () => {
  it("is asked for when the page is idle, not while the reply first draws", async () => {
    const idle: (() => void)[] = [];
    vi.stubGlobal("requestIdleCallback", (run: () => void) => idle.push(run));
    vi.stubGlobal("cancelIdleCallback", () => {});
    const importer = vi.fn(async () => "plugin");
    const lp = lazyPlugin(importer);
    function Probe({ wanted }: { wanted: boolean }) {
      return <span>{lp.use(wanted) ?? "plain"}</span>;
    }
    act(() => root.render(<Probe wanted />));
    expect(host.textContent).toBe("plain");
    expect(importer).not.toHaveBeenCalled();
    expect(idle).toHaveLength(1);
    await act(async () => {
      idle[0]();
      await lp.load();
    });
    expect(importer).toHaveBeenCalledTimes(1);
    expect(host.textContent).toBe("plugin");
  });

  it("is taken up in its transition only: a sync render of the reply before that commits still draws without it (#2905)", async () => {
    const lp = lazyPlugin(async () => "plugin");
    let bump = () => {};
    function Probe() {
      const [n, setN] = useState(0);
      bump = () => setN(n + 1);
      return <span>{lp.use(true) ?? "plain"}</span>;
    }
    act(() => root.render(<Probe />));
    await act(async () => {
      await lp.load();
      // a store's answer (path-presence) arriving before the transition commits: every waiting reply drawn in that frame
      flushSync(() => bump());
      expect(host.textContent).toBe("plain");
    });
    expect(host.textContent).toBe("plugin");
  });

  it("is not asked for by a reply that has no use for it", () => {
    const importer = vi.fn(async () => "plugin");
    const lp = lazyPlugin(importer);
    function Probe() {
      return <span>{lp.use(false) ?? "plain"}</span>;
    }
    act(() => root.render(<Probe />));
    expect(host.textContent).toBe("plain");
    expect(importer).not.toHaveBeenCalled();
  });
});

describe("Markdown's code before and after the highlighter", () => {
  it("draws blocks plain first, then colors only the same blocks near the viewport, the others as they come near", async () => {
    // An IntersectionObserver the test answers, frames it runs, and where each block is.
    const observers: { cb: IntersectionObserverCallback; root: Element | null; margin?: string; seen: Set<Element>; off: boolean }[] = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        o: (typeof observers)[number];
        constructor(cb: IntersectionObserverCallback, opts: IntersectionObserverInit) {
          this.o = { cb, root: (opts.root as Element) ?? null, margin: opts.rootMargin, seen: new Set(), off: false };
          observers.push(this.o);
        }
        observe = (el: Element) => this.o.seen.add(el);
        unobserve = (el: Element) => this.o.seen.delete(el);
        disconnect = () => (this.o.off = true);
      },
    );
    const report = (el: Element, near: boolean) => {
      const o = observers.find((x) => x.seen.has(el))!;
      o.cb([{ target: el, isIntersecting: near } as IntersectionObserverEntry], {} as IntersectionObserver);
    };
    let frames: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (run: FrameRequestCallback) => frames.push(run));
    const frame = () => {
      const run = frames;
      frames = [];
      run.forEach((f) => f(0));
    };
    const top = new Map<Element, number>();
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const y = this === host ? 0 : (top.get(this) ?? NaN);
      return Number.isNaN(y) ? new DOMRect() : new DOMRect(0, y, 100, this === host ? 500 : 50);
    });
    host.style.overflowY = "auto"; // the chat's scroller
    const commits = [0, 0, 0, 0];
    const blocks = ["```ts\nconst a = 1\n```", "```ts\nlet b = 2\n```", "```ts\nvar c = 3\n```", "no code"];
    const draw = (extra?: string) =>
      act(() =>
        root.render(
          <>
            {blocks.map((text, i) => (
              <Profiler key={i} id={String(i)} onRender={() => commits[i]++}>
                <Markdown text={text} />
              </Profiler>
            ))}
            {extra && <Markdown text={extra} />}
          </>,
        ),
      );
    draw();
    const pres = [...host.querySelectorAll("pre")];
    [0, 2000, 4000].forEach((y, i) => top.set(pres[i], y)); // the first near, the others two screens and more away
    act(frame);
    expect(observers).toHaveLength(1); // one for the scroller
    expect(observers[0]).toMatchObject({ root: host, margin: "100% 0px" });
    expect(observers[0].seen.size).toBe(3);
    act(() => pres.forEach((pre, i) => report(pre, i === 0)));
    expect(host.querySelector("[class*=hljs-]")).toBeNull();
    commits.fill(0);
    const drawn = markdownDraws.n;
    // the idle moment (a timer where there is no requestIdleCallback) asks for it
    await act(async () => {
      await new Promise((r) => setTimeout(r, 100));
      await loadHighlight();
    });
    // only the near block drew again: no reply, no far block
    expect(commits).toEqual([1, 0, 0, 0]);
    expect(markdownDraws.n).toBe(drawn);
    expect(pres[0].querySelector(".hljs-keyword")!.textContent).toBe("const");
    expect(pres[1].querySelector("[class*=hljs-]")).toBeNull();
    // the same block: nothing is mounted again, so nothing moves
    expect(host.querySelector("pre")).toBe(pres[0]);
    expect(pres[0].textContent).toBe("const a = 1\n");
    // a far block coming near
    act(() => report(pres[1], true));
    expect(pres[1].querySelector(".hljs-keyword")!.textContent).toBe("let");
    expect(commits).toEqual([1, 1, 0, 0]);
    expect(observers[0].seen).toEqual(new Set([pres[2]]));
    // a block mounted with the grammars in and near shows colored in its first frame, without waiting on the observer
    draw("```ts\nconst d = 4\n```");
    const added = host.querySelectorAll("pre")[3];
    top.set(added, 100);
    act(frame);
    expect(added.querySelector(".hljs-keyword")).not.toBeNull();
    expect(observers[0].seen.has(added)).toBe(false);
    // what it draws is what rehype-highlight drew
    const lit = added.closest(".prose-j")!.innerHTML;
    codeHighlight.inBlock = false;
    try {
      await loadHighlight();
      act(() => root.render(<Markdown text={"```ts\nconst d = 4\n```"} />));
      expect(host.querySelector(".prose-j")!.innerHTML).toBe(lit);
    } finally {
      codeHighlight.inBlock = true;
    }
    expect(observers[0].off).toBe(true); // the far block went: nothing left to watch
  });

  it("finds a block's scroller by its mark and reads no block the chat scrolls in an exchange far off screen (#2905)", async () => {
    await act(async () => void (await loadHighlight()));
    const observers: { root: Element | null; seen: Set<Element> }[] = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        o: (typeof observers)[number];
        constructor(_cb: IntersectionObserverCallback, opts: IntersectionObserverInit) {
          this.o = { root: (opts.root as Element) ?? null, seen: new Set() };
          observers.push(this.o);
        }
        observe = (el: Element) => this.o.seen.add(el);
        unobserve = (el: Element) => this.o.seen.delete(el);
        disconnect = () => {};
      },
    );
    let frames: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (run: FrameRequestCallback) => frames.push(run));
    // where each box is (top, height); what is not placed has no box
    const at = new Map<Element, [number, number]>();
    const read: Element[] = [];
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      read.push(this);
      const p = at.get(this);
      return p ? new DOMRect(0, p[0], 100, p[1]) : new DOMRect();
    });
    const styles = vi.spyOn(window, "getComputedStyle");
    const code = (s: string) => "```ts\n" + s + "\n```";
    act(() =>
      root.render(
        <div data-chat-scroll>
          <section data-segment>
            <Markdown text={code("const a = 1")} />
          </section>
          <section data-segment>
            <Markdown text={code("let b = 2")} />
          </section>
          <section data-segment>
            <div data-scroll-box>
              <Markdown text={code("var c = 3")} />
            </div>
          </section>
          <section data-segment>
            <div data-fold-scroll>
              <Markdown text={code("const d = 4")} />
            </div>
          </section>
        </div>,
      ),
    );
    const chat = host.querySelector("[data-chat-scroll]")!;
    const [near, far, inner, farFold] = [...host.querySelectorAll("section")];
    const box = host.querySelector("[data-scroll-box]")!;
    const fold = host.querySelector("[data-fold-scroll]")!;
    const pres = [...host.querySelectorAll("pre")];
    at.set(chat, [0, 500]);
    at.set(near, [0, 300]).set(pres[0], [50, 50]);
    at.set(far, [3000, 300]).set(pres[1], [3050, 50]); // an exchange six screens down, which the chat skips drawing
    at.set(inner, [300, 1000]).set(box, [300, 100]).set(pres[2], [900, 50]); // below what its own box shows
    at.set(farFold, [5000, 300]).set(fold, [5000, 100]).set(pres[3], [5010, 50]); // in view in its fold, far down
    styles.mockClear();
    act(() => {
      const run = frames;
      frames = [];
      run.forEach((f) => f(0));
    });
    expect(pres[0].querySelector(".hljs-keyword")!.textContent).toBe("const");
    expect(pres[1].querySelector("[class*=hljs-]")).toBeNull();
    expect(pres[2].querySelector("[class*=hljs-]")).toBeNull();
    // a block in a box of its own is near by its place in that box, as before: colored before that box comes into view
    expect(pres[3].querySelector(".hljs-keyword")!.textContent).toBe("const");
    expect(read).not.toContain(pres[1]); // its exchange's box said it is far
    expect(styles).not.toHaveBeenCalled(); // the marks, not the styles of every box up the page
    // the far block waits on the chat, the other on its own box
    expect(observers.map((o) => [o.root, [...o.seen]])).toEqual([
      [chat, [pres[1]]],
      [box, [pres[2]]],
    ]);
  });

  it("colors a block coming near without waiting on another transition (#2905)", async () => {
    await act(async () => void (await loadHighlight()));
    let report: IntersectionObserverCallback = () => {};
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        constructor(cb: IntersectionObserverCallback) {
          report = cb;
        }
        observe = () => {};
        unobserve = () => {};
        disconnect = () => {};
      },
    );
    let frames: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (run: FrameRequestCallback) => frames.push(run));
    // another transition that does not finish (KaTeX drawing every reply with a formula, as a long chat opens)
    let slow = () => {};
    const never = new Promise<never>(() => {});
    function Slow() {
      const [on, setOn] = useState(false);
      slow = () => startTransition(() => setOn(true));
      if (on) use(never);
      return null;
    }
    act(() =>
      root.render(
        <>
          <Suspense fallback={null}>
            <Slow />
          </Suspense>
          <Markdown text={"```ts\nconst a = 1\n```"} />
        </>,
      ),
    );
    act(() => {
      const run = frames;
      frames = [];
      run.forEach((f) => f(0));
    });
    const pre = host.querySelector("pre")!;
    expect(pre.querySelector("[class*=hljs-]")).toBeNull(); // not laid out: far
    await act(async () => {
      slow();
      report([{ target: pre, isIntersecting: true } as unknown as IntersectionObserverEntry], {} as IntersectionObserver);
    });
    expect(pre.querySelector(".hljs-keyword")?.textContent).toBe("const");
  });
});
