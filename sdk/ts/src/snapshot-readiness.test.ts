import { expect, test } from "bun:test";
import { componentPage, PageBridge, componentSnapshot } from "./page-bridge.ts";
import { connect, type Hub, type UIRequest } from "./index.ts";
import { createElement, View, Section, Text, Button, callback } from "./components.ts";
import { useEffect, useLoadEffect, useState, useFetch } from "./hooks.ts";
import { mount, type Snapshot, type UIEvent, type RendererOptions } from "./renderer.ts";

test("snapshot readiness requires both the bundled contract and a callable method", async () => {
  for (const missing of ["contract", "method"] as const) {
    const h = fixture();
    const page = { ...h.page, snapshotReadiness: missing === "contract" ? undefined : 1 as const,
      createRenderer: (host: Parameters<typeof h.page.createRenderer>[0]) => {
        const renderer = h.page.createRenderer(host);
        Object.defineProperty(renderer, "settle", { value: missing === "method" ? undefined : () => { throw new Error("not opted in"); } });
        return renderer;
      } };
    const bridge = new PageBridge({ home: page }, { notify() {} } as unknown as Hub);
    try {
      const first = await bridge.call("mount", { session: "compat", transport: "snapshot", params: { contribution: "home" } }) as Snapshot;
      expect(title(first)).toBe("loading");
      await bridge.call("suspend", { session: "compat" });
      expect(title(await bridge.call("resume", { session: "compat" }) as Snapshot)).toBe("loading");
    } finally { bridge.close(); h.bridge.close(); }
  }
});

test("queued scheduler and synchronous dependent finite loads reach 3 within the original 80 ms bound", async () => {
  const queued: (() => void)[] = []; let loads = 0;
  function Root() {
    const [n, set] = useState(0);
    useLoadEffect(() => { loads++; if (n < 3) set(n + 1); }, [n]);
    useEffect(() => new Promise(() => {}), []);
    return createElement(View, { key: "root", title: String(n) });
  }
  const renderer = mount(createElement(Root, { key: "page" }), { ...allow, schedule: task => queued.push(task) });
  try {
    const snapshot = await renderer.settle(new AbortController().signal, 80);
    expect(title(snapshot)).toBe("3"); expect(snapshot.version).toBe(4); expect(loads).toBe(4);
    for (const task of queued) task();
    expect(renderer.current()).toBe(snapshot);
    console.log(`PASS queued finite dependency: value=3 version=${snapshot.version} loads=${loads}; drained queue produced no duplicate revision`);
  } finally { renderer.dispose(); }
});

test("queued synchronous self-invalidating finite loads stop at the work ceiling", async () => {
  function Root() {
    const [n, set] = useState(0);
    useLoadEffect(() => { set(n + 1); }, [n]);
    return createElement(View, { key: "root", title: String(n) });
  }
  const queued: (() => void)[] = [];
  const renderer = mount(createElement(Root, { key: "page" }), { ...allow, schedule: task => queued.push(task), limits: { pendingEffects: 2 } });
  try {
    await expect(renderer.settle(new AbortController().signal, 80)).rejects.toThrow("work limit");
    expect(renderer.suspended).toBe(true); expect(queued.length).toBeLessThanOrEqual(514);
  } finally { renderer.dispose(); }
});

