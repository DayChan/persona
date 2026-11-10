// Installing, upgrading and removing an extension in steps (docs/protocol/protocol.md "Extension install, upgrade and uninstall progress", #3277): the
// hub runs each as an operation and pushes `extension_operation` as it goes; a page that opens again lists the ones
// in progress. Only with a hub that has `extension_operations`: an older one keeps the synchronous calls.
// An operation lives only in the memory of the manager running it (#1832): a manager that restarts has none, sends no
// final push, and the hub only pushes `managers`. So that push lists every manager's operations again, and one a
// manager no longer lists, or answers 404 for, is let go: the page stops showing it in progress.
import { api, ApiError, enc } from "./api";
import { extRoot } from "./extension-api";
import { itemHeaders, messageManager, otherManager } from "./manager-choice";
import { currentRoster, installedManagers } from "./managers";
import { changed } from "./extensions";
import { previewConsent } from "./extension-model";
import { bytes } from "./format";
import { getState, hubHas, onHubEvent } from "./store";
import { t } from "../i18n";
import type { MarketPreview } from "./extension-market";

export const OPERATIONS_FEATURE = "extension_operations";

export type OpStepState = "pending" | "running" | "done" | "failed" | "skipped";

/** One command-line program of the tools step (#4507): reused is the machine's own copy. */
export type OpTool = { name: string; state: "pending" | "running" | "done" | "failed" | (string & {}); reused?: boolean };

export type ExtensionOpStep = {
  id: string;
  state: OpStepState;
  /** The download's bytes; for the tools step, of the one program downloading. */
  bytes?: number;
  total?: number;
  cached?: boolean;
  machines?: { done: number; total: number; errors?: Record<string, string> };
  /** The tools step's programs, in the order they install. */
  tools?: OpTool[];
  started_at?: string;
  ended_at?: string;
};

export type ExtensionOperation = {
  id: string;
  kind: "install" | "upgrade" | "uninstall";
  extension: string;
  name?: string;
  market?: string;
  source?: string;
  version?: string;
  from?: string;
  actor?: string;
  state: "running" | "needs_consent" | "succeeded" | "failed" | "canceled";
  step?: string;
  steps: ExtensionOpStep[];
  error?: string;
  error_code?: string;
  preview_id?: string;
  retryable: boolean;
  /** "auth" when it succeeded and the extension still has to be signed in to: the page opens its sign-in. */
  next?: string;
  started_at: string;
  updated_at: string;
  ended_at?: string;
  /** Only from GET of one that waits for consent. */
  preview?: MarketPreview;
  /** Whose operation it is when another manager's than the default (its push says so, or this page started it with
   *  that manager's name; docs/assistant/managers.md "Clients"); none: the default's. Set here, not by the hub. */
  manager?: string;
};

/** Whether this hub runs installs as operations. */
export const operationsOn = (features: string[] = getState().features) => hubHas(features, OPERATIONS_FEATURE);

// ── the hub ─────────────────────────────────────────────

const OPS = () => `${extRoot()}/-/operations`;
type Accepted = { operation: ExtensionOperation };

/** A manager's list of operations (none: the default's; the hub doesn't merge this list). */
export const listOperations = (manager?: string) => api<{ operations?: ExtensionOperation[] }>(OPS(), { headers: itemHeaders(manager) }).then((r) => r.operations ?? []);

/** Whether e says the manager has no such operation (404 `not_found`: it restarted, or the operation ended long ago),
 *  or no longer is a manager (404 `no_manager`): either way the operation is over. */
export const opGone = (e: unknown) => e instanceof ApiError && e.status === 404 && (e.code === "not_found" || e.code === "no_manager");

/** p, letting go of operation id of manager when the answer says it is gone (opGone); the error still goes on. */
function orGone<T>(p: Promise<T>, id: string, manager?: string): Promise<T> {
  return p.catch((e: unknown) => {
    if (opGone(e)) forget({ id, manager: otherManager(manager) });
    throw e;
  });
}

