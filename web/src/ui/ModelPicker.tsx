// A launcher's /model list to pick from (#794, #943): "New agent"'s "Model" and "Models"' "Model". A trigger showing the pick,
// a searchable list under it with "Default" first; "Models" also takes an id the list does not have (`custom`). "New
// agent" also lists the IDs typed before (each can be forgotten) and "Other model…", a field for a model ID (#1644).
import { useEffect, useState } from "react";
import type { ModelBinding } from "../lib/model-config";
import { customModel, defaultDetail, defaultModel, modelDetail, modelName, modelRows, type AgentModel, type LauncherModels } from "../lib/launcher-models";
import { cleanModelId, typedModelProblem } from "../lib/model-id";
import { Collapse, cx, IconButton, Spinner } from "./atoms";
import { Icon } from "./icons";
import { ModelIdField } from "./ModelIdField";
import { t } from "../i18n";

/** The list for the launcher that runs: idle (no launcher to ask), loading, the answer (null: an older hub), failed. */
export interface ModelList {
  status: "idle" | "loading" | "ready" | "error";
  list?: LauncherModels | null;
  error?: string;
}

export function ModelPicker({ models, model, followed = {}, ownModel, open, onToggle, query, onQuery, search, onPick, onReload, custom, typing, onForget, failedNote }: {
  models: ModelList;
  /** The pick's id; "": "Default". */
  model: string;
  /** "Models"' binding when it is what runs with no pick ("New agent" following it). */
  followed?: ModelBinding;
  /** What the launcher passes as its own --model (shown when it is locked). */
  ownModel?: string;
  open: boolean;
  onToggle: () => void;
  query: string;
  onQuery: (q: string) => void;
  search: React.RefObject<HTMLInputElement | null>;
  onPick: (id: string) => void;
  onReload: () => void;
  /** An id the list does not have can be picked: what is typed in the search, and the current one stays shown. */
  custom?: boolean;
  /** "Other model…" (#1644): a field for a model ID the list doesn't have, also when the list is empty or couldn't be
   *  read; a typed pick stays shown as one. */
  typing?: boolean;
  /** Forgets an ID typed before (the hub's `custom` rows); absent: they can't be forgotten here. */
  onForget?: (id: string) => void;
  /** The list's last line when it could not be read. */
  failedNote: string;
}) {
  const all = models.list?.models ?? [];
  const locked = models.list?.locked;
  const loading = models.status === "loading";
  const picked = all.find((m) => m.id === model);
  const unlisted = !picked && model ? model : ""; // "Models"' id from before, or one typed in
  const { showDefault, rows } = modelRows(all, query, followed);
  const own = rows.filter((m) => !m.custom);
  const remembered = rows.filter((m) => m.custom);
  const takesTyped = custom || typing;
  const typed = takesTyped ? customModel(all, query) : "";
  // what is typed in the search, when it is no model's and the hub wouldn't take it as one: why
  const typedProblem = takesTyped && !rows.length && !all.some((m) => m.id === cleanModelId(query)) ? typedModelProblem(query) : "";
  const [entering, setEntering] = useState(false);
  useEffect(() => {
    if (!open) setEntering(false);
  }, [open]);
  const pickFirst = () => {
    if (showDefault) onPick("");
    else if (rows[0]) onPick(rows[0].id);
    else if (typed) onPick(typed);
  };
  const detail = picked ? (picked.custom ? t("ui-modelpicker.typed") : modelDetail(picked)) : unlisted ? (typing ? t("ui-modelpicker.typed") : t("ui-modelpicker.not_in_list")) : locked ? (ownModel ?? "") : defaultDetail(all, followed);
  const otherLabel = all.some((m) => !m.default && !m.custom) ? t("ui-modelpicker.other") : t("ui-modelpicker.enter_id");
  const typedRow = (m: AgentModel) => (
    <div key={m.id} className="flex items-center gap-0.5">
      {/* under the "Typed before" heading: the hub's line saying what it is would only repeat it */}
      <ModelRow name={modelName(m)} selected={m.id === model} onPick={() => onPick(m.id)} mono />
      {onForget && <IconButton icon="x" label={t("ui-modelpicker.forget", { id: m.id })} size={24} iconSize={12} onClick={() => onForget(m.id)} />}
    </div>
  );
  return (
    <>
      <button
        type="button"
        disabled={!!locked}
        aria-expanded={open}
        onClick={onToggle}
        title={picked?.description ?? (picked || unlisted ? undefined : defaultModel(all)?.description)}
        className="flex h-9 w-full items-center gap-2 rounded-control bg-inset px-3 text-left shadow-hairline outline-none transition-colors enabled:hover:bg-hover focus-visible:shadow-[0_0_0_1px_var(--line-strong)] disabled:cursor-not-allowed"
      >
        <span className={cx("max-w-[60%] shrink-0 truncate text-[12.5px]", locked ? "text-ink-3" : "text-ink", unlisted && "font-mono")}>{picked ? modelName(picked) : unlisted ? modelName({ id: unlisted }) : t("ui-modelpicker.default")}</span>
        {loading && !all.length ? (
          <span className="flex min-w-0 flex-1 items-center gap-1.5 text-[11.5px] text-ink-3"><Spinner size={10} /><span className="truncate">{t("ui-modelpicker.loading")}</span></span>
        ) : models.status === "error" && !unlisted && !typing ? (
          <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink-3">{t("ui-modelpicker.load_failed")}</span>
        ) : detail ? (
          <span className={cx("min-w-0 flex-1 truncate text-ink-3", unlisted ? "text-[11.5px]" : "font-mono text-[11px]")}>{detail}</span>
        ) : (
          <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink-3">{(picked ?? (locked ? undefined : defaultModel(all)))?.description}</span>
        )}
        <Icon name={locked ? "lock" : "chevron"} size={13} className={cx("shrink-0 text-ink-3 transition-transform duration-200", open && !locked && "rotate-180")} />
      </button>
      {locked && <p className="mt-1.5 text-[11.5px] text-ink-3">{locked}</p>}
      {models.status === "error" && (
        <p className="mt-1.5 flex items-center gap-2 text-[11.5px] text-ink-3">
          <span className="min-w-0 truncate" title={models.error}>{models.error}</span>
          <button type="button" onClick={onReload} className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline">{t("ui-modelpicker.retry")}</button>
        </p>
      )}
      {/* -mx-px / px-px pb-px: room for the box's hairline inside Collapse's clip */}
      <Collapse open={open && !locked} className="-mx-px">
        <div inert={!open || !!locked} className="px-px pb-px"><div className="mt-1.5 rounded-control bg-surface shadow-hairline">
          <div className="flex items-center gap-2 border-b border-line pr-1 pl-2.5">
            <Icon name="search" size={13} className="shrink-0 text-ink-3" />
            <input
              ref={search}
              value={query}
              onChange={(e) => onQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing || e.keyCode === 229) return;
                if (e.key === "Enter") { e.preventDefault(); pickFirst(); }
                if (e.key === "Escape") { e.stopPropagation(); onToggle(); }
              }}
              aria-label={t("ui-modelpicker.search_label")}
              placeholder={custom ? t("ui-modelpicker.search_custom") : t("ui-modelpicker.search")}
              spellCheck={false}
              autoComplete="off"
              className="h-8 min-w-0 flex-1 bg-transparent text-[12.5px] text-ink outline-none placeholder:text-ink-3"
            />
            {all.length > 0 && (
              <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">
                {query.trim() ? `${rows.length + (showDefault ? 1 : 0)} / ${all.length}` : t("ui-modelpicker.count", { count: all.length })}
              </span>
            )}
            <IconButton icon="retry" label={t("ui-modelpicker.reload")} size={24} iconSize={12} disabled={loading} onClick={onReload} />
          </div>
          <div role="listbox" aria-label={t("ui-modelpicker.list_label")} className="max-h-60 overflow-y-auto p-1">
            {showDefault && <ModelRow name={t("ui-modelpicker.default")} detail={defaultDetail(all, followed)} note={defaultModel(all)?.description} selected={!picked && !unlisted} onPick={() => onPick("")} />}
            {unlisted && !query.trim() && <ModelRow name={modelName({ id: unlisted })} note={typing ? t("ui-modelpicker.typed") : t("ui-modelpicker.current_unlisted")} selected onPick={() => onPick(unlisted)} mono />}
            {own.map((m) => (
              <ModelRow key={m.id} name={modelName(m)} detail={modelDetail(m)} note={m.description} selected={m.id === model} onPick={() => onPick(m.id)} />
            ))}
            {remembered.length > 0 && (
              <div role="presentation" className="px-2 pt-2 pb-1 text-[10.5px] font-medium tracking-[0.06em] text-ink-3 uppercase">{t("ui-modelpicker.typed_before")}</div>
            )}
            {remembered.map(typedRow)}
            {typed && <ModelRow name={t("ui-modelpicker.use_custom", { id: typed })} note={t("ui-modelpicker.use_custom_note")} selected={typed === unlisted} onPick={() => onPick(typed)} />}
            {typedProblem && (
              <div data-testid="model-picker-problem" className="flex min-h-8 items-center gap-2 px-2 py-1 text-[12px] text-red">
                <Icon name="warning" size={13} className="shrink-0" />
                <span className="min-w-0 break-words">{typedProblem}</span>
              </div>
            )}
            {loading && (
              <div className="flex h-8 items-center gap-2 px-2 text-[12px] text-ink-3"><Spinner size={10} />{t("ui-modelpicker.loading_slow")}</div>
            )}
            {models.status === "error" && (
              <div className="flex h-8 items-center px-2 text-[12px] text-ink-3"><span className="truncate">{failedNote}</span></div>
            )}
            {models.status === "ready" && !showDefault && rows.length === 0 && !typed && !typedProblem && (
              <div className="flex h-8 items-center px-2 text-[12px] text-ink-3"><span className="truncate">{t("ui-modelpicker.no_match", { query: query.trim() })}</span></div>
            )}
          </div>
          {/* outside the scrolling list, so the field and what it says stay in view */}
          {typing && !query.trim() && models.status !== "loading" && (
            <div className="border-t border-line p-1">
              {entering ? (
                <ModelIdField className="p-0.5" initial={unlisted} note={t("ui-modelpicker.typed_note")} onUse={(id) => { setEntering(false); onPick(id); }} onCancel={() => setEntering(false)} />
              ) : (
                <button
                  type="button"
                  data-testid="model-picker-other"
                  onClick={() => setEntering(true)}
                  className="flex h-8 w-full items-center gap-2 rounded-[6px] px-2 text-left text-[12.5px] text-ink-2 transition-colors hover:bg-hover hover:text-ink pointer-coarse:min-h-11"
                >
                  <Icon name="keyboard" size={13} className="shrink-0 text-ink-3" />
                  <span className="min-w-0 flex-1 truncate">{otherLabel}</span>
                </button>
              )}
            </div>
          )}
        </div></div>
      </Collapse>
    </>
  );
}

// A row of the list: the model's name, then the id / concrete model it stands for, or its description when the name
// already says that (Codex's GPT-5.5-Mini).
function ModelRow({ name, detail, note, selected, onPick, mono }: { name: string; detail?: string; note?: string; selected: boolean; onPick: () => void; mono?: boolean }) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      onClick={onPick}
      title={note}
      className={cx("flex h-8 w-full items-center gap-2 rounded-[6px] px-2 text-left transition-colors", selected ? "bg-accent-tint" : "hover:bg-hover")}
    >
      <span className={cx("max-w-[60%] shrink-0 truncate text-[12.5px]", selected ? "font-medium text-accent-ink" : "text-ink", mono && "font-mono")}>{name}</span>
      {detail ? <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink-3">{detail}</span> : <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink-3">{note}</span>}
      {selected && <Icon name="check" size={13} stroke={2.5} className="shrink-0 text-accent-ink" />}
    </button>
  );
}