test("canceled callbacks either finish cooperatively or retire recovery without opening a second callback gate", async () => {
  for (const transport of ["stream", "snapshot"] as const) for (const mode of ["never", "late", "cooperative"] as const) {
    const work = gate<void>(); let calls = 0, signal!: AbortSignal, set!: (value: string) => void;
    const frames: Snapshot[] = [];
    function Root() {
      const [value, update] = useState("initial"); set = update;
      return createElement(View, { key: "root", title: value, children: createElement(Section, { key: "main", children:
        createElement(Button, { key: "write", title: "Write", action: callback(async (_values, context) => {
          calls++; signal = context.signal;
          if (calls === 1) {
            if (mode === "cooperative") await new Promise<void>((_resolve, reject) => signal.addEventListener("abort", () => reject(new Error("canceled")), { once: true }));
            else if (mode === "never") await new Promise(() => {});
            else await work.promise;
            set("late"); // Even uncooperative state writes must not resurrect a retired renderer.
          }
          return { toast: "Saved" };
        }) }),
      }) });
    }
    const bridge = new PageBridge({ home: componentPage(() => createElement(Root, { key: "page" })) }, {
      notify: (_event: string, params: any) => frames.push(params.snapshot),
    } as unknown as Hub);
    const event = (snapshot: Snapshot, eventId: string) => ({ session: "owned", ticket: "host", interaction: {
      generation: snapshot.generation, version: snapshot.version, eventId, key: "write", values: {},
      actionId: (snapshot.view.sections[0].items[0] as { action: { id: string } }).action.id,
    } });
    try {
      const first = await bridge.call("mount", { session: "owned", transport, params: { contribution: "home" } }) as Snapshot;
      const original = event(first, "one");
      const pending = bridge.call("event", original).catch(error => error);
      await ticks(); expect(calls).toBe(1);
      await bridge.call("suspend", { session: "owned" });
      const canceled: any = await pending; await ticks();
      expect(signal.aborted).toBe(true);
      if (transport === "stream") { expect(canceled.status).toBe("ambiguous"); expect(canceled.result).toBeUndefined(); }
      else expect(canceled).toBeInstanceOf(Error);
      if (mode === "cooperative") {
        const resumed = await bridge.call("resume", { session: "owned" }) as Snapshot;
        const replay: any = await bridge.call("event", original);
        expect(replay.status).toBe("ambiguous"); expect(calls).toBe(1);
        const fresh = event(resumed, "two");
        const accepted: any = await bridge.call("event", fresh);
        expect(accepted.status).toBe("accepted"); expect(accepted.result).toEqual({ toast: "Saved" });
        expect((await bridge.call("event", fresh) as any).result).toEqual(accepted.result); expect(calls).toBe(2);
      } else {
        await expect(bridge.call("event", original)).rejects.toThrow("still running");
        await expect(bridge.call("resume", { session: "owned" })).rejects.toThrow("retired");
        await expect(bridge.call("event", event(first, "two"))).rejects.toThrow("retired");
        const before = frames.length;
        work.resolve(); await ticks();
        await expect(bridge.call("resume", { session: "owned" })).rejects.toThrow("retired");
        await expect(bridge.call("snapshot", { session: "owned" })).rejects.toThrow("retired");
        expect(calls).toBe(1); expect(frames.length).toBe(before);
      }
    } finally { bridge.close(); work.resolve(); }
  }
});

test("negotiated rpc.cancel targets only the active page request, not services or later invocations", async () => {
  let calls = 0, canceled = 0, callbackSignal!: AbortSignal, effectSignal!: AbortSignal;
  const service = gate<void>(); const frames: any[] = [];
  function Root() {
    useEffect(signal => { effectSignal = signal; }, []);
    return createElement(View, { key: "root", children: createElement(Section, { key: "main", children:
      createElement(Button, { key: "write", title: "Write", action: callback(async (_values, { signal }) => {
        calls++; callbackSignal = signal;
        if (calls === 1) await new Promise<void>((_resolve, reject) => signal.addEventListener("abort", () => { canceled++; reject(new Error("canceled")); }, { once: true }));
      }) }),
    }) });
  }
  const conn = connect({ id: "cancel", version: "1", ui: { home: componentPage(() => createElement(Root, { key: "page" })) },
    namespaces: { service: async () => { await service.promise; return {}; } } }, line => frames.push(JSON.parse(line)));
  const send = (id: string, method: string, params: unknown = {}) => conn.line(JSON.stringify({ id, method, params }));
  const cancel = (id: string) => conn.line(JSON.stringify({ event: "rpc.cancel", params: { id } }));
  try {
    send("init", "initialize", { protocol: 1 }); await conn.drain(); expect(frames[0].result.ui_page_cancellation).toBe(1);
    send("mount", "ui.page.mount", { session: "owned", params: { contribution: "home" } }); await conn.drain();
    const snapshot = frames.find(frame => frame.id === "mount").result as Snapshot;
    const interaction = { generation: snapshot.generation, version: snapshot.version, eventId: "one", key: "write", values: {}, actionId: (snapshot.view.sections[0].items[0] as { action: { id: string } }).action.id };
    send("service", "service.read"); send("event", "ui.page.event", { session: "owned", ticket: "host", interaction });
    await ticks(); cancel("unknown"); cancel("service"); await ticks(); expect(callbackSignal.aborted).toBe(false); expect(canceled).toBe(0);
    cancel("event"); service.resolve(); await conn.drain(); expect(callbackSignal.aborted).toBe(true); expect(effectSignal.aborted).toBe(true); expect(canceled).toBe(1);
    expect(frames.find(frame => frame.id === "event").result.status).toBe("ambiguous");
    send("resume", "ui.page.resume", { session: "owned" }); await conn.drain(); expect(effectSignal.aborted).toBe(false);
    cancel("event"); await ticks(); expect(effectSignal.aborted).toBe(false);
    send("replay", "ui.page.event", { session: "owned", ticket: "host", interaction }); await conn.drain(); expect(calls).toBe(1);
    // A cancel immediately behind a request is registered before asynchronous dispatch.
    send("cold", "ui.page.mount", { session: "cold", params: { contribution: "home" } }); cancel("cold"); await conn.drain();
    expect(frames.find(frame => frame.id === "cold").error).toBeDefined();
  } finally { conn.close(); service.resolve(); }
});

