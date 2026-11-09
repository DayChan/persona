// The page's voice call: at most one at a time, bound to the conversation page that started it (docs/assistant/voice.md
// 6.3, 6.5; docs/web/web.md "Voice"). A new call ends the one before; leaving the conversation (or closing the tab) ends
// it; a hidden tab keeps it (desktop multitasking) unless the browser suspends its audio. ChatView reads only whether a
// call is on here; the bar and the thread's tail read the call itself.
import { useSyncExternalStore } from "react";
import { getToken } from "../api";
import { BASE } from "../base";
import { extRoot } from "../extension-api";
import { getSession } from "../store";
import { BrowserVoiceAudio } from "./audio";
import { endedByUser, VoiceConversation, type VoiceDeps, type VoiceSnapshot } from "./conversation";
import { chosenVoice, resolveModel } from "./support";
import { voiceSocketURL, type VoiceLine } from "./wire";

/** Which conversation page a call belongs to: the conversation, and the page that started it (the same conversation
 *  can be open twice, as a page and in a side panel; leaving one must not end the other's call). */
export interface VoiceOwner {
  mid: string;
  sid: string;
  owner: string;
}

interface Current {
  call: VoiceConversation;
  owner: string;
  /** Whether it ended: changes only as it ends or is picked up again, so the page swaps the bar and the composer
   *  without drawing again on every caption. */
  over: boolean;
}

let current: Current | null = null;
const listeners = new Set<() => void>();
/** Told of every change of the current call too (its captions, runs, the card it asks about). */
const deep = new Set<() => void>();
let unwatch: (() => void) | null = null;

function emit() {
  listeners.forEach((l) => l());
}

function setCurrent(next: Current | null) {
  unwatch?.();
  unwatch = null;
  current = next;
  if (next) {
    unwatch = next.call.subscribe(() => {
      deep.forEach((l) => l());
      if (current?.call !== next.call || current.over === next.call.over) return;
      current = { ...current, over: next.call.over };
      // a call that ended by the user's own doing leaves nothing behind
      const phase = next.call.getSnapshot().phase;
      if (phase.kind === "ended" && endedByUser(phase.end)) setCurrent(null);
      emit();
    });
  }
  emit();
}

/** The browser's side of a call: its socket on this page's origin with this page's credential, its audio, the hub. */
export function browserDeps(mid: string, sid: string, extensionID: string): VoiceDeps {
  return {
    socketURL: () => {
      const token = getToken();
      return token ? voiceSocketURL(location, BASE, extRoot(), extensionID, token) : null;
    },
    openSocket: (url) => new WebSocket(url) as unknown as ReturnType<VoiceDeps["openSocket"]>,
    audio: (outputRate) => new BrowserVoiceAudio(outputRate),
    probe: async () => {
      try {
        // fetch, not api(): a 401 here is the call's to report, not a sign-out of the whole page
        const r = await fetch(`${BASE}/api/v1${extRoot()}/${encodeURIComponent(extensionID)}/info`, { headers: { Authorization: `Bearer ${getToken()}` } });
        return r.status;
      } catch {
        return null;
      }
    },
    resolveModel: () => resolveModel(extensionID),
    voice: () => chosenVoice(extensionID),
    session: () => getSession(mid, sid),
    now: () => Date.now(),
    setTimer: (fn, ms) => setTimeout(fn, ms),
    clearTimer: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
  };
}

export const voiceCalls = {
  /** Starts a call on `o`'s conversation (synchronously up to its audio: call it inside the click). The same page's
   *  ended call is picked up again; any other call ends first. */
  start(o: VoiceOwner & { extensionID: string; recent: VoiceLine[]; deps?: VoiceDeps }): VoiceConversation {
    if (current) {
      const c = current.call;
      if (c.mid === o.mid && c.sid === o.sid && c.extensionID === o.extensionID && current.owner === o.owner) {
        if (c.over) void c.restart();
        return c;
      }
      c.end({ kind: "replaced" });
      setCurrent(null);
    }
    const call = new VoiceConversation({ mid: o.mid, sid: o.sid, extensionID: o.extensionID, recent: o.recent, deps: o.deps ?? browserDeps(o.mid, o.sid, o.extensionID) });
    setCurrent({ call, owner: o.owner, over: false });
    void call.begin();
    return call;
  },

  /** End tapped: ends the call, whichever page shows it. */
  end() {
    current?.call.end({ kind: "user" });
    setCurrent(null);
  },

  /** Continue / Retry on the ended call of this page (inside the click: it makes the audio again). */
  resume(o: VoiceOwner) {
    if (current && bound(current, o) && current.call.over) void current.call.restart();
  },

  /** The page left the conversation (another one, another page, the tab closing): its call ends, and its Continue row
   *  goes with it. */
  leave(o: VoiceOwner) {
    if (!current || !bound(current, o)) return;
    current.call.end({ kind: "left" });
    setCurrent(null);
  },

  /** The call bound to this page, live or ended. */
  callOn(o: VoiceOwner): VoiceConversation | null {
    return current && bound(current, o) ? current.call : null;
  },

  /** Any call now (the tests, and the tab closing). */
  get current(): VoiceConversation | null {
    return current?.call ?? null;
  },
};

function bound(c: Current, o: VoiceOwner): boolean {
  return c.call.mid === o.mid && c.call.sid === o.sid && c.owner === o.owner;
}

if (typeof window !== "undefined") {
  // closing or reloading the tab: hang up properly rather than leave the extension to notice a dead socket
  window.addEventListener("pagehide", () => {
    current?.call.end({ kind: "left" });
  });
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

/** Whether this page has a call: "live", "ended" (its Continue row), or null. Changes only as a call starts, ends or
 *  picks up again. */
export function useVoiceState(o: VoiceOwner): "live" | "ended" | null {
  const get = () => (current && bound(current, o) ? (current.over ? "ended" : "live") : null);
  return useSyncExternalStore(subscribe, get, get);
}

/** The call bound to this page (changes as calls come and go, not as they talk). */
export function useVoiceCall(o: VoiceOwner): VoiceConversation | null {
  const get = () => (current && bound(current, o) ? current.call : null);
  return useSyncExternalStore(subscribe, get, get);
}

const NONE = { subscribe: () => () => {}, getSnapshot: () => null };

/** What the call says now (every caption, run and state): only the bar and the thread's tail read it. */
export function useVoiceSnapshot(call: VoiceConversation | null): VoiceSnapshot | null {
  const src = call ?? NONE;
  return useSyncExternalStore(src.subscribe, src.getSnapshot, src.getSnapshot);
}

/** Whether the voice is asking this card's question now (the `ask` frame): the card says so, so a click and an answer
 *  said aloud are not taken for two questions (docs/assistant/voice.md 1.2 H). */
export function useVoiceAsking(ix: { id: string; machine_id: string; session_id: string }): boolean {
  const get = () => {
    const call = current?.call;
    if (!call || call.over || call.mid !== ix.machine_id || call.sid !== ix.session_id) return false;
    return call.getSnapshot().askingID === ix.id;
  };
  return useSyncExternalStore(subscribeDeep, get, get);
}

function subscribeDeep(l: () => void) {
  listeners.add(l);
  deep.add(l);
  return () => {
    listeners.delete(l);
    deep.delete(l);
  };
}
