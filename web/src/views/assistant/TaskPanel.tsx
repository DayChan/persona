// A task's detail beside the assistant's conversation (docs/assistant/unified-entry.md "Task detail"; iOS TaskDetailSheet): its
// agent's own chat — everything the Agents page does works here — and, with several agents, a switch between them.
import { useState } from "react";
import { isRemovable, TODO_STATUS_INFO } from "../../lib/todos";
import { useStore } from "../../lib/store";
import { Button, IconButton, Menu, useMenu, type MenuItem } from "../../ui/atoms";
import { ChatView } from "../chat/ChatView";
import { PanelHeader } from "../SidePanel";
import { place, removeTaskNow, retryTaskNow, useRetrying, useTaskState, useTodo } from "./Tasks";
import { stateOf } from "../../lib/format";
import { usageLimitLabel } from "../../lib/usage-limit";
import { href, type Route } from "../../lib/router";
import { t } from "../../i18n";

/** ⋯'s "Open in new window": the page the panel shows, as its own tab (⌘-click on the link does the same). */
function openInWindow(to: Route): MenuItem {
  return { key: "open-window", label: t("common.open_in_new_window"), icon: "external", onSelect: () => void window.open(href(to), "_blank", "noopener") };
}

export function TaskPanel({ todoId, onClose }: { todoId: string; onClose: () => void }) {
  const todo = useTodo(todoId);
  const st = useTaskState(todo);
  const sessions = useStore((s) => s.sessions);
  const [picked, setPicked] = useState<string | null>(null);
  const retrying = useRetrying(todoId);
  if (!todo || !st) {
    return (
      <>
        <PanelHeader icon="todo" title={t("assistant-taskpanel.title")} onClose={onClose} />
        <div className="p-6 text-[13px] text-ink-2">{t("assistant-taskpanel.gone")}</div>
      </>
    );
  }
  const agents = (todo.agents ?? []).filter((a) => !a.released_at);
  const lead = st.lead;
  const chosen = agents.find((a) => `${a.machine_id}/${a.session_id}` === picked) ?? (lead ? agents.find((a) => a.session_id === lead.session_id) : agents[0]);
  const info = TODO_STATUS_INFO[st.status];
  // the header also stands for the agent shown: after ⌄ picks another, its machine and project, not the lead's
  const shown = chosen ? (sessions[chosen.machine_id] ?? []).find((x) => x.session_id === chosen.session_id) : undefined;
  // The chat header prepends the usage-limit line of the agent it shows, so the status word is not said twice.
  const head = st.status === "usage_limit" && usageLimitLabel(shown ?? lead) ? "" : info.label;
  const subtitle = [head, chosen ? place({ ...todo, agents: [chosen] }, shown ?? lead) : place(todo, lead)].filter(Boolean).join(" · ");
  const taskItems: MenuItem[] = [
    openInWindow({ name: "todos", id: todo.id }),
    ...(isRemovable(st.status) ? [{ key: "remove", label: t("assistant-taskpanel.remove_ellipsis"), icon: "trash" as const, danger: true, onSelect: () => removeTaskNow(todo) }] : []),
  ];
  // several agents: the ⌄ beside the title switches between them (iOS #3255)
  const switcher: MenuItem[] = agents.map((a) => {
    const s = (sessions[a.machine_id] ?? []).find((x) => x.session_id === a.session_id);
    const key = `${a.machine_id}/${a.session_id}`;
    return { key, label: s?.title || a.title || a.agent || a.session_id.slice(0, 8), state: s ? stateOf(s) : undefined, checked: chosen === a, onSelect: () => setPicked(key) };
  });
  if (chosen) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <ChatView
          key={`${chosen.machine_id}/${chosen.session_id}`}
          mid={chosen.machine_id}
          sid={chosen.session_id}
          panel={{ onClose, title: todo.title, sub: subtitle, items: taskItems, agents: switcher }}
        />
      </div>
    );
  }
  return (
    <>
      <PanelHeader icon="todo" title={todo.title} subtitle={subtitle} actions={<PanelMore items={taskItems} />} onClose={onClose} />
      <div className="flex min-h-0 flex-1 flex-col">
        {st.failure || todo.agents?.some((a) => a.start_error) ? (
          <div className="flex flex-col items-start gap-3 p-6 text-[13px]">
            <div className="font-semibold text-red">{t("assistant-taskpanel.start_failed")}</div>
            <div className="text-ink-2">{st.failure ?? todo.agents?.find((a) => a.start_error)?.start_error}</div>
            <div className="flex gap-2">
              {st.status === "failed" && (
                <Button icon="retry" busy={retrying} disabled={retrying} onClick={() => retryTaskNow(todo)}>
                  {t("assistant-taskpanel.retry")}
                </Button>
              )}
              <Button icon="trash" onClick={() => removeTaskNow(todo)}>
                {t("assistant-taskpanel.remove")}
              </Button>
            </div>
          </div>
        ) : (
          <div className="p-6 text-[13px] text-ink-2">{t("assistant-taskpanel.starting")}</div>
        )}
      </div>
    </>
  );
}

/** The panel header's ⋯ while no agent's chat (and so no chat header) is there yet. */
function PanelMore({ items }: { items: MenuItem[] }) {
  const more = useMenu();
  return (
    <>
      <IconButton icon="more" label={t("assistant-taskpanel.more")} onClick={more.open} active={Boolean(more.anchor)} />
      {more.anchor && <Menu anchor={more.anchor} onClose={more.close} align="right" items={items} />}
    </>
  );
}

/** An agent the main conversation's dock shows that no task stands for (#1113; iOS opens it in the task's sheet): its
 * own chat, whose header is the panel's (its state, title and machine; ⋯ opens it in a new window). */
export function AgentPanel({ mid, sid, onClose }: { mid: string; sid: string; onClose: () => void }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ChatView key={`${mid}/${sid}`} mid={mid} sid={sid} panel={{ onClose, items: [openInWindow({ name: "session", mid, sid })] }} />
    </div>
  );
}