test("readiness does not rediscover dirty loads by rescanning the hook tree on every wake", async () => {
  const work = gate<void>(); let update!: (n: number) => void, inspected = 0;
  function Leaf({ n }: { n: number }) {
    for (let i = 0; i < 4; i++) useState(0);
    useLoadEffect(async () => { await work.promise; }, []);
    return createElement(Section, { key: `section-${n}`, children: [] });
  }
  function Root() {
    const [revision, set] = useState(0); update = set;
    return createElement(View, { key: "root", title: String(revision), children: Array.from({ length: 100 }, (_, n) => createElement(Leaf, { key: `leaf-${n}`, n })) });
  }
  const renderer = mount(createElement(Root, { key: "page" }), allow);
  // Instrument only readiness's former .some predicates; render/commit traversal remains unchanged.
  for (const instance of (renderer as any).instances.values()) {
    instance.hooks = new Proxy(instance.hooks, { get(target, key, receiver) {
      if (key === "some") return (predicate: (value: unknown, index: number) => boolean) => target.some((value: unknown, index: number) => { inspected++; return predicate(value, index); });
      return Reflect.get(target, key, receiver);
    } });
  }
  try {
    const pending = renderer.settle(new AbortController().signal, 1000);
    for (let n = 1; n <= 10; n++) { update(n); await ticks(); }
    work.resolve(); await pending;
    console.log(`PASS readiness hook predicates: components=101 hooks=501 updates=10 inspected=${inspected}`);
    expect(inspected).toBe(0);
  } finally { renderer.dispose(); work.resolve(); }
});

test("serialized finite work is independent of the pending-effect concurrency limit", async () => {
  let completed = 0;
  function Root() {
    for (let n = 0; n < 5; n++) useLoadEffect(async () => { await new Promise(resolve => setTimeout(resolve, 1)); completed++; }, []);
    return createElement(View, { key: "root", title: "finite" });
  }
  const renderer = mount(createElement(Root, { key: "page" }), { ...allow, limits: { pendingEffects: 1 } });
  try {
    const started = performance.now();
    await renderer.settle(new AbortController().signal, 1000).catch(error => {
      console.log(`FAIL serialized finite loads: completed=${completed}, elapsedMs=${Math.round(performance.now() - started)}`); throw error;
    });
    expect(completed).toBe(5); expect(renderer.suspended).toBe(false);
    console.log(`PASS serialized finite loads: completed=${completed}, elapsedMs=${Math.round(performance.now() - started)}`);
  } finally { renderer.dispose(); }
});

test("ordinary 10 ms intervals cannot spend an eight-second finite read's work budget", async () => {
  let ticks = 0;
  function Root() {
    const [clock, setClock] = useState(0);
    const [loaded, setLoaded] = useState(false);
    useEffect(signal => {
      const timer = setInterval(() => { ticks++; setClock(n => n + 1); }, 10);
      signal.addEventListener("abort", () => clearInterval(timer), { once: true });
      return () => clearInterval(timer);
    }, []);
    useLoadEffect(async signal => { await new Promise(resolve => setTimeout(resolve, 8000)); if (!signal.aborted) setLoaded(true); }, []);
    return createElement(View, { key: "root", title: loaded ? "loaded" : String(clock) });
  }
  const renderer = mount(createElement(Root, { key: "page" }), allow);
  try {
    const started = performance.now();
    expect(title(await renderer.settle(new AbortController().signal, 10_000).catch(error => {
      console.log(`FAIL timer opt-out: ticks=${ticks}, elapsedMs=${Math.round(performance.now() - started)}`); throw error;
    }))).toBe("loaded");
    expect(ticks).toBeGreaterThan(512); expect(renderer.suspended).toBe(false);
    console.log(`PASS timer opt-out: ticks=${ticks}, elapsedMs=${Math.round(performance.now() - started)}, version=${renderer.current().version}`);
  } finally { renderer.dispose(); }
}, 12_000);

