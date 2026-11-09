// Whether a conversation's page offers Voice chat, and if not why (docs/assistant/voice.md 3.1, 6.1; docs/web/web.md "Voice").
// Pure: the hub's answers (the extension list, the voice extension's /info) and what the browser can do come in, the
// verdict goes out. The browser's own limit comes last: there is nothing to say about HTTPS on a hub without voice.
import { sessionCan } from "../format";
import type { HubExtension } from "../extensions";
import type { Session } from "../types";
import { validExtensionID } from "./wire";

/** The extension's `GET /info` (docs/assistant/voice.md 5.6): which protocol it speaks and what it still needs. */
export interface VoiceInfo {
  protocol: number;
  features: string[];
  /** What it still needs before it can talk (no upstream address or key), in words; empty once it is set up. */
  setup: string[];
}

/** What asking `/info` came to. */
export type VoiceInfoState =
  | { kind: "info"; info: VoiceInfo }
  /** 404: the hub or the extension is from before protocol 2. */
  | { kind: "missing" }
  /** 403: the hub does not let this browser's credential reach extensions (it may only answer). */
  | { kind: "forbidden" }
  /** Anything else (offline, 503 while the hub's assistant process is away): asked again later. */
  | { kind: "failed" };

/** What the browser can do for a call. */
export interface VoiceBrowser {
  /** `window.isSecureContext`: browsers give the microphone only to HTTPS pages and to this computer itself. */
  secure: boolean;
  /** `navigator.mediaDevices.getUserMedia` and `AudioWorkletNode` are there. */
  audio: boolean;
}

export type VoiceAvailability =
  | { kind: "available"; extensionID: string }
  /** No conversation to bind (the assistant's home before there is one). */
  | { kind: "noConversation" }
  /** The conversation cannot take messages from here (a read-only source). */
  | { kind: "readOnly" }
  /** The hub has no voice extension running (none, switched off, or a hub too old to list them). */
  | { kind: "noExtension" }
  /** The voice extension runs but has no upstream yet (no address or key). */
  | { kind: "needsSetup"; extensionID: string; reasons: string[] }
  /** The extension is from before protocol 2 (no /info, or a lower protocol): the web has no protocol 1 client. */
  | { kind: "outdated"; extensionID: string }
  /** This credential may only answer: the hub refuses it the extension. */
  | { kind: "limitedDevice" }
  /** Not known yet (being asked, or the last ask failed). */
  | { kind: "unknown" }
  /** The page is not a secure context (plain HTTP on another address): no microphone. */
  | { kind: "insecure"; extensionID: string }
  /** A browser without the microphone or audio APIs a call needs. */
  | { kind: "noAudio"; extensionID: string };

/** The extension a call talks to: the first with kind "voice" that runs (as iOS, Android and the hub's channel list). */
export function pickVoice(extensions: readonly HubExtension[]): HubExtension | undefined {
  return extensions.find((e) => e.kind === "voice" && e.status === "running" && validExtensionID(e.id));
}

/** `GET /info`'s body, every field optional (`setup` one reason or a list). */
export function readInfo(body: unknown): VoiceInfo {
  const o = (body && typeof body === "object" ? body : {}) as Record<string, unknown>;
  const protocol = typeof o.protocol === "number" && Number.isSafeInteger(o.protocol) && o.protocol > 0 ? o.protocol : 1;
  const features = Array.isArray(o.features) ? o.features.filter((f): f is string => typeof f === "string") : [];
  const setup = typeof o.setup === "string" ? (o.setup ? [o.setup] : []) : Array.isArray(o.setup) ? o.setup.filter((s): s is string => typeof s === "string" && s !== "") : [];
  return { protocol, features, setup };
}

/**
 * Whether `session`'s page offers a call. `extensions`: the hub's list (undefined while not known); `info`: the picked
 * extension's /info. A call hands things over only where typing could (the composer's own rule, `sessionCan(…, "send")`).
 */
