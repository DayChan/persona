// The words around a call that is not on (docs/assistant/voice.md 1.3, 1.2 L; iOS's VoiceNotes.swift): why the page offers
// none, and what stays after one ended by itself ("Voice ended · Continue"). Pure; the extension's or the upstream's own
// words only go under Details (docs/lessons/voice.md "Implementation reminders").
import { t } from "../../i18n";
import type { VoiceAvailability } from "./availability";
import type { VoiceEnd } from "./conversation";

/** Where "Open settings" goes: an in-app path. */
export function extensionSettings(id?: string): string {
  return id ? `/settings?tab=extensions&extension=${encodeURIComponent(id)}` : "/settings?tab=extensions";
}

export function modelSettings(id: string): string {
  return `/settings?tab=model-config&purpose=${encodeURIComponent(`plugin.${id}`)}`;
}

/** Why the voice button cannot start a call here (its click explains), for the cases the user can act on. */
export interface VoiceUnavailableNote {
  title: string;
  message: string;
  /** Settings › Extensions (› the voice extension). */
  settings?: string;
  /** Open this page over HTTPS: the hub's advertised address, when it has one. */
  https?: string;
}

export function unavailableNote(a: VoiceAvailability, https: string | null): VoiceUnavailableNote | null {
  switch (a.kind) {
    case "needsSetup": {
      const why = a.reasons.filter(Boolean).join("\n");
      return { title: t("voice.unavailable_setup_title"), message: why || t("voice.unavailable_setup"), settings: extensionSettings(a.extensionID) };
    }
    case "outdated":
      return { title: t("voice.unavailable_outdated_title"), message: t("voice.unavailable_outdated"), settings: extensionSettings() };
    case "insecure":
      return https
        ? { title: t("voice.unavailable_insecure_title"), message: t("voice.unavailable_insecure_https"), https }
        : { title: t("voice.unavailable_insecure_title"), message: t("voice.unavailable_insecure") };
    case "noAudio":
      return { title: t("voice.unavailable_browser_title"), message: t("voice.unavailable_browser") };
    default:
      return null;
  }
}

/** What stays where the bar was after a call ended by itself, until the page leaves the conversation. */
export interface VoiceEndNote {
  text: string;
  /** Continue after an ending that may just pass, Retry after a failure; none when trying again cannot help. */
  again: "resume" | "retry" | null;
  settings?: string;
  /** The extension's or the upstream's own words, behind Details. */
  details?: string;
}

/** Null for the user's own endings (End, leaving the conversation, another call): nothing stays. */
export function endNote(end: VoiceEnd, extensionID: string): VoiceEndNote | null {
  const note = (text: string, again: VoiceEndNote["again"] = "resume", extra: Partial<VoiceEndNote> = {}): VoiceEndNote => ({ text, again, ...extra, details: extra.details || undefined });
  switch (end.kind) {
    case "user":
    case "left":
    case "replaced":
      return null;
    case "interrupted":
      return note(t("voice.end_interrupted"));
    case "permissionDenied":
      return note(t("voice.end_permission"), "retry");
    case "noMicrophone":
      return note(t("voice.end_no_microphone"), "retry");
    case "audioFailed":
      return note(t("voice.end_audio_failed"), "retry");
    case "idle":
      return note(t("voice.end_idle"));
    case "lost":
      return note(t("voice.end_lost"));
    case "rejected":
      if (end.status === 401) return note(t("voice.end_rejected_token"), null);
      if (end.status === 403) return note(t("voice.end_rejected_limited"), null);
      return note(t("voice.end_rejected_missing"), "retry", { settings: extensionSettings() });
    case "incompatible":
      return note(t("voice.end_incompatible"), null);
    case "modelUnavailable":
      return note(t("voice.end_model"), "retry", { settings: modelSettings(extensionID), details: end.why });
    case "signedOut":
      return note(t("voice.end_signed_out"), null);
    case "failed":
      switch (end.code) {
        case "upstream_forbidden":
          return note(t("voice.end_upstream_forbidden"), "retry", { settings: extensionSettings(extensionID), details: end.message });
        case "upstream_unavailable":
          return note(t("voice.end_upstream_unavailable"), "retry", { details: end.message });
        case "upstream_failed":
          return note(t("voice.end_upstream_failed"), "retry", { details: end.message });
        case "no_upstream":
          return note(t("voice.unavailable_setup_title"), "retry", { settings: extensionSettings(extensionID), details: end.message });
        case "target_gone":
        case "target_read_only":
          return note(t("voice.end_target"), null, { details: end.message });
        case "target_forbidden":
          return note(t("voice.end_target_forbidden"), null, { details: end.message });
        case "model_unavailable":
          return note(t("voice.end_model"), "retry", { settings: modelSettings(extensionID), details: end.message });
        case "machine_offline":
          return note(t("voice.end_machine_offline"), "retry", { details: end.message });
        default:
          return note(t("voice.end_failed"), "retry", { details: end.message });
      }
  }
}