test("monotonic readiness deadline stops synchronous work before timer callbacks can run", async () => {
  const queued: (() => void)[] = [];
  function Root() {
    const [n, set] = useState(0);
    useLoadEffect(() => { const end = performance.now() + 2; while (performance.now() < end) {} set(n + 1); }, [n]);
    return createElement(View, { key: "root", title: String(n) });
  }
  const renderer = mount(createElement(Root, { key: "page" }), { ...allow, schedule: task => queued.push(task) });
  try {
    await expect(renderer.settle(new AbortController().signal, 20)).rejects.toThrow("timeout");
    expect(queued.length).toBeLessThan(512); expect(renderer.suspended).toBe(true);
  } finally { renderer.dispose(); }
});

test("callback authorization is signal-scoped across await, never ambient in effects", async () => {
  for (const asynchronous of [false, true]) {
    let authorize!: RendererOptions["authorize"], original!: UIEvent;
    const work = gate<void>(); const effectWork = gate<void>();
    const checks: boolean[] = [], callbackChecks: boolean[] = [];
    function Root() {
      const [revision, setRevision] = useState(0);
      useLoadEffect(async signal => {
        if (!revision) return;
        // Same event and action metadata: the effect cannot borrow the callback's ticket.
        checks.push(await authorize({ id: original.actionId }, original, signal));
        await effectWork.promise;
        checks.push(await authorize({ id: original.actionId }, original, signal));
      }, [revision]);
      return createElement(View, { key: "root", children: createElement(Section, { key: "main", children:
        createElement(Button, { key: "write", title: "Write", action: callback(async (_values, { signal }) => {
          setRevision(n => n + 1);
          callbackChecks.push(await authorize({ id: original.actionId }, original, signal));
          if (asynchronous) await work.promise;
          callbackChecks.push(await authorize({ id: original.actionId }, original, signal));
        }) }),
      }) });
    }
    const page = componentPage(() => createElement(Root, { key: "page" }));
    const wrapped = { ...page, createRenderer: (host: Parameters<typeof page.createRenderer>[0]) => { authorize = host.authorize; return page.createRenderer(host); } };
    const bridge = new PageBridge({ home: wrapped }, { notify() {} } as unknown as Hub);
    try {
      const snapshot = await bridge.call("mount", { session: "scope", transport: "snapshot", params: { contribution: "home" } }) as Snapshot;
      original = { generation: snapshot.generation, version: snapshot.version, eventId: "write", key: "write",
        actionId: (snapshot.view.sections[0].items[0] as { action: { id: string } }).action.id, values: {} };
      const pending = bridge.call("event", { session: "scope", ticket: "host", interaction: original });
      await ticks(); expect(checks).toEqual([false]);
      effectWork.resolve(); await ticks(); expect(checks).toEqual([false, false]);
      work.resolve(); await pending; expect(callbackChecks).toEqual([true, true]);
      expect(await authorize({ id: original.actionId }, original, new AbortController().signal)).toBe(false);
      console.log(`PASS scope: async=${asynchronous}, callback=${callbackChecks}, effect=${checks}`);
    } finally { bridge.close(); work.resolve(); effectWork.resolve(); }
  }
});

