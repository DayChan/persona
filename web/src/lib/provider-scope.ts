// A Persona-managed provider's device scope (docs/integrations/launchers.md "Device scope and on-demand delivery"):
// which computers it runs on, when its key travels, whether it stands back where the harness has a service of its own,
// and keys for single computers. The editor's form, its check, the summary the detail dialog shows, the fields of the
// POST / PUT body, and the list's grouping by harness. Pure: views/settings/ProviderScope.tsx only renders it.
import { formatList, t } from "../i18n";
import type { Machine } from "./types";

export type Share = "all" | "selected" | "none";
export type Delivery = "on_demand" | "push";
export type OwnService = "skip" | "use";

/** The hub's hello feature for the scope fields; without it a new provider gets no "Devices" step. */
export const LAUNCHER_SCOPE_FEATURE = "launcher_scope";

/** The scope fields of GET /api/v1/launchers' managed entry; all absent on an older hub. */
export interface ScopeView {
  share?: Share;
  scope_machines?: string[];
  delivery?: Delivery;
  own_service?: OwnService;
  /** Computers with a key of their own (the keys are never returned). */
  key_machines?: string[];
  /** Saved before scopes existed: all + push + use, work computers included, until a scope field is saved. */
  legacy_scope?: boolean;
}

export interface ScopeForm {
  share: Share;
  /** With `selected`: the computers it runs on. */
  machines: string[];
  /** With `all`: the work computers added to it. */
  work: string[];
  delivery: Delivery;
  /** null: not chosen, so it follows the hub's default for a new provider (use with assign, else skip). */
  own_service: OwnService | null;
  /** Keys typed for single computers; never prefilled. */
  keys: Record<string, string>;
  /** Computers whose stored key is to be removed. */
  remove: string[];
}

type MachineLike = Pick<Machine, "id" | "trust">;

/** What the hub gives a new provider (launchers.md "Defaults"). */
export function newScope(): ScopeForm {
  return { share: "all", machines: [], work: [], delivery: "on_demand", own_service: null, keys: {}, remove: [] };
}

/** The scope an existing provider opens with; null when its hub does not know scopes. */
export function scopeFromView(v: ScopeView | null): ScopeForm | null {
  if (!v?.share) return null;
  const ids = [...(v.scope_machines ?? [])];
  return {
    ...newScope(),
    share: v.share,
    machines: v.share === "selected" ? ids : [],
    work: v.share === "all" ? ids : [],
    delivery: v.delivery ?? "on_demand",
    own_service: v.own_service ?? "skip",
  };
}

/** The editor shows the scope: an existing provider when its view has one, a new one when the hub says it takes it. */
export const scopeShown = (old: ScopeView | null, features: string[]) => (old ? !!old.share : features.includes(LAUNCHER_SCOPE_FEATURE));

export const ownServiceOf = (f: ScopeForm, assign = false): OwnService => f.own_service ?? (assign ? "use" : "skip");

/** The ids `scope_machines` carries for the share chosen. */
export const scopeMachines = (f: ScopeForm): string[] => (f.share === "selected" ? f.machines : f.share === "all" ? f.work : []);

/** A work computer, when the hub's machine list says so (`trust: "work"`); hubs that only mark pending ones make none. */
export const isWork = (m: Pick<Machine, "trust">) => (m.trust as string | undefined) === "work";
const isPending = (m: Pick<Machine, "trust">) => m.trust === "pending";

/** The computers "Chosen computers" offers: confirmed ones, plus whatever is already chosen (kept visible to uncheck). */
export function choosable(machines: MachineLike[], chosen: string[]): string[] {
  const out = machines.filter((m) => !isPending(m) || chosen.includes(m.id)).map((m) => m.id);
  for (const id of chosen) if (!out.includes(id)) out.push(id);
  return out;
}

/** The work computers "All my computers" can add: the known ones, plus those already added. */
export function addableWork(machines: MachineLike[], added: string[]): string[] {
  const out = machines.filter((m) => isWork(m) && !isPending(m)).map((m) => m.id);
  for (const id of added) if (!out.includes(id)) out.push(id);
  return out;
}

/** The computers that may get the provider, whose own key can be set: none for `none`. */
export function keyMachines(f: ScopeForm, machines: MachineLike[]): string[] {
  if (f.share === "none") return [];
  if (f.share === "selected") return [...f.machines];
  const out = machines.filter((m) => !isPending(m) && !isWork(m)).map((m) => m.id);
  for (const id of f.work) if (!out.includes(id)) out.push(id);
  return out;
}

/** What keeps the scope from being saved ("" when it can): the hub's own check, said earlier. */
export function scopeProblem(f: ScopeForm): string {
  return f.share === "selected" && f.machines.length === 0 ? t("lib-provider-scope.pick_machine") : "";
}