// `manager` below: whose operation (ExtensionOperation.manager, or the manager a page installs for); none: the default's.
export const getOperation = (id: string, manager?: string) => orGone(api<ExtensionOperation>(`${OPS()}/${enc(id)}`, { headers: itemHeaders(manager) }), id, manager);
export const retryOperation = (id: string, manager?: string) =>
  orGone(api<Accepted>(`${OPS()}/${enc(id)}/retry`, { method: "POST", headers: itemHeaders(manager) }), id, manager).then((r) => track(r.operation, manager));
export const cancelOperation = (id: string, manager?: string) => orGone(api<unknown>(`${OPS()}/${enc(id)}`, { method: "DELETE", headers: itemHeaders(manager) }), id, manager);

/** Starts installing (or upgrading) a market's extension: download and checks in the background. */
export const startFromMarket = (market: string, extension: string, manager?: string) =>
  api<Accepted>(`${extRoot()}/-/catalog/preview`, { method: "POST", json: { market, extension, plugin: extension, async: true }, headers: itemHeaders(manager) }).then((r) => track(r.operation, manager));
/** The user agreed to the preview: the same operation goes on. */
export const agreeTo = (pv: MarketPreview, upgradeOf?: string, manager?: string) =>
  api<Accepted>(pv.rename_from || upgradeOf ? `${extRoot()}/${enc(pv.rename_from || upgradeOf!)}/-/upgrade` : `${extRoot()}/-/install`, {
    method: "POST",
    json: { ...previewConsent(pv), async: true },
    headers: itemHeaders(manager),
  }).then((r) => track(r.operation, manager));

// ── what the page knows ─────────────────────────────────

// By opKey: two managers' operations may have the same id.
let ops = new Map<string, ExtensionOperation>();

/** An operation's place in the list: its id, and its manager when another than the default's. */
const opKey = (o: Pick<ExtensionOperation, "id" | "manager">) => (o.manager ? `${o.manager}\u0000${o.id}` : o.id);
const listeners = new Set<() => void>();
let started = false;
// The lists asked for, numbered: an answer older than one already applied is dropped. Each list on its way notes the
// operations tracked since it went out: the hub read it before those pushes or starts, so its copy of them may be
// older or missing (#4574).
let listing = 0;
let applied = 0;
// Whether a list of the hub's has been applied yet: before that, what is known came from pushes and starts only.
let listed = false;
const listsOut = new Set<Set<string>>();

function emit() {
  listeners.forEach((l) => l());
}

/** raw as this page keeps it: `manager` set to whose it is when another than the default's, else left out. */
function owned(raw: ExtensionOperation, manager?: string): ExtensionOperation {
  const owner = otherManager(manager ?? raw.manager);
  return owner ? (raw.manager === owner ? raw : { ...raw, manager: owner }) : raw.manager ? { ...raw, manager: undefined } : raw;
}

/** Keeps op (newer than what is known), and tells the pages; an operation that just succeeded refreshes the lists.
 *  `manager`: whose it is (a push's sender, or the manager a request named); none: the default's. */
export function track(raw: ExtensionOperation, manager?: string): ExtensionOperation {
  const op = owned(raw, manager);
  const key = opKey(op);
  const was = ops.get(key);
  if (was && Date.parse(was.updated_at) > Date.parse(op.updated_at)) return was;
  ops = new Map(ops).set(key, op);
  listsOut.forEach((since) => since.add(key));
  if (op.state === "succeeded" && was?.state !== "succeeded") changed(null);
  emit();
  return op;
}

/** The managers whose operations are listed: the default (undefined), every other one this page knows an operation of,
 *  and every other installed one the roster has online (one may have started an install from another device). */
function listedManagers(): (string | undefined)[] {
  const others = new Set<string>();
  for (const o of ops.values()) if (o.manager) others.add(o.manager);
  const roster = currentRoster();
  if (roster) for (const m of installedManagers(roster)) if (m.online && otherManager(m.id)) others.add(m.id);
  return [undefined, ...others];
}