test("a synchronous callback cannot authorize its first synchronous loading effect", async () => {
  let authorize!: RendererOptions["authorize"], original!: UIEvent, allowed: unknown;
  function Root() {
    const [revision, set] = useState(0);
    useLoadEffect(signal => { if (revision) allowed = authorize({ id: original.actionId }, original, signal); }, [revision]);
    return createElement(View, { key: "root", children: createElement(Section, { key: "main", children:
      createElement(Button, { key: "write", title: "Write", action: callback(() => { set(1); }) }),
    }) });
  }
  const page = componentPage(() => createElement(Root, { key: "page" }));
  const wrapped = { ...page, createRenderer: (host: Parameters<typeof page.createRenderer>[0]) => { authorize = host.authorize; return page.createRenderer(host); } };
  const bridge = new PageBridge({ home: wrapped }, { notify() {} } as unknown as Hub);
  try {
    const first = await bridge.call("mount", { session: "sync", transport: "snapshot", params: { contribution: "home" } }) as Snapshot;
    original = { generation: first.generation, version: first.version, eventId: "one", key: "write", actionId: (first.view.sections[0].items[0] as { action: { id: string } }).action.id, values: {} };
    await bridge.call("event", { session: "sync", ticket: "host", interaction: original }); expect(allowed).toBe(false);
  } finally { bridge.close(); }
});

const allow = { authorize: () => true };
const ticks = async () => { for (let n = 0; n < 24; n++) await Promise.resolve(); };
function gate<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}
const title = (snapshot: Snapshot) => snapshot.view.title;
function fixture(timeoutMs = 1000) {
  let reads = 0, writes = 0, canceled = 0, active = 0;
  let cached = false, fail = false;
  const seen: UIRequest[] = [];
  function Root() {
    const [value, setValue] = useState("loading");
    const [revision, setRevision] = useState(0);
    useLoadEffect(async signal => {
      if (cached) { setValue("loaded"); return; }
      reads++; active++;
      try {
        await new Promise<void>((resolve, reject) => {
          const abort = () => { clearTimeout(timer); canceled++; reject(new Error("canceled")); };
          const timer = setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, 200);
          signal.addEventListener("abort", abort, { once: true });
        });
        if (!signal.aborted) { cached = !fail; setValue(fail ? "error" : "loaded"); }
      } catch { /* Cancellation is not a UI error. */ }
      finally { active--; }
    }, [revision]);
    return createElement(View, { key: "root", title: value, children: createElement(Section, { key: "main", children: [
      createElement(Text, { key: "value", text: value }),
      createElement(Button, { key: "reload", title: "Reload", action: callback(() => {
        writes++; cached = false; setValue("loading"); setRevision(n => n + 1); return { toast: "Saved" };
      }) }),
    ] }) });
  }
  const page = componentPage(request => { seen.push(request); return createElement(Root, { key: "page" }); }, { snapshotTimeoutMs: timeoutMs });
  const frames: Snapshot[] = [];
  const bridge = new PageBridge({ home: page }, { notify: (_event: string, params: any) => frames.push(params.snapshot) } as unknown as Hub);
  const open = (transport?: string, ui_version = 6) => bridge.call("mount", { session: "owned", transport, params: { contribution: "home", ui_version } }) as Promise<Snapshot>;
  const event = (snapshot: Snapshot, eventId = "refresh"): UIEvent => ({ generation: snapshot.generation, version: snapshot.version, eventId, key: "reload",
    actionId: (snapshot.view.sections[0].items[1] as { action: { id: string } }).action.id, values: {} });
  return { bridge, page, open, seen, frames, event, stats: () => ({ reads, writes, canceled, active }), fail: () => { fail = true; } };
}

test("snapshot transport cold 200 ms load, cached resume and action settle without spurious invalidation", async () => {
  const h = fixture();
  try {
    const first = await h.open("snapshot", 6);
    expect(title(first)).toBe("loaded"); expect(h.seen[0].transport).toBe("snapshot");
    expect(h.stats()).toEqual({ reads: 1, writes: 0, canceled: 0, active: 0 });
    await h.bridge.call("suspend", { session: "owned" });
    const resumed = await h.bridge.call("resume", { session: "owned" }) as Snapshot;
    expect(title(resumed)).toBe("loaded"); expect(resumed.version).toBe(first.version + 1);
    expect(h.stats().reads).toBe(1);
    const interaction = h.event(resumed);
    const result: any = await h.bridge.call("event", { session: "owned", ticket: "host", interaction });
    expect(result.status).toBe("accepted"); expect(result.result).toEqual({ toast: "Saved" });
    expect(title(result.snapshot)).toBe("loaded"); expect(result.snapshot.version).toBeGreaterThan(resumed.version);
    const replay: any = await h.bridge.call("event", { session: "owned", ticket: "host", interaction });
    expect(replay.result).toEqual(result.result); expect(replay.snapshot.version).toBe(result.snapshot.version);
    expect(h.stats()).toEqual({ reads: 2, writes: 1, canceled: 0, active: 0 });
    expect(h.frames).toHaveLength(0);
    expect((await h.bridge.call("event", { session: "owned", ticket: "host", interaction: { ...interaction, eventId: "stale" } }) as any).status).toBe("ambiguous");
    expect(h.stats().writes).toBe(1);
  } finally { h.bridge.close(); }
});

