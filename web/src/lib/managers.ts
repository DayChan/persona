// Settings › Managers (docs/assistant/managers.md, docs/protocol/protocol.md "Managers"): the hub's roster of managers (each a whole assistant),
// making one (a one-time install code, and the install commands when the hub gives them), renaming, the default,
// installing one elsewhere and removing one. The hub pushes {"type":"managers"} when the roster changes to a page
// that declared the feature `managers` (CLIENT_FEATURES).
import { useSyncExternalStore } from "react";
import { t } from "../i18n";
import { api, ApiError, enc, errorText } from "./api";
import { ago } from "./format";
import { BUILTIN_MANAGER, chooseManager, chosenManager, setRosterDefault, settleRosterDefault } from "./manager-choice";
import { onHubEvent } from "./store";

export interface Manager {
  id: string;
  name: string;
  online: boolean;
  default?: boolean;
  /** The assistant the hub runs itself. */
  builtin?: boolean;
  host?: string;
  os?: string;
  arch?: string;
  /** How it runs, as it reported: "child" (the hub's own), "service", "docker", "external"… */
  deploy?: string;
  version?: string;
  /** Connected since (online only). */
  since?: string;
  /** Last connected (offline: "Last synced at..."). */
  last_seen?: string;
  /** Not installed yet: when its install code runs out. */
  installing?: string;
  /** Online on a build that reads no settings of its own (no `config.own`): it runs on built-in defaults until updated. */
  needs_update?: boolean;
}

export interface ManagerList {
  managers: Manager[];
  default: string;
  /** Matching bot identities across managers, without exposing account identifiers or secrets. */
  bot_conflicts?: BotConflict[];
}

export interface BotConflict {
  platform: "feishu" | "wechat";
  managers: string[];
}

export interface InstallCode {
  code: string;
  expires_at: string;
  /** The install commands, from a hub that gives them; otherwise only the code. */
  commands?: { service?: string; docker?: string };
}

export const listManagers = () => api<ManagerList>("/managers");
export const createManager = (name: string) => api<InstallCode & { manager: Manager }>("/managers", { method: "POST", json: { name } });
export const newInstallCode = (id: string) => api<InstallCode>(`/managers/${enc(id)}/code`, { method: "POST" });
export const renameManager = (id: string, name: string) => api<{ manager: Manager }>(`/managers/${enc(id)}`, { method: "PATCH", json: { name } });
export const makeDefault = (id: string) => api<{ manager: Manager }>(`/managers/${enc(id)}`, { method: "PATCH", json: { default: true } });
export const deleteManager = (id: string) => api<{ ok: boolean }>(`/managers/${enc(id)}`, { method: "DELETE" });

/**
 * A new install code for manager id. A never-installed one (`fresh`) whose code ran out is gone from the hub's roster
 * (`404 no_manager`, #5564): it is made again with the same name, and the answer says the new id.
 */
export async function renewCode(id: string, name: string, fresh: boolean): Promise<{ id: string; code: InstallCode }> {
  try {
    return { id, code: await newInstallCode(id) };
  } catch (e) {
    if (!fresh || !(e instanceof ApiError) || e.status !== 404 || e.code !== "no_manager") throw e;
    const made = await createManager(name);
    return { id: made.manager.id, code: made };
  }
}

/** Names are 1–40 characters (hub/managers.go managerName). */
export const NAME_MAX = 40;

export function validName(name: string): boolean {
  const n = [...name.trim()].length;
  return n > 0 && n <= NAME_MAX;
}

/** Whether a hub without the roster (from before it) answered. */
export function unsupported(e: unknown): boolean {
  return e instanceof ApiError && e.status === 404 && !e.code;
}

const OS: Record<string, string> = { darwin: "macOS", linux: "Linux", windows: "Windows" };

/** How it runs, in plain words. */
export function deployText(m: Pick<Manager, "builtin" | "deploy">): string {
  if (m.builtin || m.deploy === "child") return t("settings-managers.deploy_hub");
  switch (m.deploy) {
    case undefined:
    case "":
      return "";
    case "service":
    case "systemd":
    case "launchd":
      return t("settings-managers.deploy_service");
    case "docker":
    case "container":
      return t("settings-managers.deploy_container");
    case "external":
      return t("settings-managers.deploy_external");
    default:
      return m.deploy;
  }
}