/** Lists every manager's operations again (on opening, after a reconnect, when a manager reconnects): what a manager
 * no longer lists is let go (it restarted, or the operation ended long ago). What was pushed or started while the
 * list was on its way stays where it is newer than the list's copy; a list answering after a later one was applied is
 * dropped. A manager whose list fails (offline, the network) keeps what is known of it; one the hub no longer has
 * (404 `no_manager`) loses its operations. */
export async function reloadOperations() {
  if (!operationsOn()) return;
  const mine = ++listing;
  const since = new Set<string>();
  listsOut.add(since);
  try {
    const managers = listedManagers();
    const answers = await Promise.all(
      managers.map((m) =>
        listOperations(m).then(
          (list) => ({ m, list: list.map((o) => owned(o, m)) }),
          (e: unknown) => ({ m, list: opGone(e) && m ? [] : null }),
        ),
      ),
    );
    if (mine < applied || answers.every((a) => a.list === null)) return;
    applied = mine;
    const next = new Map(ops);
    for (const { m, list } of answers) {
      if (!list) continue;
      if (!m) listed = true;
      const owner = otherManager(m);
      const fresh = new Map(list.map((o) => [opKey(o), o]));
      for (const [key, o] of next) if (o.manager === owner && !fresh.has(key) && !since.has(key)) next.delete(key);
      for (const [key, o] of fresh) {
        const known = next.get(key);
        if (!(known && since.has(key) && Date.parse(known.updated_at) > Date.parse(o.updated_at))) next.set(key, o);
      }
    }
    ops = next;
    emit();
  } finally {
    listsOut.delete(since);
  }
}

function start() {
  if (started) return;
  started = true;
  onHubEvent("extension_operation", (msg) => {
    // another manager's push says whose (`manager`); the one the hub runs itself names none
    if (msg.operation && typeof msg.operation === "object") track(msg.operation as ExtensionOperation, messageManager(msg));
  });
  // after hello (its features are in the store by then): events missed while away are not replayed
  onHubEvent("hello", () => void reloadOperations());
  // a manager came back (or went): one that restarted lost its operations and sends nothing about them (#1832)
  onHubEvent("managers", () => void reloadOperations());
  void reloadOperations();
}

/** Subscribes to the operations; the first one starts listening. */
export function watchOperations(l: () => void): () => void {
  start();
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

/** Whether the hub's list of operations has been read since listening started (it also holds ones that ended a while ago). */
export const operationsListed = () => listed;
export const operations = (): ExtensionOperation[] => [...ops.values()].sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at));
export const forget = (op: string | Pick<ExtensionOperation, "id" | "manager">) => {
  ops = new Map(ops);
  ops.delete(typeof op === "string" ? op : opKey(op));
  emit();
};

// ── words and rules ─────────────────────────────────────

/** Whether op is manager's (none: the default's). */
export const opOf = (op: ExtensionOperation, manager: string | undefined) => op.manager === otherManager(manager);

/** The newest operation of extension id (from market, when given) of manager (none: the default's), if it is going on
 *  or ended just now. */
export function latestFor(list: ExtensionOperation[], id: string, market?: string, manager?: string): ExtensionOperation | undefined {
  return list.find((o) => o.extension === id && (!market || !o.market || o.market === market) && opOf(o, manager));
}

export const isActive = (o?: ExtensionOperation) => o?.state === "running" || o?.state === "needs_consent";

/** The steps worth showing: those it skips are left out. */
export const shownSteps = (o: ExtensionOperation) => o.steps.filter((s) => s.state !== "skipped");

const STEP_KEYS: Record<string, string> = {
  download: "download",
  verify: "verify",
  unpack: "unpack",
  check: "check",
  consent: "consent",
  tools: "tools",
  install: "install",
  start: "start",
  nodes: "nodes",
  stop: "stop",
  remove: "remove",
};

