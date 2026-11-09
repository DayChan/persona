import { t } from "../i18n";
import { MANAGER_HEADER } from "./manager-choice";
import { api, enc } from "./api";
import type { ProviderAPI } from "./providers";

export interface ModelBinding {
  provider?: string;
  model?: string;
  effort?: string;
  /** Main chat only (#2455): how it runs Claude, "cli" or "agentsdk"; absent is the CLI. */
  runtime?: string;
}

export interface ModelProvider {
  id: string;
  name: string;
  kind: "api" | "launcher" | "plugin";
  agent?: string;
  api?: ProviderAPI;
  model?: string;
  machines?: string[];
  hidden?: boolean;
  has_key?: boolean;
}

export interface ModelPurpose {
  id: string;
  name: string;
  group: string;
  kind: "api" | "launcher" | "plugin";
  parent?: string;
  /** Owning category; a child with nonempty fields can still have its own editable record. */
  category_id?: string;
  machine_id?: string;
  override: ModelBinding;
  effective: ModelBinding;
  revision: number;
  inherited_from?: string;
  status?: string;
  fields?: string[];
  provider_ids?: string[];
  /** Main chat only: the runtimes it can run on (an older hub: none, no "Runtime" row). */
  runtimes?: string[];
}

export interface ModelConfigurationBody {
  purposes: ModelPurpose[];
  providers: ModelProvider[];
}
export interface ModelPurposeBody {
  purpose: ModelPurpose;
  providers: ModelProvider[];
}
export interface ModelDraft {
  provider: string;
  model: string;
  effort: string;
  /** "" when the purpose has no runtime choice. */
  runtime: string;
}

/** The runtimes this client can name, in the order "Runtime" offers them. */
export const RUNTIMES: { id: string; label: string }[] = [
  { id: "cli", label: "Claude Code CLI" },
  { id: "agentsdk", label: "Agent SDK" },
];

/** The agent CLI/runtime a provider runs ("claude" by default). */
export function providerAgent(provider?: ModelProvider, id?: string): string {
  if (provider?.agent) return provider.agent;
  const pid = provider?.id ?? id ?? "";
  if (pid === "launcher.codex") return "codex";
  if (pid === "launcher.agy") return "agy";
  if (pid === "launcher.omp") return "omp";
  if (pid === "launcher.traex") return "traex";
  return "claude";
}

/** A runtime's name; none is the CLI, one this client does not know shows its id. */
export function runtimeLabel(id: string | undefined, agent?: string): string {
  if (agent === "codex") return "Codex CLI";
  if (agent === "agy") return "Antigravity ACP";
  if (agent === "omp") return "omp CLI";
  if (agent === "traex") return "TRAEX CLI";
  return RUNTIMES.find((r) => r.id === (id || "cli"))?.label ?? id!;
}

/** "Runtime"'s options: only the ones the hub offers for this purpose (and this client knows). */
export function runtimeChoices(p: ModelPurpose, agent?: string): { id: string; label: string }[] {
  if (!p.runtimes?.length) return [];
  if (agent && agent !== "claude") return [{ id: "cli", label: runtimeLabel("cli", agent) }];
  return RUNTIMES.filter((r) => p.runtimes?.includes(r.id));
}

export const modelConfiguration = (manager?: string) => api<ModelConfigurationBody>("/model-config", { headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
export const modelPurpose = (id: string, manager?: string) => api<ModelPurposeBody>(`/model-config/purposes/${enc(id)}`, { headers: manager ? { [MANAGER_HEADER]: manager } : undefined });

export function draftFor(p: ModelPurpose): ModelDraft {
  const runtime = runtimeChoices(p).length ? p.override?.runtime || p.effective?.runtime || "cli" : "";
  return { provider: p.override?.provider ?? "", model: p.override?.model ?? "", effort: p.override?.effort ?? "", runtime };
}

export function chooseProvider(draft: ModelDraft, provider: string): ModelDraft {
  return draft.provider === provider ? draft : { provider, model: "", effort: "", runtime: draft.runtime };
}

export function supportsField(p: ModelPurpose, field: Exclude<keyof ModelDraft, "runtime">): boolean {
  return (p.fields ?? ["provider", "model"]).includes(field);
}

export function modelPatch(p: ModelPurpose, draft: ModelDraft): Record<string, string | number | null> {
  const patch: Record<string, string | number | null> = { revision: p.revision };
  for (const field of ["provider", "model", "effort"] as const) {
    if (supportsField(p, field)) patch[field] = draft[field].trim() || null;
  }
  if (runtimeChoices(p).length && draft.runtime) patch.runtime = draft.runtime;
  return patch;
}

export function saveModelPurpose(p: ModelPurpose, draft: ModelDraft, manager?: string): Promise<ModelPurposeBody> {
  return api(`/model-config/purposes/${enc(p.id)}`, { method: "PATCH", json: modelPatch(p, draft), headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
}

export function providersFor(p: ModelPurpose, providers: ModelProvider[]): ModelProvider[] {
  const candidates = providers.filter((provider) =>
    p.provider_ids ? p.provider_ids.includes(provider.id) : provider.kind === p.kind,
  );
  return candidates.filter((provider) => !provider.hidden || provider.id === p.override?.provider || provider.id === p.effective?.provider);
}

/** A provider's name for the page: its own, or nothing for one of the hub's old built-in ones (`legacy.*`) that is not
 *  set up, whose id means nothing to a person (docs/assistant/onboarding.md 4.5). */
export function providerName(id: string | undefined, providers: ModelProvider[] = []): string | undefined {
  if (!id) return undefined;
  const name = providers.find((v) => v.id === id)?.name;
  if (name) return name;
  return id.startsWith("legacy.") ? undefined : id;
}

export function modelSummary(p: ModelPurpose, providers: ModelProvider[] = []): string {
  const name = providerName(p.effective?.provider, providers);
  const effProvider = providers.find((v) => v.id === p.effective?.provider);
  const effAgent = providerAgent(effProvider, p.effective?.provider);
  const rt = p.effective?.runtime;
  const runtime = effAgent === "claude" && rt && rt !== "cli" ? runtimeLabel(rt) : ""; // the CLI is the default: not worth a word
  return [name, p.effective?.model, p.effective?.effort && t("lib-model-config.effort", { effort: p.effective.effort }), runtime].filter(Boolean).join(" · ") || t("lib-model-config.not_configured");
}

/** The main chat's note when it asked for the Agent SDK and runs on the CLI (the session's `runtime_fallback`). */
export function runtimeFallbackText(reason: string): string {
  return t("lib-model-config.runtime_fallback", { reason: reason.trim() });
}

export function purposeGroups(purposes: ModelPurpose[], query: string): [string, ModelPurpose[]][] {
  const q = query.trim().toLocaleLowerCase();
  const groups = new Map<string, ModelPurpose[]>();
  for (const p of purposes) {
    if (q && ![p.name, p.group, p.id, modelSummary(p)].some((s) => s.toLocaleLowerCase().includes(q))) continue;
    const group = p.group || t("lib-model-config.group_misc");
    groups.set(group, [...(groups.get(group) ?? []), p]);
  }
  return [...groups.entries()];
}