const sameSet = (a: string[], b: string[]) => a.length === b.length && [...a].sort().join("\n") === [...b].sort().join("\n");

/** The scope's fields of POST / PUT /api/v1/launchers. A PUT (`old` has a share) sends a field only when it changed
 *  (left out keeps what is stored, so a legacy provider stays as it was until one is changed); a POST only what
 *  differs from the hub's defaults (all, on demand, skip; use with `assign`). `scope_machines` goes with the share
 *  whenever either changed. `machine_keys` carries typed keys for computers that may get the provider (or already have
 *  a key of their own), "" for each removed one. */
export function scopeBody(f: ScopeForm, old: ScopeView | null, machines: MachineLike[], assign = false): Record<string, unknown> {
  const body: Record<string, unknown> = {};
  const ids = scopeMachines(f);
  const own = ownServiceOf(f, assign);
  if (old?.share) {
    const shareChanged = f.share !== old.share;
    if (shareChanged) body.share = f.share;
    if (shareChanged || !sameSet(ids, old.scope_machines ?? [])) body.scope_machines = ids;
    if (f.delivery !== (old.delivery ?? "on_demand")) body.delivery = f.delivery;
    if (own !== (old.own_service ?? "skip")) body.own_service = own;
  } else {
    if (f.share !== "all") body.share = f.share;
    if (ids.length) body.scope_machines = ids;
    if (f.delivery !== "on_demand") body.delivery = f.delivery;
    if (f.own_service !== null && own !== (assign ? "use" : "skip")) body.own_service = own;
  }
  const keys: Record<string, string> = {};
  const allowed = [...keyMachines(f, machines), ...(old?.key_machines ?? [])];
  for (const [id, key] of Object.entries(f.keys)) if (key.trim() && allowed.includes(id)) keys[id] = key.trim();
  for (const id of f.remove) if (!(id in keys)) keys[id] = "";
  if (Object.keys(keys).length) body.machine_keys = keys;
  return body;
}

/** One line for the detail dialog and the editor's folded badge: where, how the key travels, and whether it also
 *  runs where the harness has a service of its own. */
export function scopeSummary(v: ScopeView, name: (id: string) => string = (id) => id): string {
  if (v.share === "none") return t("lib-provider-scope.summary_none");
  const ids = v.scope_machines ?? [];
  const where = v.legacy_scope
    ? t("lib-provider-scope.summary_legacy")
    : v.share === "selected"
      ? ids.length
        ? formatList(ids.map(name), { type: "conjunction", style: "narrow" })
        : t("lib-provider-scope.summary_selected_none")
      : ids.length
        ? t("lib-provider-scope.summary_all_work", { count: ids.length })
        : t("lib-provider-scope.summary_all");
  const parts = [where, v.delivery === "push" ? t("lib-provider-scope.summary_push") : t("lib-provider-scope.summary_on_demand")];
  if (v.own_service === "use") parts.push(t("lib-provider-scope.summary_use"));
  return parts.join(" · ");
}

/** The form as the view it would save, for the summary. */
export const formScope = (f: ScopeForm, assign = false): ScopeView => ({ share: f.share, scope_machines: scopeMachines(f), delivery: f.delivery, own_service: ownServiceOf(f, assign) });

// ── The list, grouped by harness ─────────────────────────────

export type Harness = "claude" | "codex" | "agy" | "omp" | "api" | (string & {});

/** The harness a managed provider runs in: a direct API one its own group, a launcher its agent ("" is Claude Code). */
export const harnessOf = (p: { kind?: string; agent?: string }): Harness => (p.kind === "api" ? "api" : p.agent || "claude");

const HARNESS_ORDER = ["claude", "codex", "agy", "omp", "api"];

/** Providers grouped by harness: the known ones in a fixed order, any other after them; the list's order kept within each. */
export function harnessGroups<T extends { kind?: string; agent?: string }>(list: T[]): { harness: Harness; items: T[] }[] {
  const out: { harness: Harness; items: T[] }[] = [];
  for (const p of list) {
    const h = harnessOf(p);
    const g = out.find((x) => x.harness === h);
    if (g) g.items.push(p);
    else out.push({ harness: h, items: [p] });
  }
  const rank = (h: string) => (HARNESS_ORDER.includes(h) ? HARNESS_ORDER.indexOf(h) : HARNESS_ORDER.length);
  return out.sort((a, b) => rank(a.harness) - rank(b.harness));
}

/** A harness's header: the products' own names, "Direct API" translated. */
export function harnessLabel(h: Harness): string {
  switch (h) {
    case "claude":
      return "Claude Code";
    case "codex":
      return "Codex";
    case "agy":
      return "Antigravity";
    case "omp":
      return "Oh My Pi";
    case "api":
      return t("lib-provider-scope.harness_api");
    default:
      return h;
  }
}