/** A step's name; one this page does not know is shown as its id. */
export function stepLabel(id: string, kind?: ExtensionOperation["kind"]): string {
  if (id === "install" && kind === "upgrade") return t("lib-extension-operations.step_upgrade");
  const k = STEP_KEYS[id];
  return k ? t(`lib-extension-operations.step_${k}`) : id;
}

/** What a step says beside its name: the bytes of a download, the computers done, from the cache. */
export function stepDetail(s: ExtensionOpStep): string {
  if (s.id === "download") {
    if (s.cached) return t("lib-extension-operations.cached");
    if (s.total && s.state === "running") return t("lib-extension-operations.bytes_of", { done: bytes(s.bytes ?? 0), total: bytes(s.total) });
    if (s.bytes) return bytes(s.bytes);
    return "";
  }
  if (s.id === "tools" && s.tools?.length) {
    return t("lib-extension-operations.tools_count", { done: s.tools.filter((x) => x.state === "done").length, total: s.tools.length });
  }
  if (s.machines && s.machines.total > 0) return t("lib-extension-operations.machines", { done: s.machines.done, total: s.machines.total });
  return "";
}

/** One program of the tools step as the page draws it: its state, what it says beside its name, its download's fraction. */
export type ToolLine = { name: string; state: OpTool["state"]; detail: string; fraction?: number };

/**
 * The tools step's programs, one line each: a reused one says it is the machine's own; the one downloading has its
 * bytes and (its size known) a fraction.
 */
export function toolLines(s: ExtensionOpStep): ToolLine[] {
  return (s.tools ?? []).map((x) => {
    const downloading = x.state === "running" && s.state === "running";
    let detail = "";
    if (x.reused) detail = t("lib-extension-operations.tool_reused");
    else if (downloading && s.total) detail = t("lib-extension-operations.bytes_of", { done: bytes(s.bytes ?? 0), total: bytes(s.total) });
    else if (downloading && s.bytes) detail = bytes(s.bytes);
    const fraction = downloading && !x.reused && s.total ? Math.min(1, (s.bytes ?? 0) / s.total) : undefined;
    return { name: x.name, state: x.state, detail, fraction };
  });
}

/** Why a failed operation stopped, in words: the hub's error, led by what its code means when the page knows it. */
export function opError(o: ExtensionOperation): string {
  if (!o.error) return "";
  if (o.error_code === "tools_failed") return t("lib-extension-operations.tools_failed", { error: o.error });
  return t("settings-extensionmarket.op_error", { error: o.error });
}

/** An operation that ended well and leaves the extension to be signed in to (its next step is the sign-in). */
export const wantsSignIn = (o?: ExtensionOperation) => o?.state === "succeeded" && o.next === "auth";

/** A download's fraction (0–1), undefined when its size is not known or it is not downloading. */
export function stepFraction(s: ExtensionOpStep): number | undefined {
  if (s.id === "download" && s.state === "running" && s.total) return Math.min(1, (s.bytes ?? 0) / s.total);
  if (s.machines && s.state === "running" && s.machines.total) return s.machines.done / s.machines.total;
  return undefined;
}

/** The operation as one line: what it is doing now, or how it ended. */
export function opLine(o: ExtensionOperation): string {
  switch (o.state) {
    case "needs_consent":
      return t("lib-extension-operations.needs_consent");
    case "succeeded":
      return o.kind === "uninstall" ? t("lib-extension-operations.uninstalled") : o.kind === "upgrade" ? t("lib-extension-operations.upgraded") : t("lib-extension-operations.installed");
    case "failed":
      return t("lib-extension-operations.failed_at", { step: stepLabel(o.step ?? "", o.kind) });
    case "canceled":
      return t("lib-extension-operations.canceled");
  }
  const s = o.steps.find((x) => x.id === o.step);
  return s ? `${stepLabel(s.id, o.kind)}…` : t("lib-extension-operations.starting");
}

/** Done of the steps it takes (skipped ones left out), for a compact progress. */
export function stepsDone(o: ExtensionOperation): { done: number; total: number } {
  const shown = shownSteps(o);
  return { done: shown.filter((s) => s.state === "done").length, total: shown.length };
}
