import type { UIStreamActionResult } from "./wire.ts";
import type { Hub, UIRequest } from "./index.ts";
import type { Node } from "./components.ts";
import { Renderer, type RendererOptions, type Snapshot, type UIEvent } from "./renderer.ts";

/** Root factories run once per manager-owned session. Closures never leave this process. */
export interface ComponentPage {
  readonly componentPage: true;
  readonly root: (request: UIRequest) => Node | Promise<Node>;
  readonly options: Omit<RendererOptions, "authorize" | "onRender" | "transformResult"> & {
    /** May shorten the host's 10 s readiness ceiling; cannot extend it. */
    snapshotTimeoutMs?: number;
  };
  /** Versioned opt-in from the page's bundled SDK; older bundles remain immediate snapshots. */
  readonly snapshotReadiness?: 1;
  /** Versioned callback-signal handoff; no ambient asynchronous invocation context. */
  readonly callbackScope?: 1;
  /** Construct in the page bundle's module instance: element symbols alone cannot share hook context. */
  readonly createRenderer: (host: Pick<RendererOptions, "authorize" | "onRender" | "transformResult">) => Renderer;
}
export function componentPage(root: ComponentPage["root"], options: ComponentPage["options"] = {}): ComponentPage {
  return { componentPage: true, snapshotReadiness: 1, callbackScope: 1, root, options, createRenderer: host => new Renderer({
    ...options, ...host,
    // The manager evicts after 64 events and rejects those old wire revisions;
    // retaining more native results cannot serve an authenticated retry.
    limits: { ...options.limits, events: Math.min(options.limits?.events ?? 64, 64) },
  }) };
}
export function isComponentPage(page: unknown): page is ComponentPage {
  return !!page && (page as ComponentPage).componentPage === true;
}
export async function componentSnapshot(page: ComponentPage, request: UIRequest) {
 const renderer=page.createRenderer({authorize:()=>false});
 try {renderer.mount(await page.root(request));return renderer.current().view;}
 finally {renderer.dispose();}
}

const RESULT_MOST = 64 << 10;
// Allow the native 20,000-line maximum plus bounded result/container metadata.
const RESULT_NODES_MOST = 32768;
const encoder = new TextEncoder();
/** Copy before caching: guest objects/closures cannot survive as unbounded event receipts.
 * Exact native shape, saved-action and URL policy are independently checked by the manager. */
function pageResult(value: unknown): Readonly<UIStreamActionResult> {
  if (value === undefined || value === null) return Object.freeze({});
  let nodes = 0, strings = 0;
  const ancestors = new Set<object>();
  const string = (text: string) => {
    strings += text.length;
    if (strings > RESULT_MOST) throw new Error("page result byte limit");
    return text;
  };
  const copy = (item: unknown, depth: number): unknown => {
    if (++nodes > RESULT_NODES_MOST || depth > 16) throw new Error("page result structure limit");
    if (typeof item === "string") return string(item);
    if (item === null || typeof item === "boolean") return item;
    if (typeof item === "number" && Number.isFinite(item)) return item;
    if (!item || typeof item !== "object" || ancestors.has(item)) throw new Error("page results require acyclic JSON");
    ancestors.add(item);
    try {
      if (Array.isArray(item)) {
        if (item.length > RESULT_NODES_MOST - nodes) throw new Error("page result structure limit");
        const out: unknown[] = [];
        for (let i = 0; i < item.length; i++) out.push(copy(item[i], depth + 1));
        return Object.freeze(out);
      }
      if (Object.getPrototypeOf(item) !== Object.prototype && Object.getPrototypeOf(item) !== null) throw new Error("page results require plain JSON");
      const out: Record<string, unknown> = Object.create(null);
      for (const key in item) if (Object.prototype.hasOwnProperty.call(item, key)) {
        string(key); out[key] = copy((item as Record<string, unknown>)[key], depth + 1);
      }
      return Object.freeze(out);
    } finally { ancestors.delete(item); }
  };
  if (typeof value !== "object" || Array.isArray(value)) throw new Error("page results require a native result object");
  const result = copy(value, 0) as UIStreamActionResult;
  if (encoder.encode(JSON.stringify(result)).length > RESULT_MOST) throw new Error("page result byte limit");
  return result;
}