test("stream and older unspecified hosts return loading immediately and publish the loaded frame", async () => {
  for (const transport of ["stream", undefined]) {
    const h = fixture();
    try {
      const first = await h.open(transport);
      expect(title(first)).toBe("loading"); expect(h.stats().active).toBe(1);
      await new Promise(resolve => setTimeout(resolve, 230));
      expect(h.frames.some(frame => title(frame) === "loaded")).toBe(true);
    } finally { h.bridge.close(); }
  }
});

test("finite error state is returned before snapshot suspension", async () => {
  const h = fixture(); h.fail();
  try { expect(title(await h.open("snapshot"))).toBe("error"); expect(h.stats().active).toBe(0); }
  finally { h.bridge.close(); }
});

test("snapshot timeout fails and cancels finite loading, never succeeds with loading", async () => {
  const h = fixture(25);
  try {
    await expect(h.open("snapshot")).rejects.toThrow("snapshot readiness timeout");
    await ticks(); expect(h.stats()).toEqual({ reads: 1, writes: 0, canceled: 1, active: 0 });
    await expect(h.bridge.call("snapshot", { session: "owned" })).rejects.toThrow("unknown page session");
  } finally { h.bridge.close(); }
});

test("ordinary long-lived effects opt out while finite effects wait, without invalidating cached state", async () => {
  const work = gate<void>(); let signal!: AbortSignal;
  function Root() {
    useEffect(async current => { signal = current; await work.promise; }, []);
    useLoadEffect(() => {}, []);
    return createElement(View, { key: "root", title: "cached" });
  }
  const renderer = mount(createElement(Root, { key: "page" }), allow);
  try {
    expect((await renderer.settle(new AbortController().signal, 25)).version).toBe(1);
    expect(signal.aborted).toBe(false);
    renderer.suspend(); expect(signal.aborted).toBe(true);
  } finally { renderer.dispose(); work.resolve(); }
});

test("settle drains a scheduled render and a dependent finite load, not unrelated pending work", async () => {
  const first = gate<void>(), second = gate<void>(), background = gate<void>(); let set!: (n: number) => void;
  function Root() {
    const [n, update] = useState(0); set = update;
    useLoadEffect(async signal => { await (n === 0 ? first : second).promise; if (!signal.aborted && n === 0) set(1); }, [n]);
    useEffect(async () => { await background.promise; }, []);
    return createElement(View, { key: "root", title: String(n) });
  }
  const queued: (() => void)[] = [];
  const renderer = mount(createElement(Root, { key: "page" }), { ...allow, schedule: task => queued.push(task) });
  try {
    let ready = false; const waiting = renderer.settle(new AbortController().signal, 1000).then(snapshot => { ready = true; return snapshot; });
    first.resolve(); await ticks(); expect(ready).toBe(false); expect(title(renderer.current())).toBe("1");
    second.resolve(); const snapshot = await waiting;
    expect(title(snapshot)).toBe("1"); expect(snapshot.version).toBe(2);
    for (const task of queued) task(); expect(renderer.current().version).toBe(2);
  } finally { renderer.dispose(); first.resolve(); second.resolve(); background.resolve(); }
});

test("useFetch snapshot wait is explicitly opt-in and uses only the sanctioned adapter", async () => {
  const response = gate<Response>();
  function Root() { const result = useFetch("https://example.com/data", { snapshot: "wait" }); return createElement(View, { key: "root", title: result.status }); }
  const renderer = mount(createElement(Root, { key: "page" }), { ...allow, fetch: () => response.promise });
  try {
    const ready = renderer.settle(new AbortController().signal, 1000);
    response.resolve(Response.json({ n: 1 })); expect(title(await ready)).toBe("success");
  } finally { renderer.dispose(); }
});

