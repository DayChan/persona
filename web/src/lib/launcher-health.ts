// A launcher's health on one computer (#1643, docs/server/launcher-health.md) and why "New agent" grays it out. Only a
// blocked record does (the decision in #1643: gray out with the reason, never hide): failing and repairing ones are still
// the hub's to retry. Choosing a grayed-out launcher shows how to fix it; "Try anyway" there picks it after all, as a
// start with it that works clears the record. Pure: the dialog and the menus draw it.
import { t } from "../i18n";
import type { Launcher } from "./types";

/** `LauncherHealth.kind` of a launcher a new terminal tab's shell can't run, though the login shell can (#1642). */
export const NOT_IN_TERMINAL = "not_in_terminal";

export interface LauncherBlock {
  /** The failure's class ("" when the record has none). */
  kind: string;
  /** The one line under its name. */
  reason: string;
  /** The help's title. */
  title: string;
  /** The record's raw text (redacted by the hub), for Details; absent when it has none. */
  detail?: string;
  notInTerminal: boolean;
}

/** The one line for a blocked launcher's failure class. */
export function blockReason(kind: string): string {
  switch (kind) {
    case NOT_IN_TERMINAL:
      return t("lib-launcher-health.not_in_terminal");
    case "auth":
      return t("lib-launcher-health.auth");
    case "model_route":
      return t("lib-launcher-health.model_route");
    case "cli_outdated":
      return t("lib-launcher-health.cli_outdated");
    case "timeout":
      return t("lib-launcher-health.timeout");
    default:
      return t("lib-launcher-health.other");
  }
}

/** Why "New agent" grays the launcher out on the computer its list is for; null when it can be picked (never checked,
 *  working, or still retried / repaired by the hub; an older hub sends no health at all). */
export function launcherBlock(l: Pick<Launcher, "health"> | undefined): LauncherBlock | null {
  const h = l?.health;
  if (!h || h.state !== "blocked") return null;
  const kind = h.kind ?? "";
  const detail = h.error?.trim();
  const notInTerminal = kind === NOT_IN_TERMINAL;
  return {
    kind,
    reason: blockReason(kind),
    title: notInTerminal ? t("lib-launcher-health.title_terminal") : t("lib-launcher-health.title_failed"),
    ...(detail ? { detail } : {}),
    notInTerminal,
  };
}

/** The launcher a computer's dialog starts with when there is no default to follow: the one used there last unless it is
 *  blocked now, else the first that is not; only when every one is blocked, the last one (or the first), its reason
 *  shown, rather than nothing to start with. */
export function initialLauncher(saved: string | null | undefined, launchers: Launcher[]): string {
  const last = saved ? launchers.find((l) => l.name === saved) : undefined;
  if (last && !launcherBlock(last)) return last.name;
  const usable = launchers.find((l) => !launcherBlock(l));
  if (usable) return usable.name;
  return last?.name ?? launchers[0]?.name ?? "";
}

/** The launchers in order for a plain menu ("Run agent here"): the ones that can start first, then the grayed-out ones,
 *  each group as the hub listed it. */
export function usableFirst(launchers: Launcher[]): Launcher[] {
  return [...launchers.filter((l) => !launcherBlock(l)), ...launchers.filter((l) => launcherBlock(l))];
}
