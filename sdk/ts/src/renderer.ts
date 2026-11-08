import type * as Wire from "./wire.ts";
import { callbackTag, elementTag, nativeTag, Fragment, type CallbackAction, type Element, type Node, type NativeType } from "./components.ts";
import { withHooks, type EffectHook, type FetchAdapter, type HookOwner, type Instance, type StateAdapter, type TimerAdapter } from "./hooks.ts";

/** Mounted sections require stable keys even though legacy snapshot sections permit none. */
export type KeyedView = Omit<Wire.View, "sections"> & { sections: (Wire.Section & { key: string })[] };
export interface Snapshot extends Readonly<Wire.UIPageSnapshot> { readonly view: KeyedView }
export interface UIEvent {
  generation: string;
  version: number;
  eventId: string;
  key: string;
  actionId: string;
  values: unknown;
}
export interface RendererOptions {
  /** Supplied only by the sanctioned host; absence fails closed, never falls back to global fetch. */
  fetch?: FetchAdapter;
  state?: StateAdapter;
  timers?: TimerAdapter;
  /** Must enqueue asynchronously; inline schedulers defeat render coalescing. */
  schedule?: (task: () => void) => void;
  onRender?: (snapshot: Snapshot) => void;
  onError?: (error: unknown) => void;
  /** Host process bridge bounds/detaches presentation results before storing replay promises. */
  transformResult?: (result: unknown) => unknown;
  /** Required for every callback. Check saved metadata and trusted consent, never client approval. */
  authorize: (action: Readonly<Wire.Action>, event: Readonly<UIEvent>, signal: AbortSignal) => boolean | Promise<boolean>;
  limits?: { nodes?: number; depth?: number; events?: number; callbacks?: number; props?: number; hooks?: number; pendingEffects?: number; frameBytes?: number };
}
interface Registration { identity: string; key: string; action: Wire.Action; callback: CallbackAction }
interface Receipt { fingerprint: string; result: Promise<unknown> }
const defaultState: StateAdapter = { create<T>(initial: T) { let value = initial; return { get: () => value, set: (next: T) => { value = next; } }; } };
const defaultTimers: TimerAdapter = { setInterval: (fn, ms) => globalThis.setInterval(fn, ms), clearInterval: (id) => globalThis.clearInterval(id as ReturnType<typeof setInterval>) };

// Only the fixed native vocabulary is accepted, also when JS callers bypass TypeScript.
const fields: { [T in NativeType]: readonly (keyof Omit<Extract<Wire.Component, { type: T }>, "type" | "key">)[] } = {
  text: ["text", "style"], metric: ["label", "value", "detail", "tone", "icon", "icon_color", "spark"],
  progress: ["label", "ratio", "detail", "tone"], row: ["title", "subtitle", "value", "badge", "action", "icon", "avatar", "thumbnail", "icon_color", "control", "actions", "spark"],
  button: ["title", "action", "role"], link: ["title", "url"], field: ["name", "label", "kind", "value", "choices", "placeholder", "earliest", "latest", "detail"],
  divider: [], chart: ["kind", "series", "title", "unit", "stacked", "detail", "center", "rising"], stat_grid: ["stats"],
  segmented: ["name", "choices", "value", "action"], markdown: ["text"], table: ["columns", "rows", "caption"],
  empty: ["title", "detail", "icon", "button", "icon_color"], loading: ["title"], header: ["title", "subtitle", "badges", "facts", "avatar"],
  breadcrumb: ["crumbs"], file_tree: ["entries", "selected", "action", "expand"], code: ["path", "language", "text", "start_line", "highlight", "truncated", "load", "line_url"],
  diff: ["path", "old_path", "status", "language", "additions", "deletions", "binary", "collapsed", "hunks", "threads", "load", "expand", "comment", "context", "viewed", "mark_viewed", "revision", "generated", "similarity", "draft", "suggestions"],
  comment_thread: ["title", "comments", "resolved", "reply"], composer: ["name", "placeholder", "value", "buttons"],
  image: ["src", "alt", "caption", "aspect", "fit", "no_zoom"], grid: ["tiles", "max_columns"], carousel: ["title", "tiles", "width"],
};
// Length-prefixed segments stay unambiguous without repeatedly JSON-escaping ancestor paths.
function pathPart(parent: string, ...segments: string[]): string {
  return parent + segments.map(segment => `${segment.length}:${segment}`).join("");
}
function freeze<T>(value: T): T {
  if (value && typeof value === "object") { for (const child of Object.values(value)) freeze(child); Object.freeze(value); }
  return value;
}
/** UTF-16 length is a lower bound on JSON UTF-8 bytes; bound copying before exact serialization. */
class StringBudget {
  private used = 0;
  constructor(private limit: number, private message: string) {}
  add(value: string): void {
    if (value.length > this.limit - this.used) throw new Error(this.message);
    this.used += value.length;
  }
}
const encoder = new TextEncoder();

