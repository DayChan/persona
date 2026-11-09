import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, enc, errorText } from "../../lib/api";
import { extRoot } from "../../lib/extension-api";
import {
  chooseProvider, draftFor, providerAgent, providerName, modelConfiguration, modelPurpose, modelSummary, providersFor, purposeGroups, runtimeChoices, runtimeLabel, saveModelPurpose, supportsField,
  type ModelBinding, type ModelConfigurationBody, type ModelDraft, type ModelPurposeBody,
} from "../../lib/model-config";
import { effortChoices, providerModels, takesEffort } from "../../lib/launcher-models";
import { useStore } from "../../lib/store";
import { t } from "../../i18n";
import { Button, Dialog, IconButton, Loading, toast } from "../../ui/atoms";
import { Segmented } from "../assistant/parts";
import { Icon } from "../../ui/icons";
import { ModelPicker, type ModelList } from "../../ui/ModelPicker";
import { Group, Pill, Row } from "./SettingsParts";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { ConfigurationManager } from "./ConfigurationManager";
import { ModelProfiles } from "./ModelProfiles";

function larkAgentStatus(binding: ModelBinding): string {
  return binding.provider || binding.model || binding.effort
    ? t("settings-modelconfiguration.separately_configured")
    : t("settings-modelconfiguration.follow_agents");
}

export function ModelConfiguration({ open, onOpen, onProviders }: { open: string | null; onOpen: (id: string | null) => void; onProviders: () => void }) {
  return <ConfigurationManager>{(manager) => <ModelConfigurationForManager manager={manager} open={open} onOpen={onOpen} onProviders={onProviders} />}</ConfigurationManager>;
}

function ModelConfigurationForManager({ manager, open, onOpen, onProviders }: { manager: string; open: string | null; onOpen: (id: string | null) => void; onProviders: () => void }) {
  const [data, setData] = useState<ModelConfigurationBody | null>(null);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const load = async () => {
    try {
      setData(await modelConfiguration(manager));
      setError("");
    } catch (e) {
      setError(e instanceof ApiError && e.status === 404 ? t("settings-modelconfiguration.unsupported") : errorText(e));
    }
  };
  useEffect(() => { void load(); }, []);
  const groups = useMemo(() => purposeGroups(data?.purposes ?? [], query), [data, query]);
  const sourceName = (id?: string) => data?.purposes.find((p) => p.id === id)?.name ?? id;

  return (
    <>
      <h2 className="mb-3 px-1 text-[15px] font-semibold text-ink">{t("settings-nav.tab_model_config")}</h2>
      <ModelProfiles manager={manager} onChanged={() => void load()} />
      <div className="mb-4 flex items-center gap-2">
        <label className="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-control bg-surface px-3 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
          <Icon name="search" size={13} stroke={2} className="text-ink-3" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("settings-modelconfiguration.search_placeholder")} aria-label={t("settings-modelconfiguration.search_label")}
            className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3" />
          {query && <IconButton icon="x" label={t("settings-modelconfiguration.clear")} size={22} iconSize={12} onClick={() => setQuery("")} />}
        </label>
        <IconButton icon="retry" label={t("settings-modelconfiguration.refresh")} onClick={() => void load()} />
      </div>
      <p className="mb-5 px-1 text-[12px] leading-relaxed text-ink-3">{t("settings-modelconfiguration.intro")}</p>
      {!data && !error && <Loading />}
      {error && <p role="alert" className="mb-4 text-[13px] text-red">{error}</p>}
      {data && groups.length === 0 && <p className="py-10 text-center text-[13px] text-ink-3">{query ? t("settings-modelconfiguration.no_match", { query }) : t("settings-modelconfiguration.no_purposes")}</p>}
      {groups.map(([group, purposes]) => (
        <Group key={group} title={group}>
          {purposes.map((p) => (
            <Row key={p.id} label={p.name} hint={modelSummary(p, data?.providers)} onClick={() => onOpen(p.id)}>
              <span className={p.id === "lark.agent" ? "max-w-[180px] truncate text-[11.5px] text-ink-3" : "hidden max-w-[180px] truncate text-[11.5px] text-ink-3 sm:inline"}>{p.id === "lark.agent" ? larkAgentStatus(p.override) : p.status || (p.inherited_from ? t("settings-modelconfiguration.follows", { name: sourceName(p.inherited_from) ?? "" }) : t("settings-modelconfiguration.custom"))}</span>
            </Row>
          ))}
        </Group>
      ))}
      <Button tone="quiet" onClick={onProviders}>{t("settings-modelconfiguration.manage_providers")}</Button>
      <PurposeEditor manager={manager} key={open} id={open} sourceName={sourceName} onClose={() => onOpen(null)} onSaved={() => void load()} />
    </>
  );
}

