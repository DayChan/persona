// What a toast becomes before it shows (ui/atoms toast), after the app's AppModel.show / notDone and Android's
// FeedbackGate: while the hub is out of reach an action that failed *because* of it is one calm line at most every
// few seconds (the pill already says why) instead of a red error per tap; any other error (a typo in a form, a
// clipboard write) keeps its own words and stays red. The same line again within a few seconds is folded.
import { t } from "../i18n";

export type Tone = "info" | "error" | "ok";
/** Why the hub is out of reach right now, or null when it is not. */
export type Offline = "network" | "hub" | null;

/** How long a repeat of the same line, or a second "didn't go through", stays folded. */
export const FOLD_MS = 4_000;

let probe: () => Offline = () => null;

/** The store says whether the hub is reachable (lib/store.ts registers it; nothing registered: always reachable). */
export function setOfflineProbe(p: () => Offline) {
  probe = p;
}

export function offlineNow(): Offline {
  return probe();
}

/**
 * Whether an error's text says the request never reached the hub: errorText's line for a failed fetch (TypeError,
 * often inside "send failed: …" in Chinese). An HTTP gateway status can also be a business refusal
 * from a manager, so is not evidence that the hub is unreachable. The app folds only `error.isOffline` the same way.
 */
export function isOfflineError(text: string): boolean {
  return text.includes(t("lib-api.cant_reach_hub"));
}

export interface Shown {
  text: string;
  tone: Tone;
}

/** The gate's memory: what was shown last and when, and when the last "didn't go through" was. */
export class FeedbackGate {
  private last: { text: string; tone: Tone; at: number } | null = null;
  private lastNotDone = -Infinity;

  /** What to show for (text, tone) at `now` given the connection, or null to show nothing. `offlineError`: the
   *  error is the hub being out of reach (default: its text says so, isOfflineError). */
  pass(text: string, tone: Tone, offline: Offline, now: number, offlineError = isOfflineError(text)): Shown | null {
    if (tone === "error" && offline && offlineError) {
      if (now - this.lastNotDone < FOLD_MS) return null;
      this.lastNotDone = now;
      return this.remember(t(offline === "network" ? "lib-connection.not_done_network" : "lib-connection.not_done_hub"), "info", now);
    }
    if (this.last && this.last.text === text && this.last.tone === tone && now - this.last.at < FOLD_MS) return null;
    return this.remember(text, tone, now);
  }

  private remember(text: string, tone: Tone, now: number): Shown {
    this.last = { text, tone, at: now };
    return { text, tone };
  }
}