export function evaluate(o: { session: Session | undefined; extensions: readonly HubExtension[] | undefined; info: VoiceInfoState | undefined; browser: VoiceBrowser }): VoiceAvailability {
  if (!o.session) return { kind: "noConversation" };
  if (!sessionCan(o.session, "send")) return { kind: "readOnly" };
  if (!o.extensions) return { kind: "unknown" };
  const voice = pickVoice(o.extensions);
  if (!voice) {
    const waiting = o.extensions.find((e) => e.kind === "voice" && e.status === "needs_setup" && validExtensionID(e.id));
    return waiting ? { kind: "needsSetup", extensionID: waiting.id, reasons: waiting.needs_setup ?? [] } : { kind: "noExtension" };
  }
  const extensionID = voice.id;
  switch (o.info?.kind) {
    case undefined:
    case "failed":
      return { kind: "unknown" };
    case "forbidden":
      return { kind: "limitedDevice" };
    case "missing":
      return { kind: "outdated", extensionID };
    case "info":
      if (o.info.info.protocol < 2) return { kind: "outdated", extensionID };
      if (o.info.info.setup.length) return { kind: "needsSetup", extensionID, reasons: o.info.info.setup };
  }
  if (!o.browser.secure) return { kind: "insecure", extensionID };
  if (!o.browser.audio) return { kind: "noAudio", extensionID };
  return { kind: "available", extensionID };
}

/**
 * What the composer shows: `start` a call, `explain` why not (something the user can act on: set the extension up,
 * update it, open the hub over HTTPS, another browser), or nothing (`null`: no voice on this hub, a credential that may
 * only answer, a read-only chat, not known yet). The web's "+" picks files straight away (no menu, as on iOS), so the
 * explanation is the voice button's own; with no voice extension the composer stays as it was.
 */
export function entryMode(a: VoiceAvailability): "start" | "explain" | null {
  switch (a.kind) {
    case "available":
      return "start";
    case "needsSetup":
    case "outdated":
    case "insecure":
    case "noAudio":
      return "explain";
    default:
      return null;
  }
}

/** What the browser can do now. */
export function browserVoice(w: { isSecureContext?: boolean; navigator?: { mediaDevices?: { getUserMedia?: unknown } }; AudioWorkletNode?: unknown } = globalThis as never): VoiceBrowser {
  return {
    secure: w.isSecureContext === true,
    audio: typeof w.navigator?.mediaDevices?.getUserMedia === "function" && typeof w.AudioWorkletNode === "function",
  };
}

/** One address the hub advertises (hello `endpoints`). */
export interface HubEndpoint {
  url?: string;
  kind?: string;
  scheme?: string;
}

/**
 * Where this page can be opened over HTTPS, the route kept, when the hub advertises such an address: its `public_url`
 * when that is https (a reverse proxy with a real certificate), else an advertised https address by name, else any
 * advertised https address (the hub's own TLS: the browser may warn about its certificate). Null when there is none,
 * or when this page already is that address.
 */
export function httpsAddress(hello: { public_url?: unknown; endpoints?: unknown }, here: { origin: string; pathname: string; search: string; hash: string }, base: string): string | null {
  const parse = (raw: unknown): URL | null => {
    if (typeof raw !== "string" || !raw) return null;
    try {
      const u = new URL(raw);
      return u.protocol === "https:" ? u : null;
    } catch {
      return null;
    }
  };
  const endpoints = (Array.isArray(hello.endpoints) ? hello.endpoints : []) as HubEndpoint[];
  const named = endpoints.filter((e) => e && typeof e === "object" && e.kind === "name").map((e) => parse(e.url));
  const others = endpoints.filter((e) => e && typeof e === "object" && e.kind !== "name").map((e) => parse(e.url));
  const pick = [parse(hello.public_url), ...named, ...others].find((u): u is URL => u !== null && u.origin !== here.origin);
  if (!pick) return null;
  // The public URL may carry the sub-path the hub is served under; an endpoint is the hub's root, under which this
  // page's own sub-path does not apply.
  const prefix = pick.pathname.replace(/\/+$/, "");
  const route = here.pathname.startsWith(base) ? here.pathname.slice(base.length) || "/" : here.pathname;
  return `${pick.origin}${prefix}${route}${here.search}${here.hash}`;
}