/** Where it runs: host · OS · how. Empty for one not installed yet. */
export function whereText(m: Manager): string {
  const os = m.os ? (OS[m.os] ?? m.os) : "";
  return [m.host, os, deployText(m)].filter(Boolean).join(" · ");
}

/** Minutes the install code has left (to the nearest, at least 1 while it lasts: a fresh code says 10), 0 once it ran out. */
export function minutesLeft(until: string, now = Date.now()): number {
  const ms = Date.parse(until) - now;
  return Number.isFinite(ms) && ms > 0 ? Math.max(1, Math.round(ms / 60_000)) : 0;
}

export type Tone = "green" | "grey" | "accent";

/** The status line under the name and its dot's color. */
export function statusLine(m: Manager, now = Date.now()): { text: string; tone: Tone } {
  if (m.installing) {
    const n = minutesLeft(m.installing, now);
    return { text: n > 0 ? t("settings-managers.installing", { count: n }) : t("settings-managers.installing_expired"), tone: "accent" };
  }
  if (m.online) return { text: t("settings-managers.online"), tone: "green" };
  const when = ago(m.last_seen, now);
  return { text: when ? t("settings-managers.offline_since", { time: when }) : t("settings-managers.offline"), tone: "grey" };
}

/** Why Delete is off for it, or "" when it can be removed. */
export function deleteBlocked(m: Manager): string {
  if (m.builtin) return t("settings-managers.delete_builtin");
  if (m.default) return t("settings-managers.delete_default");
  return "";
}

/** A failed change, in plain words. */
export function managerError(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "default_manager":
        return t("settings-managers.err_default");
      case "builtin_manager":
        return t("settings-managers.err_builtin");
      case "no_manager":
        return t("settings-managers.err_gone");
      case "installing":
        return t("settings-managers.err_installing");
    }
    if (e.status === 400) return t("settings-managers.err_name");
    if (e.status === 403) return t("settings-managers.err_forbidden");
  }
  return errorText(e);
}

// ── the roster the other pages go by (step 4: the switcher, grouping, push switches) ──

let roster: ManagerList | null = null;
let rosterLoad: Promise<void> | null = null;
let rosterWatched = false;
const rosterListeners = new Set<() => void>();

/** How often, and how long apart, a roster read that failed is tried again (besides the next hello). */
export const ROSTER_RETRIES = 3;
export const ROSTER_RETRY_MS = 5_000;
let rosterRetries = 0;
let rosterRetry: ReturnType<typeof setTimeout> | null = null;

/** Reads the roster again (a `managers` push, the socket's hello); a hub without one stays at none. */
export function loadRoster(): Promise<void> {
  rosterLoad ??= listManagers()
    .then((r) => {
      rosterRetries = 0;
      setRoster({ managers: r.managers ?? [], default: r.default || BUILTIN_MANAGER });
    })
    .catch(rosterFailed)
    .finally(() => void (rosterLoad = null));
  return rosterLoad;
}

/** A roster read that failed: the default is taken as the hub's own assistant for now (settleRosterDefault), so pages
 *  waiting for it don't wait for good. A hub without a roster (404) has only that one. Any other failure (a 503, the
 *  network) is read again a few seconds later, a few times, so a blip on a hub whose default is another manager is
 *  put right without waiting for the next hello (what was routed or cached under the hub's own is evaluated again
 *  when the default moves: onDefaultManager). */
function rosterFailed(e: unknown) {
  settleRosterDefault();
  const status = (e as { status?: unknown } | null)?.status;
  if (status === 404 || rosterRetries >= ROSTER_RETRIES) {
    rosterRetries = 0;
    return;
  }
  rosterRetries++;
  rosterRetry ??= setTimeout(() => {
    rosterRetry = null;
    void loadRoster();
  }, ROSTER_RETRY_MS);
}

/** The roster as last read, or null until (and unless) the hub has one; for code outside React (useRoster in a view). */
export function currentRoster(): ManagerList | null {
  return roster;
}

