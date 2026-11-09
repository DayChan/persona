// A subagent's whole conversation in the side panel (its card's "View full run"), read-only: the chat it was launched
// from stays as it was.
import { useStore } from "../../lib/store";
import { subagentParent, subagentTitle } from "../../lib/tasks";
import { workflowAgentFor, workflowName } from "../../lib/workflows";
import {  } from "../../ui/atoms";
import { StatusMark } from "../../ui/StatusMark";
import { PanelHeader } from "../../ui/PanelHeader";
import { ChatView } from "./ChatView";
import { t } from "../../i18n";

export function SubagentPanel({ mid, sid, onClose }: { mid: string; sid: string; onClose: () => void }) {
  const parent = useStore((s) => (s.sessions[mid] ?? []).find((x) => x.session_id === subagentParent(sid)));
  const agent = parent?.subagents?.find((a) => a.session_id === sid);
  // one of a background workflow's agents (#2935): named by its label, under its run
  const wf = agent ? null : workflowAgentFor(parent, sid);
  const running = agent?.running ?? wf?.agent.running;
  return (
    <>
      <PanelHeader
        icon="agent"
        title={
          <span className="flex min-w-0 items-center gap-1.5">
            <StatusMark state={running ? "working" : "unread"} />
            <span className="truncate">{agent ? subagentTitle(agent) : wf?.agent.label || t("chat-subagentpanel.subagent")}</span>
          </span>
        }
        subtitle={[wf ? t("lib-workflows.line_name", { name: workflowName(wf.run) }) : t("chat-subagentpanel.subagent"), agent?.fork ? t("chat-subagentpanel.fork") : agent?.type ?? wf?.agent.phase, running ? t("chat-subagentpanel.running_read_only") : t("chat-subagentpanel.read_only")].filter(Boolean).join(" · ")}
        onClose={onClose}
      />
      <div className="flex min-h-0 flex-1 flex-col">
        <ChatView key={`${mid}/${sid}`} mid={mid} sid={sid} readOnly />
      </div>
    </>
  );
}