test("dispose and suspend abort snapshot mount readiness and leave no late mount", async () => {
  for (const method of ["dispose", "suspend"]) {
    const h = fixture();
    try {
      const pending = h.open("snapshot").catch(error => error);
      await ticks(); await h.bridge.call(method, { session: "owned" }); expect(await pending).toBeInstanceOf(Error);
      await ticks(); expect(h.stats().active).toBe(0); expect(h.stats().canceled).toBe(1);
      await expect(h.bridge.call("snapshot", { session: "owned" })).rejects.toThrow("unknown page session");
    } finally { h.bridge.close(); }
  }
});

test("canceled asynchronous factory cannot remove a replacement with the same session ID", async () => {
  const work = gate<void>(); let first = true, signal!: AbortSignal, renders = 0;
  const page = componentPage(async request => {
    if (first) { first = false; signal = request.signal!; await work.promise; }
    function Root() { renders++; return createElement(View, { key: "root", title: "replacement" }); }
    return createElement(Root, { key: "page" });
  });
  const bridge = new PageBridge({ home: page }, { notify() {} } as unknown as Hub);
  try {
    const params = { session: "same", transport: "snapshot", params: { contribution: "home" } };
    const pending = bridge.call("mount", params).catch(error => error);
    await ticks(); await bridge.call("dispose", { session: "same" });
    const replacement = await bridge.call("mount", params) as Snapshot;
    expect((await pending as Error).message).toContain("canceled"); expect(signal.aborted).toBe(true); work.resolve(); await ticks();
    expect((await bridge.call("snapshot", { session: "same" }) as Snapshot).generation).toBe(replacement.generation);
    expect(renders).toBe(1);
  } finally { bridge.close(); work.resolve(); }
});

test("concurrent mount/action is fenced while snapshot readiness is pending", async () => {
  const h = fixture();
  try {
    const pending = h.open("snapshot"); await ticks();
    await expect(h.open("snapshot")).rejects.toThrow("quota");
    await expect(h.bridge.call("event", { session: "owned", ticket: "host", interaction: {} })).rejects.toThrow("busy");
    const snapshot = await pending;
    const action = h.bridge.call("event", { session: "owned", ticket: "host", interaction: h.event(snapshot) });
    await ticks(); await expect(h.bridge.call("resume", { session: "owned" })).rejects.toThrow("busy");
    await action; expect(h.stats().writes).toBe(1);
  } finally { h.bridge.close(); }
});

test("snapshot readiness does not extend the host action ticket beyond the callback invocation", async () => {
  const work = gate<void>(); let authorize!: RendererOptions["authorize"]; let allowed: boolean | undefined;
  function Root() {
    const [revision, setRevision] = useState(0);
    useLoadEffect(async signal => {
      if (!revision) return;
      await work.promise;
      allowed = await authorize({ id: "cb.unrelated" }, {} as UIEvent, signal);
    }, [revision]);
    return createElement(View, { key: "root", children: createElement(Section, { key: "main", children:
      createElement(Button, { key: "write", title: "Write", action: callback(() => setRevision(n => n + 1)) }),
    }) });
  }
  const page = componentPage(() => createElement(Root, { key: "page" }));
  const wrapped = { ...page, createRenderer: (host: Parameters<typeof page.createRenderer>[0]) => { authorize = host.authorize; return page.createRenderer(host); } };
  const bridge = new PageBridge({ home: wrapped }, { notify() {} } as unknown as Hub);
  try {
    const snapshot = await bridge.call("mount", { session: "owned", transport: "snapshot", params: { contribution: "home" } }) as Snapshot;
    const actionId = (snapshot.view.sections[0].items[0] as { action: { id: string } }).action.id;
    const pending = bridge.call("event", { session: "owned", ticket: "host", interaction: { generation: snapshot.generation, version: snapshot.version, eventId: "write", key: "write", actionId, values: {} } });
    await ticks(); work.resolve(); await pending; expect(allowed).toBe(false);
  } finally { bridge.close(); work.resolve(); }
});

test("dispose and suspend interrupt action readiness without replaying its callback", async () => {
  for (const method of ["dispose", "suspend"]) {
    const h = fixture();
    try {
      const snapshot = await h.open("snapshot");
      const pending = h.bridge.call("event", { session: "owned", ticket: "host", interaction: h.event(snapshot) }).catch(error => error);
      await ticks(); await h.bridge.call(method, { session: "owned" });
      expect(await pending).toBeInstanceOf(Error); await ticks();
      expect(h.stats().writes).toBe(1); expect(h.stats().active).toBe(0); expect(h.stats().canceled).toBe(1);
      if (method === "suspend") {
        expect(title(await h.bridge.call("resume", { session: "owned" }) as Snapshot)).toBe("loaded");
        expect(h.stats().writes).toBe(1);
      }
    } finally { h.bridge.close(); }
  }
});

