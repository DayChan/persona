// A rehype plugin kept out of the main bundle (highlight.ts, math.ts): loaded the first time a reply needs it, then
// shared by every reply. A failed chunk (the hub was redeployed under the page) is asked for again by the next one.
// It is asked for once the page is idle, not in the frame that first draws the reply (opening a chat that has code
// would fetch and run the grammars alongside the chat's own chunks), and the replies take it up in a transition: each
// one re-renders, and a keystroke or the chat's own updates go first. Until then they draw without it.
import { startTransition, useEffect, useRef, useState } from "react";
import { whenIdle } from "./idle";

export interface LazyPlugin<P> {
  /** Loads it (once); resolves with the plugin. */
  load: () => Promise<P>;
  /** The plugin once loaded, when `wanted` (asking for it then); null otherwise, so a reply that has no use for it is not
   *  drawn again when it arrives. */
  use: (wanted: boolean) => P | null;
  /** Asks for it once the page is idle when `wanted`, without drawing the caller again when it arrives (code blocks
   *  take the highlighter up themselves, highlight.ts). */
  ask: (wanted: boolean) => void;
  /** The plugin, or null while it is not loaded. */
  loaded: () => P | null;
  /** Calls `listener` when it arrives; returns the unsubscribe. */
  subscribe: (listener: () => void) => () => void;
}

/** The longest a reply waits for an idle moment before its plugin is asked for. */
const IDLE_MS = 1500;

export function lazyPlugin<P>(importer: () => Promise<P>): LazyPlugin<P> {
  let plugin: P | null = null;
  let loading: Promise<P> | null = null;
  const listeners = new Set<() => void>();
  const load = () =>
    (loading ??= importer().then(
      (p) => {
        plugin = p;
        listeners.forEach((l) => l());
        return p;
      },
      (e) => {
        loading = null;
        throw e;
      },
    ));
  function use(wanted: boolean): P | null {
    // a plugin is a function: useState would call it
    const [ready, setReady] = useState<P | null>(() => plugin);
    // Set while the reply waits for it: it takes it up in that transition only. A render of its own before the transition
    // commits (a store's update is sync: path-presence's answer) draws it without, or every waiting reply would be parsed
    // again with it in that one frame, half a second for a long chat with formulas (#2905).
    const waiting = useRef(false);
    useEffect(() => {
      if (!wanted || ready) return;
      if (plugin) {
        setReady(() => plugin);
        return;
      }
      const took = () => startTransition(() => setReady(() => plugin));
      listeners.add(took);
      waiting.current = true;
      const cancel = whenIdle(() => void load().catch(() => {}), IDLE_MS);
      return () => {
        waiting.current = false;
        listeners.delete(took);
        cancel();
      };
    }, [wanted, ready]);
    return wanted ? (ready ?? (waiting.current ? null : plugin)) : null;
  }
  function ask(wanted: boolean): void {
    useEffect(() => {
      if (!wanted || plugin) return;
      return whenIdle(() => void load().catch(() => {}), IDLE_MS);
    }, [wanted]);
  }
  const subscribe = (listener: () => void) => {
    listeners.add(listener);
    return () => void listeners.delete(listener);
  };
  return { load, use, ask, loaded: () => plugin, subscribe };
}
