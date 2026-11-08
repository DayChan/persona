import type { Node } from "./components.ts";

/** Synchronous, session-local cells; a future execution-location adapter can implement this boundary. */
export interface StateCell<T> { get(): T; set(value: T): void }
export interface StateAdapter { create<T>(initial: T): StateCell<T> }
export interface TimerAdapter {
  setInterval(fn: () => void, milliseconds: number): unknown;
  clearInterval(handle: unknown): void;
}
export type FetchAdapter = (input: string, init: RequestInit & { signal: AbortSignal }) => Promise<Response>;
export type Cleanup = () => void;
export type Effect = (signal: AbortSignal) => void | Cleanup | Promise<void | Cleanup>;
export type Dependencies = readonly unknown[];
export interface StateHook { kind: "state"; cell: StateCell<any> }
export interface EffectHook {
  kind: "effect";
  effect: Effect;
  deps: Dependencies | undefined;
  changed: boolean;
  /** Only explicitly finite loading effects participate in snapshot readiness. */
  snapshot?: boolean;
  stop?: Cleanup;
}
interface LatestHook { kind: "latest"; value: unknown }
interface IntervalHook { kind: "interval"; running: boolean }
interface FetchHook { kind: "fetch"; deps: Dependencies; identity: number }
export type Hook = StateHook | EffectHook | LatestHook | IntervalHook | FetchHook;
export interface Instance {
  id: string;
  type: (props: any) => Node;
  hooks: Hook[];
  initialized: boolean;
  active: boolean;
}
export interface HookOwner {
  readonly state: StateAdapter;
  readonly timers: TimerAdapter;
  readonly fetch: FetchAdapter;
  readonly disposed: boolean;
  readonly suspended: boolean;
  readonly maxHooks: number;
  invalidate(): void;
  report(error: unknown): void;
}
interface Context { owner: HookOwner; instance: Instance; index: number }
// This is a synchronous render stack, never a registry of sessions or extension data.
let context: Context | undefined;
export function withHooks(owner: HookOwner, instance: Instance, render: () => Node): Node {
  const previous = context;
  const current = { owner, instance, index: 0 };
  context = current;
  try {
    const node = render();
    if (instance.initialized && current.index !== instance.hooks.length) throw new Error("hook order changed: fewer hooks");
    instance.initialized = true;
    return node;
  } finally { context = previous; }
}
function hook<T extends Hook>(kind: T["kind"], create: () => T): [T, Context] {
  if (!context) throw new Error("hooks require a mounted function component");
  const { instance, index, owner } = context;
  if (index >= owner.maxHooks) throw new Error("component hook limit exceeded");
  context.index++;
  let slot = instance.hooks[index];
  if (!slot) {
    if (instance.initialized) throw new Error("hook order changed: more hooks");
    slot = create();
    instance.hooks.push(slot);
  }
  if (slot.kind !== kind) throw new Error("hook order changed: different hook");
  return [slot as T, context];
}
export function useState<T>(initial: T | (() => T)): [T, (value: T | ((previous: T) => T)) => void] {
  const [slot, { owner, instance }] = hook<StateHook>("state", () => ({ kind: "state", cell: context!.owner.state.create(typeof initial === "function" ? (initial as () => T)() : initial) }));
  return [slot.cell.get(), (update) => {
    if (owner.disposed || !instance.active) return;
    const previous = slot.cell.get() as T;
    const next = typeof update === "function" ? (update as (previous: T) => T)(previous) : update;
    if (!Object.is(previous, next)) { slot.cell.set(next); owner.invalidate(); }
  }];
}
function equalDeps(a: Dependencies | undefined, b: Dependencies | undefined): boolean {
  return a !== undefined && b !== undefined && a.length === b.length && a.every((item, index) => Object.is(item, b[index]));
}
function effectHook(effect: Effect, dependencies: Dependencies | undefined, snapshot: boolean): void {
  const [slot] = hook<EffectHook>("effect", () => ({ kind: "effect", effect, deps: dependencies?.slice(), changed: true, snapshot }));
  if (!equalDeps(slot.deps, dependencies) || slot.snapshot !== snapshot) slot.changed = true;
  slot.effect = effect;
  slot.deps = dependencies?.slice();
  slot.snapshot = snapshot;
}
export function useEffect(effect: Effect, dependencies?: Dependencies): void {
  effectHook(effect, dependencies, false);
}
/** Finite UI loading only: snapshot transports wait for the returned promise, within a host bound.
 * Return all loading work; handle failures as UI state and honor signal before writing state.
 * Subscriptions, timers and detached background work belong in useEffect instead. */
