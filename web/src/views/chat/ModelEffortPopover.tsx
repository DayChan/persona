// The popover of the composer's "Thinking" chip on a Claude Code session (issue #2434, after Moshi's model pill): effort tabs on
// top, the agent's model list below. ↑↓ choose a model, ←→ an effort, Enter applies, Esc cancels, a click outside
// closes. The models are what the launcher's agent lists in its own /model menu (lib/launcher-models.ts); applying
// goes through the chip's `apply` (the model first, then the effort), each its own POST …/modes. Under the effort tabs,
// Claude Code's ultracode switch (#3428) applies at once, its own request. "Other model…" (#1644) takes a model ID typed by
// hand (the same POST …/modes), and the IDs typed before can be forgotten.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { t as tr } from "../../i18n";
import { errorText } from "../../lib/api";
import { keyboardFrame } from "../../lib/viewport";
import { forgetCustomModel, launcherModels, modelDetail, modelName, withoutCustom, type AgentModel, type LauncherModels } from "../../lib/launcher-models";
import { effortOptions, EFFORTS } from "../../lib/session-actions";
import { effortTabs, isCurrentModel, modelBusy, pickerKey, pickerPatch, sessionLauncher } from "../../lib/model-switch";
import { useStore } from "../../lib/store";
import { ultracodeOffered } from "../../lib/ultracode";
import type { Session } from "../../lib/types";
import { Button, cx, EASE, IconButton, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ModelIdField } from "../../ui/ModelIdField";
import { NARROW_QUERY, placeMenu, useMedia, type MenuPlace } from "../../ui/overlay";
import { UltracodeSwitchRow } from "./Ultracode";

const WIDTH = 340;

// a list read from the agent takes seconds the first time; later openings show the last one at once and read it again
// behind it (the IDs typed before change with each switch and Forget, here or elsewhere)
const cache = new Map<string, LauncherModels>();