const inputClass = "h-9 w-full rounded-control bg-field px-3 text-[13px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

function PurposeEditor({ manager, id, sourceName, onClose, onSaved }: { manager: string; id: string | null; sourceName: (id?: string) => string | undefined; onClose: () => void; onSaved: () => void }) {
  const alive = useRef(true);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const [data, setData] = useState<ModelPurposeBody | null>(null);
  const [draft, setDraft] = useState<ModelDraft>({ provider: "", model: "", effort: "", runtime: "" });
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [models, setModels] = useState<{ id: string; name?: string }[]>([]);
  const [modelsError, setModelsError] = useState("");
  // A launcher provider's /model list (#943), from a machine that has it (`machine`), and the picker over it.
  const [launcherList, setLauncherList] = useState<ModelList & { machine?: string; provider?: string }>({ status: "idle" });
  const [picking, setPicking] = useState(false);
  const [query, setQuery] = useState("");
  // "Retry" / "Reload": the next load asks the CLI again (refresh) and keeps showing the list meanwhile, once.
  const [reload, setReload] = useState(0);
  const refreshNext = useRef(false);
  const search = useRef<HTMLInputElement>(null);
  const machines = useStore((s) => s.machines);
  const load = async () => {
    if (!id) return;
    try {
      const r = await modelPurpose(id, manager);
      setData(r);
      setDraft(draftFor(r.purpose));
      setError("");
      setConflict(false);
    } catch (e) {
      setError(errorText(e));
    }
  };
  useEffect(() => { void load(); }, [id]);
  useEffect(() => {
    if (!id?.startsWith("plugin.")) return;
    const controller = new AbortController();
    api<{ models: { id: string; name?: string }[] }>(`${extRoot()}/${enc(id.slice("plugin.".length))}/models`, { signal: controller.signal, headers: { [MANAGER_HEADER]: manager } })
      .then((r) => setModels(r.models ?? []))
      .catch((e) => { if (!controller.signal.aborted) setModelsError(errorText(e)); });
    return () => controller.abort();
  }, [id]);

  const p = data?.purpose;
  const candidates = p ? providersFor(p, data.providers) : [];
  const chosen = data?.providers.find((v) => v.id === (draft.provider || p?.effective?.provider));
  const larkAgent = p?.id === "lark.agent";
  const writable = p && (["provider", "model", "effort"] as const).some((field) => supportsField(p, field));
  const effortAllowed = p && supportsField(p, "effort") && (larkAgent || !(chosen?.kind === "api" && chosen.api !== "responses"));
  const listProvider = p?.kind !== "plugin" && chosen?.kind === "launcher" && supportsField(p!, "model") ? chosen.id : "";
  useEffect(() => {
    const refresh = refreshNext.current;
    refreshNext.current = false;
    if (!refresh) { setPicking(false); setQuery(""); } // another provider: start closed; a refresh keeps the list open
    if (!listProvider) { setLauncherList({ status: "idle" }); return; }
    const controller = new AbortController();
    // Another provider's list never stays up while this one's loads (a pick would be the other provider's id).
    setLauncherList((l) => ({ status: "loading", provider: listProvider, list: refresh && l.provider === listProvider ? l.list : undefined }));
    providerModels(listProvider, { manager, refresh, machine: p?.machine_id, signal: controller.signal })
      .then((list) => setLauncherList({ status: "ready", list, machine: list?.machine, provider: listProvider }))
      .catch((e) => { if (!controller.signal.aborted) setLauncherList({ status: "error", error: errorText(e), provider: listProvider }); });
    return () => controller.abort();
  }, [listProvider, reload, p?.machine_id]);
  const reloadList = () => { refreshNext.current = true; setReload((n) => n + 1); };
  // The picker while there is a list (or one coming); the plain field without one: an older hub, a failure.
  const picker = launcherList.status === "loading" || (launcherList.status === "ready" && !!launcherList.list?.models.length);
  // Left empty, the model is inherited while the provider is too (what runs now when nothing here is set); with a
  // provider picked here it is that provider's default (the list's "Default").
  const inherited: ModelBinding = !draft.provider && !p?.override?.model && p?.effective?.model ? { model: p.effective.model } : {};
  const listedEfforts = launcherList.status === "ready" && launcherList.list && takesEffort(launcherList.list.agent)
    ? effortChoices(launcherList.list, draft.model, inherited) : ["low", "medium", "high", "xhigh", "max"];
  const effectiveProvider = data?.providers.find((v) => v.id === p?.effective?.provider);
  const effectiveAgent = providerAgent(effectiveProvider, p?.effective?.provider);
  const chosenAgent = (launcherList.provider === listProvider && launcherList.list?.agent) || providerAgent(chosen, draft.provider || p?.effective?.provider);
  const runtimes = p ? runtimeChoices(p, chosenAgent) : [];
  const listMachine = machines.find((m) => m.id === launcherList.machine)?.name ?? launcherList.machine;
  const save = async (restore = false) => {
    if (!p || !writable || busy || conflict) return;
    setBusy(true);
    setError("");
    try {
      const r = await saveModelPurpose(p, restore ? { provider: "", model: "", effort: "", runtime: draft.runtime } : draft, manager);
      if (!alive.current) return;
      setData(r);
      setDraft(draftFor(r.purpose));
      toast(restore ? t("settings-modelconfiguration.restored") : t("settings-modelconfiguration.saved"), "ok");
      onSaved();
      onClose();
    } catch (e) {
      const changed = e instanceof ApiError && e.status === 409;
      setConflict(changed);
      setError(changed ? t("settings-modelconfiguration.conflict") : errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={id !== null} onClose={() => { if (!busy) onClose(); }} width={480} title={p?.name ?? t("settings-modelconfiguration.dialog_title")}
      footer={<>
        <Button tone="quiet" disabled={busy || !writable || conflict} onClick={() => void save(true)}>{larkAgent ? t("settings-modelconfiguration.follow_agents") : t("settings-modelconfiguration.restore")}</Button>
        <span className="flex-1" />
        <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
        <Button tone="primary" busy={busy} disabled={!writable || conflict} onClick={() => void save()}>{t("common.save")}</Button>
      </>}
    >
      {!p && !error && <Loading />}
      {p && <div className="flex flex-col gap-4">
        {larkAgent && <p className="text-[12px] text-ink-3">{larkAgentStatus(p.override)}</p>}
        <Group title={t("settings-modelconfiguration.effective")} footer={p.inherited_from ? t("settings-modelconfiguration.inherited_from", { name: sourceName(p.inherited_from) ?? "" }) : t("settings-modelconfiguration.effective_footer")}>
          <Row label={t("settings-modelconfiguration.provider")} mono><span className="max-w-[220px] truncate">{providerName(p.effective?.provider, data?.providers) ?? t("settings-modelconfiguration.not_configured")}</span></Row>
          <Row label={t("settings-modelconfiguration.model")} mono><span className="max-w-[220px] truncate">{p.effective?.model || t("settings-modelconfiguration.provider_default")}</span></Row>
          {runtimes.length > 0 && <Row label={t("settings-modelconfiguration.runtime")}><span>{runtimeLabel(p.effective?.runtime, effectiveAgent)}</span></Row>}
          {p.status && <Row label={t("settings-modelconfiguration.status")}><Pill>{p.status}</Pill></Row>}
        </Group>
        {supportsField(p, "provider") && <label className="block">
          <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-modelconfiguration.model_provider")}</span>
          <select className={inputClass} value={draft.provider} disabled={busy} onChange={(e) => setDraft((d) => chooseProvider(d, e.target.value))}>
            <option value="">{larkAgent ? t("settings-modelconfiguration.follow_agents") : t("settings-modelconfiguration.follow_default")}</option>
            {draft.provider && !candidates.some((v) => v.id === draft.provider) && <option value={draft.provider}>{draft.provider.startsWith("legacy.") ? t("settings-modelconfiguration.not_configured") : t("settings-modelconfiguration.provider_unavailable", { id: draft.provider })}</option>}
            {candidates.map((v) => <option key={v.id} value={v.id}>{v.hidden ? t("settings-modelconfiguration.provider_hidden", { name: v.name }) : v.name}</option>)}
          </select>
        </label>}
        {supportsField(p, "model") && <div className="block">
          <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-modelconfiguration.specific_model")}</span>
          {p.kind === "plugin" ? <select aria-label={t("settings-modelconfiguration.specific_model")} className={inputClass} value={draft.model} disabled={busy || (!!modelsError && models.length === 0)} onChange={(e) => setDraft((d) => ({ ...d, model: e.target.value }))}>
            <option value="">{t("settings-modelconfiguration.follow_extension_default")}</option>
            {draft.model && !models.some((m) => m.id === draft.model) && <option value={draft.model}>{t("settings-modelconfiguration.model_current", { id: draft.model })}</option>}
            {models.map((m) => <option key={m.id} value={m.id}>{m.name || m.id}</option>)}
          </select> : picker ? <ModelPicker
            models={launcherList}
            model={draft.model}
            followed={inherited}
            open={picking}
            onToggle={() => setPicking((o) => !o)}
            query={query}
            onQuery={setQuery}
            search={search}
            onPick={(id) => {
              setDraft((d) => ({ ...d, model: id }));
              setPicking(false);
              setQuery("");
            }}
            onReload={reloadList}
            custom
            failedNote={t("settings-modelconfiguration.list_failed")}
          /> : <input aria-label={t("settings-modelconfiguration.specific_model")} className={`${inputClass} font-mono`} value={draft.model} disabled={busy} placeholder={chosen?.model || p.effective?.model || t("settings-modelconfiguration.follow_default")}
            spellCheck={false} autoComplete="off" onChange={(e) => setDraft((d) => ({ ...d, model: e.target.value }))} />}
          <span className="mt-1 block text-[11px] text-ink-3">
            {!picker ? t("settings-modelconfiguration.field_hint")
              : listMachine ? (draft.provider ? t("settings-modelconfiguration.picker_hint_provider_machine", { machine: listMachine }) : t("settings-modelconfiguration.picker_hint_inherit_machine", { machine: listMachine }))
              : draft.provider ? t("settings-modelconfiguration.picker_hint_provider") : t("settings-modelconfiguration.picker_hint_inherit")}
          </span>
          {modelsError && <span role="alert" className="mt-1 block text-[11px] text-red">{t("settings-modelconfiguration.extension_list_error", { error: modelsError })}</span>}
          {launcherList.status === "error" && (
            <span role="alert" className="mt-1 flex items-center gap-2 text-[11px] text-ink-3">
              <span className="min-w-0 truncate" title={launcherList.error}>{t("settings-modelconfiguration.list_error", { error: launcherList.error ?? "" })}</span>
              <button type="button" onClick={reloadList} className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline">{t("settings-modelconfiguration.retry")}</button>
            </span>
          )}
        </div>}
        {effortAllowed && <label className="block">
          <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-modelconfiguration.effort")}</span>
          <input className={inputClass} value={draft.effort} disabled={busy} placeholder={p.effective?.effort || t("settings-modelconfiguration.follow_default")} list="model-efforts"
            onChange={(e) => setDraft((d) => ({ ...d, effort: e.target.value }))} />
          <datalist id="model-efforts">{listedEfforts.map((v) => <option key={v} value={v} />)}</datalist>
        </label>}
        {runtimes.length > 0 && <div className="block">
          <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-modelconfiguration.runtime")}</span>
          <Segmented label={t("settings-modelconfiguration.runtime")} value={chosenAgent === "claude" ? draft.runtime : "cli"} options={runtimes.map((r) => ({ v: r.id, label: r.label }))}
            onChange={(runtime) => { if (!busy && chosenAgent === "claude") setDraft((d) => ({ ...d, runtime })); }} />
          {chosenAgent === "claude" && <span className="mt-1 block text-[11px] text-ink-3">{t("settings-modelconfiguration.runtime_hint")}</span>}
        </div>}
        <p className="text-[11.5px] leading-relaxed text-ink-3">{p.kind === "plugin" ? t("settings-modelconfiguration.save_note_plugin") : t("settings-modelconfiguration.save_note")}</p>
      </div>}
      {error && <p role="alert" className="mt-3 text-[12px] text-red">{error}</p>}
      {(conflict || (!p && error)) && <Button tone="secondary" disabled={busy} onClick={() => void load()}>{conflict ? t("settings-modelconfiguration.reload_discard") : t("settings-modelconfiguration.retry")}</Button>}
    </Dialog>
  );
}