/** Forgets the roster and any read waiting to be tried again (tests). */
export function resetRoster() {
  if (rosterRetry) clearTimeout(rosterRetry);
  rosterRetry = null;
  rosterRetries = 0;
  rosterLoad = null;
  roster = null;
}

/** Puts a roster in place (tests set one directly); the shown manager follows a default that moved or one that went. */
export function setRoster(next: ManagerList | null) {
  roster = next;
  if (next) {
    setRosterDefault(next.default);
    const chosen = chosenManager();
    if (chosen && !installedManagers(next).some((m) => m.id === chosen)) chooseManager(null);
  }
  rosterListeners.forEach((l) => l());
}

/** Reads the roster now and again on every hello and `managers` push. The app calls it once signed in, so the default
 *  is known before the pages that go by it ask anything (an inbox item opened from a link, the sidebar's badges). */
export function watchRoster() {
  if (rosterWatched) return;
  rosterWatched = true;
  onHubEvent("managers", () => void loadRoster());
  onHubEvent("hello", () => void loadRoster());
  void loadRoster();
}

function subscribeRoster(l: () => void) {
  watchRoster();
  rosterListeners.add(l);
  return () => void rosterListeners.delete(l);
}

/** The roster, or null until (and unless) the hub has one. */
export function useRoster(): ManagerList | null {
  return useSyncExternalStore(subscribeRoster, () => roster, () => roster);
}

/** The managers that run (or ran): the ones still being installed have nothing to show yet. */
export function installedManagers(list: ManagerList): Manager[] {
  return list.managers.filter((m) => !m.installing);
}

/** Whether there is more than one manager: only then do the pages switch and group by manager. */
export function multiManager(list: ManagerList | null): list is ManagerList {
  return !!list && installedManagers(list).length > 1;
}

/** The installed managers in the order the pages show them: the default first, then the roster's order. */
export function managerOrder(list: ManagerList): Manager[] {
  const ms = installedManagers(list);
  const d = ms.find((m) => m.id === list.default);
  return d ? [d, ...ms.filter((m) => m !== d)] : ms;
}

export interface ManagerGroup<T> {
  manager: Manager;
  items: T[];
}

/**
 * Items grouped by the manager they carry (`manager`; none is the default's), in managerOrder, empty groups left out;
 * null with one manager (the page lists them as before). An item of a manager the roster doesn't know goes to the
 * default's group, so nothing is hidden.
 */
export function groupByManager<T>(items: T[], list: ManagerList | null, of: (x: T) => string | undefined): ManagerGroup<T>[] | null {
  if (!multiManager(list)) return null;
  const order = managerOrder(list);
  const known = new Set(order.map((m) => m.id));
  const byId = new Map<string, T[]>();
  for (const x of items) {
    const id = of(x);
    const key = id && known.has(id) ? id : list.default;
    byId.set(key, [...(byId.get(key) ?? []), x]);
  }
  return order.flatMap((m) => (byId.get(m.id)?.length ? [{ manager: m, items: byId.get(m.id)! }] : []));
}

/** The line beside an offline manager's name: "Last synced 3 hours ago"; "" for one online (or never seen). */
export function lastSyncedText(m: Manager, now = Date.now()): string {
  if (m.online || m.installing) return "";
  const when = ago(m.last_seen, now);
  return when ? t("settings-managers.last_synced", { time: when }) : t("settings-managers.offline");
}

/**
 * The manager an offline computer is offline with (several managers only): its own, when that one isn't connected. The
 * hub then shows it offline with its words for an unreachable assistant; the pages name the manager instead, and offer
 * no Reconnect, which goes through that manager.
 */
export function offlineManagerOf(list: ManagerList | null, m: { online: boolean; manager?: string }): Manager | null {
  if (m.online || !multiManager(list)) return null;
  const owner = list.managers.find((x) => x.id === ownerIn(list, m.manager));
  return owner && !owner.online && !owner.installing ? owner : null;
}

/** Which of the shown groups a thing with manager `id` goes in: its own, else (unknown, or none) the default's. */
export function ownerIn(list: ManagerList, id: string | undefined): string {
  return id && installedManagers(list).some((m) => m.id === id) ? id : list.default;
}
