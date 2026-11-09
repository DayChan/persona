// The side panel's Plan (#5602): the chat's newest plan as a whole list, following it as the agent updates it.
import { usePlan } from "../../lib/plan-store";
import { planProgress } from "../../lib/work-groups";
import { t } from "../../i18n";
import { PanelHeader } from "../../ui/PanelHeader";
import { PlanList, planSummary } from "./PlanCard";

export function PlanPanel({ mid, sid, onClose }: { mid: string; sid: string; onClose: () => void }) {
  const plan = usePlan(mid, sid);
  const p = plan ? planProgress(plan.todos) : null;
  return (
    <div className="flex h-full min-h-0 flex-col" data-plan-panel>
      <PanelHeader icon="todo" title={t("chat-plancard.plan")} subtitle={p ? planSummary(p) : undefined} onClose={onClose} />
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        {plan?.todos.length ? <PlanList todos={plan.todos} live={plan.live} /> : <p className="text-[13px] text-ink-3">{t("chat-plancard.none")}</p>}
      </div>
    </div>
  );
}