/** One mounted page, entirely server-side. No transport, host, authentication or client code is installed here. */
export class Renderer implements HookOwner {
  readonly generation = globalThis.crypto.randomUUID();
  readonly state: StateAdapter;
  readonly timers: TimerAdapter;
  readonly fetch: FetchAdapter;
  disposed = false;
  suspended = false;
  private tree: Node;
  private instances = new Map<string, Instance>();
  private nativeNodes = new Map<string, string>();
  private registrations = new Map<string, Registration>();
  private callbackIDs = new Map<string, string>(); // identities only survive suspend; closures do not
  private receipts = new Map<string, Receipt>();
  private activeEvents = new Map<AbortController, string>();
  private errors: unknown[] = [];
  private snapshot?: Snapshot;
  private scheduled = false;
  private rendering = false;
  private ticket = 0;
  private visibility = 0;
  private pendingEffects = 0;
  private pendingLoads = new Set<AbortController>();
  private dirtyLoads = new Set<EffectHook>();
  private loadFailed = false;
  private readiness?: () => void;
  private readinessGuard?: () => void;
  private limits: Required<NonNullable<RendererOptions["limits"]>>;

  constructor(private options: RendererOptions) {
    if (typeof options?.authorize !== "function") throw new Error("a trusted authorize callback is required");
    this.state = options.state ?? defaultState;
    this.timers = options.timers ?? defaultTimers;
    this.fetch = options.fetch ?? (() => Promise.reject(new Error("no sanctioned fetch adapter")));
    this.limits = { nodes: 2048, depth: 64, events: 1024, callbacks: 2048, props: 32768, hooks: 128, pendingEffects: 128, frameBytes: 1048576, ...options.limits };
    for (const limit of Object.values(this.limits)) if (!Number.isSafeInteger(limit) || limit < 1) throw new Error("invalid renderer limit");
  }
  get maxHooks(): number { return this.limits.hooks; }
  /** Mount once. A new Renderer is a new generation and owns independent state/callbacks. */
  mount(tree: Node): Snapshot {
    if (this.snapshot || this.disposed) throw new Error("renderer cannot mount");
    this.tree = tree;
    return this.render();
  }
  /** Full existing-wire snapshot. The manager computes deltas and handles version negotiation. */
  render(tree: Node = this.tree): Snapshot {
    if (this.disposed || this.suspended || this.rendering) throw new Error("renderer cannot render");
    this.tree = tree;
    this.rendering = true;
    this.scheduled = false;
    ++this.ticket;
    const visited = new Set<string>();
    const keys = new Set<string>();
    const registrations = new Map<string, Registration>();
    const nativeNodes = new Map<string, string>();
    const nativeIdentity = (path: string): string => {
      const id = this.nativeNodes.get(path) ?? globalThis.crypto.randomUUID();
      nativeNodes.set(path, id);
      return id;
    };
    const previousActions = this.callbackIDs;
    let nodeCount = 0;
    let propCount = 0;
    let traversalCount = 0;
    const strings = new StringBudget(this.limits.frameBytes, "UI frame limit exceeded");
    const addKey = (key: unknown): string => {
      if (typeof key !== "string" || !key || key.length > 256 || keys.has(key)) throw new Error("missing or duplicate UI key");
      if (++nodeCount > this.limits.nodes) throw new Error("UI node limit exceeded");
      keys.add(key);
      return key;
    };
    const lower = (value: unknown, key: string, depth: number, ancestors: Set<object>, slot: string): any => {
      if (++propCount > this.limits.props) throw new Error("UI prop limit exceeded");
      if (depth > this.limits.depth) throw new Error("UI depth limit exceeded");
      if (typeof value === "string") { strings.add(value); return value; }
      if (value === undefined || value === null || typeof value === "boolean") return value;
      if (typeof value === "number" && Number.isFinite(value)) return value;
      if (typeof value !== "object") throw new Error("only native JSON props and registered callbacks are allowed");
      if (callbackTag in value) {
        const cb = value as CallbackAction;
        const identity = JSON.stringify([key, slot]);
        if (registrations.size >= this.limits.callbacks) throw new Error("UI callback limit exceeded");
        const id = previousActions.get(identity) ?? `cb.${globalThis.crypto.randomUUID()}`;
        const options = lower(cb.options, key, depth + 1, ancestors, pathPart(slot, "options"));
        if (Object.keys(options).some((name) => !["confirm", "submit", "push", "values"].includes(name))) throw new Error("invalid callback action options");
        if (options.push && (options.push.page !== undefined || Object.keys(options.push).some((name) => !["ui", "state", "title"].includes(name)))) throw new Error("assistant-only action");
        const action = { ...options, id } as Wire.Action;
        registrations.set(id, { identity, key, action, callback: cb });
        return action;
      }
      if (ancestors.has(value)) throw new Error("cyclic UI props");
      ancestors.add(value);
      try {
        if (Array.isArray(value)) {
          if (value.length > this.limits.props - propCount) throw new Error("UI prop limit exceeded");
          return Array.from(value, (item, index) => lower(item, key, depth + 1, ancestors, pathPart(slot, "array", String(index))));
        }
        if (Object.getPrototypeOf(value) !== Object.prototype && Object.getPrototypeOf(value) !== null) throw new Error("native props must be plain JSON");
        if (elementTag in value) throw new Error("unsupported native prop");
        const out: Record<string, unknown> = {};
        for (const name in value) {
          if (!Object.prototype.hasOwnProperty.call(value, name)) continue;
          strings.add(name);
          if (propCount >= this.limits.props) throw new Error("UI prop limit exceeded");
          const item = (value as Record<string, unknown>)[name];
          if (item !== undefined) {
            Object.defineProperty(out, name, { value: lower(item, key, depth + 1, ancestors, pathPart(slot, "prop", name)), enumerable: true });
          } else ++propCount;
        }
        return out;
      } finally { ancestors.delete(value); }
    };
    const resolve = (node: Node, parent: string, depth: number): any[] => {
      if (++traversalCount > this.limits.props) throw new Error("UI prop limit exceeded: child traversal limit");
      if (depth > this.limits.depth) throw new Error("UI depth limit exceeded");
      if (node === null || node === undefined || node === false) return [];
      if (Array.isArray(node)) {
        // Check width before scanning siblings, including arrays of empty nodes or sparse slots.
        if (node.length > this.limits.props - traversalCount) throw new Error("UI prop limit exceeded: child traversal limit");
        const siblingKeys = new Set<string>();
        for (const child of node) {
          if (child && !Array.isArray(child) && child.type !== Fragment) {
            if (!child.key || siblingKeys.has(child.key)) throw new Error("missing or duplicate sibling key");
            siblingKeys.add(child.key);
          }
        }
        const items: any[] = [];
        for (const child of node) items.push(...resolve(child, parent, depth + 1));
        return items;
      }
      const element = node as Element;
      if (typeof element !== "object" || element[elementTag] !== true) throw new Error("only SDK elements are allowed");
      if (element.type === Fragment) return resolve(element.props.children as Node, element.key ? pathPart(parent, element.key) : parent, depth + 1);
      if (typeof element.type !== "function") throw new Error("unknown JSX element");
      if (!element.key) throw new Error("every component requires an explicit key");
      const path = pathPart(parent, element.key);
      const kind = (element.type as any)[nativeTag] as NativeType | "view" | "section" | undefined;
      if (!kind) {
        if (visited.has(path)) throw new Error("duplicate function component key");
        visited.add(path);
        if (visited.size > this.limits.nodes) throw new Error("function component limit exceeded");
        let instance = this.instances.get(path);
        if (instance && instance.type !== element.type) { this.remove(instance); this.instances.delete(path); instance = undefined; }
        if (!instance) { instance = { id: globalThis.crypto.randomUUID(), type: element.type, hooks: [], initialized: false, active: true }; this.instances.set(path, instance); }
        const result = withHooks(this, instance, () => element.type(element.props));
        return resolve(result, pathPart(path, instance.id), depth + 1);
      }
      const key = addKey(element.key);
      const identity = nativeIdentity(JSON.stringify([path, kind]));
      const { children, ...props } = element.props;
      const allowed: readonly string[] = kind === "view" ? ["title", "state", "refresh_after", "search", "filter", "tabs", "layout", "review"]
        : kind === "section" ? ["title", "footer", "more", "pane"] : fields[kind];
      if (!allowed || Object.keys(props).some((name) => !allowed.includes(name))) throw new Error("unknown native component prop");
      if (kind === "view" || kind === "section") {
        const items = resolve(children as Node, path, depth + 1);
        const expected = kind === "view" ? "section" : "component";
        if (items.some((item) => item.kind !== expected)) throw new Error("invalid native UI nesting");
        const out = lower(props, key, depth + 1, new Set(), JSON.stringify([identity, kind]));
        return [{ kind, key, value: kind === "view" ? { ...out, sections: items.map((item) => item.value) } : { ...out, key, items: items.map((item) => item.value) } }];
      }
      if (children !== undefined) throw new Error("native leaf components do not accept children");
      let out: Record<string, unknown>;
      if (kind === "grid" || kind === "carousel") {
        const { tiles, ...rest } = props;
        if (!Array.isArray(tiles)) throw new Error("tiles must be an array");
        const lowered = lower(rest, key, depth + 1, new Set(), JSON.stringify([identity, kind]));
        // Count the array and every slot before allocation; map skips sparse holes entirely.
        const tileCount = tiles.length;
        if (++propCount > this.limits.props || tileCount > this.limits.props - propCount) throw new Error("UI prop limit exceeded");
        if (tileCount > this.limits.nodes - nodeCount) throw new Error("UI node limit exceeded");
        out = { ...lowered, tiles: Array.from({ length: tileCount }, (_, index) => {
          const tile = tiles[index];
          const tileKey = addKey(tile?.key);
          return lower(tile, tileKey, depth + 1, new Set(), nativeIdentity(JSON.stringify([identity, kind, tileKey])));
        }) };
      } else out = lower(props, key, depth + 1, new Set(), JSON.stringify([identity, kind]));
      return [{ kind: "component", key, value: { ...out, type: kind, key } }];
    };
    try {
      const roots = resolve(this.tree, "", 0);
      if (roots.length !== 1 || roots[0].kind !== "view") throw new Error("one native View root is required");
      const snapshot: Snapshot = { generation: this.generation, version: (this.snapshot?.version ?? 0) + 1, rootKey: roots[0].key, view: roots[0].value };
      if (!Number.isSafeInteger(snapshot.version) || snapshot.version > 9007199254740991) throw new Error("rotate UI generation before revision exhaustion");
      if (encoder.encode(JSON.stringify(snapshot)).byteLength > this.limits.frameBytes) throw new Error("UI frame limit exceeded");
      for (const [path, instance] of this.instances) if (!visited.has(path)) { this.remove(instance); this.instances.delete(path); }
      for (const [controller, id] of this.activeEvents) if (!registrations.has(id)) controller.abort();
      for (const instance of this.instances.values()) for (const hook of instance.hooks) {
        if (hook.kind === "effect") {
          if (hook.snapshot && hook.changed) this.dirtyLoads.add(hook); else this.dirtyLoads.delete(hook);
          if (hook.changed) hook.stop?.();
        }
      }
      this.registrations = registrations;
      this.callbackIDs = new Map([...registrations].map(([id, registration]) => [registration.identity, id]));
      this.nativeNodes = nativeNodes;
      const published = this.snapshot = freeze(snapshot);
      try { this.options.onRender?.(published); } catch (error) { this.report(error); }
      this.commitEffects();
      return published;
    } catch (error) {
      this.dispose();
      throw error;
    } finally { this.rendering = false; this.readiness?.(); }
  }
  /** Read the latest immutable snapshot without triggering rendering or advancing its version. */
  current(): Snapshot {
    if (!this.snapshot || this.disposed) throw new Error("UI session is not mounted");
    return this.snapshot;
  }
  /** Drain only opted-in finite loads and already scheduled renders, never invalidate a cached view.
   * A timeout is a failure, not a successful loading snapshot. One bounded waiter per renderer. */
  async settle(signal: AbortSignal, timeoutMs = 10_000): Promise<Snapshot> {
    if (this.readiness) throw new Error("snapshot readiness already running");
    if (!Number.isFinite(timeoutMs) || timeoutMs <= 0 || timeoutMs > 10_000) throw new Error("invalid snapshot readiness timeout");
    const visibility = this.visibility;
    const deadline = performance.now() + timeoutMs;
    // Bound uninterrupted render work, not the total number of finite resources.
    // Real asynchronous completion gives the next event-loop turn a fresh allowance.
    const workLimit = 512;
    let drains = 0, renders = 0;
    let turnTimer: ReturnType<typeof setTimeout> | undefined;
    let resolve: (() => void) | undefined;
    let failure: Error | undefined;
    const wake = () => resolve?.();
    const check = () => {
      if (signal.aborted || failure) throw failure ?? new Error("snapshot readiness canceled");
      if (this.disposed || this.suspended || this.visibility !== visibility) throw new Error("inactive snapshot readiness");
      if (performance.now() >= deadline) throw new Error("snapshot readiness timeout");
    };
    const guard = () => {
      try {
        check();
        if (++renders > workLimit) throw new Error("snapshot readiness work limit");
        turnTimer ??= globalThis.setTimeout(() => { renders = 0; turnTimer = undefined; }, 0);
      } catch (error) {
        failure = error as Error;
        if (this.visibility === visibility) this.suspend();
        wake();
        throw error;
      }
    };
    const abort = () => { failure = new Error("snapshot readiness canceled"); wake(); };
    const timer = globalThis.setTimeout(() => { failure = new Error("snapshot readiness timeout"); wake(); }, timeoutMs);
    this.readiness = wake;
    this.readinessGuard = guard;
    signal.addEventListener("abort", abort, { once: true });
    try {
      for (;;) {
        if (drains > workLimit) throw new Error("snapshot readiness work limit");
        check();
        // Register before rendering/checking: synchronous load effects can wake and
        // queue another render in this turn, even when the scheduler never drains itself.
        const waiting = new Promise<void>(done => { resolve = done; });
        // Flush an existing invalidation, including custom schedulers, without an extra revision.
        if (this.scheduled) { drains++; this.render(); }
        if (drains > workLimit) throw new Error("snapshot readiness work limit");
        // Effects and publication callbacks can synchronously change visibility.
        // Never return a snapshot from the epoch that was just suspended/resumed.
        check();
        if (this.loadFailed) throw new Error("snapshot loading effect failed");
        if (!this.pendingLoads.size && !this.dirtyLoads.size && !this.scheduled && !this.activeEvents.size) return this.current();
        // Runnable work needs no external wake. The turn ceiling bounds self-invalidation.
        if (this.scheduled) continue;
        await waiting;
        resolve = undefined;
        drains = 0;
      }
    } catch (error) {
      if (this.visibility === visibility) this.suspend();
      throw error;
    } finally {
      globalThis.clearTimeout(timer);
      globalThis.clearTimeout(turnTimer);
      signal.removeEventListener("abort", abort);
      this.readiness = undefined;
      this.readinessGuard = undefined;
    }
  }
  invalidate(): void {
    if (this.disposed || this.suspended || this.scheduled) return;
    this.scheduled = true;
    const ticket = ++this.ticket;
    (this.options.schedule ?? queueMicrotask)(() => {
      if (ticket !== this.ticket || this.disposed || this.suspended) return;
      this.scheduled = false;
      try { this.readinessGuard?.(); this.render(); } catch (error) { this.report(error); }
    });
    this.readiness?.();
  }
  private commitEffects(): void {
    // Commit effects after the published snapshot, independently of invalidation tickets.
    // A state update from one effect must not suppress another effect or its errors.
    for (const instance of this.instances.values()) {
      for (const hook of instance.hooks) {
        if (this.disposed || this.suspended) return;
        if (hook.kind === "effect" && hook.changed) this.runEffect(hook);
      }
    }
  }
  private runEffect(hook: EffectHook): void {
    if (this.pendingEffects >= this.limits.pendingEffects) {
      // Keep the effect dirty so completion of pending work can retry it.
      this.report(new Error("pending effect limit exceeded"));
      return;
    }
    hook.changed = false;
    this.dirtyLoads.delete(hook);
    const snapshot = hook.snapshot;
    const controller = new AbortController();
    let cleanup: (() => void) | undefined;
    let stopped = false;
    const clean = () => { if (cleanup) { const fn = cleanup; cleanup = undefined; try { fn(); } catch (error) { this.report(error); } } };
    const stop = () => {
      if (!stopped) { stopped = true; controller.abort(); clean(); }
      this.pendingLoads.delete(controller);
      this.readiness?.();
    };
    hook.stop = stop;
    const resolved = (result: void | (() => void)) => {
      if (result !== undefined && typeof result !== "function") throw new Error("invalid effect cleanup");
      cleanup = result || undefined;
      if (stopped) clean();
    };
    const failed = (error: unknown) => {
      if (snapshot && !controller.signal.aborted) this.loadFailed = true;
      stop(); this.report(error);
    };
    try {
      const result = hook.effect(controller.signal);
      if (result && typeof (result as Promise<unknown>).then === "function") {
        ++this.pendingEffects;
        if (snapshot && !stopped) this.pendingLoads.add(controller);
        Promise.resolve(result).then(resolved).catch(failed).finally(() => {
          --this.pendingEffects;
          this.pendingLoads.delete(controller);
          this.commitEffects();
          this.readiness?.();
        });
      } else resolved(result as void | (() => void));
    } catch (error) { failed(error); }
  }
  /** Returns and clears bounded local diagnostics. Errors never become user UI text automatically. */
  takeErrors(): unknown[] { const errors = this.errors; this.errors = []; return errors; }
  report(error: unknown): void {
    if (this.errors.length < 64) this.errors.push(error);
    try { this.options.onError?.(error); } catch { /* Error reporting must not create an unhandled rejection. */ }
  }
  private remove(instance: Instance): void {
    instance.active = false;
    for (const hook of instance.hooks) if (hook.kind === "effect") { this.dirtyLoads.delete(hook); hook.stop?.(); }
  }
  private cancelEvents(): void { for (const controller of this.activeEvents.keys()) controller.abort(); }
  /** Only accepts saved callbacks. The manager first authenticates owner/extension/contribution/session.
   * Identical accepted retries replay their original promise, even after a render advances the version.
   * Every invoked/ambiguous callback consumes a native revision. Bounded receipts
   * can then be evicted safely: their old revisions never execute again.
   */
  event(event: UIEvent, controller = new AbortController()): Promise<unknown> {
    try {
      if (this.disposed || this.suspended || !this.snapshot) throw new Error("inactive UI session");
      if (event.generation !== this.generation) throw new Error("stale UI event generation");
      if (typeof event.eventId !== "string" || !event.eventId || event.eventId.length > 256) throw new Error("invalid event ID");
      if (typeof event.key !== "string" || !event.key || event.key.length > 256
        || typeof event.actionId !== "string" || !event.actionId || event.actionId.length > 256) throw new Error("invalid callback key or ID");
      const strings = new StringBudget(this.limits.frameBytes, "UI event frame limit exceeded");
      const saved = freeze({ generation: event.generation, version: event.version, eventId: event.eventId,
        key: event.key, actionId: event.actionId, values: this.eventValues(event.values, strings) });
      const fingerprint = JSON.stringify([saved.generation, saved.version, saved.key, saved.actionId, saved.values]);
      if (encoder.encode(fingerprint).byteLength > this.limits.frameBytes) throw new Error("UI event frame limit exceeded");
      const receipt = this.receipts.get(saved.eventId);
      if (receipt) { if (receipt.fingerprint !== fingerprint) throw new Error("event ID collision: body mismatch"); return receipt.result; }
      if (saved.version !== this.snapshot.version) throw new Error("stale UI event version");
      if (this.snapshot.version >= 9007199254740991) throw new Error("rotate UI generation before revision exhaustion");
      const registration = this.registrations.get(saved.actionId);
      if (!registration || registration.key !== saved.key) throw new Error("unknown or stale callback");
      if (this.activeEvents.size) throw new Error("UI event already running");
      // Old receipts can be evicted safely: every invoked callback consumes a native
      // revision even when it throws or changes no UI. An old body then fails stale.
      while (this.receipts.size >= this.limits.events) this.receipts.delete(this.receipts.keys().next().value!);
      this.activeEvents.set(controller, saved.actionId);
      let invoked = false;
      const result = Promise.resolve().then(async () => {
        if (controller.signal.aborted) throw new Error("stale UI event");
        const allowed = await this.options.authorize(registration.action, saved, controller.signal);
        if (allowed !== true) throw new Error("native action gate not satisfied");
        if (controller.signal.aborted || this.disposed || this.suspended || saved.version !== this.snapshot?.version
          || this.registrations.get(saved.actionId) !== registration) throw new Error("stale UI event");
        let values = saved.values;
        if (registration.action.values) {
          if (!values || typeof values !== "object" || Array.isArray(values)) throw new Error("native action values require a record");
          // Native schema semantics: saved action-carried values win over submitted values.
          values = freeze({ ...values, ...registration.action.values });
        }
        invoked = true;
        const value = await registration.callback.invoke(values, { signal: controller.signal });
        return this.options.transformResult ? this.options.transformResult(value) : value;
      }).finally(() => {
        if (invoked && this.snapshot && this.snapshot.version >= 9007199254740991) this.dispose();
        if (invoked && this.snapshot && !this.disposed) {
          // Rendering may already have advanced several times while awaiting the
          // callback. Consume one more revision; the manager coalesces wire frames.
          this.snapshot = freeze({ ...this.snapshot, version: this.snapshot.version + 1 });
          if (!this.suspended) {
            try { this.options.onRender?.(this.snapshot); } catch (error) { this.report(error); }
          }
        }
        this.activeEvents.delete(controller);
        this.readiness?.();
      });
      this.receipts.set(saved.eventId, { fingerprint, result });
      return result;
    } catch (error) { return Promise.reject(error); }
  }
  private eventValues(value: unknown, strings: StringBudget): unknown {
    let count = 0;
    const ancestors = new Set<object>();
    const copy = (item: unknown, depth: number): unknown => {
      if (++count > this.limits.props || depth > this.limits.depth) throw new Error("UI event value limit exceeded");
      if (typeof item === "string") { strings.add(item); return item; }
      if (item === null || typeof item === "boolean") return item;
      if (typeof item === "number" && Number.isFinite(item)) return item;
      if (!item || typeof item !== "object" || ancestors.has(item)) throw new Error("UI event values must be acyclic JSON");
      ancestors.add(item);
      try {
        if (Array.isArray(item)) {
          if (item.length > this.limits.props - count) throw new Error("UI event value limit exceeded");
          // Visit sparse slots too: undefined is not JSON and must not alias a null retry.
          return Array.from(item, child => copy(child, depth + 1));
        }
        if (Object.getPrototypeOf(item) !== Object.prototype && Object.getPrototypeOf(item) !== null) throw new Error("UI event values must be plain JSON");
        const keys: string[] = [];
        for (const key in item) {
          if (!Object.prototype.hasOwnProperty.call(item, key)) continue;
          if (keys.length >= this.limits.props - count) throw new Error("UI event value limit exceeded");
          strings.add(key);
          keys.push(key);
        }
        return Object.fromEntries(keys.sort().map(key => [key, copy((item as Record<string, unknown>)[key], depth + 1)]));
      } finally { ancestors.delete(item); }
    };
    return copy(value, 0);
  }
  /** Abort effects/fetches/events and clear callbacks immediately; retain only bounded component state. */
  suspend(): void {
    if (this.disposed || this.suspended) return;
    this.suspended = true;
    this.visibility++;
    this.loadFailed = false;
    this.readiness?.();
    this.scheduled = false;
    ++this.ticket;
    this.cancelEvents();
    this.registrations.clear();
    for (const instance of this.instances.values()) for (const hook of instance.hooks) if (hook.kind === "effect") { hook.stop?.(); hook.changed = true; }
  }
  /** Publish a new full snapshot before effects/timers restart. Never replay a stale callback table. */
  resume(): Snapshot {
    if (this.disposed || !this.suspended) throw new Error("renderer cannot resume");
    this.suspended = false;
    return this.render();
  }
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.readiness?.();
    this.scheduled = false;
    ++this.ticket;
    this.cancelEvents();
    for (const instance of this.instances.values()) this.remove(instance);
    this.instances.clear();
    this.nativeNodes.clear();
    this.callbackIDs.clear();
    this.registrations.clear();
    this.receipts.clear();
    this.tree = undefined;
    this.snapshot = undefined;
  }
}
export function mount(tree: Node, options: RendererOptions): Renderer {
  const renderer = new Renderer(options);
  renderer.mount(tree);
  return renderer;
}