export function ModelEffortPopover({
  session,
  anchor,
  onClose,
  apply,
}: {
  session: Session;
  anchor: HTMLElement;
  onClose: () => void;
  /** Sets one thing on the session (POST …/modes); false when it failed (the chip has told why). */
  apply: (patch: { model?: string; effort?: string }) => Promise<boolean>;
}) {
  const mid = session.machine_id;
  const launcher = sessionLauncher(session);
  const cacheKey = `${mid}/${launcher}`;
  const [list, setList] = useState<LauncherModels | null>(cache.get(cacheKey) ?? null);
  const [loading, setLoading] = useState(!cache.has(cacheKey));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const rows = useRef<(HTMLButtonElement | null)[]>([]);
  const narrow = useMedia(NARROW_QUERY);
  const [pos, setPos] = useState<React.CSSProperties>({ visibility: "hidden" });
  const ultracode = useStore((s) => ultracodeOffered(session, s.features));
  // what grows inside it after it opened (the ultracode row's reason or quota line) places it again
  const [height, setHeight] = useState(0);
  const [rowHeight, setRowHeight] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const row = el.querySelector<HTMLElement>("[data-popover-switch]");
    const ro = new ResizeObserver(() => {
      setHeight(el.offsetHeight);
      setRowHeight(row?.offsetHeight ?? 0);
    });
    ro.observe(el);
    if (row) ro.observe(row);
    return () => ro.disconnect();
  }, [ultracode]);

  // the typed IDs whose Forget is still on its way: a list read meanwhile doesn't bring them back
  const forgetting = useRef(new Set<string>());
  useEffect(() => {
    const ctl = new AbortController();
    launcherModels(mid, launcher, { signal: ctl.signal }).then(
      (r) => {
        if (ctl.signal.aborted) return;
        const fresh = r && forgetting.current.size ? { ...r, models: r.models.filter((m) => !(m.custom && forgetting.current.has(m.id))) } : r;
        if (fresh) cache.set(cacheKey, fresh);
        else cache.delete(cacheKey);
        setList(fresh);
        setLoading(false);
      },
      (e) => {
        if (ctl.signal.aborted) return;
        if (!cache.has(cacheKey)) setError(errorText(e)); // a list already shown stays
        setLoading(false);
      },
    );
    return () => ctl.abort();
  }, [mid, launcher, cacheKey]);

  const models = list?.models ?? [];
  const busyNote = modelBusy(session);
  // a launcher that fixes its own --model: a pick would not reach the agent
  const locked = Boolean(list?.locked);
  const switchable = models.length > 0 && !busyNote && !locked;
  // "Other model…" (#1644): also with an empty list or none read; Claude Code takes any `/model <id>`
  const typable = !loading && !busyNote && !locked;
  const current = session.model;

  // the model chosen, by its id (a Forget or a list read again moves the rows); null: keep the session's
  const [pickId, setPickId] = useState<string | null>(null);
  const pick = pickId === null ? -1 : models.findIndex((m) => m.id === pickId);
  const [typed, setTyped] = useState(""); // a good model ID in "Other model…"'s field, staged for Apply; "": none
  const [entering, setEntering] = useState(false);
  const typedBefore = useRef(""); // what Esc in the field puts back
  const [forgetError, setForgetError] = useState("");
  // setters only: the key handler, registered once, calls it too
  const chooseId = (id: string | null) => {
    setTyped("");
    setEntering(false);
    setPickId(id);
  };
  // Forget an ID typed before: off the list at once, back where it was when the hub says no.
  const forget = (id: string) => {
    if (!list) return;
    const { list: without, restore } = withoutCustom(list, id);
    setForgetError("");
    setList(without);
    cache.set(cacheKey, without);
    setPickId((p) => (p === id ? null : p));
    forgetting.current.add(id);
    forgetCustomModel(mid, launcher, id).then(
      () => forgetting.current.delete(id),
      (e) => {
        forgetting.current.delete(id);
        setList((now) => {
          const back = now ? restore(now) : list;
          cache.set(cacheKey, back);
          return back;
        });
        setForgetError(tr("composer.forget_failed", { id, error: errorText(e) }));
      },
    );
  };
  const [effort, setEffort] = useState(session.effort ?? "");
  const seeded = useRef(false);
  useEffect(() => {
    if (seeded.current || !models.length) return;
    seeded.current = true;
    setPickId(models.find((m) => isCurrentModel(m, current))?.id ?? null);
  }, [models, current]);

  const all = useMemo(() => effortOptions(session).map((e) => e.id), [session]);
  const typedModel: AgentModel | undefined = typed ? { id: typed, name: typed, custom: true } : undefined;
  const chosen: AgentModel | undefined = typedModel ?? (pick >= 0 ? models[pick] : undefined);
  // a typed ID's efforts aren't known: every tab stays
  const tabs = useMemo(() => (typedModel ? all : effortTabs(list, chosen ?? models.find((m) => isCurrentModel(m, current)), all)), [typedModel, list, chosen, models, current, all]);
  // the effort stays while the picked model takes it, else nothing is chosen (the session's own stays)
  const effortNow = tabs.includes(effort) ? effort : "";
  const patch = pickerPatch({ model: typedModel ? (typable ? typedModel : undefined) : switchable ? chosen : undefined, current, effort: effortNow, currentEffort: session.effort });
  const changed = Boolean(patch.model || patch.effort);

  const run = async () => {
    if (busy) return;
    if (!changed) return onClose();
    setBusy(true);
    // the model first: the efforts a model takes depend on it
    let ok = true;
    if (patch.model) ok = await apply({ model: patch.model });
    if (ok && patch.effort) ok = await apply({ effort: patch.effort });
    setBusy(false);
    if (ok) onClose();
  };

  // the key handler is registered once and reads this
  const live = useRef({ pick, effort: effortNow, tabs, models, run });
  live.current = { pick, effort: effortNow, tabs, models, run };

  useEffect(() => {
    const down = (e: PointerEvent) => {
      const t = e.target as Node | null;
      if (ref.current?.contains(t) || anchor.contains(t)) return;
      onClose();
    };
    const key = (e: KeyboardEvent) => {
      const l = live.current;
      // Enter on the footer's own buttons is theirs ("Cancel" must cancel), and on the ultracode switch its own
      if ((e.key === "Enter" || e.key === " ") && (e.target as Element | null)?.closest?.("[data-popover-footer],[data-popover-switch]")) return;
      // "Other model…"'s field takes its own keys (arrows move in the text, Esc puts the row back)
      if ((e.target as Element | null)?.closest?.("[data-model-id-field]")) return;
      const r = pickerKey({ model: l.pick, effort: l.tabs.indexOf(l.effort) }, { models: l.models.length, efforts: l.tabs.length }, e);
      if (!r.handled) return;
      e.preventDefault();
      e.stopPropagation();
      if (r.action === "cancel") return onClose();
      if (r.action === "apply") return void l.run();
      if (r.state.model !== l.pick && switchable) chooseId(l.models[r.state.model]?.id ?? null);
      if (r.state.effort !== l.tabs.indexOf(l.effort) && r.state.effort >= 0) setEffort(l.tabs[r.state.effort]);
    };
    addEventListener("pointerdown", down);
    addEventListener("keydown", key, true);
    return () => {
      removeEventListener("pointerdown", down);
      removeEventListener("keydown", key, true);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the handlers read `live`
  }, [anchor, onClose, switchable]);

  useEffect(() => {
    rows.current[pick]?.scrollIntoView({ block: "nearest" });
  }, [pick]);

  useLayoutEffect(() => {
    if (!ref.current) return;
    const view = narrow ? keyboardFrame(innerHeight, window.visualViewport) : undefined;
    const at: MenuPlace = placeMenu(anchor.getBoundingClientRect(), ref.current.offsetHeight, { width: WIDTH, align: "left", side: "top", screenW: innerWidth, screenH: innerHeight, view });
    const { maxHeight, ...rest } = at;
    // the ultracode row (#3428) brings its own height on top of the 520 px cap, so the models keep the room they had
    // without it (still within the room there is and 80% of the window); without the row it is as before
    const extra = ref.current.querySelector<HTMLElement>("[data-popover-switch]")?.offsetHeight ?? 0;
    if (extra) setPos({ ...rest, maxHeight: `min(80vh, ${520 + extra}px${maxHeight === undefined ? "" : `, ${maxHeight}px`})` });
    else setPos(maxHeight === undefined ? rest : { ...rest, maxHeight: `min(80vh, 520px, ${maxHeight}px)` });
  }, [anchor, narrow, loading, models.length, error, busyNote, tabs.length, height, rowHeight, entering, forgetError]);

  return createPortal(
    <div
      ref={ref}
      role="dialog"
      aria-label={tr("composer.model_effort")}
      className="fixed z-50 flex max-h-[min(80vh,520px)] flex-col rounded-[10px] bg-surface p-1 shadow-overlay"
      style={{ ...pos, animation: `pop-in 160ms ${EASE} both`, transformOrigin: "bottom left" }}
    >
      <div className="px-2 pt-1.5 pb-1 text-[10.5px] font-medium tracking-[0.06em] text-ink-3 uppercase">{tr("composer.effort")}</div>
      <div role="tablist" aria-label={tr("composer.effort")} className="flex gap-0.5 px-1 pb-1">
        {all.map((id) => {
          const title = (effortOptions(session).find((e) => e.id === id) ?? EFFORTS.find((e) => e.id === id))?.title ?? id;
          const allowed = tabs.includes(id);
          return (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={effortNow === id}
              disabled={!allowed}
              title={allowed ? effortOptions(session).find((e) => e.id === id)?.detail : tr("composer.effort_missing")}
              onClick={() => setEffort(id)}
              className={cx(
                "h-7 min-w-0 flex-1 truncate rounded-[6px] px-1 text-[12px] whitespace-nowrap transition-colors pointer-coarse:min-h-11 disabled:opacity-35",
                effortNow === id ? "bg-ink font-medium text-surface" : session.effort === id ? "bg-hover text-ink" : "text-ink-2 enabled:hover:bg-hover enabled:hover:text-ink",
              )}
            >
              {title}
            </button>
          );
        })}
      </div>
      {ultracode && <UltracodeSwitchRow session={session} />}

      {(loading || models.length > 0 || error || typable) && <div className="mt-1 border-t border-line px-2 pt-1.5 pb-1 text-[10.5px] font-medium tracking-[0.06em] text-ink-3 uppercase">{tr("composer.models")}</div>}
      {loading && (
        <div role="status" className="flex h-8 items-center gap-2 px-2 text-[12.5px] text-ink-3">
          <Spinner size={12} />
          {tr("composer.loading_models")}
        </div>
      )}
      {error && <div role="alert" className="px-2 py-1.5 text-[12px] break-words whitespace-pre-wrap text-red">{tr("composer.models_error", { error })}</div>}
      {models.length > 0 && (
        <div role="listbox" aria-label={tr("composer.models")} className="min-h-0 overflow-y-auto">
          {models.map((m, i) => {
            const cur = isCurrentModel(m, current);
            const detail = m.description || modelDetail(m);
            const row = (
              <button
                key={m.id}
                ref={(el) => {
                  rows.current[i] = el;
                }}
                type="button"
                role="option"
                aria-selected={!typed && pick === i}
                disabled={!switchable}
                onClick={() => chooseId(m.id)}
                onDoubleClick={() => void run()}
                className={cx(
                  "flex min-h-9 w-full min-w-0 items-center gap-2 rounded-[6px] px-2 py-1 text-left pointer-coarse:min-h-11 disabled:opacity-50",
                  !typed && pick === i ? "bg-hover" : "enabled:hover:bg-hover",
                )}
              >
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="flex items-center gap-1.5">
                    <span className={cx("min-w-0 truncate text-[12.5px] text-ink", m.custom && "font-mono")}>{modelName(m)}</span>
                    {cur && <span className="shrink-0 rounded-full bg-field px-1.5 text-[10.5px] leading-4.5 whitespace-nowrap text-ink-2">{tr("composer.current")}</span>}
                  </span>
                  {detail && <span className="truncate text-[11px] text-ink-3">{detail}</span>}
                </span>
                <Icon name="check" size={13} stroke={2.5} className={!typed && pick === i ? "shrink-0 text-ink" : "invisible shrink-0"} />
              </button>
            );
            // an ID typed before: its Forget beside it (#1644)
            return m.custom ? (
              <div key={m.id} className="flex items-center gap-0.5">
                {row}
                <IconButton icon="x" label={tr("composer.forget", { id: m.id })} size={24} iconSize={12} onClick={() => forget(m.id)} />
              </div>
            ) : row;
          })}
        </div>
      )}
      {/* outside the scrolling list, so the field and what it says stay in view */}
      {typable && (entering ? (
        <div className="px-1 pt-1 pb-0.5">
          {/* a good ID is staged as it is typed: Apply (or Enter) switches to it, no second button */}
          <ModelIdField
            initial={typed}
            note={tr("composer.typed_note")}
            useButton={false}
            onValid={setTyped}
            onUse={() => void live.current.run()}
            onCancel={() => {
              setTyped(typedBefore.current);
              setEntering(false);
            }}
          />
        </div>
      ) : (
        <button
          type="button"
          aria-current={typed ? "true" : undefined}
          data-testid="model-popover-other"
          onClick={() => {
            typedBefore.current = typed;
            setEntering(true);
          }}
          className={cx("flex min-h-9 w-full items-center gap-2 rounded-[6px] px-2 py-1 text-left pointer-coarse:min-h-11", typed ? "bg-hover" : "hover:bg-hover")}
        >
          <Icon name="keyboard" size={13} className="shrink-0 text-ink-3" />
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="truncate text-[12.5px] text-ink">{models.some((m) => !m.default && !m.custom) ? tr("composer.other_model") : tr("composer.enter_model")}</span>
            {typed && <span className="truncate font-mono text-[11px] text-ink-3">{typed}</span>}
          </span>
          <Icon name="check" size={13} stroke={2.5} className={typed ? "shrink-0 text-ink" : "invisible shrink-0"} />
        </button>
      ))}
      {forgetError && <div role="alert" className="px-2 py-1.5 text-[12px] break-words whitespace-pre-wrap text-red">{forgetError}</div>}
      {(switchable || typable) && <div className="px-2 pt-1 pb-0.5 text-[11px] break-words text-ink-3">{tr("composer.default_note")}</div>}
      {(busyNote || locked) && (
        <div role="status" className="px-2 py-1.5 text-[11.5px] break-words text-ink-3">
          {busyNote || tr("composer.locked")}
        </div>
      )}

      <div data-popover-footer className="mt-1 flex items-center gap-2 border-t border-line px-2 pt-1.5 pb-1">
        <span className="min-w-0 flex-1 truncate text-[11px] text-ink-3 max-sm:hidden pointer-coarse:hidden">{tr("composer.keys_hint")}</span>
        <Button tone="quiet" onClick={onClose}>{tr("common.cancel")}</Button>
        <Button tone="primary" busy={busy} disabled={!changed} onClick={() => void run()}>{tr("composer.apply")}</Button>
      </div>
    </div>,
    document.body,
  );
}
