// One scheduled task in a dialog (the iOS app's AssistantTaskEditor): when (once / every day / every N minutes), the
// prompt, permission mode, which conversation runs go into, "Run now", the last result and the task's own runs.
// A new one has "Cancel" / "Add", so trying a template and backing out creates nothing; an existing one is saved with
// "Save" and deleted after an inline confirmation.
import { useEffect, useMemo, useState } from "react";
import { api, ApiError, enc, errorText } from "../../lib/api";
import { navigate } from "../../lib/router";
import {
  BYPASS, DAY_NAMES, everyLabel, fromLocalInput, INTERVALS, lastRunText, MAX_EVERY, MIN_EVERY, momentLabel, nextRun, normalizeDays,
  parseTime, PERMISSION_MODES, repeatsLabel, runDetail, runStatusWord, sameTask, taskBody, taskTitle, toLocalInput, untilLabel, validateTask,
  TASK_PUSH_FEATURE, WEEKEND, withPush, withRepeat, WORKDAYS, type Push, type Repeat, type Run, type Task,
} from "../../lib/routines";
import { hubHas, useStore } from "../../lib/store";
import { Button, cx, Dialog, Spinner, toast, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { scrollBehavior } from "../../ui/stream/motion";
import { Field, inputClass, Segmented, SectionTitle, Switch, textareaClass } from "./parts";
import { t } from "../../i18n";

/** Brings an inline confirmation into view once, when it appears (stable, so not on every render). */
const reveal = (el: HTMLElement | null) => el?.scrollIntoView({ block: "nearest", behavior: scrollBehavior() });

const everyOk = (m?: number) => Number.isInteger(m) && (m ?? 0) >= MIN_EVERY && (m ?? 0) <= MAX_EVERY;

export function TaskEditor({
  task,
  onClose,
  onSaved,
  onDeleted,
}: {
  task: Task;
  onClose: () => void;
  onSaved: (t: Task) => void;
  onDeleted: (t: Task) => void;
}) {
  const creating = !task.id;
  const [draft, setDraft] = useState<Task>(task);
  const [saved, setSaved] = useState<Task | null>(creating ? null : task);
  const [busy, setBusy] = useState<"" | "save" | "run" | "delete" | "fresh">("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [askBypass, setAskBypass] = useState(false);
  const [central, setCentral] = useState(false);
  const [runs, setRuns] = useState<Run[]>([]);
  const nowMs = useNow(30_000);
  const now = useMemo(() => new Date(nowMs), [nowMs]);
  /** The conversation it was scheduled from: "Continue the original chat" can be switched off (and back on before saving). */
  const origin = task.session_id;
  /** "Push when done" shows when the hub takes it (task_push), or the task already has it. */
  const hasPush = useStore((s) => hubHas(s.features, TASK_PUSH_FEATURE)) || task.push === "asked";

  useEffect(() => {
    let live = true;
    api("/model-config").then(() => live && setCentral(true), () => {});
    return () => {
      live = false;
    };
  }, []);

  useEffect(() => {
    if (!saved?.id) return;
    let live = true;
    api<{ runs: Run[] }>(`/assistant/runs?ref=${enc(saved.id)}`)
      .then((r) => live && setRuns(r.runs ?? []))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [saved?.id, saved?.last_run]);

  const problem = validateTask(draft, now);
  const changed = !saved || !sameTask(saved, draft);
  const set = (patch: Partial<Task>) => setDraft((d) => ({ ...d, ...patch }));
  const working = busy !== "";

  /** Saves what changed (adds a new one); returns the task as the hub has it, null when it didn't. */
  const persist = async (): Promise<Task | null> => {
    if (saved && !changed) return saved;
    if (problem) {
      toast(problem, "error");
      return null;
    }
    const body = taskBody(draft, saved);
    const r = saved
      ? await api<{ task: Task }>(`/assistant/tasks/${enc(saved.id)}`, { method: "PUT", json: body })
      : await api<{ task: Task }>("/assistant/tasks", { method: "POST", json: body });
    setSaved(r.task);
    setDraft((d) => ({ ...d, id: r.task.id }));
    onSaved(r.task);
    return r.task;
  };

  const save = async () => {
    setBusy("save");
    try {
      if (await persist()) {
        toast(creating ? t("assistant-taskeditor.added", { title: taskTitle(draft) }) : t("assistant-taskeditor.saved"), "ok");
        onClose();
      }
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setBusy("");
    }
  };

  const runNow = async () => {
    setBusy("run");
    try {
      const task = await persist();
      if (!task) return;
      const r = await api<{ session_id: string; machine_id: string }>(`/assistant/tasks/${enc(task.id)}/run`, { method: "POST", json: {} });
      toast(t("assistant-taskeditor.run_started", { title: taskTitle(task) }), "ok");
      onClose();
      navigate({ name: "persona", sid: r.session_id });
    } catch (e) {
      toast(e instanceof ApiError && e.status === 409 ? e.message || t("assistant-taskeditor.still_running") : errorText(e), "error");
    } finally {
      setBusy("");
    }
  };

  const startOver = async () => {
    if (!saved) return;
    setBusy("fresh");
    try {
      const r = await api<{ task: Task }>(`/assistant/tasks/${enc(saved.id)}/fresh`, { method: "POST", json: {} });
      setSaved((s) => (s ? { ...s, run_machine_id: r.task.run_machine_id, run_session_id: r.task.run_session_id, run_launcher: r.task.run_launcher } : s));
      onSaved({ ...saved, ...r.task });
      toast(t("assistant-taskeditor.fresh_next"), "ok");
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setBusy("");
    }
  };

  const remove = async () => {
    if (!saved) return;
    setBusy("delete");
    try {
      await api(`/assistant/tasks/${enc(saved.id)}`, { method: "DELETE" });
      toast(t("assistant-taskeditor.deleted", { title: taskTitle(saved) }), "ok");
      onDeleted(saved);
      onClose();
    } catch (e) {
      toast(errorText(e), "error");
      setBusy("");
    }
  };

  const open = (sid: string) => {
    onClose();
    navigate({ name: "persona", sid });
  };

  const close = () => {
    if (!working) onClose();
  };

  const footer = confirmDelete ? (
    <>
      <span className="mr-auto min-w-0 text-[12px] text-ink-2">{t("assistant-taskeditor.delete_confirm")}</span>
      <Button tone="quiet" disabled={working} onClick={() => setConfirmDelete(false)}>{t("common.cancel")}</Button>
      <Button tone="danger" busy={busy === "delete"} onClick={() => void remove()}>{t("assistant-taskeditor.delete")}</Button>
    </>
  ) : (
    <>
      {!creating && (
        <Button tone="quiet" icon="trash" className="text-red enabled:hover:text-red" disabled={working} onClick={() => setConfirmDelete(true)}>{t("assistant-taskeditor.delete")}</Button>
      )}
      <span className={cx("mr-auto min-w-0 truncate text-[11.5px]", problem ? "text-red" : "text-ink-3")}>{changed && problem ? problem : ""}</span>
      <Button tone="quiet" disabled={working} onClick={onClose}>{t("common.cancel")}</Button>
      <Button tone="primary" busy={busy === "save"} disabled={working || !!problem || (!creating && !changed)} onClick={() => void save()}>{creating ? t("assistant-taskeditor.add") : t("common.save")}</Button>
    </>
  );

  const next = nextRun(saved ?? draft);
  const last = saved ? lastRunText(saved, now) : null;
  const own = saved?.run_session_id;

  return (
    <Dialog open onClose={close} width={600} title={creating ? t("assistant-taskeditor.new_title") : taskTitle(saved ?? draft)} footer={footer}>
      <div className="-mx-4 max-h-[min(64vh,680px)] overflow-y-auto px-4 pb-1">
        <div className="flex flex-col gap-5">
          <div className="flex items-end gap-3">
            <div className="min-w-0 flex-1">
              <Field label={t("assistant-taskeditor.title_label")}>
                <input
                  className={inputClass}
                  value={draft.title}
                  autoFocus={creating}
                  placeholder={draft.repeat === "once" ? t("assistant-taskeditor.title_optional") : t("assistant-taskeditor.title_example")}
                  onChange={(e) => set({ title: e.target.value })}
                />
              </Field>
            </div>
            <label className="flex h-9 shrink-0 cursor-pointer items-center gap-2 whitespace-nowrap text-[12.5px] text-ink-2">
              {t("assistant-taskeditor.enabled")}
              <Switch checked={draft.enabled} label={t("assistant-taskeditor.enabled")} onChange={(on) => set({ enabled: on })} />
            </label>
          </div>

          <section>
            <SectionTitle>{t("assistant-taskeditor.section_when")}</SectionTitle>
            <div className="flex flex-col gap-3 rounded-card bg-inset p-3 shadow-hairline">
              <Segmented<Repeat>
                label={t("assistant-taskeditor.repeat")}
                value={draft.repeat}
                onChange={(r) => setDraft((d) => withRepeat(d, r, new Date()))}
                options={[
                  { v: "once", label: t("assistant-taskeditor.repeat_once") },
                  { v: "daily", label: t("assistant-taskeditor.repeat_daily") },
                  { v: "interval", label: t("assistant-taskeditor.repeat_interval") },
                ]}
              />
              {draft.repeat === "once" && <OnceWhen draft={draft} now={now} onChange={(at) => set({ at })} />}
              {draft.repeat === "daily" && <DailyWhen draft={draft} onChange={set} />}
              {draft.repeat === "interval" && <IntervalWhen minutes={draft.every_minutes ?? 0} onChange={(m) => set({ every_minutes: m })} />}
              {(draft.repeat === "daily" || draft.repeat === "interval") && <UntilWhen draft={draft} now={now} onChange={(until) => set({ until })} />}
            </div>
            <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">
              {draft.enabled && next && saved && !changed
                ? t("assistant-taskeditor.next_run", { when: momentLabel(next, now), until: untilLabel(next, now) })
                : draft.repeat === "once"
                  ? t("assistant-taskeditor.once_hint")
                  : draft.repeat === "daily"
                    ? t("assistant-taskeditor.daily_hint", { days: repeatsLabel(draft.weekdays), time: draft.time || "--:--" })
                    : everyOk(draft.every_minutes)
                      ? t("assistant-taskeditor.interval_hint", { every: everyLabel(draft.every_minutes ?? 0) })
                      : t("assistant-taskeditor.interval_hint_blank")}
            </p>
            {(draft.repeat === "daily" || draft.repeat === "interval") && (
              <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("assistant-taskeditor.until_hint")}</p>
            )}
          </section>

          <section>
            <SectionTitle>{t("assistant-taskeditor.section_prompt")}</SectionTitle>
            <textarea
              className={textareaClass}
              rows={6}
              value={draft.prompt}
              placeholder={t("assistant-taskeditor.prompt_placeholder")}
              aria-label={t("assistant-taskeditor.prompt_placeholder")}
              onChange={(e) => set({ prompt: e.target.value })}
            />
            <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("assistant-taskeditor.prompt_hint")}</p>
          </section>

          <section>
            <SectionTitle>{t("assistant-taskeditor.section_run")}</SectionTitle>
            <div>
              <Field label={t("assistant-taskeditor.permission_label")}>
                <select
                  className={draft.permission_mode === BYPASS ? inputClass.replace(" text-ink ", " font-medium text-red ") : inputClass}
                  value={askBypass ? BYPASS : draft.permission_mode === "default" ? "" : draft.permission_mode ?? ""}
                  onChange={(e) => {
                    const v = e.target.value;
                    if (v === BYPASS && draft.permission_mode !== BYPASS) setAskBypass(true);
                    else {
                      setAskBypass(false);
                      set({ permission_mode: v || undefined });
                    }
                  }}
                >
                  <option value="">{t("assistant-taskeditor.permission_default")}</option>
                  {draft.permission_mode && draft.permission_mode !== "default" && !PERMISSION_MODES.some((m) => m.id === draft.permission_mode) && (
                    <option value={draft.permission_mode}>{draft.permission_mode}</option>
                  )}
                  {PERMISSION_MODES.map((m) => (
                    <option key={m.id} value={m.id}>{m.title}</option>
                  ))}
                </select>
              </Field>
            </div>
            {askBypass ? (
              <div ref={reveal} role="alertdialog" aria-label={t("assistant-taskeditor.bypass_title")} className="mt-3 rounded-card bg-red-tint p-3">
                <p className="text-[13px] font-medium text-red">{t("assistant-taskeditor.bypass_title")}</p>
                <p className="mt-1 text-[12px] leading-relaxed text-ink-2">
                  {t("assistant-taskeditor.bypass_body")}
                </p>
                <div className="mt-2.5 flex justify-end gap-2">
                  <Button tone="quiet" onClick={() => setAskBypass(false)}>{t("common.cancel")}</Button>
                  <Button
                    tone="danger"
                    onClick={() => {
                      setAskBypass(false);
                      set({ permission_mode: BYPASS });
                    }}
                  >
                    {t("assistant-taskeditor.bypass_confirm")}
                  </Button>
                </div>
              </div>
            ) : draft.permission_mode === BYPASS ? (
              <p className="mt-1.5 flex items-center gap-1.5 px-1 text-[11.5px] font-medium text-red">
                <Icon name="info" size={13} />
                {t("assistant-taskeditor.bypass_on")}
              </p>
            ) : (
              <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">
                {t("assistant-taskeditor.permission_hint")}
                {draft.session_id ? t("assistant-taskeditor.permission_hint_resume") : ""}
              </p>
            )}
            {central && (
              <button
                type="button"
                onClick={() => {
                  onClose();
                  navigate("/settings?tab=model-config&purpose=assistant");
                }}
                className="mt-1 inline-flex items-center gap-1 whitespace-nowrap px-1 text-[11.5px] text-accent-ink hover:underline"
              >
                {t("assistant-taskeditor.model_config_assistant")}
                <Icon name="chevronRight" size={11} stroke={2.2} />
              </button>
            )}
          </section>

          {hasPush && (
            <section>
              <SectionTitle>{t("assistant-taskeditor.section_push")}</SectionTitle>
              <Segmented<Push>
                label={t("assistant-taskeditor.section_push")}
                value={draft.push ?? ""}
                onChange={(p) => setDraft((d) => withPush(d, p))}
                options={[
                  { v: "", label: t("assistant-taskeditor.push_every") },
                  { v: "asked", label: t("assistant-taskeditor.push_asked") },
                ]}
              />
              <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">
                {draft.push === "asked" ? t("assistant-taskeditor.push_hint_asked") : t("assistant-taskeditor.push_hint_every")}
              </p>
            </section>
          )}

          <section>
            <SectionTitle>{t("assistant-taskeditor.section_chat")}</SectionTitle>
            <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
              {origin && draft.push !== "asked" && (
                <div className="flex items-center gap-3 px-3.5 py-2.5">
                  <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{t("assistant-taskeditor.resume_chat")}</span>
                  <Switch checked={!!draft.session_id} label={t("assistant-taskeditor.resume_chat")} onChange={(on) => set({ session_id: on ? origin : undefined })} />
                </div>
              )}
              {!draft.session_id &&
                (own ? (
                  <>
                    <LinkRow icon="chat" label={t("assistant-taskeditor.same_chat")} onClick={() => open(own)} />
                    <LinkRow icon="plus" label={t("assistant-taskeditor.fresh_chat")} busy={busy === "fresh"} disabled={working} onClick={() => void startOver()} chevron={false} />
                  </>
                ) : (
                  <div className="flex items-center gap-2.5 px-3.5 py-2.5 text-[13px] text-ink-2">
                    <Icon name="chat" size={14} className="text-ink-3" />
                    {t("assistant-taskeditor.same_chat")}
                  </div>
                ))}
            </div>
            <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">
              {draft.session_id
                ? t("assistant-taskeditor.chat_hint_resume")
                : own
                  ? t("assistant-taskeditor.chat_hint_own")
                  : t("assistant-taskeditor.chat_hint_new")}
            </p>
          </section>

          <section>
            <div className="flex flex-wrap items-center gap-2">
              <Button tone="secondary" icon="play" busy={busy === "run"} disabled={working || !!problem} onClick={() => void runNow()}>{t("assistant-taskeditor.run_now")}</Button>
              {saved?.last_session_id && saved.last_session_id !== own && (
                <Button tone="quiet" icon="chat" disabled={working} onClick={() => open(saved.last_session_id!)}>{t("assistant-taskeditor.view_last")}</Button>
              )}
              {last && <span className={cx("text-[11.5px] tabular-nums", last.failed ? "text-red" : "text-ink-3")}>{last.text}</span>}
            </div>
            <p className={cx("mt-1.5 px-1 text-[11.5px] leading-relaxed", saved?.last_status === "failed" && saved.last_error ? "text-red" : "text-ink-3")}>
              {saved?.last_status === "failed" && saved.last_error
                ? t("assistant-taskeditor.last_failed", { error: saved.last_error })
                : creating || changed ? t("assistant-taskeditor.run_now_hint_save") : t("assistant-taskeditor.run_now_hint")}
            </p>
          </section>

          {runs.length > 0 && (
            <section>
              <SectionTitle right={runs.length > 5 ? <span className="text-[11px] text-ink-3">{t("assistant-taskeditor.run_total", { count: runs.length })}</span> : undefined}>{t("assistant-taskeditor.section_history")}</SectionTitle>
              <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
                {runs.slice(0, 5).map((r) => (
                  <RunLine key={r.id} run={r} now={now} onOpen={r.session_id ? () => open(r.session_id!) : undefined} />
                ))}
              </div>
            </section>
          )}
        </div>
      </div>
    </Dialog>
  );
}

function LinkRow({ icon, label, onClick, busy, disabled, chevron = true }: { icon: "chat" | "plus"; label: string; onClick: () => void; busy?: boolean; disabled?: boolean; chevron?: boolean }) {
  return (
    <button type="button" disabled={disabled} onClick={onClick} className="flex w-full items-center gap-2.5 whitespace-nowrap px-3.5 py-2.5 text-left text-[13px] text-accent-ink transition-colors enabled:hover:bg-hover disabled:opacity-50">
      <Icon name={icon} size={14} />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {busy ? <Spinner /> : chevron ? <Icon name="chevronRight" size={12} stroke={2} className="text-ink-3" /> : null}
    </button>
  );
}

/** One run in a task's history: when, how it went. */
export function RunLine({ run, now, onOpen, showTitle }: { run: Run; now: Date; onOpen?: () => void; showTitle?: boolean }) {
  const at = parseTime(run.at);
  const failed = run.status !== "ok" && run.status !== "skipped";
  const body = (
    <>
      <RunMark status={run.status} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] text-ink">{showTitle ? run.title : at ? momentLabel(at, now) : runStatusWord(run)}</span>
        <span className={cx("block truncate text-[11.5px]", failed ? "text-red" : "text-ink-3")}>
          {showTitle && at ? `${String(at.getHours()).padStart(2, "0")}:${String(at.getMinutes()).padStart(2, "0")} · ` : ""}
          {runDetail(run)}
        </span>
      </span>
      {onOpen && <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />}
    </>
  );
  return onOpen ? (
    <button type="button" onClick={onOpen} title={t("assistant-taskeditor.open_run")} className="flex w-full items-center gap-2.5 px-3.5 py-2 text-left transition-colors hover:bg-hover">
      {body}
    </button>
  ) : (
    <div className="flex items-center gap-2.5 px-3.5 py-2">{body}</div>
  );
}

