// Settings › Model providers › a provider › "Devices" (docs/integrations/launchers.md "Device scope and on-demand
// delivery"): where it runs (all my computers, chosen ones, none), whether each computer keeps a copy of the key,
// whether it also runs where Claude Code / Codex has a service of its own, and keys for single computers. The logic
// is lib/provider-scope.ts; ProviderWizard holds the form and sends it.
import { useState } from "react";
import { addableWork, choosable, keyMachines, ownServiceOf, scopeProblem, type ScopeForm, type ScopeView, type Share } from "../../lib/provider-scope";
import { useStore } from "../../lib/store";
import { Switch } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Segmented } from "../assistant/parts";
import { CheckMark, Disclosure, Labelled, SecretField } from "./ProviderFields";
import { t } from "../../i18n";

export function ProviderScope({ scope, setScope, old, kind, agent, assign = false }: {
  scope: ScopeForm;
  setScope: React.Dispatch<React.SetStateAction<ScopeForm>>;
  /** The stored provider's scope (its key_machines, legacy_scope); null for a new one. */
  old: ScopeView | null;
  kind: "launcher" | "api";
  agent?: string;
  /** A new provider saved with assign: the hub's own-service default is then "use". */
  assign?: boolean;
}) {
  const machines = useStore((s) => s.machines);
  const [keysOpen, setKeysOpen] = useState(false);
  const name = (id: string) => machines.find((m) => m.id === id)?.name || id;
  const problem = scopeProblem(scope);
  const toggle = (k: "machines" | "work", id: string) =>
    setScope((x) => ({ ...x, [k]: x[k].includes(id) ? x[k].filter((y) => y !== id) : [...x[k], id] }));
  // the harnesses whose own sign-in the node can see (launchers.md "A usable service of its own")
  const ownDetected = kind !== "api" && (!agent || agent === "claude" || agent === "codex");
  const work = scope.share === "all" ? addableWork(machines, scope.work) : [];
  const stored = old?.key_machines ?? [];
  const keyed = [...keyMachines(scope, machines), ...stored].filter((id, i, all) => all.indexOf(id) === i);
  const ownKeys = Object.values(scope.keys).filter((k) => k.trim()).length;

  const list = (k: "machines" | "work", ids: string[], label: string) => (
    <ul aria-label={label} className="overflow-hidden rounded-card bg-surface shadow-btn">
      {ids.map((id) => (
        <li key={id} className="border-b border-line last:border-b-0">
          <button type="button" role="checkbox" aria-checked={scope[k].includes(id)} onClick={() => toggle(k, id)}
            className="touch-hit flex w-full items-center gap-2.5 px-3 py-2.5 text-left transition-colors hover:bg-hover">
            <CheckMark on={scope[k].includes(id)} />
            <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{name(id)}</span>
          </button>
        </li>
      ))}
    </ul>
  );

  const shareHint = scope.share === "none" ? t("settings-providerscope.share_none_hint") : scope.share === "selected" ? t("settings-providerscope.share_selected_hint") : t("settings-providerscope.share_all_hint");
  const chosen = choosable(machines, scope.machines);

  return (
    <div className="flex flex-col gap-3">
      {old?.legacy_scope && (
        <p className="flex items-start gap-2 rounded-card bg-inset p-3 text-[12px] leading-relaxed text-ink-2 shadow-hairline">
          <Icon name="info" size={13} stroke={2} className="mt-0.5 shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1">{t("settings-providerscope.legacy_note")}</span>
        </p>
      )}
      <Labelled label={t("settings-providerscope.share")} hint={shareHint}>
        <Segmented<Share>
          wrap
          label={t("settings-providerscope.share")}
          value={scope.share}
          onChange={(share) => setScope((x) => ({ ...x, share }))}
          options={[
            { v: "all", label: t("settings-providerscope.share_all") },
            { v: "selected", label: t("settings-providerscope.share_selected") },
            { v: "none", label: t("settings-providerscope.share_none") },
          ]}
        />
      </Labelled>
      {kind === "api" && scope.share !== "none" && <p className="text-[11.5px] leading-relaxed text-ink-3">{t("settings-providerscope.api_hint")}</p>}

      {scope.share === "selected" && (
        <div className="flex flex-col gap-1">
          {chosen.length ? list("machines", chosen, t("settings-providerscope.machines")) : <p className="rounded-card bg-inset px-3 py-4 text-center text-[12px] text-ink-3 shadow-hairline">{t("settings-providerscope.no_machines")}</p>}
          {problem && <p role="alert" className="text-[11px] text-red">{problem}</p>}
        </div>
      )}

      {work.length > 0 && (
        <Labelled label={t("settings-providerscope.work")} hint={t("settings-providerscope.work_hint")}>
          {list("work", work, t("settings-providerscope.work"))}
        </Labelled>
      )}

      {scope.share !== "none" && (
        <>
          <ToggleRow label={t("settings-providerscope.push")} hint={t("settings-providerscope.push_hint")} on={scope.delivery === "push"} onChange={(on) => setScope((x) => ({ ...x, delivery: on ? "push" : "on_demand" }))} />
          {ownDetected && (
            <ToggleRow label={t("settings-providerscope.use_own")} hint={t("settings-providerscope.use_own_hint")} on={ownServiceOf(scope, assign) === "use"} onChange={(on) => setScope((x) => ({ ...x, own_service: on ? "use" : "skip" }))} />
          )}
        </>
      )}

      {keyed.length > 0 && (
        <Disclosure title={t("settings-providerscope.machine_keys")} open={keysOpen} onToggle={() => setKeysOpen((o) => !o)}
          badge={keysOpen ? undefined : stored.length || ownKeys ? t("settings-providerscope.machine_keys_count", { count: stored.length + ownKeys }) : t("settings-providerscope.machine_keys_badge")}>
          <p className="text-[11.5px] leading-relaxed text-ink-3">{t("settings-providerscope.machine_keys_hint")}</p>
          {keyed.map((id) => {
            const has = stored.includes(id);
            const removed = scope.remove.includes(id);
            const typed = !!scope.keys[id]?.trim();
            return (
              <SecretField
                key={id}
                label={t("settings-providerscope.key_for", { machine: name(id) })}
                value={scope.keys[id] ?? ""}
                onChange={(v) => setScope((x) => ({ ...x, keys: { ...x.keys, [id]: v } }))}
                placeholder={has && !removed ? t("settings-providerscope.replace_key") : t("settings-providerscope.uses_provider_key")}
                hint={
                  has && !removed && !typed ? (
                    <span className="flex flex-wrap items-center gap-x-2">
                      <span>{t("settings-providerscope.has_own_key")}</span>
                      <button type="button" onClick={() => setScope((x) => ({ ...x, remove: [...x.remove, id] }))} className="touch-hit whitespace-nowrap text-red hover:underline">
                        {t("settings-providerscope.remove_key")}
                      </button>
                    </span>
                  ) : has && removed && !typed ? (
                    <span className="flex flex-wrap items-center gap-x-2">
                      <span>{t("settings-providerscope.key_removed")}</span>
                      <button type="button" onClick={() => setScope((x) => ({ ...x, remove: x.remove.filter((y) => y !== id) }))} className="touch-hit whitespace-nowrap text-accent hover:underline">
                        {t("settings-providerscope.undo")}
                      </button>
                    </span>
                  ) : undefined
                }
              />
            );
          })}
        </Disclosure>
      )}
    </div>
  );
}

function ToggleRow({ label, hint, on, onChange }: { label: string; hint: string; on: boolean; onChange: (on: boolean) => void }) {
  return (
    <div className="flex items-center gap-3 rounded-card bg-surface p-3 shadow-btn">
      <span className="min-w-0 flex-1">
        <span className="block text-[13px] text-ink">{label}</span>
        <span className="mt-0.5 block text-[11.5px] leading-relaxed text-ink-3">{hint}</span>
      </span>
      <Switch on={on} onChange={onChange} label={label} />
    </div>
  );
}
