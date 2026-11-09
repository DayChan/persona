// Beautiful UI Todos View: left list (sorted by status & created_at), right detail, handover dialog.
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, enc, errorText } from "../lib/api";
import { ago, clock, shortPath, stateOf, tabLabel, uuid } from "../lib/format";
import { launchAgent } from "../lib/new-agent";
import { initialLauncher, launcherBlock, usableFirst } from "../lib/launcher-health";
import { navigate } from "../lib/router";
import { interactionAsk } from "../lib/inbox";
import { usageLimitLabel } from "../lib/usage-limit";
import { plainMarkdown } from "../lib/plain-markdown";
import { composing } from "../lib/send-history";
import { useAgentSessions } from "../lib/sessions";
import { dropTodo, refresh, refreshTodos, useStore } from "../lib/store";
import {
  addTodoDecision,
  createTodo,
  decisionSourceLabel,
  deleteTodo,
  fetchTodoDecisions,
  setDecisionSupersedes,
  supersedeCandidates,
  formatAgentSource,
  formatTodoContext,
  isLegacySpacesRouteError,
  makeSessionLookup,
  mostUrgentAgent,
  sortTodos,
  TODO_STATUS_INFO,
  heldDelivery,
  TODO_TONE_CLASS,
  isAutoDone,
  todoStatusLabel,
  todoStatus,
  unlinkAgentFromTodo,
  unlinkSpaceFromTodo,
  updateTodo,
  type Todo,
  type TodoAgent,
  type TodoDecision,
  type TodoStatus,
  type SessionLookup,
  taskNow,
} from "../lib/todos";
import type { AgentState, Interaction, Launcher, Machine } from "../lib/types";
import { Button, cx, Dialog, IconButton, Loading, Menu, toast, useMenu } from "../ui/atoms";
import { SearchField } from "../ui/SearchField";
import { StatusMark } from "../ui/StatusMark";
import { Icon } from "../ui/icons";
import { t } from "../i18n";
import { groupTodosByProject, treeStateLabel, type TodoProjectGroup, type TreeState } from "../lib/todo-tree";
import { useFolded } from "../lib/use-folded";
import { answeredLookup } from "./assistant/answered-lookup";
import { Empty } from "./assistant/parts";
import { ReviewBadge, ReviewChecklist } from "./assistant/Supervision";
import { TodoStageBar } from "./TodoStageBar";
import { todoUltracode } from "../lib/ultracode";
import { UltraMark } from "./chat/Ultracode";
import { boundThread, threadLine, threadName, todoThreadsOn, unbindTodoThread } from "../lib/todo-thread";