export function RunMark({ status }: { status: string }) {
  if (status === "ok")
    return (
      <span aria-label={t("assistant-taskeditor.status_ok")} role="img" className="flex size-[18px] shrink-0 items-center justify-center rounded-full bg-green-tint text-green">
        <Icon name="check" size={11} stroke={3} />
      </span>
    );
  if (status === "skipped") return <span aria-label={t("assistant-taskeditor.status_skipped")} role="img" className="size-[18px] shrink-0 rounded-full border-[1.5px] border-line-strong" />;
  return (
    <span aria-label={t("assistant-taskeditor.status_failed")} role="img" className="flex size-[18px] shrink-0 items-center justify-center rounded-full bg-field text-red">
      <Icon name="x" size={11} stroke={3} />
    </span>
  );
}

function LocalDateTime({ label, value, now, onChange }: { label: string; value?: string; now: Date; onChange: (iso: string | undefined) => void }) {
  return (
    <Field label={label}>
      <input
        type="datetime-local"
        className={cx(inputClass, "tabular-nums")}
        value={toLocalInput(parseTime(value))}
        min={toLocalInput(now)}
        onChange={(e) => onChange(fromLocalInput(e.target.value)?.toISOString())}
      />
    </Field>
  );
}

function UntilWhen({ draft, now, onChange }: { draft: Task; now: Date; onChange: (until: string | undefined) => void }) {
  const value = toLocalInput(parseTime(draft.until));
  return (
    <div className="flex items-end gap-2">
      <div className="min-w-0 flex-1">
        <LocalDateTime label={t("assistant-taskeditor.until_label")} value={draft.until} now={now} onChange={onChange} />
      </div>
      {value && (
        <Button tone="quiet" className="whitespace-nowrap" onClick={() => onChange(undefined)}>
          {t("assistant-taskeditor.until_clear")}
        </Button>
      )}
    </div>
  );
}