export function useLoadEffect(effect: Effect, dependencies?: Dependencies): void {
  effectHook(effect, dependencies, true);
}
function useLatest<T>(value: T): LatestHook & { value: T } {
  const [slot] = hook<LatestHook>("latest", () => ({ kind: "latest", value }));
  slot.value = value;
  return slot as LatestHook & { value: T };
}

export interface FetchState<T> { status: "idle" | "loading" | "success" | "error"; data?: T; error?: unknown }
export interface FetchOptions<T> {
  init?: Omit<RequestInit, "signal">;
  decode?: (response: Response) => T | Promise<T>;
  /** Include every request/decode input other than the URL that can change. */
  dependencies?: Dependencies;
  /** Opt in to finite snapshot loading; streaming behavior is unchanged. */
  snapshot?: "wait";
}
export function useFetch<T = unknown>(input: string | null, options: FetchOptions<T> = {}): FetchState<T> {
  if (!context) throw new Error("hooks require a mounted function component");
  const owner = context.owner;
  const dependencies = [input, ...(options.dependencies ?? [])];
  const [resource] = hook<FetchHook>("fetch", () => ({ kind: "fetch", deps: dependencies, identity: 0 }));
  if (!equalDeps(resource.deps, dependencies)) { resource.deps = dependencies; resource.identity++; }
  const identity = resource.identity;
  const current: FetchState<T> = { status: input === null ? "idle" : "loading" };
  const [saved, setSaved] = useState(() => ({ identity, result: current }));
  const setResult = (result: FetchState<T>) => {
    if (resource.identity === identity) setSaved({ identity, result });
  };
  const loadEffect = options.snapshot === "wait" ? useLoadEffect : useEffect;
  loadEffect(async (signal) => {
    if (input === null) { setResult({ status: "idle" }); return; }
    setResult({ status: "loading" });
    try {
      const response = await owner.fetch(input, { ...options.init, signal });
      if (signal.aborted) return;
      if (!response.ok) throw new Error(`fetch status ${response.status}`);
      const data = options.decode ? await options.decode(response) : await response.json() as T;
      if (!signal.aborted) setResult({ status: "success", data });
    } catch (error) {
      if (!signal.aborted) setResult({ status: "error", error });
    }
  }, dependencies);
  // Derive the first snapshot for a new resource; state writes and fetches remain commit effects.
  return saved.identity === identity ? saved.result : current;
}

/** UI intervals are effects: hidden, unmounted and disposed pages have no running timer. */
export function useInterval(fn: (signal: AbortSignal) => void | Promise<void>, milliseconds: number | null): void {
  if (!context) throw new Error("hooks require a mounted function component");
  const owner = context.owner;
  const latest = useLatest(fn);
  // Keep the overlap guard across suspend/resume, not inside each timer generation.
  const [work] = hook<IntervalHook>("interval", () => ({ kind: "interval", running: false }));
  useEffect((signal) => {
    if (milliseconds === null) return;
    if (!Number.isFinite(milliseconds) || milliseconds <= 0) throw new Error("interval must be positive and finite");
    const handle = owner.timers.setInterval(() => {
      if (signal.aborted || work.running || owner.suspended || owner.disposed) return;
      work.running = true;
      Promise.resolve().then(() => {
        if (!signal.aborted) return latest.value(signal);
      }).catch((error) => owner.report(error)).finally(() => { work.running = false; });
    }, milliseconds);
    return () => owner.timers.clearInterval(handle);
  }, [milliseconds]);
}