export function TodosView({
  selectedId,
  onToggleSidebar,
}: {
  selectedId?: string;
  onToggleSidebar?: () => void;
}) {
  const todos = useStore((s) => s.todos);
  const todosLoaded = useStore((s) => s.todosLoaded);
  const todosError = useStore((s) => s.todosError);
  const machines = useStore((s) => s.machines);
  const interactions = useStore((s) => s.interactions);
  const allSessions = useAgentSessions();
  const answered = useStore((s) => s.answered);
  // a card answered here stops its agent's "Needs you" at once, not at the hub's next push (#4186)
  const lookup = useMemo(() => answeredLookup(allSessions, interactions, answered), [allSessions, interactions, answered]);

  const [activeId, setActiveId] = useState<string | undefined>(selectedId);
  const [search, setSearch] = useState("");
  const [newOpen, setNewOpen] = useState(false);
  const [handoverOpen, setHandoverOpen] = useState(false);
  const [deleteConfirm, setDeleteConfirm] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [showDone, setShowDone] = useState(false);
  const [showReleased, setShowReleased] = useState(false);

  // Sync selectedId from URL
  useEffect(() => {
    if (selectedId) setActiveId(selectedId);
  }, [selectedId]);

  // Separate uncompleted and completed todos
  const { uncompleted, completed } = useMemo(() => {
    const un: Todo[] = [];
    const comp: Todo[] = [];
    for (const t of todos) {
      if (t.done_at) comp.push(t);
      else un.push(t);
    }
    return {
      uncompleted: sortTodos(un, lookup),
      completed: sortTodos(comp, lookup),
    };
  }, [todos, lookup]);
  // A sessions push builds a new lookup: what it leaves in the same order stays the same list (and the same groups,
  // the same row views below), so the rows and sections it did not touch skip their render.
  const steadyUncompleted = useSteady(uncompleted, sameList);
  const steadyCompleted = useSteady(completed, sameList);
  const views = useSteady(useMemo(() => todoRowViews(todos, lookup, interactions), [todos, lookup, interactions]), steadyViews);

  // Filter by search query
  const filterTodo = (t: Todo) => {
    const q = search.trim().toLowerCase();
    if (!q) return true;
    return t.title.toLowerCase().includes(q) || (t.notes ?? "").toLowerCase().includes(q);
  };

  const filteredUncompleted = useMemo(() => steadyUncompleted.filter(filterTodo), [steadyUncompleted, search]);
  const filteredCompleted = useMemo(() => steadyCompleted.filter(filterTodo), [steadyCompleted, search]);
  // open todos under the project their agents work in (#2526)
  const projectGroups = useSteady(useMemo(() => groupTodosByProject(filteredUncompleted, lookup), [filteredUncompleted, lookup]), steadyGroups);
  // each section gets only its own rows' views, so one row's change re-renders that row's section alone
  const sectionViews = useSteady(
    useMemo((): ReadonlyMap<string, TodoRowView[]> => new Map(projectGroups.map((g) => [g.key, g.todos.map((td) => views.get(td.id)!)])), [projectGroups, views]),
    steadyLists,
  );

  // Active selected todo
  const activeTodo = useMemo(() => {
    if (activeId) {
      const found = todos.find((t) => t.id === activeId);
      if (found) return found;
    }
    return filteredUncompleted[0] ?? filteredCompleted[0];
  }, [activeId, todos, filteredUncompleted, filteredCompleted]);

  // Active agents of current selected todo
  const activeAgents = useMemo(() => {
    return (activeTodo?.agents ?? []).filter((a) => !a.released_at);
  }, [activeTodo]);

  const releasedAgents = useMemo(() => {
    return (activeTodo?.agents ?? []).filter((a) => a.released_at);
  }, [activeTodo]);

  // Narrow (< md): the list and a todo's detail take turns, as on a phone (iOS pushes TodoDetailView). The URL's ?id=
  // says a todo was opened; back goes to the list.
  const detailOnNarrow = Boolean(selectedId && todos.some((t) => t.id === selectedId));
  // The id a row click in this page pushed (/todos → /todos?id=…). Back from that detail pops the entry, as iOS pops
  // its NavigationStack, so the browser's Back does not return to the detail just left; a deep link replaces instead.
  const pushedId = useRef<string | undefined>(undefined);
  // stable across pushes, so the memoized rows keep the same props
  const openFromList = useCallback(
    (id: string) => {
      setActiveId(id);
      if (id === selectedId) return; // already this entry (a deep link stays a deep link)
      pushedId.current = id;
      navigate(`/todos?id=${encodeURIComponent(id)}`);
    },
    [selectedId],
  );
  const backToList = () => {
    if (selectedId && pushedId.current === selectedId) {
      pushedId.current = undefined;
      history.back();
    } else navigate("/todos", true);
  };

  const toggleDone = useCallback(async (todo: Todo) => {
    try {
      await updateTodo(todo.id, { done: !todo.done_at });
      await refreshTodos();
      await refresh();
      toast(todo.done_at ? t("todos.reopened") : t("todos.completed"), "ok");
    } catch (e) {
      toast(t("todos.action_failed", { error: errorText(e) }), "error");
    }
  }, []);

  const retryTodos = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await refreshTodos();
    } finally {
      setRetrying(false);
    }
  };

  const handleDelete = async () => {
    if (!activeTodo || deleting) return;
    const id = activeTodo.id;
    setDeleting(true);
    try {
      await deleteTodo(id);
    } catch (e) {
      toast(t("todos.delete_failed", { error: errorText(e) }), "error");
      return;
    } finally {
      setDeleting(false);
    }
    // The todo is gone on the hub: close and drop it now, not after refresh() (seconds on a busy hub), so it cannot
    // be deleted a second time (the hub answers 404 for it).
    dropTodo(id);
    setDeleteConfirm(false);
    setActiveId(undefined);
    pushedId.current = undefined;
    // replace: Back must not land on ?id=<the deleted todo>
    navigate("/todos", true);
    toast(t("todos.deleted"), "ok");
    // the rest of the page catches up in the background; a failed refresh leaves the list as dropped, the next push fixes it
    void refreshTodos().then(() => refresh()).catch(() => {});
  };

  return (
    <div className="flex h-full flex-col bg-surface">
      {/* Top Header */}
      <header className="flex h-13 shrink-0 items-center justify-between border-b border-line bg-surface px-4 sm:px-6">
        <div className="flex items-center gap-3">
          {onToggleSidebar && <IconButton icon="sidebar" label={t("agents-agentsview.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />}
          <div className="flex items-baseline gap-2">
            <h1 className="text-[16px] font-semibold text-ink">{t("todos.title")}</h1>
            {!(todos.length === 0 && todosError) && (
              <span className="text-[12px] text-ink-3">
                {t("todos.summary", { open: uncompleted.length, done: completed.length })}
              </span>
            )}
          </div>
        </div>
        <Button tone="secondary" icon="plus" onClick={() => setNewOpen(true)} className="h-8 text-[12.5px]">
          {t("todos.new_todo")}
        </Button>
      </header>

      {/* Main Split Layout: Left List & Right Detail */}
      <div className="flex min-h-0 flex-1">
        {/* Left Column: Todo List */}
        <div
          data-testid="todos-list-column"
          data-narrow-hidden={detailOnNarrow || undefined}
          // the edge parts the list from the detail beside it; on a phone the list is the whole page and has none
          className={cx("flex w-full shrink-0 flex-col border-line md:w-[380px] md:border-r lg:w-[400px]", detailOnNarrow && "max-md:hidden")}
        >
          {/* Search Bar */}
          <div className="p-3">
            <SearchField tone="inset" value={search} onChange={setSearch} placeholder={t("todos.search_placeholder")} />
          </div>

          {/* List Content */}
          <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-4">
            {!todosLoaded && todos.length === 0 ? (
              <div className="flex justify-center py-16" data-testid="todos-loading">
                <Loading />
              </div>
            ) : todos.length === 0 && todosError ? (
              <Empty
                icon="todo"
                title={t("todos.load_failed")}
                action={
                  <Button tone="secondary" icon="retry" busy={retrying} onClick={() => void retryTodos()}>
                    {t("todos.retry")}
                  </Button>
                }
              >
                {todosError}
              </Empty>
            ) : filteredUncompleted.length === 0 && filteredCompleted.length === 0 ? (
              search.trim() ? (
                <Empty icon="search" title={t("todos.no_match_title")}>
                  {t("todos.no_match_body")}
                </Empty>
              ) : (
                <Empty
                  icon="todo"
                  title={t("todos.empty_title")}
                  action={
                    <Button tone="accent" icon="plus" onClick={() => setNewOpen(true)}>
                      {t("todos.new_todo")}
                    </Button>
                  }
                >
                  {t("todos.empty_body")}
                </Empty>
              )
            ) : (
              <div className="flex flex-col gap-1">
                {projectGroups.map((g) => (
                  <TodoProjectSection
                    key={g.key}
                    group={g}
                    views={sectionViews.get(g.key)!}
                    selectedId={activeTodo?.id}
                    onSelect={openFromList}
                    onToggleDone={toggleDone}
                  />
                ))}

                {/* Completed Fold */}
                {filteredCompleted.length > 0 && (
                  <div className="mt-2 border-t border-line/60 pt-2">
                    <button
                      type="button"
                      onClick={() => setShowDone((v) => !v)}
                      className="flex h-8 w-full items-center justify-between rounded-control px-2.5 text-[12px] text-ink-3 transition-colors hover:bg-hover hover:text-ink-2"
                    >
                      <span>{t("todos.done_section", { count: filteredCompleted.length })}</span>
                      <Icon
                        name="chevron"
                        size={12}
                        className={cx("transition-transform duration-200", showDone ? "rotate-180" : "")}
                      />
                    </button>
                    {showDone && (
                      <div className="flex flex-col gap-1 pt-1">
                        {filteredCompleted.map((todo) => (
                          <TodoListItem
                            key={todo.id}
                            todo={todo}
                            view={views.get(todo.id)!}
                            selected={activeTodo?.id === todo.id}
                            onSelect={openFromList}
                            onToggleDone={toggleDone}
                          />
                        ))}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>

        {/* Right Column: Todo Detail */}
        <div
          data-testid="todos-detail-column"
          data-narrow-shown={detailOnNarrow || undefined}
          className={cx("min-h-0 flex-1 flex-col overflow-y-auto md:flex", detailOnNarrow ? "flex" : "hidden")}
        >
          {detailOnNarrow && (
            <div className="flex shrink-0 items-center px-2 pt-2 md:hidden" data-testid="todos-back">
              <Button tone="quiet" icon="chevronLeft" onClick={backToList}>
                {t("todos.back_to_list")}
              </Button>
            </div>
          )}
          {activeTodo ? (
            <TodoDetail
              key={activeTodo.id}
              todo={activeTodo}
              activeAgents={activeAgents}
              releasedAgents={releasedAgents}
              lookup={lookup}
              machines={machines}
              interactions={interactions}
              showReleased={showReleased}
              onToggleReleased={() => setShowReleased((v) => !v)}
              onToggleDone={() => toggleDone(activeTodo)}
              onOpenHandover={() => setHandoverOpen(true)}
              onDeleteClick={() => setDeleteConfirm(true)}
            />
          ) : (
            <div className="flex flex-1 items-center justify-center text-[13px] text-ink-3">
              {t("todos.pick_hint")}
            </div>
          )}
        </div>
      </div>

      {/* Dialogs */}
      <NewTodoDialog open={newOpen} onClose={() => setNewOpen(false)} onCreated={(id) => setActiveId(id)} />

      {activeTodo && (
        <HandoverDialog
          key={activeTodo.id}
          open={handoverOpen}
          todo={activeTodo}
          machines={machines}
          onClose={() => setHandoverOpen(false)}
        />
      )}

      {activeTodo && (
        <Dialog open={deleteConfirm} onClose={() => setDeleteConfirm(false)} title={t("todos.delete_title")}>
          <div className="py-2 text-[13.5px] text-ink-2">
            {t("todos.delete_confirm", { title: activeTodo.title })}
          </div>
          <div className="mt-4 flex justify-end gap-2">
            <Button tone="secondary" onClick={() => setDeleteConfirm(false)}>
              {t("common.cancel")}
            </Button>
            <Button tone="danger" busy={deleting} onClick={handleDelete}>
              {t("todos.delete")}
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

// ── Steady values ────────────────────────────────────────────

/** The last value while `steady` says the new one is the same, so memoized children see the same props. */
function useSteady<T>(value: T, steady: (prev: T, next: T) => T): T {
  const last = useRef(value);
  if (last.current !== value) last.current = steady(last.current, value);
  return last.current;
}

/** The same items in the same order. */
function sameItems<T>(a: readonly T[], b: readonly T[]): boolean {
  return a.length === b.length && a.every((x, i) => Object.is(x, b[i]));
}

const sameList = <T,>(prev: T[], next: T[]): T[] => (sameItems(prev, next) ? prev : next);

/** Two objects with the same fields, each the same value or object. */
function sameFields(a: object, b: object): boolean {
  const ka = Object.keys(a), kb = Object.keys(b);
  return ka.length === kb.length && ka.every((k) => Object.is((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]));
}

/** Each group that has the same title, state and todos stays the last one; so does the list when they all do. */
function steadyGroups(prev: TodoProjectGroup[], next: TodoProjectGroup[]): TodoProjectGroup[] {
  const byKey = new Map(prev.map((g) => [g.key, g]));
  const out = next.map((g) => {
    const old = byKey.get(g.key);
    return old && old.name === g.name && old.state === g.state && old.folded === g.folded && sameItems(old.todos, g.todos) ? old : g;
  });
  return sameItems(prev, out) ? prev : out;
}

/** Each list that has the same items stays the last one; so does the map when they all do. */
function steadyLists<T>(prev: ReadonlyMap<string, T[]>, next: ReadonlyMap<string, T[]>): ReadonlyMap<string, T[]> {
  let same = prev.size === next.size;
  const out = new Map<string, T[]>();
  for (const [k, list] of next) {
    const old = prev.get(k);
    const kept = old && sameItems(old, list) ? old : list;
    if (kept !== old) same = false;
    out.set(k, kept);
  }
  return same ? prev : out;
}

/** Each todo's view that has the same fields stays the last one; so does the map when they all do. */
function steadyViews(prev: ReadonlyMap<string, TodoRowView>, next: ReadonlyMap<string, TodoRowView>): ReadonlyMap<string, TodoRowView> {
  let same = prev.size === next.size;
  const out = new Map<string, TodoRowView>();
  for (const [id, v] of next) {
    const old = prev.get(id);
    const kept = old && sameFields(old, v) ? old : v;
    if (kept !== old) same = false;
    out.set(id, kept);
  }
  return same ? prev : out;
}

// ── Project Section ──────────────────────────────────────────

const TREE_TONE: Record<TreeState, string> = { needs_you: "text-attention", working: "text-orange", bg_waiting: "text-status-waiting", waiting: "text-ink-2", done: "text-ink-3" };

/** One project's open todos under a folding title with its Needs you / Working / Done (#2526). */
const TodoProjectSection = memo(function TodoProjectSection({
  group,
  views,
  selectedId,
  onSelect,
  onToggleDone,
}: {
  group: TodoProjectGroup;
  /** Its todos' row views, in the same order as group.todos. */
  views: readonly TodoRowView[];
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo) => void;
}) {
  const holds = group.todos.some((td) => td.id === selectedId);
  const [folded, toggle] = useFolded(group.key, group.folded, { follow: true, holds });
  return (
    <div className="mt-1 first:mt-0">
      <button type="button" aria-expanded={!folded} onClick={toggle} className="flex h-7 w-full min-w-0 items-center gap-1.5 rounded-control px-2 text-left text-[12px] text-ink-2 hover:bg-hover">
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        <Icon name="folder" size={12} stroke={2} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate font-semibold">{group.name}</span>
        <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{group.todos.length}</span>
        <span className={cx("shrink-0 text-[11px] font-semibold whitespace-nowrap", TREE_TONE[group.state])}>{treeStateLabel(group.state)}</span>
      </button>
      {!folded && (
        <div className="flex flex-col gap-1 pl-2 pt-0.5">
          {group.todos.map((todo, i) => (
            <TodoListItem
              key={todo.id}
              todo={todo}
              view={views[i]}
              selected={selectedId === todo.id}
              onSelect={onSelect}
              onToggleDone={onToggleDone}
            />
          ))}
        </div>
      )}
    </div>
  );
});

// ── Todo List Item ───────────────────────────────────────────

/** What a todo's row draws from its agents and their cards: plain values, so a push that leaves them alone leaves an
 *  equal view and the row skips its render. */
export type TodoRowView = {
  status: TodoStatus;
  dotState: AgentState;
  /** How long since it changed ("3 minutes ago"): here so a push that moves the text still redraws it, as before. */
  age: string;
  /** Its agents not released. */
  activeCount: number;
  /** Whether the most urgent agent's line shows (open, with a session, something to say). */
  urgentLine: boolean;
  /** That line's red lead ("<agent> Needs you<kicker>"), empty unless the agent waits on you. */
  urgentLead: string;
  /** What the agent asks (its card) or does / last said. */
  urgentText: string;
  /** One of its agents runs with ultracode on (#12): the Agent list row's mark after its title. */
  ultracode: boolean;
};

const DOT_STATE: Record<TodoStatus, AgentState> = {
  needs_you: "needs_you",
  interrupted: "needs_you",
  usage_limit: "idle",
  unread: "unread",
  working: "working",
  follow_up: "idle",
  done: "offline",
  not_started: "offline",
  failed: "offline",
};

/** The view of one todo's row (TodoRowView). */
export function todoRowView(todo: Todo, lookup: SessionLookup, interactions: Interaction[]): TodoRowView {
  const status = todoStatus(todo, lookup);
  const urgent = mostUrgentAgent(todo, lookup);
  const activeCount = (todo.agents ?? []).filter((a) => !a.released_at).length;
  const view: TodoRowView = { status, dotState: DOT_STATE[status], age: ago(todo.updated_at || todo.created_at), activeCount, urgentLine: false, urgentLead: "", urgentText: "", ultracode: todoUltracode(todo, lookup) };
  if (todo.done_at || !urgent?.session) return view;
  const limit = status === "usage_limit" ? usageLimitLabel(urgent.session) : undefined;
  if (limit) {
    view.urgentLine = true;
    view.urgentText = limit;
    return view;
  }
  const urgentNow = taskNow(urgent.session, stateOf(urgent.session) === "working");
  const urgentIx = interactions.find((i) => i.machine_id === urgent.agent.machine_id && i.session_id === urgent.agent.session_id);
  // what it waits on, from its card (#4111): the question, the command; not its last words
  const urgentAsk = urgent.status === "needs_you" ? askText(urgentIx) : "";
  if (!urgentAsk && !urgentNow) return view;
  const kicker = urgentIx?.kind === "plan" ? t("todos.kicker_plan") : urgentIx?.kind === "permission" ? t("todos.kicker_permission") : urgentIx?.kind === "screen" ? t("todos.kicker_screen") : urgentIx?.kind === "elicitation" ? t("todos.kicker_elicitation") : t("todos.kicker_question");
  view.urgentLine = true;
  if (urgent.status === "needs_you") view.urgentLead = t("todos.urgent_line", { name: urgent.agent.title || urgent.session.title || "Agent", kicker });
  view.urgentText = urgentAsk || (urgentNow ? (urgentNow.kind === "words" ? plainMarkdown(urgentNow.text) : urgentNow.text) : "");
  return view;
}

/** Every todo's row view by id. */
function todoRowViews(todos: Todo[], lookup: SessionLookup, interactions: Interaction[]): ReadonlyMap<string, TodoRowView> {
  return new Map(todos.map((td) => [td.id, todoRowView(td, lookup, interactions)]));
}

const TONE_CLASS = TODO_TONE_CLASS;

const TodoListItem = memo(function TodoListItem({
  todo,
  view,
  selected,
  onSelect,
  onToggleDone,
}: {
  todo: Todo;
  view: TodoRowView;
  selected: boolean;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo) => void;
}) {
  const { status, dotState, activeCount } = view;
  const toneClass = TONE_CLASS[TODO_STATUS_INFO[status].tone];
  const held = heldDelivery(todo);
  const thread = useStore((s) => todoThreadsOn(s.features)) ? boundThread(todo) : undefined;

  return (
    <div
      role="button"
      tabIndex={0}
      aria-current={selected || undefined}
      onClick={() => onSelect(todo.id)}
      // Enter / Space on the row itself (not on its own buttons) opens it, as a click does
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget || (e.key !== "Enter" && e.key !== " ")) return;
        e.preventDefault();
        onSelect(todo.id);
      }}
      className={cx(
        "group/item flex cursor-pointer gap-2.5 rounded-control p-2.5 transition-colors focus-visible:outline-2 focus-visible:outline-accent",
        selected ? "bg-accent-tint/70" : "hover:bg-hover",
      )}
    >
      {/* Checkbox button: touch-hit makes the 16px circle a 44px target on touch, its own (no row underneath) */}
      <button
        type="button"
        aria-label={todo.done_at ? t("todos.reopen_todo") : t("todos.complete_todo")}
        onClick={(e) => {
          e.stopPropagation();
          onToggleDone(todo);
        }}
        className={cx(
          "touch-hit mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border transition-colors",
          todo.done_at
            ? "border-accent bg-accent text-white"
            : "border-line-strong hover:border-accent hover:text-accent text-transparent",
        )}
      >
        <Icon name="check" size={10} stroke={2.6} />
      </button>

      {/* Body */}
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline justify-between gap-2">
          {view.ultracode ? (
            <span className="flex min-w-0 items-center gap-1">
              <span className="truncate text-[13.5px] font-semibold text-ink">{todo.title}</span>
              <UltraMark size={11} />
            </span>
          ) : (
            <span className={cx("truncate text-[13.5px] font-semibold text-ink", todo.done_at && "line-through text-ink-3 font-normal")}>
              {todo.title}
            </span>
          )}
          <span className="flex shrink-0 items-center gap-1.5 text-[11px] text-ink-3">
            {thread && (
              <span data-testid="todo-thread-mark" role="img" aria-label={t("todos.thread_mark")} title={threadLine(thread)} className="inline-flex">
                <Icon name="chat" size={12} stroke={2} />
              </span>
            )}
            {view.age}
          </span>
        </div>

        {/* Status Line */}
        <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[12px]">
          {!todo.done_at && <StatusMark state={dotState} size="sm" />}
          <span className={cx("shrink-0 whitespace-nowrap font-medium", toneClass)}>{todoStatusLabel(todo, status)}</span>
          {!todo.done_at && activeCount > 0 && (
            <>
              <span className="text-ink-3">·</span>
              <span className="text-ink-3">{t("todos.agent_count", { count: activeCount })}</span>
            </>
          )}
          {!todo.done_at && activeCount === 0 && (
            <span className="truncate text-ink-3">· {t("todos.not_handed")}</span>
          )}
          {!todo.done_at && <ReviewBadge todo={todo} className="ml-auto" />}
        </div>

        {/* Why the hub closed it */}
        {isAutoDone(todo) && todo.done_reason && (
          <div className="mt-1 truncate text-[11.5px] text-ink-3">{todo.done_reason}</div>
        )}

        {/* Where its finished work is held short of live */}
        {held && (
          <div className="mt-1 truncate text-[11.5px] text-ink-3">
            <span className="font-medium text-orange">{t(`todos.delivery_${held.state}`)}</span>
            {held.gate && <> · {t("todos.delivery_gate", { gate: held.gate })}</>}
            {held.unblock && ["user", "agent", "other"].includes(held.unblock) && <> · {t(`todos.unblock_${held.unblock}`)}</>}
          </div>
        )}

        {/* Most Urgent Agent Line */}
        {view.urgentLine && (
          <div className="mt-1 truncate text-[11.5px] text-ink-3">
            {view.urgentLead && <span className="font-medium text-attention">{view.urgentLead}</span>}
            {view.urgentText}
          </div>
        )}
      </div>
    </div>
  );
});

/** What a waiting agent's card asks, as one line of plain text; empty for a plan (its kicker says it) or no card. */
function askText(ix: Interaction | undefined): string {
  if (!ix || ix.kind === "plan") return "";
  return plainMarkdown(interactionAsk(ix)).replace(/\s+/g, " ").trim();
}

// ── Todo Detail ──────────────────────────────────────────────

function TodoDetail({
  todo,
  activeAgents,
  releasedAgents,
  lookup,
  machines,
  interactions,
  showReleased,
  onToggleReleased,
  onToggleDone,
  onOpenHandover,
  onDeleteClick,
}: {
  todo: Todo;
  activeAgents: TodoAgent[];
  releasedAgents: TodoAgent[];
  lookup: ReturnType<typeof makeSessionLookup>;
  machines: Machine[];
  interactions: Interaction[];
  showReleased: boolean;
  onToggleReleased: () => void;
  onToggleDone: () => void;
  onOpenHandover: () => void;
  onDeleteClick: () => void;
}) {
  const [title, setTitle] = useState(todo.title);
  const [notes, setNotes] = useState(todo.notes ?? "");
  const [savingTitle, setSavingTitle] = useState(false);
  const [savingNotes, setSavingNotes] = useState(false);
  const menu = useMenu();
  const allSessions = useAgentSessions();
  const spacesSupported = useStore((s) => s.spacesSupported);

  const hasLiveAgent = (mid: string, spaceName: string) => {
    return allSessions.some(
      (s) =>
        s.machine_id === mid &&
        (s.space_name || s.space_id) === spaceName &&
        stateOf(s) !== "offline" &&
        machines.some((m) => m.id === mid && m.online),
    );
  };

  const removeSpace = async (mid: string, space: string) => {
    try {
      await unlinkSpaceFromTodo(todo.id, mid, space);
      await refreshTodos();
      await refresh();
      toast(t("todos.space_removed"), "ok");
    } catch (e) {
      toast(t("todos.space_remove_failed", { error: errorText(e) }), "error");
    }
  };

  useEffect(() => {
    setTitle(todo.title);
    setNotes(todo.notes ?? "");
  }, [todo]);

  const saveTitle = async () => {
    if (!title.trim() || title.trim() === todo.title) {
      setTitle(todo.title);
      return;
    }
    setSavingTitle(true);
    try {
      await updateTodo(todo.id, { title: title.trim() });
      await refreshTodos();
    } catch (e) {
      toast(t("todos.save_title_failed", { error: errorText(e) }), "error");
    } finally {
      setSavingTitle(false);
    }
  };

  const saveNotes = async () => {
    if ((notes.trim() || undefined) === (todo.notes?.trim() || undefined)) return;
    setSavingNotes(true);
    try {
      await updateTodo(todo.id, { notes: notes.trim() });
      await refreshTodos();
    } catch (e) {
      toast(t("todos.save_notes_failed", { error: errorText(e) }), "error");
    } finally {
      setSavingNotes(false);
    }
  };

  // "Remove" for an agent that did not start, "Unlink" for the others (iOS: the row's swipe; Android: its ×).
  const unlinkAgent = async (agent: TodoAgent) => {
    const failed = Boolean(agent.start_error);
    try {
      await unlinkAgentFromTodo(todo.id, agent.machine_id, agent.session_id);
      await refreshTodos();
      await refresh();
      toast(failed ? t("todos.failed_agent_removed") : t("todos.agent_unlinked"), "ok");
    } catch (e) {
      toast(failed ? t("todos.remove_failed", { error: errorText(e) }) : t("todos.unlink_failed", { error: errorText(e) }), "error");
    }
  };

  const status = todoStatus(todo, lookup);
  const statusInfo = TODO_STATUS_INFO[status];
  const toneClass = {
    red: "bg-red-tint text-red",
    green: "bg-green-tint text-green",
    amber: status === "needs_you" ? "bg-field text-attention" : "bg-orange-tint text-orange",
    idle: "bg-hover text-ink-2",
    off: "bg-hover text-ink-3",
  }[statusInfo.tone];

  const dotState =
    status === "needs_you" || status === "interrupted"
      ? "needs_you"
      : status === "usage_limit"
        ? "idle"
      : status === "unread"
        ? "unread"
        : status === "working"
          ? "working"
          : status === "follow_up"
            ? "idle"
            : "offline";

  return (
    <div className="flex min-h-full flex-col p-6 sm:p-8">
      {/* Title Row with ⋯ menu */}
      <div className="flex items-center justify-between gap-3">
        <input
          type="text"
          value={title}
          disabled={savingTitle}
          onChange={(e) => setTitle(e.target.value)}
          onBlur={saveTitle}
          onKeyDown={(e) => {
            if (composing(e.nativeEvent)) return;
            if (e.key === "Enter") {
              e.currentTarget.blur();
            }
          }}
          placeholder={t("todos.title_placeholder")}
          className="min-w-0 flex-1 bg-transparent text-[22px] font-bold text-ink outline-none transition-colors focus:border-b focus:border-accent"
        />
        <IconButton icon="more" label={t("todos.more_options")} onClick={menu.open} active={Boolean(menu.anchor)} />
        {menu.anchor && (
          <Menu
            anchor={menu.anchor}
            onClose={menu.close}
            align="right"
            items={[
              {
                key: "delete",
                label: t("todos.delete_menu"),
                icon: "x" as const,
                danger: true,
                onSelect: onDeleteClick,
              },
            ]}
          />
        )}
      </div>

      {/* Meta Line */}
      <div className="mt-2.5 flex flex-wrap items-center gap-2.5 text-[12.5px] text-ink-2">
        <span className={cx("inline-flex h-5.5 items-center gap-1.5 rounded-md px-2 text-[12px] font-semibold", toneClass)}>
          <StatusMark state={dotState} size="sm" />
          {todoStatusLabel(todo, status)}
        </span>
        <span className="text-ink-3">·</span>
        <span>{t("todos.agent_count", { count: activeAgents.length })}</span>
        <span className="text-ink-3">·</span>
        <span>{t("todos.created_at", { time: clock(todo.created_at) })}</span>
      </div>
      {isAutoDone(todo) && (
        <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-3">
          {todo.done_reason ? t("todos.auto_done_reason", { reason: todo.done_reason }) : t("todos.auto_done")}
        </p>
      )}
      <ReviewChecklist todo={todo} />

      {/* Stage Bar (#2776): progress from plan to verified, from the hub's todo; nothing on hubs that don't send it. */}
      <TodoStageBar stage={todo.stage} prURL={todo.pr_url} className="mt-4" />

      {/* Notes / Description */}
      <div className="mt-4">
        <div className="relative rounded-control bg-inset p-3.5 shadow-hairline">
          <textarea
            rows={3}
            value={notes}
            disabled={savingNotes}
            onChange={(e) => setNotes(e.target.value)}
            onBlur={saveNotes}
            placeholder={t("todos.notes_placeholder")}
            className="w-full resize-none bg-transparent text-[13.5px] leading-[1.65] text-ink outline-none placeholder:text-ink-3"
          />
        </div>
      </div>

      <ThreadSection todo={todo} />

      <DecisionsSection todo={todo} lookup={lookup} />

      {/* Spaces Section */}
      {spacesSupported !== false && todo.spaces && todo.spaces.length > 0 && (
        <div className="mt-6">
          <div className="mb-2 text-[11.5px] font-medium tracking-[0.04em] text-ink-3 uppercase">
            {t("todos.spaces_section", { count: todo.spaces.length })}
          </div>
          <div className="flex flex-col gap-2">
            {todo.spaces.map((sp) => {
              const mName = machines.find((m) => m.id === sp.machine_id)?.name || sp.machine_id;
              const live = hasLiveAgent(sp.machine_id, sp.space);
              return (
                <div
                  key={`${sp.machine_id}/${sp.space}`}
                  className="flex items-center justify-between gap-3 rounded-control border border-line bg-canvas p-3"
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-[13px] font-semibold text-ink">
                      {sp.space} · {mName}
                    </span>
                    {!live && (
                      <span className="shrink-0 rounded bg-hover px-1.5 py-0.5 text-[10.5px] text-ink-3">
                        {t("todos.space_no_agent")}
                      </span>
                    )}
                  </div>
                  <Button
                    tone="quiet"
                    className="shrink-0 text-ink-3 hover:bg-red-tint/50 hover:text-red whitespace-nowrap px-2 py-1 text-[12px]"
                    onClick={() => void removeSpace(sp.machine_id, sp.space)}
                  >
                    {t("todos.space_remove")}
                  </Button>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* Current Agents */}
      <div className="mt-6 flex-1">
        <div className="mb-2 text-[11.5px] font-medium tracking-[0.04em] text-ink-3">
          {t("todos.agents_section", { count: activeAgents.length })}
        </div>

        {activeAgents.length === 0 ? (
          <div className="rounded-control border border-dashed border-line p-6 text-center text-[13px] text-ink-3">
            {t("todos.no_agents")}
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {activeAgents.map((agent) => {
              const session = lookup(agent.machine_id, agent.session_id);
              const st = session ? stateOf(session) : "offline";
              const now = taskNow(session, st === "working");
              const isExpired = !session && !agent.start_error;
              const displayTitle = session?.title || agent.title || t("lib-format.untitled_session");

              // computer · space › tab (the computer's name is its display name in machines)
              const mName = machines.find((m) => m.id === agent.machine_id)?.name || agent.machine_id;
              const spaceTab = session ? [session.space_name, tabLabel(session)].filter(Boolean).join(" › ") : "";
              const displaySub = spaceTab
                ? `${mName} · ${spaceTab}`
                : `${mName} · ${shortPath(session?.cwd || agent.cwd)}`;

              const ix = interactions.find((i) => i.machine_id === agent.machine_id && i.session_id === agent.session_id);
              const kicker = ix?.kind === "plan" ? t("todos.kicker_plan") : ix?.kind === "permission" ? t("todos.kicker_permission") : ix?.kind === "screen" ? t("todos.kicker_screen") : ix?.kind === "elicitation" ? t("todos.kicker_elicitation") : t("todos.kicker_question");
              const sourceLabel = formatAgentSource(agent, todo.spaces, lookup);

              return (
                <div
                  key={`${agent.machine_id}/${agent.session_id}`}
                  onClick={() => {
                    if (session) {
                      navigate({ name: "session", mid: agent.machine_id, sid: agent.session_id });
                    }
                  }}
                  className={cx(
                    "flex items-start gap-3 rounded-control border border-line bg-canvas p-3 transition-colors",
                    session ? "cursor-pointer hover:bg-hover" : "",
                  )}
                >
                  <div className="mt-1">
                    <StatusMark state={agent.start_error ? "offline" : isExpired ? "offline" : st} />
                  </div>
                  <div className="min-w-0 flex-1">
                    {/* The source tag leads the title line (as on iOS, #3150) and is never squeezed; a long one
                        ("Spawned by …") stops at 45% of the line, so the title keeps the rest and truncates (#3577). */}
                    <div className="flex min-w-0 items-center gap-2">
                      {sourceLabel && (
                        <span
                          data-testid="todo-agent-source"
                          title={sourceLabel}
                          className="max-w-[45%] shrink-0 truncate rounded bg-hover px-1.5 py-0.5 text-[10.5px] text-ink-3"
                        >
                          {sourceLabel}
                        </span>
                      )}
                      <span data-testid="todo-agent-title" title={displayTitle} className="min-w-0 flex-1 truncate text-[13.5px] font-semibold text-ink">
                        {displayTitle}
                      </span>
                      {isExpired && (
                        <span className="shrink-0 whitespace-nowrap rounded bg-hover px-1.5 py-0.5 text-[10.5px] text-ink-3">{t("todos.ended")}</span>
                      )}
                    </div>
                    <div className="mt-0.5 truncate text-[12px] text-ink-3">{displaySub}</div>

                    {/* Error / Last Text */}
                    {agent.start_error ? (
                      <div className="mt-1.5 flex items-center justify-between gap-2 rounded bg-red-tint/50 px-2 py-1 text-[12px] text-red">
                        <span className="truncate">{t("todos.start_failed", { error: agent.start_error })}</span>
                        <button
                          type="button"
                          onClick={(e) => {
                            e.stopPropagation();
                            void unlinkAgent(agent);
                          }}
                          className="shrink-0 font-medium hover:underline"
                        >
                          {t("todos.remove")}
                        </button>
                      </div>
                    ) : now || (st === "needs_you" && askText(ix)) ? (
                      <div className="mt-1 line-clamp-2 text-[12.5px] text-ink-2">
                        {st === "needs_you" && (
                          <span className="font-medium text-attention">{t("todos.kicker_line", { kicker })}</span>
                        )}
                        {(st === "needs_you" && askText(ix)) || (now && (now.kind === "words" ? plainMarkdown(now.text) : now.text))}
                      </div>
                    ) : null}
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <span data-testid="todo-agent-time" className="whitespace-nowrap text-[11px] text-ink-3">
                      {ago(session?.updated_at || agent.linked_at)}
                    </span>
                    {!agent.start_error && (
                      <IconButton
                        icon="x"
                        label={t("todos.unlink_agent", { name: displayTitle })}
                        size={24}
                        iconSize={12}
                        onClick={(e) => {
                          e.stopPropagation();
                          void unlinkAgent(agent);
                        }}
                      />
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        )}

        {/* Released Agents (Participated) */}
        {releasedAgents.length > 0 && (
          <div className="mt-4">
            <button
              type="button"
              onClick={onToggleReleased}
              className="flex items-center gap-1 text-[12px] text-ink-3 hover:text-ink-2"
            >
              <Icon
                name="chevron"
                size={11}
                className={cx("transition-transform duration-200", showReleased ? "rotate-180" : "")}
              />
              <span>{t("todos.released_section", { count: releasedAgents.length })}</span>
            </button>
            {showReleased && (
              <div className="mt-2 flex flex-col gap-1.5">
                {releasedAgents.map((agent) => {
                  const mName = machines.find((m) => m.id === agent.machine_id)?.name || agent.machine_id;
                  const displaySub = `${mName} · ${shortPath(agent.cwd)}`;
                  return (
                    <div
                      key={`${agent.machine_id}/${agent.session_id}`}
                      className="flex items-center justify-between rounded-control border border-line/60 bg-hover/40 px-3 py-2 text-[12px] text-ink-3"
                    >
                      <div className="min-w-0 flex-1">
                        <span className="truncate font-medium text-ink-2">{agent.title || agent.session_id}</span>
                        <div className="text-[11px] text-ink-3">{displaySub}</div>
                      </div>
                      <span className="shrink-0 text-[11px]">{t("todos.left_at", { time: ago(agent.released_at ?? undefined) })}</span>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        )}
      </div>

      {/* Bottom Action Bar */}
      <div className="mt-8 flex flex-wrap items-center gap-2.5 border-t border-line pt-4">
        <Button tone="accent" icon="hand" onClick={onOpenHandover} className="h-8.5 px-3.5 text-[13px]">
          {t("todos.hand_over")}
        </Button>
        <Button tone="secondary" icon="check" onClick={onToggleDone} className="h-8.5 px-3 text-[13px]">
          {todo.done_at ? t("todos.reopen") : t("todos.complete")}
        </Button>
      </div>
    </div>
  );
}

// ── Chat thread (#5279) ──────────────────────────────────────

/**
 * The chat thread the todo is bound to, with Unbind. Binding is not done here: it needs the bot's recent topics, which the
 * hub has no route for, so an unbound todo says how a binding comes about. A hub from before threads answers the unbind
 * with a route-level 404: the section then goes away without a word, as the decisions do. It shows only when the hub says it
 * has to-do threads (feature `todo_threads`): an older hub never sends a thread, so an "unbound" note there would be false.
 */
function ThreadSection({ todo }: { todo: Todo }) {
  const thread = boundThread(todo);
  const threadsOn = useStore((s) => todoThreadsOn(s.features));
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [unsupported, setUnsupported] = useState(false);

  useEffect(() => {
    setConfirm(false);
    setBusy(false);
  }, [todo.id]);

  const unbind = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await unbindTodoThread(todo.id);
      setConfirm(false);
      await refreshTodos();
      toast(t("todos.thread_unbound"), "ok");
    } catch (e) {
      if (isLegacySpacesRouteError(e)) {
        setConfirm(false);
        setUnsupported(true);
      } else {
        toast(t("todos.thread_unbind_failed", { error: errorText(e) }), "error");
      }
    } finally {
      setBusy(false);
    }
  };

  if (!threadsOn || unsupported) return null;
  return (
    <div className="mt-6" data-testid="todo-thread">
      <div className="mb-2 text-[11.5px] font-medium tracking-[0.04em] text-ink-3 uppercase">{t("todos.thread_section")}</div>
      {thread ? (
        <div className="flex items-center justify-between gap-3 rounded-control border border-line bg-canvas p-3">
          <div className="flex min-w-0 items-center gap-2 text-[13px] font-semibold text-ink">
            <span className="shrink-0 text-ink-3">
              <Icon name="chat" size={14} />
            </span>
            <span data-testid="todo-thread-line" title={threadLine(thread)} className="truncate">
              {threadLine(thread)}
            </span>
          </div>
          <Button
            tone="quiet"
            title={t("todos.thread_unbind_label", { name: threadName(thread) })}
            className="shrink-0 whitespace-nowrap px-2 py-1 text-[12px] text-ink-3 hover:bg-red-tint/50 hover:text-red"
            onClick={() => setConfirm(true)}
          >
            {t("todos.thread_unbind")}
          </Button>
        </div>
      ) : (
        <div className="rounded-control border border-dashed border-line p-4 text-[12.5px] leading-[1.6] text-ink-3">
          {t("todos.thread_empty")}
        </div>
      )}
      {thread && (
        <Dialog open={confirm} onClose={() => !busy && setConfirm(false)} title={t("todos.thread_unbind_title")}>
          <div className="py-2 text-[13.5px] text-ink-2">{t("todos.thread_unbind_confirm", { name: threadName(thread) })}</div>
          <div className="mt-4 flex justify-end gap-2">
            <Button tone="secondary" disabled={busy} onClick={() => setConfirm(false)}>
              {t("common.cancel")}
            </Button>
            <Button tone="danger" busy={busy} onClick={() => void unbind()}>
              {t("todos.thread_unbind")}
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

// ── Decisions (#1618) ────────────────────────────────────────

/**
 * Decisions: the calls made on this todo, in effect first-to-last; "Revise" writes a new one that replaces it, "full history" shows
 * the replaced ones struck through. Refetched whenever the todo changes (the hub bumps it when a decision is written,
 * also from an answered card). Hidden on a hub without decisions (404 on the route).
 */
function DecisionsSection({ todo, lookup }: { todo: Todo; lookup: ReturnType<typeof makeSessionLookup> }) {
  const [all, setAll] = useState<TodoDecision[] | null>(null);
  const [failed, setFailed] = useState("");
  const [unsupported, setUnsupported] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const [form, setForm] = useState<{ supersedes?: TodoDecision } | null>(null);
  const [linking, setLinking] = useState<TodoDecision | null>(null);
  const menu = useMenu();
  // Only the newest load lands: a slow earlier one must not put back an older list.
  const loads = useRef(0);

  const load = useCallback(async () => {
    const n = ++loads.current;
    try {
      const list = await fetchTodoDecisions(todo.id);
      if (n !== loads.current) return;
      setAll(list);
      setFailed("");
      setUnsupported(false);
    } catch (e) {
      if (n !== loads.current) return;
      if (isLegacySpacesRouteError(e)) setUnsupported(true); // a hub from before decisions: no route
      else setFailed(errorText(e));
    }
  }, [todo.id]);

  useEffect(() => {
    void load();
  }, [load, todo.updated_at]);

  useEffect(() => {
    setAll(null);
    setFailed("");
    setShowAll(false);
    setForm(null);
  }, [todo.id]);

  if (unsupported) return null;
  const effective = (all ?? []).filter((d) => !d.superseded_by);
  const replaced = (all ?? []).length - effective.length;
  const shown = showAll ? (all ?? []) : effective;
  const byId = new Map((all ?? []).map((d) => [d.id, d]));

  const markSupersedes = async (d: TodoDecision, old: string) => {
    try {
      await setDecisionSupersedes(todo.id, d.id, old);
      await load();
      toast(old ? t("todos.marked_superseded") : t("todos.unmarked_superseded"), "ok");
    } catch (e) {
      toast(t("todos.action_failed", { error: errorText(e) }), "error");
    }
  };

  return (
    <div className="mt-6">
      <div className="mb-2 flex items-center justify-between gap-3">
        <span className="text-[11.5px] font-medium tracking-[0.04em] text-ink-3 uppercase">{t("todos.decisions_section", { count: effective.length })}</span>
        <div className="flex shrink-0 items-center gap-1">
          {replaced > 0 && (
            <Button tone="quiet" className="whitespace-nowrap px-2 py-1 text-[12px]" onClick={() => setShowAll((v) => !v)}>
              {showAll ? t("todos.show_effective") : t("todos.show_history", { count: replaced })}
            </Button>
          )}
          <Button tone="quiet" icon="plus" className="whitespace-nowrap px-2 py-1 text-[12px]" onClick={() => setForm({})}>
            {t("todos.add_decision")}
          </Button>
        </div>
      </div>

      {failed && all === null ? (
        <div className="flex items-center justify-between gap-3 rounded-control border border-dashed border-line p-4 text-[12.5px] text-ink-3">
          <span className="min-w-0 truncate">{t("todos.decisions_failed", { error: failed })}</span>
          <Button tone="quiet" className="shrink-0 whitespace-nowrap px-2 py-1 text-[12px]" onClick={() => void load()}>
            {t("todos.retry")}
          </Button>
        </div>
      ) : all === null ? null : shown.length === 0 ? (
        <div className="rounded-control border border-dashed border-line p-4 text-[12.5px] leading-[1.6] text-ink-3">
          {t("todos.decisions_empty")}
        </div>
      ) : (
        <ol className="flex flex-col gap-2">
          {shown.map((d) => {
            const off = Boolean(d.superseded_by);
            const replaces = d.supersedes_id ? byId.get(d.supersedes_id) : undefined;
            const candidates = supersedeCandidates(d, all ?? []);
            return (
              <li
                key={d.id}
                className={cx("rounded-control border border-line p-3", off ? "bg-hover/40" : "bg-canvas")}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <div className={cx("text-[13.5px] font-semibold", off ? "text-ink-3 line-through" : "text-ink")}>{d.question}</div>
                    <div className={cx("mt-1 text-[13px] whitespace-pre-wrap", off ? "text-ink-3 line-through" : "text-ink")}>
                      <span className="text-ink-3">{t("todos.chose")}</span>
                      {d.choice}
                    </div>
                    {d.reason && (
                      <div className={cx("mt-0.5 text-[12.5px] whitespace-pre-wrap text-ink-2", off && "line-through")}>
                        <span className="text-ink-3">{t("todos.reason")}</span>
                        {d.reason}
                      </div>
                    )}
                    <div className="mt-1.5 flex flex-wrap items-center gap-x-1.5 text-[11.5px] text-ink-3">
                      <span>{clock(d.created_at)}</span>
                      <span>·</span>
                      <span>{decisionSourceLabel(d, todo, lookup)}</span>
                      {off && (
                        <>
                          <span>·</span>
                          <span>{t("todos.superseded")}</span>
                        </>
                      )}
                      {!off && replaces && (
                        <>
                          <span>·</span>
                          <span className="truncate">{t("todos.supersedes", { choice: replaces.choice })}</span>
                        </>
                      )}
                    </div>
                  </div>
                  {!off && (
                    <div className="flex shrink-0 items-center gap-0.5">
                      <Button tone="quiet" className="whitespace-nowrap px-2 py-1 text-[12px]" onClick={() => setForm({ supersedes: d })}>
                        {t("todos.supersede")}
                      </Button>
                      {(candidates.length > 0 || d.supersedes_id) && (
                        <IconButton
                          icon="more"
                          label={t("todos.more")}
                          size={26}
                          active={linking?.id === d.id && Boolean(menu.anchor)}
                          onClick={(e) => {
                            setLinking(d);
                            menu.open(e);
                          }}
                        />
                      )}
                    </div>
                  )}
                </div>
              </li>
            );
          })}
        </ol>
      )}

      {menu.anchor && linking && (
        <Menu
          anchor={menu.anchor}
          onClose={menu.close}
          align="right"
          width={260}
          items={[
            ...supersedeCandidates(linking, all ?? []).map((c) => ({
              key: c.id,
              label: t("todos.supersedes_pair", { question: c.question, choice: c.choice }),
              onSelect: () => void markSupersedes(linking, c.id),
            })),
            ...(linking.supersedes_id
              ? [{ key: "undo", label: t("todos.undo_supersede"), divider: true, onSelect: () => void markSupersedes(linking, "") }]
              : []),
          ]}
        />
      )}

      <DecisionDialog
        open={form !== null}
        todoId={todo.id}
        supersedes={form?.supersedes}
        onClose={() => setForm(null)}
        onSaved={load}
      />
    </div>
  );
}

function DecisionDialog({
  open,
  todoId,
  supersedes,
  onClose,
  onSaved,
}: {
  open: boolean;
  todoId: string;
  supersedes?: TodoDecision;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [question, setQuestion] = useState("");
  const [choice, setChoice] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setQuestion(supersedes?.question ?? "");
      setChoice("");
      setReason("");
    }
  }, [open, supersedes]);

  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!question.trim() || !choice.trim()) return;
    setBusy(true);
    try {
      await addTodoDecision(todoId, { question, choice, reason, supersedes_id: supersedes?.id });
      await onSaved();
      toast(supersedes ? t("todos.superseded_toast") : t("todos.recorded"), "ok");
      onClose();
    } catch (err) {
      toast(t("todos.save_failed", { error: errorText(err) }), "error");
    } finally {
      setBusy(false);
    }
  };

  const field = "w-full rounded-control border border-line bg-canvas px-3 py-1.5 text-[13.5px] text-ink outline-none focus:border-accent";
  return (
    <Dialog open={open} onClose={onClose} title={supersedes ? t("todos.supersede") : t("todos.decision_title")}>
      <form onSubmit={submit} className="flex flex-col gap-3.5">
        {supersedes && (
          <p className="text-[12.5px] leading-[1.6] text-ink-3">
            {t("todos.supersede_hint", { choice: supersedes.choice })}
          </p>
        )}
        <div>
          <label htmlFor="decision-question" className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.question")}</label>
          <input id="decision-question" type="text" required autoFocus={!supersedes} value={question}
            onChange={(e) => setQuestion(e.target.value)} placeholder={t("todos.question_placeholder")} className={field} />
        </div>
        <div>
          <label htmlFor="decision-choice" className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.choice")}</label>
          <input id="decision-choice" type="text" required autoFocus={Boolean(supersedes)} value={choice}
            onChange={(e) => setChoice(e.target.value)} placeholder={t("todos.choice_placeholder")} className={field} />
        </div>
        <div>
          <label htmlFor="decision-reason" className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.reason_optional")}</label>
          <textarea id="decision-reason" rows={2} value={reason} onChange={(e) => setReason(e.target.value)}
            placeholder={t("todos.reason_placeholder")} className={cx(field, "resize-y text-[13px]")} />
        </div>
        <div className="mt-2 flex justify-end gap-2">
          <Button tone="secondary" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="accent" type="submit" disabled={!question.trim() || !choice.trim() || busy} busy={busy}>
            {supersedes ? t("todos.supersede") : t("todos.record")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ── New Todo Dialog ──────────────────────────────────────────

function NewTodoDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const [title, setTitle] = useState("");
  const [notes, setNotes] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setTitle("");
      setNotes("");
    }
  }, [open]);

  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!title.trim()) return;
    setBusy(true);
    try {
      const todo = await createTodo(title, notes);
      await refreshTodos();
      await refresh();
      toast(t("todos.created"), "ok");
      onClose();
      onCreated(todo.id);
      navigate(`/todos?id=${encodeURIComponent(todo.id)}`);
    } catch (err) {
      toast(t("todos.create_failed", { error: errorText(err) }), "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title={t("todos.new_todo")}>
      <form onSubmit={submit} className="flex flex-col gap-3.5">
        <div>
          <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.title_label")}</label>
          <input
            type="text"
            required
            autoFocus
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={t("todos.new_title_placeholder")}
            className="w-full rounded-control border border-line bg-canvas px-3 py-1.5 text-[13.5px] text-ink outline-none focus:border-accent"
          />
        </div>

        <div>
          <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.notes_optional")}</label>
          <textarea
            rows={3}
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder={t("todos.new_notes_placeholder")}
            className="w-full resize-y rounded-control border border-line bg-canvas px-3 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
          />
        </div>

        <div className="mt-2 flex justify-end gap-2">
          <Button tone="secondary" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="accent" type="submit" disabled={!title.trim() || busy} busy={busy}>
            {t("todos.create")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ── Handover to Agent Dialog ─────────────────────────────────

export function HandoverDialog({
  open,
  todo,
  machines,
  onClose,
}: {
  open: boolean;
  todo: Todo;
  machines: Machine[];
  onClose: () => void;
}) {
  const [tab, setTab] = useState<"new" | "existing">("new");
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);

  // For "new":
  const onlineMachines = useMemo(() => machines.filter((m) => m.online), [machines]);
  const [mid, setMid] = useState(() => onlineMachines[0]?.id ?? "");
  const [cwd, setCwd] = useState("");
  const [launchers, setLaunchers] = useState<Launcher[]>([]);
  const [launcher, setLauncher] = useState("");

  // For "existing":
  const allSessions = useAgentSessions();
  const liveSessions = useMemo(() => {
    return allSessions.filter((s) => !s.assistant && stateOf(s) !== "offline");
  }, [allSessions]);
  const [selectedSid, setSelectedSid] = useState(() => liveSessions[0]?.session_id ?? "");

  // The form starts over when the dialog opens. While it is open the lists change with every push of a session or a
  // machine: what is typed stays, and the picked agent / computer changes only once it is gone from its list.
  const wasOpen = useRef(false);
  useEffect(() => {
    const opening = open && !wasOpen.current;
    wasOpen.current = open;
    if (!open) return;
    if (opening) setPrompt("");
    if (onlineMachines[0] && (!mid || !onlineMachines.some((m) => m.id === mid))) {
      setMid(onlineMachines[0].id);
    }
    const first = liveSessions[0]?.session_id;
    if (first) setSelectedSid((sid) => (opening || !liveSessions.some((s) => s.session_id === sid) ? first : sid));
  }, [open, onlineMachines, liveSessions]);

  useEffect(() => {
    if (!open || !mid) return;
    const controller = new AbortController();
    api<{ launchers: Launcher[] }>(`/machines/${enc(mid)}/launchers`, { signal: controller.signal })
      .then((r) => {
        setLaunchers(r.launchers ?? []);
        // the first this computer can start now, not one its terminal can't run (#1643)
        if (r.launchers?.length) setLauncher(initialLauncher(null, r.launchers));
      })
      .catch(() => {});
    return () => controller.abort();
  }, [open, mid]);

  const previewCard = useMemo(() => {
    return formatTodoContext(todo.title, todo.notes);
  }, [todo]);
  const pickedBlock = launcherBlock(launchers.find((l) => l.name === launcher));

  const submitNew = async () => {
    if (!mid) return;
    setBusy(true);
    try {
      const fullText = [previewCard, prompt.trim()].filter(Boolean).join("\n\n");
      launchAgent({
        machineId: mid,
        cwd: cwd.trim(),
        launcher,
        text: fullText,
        attachments: [],
        todoId: todo.id,
      });
      toast(t("todos.launched"), "ok");
      onClose();
    } catch (e) {
      toast(t("todos.launch_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  const submitExisting = async () => {
    const s = liveSessions.find((x) => x.session_id === selectedSid);
    if (!s) return;
    setBusy(true);
    try {
      const fullText = [previewCard, prompt.trim()].filter(Boolean).join("\n\n");
      const clientMsgId = uuid();
      await api(`/sessions/${enc(s.machine_id)}/${enc(s.session_id)}/messages`, {
        method: "POST",
        json: {
          text: fullText,
          client_msg_id: clientMsgId,
          todo_id: todo.id,
        },
      });
      await refreshTodos();
      await refresh();
      toast(t("todos.sent"), "ok");
      onClose();
      navigate({ name: "session", mid: s.machine_id, sid: s.session_id });
    } catch (e) {
      toast(t("todos.send_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title={t("todos.hand_over")} width={520}>
      <div className="flex flex-col gap-4">
        {/* Tab Switcher */}
        <div className="flex rounded-control bg-field p-0.5">
          <button
            type="button"
            onClick={() => setTab("new")}
            className={cx(
              "flex-1 rounded-[6px] py-1 text-[13px] font-medium transition-colors",
              tab === "new" ? "bg-surface text-ink shadow-hairline" : "text-ink-3 hover:text-ink-2",
            )}
          >
            {t("todos.tab_new")}
          </button>
          <button
            type="button"
            onClick={() => setTab("existing")}
            className={cx(
              "flex-1 rounded-[6px] py-1 text-[13px] font-medium transition-colors",
              tab === "existing" ? "bg-surface text-ink shadow-hairline" : "text-ink-3 hover:text-ink-2",
            )}
          >
            {t("todos.tab_existing")}
          </button>
        </div>

        {/* Tab 1: New Agent */}
        {tab === "new" && (
          <div className="flex flex-col gap-3">
            <div>
              <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.computer")}</label>
              <select
                value={mid}
                onChange={(e) => setMid(e.target.value)}
                className="w-full rounded-control border border-line bg-canvas px-2.5 py-1.5 text-[13px] text-ink outline-none"
              >
                {onlineMachines.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </select>
            </div>

            <div>
              <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.cwd_optional")}</label>
              <input
                type="text"
                placeholder={t("todos.cwd_placeholder")}
                value={cwd}
                onChange={(e) => setCwd(e.target.value)}
                className="w-full rounded-control border border-line bg-canvas px-2.5 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
              />
            </div>

            {launchers.length > 0 && (
              <div>
                <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.launcher")}</label>
                <select
                  value={launcher}
                  onChange={(e) => setLauncher(e.target.value)}
                  className="w-full rounded-control border border-line bg-canvas px-2.5 py-1.5 text-[13px] text-ink outline-none"
                >
                  {usableFirst(launchers).map((l) => {
                    const block = launcherBlock(l);
                    // still a choice (a start that works clears it), with why, after the ones that can start (#1643)
                    return (
                      <option key={l.name} value={l.name}>
                        {block ? `${l.name} · ${block.reason}` : l.name}
                      </option>
                    );
                  })}
                </select>
                {pickedBlock && (
                  <p data-testid="todo-handoff-blocked" className="mt-1.5 flex min-w-0 items-start gap-1.5 text-[11.5px] leading-snug text-ink-3">
                    <Icon name="warning" size={12} className="mt-px shrink-0 text-orange" />
                    <span className="min-w-0">{pickedBlock.reason}</span>
                  </p>
                )}
              </div>
            )}
          </div>
        )}

        {/* Tab 2: Existing Agent */}
        {tab === "existing" && (
          <div>
            <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.pick_agent")}</label>
            {liveSessions.length === 0 ? (
              <div className="py-6 text-center text-[12.5px] text-ink-3">{t("todos.no_live_agents")}</div>
            ) : (
              <div className="max-h-44 overflow-y-auto rounded-control border border-line p-1">
                {liveSessions.map((s) => {
                  const isSel = s.session_id === selectedSid;
                  const st = stateOf(s);
                  return (
                    <div
                      key={s.session_id}
                      onClick={() => setSelectedSid(s.session_id)}
                      className={cx(
                        "flex cursor-pointer items-center justify-between gap-2 rounded px-2.5 py-1.5 text-[12.5px] transition-colors",
                        isSel ? "bg-accent text-white" : "hover:bg-hover text-ink",
                      )}
                    >
                      <div className="flex min-w-0 items-center gap-2">
                        <StatusMark state={st} size="sm" />
                        <span className="truncate font-medium">{s.title || s.cwd || t("todos.unnamed")}</span>
                        <span className={cx("text-[11px]", isSel ? "text-white/80" : "text-ink-3")}>
                          {s.space_name || s.machine_id}
                        </span>
                      </div>
                      {isSel && <Icon name="check" size={13} stroke={2.5} />}
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        )}

        {/* Prompt Input */}
        <div>
          <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.prompt_label")}</label>
          <textarea
            rows={2}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            placeholder={t("todos.prompt_placeholder")}
            className="w-full resize-none rounded-control border border-line bg-canvas px-3 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
          />
        </div>

        {/* Context Preview Card */}
        <div>
          <label className="mb-1 block text-[11px] font-medium text-ink-3">{t("todos.preview")}</label>
          <div className="flex flex-col gap-1 rounded-control bg-field p-2.5 shadow-hairline">
            <div className="rounded border-l-2 border-accent bg-surface px-2.5 py-1.5 text-left shadow-hairline">
              <div className="text-[11.5px] font-medium text-ink-2">{t("todos.preview_todo", { title: todo.title })}</div>
              {todo.notes && <div className="mt-0.5 line-clamp-2 text-[12px] text-ink-3">{todo.notes}</div>}
            </div>
            {prompt.trim() && (
              <div className="mt-1 px-1 text-[13px] whitespace-pre-wrap text-ink">{prompt.trim()}</div>
            )}
          </div>
        </div>

        {/* Footer Buttons */}
        <div className="mt-2 flex justify-end gap-2">
          <Button tone="secondary" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {tab === "new" ? (
            <Button tone="accent" onClick={submitNew} disabled={!mid || busy} busy={busy}>
              {t("todos.launch_and_hand")}
            </Button>
          ) : (
            <Button
              tone="accent"
              onClick={submitExisting}
              disabled={!selectedSid || busy || liveSessions.length === 0}
              busy={busy}
            >
              {t("todos.send_and_hand")}
            </Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}