function OnceWhen({ draft, now, onChange }: { draft: Task; now: Date; onChange: (at: string | undefined) => void }) {
  return <LocalDateTime label={t("assistant-taskeditor.time")} value={draft.at} now={now} onChange={onChange} />;
}

function DailyWhen({ draft, onChange }: { draft: Task; onChange: (p: Partial<Task>) => void }) {
  const days = normalizeDays(draft.weekdays);
  const selected = new Set(days.length ? days : [1, 2, 3, 4, 5, 6, 7]);
  const toggle = (d: number) => {
    const s = new Set(selected);
    if (s.has(d)) {
      if (s.size === 1) return; // no days would read as every day
      s.delete(d);
    } else s.add(d);
    onChange({ weekdays: normalizeDays([...s]) });
  };
  const shortcut = (label: string, value: number[]) => {
    const on = repeatsLabel(days) === repeatsLabel(value);
    return (
      <button
        key={label}
        type="button"
        aria-pressed={on}
        onClick={() => onChange({ weekdays: value })}
        className={cx("h-6 whitespace-nowrap rounded-full px-2.5 text-[11.5px] font-medium transition-colors", on ? "bg-accent-tint text-accent-ink" : "bg-field text-ink-3 hover:text-ink-2")}
      >
        {label}
      </button>
    );
  };
  return (
    <div className="flex flex-col gap-3">
      <Field label={t("assistant-taskeditor.time")}>
        <input
          type="time"
          className={cx(inputClass.replace("w-full", "w-36"), "tabular-nums")}
          value={draft.time ?? ""}
          onChange={(e) => onChange({ time: e.target.value.slice(0, 5) })}
        />
      </Field>
      <div>
        <span className="mb-1.5 block text-[12px] font-medium text-ink-2">{t("assistant-taskeditor.which_days")}</span>
        <div className="flex flex-wrap items-center gap-1.5">
          {DAY_NAMES.map((n, i) => {
            const on = selected.has(i + 1);
            return (
              <button
                key={n}
                type="button"
                aria-pressed={on}
                aria-label={t("assistant-taskeditor.weekday", { day: n })}
                onClick={() => toggle(i + 1)}
                className={cx(
                  "flex size-8 items-center justify-center rounded-full text-[12.5px] font-medium transition-[background-color,color,transform] duration-150 active:scale-[0.94]",
                  on ? "bg-accent text-white" : "bg-field text-ink-2 hover:bg-hover",
                )}
              >
                {n}
              </button>
            );
          })}
          <span className="mx-1 hidden h-4 w-px bg-line sm:block" />
          {shortcut(t("assistant-taskeditor.every_day"), [])}
          {shortcut(t("assistant-taskeditor.weekdays"), WORKDAYS)}
          {shortcut(t("assistant-taskeditor.weekend"), WEEKEND)}
        </div>
      </div>
    </div>
  );
}