test("ordinary componentSnapshot widgets/panels retain their immediate one-shot lifecycle", async () => {
  const h = fixture();
  try {
    const view = await componentSnapshot(h.page, { contribution: "home", hub: {} } as UIRequest);
    expect(view.title).toBe("loading"); await ticks(); expect(h.stats().active).toBe(0); expect(h.stats().canceled).toBe(1);
    expect(h.seen[0].transport).toBeUndefined();
  } finally { h.bridge.close(); }
});

test("a retired readiness wait cannot succeed after suspend/resume or undo newer visibility", async () => {
  const work = gate<void>(); const signals: AbortSignal[] = [];
  function Root() {
    useLoadEffect(async signal => { signals.push(signal); await work.promise; }, []);
    return createElement(View, { key: "root" });
  }
  const renderer = mount(createElement(Root, { key: "page" }), allow);
  try {
    const pending = renderer.settle(new AbortController().signal, 1000).catch(error => error);
    renderer.suspend(); renderer.resume();
    expect((await pending as Error).message).toContain("inactive");
    expect(renderer.suspended).toBe(false); expect(signals[0].aborted).toBe(true); expect(signals[1].aborted).toBe(false);
    work.resolve(); await ticks(); expect((await renderer.settle(new AbortController().signal, 25)).version).toBe(2);
  } finally { renderer.dispose(); work.resolve(); }
});

test("unhandled finite load failures cannot masquerade as successful loading snapshots", async () => {
  for (const asynchronous of [false, true]) {
    function Root() {
      useLoadEffect(() => { if (asynchronous) return Promise.reject(new Error("synthetic failure")); throw new Error("synthetic failure"); }, []);
      return createElement(View, { key: "root", title: "loading" });
    }
    const renderer = mount(createElement(Root, { key: "page" }), allow);
    try { await expect(renderer.settle(new AbortController().signal, 1000)).rejects.toThrow("loading effect failed"); }
    finally { renderer.dispose(); }
  }
});

test("snapshot work bounds stop self-invalidating loads and capacity blocked by unrelated work times out", async () => {
  function Loop() {
    const [n, set] = useState(0);
    useLoadEffect(async () => { set(n + 1); }, [n]);
    return createElement(View, { key: "root", title: String(n) });
  }
  const renderer = mount(createElement(Loop, { key: "page" }), { ...allow, limits: { pendingEffects: 2 } });
  try { await expect(renderer.settle(new AbortController().signal, 1000)).rejects.toThrow("work limit"); }
  finally { renderer.dispose(); }
  const unrelated = gate<void>();
  function Blocked() {
    useEffect(async () => { await unrelated.promise; }, []);
    useLoadEffect(() => {}, []);
    return createElement(View, { key: "root" });
  }
  const blocked = mount(createElement(Blocked, { key: "page" }), { ...allow, limits: { pendingEffects: 1 } });
  try { await expect(blocked.settle(new AbortController().signal, 25)).rejects.toThrow("timeout"); }
  finally { blocked.dispose(); unrelated.resolve(); }
});

test("readiness cancellation, work bound and shortened timeout never keep a renderer subscribed", async () => {
  const work = gate<void>();
  function Root() { useLoadEffect(async () => { await work.promise; }, []); return createElement(View, { key: "root" }); }
  for (const method of ["suspend", "dispose"] as const) {
    const renderer = mount(createElement(Root, { key: "page" }), allow);
    const pending = renderer.settle(new AbortController().signal, 1000).catch(error => error);
    renderer[method](); expect((await pending as Error).message).toContain("inactive"); renderer.dispose();
  }
  const renderer = mount(createElement(Root, { key: "page" }), allow);
  try {
    const controller = new AbortController();
    const pending = renderer.settle(controller.signal, 1000).catch(error => error);
    controller.abort(); expect((await pending as Error).message).toContain("canceled");
    await expect(renderer.settle(controller.signal, 20_000)).rejects.toThrow("invalid");
  } finally { renderer.dispose(); work.resolve(); }
});
