// What the hub says about voice, kept a minute (docs/assistant/voice.md 6.3 "/info cached per group for 60 seconds"): its
// extensions and the voice extension's /info, asked again after that, after a reconnect (a new hello) and when the
// page comes back into view; the last answer stays until the new one comes, so the voice button does not blink.
// Also the model a call starts with (the use `plugin.<id>`, as iOS's VoiceModelPick and Android's resolveVoiceModel).
import { useSyncExternalStore } from "react";
import { api, ApiError } from "../api";
import { listOf, extRoot } from "../extension-api";
import type { HubExtension } from "../extensions";
import { modelPurpose } from "../model-config";
import { onHubEvent } from "../store";
import { pickVoice, readInfo, type VoiceInfoState } from "./availability";
import type { ModelPick } from "./conversation";

export interface VoiceProbe {
  extensions?: readonly HubExtension[];
  info?: VoiceInfoState;
}

export const PROBE_TTL = 60_000;

let probe: VoiceProbe = {};
let askedAt = 0;
let asking = false;
let hello: { public_url?: unknown; endpoints?: unknown } = {};
const listeners = new Set<() => void>();

function publish(next: VoiceProbe) {
  if (JSON.stringify(next) === JSON.stringify(probe)) return;
  probe = next;
  listeners.forEach((l) => l());
}

onHubEvent("hello", (msg) => {
  hello = { public_url: msg.public_url, endpoints: msg.endpoints };
  askedAt = 0; // a new connection (a hub restart, an upgrade): ask again
  if (listeners.size) void refreshVoice();
});
if (typeof document !== "undefined") {
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") askedAt = 0;
  });
}

/** The hub's last hello's addresses (for the HTTPS offer). */
export function helloAddresses() {
  return hello;
}

/** Asks the hub unless its answer is younger than a minute (a failed /info is asked again at once next time). */
export async function refreshVoice(now = Date.now()): Promise<void> {
  if (asking) return;
  if (probe.extensions && now - askedAt < PROBE_TTL && probe.info?.kind !== "failed") return;
  asking = true;
  try {
    let extensions: HubExtension[];
    try {
      extensions = listOf(await api<{ extensions?: HubExtension[]; plugins?: HubExtension[] }>(extRoot()));
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) extensions = [];
      else return;
    }
    const voice = pickVoice(extensions);
    const info = voice ? await askInfo(voice.id) : undefined;
    askedAt = Date.now();
    publish({ extensions, info });
  } finally {
    asking = false;
  }
}

async function askInfo(id: string): Promise<VoiceInfoState> {
  try {
    return { kind: "info", info: readInfo(await api<unknown>(`${extRoot()}/${encodeURIComponent(id)}/info`)) };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return { kind: "missing" };
    if (e instanceof ApiError && e.status === 403) return { kind: "forbidden" };
    return { kind: "failed" };
  }
}

/** Forgets what the hub said (a setting changed): the button waits for the next answer. */
export function forgetVoice() {
  askedAt = 0;
  publish({});
}

/** What the hub said last; asks again when it is old. */
export function useVoiceProbe(): VoiceProbe {
  const p = useSyncExternalStore(subscribe, () => probe, () => probe);
  return p;
}

function subscribe(l: () => void) {
  listeners.add(l);
  void refreshVoice();
  return () => {
    listeners.delete(l);
  };
}

/** The use's status for a usable model nobody has tried yet: not a problem. Matched against what the hub sends. */
const UNVERIFIED = "未验证"; // i18n-ignore: the hub's own status word, compared, never shown

/**
 * The model to send: no configuration (an older hub) or none chosen takes the extension's default; a use whose status
 * says it is not usable stops the call; so does a chosen model the extension no longer lists (only when it lists any).
 */
export function pickModel(choice: { model?: string; status?: string } | null, models: { models?: { id: string }[] } | null): ModelPick {
  if (!choice) return { ok: true };
  const status = choice.status?.trim();
  if (status && status !== UNVERIFIED) return { ok: false, why: status };
  const model = choice.model?.trim();
  if (!model) return { ok: true };
  const listed = models?.models ?? [];
  if (listed.length && !listed.some((m) => m.id === model)) return { ok: false, why: model };
  return { ok: true, model };
}

/** The use `plugin.<id>` and the extension's models, as the call starts. A request that fails here does not stop it
 *  (the extension's default applies, and the socket will say what is wrong). */
export async function resolveModel(extensionID: string): Promise<ModelPick> {
  const [choice, models] = await Promise.all([
    modelPurpose(`plugin.${extensionID}`).then((r) => ({ model: r.purpose.effective?.model, status: r.purpose.status }), () => null),
    api<{ models?: { id: string }[] }>(`${extRoot()}/${encodeURIComponent(extensionID)}/models`).catch(() => null),
  ]);
  return pickModel(choice, models);
}

/** The voices of `model` (none: the extension's default). */
export async function listVoices(extensionID: string, model?: string): Promise<{ voices: { id: string; name?: string }[]; default?: string }> {
  try {
    const r = await api<{ voices?: { id: string; name?: string }[]; default?: string }>(`${extRoot()}/${encodeURIComponent(extensionID)}/voices?model=${encodeURIComponent(model ?? "")}`);
    return { voices: Array.isArray(r.voices) ? r.voices.filter((v) => v && typeof v.id === "string") : [], default: r.default };
  } catch {
    return { voices: [] };
  }
}

const VOICE_KEY = "persona.voice.timbre.";

/** The voice picked in this browser for an extension. */
export function chosenVoice(extensionID: string): string | undefined {
  try {
    return localStorage.getItem(VOICE_KEY + extensionID) || undefined;
  } catch {
    return undefined;
  }
}

export function chooseVoice(extensionID: string, voice: string | undefined) {
  try {
    if (voice) localStorage.setItem(VOICE_KEY + extensionID, voice);
    else localStorage.removeItem(VOICE_KEY + extensionID);
  } catch {
    // private mode: the call still uses it, it is just not remembered
  }
}