function IntervalWhen({ minutes, onChange }: { minutes: number; onChange: (m: number) => void }) {
  const [text, setText] = useState(String(minutes || ""));
  useEffect(() => setText(String(minutes || "")), [minutes]);
  const bad = !everyOk(minutes);
  return (
    <div className="flex flex-col gap-2">
      <Field label={t("assistant-taskeditor.interval_label")} note={bad ? t("assistant-taskeditor.interval_bad") : undefined} error={bad}>
        <input
          type="number"
          inputMode="numeric"
          min={MIN_EVERY}
          max={MAX_EVERY}
          step={1}
          className={cx(inputClass.replace("w-full", "w-36"), "tabular-nums", bad && "shadow-[inset_0_0_0_1px_var(--red)]")}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            const n = Number(e.target.value);
            onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : n);
          }}
        />
      </Field>
      <div className="flex flex-wrap gap-1.5">
        {INTERVALS.map((m) => (
          <button
            key={m}
            type="button"
            aria-pressed={m === minutes}
            onClick={() => onChange(m)}
            className={cx("h-6 whitespace-nowrap rounded-full px-2.5 text-[11.5px] font-medium transition-colors", m === minutes ? "bg-accent-tint text-accent-ink" : "bg-field text-ink-3 hover:text-ink-2")}
          >
            {m < 60 ? t("assistant-taskeditor.minutes", { count: m }) : m % 1440 === 0 ? t("assistant-taskeditor.days", { count: m / 1440 }) : t("assistant-taskeditor.hours", { count: m / 60 })}
          </button>
        ))}
      </div>
    </div>
  );
}