interface Mounted {
  renderer: Renderer; contribution: string; ticket?: string; busy: boolean;
  transport: "snapshot" | "stream"; controller: AbortController; timeoutMs: number;
  readiness: boolean; scopedCallbacks: boolean; pendingEvent?: Promise<unknown>; retired?: boolean;
  authorization?: { event: UIEvent; signal?: AbortSignal };
}

/** Detach a canceled factory/callback even if guest code ignores its signal. Never late-mount it. */
async function active<T>(work: Promise<T> | T, signal: AbortSignal): Promise<T> {
  let abort!: () => void;
  const canceled = new Promise<never>((_resolve, reject) => {
    abort = () => reject(new Error("page operation canceled"));
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) abort();
  });
  try { return await Promise.race([work, canceled]); }
  finally { signal.removeEventListener("abort", abort); }
}

/** The process bridge trusts only calls from its host, not values inside an event.
 * A ticket is host-issued after saved-native-action gates, scoped to this invocation.
 * The bridge does not interpret a source authorize function as user approval. */
export class PageBridge {
  private sessions = new Map<string, Mounted>();
  constructor(private pages: Record<string, unknown>, private hub: Hub) {}
  async call(method: string, params: any, cancellation?: AbortSignal): Promise<unknown> {
    const id = params?.session;
    if (typeof id !== "string" || !id || id.length > 128) throw new Error("invalid page session");
    if (method === "dispose") {
      const entry = this.sessions.get(id);
      entry?.controller.abort(); entry?.renderer.dispose(); this.sessions.delete(id); return {};
    }
    if (method === "mount") {
      if (this.sessions.has(id) || this.sessions.size >= 256) throw new Error("page session quota");
      const page = this.pages[params.params?.contribution];
      if (!isComponentPage(page)) throw new Error("not a component page");
      const transport = params.transport ?? "stream"; // Older hosts retain immediate stream behavior.
      if (transport !== "snapshot" && transport !== "stream") throw new Error("invalid page transport");
      const timeoutMs = page.options.snapshotTimeoutMs ?? 10_000;
      if (!Number.isFinite(timeoutMs) || timeoutMs <= 0 || timeoutMs > 10_000) throw new Error("invalid snapshot readiness timeout");
      const entry = { contribution: params.params.contribution, busy: true, transport, timeoutMs, controller: new AbortController() } as Mounted;
      if (typeof page.createRenderer !== "function") throw new Error("component page requires its bundled renderer factory");
      const renderer = entry.renderer = page.createRenderer({
        authorize: (action, event, signal) => {
          const scope = entry.authorization;
          const saved = scope?.event;
          if (!entry.ticket || !saved || signal.aborted || action.id !== saved.actionId
            || event.generation !== saved.generation || event.version !== saved.version
            || event.eventId !== saved.eventId || event.key !== saved.key || event.actionId !== saved.actionId) return false;
          // Modern pages receive this host-created callback signal; effects never do.
          // Old renderers retain their preflight handoff without needing a new method.
          scope.signal ??= signal;
          return scope.signal === signal;
        },
        transformResult: pageResult,
        onRender: snapshot => { if (!entry.busy) this.hub.notify("ui.page.snapshot", { session: id, snapshot }); },
      });
      // The trusted bridge and page bundle may use different SDK versions. A method
      // alone is not an opt-in contract; a marker alone cannot make an old renderer wait.
      entry.readiness = page.snapshotReadiness === 1 && typeof renderer.settle === "function";
      entry.scopedCallbacks = page.callbackScope === 1;
      // Reserve before awaiting a factory, so concurrent mounts cannot evade totals.
      this.sessions.set(id, entry);
      const uncancel = this.cancellation(entry, cancellation);
      try {
        const signal = entry.controller.signal;
        if (signal.aborted) throw new Error("page operation canceled");
        const root = await active(page.root({ ...params.params, hub: this.hub, transport, signal }), signal);
        if (signal.aborted || this.sessions.get(id) !== entry) throw new Error("page operation canceled");
        renderer.mount(root);
        const snapshot = transport === "snapshot" ? await this.snapshot(entry) : renderer.current();
        entry.busy = false;
        return snapshot;
      } catch (error) {
        entry.controller.abort(); renderer.dispose();
        if (this.sessions.get(id) === entry) this.sessions.delete(id);
        throw error;
      } finally { uncancel(); }
    }
    const entry = this.sessions.get(id);
    if (!entry) throw new Error("unknown page session");
    if (entry.retired) throw new Error("retired page session");
    // Visibility changes abort UI-only work even while a callback is awaiting.
    if (method === "suspend") { entry.controller.abort(); entry.renderer.suspend(); return {}; }
    if (entry.busy) throw new Error("page session busy");
    entry.busy = true;
    const uncancel = this.cancellation(entry, cancellation);
    try {
      if (cancellation?.aborted) throw new Error("page operation canceled");
      if (method === "snapshot") return entry.renderer.current();
      if (method === "resume") {
        if (entry.pendingEvent) {
          // Aborting the RPC is not proof that a guest callback stopped or did not
          // commit. Fence this generation, without clearing its callback gate. The
          // manager maps failed recovery to its existing correlated expired path.
          entry.retired = true; entry.controller.abort(); entry.renderer.dispose();
          throw new Error("retired page session");
        }
        if (entry.renderer.suspended) { entry.controller = new AbortController(); entry.renderer.resume(); }
        if (entry.controller.signal.aborted || entry.renderer.suspended || entry.renderer.disposed) throw new Error("inactive page recovery");
        return entry.transport === "snapshot" ? await this.snapshot(entry) : entry.renderer.current();
      }
      if (method === "event") {
        if (entry.pendingEvent) throw new Error("page callback still running");
        if (typeof params.ticket !== "string" || !params.ticket) throw new Error("missing host action ticket");
        entry.ticket = params.ticket;
        const callbackController = entry.scopedCallbacks ? new AbortController() : undefined;
        entry.authorization = { event: params.interaction as UIEvent, signal: callbackController?.signal };
        let result: unknown;
        let status = "accepted";
        const work = entry.pendingEvent = entry.renderer.event(params.interaction as UIEvent, callbackController);
        const finished = () => {
          if (entry.pendingEvent === work) {
            entry.pendingEvent = undefined;
            entry.ticket = undefined; entry.authorization = undefined;
          }
        };
        // Install readiness before the callback's first asynchronous turn. Otherwise
        // self-invalidating finite effects can starve the timer before the guard exists.
        const ready = entry.transport === "snapshot" ? this.snapshot(entry) : undefined;
        // Observe both outcomes even when active() detaches an uncooperative callback.
        void work.then(finished, finished);
        const callback = active(work, entry.controller.signal).then(value => { result = value; }, () => { status = "ambiguous"; })
          .finally(() => { entry.ticket = undefined; entry.authorization = undefined; });
        const [, snapshot] = await Promise.all([callback, ready]);
        return { snapshot: snapshot ?? entry.renderer.current(), status, ...(status === "accepted" ? { result } : {}) };
      }
      throw new Error("unknown page method");
    } finally { uncancel(); entry.ticket = undefined; entry.authorization = undefined; entry.busy = false; }
  }
  private cancellation(entry: Mounted, signal?: AbortSignal): () => void {
    const cancel = () => {
      entry.ticket = undefined; entry.authorization = undefined;
      entry.controller.abort(); entry.renderer.suspend();
    };
    signal?.addEventListener("abort", cancel, { once: true });
    if (signal?.aborted) cancel();
    return () => signal?.removeEventListener("abort", cancel);
  }
  private async snapshot(entry: Mounted): Promise<Snapshot> {
    if (!entry.readiness) return entry.renderer.current();
    try { return await entry.renderer.settle(entry.controller.signal, entry.timeoutMs); }
    catch (error) {
      // settle owns its visibility fence; a retired wait must not suspend a newer epoch.
      if (entry.renderer.suspended || entry.renderer.disposed) entry.controller.abort();
      throw error;
    }
  }
  close(): void {
    for (const entry of this.sessions.values()) { entry.controller.abort(); entry.renderer.dispose(); }
    this.sessions.clear();
  }
}
