// "Edit" on an open plan card (iOS PlanEditSheet): change each task's title, machine, directory, model, permission,
// ultracode (#12) and first message before it starts; "Start" starts the plan with them.
import { useState } from "react";
import { PLAN_PERMISSIONS, planEditsChanged, planEditsOf, type PlanTaskEdit, type TaskPlan } from "../../lib/feed";
import { hubHas, useStore } from "../../lib/store";
import { machineStartBlocked, ULTRACODE_FEATURE } from "../../lib/ultracode";
import { Button, Dialog } from "../../ui/atoms";
import { UltracodeStartRow } from "../chat/Ultracode";
import { t } from "../../i18n";

const FIELD = "w-full rounded-control border border-line bg-canvas px-3 py-1.5 text-[13.5px] text-ink outline-none focus:border-accent";
const LABEL = "mb-1 block text-[12px] font-medium text-ink-2";

export function permissionLabel(mode: string): string {
  return mode === "acceptEdits"
    ? t("assistant-tasks.perm_accept_edits")
    : mode === "plan"
      ? t("assistant-tasks.perm_plan")
      : mode === "bypassPermissions"
        ? t("assistant-tasks.perm_bypass")
        : t("assistant-tasks.perm_default");
}

/** The hub keeps the plan's model when the field is sent empty (applyPlanEdit): say so rather than "use the default". */
export function modelPlaceholder(planned?: string, tier?: string): string {
  const m = planned?.trim();
  if (m) return t("assistant-tasks.plan_edit_model_placeholder_kept", { model: m });
  return tier ? t("assistant-tasks.plan_edit_model_placeholder_tier", { tier }) : t("assistant-tasks.plan_edit_model_placeholder");
}

/** `ultracode`: each task's choice on the card, where "Edit" starts from: kept while its computer can't (the switch is
 *  drawn off, and what Start sends turns it off there), so moving the task to one that can turns it back on. */
export function PlanEditDialog({ plan, ultracode, onClose, onStart }: { plan: TaskPlan; ultracode?: readonly boolean[]; onClose: () => void; onStart: (edits: PlanTaskEdit[]) => void }) {
  const machines = useStore((s) => s.machines);
  const features = useStore((s) => s.features);
  const ultracodeOffered = hubHas(features, ULTRACODE_FEATURE);
  const [before] = useState<PlanTaskEdit[]>(() => planEditsOf(plan, ultracode));
  const [tasks, setTasks] = useState<PlanTaskEdit[]>(before);
  const set = (i: number, patch: Partial<PlanTaskEdit>) => setTasks((list) => list.map((x, j) => (j === i ? { ...x, ...patch } : x)));
  const close = () => {
    if (planEditsChanged(before, tasks) && !confirm(t("assistant-tasks.plan_edit_discard"))) return;
    onClose();
  };
  const ready = tasks.every((x) => x.title.trim() && x.cwd.trim() && x.prompt.trim());
  return (
    <Dialog
      open
      onClose={close}
      title={t("assistant-tasks.plan_edit")}
      width={520}
      footer={
        <>
          <Button onClick={close}>{t("common.cancel")}</Button>
          <Button tone="accent" disabled={!ready} onClick={() => onStart(tasks)}>
            {t("assistant-tasks.plan_start")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-5" data-testid="plan-edit">
        {tasks.map((task, i) => {
          const id = `plan-${plan.id}-${i}`;
          const known = machines.some((m) => m.id === task.machine);
          return (
            <fieldset key={i} className="flex flex-col gap-3">
              <legend className="mb-2 text-[12px] font-semibold text-ink-3">{tasks.length > 1 ? t("assistant-tasks.plan_edit_task_n", { n: i + 1 }) : t("assistant-tasks.plan_edit_task")}</legend>
              <div>
                <label htmlFor={`${id}-title`} className={LABEL}>{t("assistant-tasks.plan_edit_title")}</label>
                <input id={`${id}-title`} value={task.title} onChange={(e) => set(i, { title: e.target.value })} className={FIELD} />
              </div>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div>
                  <label htmlFor={`${id}-machine`} className={LABEL}>{t("assistant-tasks.plan_edit_machine")}</label>
                  <select id={`${id}-machine`} value={task.machine} onChange={(e) => set(i, { machine: e.target.value })} className={FIELD}>
                    {machines.map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.name}
                      </option>
                    ))}
                    {!known && <option value={task.machine}>{task.machine}</option>}
                  </select>
                </div>
                <div>
                  <label htmlFor={`${id}-permission`} className={LABEL}>{t("assistant-tasks.plan_edit_permission")}</label>
                  <select id={`${id}-permission`} value={task.permission} onChange={(e) => set(i, { permission: e.target.value })} className={FIELD}>
                    {PLAN_PERMISSIONS.map((p) => (
                      <option key={p} value={p}>
                        {permissionLabel(p)}
                      </option>
                    ))}
                  </select>
                </div>
              </div>
              <div>
                <label htmlFor={`${id}-cwd`} className={LABEL}>{t("assistant-tasks.plan_edit_cwd")}</label>
                <input id={`${id}-cwd`} value={task.cwd} spellCheck={false} autoCapitalize="off" autoCorrect="off" onChange={(e) => set(i, { cwd: e.target.value })} className={`${FIELD} font-mono text-[12.5px]`} />
              </div>
              <div>
                <label htmlFor={`${id}-model`} className={LABEL}>{t("assistant-tasks.plan_edit_model")}</label>
                <input id={`${id}-model`} value={task.model ?? ""} spellCheck={false} autoCapitalize="off" autoCorrect="off" placeholder={modelPlaceholder(plan.tasks[i]?.model, plan.tasks[i]?.tier)} onChange={(e) => set(i, { model: e.target.value })} className={FIELD} />
              </div>
              {ultracodeOffered && (
                <div data-testid="plan-edit-ultracode">
                  <UltracodeStartRow on={task.ultracode} blocked={machineStartBlocked({ features, machines }, task.machine)} onChange={(on) => set(i, { ultracode: on })} />
                </div>
              )}
              <div>
                <label htmlFor={`${id}-prompt`} className={LABEL}>{t("assistant-tasks.plan_edit_prompt")}</label>
                <textarea id={`${id}-prompt`} rows={4} value={task.prompt} onChange={(e) => set(i, { prompt: e.target.value })} className={`${FIELD} resize-y leading-relaxed`} />
              </div>
            </fieldset>
          );
        })}
      </div>
    </Dialog>
  );
}
