// Whether the ports an answer links are still served on its computer (docs/operations/ports.md "Recognizing links in
// replies"; iOS PortPresence): a port an agent on that computer lists (Session.ports) is live with its page's title;
// any other is asked about (GET /machines/{mid}/ports/{port}, nothing is opened) as its chip shows, and remembered a
// little while: a live answer for a minute, a gone one for 30 s (the agent may be starting it again), no answer for
// 20 s (an old hub, a hiccup), so scrolling a chat back and forth asks once. Answers with no result are dropped when
// the hub says hello after a reconnect.
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { api, enc } from "./api";
import { onHubEvent, useStore } from "./store";
import type { Port } from "./types";

export type PortState = { kind: "live"; title?: string } | { kind: "gone" };

const LIFETIME = { live: 60e3, gone: 30e3, none: 20e3 };
const CAP = 500; // answers kept; past it the oldest go

type Entry = { state?: PortState; at: number };

const known = new Map<string, Entry>();
const asking = new Set<string>();
const listeners = new Set<() => void>();
let version = 0;
let generation = 0; // bumped by resetPortPresence: answers to what was asked before it are dropped

const key = (mid: string, port: number) => `${mid}\n${port}`;

function fresh(e: Entry | undefined, now: number) {
  return !!e && now - e.at < LIFETIME[e.state?.kind ?? "none"];
}

function changed() {
  version++;
  listeners.forEach((l) => l());
}

async function ask(mid: string, port: number) {
  const gen = generation;
  const k = key(mid, port);
  asking.add(k);
  let state: PortState | undefined;
  try {
    const r = await api<{ listening?: boolean; title?: string }>(`/machines/${enc(mid)}/ports/${port}`);
    state = r.listening ? { kind: "live", title: r.title || undefined } : { kind: "gone" };
  } catch {
    // no answer: the chip says nothing either way, asked again in a little while or when the hub is back
  }
  if (gen !== generation) return;
  asking.delete(k);
  known.delete(k); // re-inserted last: Map order is then oldest answer first
  known.set(k, { state, at: Date.now() });
  for (const old of known.keys()) {
    if (known.size <= CAP) break;
    known.delete(old);
  }
  changed();
}

/** Asks about the ports of mid that no agent lists and that are not known (or known too long ago). */
export function askPorts(mid: string, ports: number[], listed: readonly Port[]) {
  const now = Date.now();
  for (const port of ports) {
    const k = key(mid, port);
    if (listed.some((p) => p.port === port) || asking.has(k) || fresh(known.get(k), now)) continue;
    void ask(mid, port);
  }
}

/** A port's state: listed by an agent on mid, else what the hub said last; undefined while unknown. */
export function portState(mid: string, port: number, listed: readonly Port[]): PortState | undefined {
  const p = listed.find((x) => x.port === port);
  if (p) return { kind: "live", title: p.title || undefined };
  return known.get(key(mid, port))?.state;
}

onHubEvent("hello", () => {
  let dropped = false;
  for (const [k, e] of known) {
    if (e.state) continue;
    known.delete(k);
    dropped = true;
  }
  if (dropped) changed();
});

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** The ports the agents on mid list (Session.ports of each of its sessions), one entry per port. */
export function listedPorts(sessions: readonly { ports?: Port[] }[] | undefined): Port[] {
  const out: Port[] = [];
  for (const s of sessions ?? []) for (const p of s.ports ?? []) if (!out.some((x) => x.port === p.port)) out.push(p);
  return out;
}

/** What is known of each of `ports` on mid, asking about the ones no agent lists; re-renders when answers come. */
export function usePortStates(mid: string, ports: number[]): (port: number) => PortState | undefined {
  const v = useSyncExternalStore(subscribe, () => version, () => version);
  const listed = useStore((s) => listedPorts(s.sessions[mid]));
  const joined = ports.join(",");
  useEffect(() => {
    if (joined) askPorts(mid, joined.split(",").map(Number), listed);
  }, [mid, joined, listed, v]);
  return useMemo(() => (port: number) => portState(mid, port, listed), [mid, listed, v]); // eslint-disable-line react-hooks/exhaustive-deps
}

/** Forget everything (tests). */
export function resetPortPresence() {
  known.clear();
  asking.clear();
  generation++;
  version++;
}
