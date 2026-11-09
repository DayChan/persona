import { removeStoredValue, storedValue, storeValue } from "../lib/compatibility";
// Beautiful UI's Sidebar Nav: workspace row, quick search, sections, one gliding highlight. Persona's main conversation
// is the first entry (the app opens on the assistant); a new conversation and a new agent are the "+" of their sections (#1239).
import { memo, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { NARROW, useMedia } from "../lib/viewport";
import { AGENT_FILTERS, agentFilter, applyFilter, arrangeSpaces, BOTMUX_HIDDEN_KEY, botmuxAndFilter, filterCounts, isBotmux, isSpaceMuted, matchingKeys, shownFilters, unreadOf, withoutBotmux, type AgentFilter, type FilterCounts } from "../lib/agent-filter";
import { AGENT_SORT_KEY, AGENT_SORTS, agentSort, machineTreeByUpdate, projectsByUpdate, repoMachinesByUpdate, todoTreeByUpdate, type AgentSort } from "../lib/agent-sort";
import { agentGrouping, AGENT_GROUPINGS, type AgentGrouping, awaitsReply, buildAgentGroups, buildSpaces, hostsOf, isQuiet, sessionHost, sessionSource, sourceName, splitAgentSessions, type AgentGroup, type AgentMutes, type Space } from "../lib/agent-groups";
import { buildRepoMachineTree, showsWorktreeLevel, type RepoMachine, type RepoTree, type RepoWorktree } from "../lib/agent-repos";
import { keyedRefs } from "../lib/keyed-refs";
import { buildMachineTree, paneRoute, resolveTab, tabRowKeys, type ChildStates, type HerdrMachine, type HerdrSpace, type HerdrTab } from "../lib/herdr-tree";
import { addSection, arrangeSessions, assignSection, GROUP_LABEL, isDefaultView, providerLabel, refreshAgentSessions, removeSection, resetView, setView, SHOW_LABEL, SORT_LABEL, toggleProvider, togglePin, totalUnread, updateAgentPrefs, useAgentList, useAgentPrefs, viewOf, type AgentPrefs, type AgentSession, type GroupBy, type ShowMode, type SortBy } from "../lib/agents";
import { useInboxBadge } from "../lib/inbox";
import { useLarkHome } from "../lib/lark-chats";
import { machineWithHub } from "../lib/mesh";
import { agentLabel } from "../lib/mentions";
import { ago, backgroundWaiting, baseName, sessionCan, sessionPreview, sourceSubtitle, sessionTitle, stateOf, tabLabel } from "../lib/format";
import { agentKey, isSpacePinned, pinnedSessions } from "../lib/pins";
import { NAV_PAGES, firstAndCurrent, extensionNavKey, sidebarActiveKey } from "../lib/persona-nav";
import { arrangePages, useSidebarPrefs } from "../lib/sidebar-pages";
import { sidebarConversations, usePinnedConversations } from "../lib/conversation-pins";
import { autoConversations, autoOrigin, isAutoConversation, originLabel, type AutoOrigin, type OriginTally } from "../lib/conversation-origins";
import { ConversationRowMenu, pinnedSectionItems } from "./ConversationMenu";
import { useDisconnect } from "./DisconnectBrowser";
import { useExtensionSlot } from "../lib/extension-slots";
import { toneOf } from "../lib/extension-ui";
import { BADGE_TONE, navIcon, pageRoute } from "./extensions/ExtensionSlots";
import { href, navigate, routePath, useRoute, type Route } from "../lib/router";
import { useAgentSessions, useCounts, useFullAgentSessions } from "../lib/sessions";
import { terminalTarget } from "../lib/session-view";
import { refresh, rememberSessionRow, useLarkOn, useStore } from "../lib/store";
import { draftLine, rowLine } from "../lib/row-line";
import { usageLimitLabel } from "../lib/usage-limit";
import { useLeftDraft } from "../lib/drafts";
import { planInRow, planLabel, planLine } from "../lib/plan";
import { answeredSession, answerPending, lastAnsweredAt } from "./assistant/answered-lookup";
import { conversationTitle } from "./assistant/conversations";
import { BREATHING, connPhase, connStatus, phaseTone, type ConnPhase } from "../lib/connection";
import { useWorkspaceBoardSetting } from "../lib/workspace-board-setting";
import { buildTodoTree, treeStateLabel, type TodoTree, type TreeAgent, type TreeState, type TreeTodo } from "../lib/todo-tree";
import { findTodoForSpace, openTodoFromTag, TODO_STATUS_INFO, type TodoStatus } from "../lib/todos";
import type { AgentState, Machine, Session } from "../lib/types";
import { Collapse, cx, EASE, Kbd, Menu, Spinner, useMenu, useNow, type MenuItem } from "../ui/atoms";
import { StatusMark, StateTally } from "../ui/StatusMark";
import { foldedCounts, hasFoldedCounts } from "../lib/folded-counts";
import { runningWorkflows, workflowLine, workflowShortLine } from "../lib/workflows";
import { ultracodeMarkShown } from "../lib/ultracode";
import { Rolling } from "../ui/Rolling";
import { AppMark } from "../ui/AppMark";
import { BrandMark } from "../ui/BrandMark";
import { Icon, type IconName } from "../ui/icons";
import { HerdrNotices } from "./agents/HerdrNotices";
import { HubNoticeLine } from "./agents/HubNoticeRow";
import { ArchiveCloseDialog, CloseAgentDialog, DeleteSessionDialog, RenameDialog, RenameSpaceDialog } from "./chat/SessionDialogs";
import { AGENT_ARCHIVES_FEATURE } from "../lib/agent-archives";
import { UltraMark } from "./chat/Ultracode";
import { AgentRowMenu, MarkSeenDialog, SpaceMenu } from "./AgentMenus";
import { NodeMenu } from "./NodeMenu";
import { NodePrompt } from "./NodePrompt";
import { SidebarUsage } from "./UsageBars";
import "../lib/node-palette";
import { machineNode, termPaneNode, termSpaceNode, termTabNode, type MenuNode } from "../lib/node-actions";
import { contextMenuProps } from "../ui/context-menu";
import type { MenuAt } from "../ui/menu-model";
import { useSidebarTerminals } from "../lib/sidebar-terminals";
import { rowDetail, useAgentDetails, useContextAsUsed } from "../lib/context-usage";
import { usePanes } from "../lib/terminal-panes";
import { buildTerminalTree, shellsOnly, type TermSpace } from "../lib/terminals";
import { setTheme, useTheme, type Theme } from "./theme";
import { SessionMark } from "../ui/AgentMark";
import { AgentAvatar } from "./agents/AgentAvatar";
import { LinkTodoDialog, type LinkTargetSpace } from "./LinkTodoDialog";
import { hubMenuItems, useHubs } from "./Hubs";
import { FoldScope, useFolded } from "../lib/use-folded";
import { clipsOverflow, glideBox, glideStyle, sameBox, type GlideBox, type Span } from "../lib/glide";
import { AGENTS_FOLD, onGrouping, setAgentsFolded } from "../lib/sidebar-api";
import { ariaKeyshortcuts, formatChord, jumpHint, useBindings } from "../lib/shortcuts";
import { toggleSidebarColumn } from "../lib/layout";
import { markAllSeenLabel, stateCount } from "../lib/state-labels";
import { PagedSessionList } from "./agents/PagedSessionList";
import { SESSION_LIST_STYLE } from "../ui/list-metrics";
import { runtimeIdentity, runtimeOrder, runtimeRemoval, runtimeSessions } from "../lib/runtime-list";
import { heldSeen } from "../lib/seen-outbox";
import { SESSIONS_LIST_FEATURE } from "../lib/session-list";
import { personalHideDeps, personalOrder, personalSessions, removeAgentSession, useGoingIds, type HideDeps } from "../lib/agents";
import { NoComputers, useAddComputer } from "./machines/AddComputerEntry";
import { neverConnected } from "../lib/add-computer";

/** The open row's own fill (#2283): it stays while the pointer is elsewhere in the list, a step darker than hover. */
const CURRENT_ROW = "bg-hover-2";
/** The least an agent row's title gets behind a prefix (styles.css `--row-title-min`, about 4.5 characters). */
const ROW_TITLE_MIN = "min-w-(--row-title-min)";

/** Brings `el` to the middle of its nearest scrolling ancestor when it is not fully in view (rows loading above it
 *  later still leave it on screen). Not `scrollIntoView`: that also scrolls the clipped (overflow-hidden) box of a
 *  Collapse that is still opening, and it stays scrolled. */
function reveal(el: HTMLElement): boolean {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const o = getComputedStyle(p).overflowY;
    if (o !== "auto" && o !== "scroll") continue;
    const r = el.getBoundingClientRect(),
      b = p.getBoundingClientRect();
    // a row under the pinned Persona row (#5581) is not in view; that row itself always is
    const pinned = p.querySelector<HTMLElement>(":scope > [data-sidebar-sticky]");
    const top = pinned && !pinned.contains(el) ? pinned.getBoundingClientRect().bottom : b.top;
    if (r.top >= top && r.bottom <= b.bottom) return false;
    p.scrollTop += r.top + r.height / 2 - (b.top + b.height / 2);
    return true;
  }
  return false;
}

/** Whether a scrolling list is scrolled down from its top (rows have gone under its pinned head). */
function useScrolled(ref: React.RefObject<HTMLElement | null>): boolean {
  const [scrolled, setScrolled] = useState(false);
  useEffect(() => {
    const n = ref.current;
    if (!n) return;
    const read = () => setScrolled(n.scrollTop > 0);
    read();
    n.addEventListener("scroll", read, { passive: true });
    return () => n.removeEventListener("scroll", read);
  }, [ref]);
  return scrolled;
}

/** The gliding highlight under a list of rows (Beautiful UI's Sidebar Nav): rows register under their keys, and the
 *  one under the pointer gets the box; the open row has its own fill (CURRENT_ROW), so the box doesn't take it away.
 *  The list is the box's scroll container (`nav`). The open row is kept in view from when it opens until the list is
 *  touched (wheel, pointer, keys): it may come later, and rows loading above it push it down.
 *  The box is measured after each render and again whenever the list's layout moves without one (#2476): a group
 *  folding or opening (its Collapse animates, then ends), rows arriving or changing height, scrolling. It covers only
 *  the part of its row on show and goes away when the row is folded away or gone (lib/glide.ts).
 *  The row under the pointer is kept in a ref and the box in a small store only GlideHighlight reads: hovering
 *  re-measures and moves the box without re-rendering the list that holds the rows. */
function useGlide(activeKey: string) {
  const nav = useRef<HTMLDivElement>(null);
  const rows = useRef(new Map<string, HTMLElement>());
  const lastKey = useRef<string | null>(null);
  const following = useRef(false);
  const hovered = useRef<string | null>(null);
  const [box] = useState(glideStore);
  const active = useRef(activeKey);
  active.current = activeKey;
  const sizes = useRef<ResizeObserver | null>(null);
  const measure = useRef(() => {});
  measure.current = () => {
    const n = nav.current;
    // the box rests under the open row when the pointer is away, so it glides from there; that row's fill covers it
    const el = rows.current.get(hovered.current ?? active.current);
    let next: GlideBox | null = null;
    if (n && el?.isConnected) {
      const clips: Span[] = [];
      for (let p = el.parentElement; p && p !== n; p = p.parentElement) if (clipsOverflow(getComputedStyle(p).overflowY)) clips.push(p.getBoundingClientRect());
      next = glideBox(el.getBoundingClientRect(), clips, n.getBoundingClientRect(), n.scrollTop);
    }
    // only a real move updates
    box.set((b) => (next ? (b.on && sameBox(b.at, next) ? b : { at: next, on: true }) : b.on ? { at: b.at, on: false } : b));
  };
  useEffect(() => {
    const n = nav.current;
    if (!n) return;
    const stop = () => (following.current = false);
    const events = ["wheel", "touchstart", "pointerdown", "keydown"] as const;
    for (const e of events) n.addEventListener(e, stop, { passive: true });
    // layout moves the list doesn't re-render for, measured once per frame however many come in it
    let frame = 0;
    const soon = () => {
      if (!frame) frame = requestAnimationFrame(() => ((frame = 0), measure.current()));
    };
    const ro = new ResizeObserver(soon);
    sizes.current = ro;
    // the list and its blocks (sections) change height as a group inside folds or opens, and as rows come and go;
    // the rows themselves are observed as they register (one growing moves the rest)
    const watch = () => {
      ro.observe(n);
      for (const c of n.children) ro.observe(c);
      for (const el of rows.current.values()) ro.observe(el);
    };
    watch();
    const mo = new MutationObserver(() => (ro.disconnect(), watch(), soon()));
    mo.observe(n, { childList: true });
    // a Collapse's transition ending (it may end without a size change, e.g. its opacity) and scrolling inside the list
    n.addEventListener("transitionend", soon);
    n.addEventListener("scroll", soon, { passive: true, capture: true });
    return () => {
      events.forEach((e) => n.removeEventListener(e, stop));
      n.removeEventListener("transitionend", soon);
      n.removeEventListener("scroll", soon, { capture: true });
      mo.disconnect();
      ro.disconnect();
      sizes.current = null;
      cancelAnimationFrame(frame);
    };
  }, []);
  useLayoutEffect(() => {
    if (lastKey.current !== activeKey) {
      lastKey.current = activeKey;
      following.current = true;
    }
    const open = following.current ? rows.current.get(activeKey) : undefined;
    // a group that opened for it is still growing, and the list can't scroll past its end yet: again once it has
    if (open?.offsetHeight && reveal(open)) setTimeout(() => following.current && open.isConnected && reveal(open), 350);
    // runs after every render (rows fold, lists change)
    measure.current();
  });
  // keyedRefs keeps a key's entry for the element that holds it (a folded tab's row and its hidden pane row may share a
  // key, #2320); on top, each row's size is watched while it is registered, so a row growing or folding away moves the
  // glide (#2476)
  const [reg] = useState<RegFn>(() => {
    const keyed = keyedRefs(rows.current);
    return (k: string) => {
      const set = keyed(k);
      let mine: HTMLElement | null = null;
      return (el: HTMLElement | null) => {
        if (mine && mine !== el) sizes.current?.unobserve(mine);
        set(el);
        mine = el;
        if (el) sizes.current?.observe(el);
      };
    };
  });
  // one function for the list's life: it moves the box straight away (as a render's layout effect did) and changes
  // nothing the rows or the list render from
  const [hover] = useState(() => (k: string | null) => {
    hovered.current = k;
    measure.current();
  });
  const highlight = <GlideHighlight box={box} />;
  return { nav, reg, hover, highlight };
}

/** Where the box is: `at` is the last place it was on show; a box that goes away (`on` false) fades out there, and
 *  glides from there back. */
type GlideState = { at: GlideBox | null; on: boolean };

/** The box's place outside React state, so moving it re-renders only GlideHighlight. */
function glideStore() {
  let state: GlideState = { at: null, on: false };
  const subs = new Set<() => void>();
  return {
    get: () => state,
    set(f: (b: GlideState) => GlideState) {
      const next = f(state);
      if (next === state) return;
      state = next;
      subs.forEach((s) => s());
    },
    subscribe(s: () => void) {
      subs.add(s);
      return () => void subs.delete(s);
    },
  };
}

function GlideHighlight({ box }: { box: ReturnType<typeof glideStore> }) {
  // box.get is the server snapshot too: the shell views are also rendered to a string (shell-views.test)
  const { at, on } = useSyncExternalStore(box.subscribe, box.get, box.get);
  return <span aria-hidden data-glide className="pointer-events-none absolute inset-x-2.5 rounded-[7px] bg-hover" style={glideStyle(at, on, EASE)} />;
}

/** The row a route lights up in the sidebar: a tab's page is the pane it shows (its row in the tree is the tab's). */
function useActiveKey(route: Route): string {
  const all = useAgentSessions();
  const assistant = useStore((s) => s.assistant);
  const tabPath = route.name === "tab" ? href(route) : "";
  // eslint-disable-next-line react-hooks/exhaustive-deps -- `route` is parsed anew each render; its path says it all
  const tabPane = useMemo(() => (route.name === "tab" ? resolveTab(all, route) : undefined), [all, tabPath]);
  return route.name === "tab" ? (tabPane ? rowKey(tabPane) : "") : sidebarActiveKey(route, assistant?.main);
}

// One function for the component's life, so the rows given it skip a re-render (memo); a click still calls the
// latest onNavigate (the phone's drawer closing).
function useGo(onNavigate?: () => void): GoFn {
  const latest = useRef(onNavigate);
  useLayoutEffect(() => {
    latest.current = onNavigate;
  });
  return useCallback(
    (to: Route) => (e: React.MouseEvent) => {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
      e.preventDefault();
      navigate(to);
      latest.current?.();
    },
    [],
  );
}

/** One entry of the pages under Persona ("Overview", "Inbox" … "Settings", and the extensions' pages). */
export type NavPage = { k: string; to: Route; icon: IconName; label: string; short?: string; badge?: number | string; badgeTone?: keyof typeof BADGE_CLASS; hint?: string; title?: string };

/** The sidebar's pages ("Overview" … "Settings", the extensions' pages) and the connection, shared by the sidebar and its rail. */
export function useNavPages() {
  const phase = connPhase(useStore((s) => s.conn), useStore((s) => s.link));
  const machines = useStore((s) => s.machines);
  // "Terminal" (docs/integrations/terminal.md "Standalone terminals") is there where the hub can list panes
  const terminalPanes = useStore((s) => s.features.includes("terminal_panes"));
  const assistant = useStore((s) => s.assistant);
  const todos = useStore((s) => s.todos);
  const todosSupported = useStore((s) => s.todosSupported);
  // Quiet tasks (#1731) are told about in the main conversation, not counted here.
  const uncompletedTodosCount = useMemo(() => todos.filter((todo) => !todo.done_at && !todo.quiet).length, [todos]);
  const counts = useCounts();
  const inbox = useInboxBadge();
  const larkSwitch = useLarkHome();
  const lark = useLarkOn();
  // "Messages" needs the hub to read Feishu (feature `lark`) as well as this browser's switch
  const larkHome = larkSwitch && lark;
  const extensionPages = useExtensionSlot("nav_page");
  // off unless turned on in Settings › General › Agent (#2475)
  const boardOn = useWorkspaceBoardSetting().enabled === true;

  const online = machines.filter((m) => m.online).length;
  const main = assistant?.main ? assistant.conversations.find((c) => c.session_id === assistant.main) : undefined;
  const page: Record<(typeof NAV_PAGES)[number], NavPage | false> = {
    home: { k: "home", to: { name: "home" }, icon: "home", label: t("sidebar.nav_home"), short: t("sidebar.nav_home_short"), badge: counts.badge || undefined, badgeTone: "attention" },
    inbox: { k: "inbox", to: { name: "inbox" }, icon: "inbox", label: t("sidebar.nav_inbox"), short: t("sidebar.nav_inbox_short"), badge: inbox || undefined, badgeTone: "attention", title: inbox ? t("sidebar.inbox_badge_title", { count: inbox }) : undefined },
    health: { k: "health", to: { name: "health" }, icon: "heart", label: t("sidebar.nav_health"), short: t("sidebar.nav_health_short") },
    messages: larkHome && { k: "messages", to: { name: "messages" }, icon: "chat", label: t("sidebar.nav_messages") },
    "workspace-board": boardOn && { k: "workspace-board", to: { name: "workspace-board" }, icon: "layers", label: t("sidebar.nav_board"), short: t("sidebar.nav_board_short") },
    todos: todosSupported !== false && { k: "todos", to: { name: "todos" }, icon: "todo", label: t("sidebar.nav_todos"), badge: uncompletedTodosCount || undefined },
    routines: { k: "routines", to: { name: "routines" }, icon: "clock", label: t("sidebar.nav_routines"), short: t("sidebar.nav_routines_short") },
    kb: { k: "kb", to: { name: "kb" }, icon: "book", label: t("sidebar.nav_kb"), short: t("sidebar.nav_kb_short") },
    machines: { k: "machines", to: { name: "machines" }, icon: "machine", label: t("sidebar.nav_machines"), short: t("sidebar.nav_machines_short"), hint: machines.length ? `${online}/${machines.length}` : undefined },
    terminal: terminalPanes && { k: "terminal", to: { name: "terminal" }, icon: "terminal", label: t("sidebar.nav_terminal") },
    settings: { k: "settings", to: { name: "settings" }, icon: "gear", label: t("sidebar.nav_settings") },
  };
  const all = NAV_PAGES.flatMap((k): NavPage[] => [
    // the extensions' pages (docs/extensions/extensions.md "UI slots" nav_page): one entry each, right before "Settings"
    ...(k === "settings"
      ? extensionPages.map((u) => {
          const key = extensionNavKey(u.extension, u.id);
          return { k: key, to: pageRoute(u.extension, u.id), icon: navIcon(u.icon), label: u.title, badge: u.badge?.text || undefined, badgeTone: BADGE_TONE[toneOf(u.badge?.tone)], title: u.extension_name ? `${u.title} · ${u.extension_name}` : u.title };
        })
      : []),
    ...(page[k] ? [page[k]] : []),
  ]);

  // as arranged in Settings › General › Sidebar pages: the list, and the pages that wait under More (docs/web/web.md "The sidebar's pages")
  const { shown: pages, more } = arrangePages(all, useSidebarPrefs());

  return { phase, online, total: machines.length, main, pages, more, all };
}

export function Sidebar({ route, onSearch, onNewAgent, onNavigate }: { route: Route; onSearch: () => void; onNewAgent: () => void; onNavigate?: () => void }) {
  const bindings = useBindings();
  const searchHint = jumpHint(bindings);
  const assistant = useStore((s) => s.assistant);
  const { phase, online, total, main: mainConversation, pages, more } = useNavPages();
  // a phone's drawer has the pages as two rows of icons, so its first screen reaches the Agents (#1996)
  const narrow = useMedia(NARROW);
  const activeKey = useActiveKey(route);
  const pinnedIds = usePinnedConversations();
  const { pinned: pinnedConvs, recent } = sidebarConversations(assistant?.conversations ?? [], assistant?.main, pinnedIds, 5, activeKey.startsWith("j:") ? activeKey.slice(2) : undefined);
  const convs = [...pinnedConvs, ...recent];
  // the main row sits above the pages, so"Persona chats"waits for the assistant (a hub without it has none)
  const chats = !!assistant;
  // a conversation row's ⋯ / right click (iOS: its long press), and the section title's Unpin all
  const [convMenu, setConvMenu] = useState<{ s: Session; anchor: HTMLElement; at?: MenuAt } | null>(null);
  // one function for the sidebar's life: the conversation rows and the main row keep their memo across pushes
  const onConvMenu = useCallback<RowMenuFn>((s, anchor, at) => setConvMenu((m) => (!at && m?.anchor === anchor ? null : { s, anchor, at })), []);
  const [chatsMenu, setChatsMenu] = useState<MenuAt | null>(null);
  const autoHolds = activeKey.startsWith("auto:") || isOpenAuto(assistant?.conversations, activeKey, convs);
  const [personaFolded, togglePersona] = useFolded("persona", false, { holds: activeKey === "history" || autoHolds || convs.some((c) => activeKey === `j:${c.session_id}`) });
  // the pages fold under their own title (#2524); the drawer's grid of icons doesn't
  const [pagesFolded, togglePages] = useFolded("pages", false, { all: false, holds: more.some((p) => p.k === activeKey) });
  const { nav, reg, hover, highlight } = useGlide(activeKey);
  const go = useGo(onNavigate);
  const scrolled = useScrolled(nav);

  return (
    <aside className="flex h-full w-full flex-col bg-canvas">
      <div className="px-2.5 pt-2.5">
        <div className="mb-2 flex items-center gap-1">
          <div className="min-w-0 flex-1"><AccountRow phase={phase} online={online} total={total} /></div>
          {/* folds the column to the rail of icons (a wide screen; the drawer closes by its scrim) */}
          {!narrow && <CollapseButton collapsed={false} chord={bindings["left-sidebar"]} />}
        </div>
        <button type="button" onClick={onSearch} className="touch-hit mb-1 flex h-8 w-full items-center gap-2 rounded-control bg-inset px-2.5 text-left shadow-hairline transition-colors hover:bg-hover">
          <Icon name="search" size={12} stroke={2} className="text-ink-3" />
          <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-3">{t("sidebar.search_sessions")}</span>
          <span className="hidden sm:inline-flex"><Kbd>{searchHint}</Kbd></span>
        </button>
        {chats && (
          <a href={href({ name: "persona-new" })} onClick={go({ name: "persona-new" })} aria-current={route.name === "persona-new" ? "page" : undefined} className={cx("touch-hit mb-1 flex h-8 w-full items-center gap-2 rounded-control px-2.5 text-[12.5px] font-medium text-ink-2 transition-colors hover:text-ink", route.name === "persona-new" ? CURRENT_ROW : "hover:bg-hover")}>
            <Icon name="compose" size={13} stroke={1.8} />
            <span className="min-w-0 flex-1 truncate">{t("sidebar.new_chat")}</span>
          </a>
        )}
      </div>

      {/* scroll-pt: a row reached by Tab doesn't stop under the pinned Persona row — about 62px with its 主对话 line
          (4 + 48 + 1 gap + 1 line + 8 margins), 58px on a coarse pointer (44px touch row): 64px covers both */}
      <div ref={nav} data-sidebar-rows onMouseLeave={() => hover(null)} className="relative min-h-0 flex-1 scroll-pt-16 overflow-y-auto overscroll-contain px-2.5 pb-3">
        {highlight}
        {/* the main conversation is the column's first row, Persona with the app's mark, and only there (#2004, #5109); it
            stays at the top while the rest scrolls (#5581), its line and a fog under it once rows go beneath */}
        <div data-sidebar-sticky className="sticky top-0 z-30 flex flex-col gap-px bg-canvas pt-1">
          <PersonaRow reg={reg} hover={hover} active={activeKey === "persona"} go={go} main={mainConversation} chord={bindings.persona} onMenu={onConvMenu} />
          <span aria-hidden data-persona-divider className="mx-2 my-1 h-px shrink-0 bg-line" />
          <span aria-hidden data-persona-fog className={cx("pointer-events-none absolute inset-x-0 top-full h-3 bg-gradient-to-b from-canvas to-transparent transition-opacity duration-150", scrolled ? "opacity-100" : "opacity-0")} />
        </div>
        {narrow && <NavGrid pages={pages} more={more} activeKey={activeKey} go={go} onNavigate={onNavigate} />}
        {!narrow && (
          <Section title={t("sidebar.pages")} folded={pagesFolded} onToggle={togglePages} right={pagesFolded ? <PagesTally pages={[...pages, ...more]} /> : undefined} first>
            {pages.map((p) => <NavRow key={p.k} {...p} reg={reg} hover={hover} active={activeKey === p.k} go={go} />)}
            {more.length > 0 && <MorePages more={more} reg={reg} hover={hover} activeKey={activeKey} go={go} />}
          </Section>
        )}

        {chats && (
          <Section
            title={t("sidebar.persona_chats")}
            folded={personaFolded}
            onToggle={togglePersona}
          >
            {pinnedConvs.length > 0 && <PinnedHeading onMenu={(at) => setChatsMenu(at)} />}
            {convs.map((c) => (
              <SessionRow key={c.session_id} s={c} k={`j:${c.session_id}`} reg={reg} hover={hover} active={activeKey === `j:${c.session_id}`} to={{ name: "persona", sid: c.session_id }} go={go} label={conversationTitle(c)} showState={false} icon={pinnedIds.includes(c.session_id) ? "pin" : undefined} onMenu={onConvMenu} menuLabel={t("conversation-menu.actions")} />
            ))}
            {assistant && <AutoConversations conversations={assistant.conversations} main={assistant.main} shown={convs} activeKey={activeKey} holds={autoHolds} reg={reg} hover={hover} go={go} />}
            {assistant && <NavRow k="history" reg={reg} hover={hover} active={activeKey === "history"} to={{ name: "history" }} go={go} icon="list" label={t("sidebar.all_chats")} hint={assistant.conversations.length > convs.length ? String(assistant.conversations.length) : undefined} />}
          </Section>
        )}

        <PersonalAgents reg={reg} hover={hover} activeKey={activeKey} go={go} />

        {/* the phone's Agents page shows them already: the drawer over it doesn't build them twice */}
        {!(narrow && route.name === "agent-list") && <AgentsBlock reg={reg} hover={hover} activeKey={activeKey} go={go} onNewAgent={onNewAgent} onNavigate={onNavigate} foldable />}
      </div>
      {convMenu && <ConversationRowMenu s={convMenu.s} main={convMenu.s.session_id === assistant?.main} pinned={pinnedIds.includes(convMenu.s.session_id)} anchor={convMenu.anchor} at={convMenu.at} onClose={() => setConvMenu(null)} />}
      {chatsMenu && <Menu anchor={chatsMenu.anchor ?? null} point={chatsMenu.point} onClose={() => setChatsMenu(null)} align="right" width={200} header={t("sidebar.pinned_section")} items={pinnedSectionItems(pinnedConvs.length)} />}
      {/* subscription rate-limit windows (#2432): nothing is drawn without data */}
      <SidebarUsage />
      <NodePrompt />
    </aside>
  );
}

/** The phone's "Agents" page (#1996): the sidebar's Agents block, the same components, filling the page. */
export function AgentsList({ route, onNewAgent }: { route: Route; onNewAgent: () => void }) {
  const activeKey = useActiveKey(route);
  const { nav, reg, hover, highlight } = useGlide(activeKey);
  const go = useGo();
  // no computer and nothing to list (docs/assistant/onboarding.md 4.5): the machines page's empty state, not a bare title
  const bare = useStore((s) => s.loaded && s.machines.length === 0 && Object.keys(s.launches).length === 0);
  const none = useAgentSessions().length === 0 && bare;
  const adding = useAddComputer();
  return (
    <div className="h-full overflow-y-auto overscroll-contain">
      <div ref={nav} onMouseLeave={() => hover(null)} className="relative mx-auto max-w-[640px] px-2.5 pb-6">
        {highlight}
        {none ? <NoComputers body={t("agentspage.no_computers_body")} onAdd={adding.open} /> : <AgentsBlock reg={reg} hover={hover} activeKey={activeKey} go={go} onNewAgent={onNewAgent} add={false} />}
      </div>
      {adding.dialog}
    </div>
  );
}

/** How many columns the phone's page grid has for `n` pages: two even rows (8 → 4 + 4), at most 5 to a row. */
export function navGridColumns(n: number): number {
  return Math.max(1, Math.min(5, Math.ceil(n / 2)));
}

/** The pages as two rows of icons in a phone's drawer: one tap away like the list, a third of its height. */
export function NavGrid({ pages, more = [], activeKey, go, onNavigate }: { pages: NavPage[]; more?: NavPage[]; activeKey: string; go: GoFn; onNavigate?: () => void }) {
  const [menu, setMenu] = useState<HTMLElement | null>(null);
  const moreActive = more.some((p) => p.k === activeKey);
  const moreBadge = more.find((p) => p.badge !== undefined);
  return (
    <nav aria-label={t("sidebar.pages")} className="mb-1 grid gap-0.5" style={{ gridTemplateColumns: `repeat(${navGridColumns(pages.length + (more.length ? 1 : 0))}, minmax(0, 1fr))` }}>
      {pages.map((p) => {
        const active = activeKey === p.k;
        return (
          <a
            key={p.k}
            href={href(p.to)}
            onClick={go(p.to)}
            title={p.title ?? p.label}
            aria-label={p.badge !== undefined ? `${p.label} ${p.badge}` : p.label}
            aria-current={active ? "page" : undefined}
            className={cx("relative z-10 flex min-h-12 min-w-0 flex-col items-center justify-center gap-1 rounded-[9px] px-0.5 py-1.5 transition-[background-color,transform] duration-150 active:scale-[0.96]", active ? "bg-hover" : "hover:bg-hover")}
          >
            <span className={cx("relative", active ? "text-ink" : "text-ink-3")}>
              <Icon name={p.icon} size={17} stroke={1.8} />
              {p.badge !== undefined && (
                <span className={cx("absolute -top-1.5 left-2.5 flex h-4 min-w-4 items-center justify-center whitespace-nowrap rounded-full px-1 text-[10px] font-semibold tabular-nums", BADGE_CLASS[p.badgeTone ?? "accent"])}>{p.badge}</span>
              )}
            </span>
            <span className={cx("w-full truncate text-center text-[11px] leading-tight", active ? "font-medium text-ink" : "text-ink-2")}>{p.short ?? p.label}</span>
          </a>
        );
      })}
      {more.length > 0 && (
        <button
          type="button"
          onClick={(e) => setMenu(menu ? null : e.currentTarget)}
          aria-haspopup="menu"
          aria-expanded={!!menu}
          aria-label={t("sidebar.more_pages_count", { count: more.length })}
          className={cx("relative z-10 flex min-h-12 min-w-0 flex-col items-center justify-center gap-1 rounded-[9px] px-0.5 py-1.5 transition-[background-color,transform] duration-150 active:scale-[0.96]", moreActive || menu ? "bg-hover" : "hover:bg-hover")}
        >
          <span className={cx("relative", moreActive ? "text-ink" : "text-ink-3")}>
            <Icon name="more" size={17} stroke={1.8} />
            {moreBadge && <span aria-hidden className={cx("absolute -top-0.5 -right-1 size-2 rounded-full ring-2 ring-canvas", DOT_CLASS[moreBadge.badgeTone ?? "accent"])} />}
          </span>
          <span className={cx("w-full truncate text-center text-[11px] leading-tight", moreActive ? "font-medium text-ink" : "text-ink-2")}>{t("sidebar.more_pages")}</span>
        </button>
      )}
      {menu && <Menu anchor={menu} onClose={() => setMenu(null)} align="right" width={220} header={t("sidebar.more_pages")} items={morePagesMenu(more, activeKey, onNavigate)} />}
    </nav>
  );
}

/** The pages under "More" as menu rows (the phone's grid, the rail): a choice opens the page, then `onNavigate`
 *  (the phone's drawer closes, as it does for a tap on any other page). */
function morePagesMenu(more: NavPage[], activeKey: string, onNavigate?: () => void): MenuItem[] {
  const open = (to: Route) => () => {
    navigate(to);
    onNavigate?.();
  };
  return [
    ...more.map((p): MenuItem => ({ key: p.k, label: p.label, icon: p.icon, hint: p.badge !== undefined ? String(p.badge) : p.hint, checked: p.k === activeKey || undefined, onSelect: open(p.to) })),
    { key: "-arrange", label: t("sidebar.arrange_pages"), icon: "gear", divider: true, onSelect: open({ name: "settings" }) },
  ];
}

/**
 * "More" at the end of the pages (#5026): the ones put away in Settings › General › Sidebar, and the extensions' pages past
 * the list's share of them. Folded by default; it opens by itself while one of its pages is open, and a count on one
 * of them shows as a dot on the row, so nothing waiting there goes unseen.
 */
function MorePages({ more, reg, hover, activeKey, go }: { more: NavPage[]; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const [folded, toggle] = useFolded("pages-more", true, { all: false, holds: more.some((p) => p.k === activeKey) });
  const counted = more.filter((p) => p.badge !== undefined);
  return (
    <>
      <button
        type="button"
        onClick={toggle}
        aria-expanded={!folded}
        title={counted.map((p) => `${p.label} ${p.badge}`).join(t("sidebar.list_separator")) || undefined}
        className="touch-hit relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row text-left transition-[background-color,transform] duration-150 hover:bg-hover active:scale-[0.98] [@media(pointer:coarse)]:min-h-11"
      >
        <span className="text-ink-3"><Icon name="more" size={14} stroke={1.8} /></span>
        <span className="min-w-0 flex-1 truncate text-[13px] text-ink-2">{t("sidebar.more_pages")}</span>
        {folded && counted.length > 0 && <span aria-hidden className={cx("size-1.5 rounded-full", DOT_CLASS[counted[0].badgeTone ?? "accent"])} />}
        <span className="text-[11px] text-ink-3 tabular-nums">{more.length}</span>
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
      </button>
      <Collapse open={!folded}>
        <div className="flex flex-col gap-px pl-3" inert={folded || undefined}>
          {more.map((p) => <NavRow key={p.k} {...p} reg={reg} hover={hover} active={activeKey === p.k} go={go} />)}
        </div>
      </Collapse>
    </>
  );
}

/** The sidebar's "Agents": grouping, the quick filter, "Pinned", "Starting", the groups (spaces, machines, repositories,
 *  states or todos) and "Recent". The drawer and the phone's Agents page both show it. `add`: the title row's "+". */
type AgentsBlockProps = { reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; onNewAgent: () => void; onNavigate?: () => void; add?: boolean; foldable?: boolean; chooseGrouping?: (g: AgentGrouping) => void };

function AgentsBlock(props: AgentsBlockProps) {
  const bounded = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  const pins = useStore((s) => s.pins);
  const [grouping, setGrouping] = useState(() => storedValue("persona.agentGrouping"));
  useEffect(() => onGrouping(setGrouping), []);
  // A saved grouping (including an explicitly selected default) is a view preference the paged API cannot express.
  const grouped = AGENT_GROUPINGS.some((g) => g.key === grouping);
  const full = pins.sessions.length > 0 || pins.spaces.length > 0 || grouped;
  const chooseGrouping = (g: AgentGrouping) => {
    storeValue("persona.agentGrouping", g);
    setGrouping(g);
    props.hover(null);
  };
  // Back to the paged list: forget the saved grouping. Offered only where the paged list exists and nothing is pinned
  // (pins keep the full list either way).
  const pagedList = bounded && grouped && pins.sessions.length === 0 && pins.spaces.length === 0
    ? () => { removeStoredValue("persona.agentGrouping"); setGrouping(null); props.hover(null); }
    : undefined;
  return bounded && !full ? <BoundedAgentsBlock {...props} chooseGrouping={chooseGrouping} /> : <LegacyAgentsBlock {...props} grouping={agentGrouping(grouping)} chooseGrouping={chooseGrouping} pagedList={pagedList} />;
}

function BoundedAgentsBlock({ reg, hover, activeKey, go, onNewAgent, add = true, chooseGrouping }: AgentsBlockProps) {
  const machines = useStore((s) => s.machines);
  const archives = useStore((s) => s.features.includes(AGENT_ARCHIVES_FEATURE));
  const [rowMenu, setRowMenu] = useState<{ s: Session; anchor: HTMLElement; at?: MenuAt } | null>(null);
  const [renaming, setRenaming] = useState<Session | null>(null);
  const [deleting, setDeleting] = useState<Session | null>(null);
  const [closing, setClosing] = useState<Session | null>(null);
  const [archiveClosing, setArchiveClosing] = useState<Session | null>(null);
  const [linkTarget, setLinkTarget] = useState<Session>();
  // the ⋯ toggles the menu, as in the full list
  const onRowMenu = useCallback<RowMenuFn>((s, anchor, at) => setRowMenu((m) => (!at && m?.anchor === anchor ? null : { s, anchor, at })), []);
  return <>
    <div className={SESSION_LIST_STYLE.heading}><span className={SESSION_LIST_STYLE.headingText}>{t("agentspage.title")}</span>{archives && <SectionAdd icon="archive" label={t("agentmenus.archived_agents")} to={{ name: "agent-archives" }} go={go} />}{add && <SectionAdd label={t("sidebar.new_agent")} onClick={onNewAgent} />}</div>
    <HubNoticeLine />
    <HerdrNotices />
    <PagedSessionList root={runtimeSessions} path="/sessions" identity={runtimeIdentity} compare={runtimeOrder} removeIdentity={runtimeRemoval} event="sessions_list" onRepo={chooseGrouping ? () => chooseGrouping("repo") : undefined}
      label={(dimension, id) => dimension === "machine" ? machineWithHub(id, machines) : id}
      renderRow={(s) => {
        const k = `s:${s.machine_id}/${s.session_id}`;
        return <SessionRow s={s} k={k} reg={reg} hover={hover} active={activeKey === k} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} onMenu={onRowMenu} hint={[machineWithHub(s.machine_id, machines), s.space_name || baseName(s.cwd)].filter(Boolean).join(" · ")} showTodoTag />;
      }} />
    {rowMenu && <AgentRowMenu s={rowMenu.s} anchor={rowMenu.anchor} at={rowMenu.at} onClose={() => setRowMenu(null)} onRename={setRenaming} onDelete={setDeleting} onLinkTodo={setLinkTarget} onCloseAgent={setClosing} onArchiveClose={setArchiveClosing} />}
    {renaming && <RenameDialog open session={renaming} onClose={() => setRenaming(null)} />}
    {deleting && <DeleteSessionDialog open session={deleting} machineName={machineWithHub(deleting.machine_id, machines)} onClose={() => setDeleting(null)} />}
    {closing && <CloseAgentDialog open session={closing} machineName={machineWithHub(closing.machine_id, machines)} onClose={() => setClosing(null)} />}
    {archiveClosing && <ArchiveCloseDialog open session={archiveClosing} machineName={machineWithHub(archiveClosing.machine_id, machines)} onClose={() => setArchiveClosing(null)} />}
    <LinkTodoDialog open={Boolean(linkTarget)} session={linkTarget} onClose={() => setLinkTarget(undefined)} />
  </>;
}

function LegacyAgentsBlock({ reg, hover, activeKey, go, onNewAgent, onNavigate, add = true, foldable = false, grouping, chooseGrouping, pagedList }: AgentsBlockProps & { grouping: AgentGrouping; chooseGrouping: (g: AgentGrouping) => void; pagedList?: () => void }) {
  const machines = useStore((s) => s.machines);
  const { sessions: all, loaded } = useFullAgentSessions();
  const mutes = useStore((s) => s.mutes);
  const pins = useStore((s) => s.pins);
  const todos = useStore((s) => s.todos);
  const todosSupported = useStore((s) => s.todosSupported);
  const todosLoaded = useStore((s) => s.todosLoaded);
  const launches = useStore((s) => Object.values(s.launches).filter((launch) => launch.phase !== "ready"));
  const availableGroupings = useMemo(() => {
    if (todosSupported === false) return AGENT_GROUPINGS.filter((g) => g.key !== "todo");
    return AGENT_GROUPINGS;
  }, [todosSupported]);
  // a hub without todos shows by machine, a by-todo choice kept for when it has them
  const shownGrouping = todosSupported === false && grouping === "todo" ? "machine" : grouping;
  // "State first" / "Recently updated" (iOS AgentSort, G099): not under "By status", whose sections are the states already
  const [sortChoice, setSortChoice] = useState<AgentSort>(() => agentSort(localStorage.getItem(AGENT_SORT_KEY)));
  const byUpdate = sortChoice === "updated" && shownGrouping !== "state";
  // the title row's ⋯ (#2812): what the grouping dropdown held besides the groupings, the sort and "Fold all" / "Unfold all"
  const listMenu = useMenu();
  const listLabel = byUpdate ? t("sidebar.list_menu_sorted", { sort: AGENT_SORTS[1].label }) : t("sidebar.list_menu");
  // the quick filter ("All" / "Needs you" / "Unread" / "Working"), stacked on any grouping; what it showed when chosen stays (`kept`)
  const [filter, setFilter] = useState(() => agentFilter(storedValue("persona.agentFilter")));
  const [kept, setKept] = useState<ReadonlySet<string>>(() => new Set());
  // "botmux" beside the filters (#2786): botmux sessions shown or hidden in this browser
  const [botmuxHidden, setBotmuxHidden] = useState(() => botmuxAndFilter(filter, localStorage.getItem(BOTMUX_HIDDEN_KEY) === "1", "filter").hidden);
  const botmuxCount = useMemo(() => all.filter(isBotmux).length, [all]);
  const saveBotmuxHidden = (next: boolean) => {
    localStorage.setItem(BOTMUX_HIDDEN_KEY, next ? "1" : "0");
    setBotmuxHidden(next);
  };
  const toggleBotmux = () => {
    const next = botmuxAndFilter(filter, !botmuxHidden, "botmux");
    saveBotmuxHidden(next.hidden);
    if (next.filter !== filter) choose(next.filter);
    hover(null);
  };
  const listed = useMemo(() => withoutBotmux(all, botmuxHidden), [all, botmuxHidden]);
  const { live, recent: offline } = useMemo(() => splitAgentSessions(listed, machines), [listed, machines]);
  const seeded = useRef(false);
  useEffect(() => {
    // a filter saved in this browser starts from what it matches once the list has loaded
    if (!loaded || seeded.current) return;
    seeded.current = true;
    setKept(matchingKeys(live, filter, mutes));
  }, [loaded, live, filter, mutes]);
  const filtered = filter !== "all";
  const filterTotals = useMemo(() => filterCounts(live, mutes), [live, mutes]);
  const shown = useMemo(() => applyFilter(live, filter, mutes, kept), [live, filter, mutes, kept]);
  // a pinned agent shows once, under "Pinned" (an offline one too, unless a filter is on); the groups below are the rest
  const pinnedAgents = useMemo(() => pinnedSessions(filtered ? shown : [...shown, ...offline], pins), [filtered, shown, offline, pins]);
  const unpinned = useMemo(() => {
    const keys = new Set(pinnedAgents.map(agentKey));
    return keys.size ? shown.filter((s) => !keys.has(agentKey(s))) : shown;
  }, [shown, pinnedAgents]);
  const recent = useMemo(() => (filtered ? [] : offline.filter((s) => !pinnedAgents.some((p) => agentKey(p) === agentKey(s)))), [filtered, offline, pinnedAgents]);
  const { spaces, loose } = useMemo(() => {
    const built = buildSpaces(unpinned, machines);
    return byUpdate ? projectsByUpdate(built) : built;
  }, [unpinned, machines, byUpdate]);
  const groups = useMemo(() => {
    if (shownGrouping === "space" || shownGrouping === "repo" || shownGrouping === "machine") return [];
    if (shownGrouping === "todo") return [];
    return buildAgentGroups(unpinned, machines, "state", mutes);
  }, [unpinned, machines, shownGrouping, mutes, todos]);
  // by todo waits for the first GET /todos: the socket pushes the sessions before it answers, and a tree drawn without
  // the todos puts every agent under No to-do for a moment, then moves them under their todos (#2812)
  const todosPending = shownGrouping === "todo" && !todosLoaded;
  const todoTree = useMemo(() => {
    if (shownGrouping !== "todo" || !todosLoaded) return undefined;
    const built = buildTodoTree(unpinned, machines, todos, mutes);
    return byUpdate ? todoTreeByUpdate(built) : built;
  }, [unpinned, machines, shownGrouping, mutes, todos, todosLoaded, byUpdate]);
  const repoMachines = useMemo(() => {
    if (shownGrouping !== "repo") return [];
    const built = buildRepoMachineTree(unpinned, machines);
    return byUpdate ? repoMachinesByUpdate(built) : built;
  }, [unpinned, machines, shownGrouping, byUpdate]);
  const machineTree = useMemo(() => {
    if (shownGrouping !== "machine") return [];
    const built = buildMachineTree(machines, unpinned, { pins, hideEmpty: filtered });
    return byUpdate ? machineTreeByUpdate(built) : built;
  }, [shownGrouping, machines, unpinned, pins, filtered, byUpdate]);
  // by todo a group mixes hosts (herdr, botmux…): when the list has more than one, rows outside herdr name theirs (#1836)
  const mixedHosts = useMemo(() => shownGrouping === "todo" && hostsOf(live).size > 1, [shownGrouping, live]);
  const unreadShown = filter === "unread" ? unreadOf(shown) : [];
  const choose = (f: AgentFilter) => {
    if (botmuxAndFilter(f, botmuxHidden, "filter").hidden !== botmuxHidden) saveBotmuxHidden(false);
    storeValue("persona.agentFilter", f);
    setFilter(f);
    setKept(matchingKeys(live, f, mutes));
    hover(null);
  };
  // Settings › Appearance › Show terminals in sidebar (off): the plain shells of each machine's terminal host, under its agents in "By machine"
  const showTerminals = useSidebarTerminals() && shownGrouping === "machine";
  const { listing: panes, refresh: refreshPanes } = usePanes(machines, showTerminals);
  const shellTrees = useMemo(() => {
    if (!showTerminals) return undefined;
    return Object.fromEntries(machines.map((m) => [m.id, shellsOnly(buildTerminalTree(panes[m.id]?.data, []))])) as Record<string, TermSpace[]>;
  }, [showTerminals, machines, panes]);
  const [rowMenu, setRowMenu] = useState<{ s: Session; anchor: HTMLElement; at?: MenuAt } | null>(null);
  const [spaceMenu, setSpaceMenu] = useState<{ sp: Space; anchor: HTMLElement; at?: MenuAt; fold?: MenuNode["fold"] } | null>(null);
  const [nodeMenu, setNodeMenu] = useState<{ node: MenuNode; at: MenuAt; header?: string } | null>(null);
  const [renaming, setRenaming] = useState<Session | null>(null);
  const [renamingSpace, setRenamingSpace] = useState<Session | null>(null);
  const [deleting, setDeleting] = useState<Session | null>(null);
  const [closingAgent, setClosingAgent] = useState<Session | null>(null);
  const [archiveClosing, setArchiveClosing] = useState<Session | null>(null);
  const archives = useStore((s) => s.features.includes(AGENT_ARCHIVES_FEATURE));
  const [confirmSeen, setConfirmSeen] = useState<Session[] | null>(null);
  // stable, like `go`: every row gets it, and a new one each render would re-render them all (SessionRow's memo)
  const onRowMenu = useCallback<RowMenuFn>((s, anchor, at) => setRowMenu((m) => (!at && m?.anchor === anchor ? null : { s, anchor, at })), []);
  // stable too: TabRow builds the menu of a tab with several panes on onNodeMenu, and that menu is its lead row's onMenu
  const onSpaceMenu = useCallback<SpaceMenuFn>((sp, anchor, at, fold) => setSpaceMenu((m) => (!at && m?.anchor === anchor ? null : { sp, anchor, at, fold })), []);
  const onNodeMenu = useCallback<NodeMenuFn>((node, at, header, toggle) => setNodeMenu((m) => (toggle && m?.at.anchor && m.at.anchor === at.anchor ? null : { node, at, header })), []);
  const [recentFolded, toggleRecent] = useFolded("recent", true, { holds: holdsRow(recent, activeKey), scope: AGENTS_FOLD });
  const [idleFolded, toggleIdle] = useFolded("idle-spaces", true, { scope: AGENTS_FOLD });
  const [pinnedFolded, togglePinned] = useFolded("agents-pinned", false, { holds: holdsRow(pinnedAgents, activeKey), scope: AGENTS_FOLD });
  const [launchesFolded, toggleLaunches] = useFolded("agents-launching", false, { holds: launches.some((l) => activeKey === `s:${l.machineId}/${l.id}`), scope: AGENTS_FOLD });
  // the whole block folds under its title in the sidebar (#2524); the phone's Agents page is the block, it doesn't
  const [blockFolded, toggleBlock] = useFolded("agents", false, { all: false });
  const folded = foldable && blockFolded;
  const [showAllRecent, setShowAllRecent] = useState(false);
  const recentShown = showAllRecent ? recent : firstAndCurrent(recent, 8, (s) => rowKey(s) === activeKey);
  const [linkTargetSession, setLinkTargetSession] = useState<Session | undefined>();
  const [linkTargetSpace, setLinkTargetSpace] = useState<LinkTargetSpace | undefined>();

  // a space with nothing to look at (every agent seen and waiting, none awaiting your reply) goes to "Idle spaces", like
  // the app; the open conversation's space stays where it is so the row under the pointer doesn't move away
  const busy = (sp: Space) => sp.sessions.some((s) => !isQuiet(s, mutes) || activeKey === `s:${s.machine_id}/${s.session_id}`);
  // pinned spaces lead; under a quick filter nothing folds into "Idle spaces"
  const { active, idle } = arrangeSpaces(spaces, { pins, mutes, filtered, busy });
  const tree = { machines, reg, hover, activeKey, go, mutes, onSpaceMenu, onRowMenu, onNodeMenu, onNavigate };

  return (
    <>
      <div className="group/sec mt-3 flex items-center gap-1 px-2">
        {foldable ? (
          <button type="button" aria-expanded={!folded} onClick={toggleBlock} className="touch-hit relative z-10 flex min-w-0 flex-1 items-center gap-1 whitespace-nowrap text-left text-[11px] font-semibold text-ink-3 hover:text-ink-2">
            <span className="min-w-0 truncate">Agents</span>
            <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-0 transition-[opacity,transform] duration-200 group-hover/sec:opacity-100 [@media(hover:none)]:opacity-100" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
          </button>
        ) : (
          <span className="min-w-0 flex-1 text-[11px] font-semibold text-ink-3">Agents</span>
        )}
        {folded && <SessionTally sessions={live} folded />}
        <button type="button" title={listLabel} aria-label={listLabel} aria-haspopup="menu" aria-expanded={Boolean(listMenu.anchor)} onClick={listMenu.open} className={cx("touch-hit relative z-10 flex size-5 shrink-0 items-center justify-center rounded-[5px] hover:bg-hover hover:text-ink-2", byUpdate ? "text-accent" : "text-ink-3", listMenu.anchor && "bg-hover")}>
          <Icon name={byUpdate ? "clock" : "more"} size={12} stroke={2} />
        </button>
        {add && <SectionAdd label={t("sidebar.new_agent")} onClick={onNewAgent} />}
      </div>
      {listMenu.anchor && (
        <Menu anchor={listMenu.anchor} onClose={listMenu.close} align="right" items={[...(shownGrouping === "state" ? [] : AGENT_SORTS.map((o): MenuItem => ({
          key: `sort:${o.key}`, label: o.label, icon: o.key === "updated" ? "clock" : "layers", checked: sortChoice === o.key,
          onSelect: () => { localStorage.setItem(AGENT_SORT_KEY, o.key); setSortChoice(o.key); hover(null); },
        }))),
          // every group of the list, like the app's list settings (#3027); "Expand all" also opens the spaces and tab groups
          // folded by hand that are not drawn now
          { key: "fold-all", label: t("sidebar.fold_all"), icon: "up", checked: false, divider: true, onSelect: () => { setAgentsFolded(true); hover(null); } },
          { key: "open-all", label: t("sidebar.open_all"), icon: "chevron", checked: false, onSelect: () => { setAgentsFolded(false); hover(null); } },
          // the agents archived from a row's ⋯ (lib/agent-archives.ts), to find and restore
          ...(archives ? [{ key: "archived", label: t("agentmenus.archived_agents"), icon: "archive" as const, divider: true, onSelect: () => { navigate({ name: "agent-archives" }); onNavigate?.(); hover(null); } }] : []),
          // a hub with the paged list: return to it from a grouping chosen there (its Browse by offers By repo)
          ...(pagedList ? [{ key: "paged-list", label: t("sidebar.paged_list"), icon: "clock" as const, divider: true, onSelect: pagedList }] : []),
        ]} />
      )}
      <FoldScope.Provider value={AGENTS_FOLD}>
      <Collapse open={!folded}>
      <div inert={folded || undefined}>
      <GroupingBar groupings={availableGroupings} selection={shownGrouping} onChoose={chooseGrouping} />
      <FilterBar selection={filter} counts={filterTotals} onChoose={choose} botmux={botmuxCount > 0 || botmuxHidden ? { hidden: botmuxHidden, onToggle: toggleBotmux } : undefined} />
      {/* "herdr is not running on <computer>" with "Start" (#1916): before a message to an agent there fails */}
      {/* the one line about the hub (#2897): updating, rolled back, a newer version or image */}
      {!folded && <HubNoticeLine />}
      {!folded && <HerdrNotices />}

      {unreadShown.length > 0 && (
        <button type="button" onClick={() => setConfirmSeen(unreadShown)} className="relative z-10 mt-1 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row text-left transition-[background-color,transform] duration-100 hover:bg-hover active:scale-[0.98] [@media(pointer:coarse)]:min-h-11">
          <span className="flex size-3.5 shrink-0 items-center justify-center text-green"><Icon name="check" size={13} stroke={2.4} /></span>
          <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{markAllSeenLabel()}</span>
          <span className="shrink-0 text-[11px] whitespace-nowrap text-ink-3 tabular-nums">{stateCount("unread", unreadShown.length)}</span>
        </button>
      )}

      {filtered && shown.length === 0 && (
        <div className="mt-2 flex items-center gap-2 px-2 py-1.5 text-[12px] text-ink-3">
          <span className="min-w-0 flex-1 truncate">{t("sidebar.filter_empty", { filter: AGENT_FILTERS.find((f) => f.key === filter)!.label })}</span>
          <button type="button" onClick={() => choose("all")} className="touch-hit relative z-10 shrink-0 whitespace-nowrap rounded-control px-1.5 py-0.5 text-[12px] font-medium text-accent-ink hover:bg-hover">{t("sidebar.show_all")}</button>
        </div>
      )}

      {pinnedAgents.length > 0 && (
        <Section title={<span className="flex items-center gap-1"><Icon name="pin" size={10} stroke={2.2} />{t("sidebar.pinned_section")}</span>} folded={pinnedFolded} onToggle={togglePinned} right={<SessionTally sessions={pinnedAgents} folded={pinnedFolded} />}>
          {pinnedAgents.map((s) => {
            const key = `s:${s.machine_id}/${s.session_id}`;
            const context = [s.space_name || baseName(s.cwd), machines.length > 1 ? machineWithHub(s.machine_id, machines) : ""].filter(Boolean).join(" · ");
            return <SessionRow key={key} s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} hint={context} onMenu={onRowMenu} />;
          })}
        </Section>
      )}

      {launches.length > 0 && <Section title={t("sidebar.launching")} folded={launchesFolded} onToggle={toggleLaunches} right={<GroupCount n={launches.length} label={t("sidebar.launching_count", { count: launches.length })} />}>
        {launches.map((launch) => <NavRow key={launch.id} k={`s:${launch.machineId}/${launch.id}`} reg={reg} hover={hover} active={activeKey === `s:${launch.machineId}/${launch.id}`} to={{ name: "session", mid: launch.machineId, sid: launch.id }} go={go} icon={launch.phase === "failed" ? "retry" : "agent"} label={launch.text || baseName(launch.cwd) || t("sidebar.launch_untitled")} hint={launch.phase === "failed" ? t("sidebar.launch_failed") : t("sidebar.launch_background")} />)}
      </Section>}

      {shownGrouping === "space" ? <>
      {active.map((sp) => (
        <SpaceSection key={sp.key} sp={sp} machineName={machines.length > 1 ? machineWithHub(sp.machineId, machines) : undefined} reg={reg} hover={hover} activeKey={activeKey} go={go} pinned={isSpacePinned(sp.machineId, sp.name, pins)} muted={isSpaceMuted(sp, mutes)} onMenu={onSpaceMenu} onRowMenu={onRowMenu} onNavigate={onNavigate} />
      ))}

      {idle.length > 0 && (
        <Section title={t("sidebar.idle_spaces", { count: idle.length })} folded={idleFolded} onToggle={toggleIdle}>
          {idle.flatMap((sp) =>
            sp.sessions.map((s) => (
              <SessionRow key={s.session_id} s={s} k={`s:${s.machine_id}/${s.session_id}`} reg={reg} hover={hover} active={activeKey === `s:${s.machine_id}/${s.session_id}`} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} hint={sp.sessions.length > 1 ? `${sp.name} › ${tabLabel(s)}` : sp.name} onMenu={onRowMenu} />
            )),
          )}
        </Section>
      )}

      {loose.length > 0 && <LooseSection sessions={loose} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} />}

      </> : shownGrouping === "machine" ? machineTree.map((m) => (
        <MachineSection key={m.key} m={m} shells={shellTrees?.[m.machine.id]} {...tree} />
      )) : shownGrouping === "repo" ? (
        repoMachines.map((m) => (
          <RepoMachineSection key={m.key} m={m} flat={repoMachines.length <= 1} machines={machines} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} />
        ))
      ) : todosPending ? (
        <p className="px-2 py-1 text-[12px] text-ink-3">{t("sidebar.loading")}</p>
      ) : todoTree ? (
        <TodoTreeView tree={todoTree} machines={machines} showHost={mixedHosts} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} onNavigate={onNavigate} />
      ) : groups.map((group) => (
        <AgentSection key={group.key} group={group} machines={machines} byTodo={shownGrouping === "todo"} showHost={mixedHosts} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} onNavigate={onNavigate} />
      ))}

      {recent.length > 0 && (
        <Section title={t("sidebar.recent")} folded={recentFolded} onToggle={toggleRecent} right={<GroupCount n={recent.length} label={t("sidebar.agent_count", { count: recent.length })} />}>
          {recentShown.map((s) => (
            <SessionRow key={s.session_id} s={s} k={`s:${s.machine_id}/${s.session_id}`} reg={reg} hover={hover} active={activeKey === `s:${s.machine_id}/${s.session_id}`} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} onMenu={onRowMenu} />
          ))}
          {recent.length > 8 && (
            <button type="button" onClick={() => setShowAllRecent((x) => !x)} className="relative z-10 w-full rounded-[7px] px-2 py-1 text-left text-[12px] text-ink-3 hover:text-ink-2 [@media(pointer:coarse)]:min-h-11">
              {showAllRecent ? t("sidebar.show_less") : t("sidebar.show_more", { count: recent.length - recentShown.length })}
            </button>
          )}
        </Section>
      )}
      </div>
      </Collapse>
      </FoldScope.Provider>
      {/* the row's ⋯ (and its right click): pin, rename, mute, the todo it hangs on, close, delete — a long press on iOS
          Safari sends no contextmenu, so nothing lives only there */}
      {rowMenu && <AgentRowMenu s={rowMenu.s} anchor={rowMenu.anchor} at={rowMenu.at} onClose={() => setRowMenu(null)} onRename={setRenaming} onDelete={setDeleting} onLinkTodo={setLinkTargetSession} onCloseAgent={setClosingAgent} onArchiveClose={setArchiveClosing} />}
      {spaceMenu && (
        <SpaceMenu
          sp={spaceMenu.sp}
          anchor={spaceMenu.anchor}
          at={spaceMenu.at}
          fold={spaceMenu.fold}
          onClose={() => setSpaceMenu(null)}
          onRename={setRenamingSpace}
          onMarkSeen={setConfirmSeen}
          onLinkTodo={(sp) => setLinkTargetSpace({ machineId: sp.machineId, space: sp.name })}
        />
      )}
      {nodeMenu && <NodeMenu node={nodeMenu.node} at={nodeMenu.at} header={nodeMenu.header} onClose={() => setNodeMenu(null)} changed={() => { void refresh().catch(() => {}); void refreshPanes(); }} />}
      {renaming && <RenameDialog open session={renaming} onClose={() => setRenaming(null)} />}
      {renamingSpace && <RenameSpaceDialog open session={renamingSpace} onClose={() => setRenamingSpace(null)} />}
      {deleting && <DeleteSessionDialog open session={deleting} machineName={machineWithHub(deleting.machine_id, machines)} onClose={() => setDeleting(null)} />}
      {closingAgent && <CloseAgentDialog open session={closingAgent} machineName={machineWithHub(closingAgent.machine_id, machines)} onClose={() => setClosingAgent(null)} />}
      {archiveClosing && <ArchiveCloseDialog open session={archiveClosing} machineName={machineWithHub(archiveClosing.machine_id, machines)} onClose={() => setArchiveClosing(null)} />}
      <MarkSeenDialog list={confirmSeen} onClose={() => setConfirmSeen(null)} onDone={() => setKept(new Set())} />
      <LinkTodoDialog open={Boolean(linkTargetSession)} session={linkTargetSession} onClose={() => setLinkTargetSession(undefined)} />
      <LinkTodoDialog open={Boolean(linkTargetSpace)} space={linkTargetSpace} onClose={() => setLinkTargetSpace(undefined)} />
    </>
  );
}

/** How Agents are grouped (#2812): one segment per grouping, "By to-do" first, above the quick filter. The segments say
 *  the short name (To-do / Machine…) so all five fit the sidebar's width on one line; the full name is their label. */
function GroupingBar({ groupings, selection, onChoose }: { groupings: readonly { key: AgentGrouping; label: string; short: string }[]; selection: AgentGrouping; onChoose: (g: AgentGrouping) => void }) {
  return (
    <div role="radiogroup" aria-label={t("sidebar.grouping_header")} className="mt-1.5 mx-1 flex gap-0.5 rounded-full bg-inset p-0.5 shadow-hairline">
      {groupings.map((g) => {
        const on = g.key === selection;
        return (
          <button
            key={g.key}
            type="button"
            role="radio"
            aria-checked={on}
            aria-label={g.label}
            title={g.label}
            onClick={() => onChoose(g.key)}
            className={cx(
              "touch-hit relative z-10 flex h-6 min-w-0 flex-auto items-center justify-center whitespace-nowrap rounded-full px-1.5 text-[11.5px] transition-[background-color,color,box-shadow] duration-150",
              on ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-2 hover:bg-hover hover:text-ink",
            )}
          >
            <span className="truncate">{g.short}</span>
          </button>
        );
      })}
    </div>
  );
}

/** "All" / "Needs you" / "Unread" / "Working" with their counts (iOS QuickFilterBar): "All" and the states that have agents now; the
 * chosen one stays while it empties, so it can be left. Then "botmux" (#2786), when there are botmux sessions: a
 * show / hide switch that stacks on any filter. Hidden when there is nothing to pick. */
function FilterBar({ selection, counts, onChoose, botmux }: { selection: AgentFilter; counts: FilterCounts; onChoose: (f: AgentFilter) => void; botmux?: { hidden: boolean; onToggle: () => void } }) {
  const keys = shownFilters(selection, counts);
  if (keys.length < 2 && !botmux) return null;
  return (
    <div className="mt-1.5 flex flex-wrap gap-0.5 px-1">
      <div role="radiogroup" aria-label={t("sidebar.quick_filters")} className="contents">
      {keys.map((k) => {
        const f = AGENT_FILTERS.find((x) => x.key === k)!;
        const on = k === selection;
        const n = k === "all" ? undefined : counts[k];
        return (
          <button
            key={k}
            type="button"
            role="radio"
            aria-checked={on}
            aria-label={n === undefined ? f.label : `${f.label} ${n}`}
            onClick={() => onChoose(k)}
            className={cx(
              "touch-hit relative z-10 flex h-6 items-center gap-1 whitespace-nowrap rounded-full px-[7px] text-[11.5px] transition-[background-color,color,box-shadow] duration-150",
              on ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-2 hover:bg-hover hover:text-ink",
            )}
          >
            {f.state && <StatusMark state={f.state} size="sm" waiting={k === "bg_waiting"} />}
            {f.label}
            {n !== undefined && <span className={cx("tabular-nums", on ? "text-ink-2" : "text-ink-3")}>{n}</span>}
          </button>
        );
      })}
      </div>
      {botmux && (
        <button
          type="button"
          aria-pressed={!botmux.hidden}
          title={t(botmux.hidden ? "lib-agent-filter.botmux_show" : "lib-agent-filter.botmux_hide")}
          onClick={botmux.onToggle}
          className={cx(
            "touch-hit relative z-10 flex h-6 items-center gap-1 whitespace-nowrap rounded-full px-[7px] text-[11.5px] transition-[background-color,color,box-shadow] duration-150",
            botmux.hidden ? "text-ink-3 line-through hover:bg-hover hover:text-ink-2" : "text-ink-2 hover:bg-hover hover:text-ink",
          )}
        >
          <Icon name={botmux.hidden ? "x" : "check"} size={10} stroke={2.4} />
          {t("lib-agent-filter.botmux")}
        </button>
      )}
    </div>
  );
}

function AccountRow({ phase, online, total }: { phase: ConnPhase; online: number; total: number }) {
  const menu = useMenu();
  const theme = useTheme();
  const otherHubs = hubMenuItems(useHubs());
  const themes: { k: Theme; label: string; icon: IconName }[] = [
    { k: "system", label: t("sidebar.theme_system"), icon: "monitor" },
    { k: "light", label: t("sidebar.theme_light"), icon: "sun" },
    { k: "dark", label: t("sidebar.theme_dark"), icon: "moon" },
  ];
  const disconnect = useDisconnect();
  return (
    <>
      <button type="button" onClick={menu.open} className="flex w-full items-center gap-2.5 rounded-control p-1.5 text-left transition-[background-color,transform] duration-100 hover:bg-hover active:scale-[0.98]">
        <AppMark size={32} radius={8} className="shadow-hairline" />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[13px] leading-tight font-medium text-ink">Persona</span>
          <span className="flex items-center gap-1.5 truncate text-[11px] leading-tight text-ink-3">
            <span aria-hidden data-conn-dot className={cx("size-1.5 rounded-full", phaseTone(phase).color)} style={phaseTone(phase).breathing ? BREATHING : undefined} />
            {connStatus(phase, online, total)}
          </span>
        </span>
        {/* the menu is gone while the hub revokes this browser's token: the row it opened from says it is busy */}
        {disconnect.busy ? <Spinner size={12} /> : <Icon name="chevron" size={12} stroke={2} className="text-ink-3" />}
      </button>
      {menu.anchor && (
        <Menu
          anchor={menu.anchor}
          onClose={menu.close}
          width={220}
          header={t("sidebar.appearance")}
          items={[
            ...themes.map((t) => ({ key: t.k, label: t.label, icon: t.icon, checked: theme === t.k, onSelect: () => setTheme(t.k) })),
            // the other hubs in this browser's list (#1014); none, and the menu is as before
            ...otherHubs,
            { key: "settings", label: t("sidebar.nav_settings"), icon: "gear", divider: otherHubs.length > 0, onSelect: () => navigate({ name: "settings" }) },
            { key: "logout", label: t("sidebar.disconnect"), icon: "logout", danger: true, onSelect: disconnect.start },
          ]}
        />
      )}
      {disconnect.dialog}
    </>
  );
}

// Every group in the sidebar is one of these: its title folds it, and the fold is kept in this browser (useFolded).
const ORIGIN_ICON: Record<AutoOrigin, IconName> = { inbox: "inbox", task: "clock", lark_agent: "at", spawned: "fork", extension: "extension", other: "more" };

/** The open conversation is an automatic one with no row of its own above (not pinned): its group's row stands for it. */
function isOpenAuto(conversations: readonly Session[] | undefined, activeKey: string, shown: readonly Session[]): boolean {
  if (!activeKey.startsWith("j:")) return false;
  const c = conversations?.find((x) => `j:${x.session_id}` === activeKey);
  return !!c && isAutoConversation(c) && !shown.includes(c);
}

/** A folded row's state: someone waiting for the user first (its count), else a dot while some run. */
function TallyMark({ tally }: { tally: OriginTally }) {
  if (tally.needsYou > 0) {
    return (
      <span title={t("sidebar.auto_needs_you", { count: tally.needsYou })} className={cx("flex h-4.5 min-w-4.5 shrink-0 items-center justify-center whitespace-nowrap rounded-full px-1 text-[10.5px] font-semibold tabular-nums", BADGE_CLASS.attention)}>
        {tally.needsYou}
      </span>
    );
  }
  if (tally.working > 0) {
    return (
      <span title={t("sidebar.auto_working", { count: tally.working })} className="flex">
        <StatusMark state="working" size="sm" />
      </span>
    );
  }
  return null;
}

/** "Automatic" (#3264): the conversations the hub opened by itself, out of "Recent", as one row folded by default (per browser);
 *  open, a row per origin that has any, each opening "All chats" filtered to it. Folded, the row still shows what runs and
 *  what waits for the user. Nothing on a hub that doesn't tell origins. */
function AutoConversations({ conversations, main, shown, activeKey, holds, reg, hover, go }: { conversations: readonly Session[]; main?: string; shown: readonly Session[]; activeKey: string; holds: boolean; reg: RegFn; hover: (k: string | null) => void; go: GoFn }) {
  const { total, groups } = autoConversations(conversations, main);
  const [folded, toggle] = useFolded("auto-chats", true, { holds });
  // one ref for the row's life, so a re-render does not let go of it and register it again (useGlide's reg)
  const regRow = useMemo(() => reg("auto-chats"), [reg]);
  if (total.count === 0) return null;
  const open = activeKey.startsWith("j:") ? conversations.find((c) => `j:${c.session_id}` === activeKey && !shown.includes(c)) : undefined;
  const openGroup = open ? autoOrigin(open) : undefined;
  return (
    <div>
      <button
        type="button"
        ref={regRow}
        aria-expanded={!folded}
        aria-label={t("sidebar.auto_chats_label", { count: total.count })}
        onClick={toggle}
        onMouseEnter={() => hover("auto-chats")}
        onFocus={() => hover("auto-chats")}
        onBlur={() => hover(null)}
        className="relative z-10 flex w-full items-center gap-2 whitespace-nowrap rounded-[7px] px-2 sidebar-row text-left transition-transform duration-150 active:scale-[0.98] [@media(pointer:coarse)]:min-h-11"
      >
        <span className="text-ink-3"><Icon name="bolt" size={14} stroke={1.8} /></span>
        <span className="min-w-0 flex-1 truncate text-[13px] text-ink-2">{t("sidebar.auto_chats")}</span>
        {folded && <TallyMark tally={total} />}
        <span className="text-[11px] text-ink-3 tabular-nums">{total.count}</span>
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
      </button>
      <Collapse open={!folded}>
        <div className="flex flex-col gap-px pl-3" inert={folded || undefined}>
          {groups.map((g) => {
            const k = `auto:${g.origin}`;
            return (
              <NavRow key={g.origin} k={k} reg={reg} hover={hover} active={activeKey === k || openGroup === g.origin} to={{ name: "history", origin: g.origin }} go={go} icon={ORIGIN_ICON[g.origin]} label={originLabel(g.origin)} hint={String(g.count)} badge={g.needsYou || undefined} badgeTone="attention" dot={!g.needsYou && g.working > 0 ? "working" : undefined} />
            );
          })}
        </div>
      </Collapse>
    </div>
  );
}

function Section({ title, children, folded, onToggle, right, onMenu, first, hint }: { title: React.ReactNode; children: React.ReactNode; folded: boolean; onToggle: () => void; right?: React.ReactNode; onMenu?: (at: MenuAt, el: HTMLElement) => void; first?: boolean; hint?: string }) {
  return (
    <div className={first ? "mt-1" : "mt-3"}>
      <div className="group/sec relative flex items-center gap-1 px-2 pb-1" {...(onMenu ? contextMenuProps(onMenu) : {})}>
        <button type="button" aria-expanded={!folded} onClick={onToggle} title={hint} className="touch-hit relative z-10 flex min-w-0 flex-1 items-center gap-1 whitespace-nowrap text-left text-[11px] font-semibold text-ink-3 hover:text-ink-2">
          <span className="min-w-0 truncate">{title}</span>
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-0 transition-[opacity,transform] duration-200 group-hover/sec:opacity-100 [@media(hover:none)]:opacity-100" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        </button>
        {right}
      </div>
      {/* a group whose name does not say why its agents are here ("Not on a to-do", "Opened in the terminal") says it in one line, folded too */}
      {hint && <p className="px-2 pb-1 text-[11px] leading-[1.45] text-ink-3">{hint}</p>}
      <Collapse open={!folded}>
        <div className="flex flex-col gap-px" inert={folded || undefined}>{children}</div>
      </Collapse>
    </div>
  );
}

function AgentSection({ group, machines, byTodo, showHost, reg, hover, activeKey, go, onRowMenu, onNavigate }: { group: AgentGroup; machines: Machine[]; byTodo?: boolean; showHost?: boolean; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; onRowMenu: RowMenuFn; onNavigate?: () => void }) {
  const [folded, toggle] = useFolded(group.key, group.folded, { follow: true, holds: holdsRow(group.sessions, activeKey) });
  const row = (s: Session) => {
    const key = `s:${s.machine_id}/${s.session_id}`;
    const context = [s.space_name || baseName(s.cwd), machines.length > 1 ? machineWithHub(s.machine_id, machines) : ""].filter(Boolean).join(" · ");
    return <SessionRow key={key} s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} hint={context} host={showHost && sessionHost(s) !== "herdr" ? sessionHost(s) : undefined} showTodoTag={!byTodo} onMenu={onRowMenu} />;
  };

  const statusInfo = group.todoStatus ? TODO_STATUS_INFO[group.todoStatus as TodoStatus] : undefined;
  const statusTone = statusInfo?.tone === "red" ? "text-red" : statusInfo?.tone === "green" ? "text-green" : statusInfo?.tone === "amber" ? "text-orange" : "text-ink-3";

  const titleNode = group.todoId ? (
    <span className="flex min-w-0 items-center gap-1.5">
      <Icon name="todo" size={12} stroke={2} className="shrink-0 text-ink-3" />
      <span
        onClick={(e) => {
          e.stopPropagation();
          openTodoFromTag(group.todoId!, navigate, onNavigate);
        }}
        className="truncate normal-case tracking-normal hover:underline hover:text-ink cursor-pointer"
      >
        {group.name}
      </span>
    </span>
  ) : (
    <span className="flex min-w-0 items-center gap-1.5">
      {group.state && <span aria-hidden><StatusMark state={group.state} size="sm" waiting={group.waiting} /></span>}
      <span className="truncate">{group.name}</span>
    </span>
  );

  const rightNode = group.todoStatus ? (
    <span className={cx("text-[11px] font-semibold tracking-normal normal-case", statusTone)}>
      {statusInfo?.label}
    </span>
  ) : (
    <GroupCount n={group.sessions.length} label={t("sidebar.agent_count", { count: group.sessions.length })} />
  );

  return (
    <Section title={titleNode} folded={folded} onToggle={toggle} right={rightNode} hint={group.hint}>
      {group.sessions.map(row)}
    </Section>
  );
}

type TreeRowProps = { machines: Machine[]; showHost?: boolean; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; onRowMenu: RowMenuFn; onNavigate?: () => void };

const TREE_TONE: Record<TreeState, string> = { needs_you: "text-attention", working: "text-orange", bg_waiting: "text-status-waiting", waiting: "text-ink-2", done: "text-ink-3" };

/** "Needs you" / "Working" / "Done" at the end of a tree node's line. */
function TreeStateText({ state }: { state: TreeState }) {
  return <span className={cx("shrink-0 text-[11px] font-semibold whitespace-nowrap", TREE_TONE[state])}>{treeStateLabel(state)}</span>;
}

/** Agents "By to-do" as project › to-do › agent (#2526): projects with their todos, a spawned agent under the one that
 *  spawned it, "Unassigned" last (by project). */
function TodoTreeView({ tree, ...p }: { tree: TodoTree } & TreeRowProps) {
  return (
    <>
      {tree.projects.map((pr) => (
        <TreeProjectSection key={pr.key} k={pr.key} name={pr.name} state={pr.state} folded={pr.folded} sessions={pr.sessions} todos={pr.todos} {...p} />
      ))}
      {tree.loose.map((g) => (
        <TreeProjectSection key={g.key} k={g.key} name={g.name} state={g.state} folded={g.folded} sessions={g.sessions} todos={g.projects} hint={g.hint} loose {...p} />
      ))}
    </>
  );
}

function TreeProjectSection({ k, name, state, folded: initial, sessions, todos, loose, hint, ...p }: { k: string; name: string; state: TreeState; folded: boolean; sessions: Session[]; todos: TreeTodo[]; loose?: boolean; hint?: string } & TreeRowProps) {
  const [folded, toggle] = useFolded(k, initial, { follow: true, holds: holdsRow(sessions, p.activeKey) });
  return (
    <Section
      title={<span className="flex min-w-0 items-center gap-1.5"><Icon name={loose ? "layers" : "folder"} size={12} stroke={2} className="shrink-0 text-ink-3" /><span className="truncate normal-case tracking-normal text-[11.5px] text-ink-2">{name}</span></span>}
      folded={folded}
      onToggle={toggle}
      right={<TreeStateText state={state} />}
      hint={hint}
    >
      {todos.map((td) => <TreeTodoRows key={td.key} td={td} {...p} />)}
    </Section>
  );
}

/** One todo (or, under "Unassigned", one project) and its agents under its own folding line. */
function TreeTodoRows({ td, ...p }: { td: TreeTodo } & TreeRowProps) {
  const [folded, toggle] = useFolded(td.key, td.folded, { follow: true, holds: holdsRow(td.sessions, p.activeKey) });
  return (
    <div className="mt-px">
      <div className="relative z-10 flex w-full min-w-0 items-center gap-1.5 rounded-control py-1 pl-2 pr-2 text-[12px] text-ink-2 hover:bg-hover">
        <button type="button" aria-expanded={!folded} aria-label={td.name} onClick={toggle} className="touch-hit flex shrink-0 items-center">
          <Icon name="chevron" size={9} stroke={2.4} className="opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        </button>
        {td.todoId && <Icon name="todo" size={11} stroke={2} className="shrink-0 text-ink-3" />}
        {td.todoId ? (
          <a href={href({ name: "todos", id: td.todoId })} onClick={(e) => { e.preventDefault(); openTodoFromTag(td.todoId!, navigate, p.onNavigate); }} className="min-w-0 flex-1 truncate font-medium text-ink hover:underline">{td.name}</a>
        ) : (
          <button type="button" onClick={toggle} className="min-w-0 flex-1 truncate text-left">{td.name}</button>
        )}
        <GroupCount n={td.sessions.length} label={t("sidebar.agent_count", { count: td.sessions.length })} className="shrink-0" />
        {td.status === "usage_limit" ? (
          <span className="shrink-0 text-[11px] font-semibold whitespace-nowrap text-orange">{t("lib-usage-limit.short")}</span>
        ) : (
          <TreeStateText state={td.state} />
        )}
      </div>
      <Collapse open={!folded}>
        <div className="flex flex-col gap-px pl-3" inert={folded || undefined}>
          {td.agents.map((a) => <TreeAgentRows key={a.key} a={a} depth={0} {...p} />)}
        </div>
      </Collapse>
    </div>
  );
}

/** An agent and, folded under it, the agents it spawned for the same todo. */
function TreeAgentRows({ a, depth, machines, showHost, reg, hover, activeKey, go, onRowMenu }: { a: TreeAgent; depth: number } & TreeRowProps) {
  const subtree = (n: TreeAgent): Session[] => n.children.flatMap((c) => [c.session, ...subtree(c)]);
  const [folded, toggle] = useFolded(a.key + ":children", a.folded, { follow: true, holds: holdsRow(subtree(a), activeKey) });
  const s = a.session;
  const key = `s:${s.machine_id}/${s.session_id}`;
  const context = [s.space_name || baseName(s.cwd), machines.length > 1 ? machineWithHub(s.machine_id, machines) : ""].filter(Boolean).join(" · ");
  return (
    <div style={depth ? { paddingLeft: 12 } : undefined}>
      <div className="flex items-center">
        {a.children.length > 0 && (
          <button type="button" aria-expanded={!folded} aria-label={t("lib-todo-tree.spawned_agents", { count: a.children.length })} onClick={toggle} className="touch-hit relative z-10 -mr-1 flex size-4 shrink-0 items-center justify-center text-ink-3">
            <Icon name="chevron" size={9} stroke={2.4} className="opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
          </button>
        )}
        <div className="min-w-0 flex-1">
          <SessionRow s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} hint={context} host={showHost && sessionHost(s) !== "herdr" && sessionHost(s) !== "botmux" ? sessionHost(s) : undefined} onMenu={onRowMenu} />
        </div>
      </div>
      {a.children.length > 0 && (
        <Collapse open={!folded}>
          <div className="flex flex-col gap-px border-l border-line ml-2" inert={folded || undefined}>
            {a.children.map((c) => <TreeAgentRows key={c.key} a={c} depth={depth + 1} machines={machines} showHost={showHost} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} />)}
          </div>
        </Collapse>
      )}
    </div>
  );
}

/** One computer in the repository tree (#2108 / #2126): header folds the whole computer; flat when only one computer has agents. */
function RepoMachineSection({ m, flat, machines, reg, hover, activeKey, go, onRowMenu }: { m: RepoMachine; flat: boolean; machines: Machine[]; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; onRowMenu: RowMenuFn }) {
  const online = m.machine.online;
  const [folded, toggle] = useFolded(m.foldKey, !online, { holds: holdsRow(m.sessions, activeKey) });
  const repos = m.repos.map((tree) => <RepoSection key={tree.key} tree={tree} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} />);
  if (flat) return <>{repos}</>;
  return (
    <div className="mt-3">
      <div className="group/sec flex items-center gap-1 px-2 pb-0.5">
        <button type="button" aria-expanded={!folded} onClick={toggle} className="relative z-10 flex min-h-7 min-w-0 flex-1 items-center gap-1.5 whitespace-nowrap text-left [@media(pointer:coarse)]:min-h-11">
          <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", online ? "bg-green" : "bg-line-strong")} />
          <span className={cx("min-w-0 truncate text-[12.5px] font-semibold", online ? "text-ink" : "text-ink-3")}>{machineWithHub(m.machine.id, machines)}</span>
          <span className="sr-only">{online ? t("sidebar.online") : neverConnected(m.machine) ? t("sidebar.not_connected_yet") : t("sidebar.offline")}</span>
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 text-ink-3 opacity-0 transition-[opacity,transform] duration-200 group-hover/sec:opacity-100 [@media(hover:none)]:opacity-100" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        </button>
        {m.needs > 0 && !(folded && online) ? (
          <span title={stateCount("needs_you", m.needs)} className="flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-field px-1 text-[10.5px] font-semibold text-attention tabular-nums">
            {m.needs}<span className="sr-only"> {t("sidebar.needs_you_suffix", { count: m.needs })}</span>
          </span>
        ) : !online ? (
          <span className="shrink-0 text-[10.5px] whitespace-nowrap text-ink-3">{neverConnected(m.machine) ? t("sidebar.not_connected_yet") : t("sidebar.offline")}</span>
        ) : (
          <SessionTally sessions={m.sessions} folded={folded} />
        )}
      </div>
      <Collapse open={!folded}>
        <div className="flex flex-col gap-px" inert={folded || undefined}>
          {repos}
        </div>
      </Collapse>
    </div>
  );
}

/** One repository on one machine ("By repo"): its working trees under it, the main checkout marked and first. */
function RepoSection({ tree, reg, hover, activeKey, go, onRowMenu }: { tree: RepoTree; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; onRowMenu: RowMenuFn }) {
  const [folded, toggle] = useFolded(tree.key, false, { holds: tree.worktrees.some((w) => holdsRow(w.tree, activeKey)) });
  const row = (s: Session, hint: string) => {
    const key = `s:${s.machine_id}/${s.session_id}`;
    return <SessionRow key={key} s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} hint={hint} onMenu={onRowMenu} />;
  };
  // A lone main checkout, or an Orca worktree whose folder names its project, needs no extra level.
  const flat = !showsWorktreeLevel(tree);
  return (
    <Section
      title={<span className="flex min-w-0 items-center gap-1.5"><Icon name="folder" size={12} stroke={2} className="shrink-0 text-ink-3" /><span className="truncate">{tree.name}</span></span>}
      folded={folded}
      onToggle={toggle}
      right={<GroupCount n={tree.count} label={t("sidebar.agent_count", { count: tree.count })} />}
    >
      {flat ? tree.worktrees[0]!.sessions.map((s) => row(s, tree.name)) : tree.worktrees.map((w) => <RepoWorktreeRows key={w.key} w={w} repo={tree.name} row={row} activeKey={activeKey} />)}
    </Section>
  );
}

/** One working tree's agents under its own folding row: the main checkout wears a home mark, and a tree with nothing
 *  running says so. Orca child worktrees have their own remembered, initially expanded chip after the parent's agents. */
function RepoWorktreeRows({ w, repo, row, activeKey }: { w: RepoWorktree; repo: string; row: (s: Session, hint: string) => React.ReactNode; activeKey: string }) {
  const [folded, toggle] = useFolded(w.key, w.sessions.length === 0, { holds: holdsRow(w.tree, activeKey) });
  const descendants = w.tree.slice(w.sessions.length);
  const [childrenFolded, toggleChildren] = useFolded(`${w.key}/children`, false, { holds: holdsRow(descendants, activeKey) });
  const mutes = useStore((st) => st.mutes);
  const running = w.sessions.filter((s) => !isQuiet(s, mutes)).length;
  return (
    <div className="mt-px">
      <button type="button" aria-expanded={!folded} onClick={toggle} className="relative z-10 flex w-full min-w-0 items-center gap-1.5 whitespace-nowrap rounded-control py-1 pl-3 pr-2 text-left text-[11.5px] text-ink-2 hover:bg-hover">
        <Icon name="chevron" size={9} stroke={2.4} className="shrink-0 opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        {w.main && <Icon name="home" size={11} stroke={2} className="shrink-0 text-ink-3" />}
        <span className="min-w-0 truncate">{w.name}</span>
        <span className="ml-auto shrink-0 text-[10.5px] text-ink-3 tabular-nums">{running > 0 ? t("sidebar.worktree_agents", { count: w.sessions.length }) : t("sidebar.idle")}</span>
      </button>
      <Collapse open={!folded}>
        <div inert={folded || undefined}>
          <div className="flex flex-col gap-px pl-2">{w.sessions.map((s) => row(s, `${repo} › ${w.name}`))}</div>
          {w.children.length > 0 && (
            <>
              <RepoChildToggle count={w.children.length} counts={w.childStates} folded={childrenFolded} onToggle={toggleChildren} />
              <Collapse open={!childrenFolded}>
                <div className="ml-3.5 flex w-[calc(100%-0.875rem)] flex-col gap-px" inert={childrenFolded || undefined}>
                  {w.children.map((c) => <RepoWorktreeRows key={c.key} w={c} repo={repo} row={row} activeKey={activeKey} />)}
                </div>
              </Collapse>
            </>
          )}
        </div>
      </Collapse>
    </div>
  );
}

/** Orca's child-workspace chip: one status per direct child, summarized from that child's whole subtree. */
function RepoChildToggle({ count, counts, folded, onToggle }: { count: number; counts: ChildStates; folded: boolean; onToggle: () => void }) {
  const descriptionId = useId();
  const parts = (["needs_you", "working", "done"] as const).filter((k) => counts[k] > 0);
  const description = parts.map((k) => stateCount(k, counts[k])).join(t("sidebar.list_separator"));
  const action = t(folded ? "sidebar.show_child_workspaces" : "sidebar.hide_child_workspaces", { count });
  return (
    <div className="@container relative mt-1 flex min-w-0 justify-start pl-7">
      <button
        type="button"
        aria-expanded={!folded}
        aria-label={action}
        aria-describedby={description ? descriptionId : undefined}
        title={description ? `${action}\n${description}` : action}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          onToggle();
        }}
        className="relative z-10 flex min-h-4 max-w-full items-center whitespace-nowrap rounded-[4px] border border-line bg-surface px-0.5 text-[10px] leading-none text-ink-3 shadow-hairline transition-colors hover:bg-hover hover:text-ink-2 focus-visible:bg-hover focus-visible:ring-2 focus-visible:ring-canvas"
      >
        <span className="inline-flex min-w-0 max-w-full flex-wrap items-center gap-x-1 gap-y-0.5 py-0.5">
          <span className="inline-flex shrink-0 items-center gap-1">
            <Icon name="workflow" size={10} stroke={2.2} className="shrink-0" />
            <span className={cx("tabular-nums", parts.length > 0 && "hidden @min-[13rem]:inline")}>{t("sidebar.child_worktree_count", { count })}</span>
            {parts.length > 0 && <span className="tabular-nums @min-[13rem]:hidden">{count}</span>}
          </span>
          <span className="inline-flex min-w-0 max-w-full items-center gap-0.5">
            {parts.length > 0 && (
              <span className="inline-flex min-w-0 flex-wrap items-center gap-0.5 border-l border-line pl-1 text-ink-3 @max-[13rem]:pl-0.5">
                {parts.map((k) => (
                  <span key={k} aria-hidden className="inline-flex shrink-0 items-center gap-0.5 tabular-nums @max-[13rem]:gap-0">
                    <StatusMark state={k === "done" ? "unread" : k} size="sm" label={false} />
                    {counts[k]}
                  </span>
                ))}
              </span>
            )}
            <Icon name="chevron" size={10} stroke={2.4} className="size-2.5 shrink-0 text-ink-3 transition-transform duration-200 motion-reduce:transition-none" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
          </span>
        </span>
      </button>
      {description && <span id={descriptionId} className="sr-only">{description}</span>}
    </div>
  );
}

function SpaceSection({ sp, machineName, reg, hover, activeKey, go, pinned, muted, onMenu, onRowMenu, onNavigate }: { sp: Space; machineName?: string; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; pinned: boolean; muted: boolean; onMenu: SpaceMenuFn; onRowMenu: RowMenuFn; onNavigate?: () => void }) {
  const mutes = useStore((st) => st.mutes);
  const allIdle = sp.sessions.every((s) => isQuiet(s, mutes));
  const [folded, toggle] = useFolded(`space:${sp.key}`, allIdle && sp.sessions.length > 3, { holds: holdsRow(sp.sessions, activeKey) });

  return (
    <Section
      folded={folded}
      onToggle={toggle}
      onMenu={(at, el) => onMenu(sp, el, at, { folded, toggle })}
      title={
        <span className="flex items-center gap-1.5">
          <span className="truncate normal-case tracking-normal text-[11.5px] text-ink-2">{sp.name}</span>
          <SpaceMarks pinned={pinned} muted={muted} />
          {machineName && <span className="truncate normal-case tracking-normal text-[11px] font-normal text-ink-3">{machineName}</span>}
        </span>
      }
      right={
        <>
          <span className={cx("flex items-center gap-1", MENU_STEP_ASIDE, MENU_TOUCH_ROOM)}>
            <SpaceTodoTag sp={sp} onNavigate={onNavigate} />
            <span className={MENU_HIDES}><SessionTally sessions={sp.sessions} folded={folded} /></span>
          </span>
          <SpaceMenuButton sp={sp} onMenu={onMenu} />
        </>
      }
    >
      {sp.sessions.map((s) => (
        <SessionRow key={s.session_id} s={s} k={`s:${s.machine_id}/${s.session_id}`} reg={reg} hover={hover} active={activeKey === `s:${s.machine_id}/${s.session_id}`} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} tab showTodoTag onMenu={onRowMenu} />
      ))}
    </Section>
  );
}

function SpaceMarks({ pinned, muted }: { pinned: boolean; muted: boolean }) {
  return (
    <>
      {pinned && <span title={t("sidebar.pinned")} className="shrink-0 text-accent"><Icon name="pin" size={10} stroke={2.2} /><span className="sr-only">{t("sidebar.pinned")}</span></span>}
      {muted && <span title={t("sidebar.muted")} className="shrink-0 text-ink-3"><Icon name="bellOff" size={10} stroke={2.2} /><span className="sr-only">{t("sidebar.muted")}</span></span>}
    </>
  );
}

// The todo a space hangs on (docs/assistant/todos.md), when the hub supports both: a tag that opens it.
function SpaceTodoTag({ sp, onNavigate }: { sp: Space; onNavigate?: () => void }) {
  const todos = useStore((st) => st.todos);
  const todosSupported = useStore((st) => st.todosSupported);
  const todosLoaded = useStore((st) => st.todosLoaded);
  const spacesSupported = useStore((st) => st.spacesSupported);
  const supportSpaces = todosLoaded && todosSupported !== false && spacesSupported !== false;
  const linkedTodo = supportSpaces ? findTodoForSpace(todos, sp.machineId, sp.name) : undefined;
  if (!linkedTodo) return null;
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        openTodoFromTag(linkedTodo.id, navigate, onNavigate);
      }}
      className="relative z-10 flex shrink-0 items-center justify-center rounded bg-hover px-1 py-0.5 text-ink-3 hover:bg-hover-2 hover:text-ink transition-colors cursor-pointer"
      title={t("sidebar.todo_tag", { title: linkedTodo.title })}
      aria-label={t("sidebar.todo_tag", { title: linkedTodo.title })}
    >
      <Icon name="todo" size={10} stroke={2} />
    </button>
  );
}

// A header row's ⋯ lies over the row's right end (absolute, so it reserves no width and every row's count keeps the
// same right edge, the machine row's, which has none). With a pointer it shows on hover or keyboard focus and the count
// under it steps aside (MENU_HIDES) with whatever sits beside the count (MENU_STEP_ASIDE); on a touch screen it is
// always there and the row keeps room for it (MENU_TOUCH_ROOM here, the 44px the tree's headers reserve).
// (Written out in full: Tailwind finds class names by scanning the source for them.)
const MENU_HIDES = "group-hover/sec:invisible group-has-[[data-row-menu]:focus-visible]/sec:invisible";
const MENU_STEP_ASIDE = "group-hover/sec:-translate-x-5 group-has-[[data-row-menu]:focus-visible]/sec:-translate-x-5";
const MENU_TOUCH_ROOM = "[@media(hover:none)]:mr-9";

// A space's ⋯ (rename, pin, mute, mark seen, link a todo): on hover with a pointer, always on a touch screen.
function SpaceMenuButton({ sp, onMenu }: { sp: Space; onMenu: (sp: Space, anchor: HTMLElement) => void }) {
  return (
    <span className="absolute top-1/2 right-2 z-10 -translate-y-1/2">
      <button
        type="button"
        data-row-menu
        aria-label={t("sidebar.space_actions_named", { name: sp.name })}
        aria-haspopup="menu"
        title={t("sidebar.space_actions")}
        onClick={(e) => onMenu(sp, e.currentTarget)}
        className="touch-hit relative flex size-5 items-center justify-center rounded-[5px] text-ink-3 opacity-0 transition-opacity duration-150 group-hover/sec:opacity-100 hover:bg-hover-2 hover:text-ink focus-visible:opacity-100 [@media(hover:none)]:size-8 [@media(hover:none)]:opacity-100"
      >
        <Icon name="more" size={12} />
      </button>
    </span>
  );
}

type SpaceMenuFn = (sp: Space, anchor: HTMLElement, at?: MenuAt, fold?: MenuNode["fold"]) => void;
type TreeProps = { machines: Machine[]; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; mutes: AgentMutes; onSpaceMenu: SpaceMenuFn; onRowMenu: RowMenuFn; onNodeMenu: NodeMenuFn; onNavigate?: () => void };

// "By machine" (lib/herdr-tree.ts): one machine as herdr lays it out, its workspaces, their tabs, then "Other". A machine that
// is away starts folded; its agents are under "Recent (not running)".
function MachineSection({ m, shells, ...p }: { m: HerdrMachine; shells?: TermSpace[] } & TreeProps) {
  const online = m.machine.online;
  const [folded, toggle] = useFolded(m.foldKey, !online, { holds: holdsRow(m.sessions, p.activeKey) });
  const name = machineWithHub(m.machine.id, p.machines);
  return (
    <div className="mt-3">
      <div className="group/sec flex items-center gap-1 px-2 pb-0.5 [@media(hover:none)]:pr-11" {...contextMenuProps((at) => p.onNodeMenu(machineNode(m.machine, { fold: { folded, toggle } }), at, name))}>
        <button type="button" aria-expanded={!folded} onClick={toggle} className="relative z-10 flex min-h-7 min-w-0 flex-1 items-center gap-1.5 whitespace-nowrap text-left [@media(pointer:coarse)]:min-h-11">
          <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", online ? "bg-green" : "bg-line-strong")} />
          <span className={cx("min-w-0 truncate text-[12.5px] font-semibold", online ? "text-ink" : "text-ink-3")}>{machineWithHub(m.machine.id, p.machines)}</span>
          <span className="sr-only">{online ? t("sidebar.online") : neverConnected(m.machine) ? t("sidebar.not_connected_yet") : t("sidebar.offline")}</span>
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 text-ink-3 opacity-0 transition-[opacity,transform] duration-200 group-hover/sec:opacity-100 [@media(hover:none)]:opacity-100" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        </button>
        {m.needs > 0 && !(folded && online) ? (
          <span title={stateCount("needs_you", m.needs)} className="flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-field px-1 text-[10.5px] font-semibold text-attention tabular-nums">
            {m.needs}<span className="sr-only"> {t("sidebar.needs_you_suffix", { count: m.needs })}</span>
          </span>
        ) : !online ? (
          <span className="shrink-0 text-[10.5px] whitespace-nowrap text-ink-3">{neverConnected(m.machine) ? t("sidebar.not_connected_yet") : t("sidebar.offline")}</span>
        ) : (
          <SessionTally sessions={m.sessions} folded={folded} />
        )}
      </div>
      <Collapse open={!folded}>
        <div className="flex flex-col gap-px" inert={folded || undefined}>
          {m.spaces.map((sp) => <WorkspaceBlock key={sp.key} sp={sp} {...p} />)}
          {shells && shells.length > 0 && <ShellTree mid={m.machine.id} online={online} spaces={shells} {...p} />}
          {m.other.length > 0 && (
            // "Other": the machine's agents outside any workspace (Codex threads, runtimes without spaces)
            <TreeBlock foldKey={m.otherFoldKey} sessions={m.other} activeKey={p.activeKey} title={<span className="min-w-0 truncate font-medium">{t("sidebar.other")}</span>} right={(folded) => <SessionTally sessions={m.other} folded={folded} />}>
              {m.other.map((s) => <TreeSessionRow key={s.session_id} s={s} {...p} />)}
            </TreeBlock>
          )}
          {!m.sessions.length && <p className="truncate px-3 py-1 text-[12px] text-ink-3">{online ? t("sidebar.none_running") : t("sidebar.agents_in_recent")}</p>}
        </div>
      </Collapse>
    </div>
  );
}

// A workspace or "Other" in the tree: its title row folds what is under it, the fold kept by machine/space name.
function TreeBlock({ foldKey, sessions, activeKey, state, title, right, children, onMenu }: { foldKey: string; sessions: Session[]; activeKey: string; state?: AgentState; title: React.ReactNode; right: (folded: boolean) => React.ReactNode; children: React.ReactNode; onMenu?: (at: MenuAt, el: HTMLElement, fold: { folded: boolean; toggle: () => void }) => void }) {
  const mutes = useStore((st) => st.mutes);
  const [folded, toggle] = useFolded(foldKey, sessions.every((s) => isQuiet(s, mutes)) && sessions.length > 3, { holds: holdsRow(sessions, activeKey) });
  return (
    <div>
      <div className="group/sec relative flex items-center gap-1 pr-2 pl-2 [@media(hover:none)]:pr-11" {...(onMenu ? contextMenuProps((at, el) => onMenu(at, el, { folded, toggle })) : {})}>
        <button type="button" aria-expanded={!folded} onClick={toggle} className="relative z-10 flex min-h-7 min-w-0 flex-1 items-center gap-1.5 whitespace-nowrap text-left text-[12px] text-ink-2 hover:text-ink [@media(pointer:coarse)]:min-h-11">
          <Icon name="chevron" size={9} stroke={2.4} className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
          {state ? <StatusMark state={state} size="sm" /> : <span className="size-1.5 shrink-0" />}
          {title}
        </button>
        {right(folded)}
      </div>
      <Collapse open={!folded}>
        <div className="flex flex-col gap-px pl-3" inert={folded || undefined}>{children}</div>
      </Collapse>
    </div>
  );
}

function WorkspaceBlock({ sp, ...p }: { sp: HerdrSpace } & TreeProps) {
  const tabs = sp.tabs.length;
  return (
    <TreeBlock
      foldKey={sp.foldKey}
      sessions={sp.tree}
      activeKey={p.activeKey}
      state={sp.state}
      onMenu={(at, el, fold) => p.onSpaceMenu(sp, el, at, fold)}
      title={<><span className="min-w-0 truncate font-medium">{sp.name}</span><SpaceMarks pinned={sp.pinned} muted={isSpaceMuted(sp, p.mutes)} /></>}
      right={(folded) => (
        <>
          <span className={cx("flex shrink-0 items-center gap-1", MENU_STEP_ASIDE)}>
            <SpaceTodoTag sp={sp} onNavigate={p.onNavigate} />
            {sp.children.length > 0 && <ChildSummary counts={sp.childStates} />}
            <span className={MENU_HIDES}>
              {folded ? <SessionTally sessions={sp.tree} folded /> : tabs > 0 && !sp.children.length && (
                // agents, as the machine and the quick filter count them: a tab with several agents is one row (#2320)
                <span title={t("sidebar.space_tally", { count: sp.sessions.length, tabs })} className="text-[10.5px] text-ink-3 tabular-nums">{sp.sessions.length}<span className="sr-only"> {t("sidebar.agent_suffix", { count: sp.sessions.length })}</span></span>
              )}
            </span>
          </span>
          <SpaceMenuButton sp={sp} onMenu={p.onSpaceMenu} />
        </>
      )}
    >
      {sp.tabs.map((t) => <TabRow key={t.key} t={t} {...p} />)}
      {sp.loose.map((s) => <TreeSessionRow key={s.session_id} s={s} {...p} />)}
      {sp.children.length > 0 && <ChildSpaces sp={sp} {...p} />}
    </TreeBlock>
  );
}

// A space's child spaces (Orca's "N child spaces"), folded at first like Orca's sidebar: the parent's row already sums up
// what they are doing (ChildSummary), and the fold is kept by machine/space name like the space's own.
function ChildSpaces({ sp, ...p }: { sp: HerdrSpace } & TreeProps) {
  // i18n-ignore: a stored fold key, not interface text
  const [folded, toggle] = useFolded(`${sp.foldKey}/子项`, true, { holds: holdsRow(sp.tree.slice(sp.sessions.length), p.activeKey) });
  const n = sp.children.length;
  return (
    <div>
      <button type="button" aria-expanded={!folded} onClick={toggle} className="relative z-10 flex min-h-7 w-full min-w-0 items-center gap-1.5 pr-2 pl-2 text-left text-[11.5px] whitespace-nowrap text-ink-3 hover:text-ink [@media(pointer:coarse)]:min-h-11">
        <Icon name="chevron" size={9} stroke={2.4} className="shrink-0 transition-transform duration-200" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        <span className="min-w-0 flex-1 truncate">{t("sidebar.child_count", { count: n })}</span>
        {folded && <SessionTally sessions={sp.tree.slice(sp.sessions.length)} folded />}
      </button>
      <Collapse open={!folded}>
        <div className="flex flex-col gap-px" inert={folded || undefined}>
          {sp.children.map((c) => <WorkspaceBlock key={c.key} sp={c} {...p} />)}
        </div>
      </Collapse>
    </div>
  );
}

// What a parent space's children are doing, as Orca's parent row shows it: how many need you, work, are done.
function ChildSummary({ counts }: { counts: ChildStates }) {
  const parts = (["needs_you", "working", "done"] as const).filter((k) => counts[k] > 0);
  if (!parts.length) return null;
  return (
    <span title={t("sidebar.children_title", { list: parts.map((k) => stateCount(k, counts[k])).join(t("sidebar.list_separator")) })} className="flex items-center gap-1.5 text-[10.5px] text-ink-3 tabular-nums">
      {parts.map((k) => (
        <span key={k} className="flex items-center gap-0.5">
          <span aria-hidden className="flex"><StatusMark state={k === "done" ? "unread" : k} size="sm" /></span>
          {counts[k]}
          <span className="sr-only"> {t(`sidebar.child_sr_${k}`, { count: counts[k] })}</span>
        </span>
      ))}
    </span>
  );
}

function TreeSessionRow({ s, reg, hover, activeKey, go, onRowMenu }: { s: Session } & TreeProps) {
  const key = rowKey(s);
  return <SessionRow s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} showTodoTag onMenu={onRowMenu} />;
}

/** The menu node of a tab of the sidebar's tree: its most urgent pane stands for it. */
function tabNode(t: HerdrTab, machines: Machine[]): MenuNode {
  const s = t.lead;
  return {
    kind: "tab",
    mid: s.machine_id,
    online: machines.find((m) => m.id === s.machine_id)?.online !== false,
    host: s.space_kind,
    label: s.slot_name,
    workspaceId: t.spaceId,
    tabId: t.slotId,
    paneId: s.pane_id,
    tabSessionId: s.session_id,
    cwd: s.cwd,
  };
}

// One herdr tab: `#N` (or its name) and what its most urgent pane is doing; it opens on that pane, the others are the
// chat's pane tabs (PaneTabs). A tab with one pane has that agent's ⋯, one with several the tab's own. A tab with several
// agents (#2320) opens up to them, in pane order: the "Lead" (the first pane, the others split from it) on top, its
// branches under it; the choice is kept per tab like the sidebar's other folds, and folded it is the one row it always was.
function TabRow({ t: tab, ...p }: { t: HerdrTab } & TreeProps) {
  const { reg, hover, activeKey, go, onRowMenu, onNodeMenu, machines } = p;
  const n = tab.sessions.length;
  const single = n === 1;
  // open at first: the point is to see which agents belong together (#2320); a tab folded by hand stays folded
  const [folded, toggle] = useFolded(tab.foldKey, false);
  const shut = single || folded;
  const open = tab.sessions.find((x) => rowKey(x) === activeKey);
  // opened up, the open agent's own row is the one lit; folded, the tab's row is, and the hidden pane rows (still
  // mounted in the collapse) register under no key, so the highlight and the scroll to the open row find the tab's
  const keys = tabRowKeys(`t:${tab.key}`, tab.sessions.map(rowKey), activeKey, shut);
  const k = keys.row;
  const to: Route = { name: "tab", mid: tab.lead.machine_id, space: tab.spaceId, slot: tab.slotId };
  // a tab with several panes has a menu of its own: rename, split, git, close it. One function per tab (a new one each
  // render would re-render the lead row on every push, SessionRow's memo); a click reads the tab as it is now.
  const latest = useRef({ tab, machines });
  useLayoutEffect(() => {
    latest.current = { tab, machines };
  });
  const onTabMenu = useCallback<RowMenuFn>(
    (_s, anchor, at) => {
      const { tab: now, machines: ms } = latest.current;
      onNodeMenu(tabNode(now, ms), at ?? { anchor }, t("sidebar.tab_named", { label: now.label || "" }).trim(), !at);
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the tab is read from `latest`; its key names the tab
    [onNodeMenu, tab.key],
  );
  const row = (
    <SessionRow
      s={tab.lead}
      k={k}
      reg={reg}
      hover={hover}
      active={Boolean(open) && shut}
      to={to}
      go={go}
      // a long tab name gives way: it would run under the "N agents" button (endRoom) and hide the agent's title;
      // squeezed, it shrinks (truncated) before the title goes under its minimum (SessionRow)
      prefix={tab.label}
      prefixClass="min-w-0 max-w-[50%] shrink truncate text-[11.5px] text-ink-3 tabular-nums"
      showTodoTag={single}
      onMenu={single ? onRowMenu : onTabMenu}
      menuLabel={single ? undefined : t("sidebar.tab_actions")}
      endRoom={!single}
    />
  );
  if (single) return row;
  return (
    <div>
      <div className="relative">
        {row}
        <button
          type="button"
          aria-expanded={!folded}
          aria-label={tab.label ? t("sidebar.tab_agents_named", { label: tab.label, count: n }) : t("sidebar.agent_count", { count: n })}
          title={folded ? t("sidebar.tab_expand") : t("sidebar.show_less")}
          onClick={toggle}
          // left of the tab's own ⋯ (24 px, 44 on touch), which sits at the row's end
          className="touch-hit absolute top-1/2 right-8 z-20 flex h-6 -translate-y-1/2 items-center gap-0.5 whitespace-nowrap rounded-[6px] pr-0.5 pl-1 text-[11px] text-ink-3 tabular-nums hover:bg-hover hover:text-ink-2 [@media(hover:none)]:right-11"
        >
          {t("sidebar.agent_count", { count: n })}
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 transition-transform duration-200" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        </button>
      </div>
      <Collapse open={!folded}>
        <div className="flex flex-col gap-px pl-3" inert={folded || undefined}>
          {tab.sessions.map((s, i) => {
            const key = rowKey(s);
            const branch = Boolean(tab.launcher) && s !== tab.launcher;
            const paneKey = keys.panes[i];
            const pane = (
              <SessionRow
                key={s.session_id}
                s={s}
                k={paneKey ?? key}
                reg={paneKey ? reg : unregistered}
                hover={hover}
                active={activeKey === key}
                to={paneRoute(s)}
                go={go}
                prefix={agentLabel(s)}
                prefixClass="min-w-0 shrink truncate text-[11px] text-ink-3"
                host={s === tab.launcher ? t("sidebar.launcher") : undefined}
                showTodoTag
                onMenu={onRowMenu}
              />
            );
            // the branches hang off the "Lead" on a thin line
            return branch ? <div key={s.session_id} className="ml-[13px] border-l border-line pl-1">{pane}</div> : pane;
          })}
        </div>
      </Collapse>
    </div>
  );
}

// "Terminal" under a machine (Settings › Appearance › Show terminals in sidebar): its terminal host's workspaces and the plain shells in them, each
// opening the "Terminal" page on that pane. Folds are kept by machine/workspace name like the Agents' above.
function ShellTree({ mid, online, spaces, reg, hover, go, activeKey, onNodeMenu }: { mid: string; online: boolean; spaces: TermSpace[] } & TreeProps) {
  const route = useRoute();
  const shown = route.name === "terminal" && route.mid === mid ? route.id : undefined;
  const sessions = useStore((s) => s.sessions[mid]);
  const count = spaces.reduce((n, sp) => n + sp.tabs.reduce((m, t) => m + t.panes.length, 0), 0);
  return (
    <TreeBlock foldKey={`term:${mid}`} sessions={[]} activeKey={activeKey} title={<span className="min-w-0 truncate font-medium">{t("sidebar.nav_terminal")}</span>} right={() => <GroupCount n={count} label={t("sidebar.pane_count", { count })} />}>
      {spaces.map((sp) => (
        <TreeBlock
          key={`${sp.host}/${sp.id}`}
          foldKey={`term:${mid}/${sp.host}:${sp.label}`}
          sessions={[]}
          activeKey={activeKey}
          title={<span className="min-w-0 truncate font-medium">{sp.label}</span>}
          right={() => null}
          onMenu={(at) => onNodeMenu(termSpaceNode(mid, sp, online), at, t("sidebar.space_named", { name: sp.label }))}
        >
          {sp.tabs.flatMap((tab) =>
            tab.panes.map((p) => {
              const k = `term:${mid}/${p.id}`;
              const node = tab.panes.length === 1 ? termTabNode(mid, sp, tab, online) : termPaneNode(mid, sp, tab, p, online);
              return (
                <NavRow
                  key={k}
                  k={k}
                  reg={reg}
                  hover={hover}
                  active={shown === p.id}
                  to={terminalTarget(mid, p.id, sessions ?? [])}
                  go={go}
                  icon="terminal"
                  label={tab.panes.length > 1 ? `${tab.label} · ${p.title}` : p.title}
                  title={p.detail ? `${p.title}\n${p.detail}` : p.title}
                  menuLabel={t("sidebar.terminal_actions")}
                  onMenu={(at, _el, button) => onNodeMenu(node, at, tab.label, button)}
                />
              );
            }),
          )}
        </TreeBlock>
      ))}
    </TreeBlock>
  );
}

// Agents outside any space (Codex sessions and the like): folds like a space, folded at first when it is long and
// nothing in it wants a look. The app lists them as a space of their machine.
function LooseSection({ sessions, reg, hover, activeKey, go, onRowMenu }: { sessions: Session[]; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; onRowMenu: RowMenuFn }) {
  const mutes = useStore((st) => st.mutes);
  const [folded, toggle] = useFolded("loose", sessions.every((s) => isQuiet(s, mutes)) && sessions.length > 3, { holds: holdsRow(sessions, activeKey) });
  return (
    <Section title={t("sidebar.other_agents")} folded={folded} onToggle={toggle} right={<SessionTally sessions={sessions} folded={folded} />}>
      {sessions.map((s) => (
        <SessionRow key={s.session_id} s={s} k={`s:${s.machine_id}/${s.session_id}`} reg={reg} hover={hover} active={activeKey === `s:${s.machine_id}/${s.session_id}`} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} onMenu={onRowMenu} />
      ))}
    </Section>
  );
}

// A group's count; folded, the states that want a look instead (a mark and a count each, needs you first, #3559), so a
// folded group doesn't hide them.
function SessionTally({ sessions, folded }: { sessions: Session[]; folded: boolean }) {
  const counts = foldedCounts(sessions);
  if (folded && hasFoldedCounts(counts)) return <StateTally counts={counts} />;
  return <GroupCount n={sessions.length} label={t("sidebar.agent_count", { count: sessions.length })} />;
}

/** A group title's count with what it counts: a tooltip, and the same words for a screen reader. */
function GroupCount({ n, label, className }: { n: number; label: string; className?: string }) {
  return (
    <span title={label} className={cx("text-[10.5px] text-ink-3 tabular-nums", className)}>
      <span aria-hidden>{n}</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}

type RegFn = (k: string) => (el: HTMLElement | null) => void;
/** For a row that is there but hidden (a folded tab's panes): it registers nowhere. */
const unregistered: RegFn = () => () => {};
/** An agent row's key in the sidebar (highlight, active): sidebarActiveKey's for a session. */
const rowKey = (s: Pick<Session, "machine_id" | "session_id">) => `s:${agentKey(s)}`;
/** Whether the open row (`activeKey`) is one of these agents': its group opens for it (useFolded). */
const holdsRow = (sessions: readonly Session[], activeKey: string) => sessions.some((s) => rowKey(s) === activeKey);
type GoFn = (to: Route) => (e: React.MouseEvent) => void;
/** `at`: where a right click or the menu key opened it; without it (the ⋯) the row's menu toggles beside its button.
 *  A row's ⋯ lies beside its link, not in it, and gives as `anchor` its parent, the box holding both: the menu
 *  closes on a pointerdown outside its anchor, and a press on the ⋯ has to stay inside it for the click that
 *  follows to toggle. */
type RowMenuFn = (s: Session, anchor: HTMLElement, at?: MenuAt) => void;
/** A machine's, a workspace's or a tab's menu (src/lib/node-actions.ts); `toggle`: from its ⋯, which closes it again
 *  (a row's ⋯ anchors it on the box holding the row and the ⋯, as in RowMenuFn). */
type NodeMenuFn = (node: MenuNode, at: MenuAt, header?: string, toggle?: boolean) => void;

// Persona's main conversation: the column's first and strongest entry (the app's mark, always in ink, #2004), with the
// conversation's state when it is working or wants you. A line under it parts it from the pages.
// The small "Main chat" under the name keeps the brand row readable as a conversation, not a heading (#6293); a draft
// waiting in the box takes that line (#5568).
// Its menu (Copy session ID, End…: iOS's long press on the main row) opens on a right click, the menu key or its ⋯.
// Memoized like SessionRow: a push that leaves the main conversation as it was does not render it again.
export const PersonaRow = memo(function PersonaRow({ reg, hover, active, go, main, chord, onMenu: menuOf }: { reg: RegFn; hover: (k: string | null) => void; active: boolean; go: GoFn; main?: Session; /** The "Open Persona" shortcut as bound now ("": none). */ chord?: string; /** The conversation rows' menu, opened on `main`: none without it. */ onMenu?: RowMenuFn }) {
  const to: Route = { name: "persona" };
  const st = main ? stateOf(main) : undefined;
  const pending = main?.pending_interactions ?? 0;
  const onMenu = main && menuOf ? (anchor: HTMLElement, at?: MenuAt) => menuOf(main, anchor, at) : undefined;
  // one ref for the row's life, so a re-render does not let go of it and register it again (useGlide's reg)
  const ref = useMemo(() => reg("persona"), [reg]);
  const aside = onMenu && !active ? "group-hover/row:invisible" : undefined;
  // words left unsent in the main conversation's box, once you are not in it (#5568)
  const draft = draftLine(useLeftDraft(main ? `${main.machine_id}/${main.session_id}` : null));
  const link = (
    <a
      ref={ref}
      href={href(to)}
      onClick={go(to)}
      {...(onMenu ? contextMenuProps((at, el) => onMenu(el, at)) : {})}
      onMouseEnter={() => hover("persona")}
      onFocus={() => hover("persona")}
      onBlur={() => hover(null)}
      aria-current={active ? "page" : undefined}
      aria-keyshortcuts={chord ? ariaKeyshortcuts(chord) : undefined}
      title={t("sidebar.persona_main")}
      // pinned over the list (#5581), its own hover fill: the gliding highlight passes under the pinned box
      className={cx("relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row transition-[background-color,transform] duration-150 active:scale-[0.98] [@media(pointer:coarse)]:min-h-11", active ? CURRENT_ROW : "hover:bg-hover focus-visible:bg-hover", onMenu && active && "pr-8", onMenu && "[@media(hover:none)]:pr-11")}
    >
      <AppMark size={18} radius={5} className="shadow-hairline" />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-[13.5px] font-semibold text-ink">Persona</span>
        {draft ? <SecondLine line={draft} draft /> : <SecondLine line={{ text: t("sidebar.main_chat") }} draft={false} />}
      </span>
      {chord && <span aria-hidden data-persona-chord className={cx("hidden sm:inline-flex [@media(hover:none)]:hidden", aside)}><Kbd>{formatChord(chord)}</Kbd></span>}
      {pending > 0 ? (
        <span className={cx("flex h-4.5 min-w-4.5 items-center justify-center rounded-full bg-field px-1 text-[10.5px] font-semibold text-attention tabular-nums", aside)}>{pending}</span>
      ) : st === "working" || st === "needs_you" || st === "unread" ? (
        <span className={cx("flex", aside)}><StatusMark state={st} /></span>
      ) : null}
    </a>
  );
  if (!onMenu) return link;
  return (
    <div className="group/row relative">
      {link}
      <button
        type="button"
        aria-label={t("conversation-menu.actions")}
        aria-haspopup="menu"
        title={t("sidebar.more")}
        tabIndex={-1}
        onClick={(e) => onMenu(e.currentTarget.parentElement ?? e.currentTarget)}
        className={cx("absolute top-1/2 right-1 z-20 size-6 -translate-y-1/2 items-center justify-center rounded-[6px] text-ink-3 hover:text-ink group-hover/row:flex [@media(hover:none)]:right-0 [@media(hover:none)]:flex [@media(hover:none)]:size-11 [@media(hover:none)]:bg-transparent", active ? "flex hover:bg-hover" : "hidden bg-hover hover:bg-hover-2")}
      >
        <Icon name="more" size={14} />
      </button>
    </div>
  );
});

// The "Pinned" heading above the pinned conversations (iOS's pinned heading, Android's pinned section): its ⋯ and a right
// click on it give "Unpin all", which a touch screen reaches through the ⋯.
export function PinnedHeading({ onMenu }: { onMenu: (at: MenuAt) => void }) {
  return (
    <div className="flex items-center gap-1 px-2 pt-0.5 pb-0.5" {...contextMenuProps((at) => onMenu(at))}>
      <span className="flex min-w-0 flex-1 items-center gap-1 truncate whitespace-nowrap text-[11px] font-semibold text-ink-3">
        <Icon name="pin" size={10} stroke={2.2} className="shrink-0" />
        {t("sidebar.pinned_section")}
      </span>
      <button
        type="button"
        aria-label={t("conversation-menu.unpin_all")}
        aria-haspopup="menu"
        title={t("sidebar.more")}
        onClick={(e) => onMenu({ anchor: e.currentTarget })}
        className="touch-hit relative z-10 flex size-5 shrink-0 items-center justify-center rounded-[5px] text-ink-3 hover:bg-hover hover:text-ink-2"
      >
        <Icon name="more" size={12} />
      </button>
    </div>
  );
}

// A section's "+" (a new conversation, a new agent): quiet, next to the section's title.
/** A small button at the end of a section's heading: "+" (add), or another page of the section (`icon`). */
function SectionAdd({ label, to, go, onClick, icon: name = "plus" }: { label: string; to?: Route; go?: GoFn; onClick?: () => void; icon?: IconName }) {
  const cls = "touch-hit relative z-10 flex size-5 shrink-0 items-center justify-center rounded-[5px] text-ink-3 hover:bg-hover hover:text-ink-2";
  const icon = <Icon name={name} size={12} stroke={2} />;
  if (to && go) return <a href={href(to)} onClick={go(to)} title={label} aria-label={label} className={cls}>{icon}</a>;
  return <button type="button" onClick={onClick} title={label} aria-label={label} className={cls}>{icon}</button>;
}

const BADGE_CLASS = { attention: "bg-field text-attention", red: "bg-red-tint text-red", orange: "bg-orange-tint text-orange", green: "bg-green-tint text-green", accent: "bg-accent-tint text-accent-ink" } as const;

type NavRowProps = { onMenu?: (at: MenuAt, el: HTMLElement, button: boolean) => void; menuLabel?: string; k: string; reg: RegFn; hover: (k: string | null) => void; active: boolean; to: Route; go: GoFn; icon: IconName; label: string; badge?: number | string; badgeTone?: keyof typeof BADGE_CLASS; hint?: string; title?: string; /** A state shown as a dot before the hint ("Automatic"'s rows: some run). */ dot?: AgentState };

// The pages take nothing from the sessions, yet every sessions push re-renders the sidebar: memoized like SessionRow
// (sameProps), a page row renders again only when its own props change (a badge, the highlight).
const NavRow = memo(function NavRow({ k, reg, hover, active, to, go, icon, label, badge, badgeTone, hint, title, onMenu, menuLabel, dot }: NavRowProps) {
  // one ref for the row's life, so a re-render does not let go of it and register it again (useGlide's reg)
  const ref = useMemo(() => reg(k), [reg, k]);
  const link = (
    <a
      ref={ref}
      {...(onMenu ? contextMenuProps((at, el) => onMenu(at, el, false)) : {})}
      href={href(to)}
      title={title}
      onClick={go(to)}
      onMouseEnter={() => hover(k)}
      onFocus={() => hover(k)}
      onBlur={() => hover(null)}
      aria-current={active ? "page" : undefined}
      className={cx("relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row transition-transform duration-150 active:scale-[0.98] [@media(pointer:coarse)]:min-h-11", active && CURRENT_ROW)}
    >
      <span className={active ? "text-ink" : "text-ink-3"}>
        <Icon name={icon} size={14} stroke={1.8} />
      </span>
      <span className={cx("min-w-0 flex-1 truncate text-[13px]", active ? "font-medium text-ink" : "text-ink-2")}>{label}</span>
      {dot && <StatusMark state={dot} size="sm" />}
      {hint && <span className="text-[11px] text-ink-3 tabular-nums">{hint}</span>}
      {badge !== undefined && (
        <span className={cx("flex h-4.5 min-w-4.5 shrink-0 items-center justify-center whitespace-nowrap rounded-full px-1 text-[10.5px] font-semibold tabular-nums", BADGE_CLASS[badgeTone ?? "accent"])} style={{ animation: `pop-in 250ms ${EASE} both` }} key={badge}>
          {badge}
        </span>
      )}
    </a>
  );
  if (!onMenu) return link;
  // the ⋯ shows over the row's end on hover (a pointer); on a touch screen it stays, in the room the row leaves for it
  return (
    <div className="group/row relative">
      {link}
      <button
        type="button"
        aria-label={menuLabel}
        aria-haspopup="menu"
        title={t("sidebar.more")}
        tabIndex={-1}
        onClick={(e) => {
          const box = e.currentTarget.parentElement ?? e.currentTarget;
          onMenu({ anchor: box }, box, true);
        }}
        className="absolute top-1/2 right-1 z-20 hidden size-6 -translate-y-1/2 items-center justify-center rounded-[6px] bg-hover text-ink-3 hover:bg-hover-2 hover:text-ink group-hover/row:flex [@media(hover:none)]:right-0 [@media(hover:none)]:flex [@media(hover:none)]:size-11 [@media(hover:none)]:bg-transparent"
      >
        <Icon name="more" size={14} />
      </button>
    </div>
  );
}, sameProps);

// two shapes as well as two colors, so they tell apart without color: a bare prompt (no frame) and a window with a title bar
const SOURCE_ICON = { terminal: { name: "run", tone: "text-source-terminal" }, desktop: { name: "appWindow", tone: "text-source-desktop" } } as const;

/** Where the agent was opened (#2526), as a mark: a terminal or a desktop app's window in its own color, Lark's mark for a
 *  Lark group (a click opens its Feishu topic). Nothing for other sources. Its name is the tooltip and what a screen reader
 *  reads. At the row's end, so the title starts where an unmarked one does (#3043). */
export function SourceTag({ s, className }: { s: Session; className?: string }) {
  const source = sessionSource(s);
  if (!source) return null;
  const label = sourceName(source);
  const mark = source === "feishu" ? <BrandMark brand="lark" size={16} fit="badge" /> : <Icon name={SOURCE_ICON[source].name} size={14} className={SOURCE_ICON[source].tone} />;
  const cls = cx("flex size-4 shrink-0 items-center justify-center", className);
  if (source === "feishu" && s.external_url) {
    // a link inside the row's link: opened by hand, so the row does not navigate too
    const open = (e: React.SyntheticEvent) => {
      e.preventDefault();
      e.stopPropagation();
      window.open(s.external_url, "_blank", "noopener");
    };
    const title = t("lib-agent-groups.open_feishu_topic");
    return (
      <span data-source={source} role="link" tabIndex={0} title={`${label} · ${title}`} aria-label={`${label} · ${title}`} onClick={open} onKeyDown={(e) => e.key === "Enter" && open(e)} className={cx(cls, "cursor-pointer rounded-[4px] hover:bg-hover-2")}>
        {mark}
      </span>
    );
  }
  return <span data-source={source} role="img" title={label} aria-label={label} className={cls}>{mark}</span>;
}

/** A row's line under its title (rowLine, or draftLine): the red lead, then the words. */
function SecondLine({ line, draft }: { line: { lead?: string; text: string; tone?: "needs" | "reply" }; draft: boolean }) {
  return (
    <span data-row-line data-draft={draft ? "" : undefined} className="truncate text-[11.5px] leading-4 text-ink-3">
      {line.lead && <span className={cx("font-medium", line.tone === "reply" ? "text-green" : "text-attention")}>{line.lead}</span>}
      {line.text}
    </span>
  );
}

// `prefix`: a word before the title (a tab's name, a pane's agent), in `prefixClass`
type SessionRowProps = { menuLabel?: string; /** The mark at the start when the state is not shown (a Persona conversation's; pinned: the pin). */ icon?: IconName; s: Session; k: string; reg: RegFn; hover: (k: string | null) => void; active: boolean; to: Route; go: GoFn; tab?: boolean; label?: string; prefix?: string; prefixClass?: string; hint?: string; host?: string; showState?: boolean; showTodoTag?: boolean; onMenu?: RowMenuFn; endRoom?: boolean };

// Every sessions push re-renders the sidebar and the Agents list, while lib/sessions-delta.ts keeps the object of each
// session that did not change: a row renders again only when one of its props did. The props compare by identity
// (`go` and the menus are stable), except the route, which the call sites build anew and which is its path (routePath).
function sameProps<P extends { to: Route }>(a: P, b: P): boolean {
  const keys = Object.keys(a) as (keyof P)[];
  if (keys.length !== Object.keys(b).length) return false;
  return keys.every((key) => (key === "to" ? routePath(a.to) === routePath(b.to) : Object.is(a[key], b[key])));
}

export const SessionRow = memo(function SessionRow({ s: given, k, reg, hover, active, to, go, tab, label, prefix, prefixClass, hint, host, showState = true, showTodoTag, onMenu, menuLabel = t("sidebar.agent_actions"), endRoom, icon = "chat" }: SessionRowProps) {
  // what it asks you, or what it did, under its title (#3195): no hover or click needed to see why it is red or green
  const ix = useStore((st) => st.interactions.find((i) => i.machine_id === given.machine_id && i.session_id === given.session_id));
  // a card answered here takes its red dot and "Asking you" line away at once, not at the hub's next push (#4286)
  const justAnswered = useStore((st) => lastAnsweredAt(st.answered, given.machine_id, given.session_id));
  const pending = useStore((st) => answerPending(st.answered, given.machine_id, given.session_id));
  // seen on this page and not yet recorded by the hub (lib/seen-outbox.ts): the paged list's rows come from the list
  // itself, not the store, so the row holds it seen too
  const seenMark = useStore((st) => st.seen[runtimeIdentity(given)]);
  const s = heldSeen(answeredSession(given, Boolean(ix), justAnswered, pending), seenMark, Date.now());
  const st = stateOf(s);
  // Awaiting Reply (#5096): seen, but it asked you something you have not answered
  const awaiting = useStore((x) => awaitsReply(s, x.mutes));
  // where it was opened, marked at the row's end: not on a tab with several panes (endRoom), where a herdr tab is a
  // terminal by definition, each pane's row under it says so, and the mark's room is the title's
  const source = showState && !s.assistant && !endRoom ? sessionSource(s) : undefined;
  // a desktop app's runtime that can be written to: its mark says where it is and the agent's mark which agent, so the
  // runtime's name at the end only repeats them (it stays in the tooltip); a read-only one still says why
  const sameAsMarks = source === "desktop" && s.source_kind === "runtime" && sessionCan(s, "send");
  const tail = [sameAsMarks ? "" : sourceSubtitle(s), hint ?? (tab ? tabLabel(s) : "")].filter(Boolean).join(" · ");
  const preview = sessionPreview(s);
  // Settings › Appearance › show details on agent rows (#2430): a second line under an agent's title — folder · model · context · age
  const details = useAgentDetails();
  const contextUsed = useContextAsUsed();
  const withDetail = details && showState && !s.assistant;
  // the age at the row's end (an offline or stateless row), in the details line, or a background workflow's progress
  // (#2935) moves on with the clock: the memo keeps an unchanged row from re-rendering on a push, so a minute tick
  // does it, and only in a row that shows one of them. The ages read the clock when the row renders: `now` is the
  // mount time until the first tick, and a row may only start showing an age long after it mounted
  const showsAge = (!tail && (st === "offline" || !showState)) || (withDetail && !!s.updated_at);
  const now = useNow(60_000, showsAge || runningWorkflows(s).length > 0 || s.turn_error_kind === "usage_limit");
  const detail = withDetail ? rowDetail(s, contextUsed) : [];
  const workflow = showState && !s.assistant && st !== "offline" ? workflowShortLine(s) : "";
  const workflowTitle = workflow ? workflowLine(s, now) : "";
  // its own task list (#5302): 3/5 · the step in progress
  const plan = showState && !s.assistant && st !== "offline" && planInRow(s, st === "working") ? planLine(s) : "";
  // what sits at the row's end steps aside for the hover ⋯; the open row leaves room for its ⋯ instead
  const aside = active ? undefined : "group-hover/row:invisible";
  // behind a prefix (a tab's name, a pane's agent) the title keeps room for a few characters: the prefix shrinks first
  const titleMin = prefix ? ROW_TITLE_MIN : "min-w-0";
  // with words at the end (where it came from, a hint) the title keeps its own width first: those words give way before it does
  const titleFirst = tail ? "basis-auto" : undefined;
  // one ref per key, so letting go takes out only this row's entry (keyedRefs)
  const regRow = useMemo(() => reg(k), [reg, k]);
  // words left unsent in its box, once you are not in it (#5568): in place of what it asks or did
  const draft = draftLine(useLeftDraft(`${given.machine_id}/${given.session_id}`));
  const line = draft ?? (showState && !s.assistant ? rowLine(s, ix, awaiting) : undefined);
  // ultracode on (#3428): the workflow mark in the AI colour right after the title; a row without it is drawn as before.
  // Not on an offline row: the node keeps the last value it saw after the process is gone, and a resume starts it off.
  const ultra = showState && ultracodeMarkShown(s);
  const link = (
    <a
      ref={regRow}
      href={href(to)}
      onClick={(event) => { rememberSessionRow(given); go(to)(event); }}
      // right click (at the pointer), the menu key and Shift+F10 (beside the row): the row's menu, the same as its ⋯
      {...(onMenu ? contextMenuProps((at, el) => onMenu(s, el, at)) : {})}
      onMouseEnter={() => hover(k)}
      onFocus={() => hover(k)}
      onBlur={() => hover(null)}
      aria-current={active ? "page" : undefined}
      // its mark's name is in the row's tooltip too: the mark steps aside while the row is hovered
      title={`${sessionTitle(s)}${source ? `\n${sourceName(source)}` : ""}${sourceSubtitle(s) ? `\n${sourceSubtitle(s)}` : ""}${hint ? `\n${hint}` : ""}${s.todo ? `\n${t("sidebar.todo_tag", { title: s.todo.title })}` : ""}${preview ? `\n${preview}` : ""}`}
      // a touch screen: rows a finger can hit (44), and room at the end for the ⋯ that is always there; the open row
      // keeps its ⋯ too, so it has room for it
      className={cx("relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row transition-transform duration-150 active:scale-[0.98] [@media(pointer:coarse)]:min-h-11", active && CURRENT_ROW, // room for "N agents" and the ⋯ beside it (instead of the ⋯'s own)
        endRoom ? "pr-[108px] [@media(hover:none)]:pr-[120px]" : cx(onMenu && active && "pr-8", onMenu && "[@media(hover:none)]:pr-11"))}
    >
      {/* the agent's mark before its state (#6390): what runs there, then how it is doing */}
      {showState && !s.assistant && <SessionMark s={s} size={14} />}
      <span className="flex size-3.5 shrink-0 items-center justify-center">{showState || st === "working" ? <StatusMark state={usageLimitLabel(s) && st === "unread" ? "idle" : st} awaiting={awaiting} waiting={backgroundWaiting(s)} /> : <Icon name={icon} size={12} className="text-ink-3" />}</span>
      {prefix && <span className={prefixClass}>{prefix}</span>}
      {/* with the mark, the title sits in the column whether a second line shows or not: the same element through a
          workflow's start and end, so the mark does not fade in again */}
      {ultra || detail.length > 0 || workflow || line || plan ? (
        <span className={cx("flex flex-1 flex-col", titleMin, titleFirst)}>
          {ultra ? (
            <span className="flex min-w-0 items-center gap-1">
              <span className={cx("min-w-0 truncate text-[13px]", active ? "font-medium text-ink" : "text-ink-2", st === "unread" && !active && !usageLimitLabel(s) && "font-medium text-ink")}>
                {label ?? sessionTitle(s)}
              </span>
              <UltraMark size={10} />
            </span>
          ) : (
            <span className={cx("truncate text-[13px]", active ? "font-medium text-ink" : st === "offline" ? "text-ink-3" : "text-ink-2", st === "unread" && !active && !usageLimitLabel(s) && "font-medium text-ink")}>
              {label ?? sessionTitle(s)}
            </span>
          )}
          {line && <SecondLine line={line} draft={!!draft} />}
          {plan && (
            <span data-plan-line title={planLabel(s)} aria-label={planLabel(s)} className="flex h-4 min-w-0 items-center gap-1 text-[11px] leading-4 text-ink-3">
              <Icon name="list" size={10} stroke={2.2} className="shrink-0" />
              <span className="truncate tabular-nums">{plan}</span>
            </span>
          )}
          {/* one line high: a piece that does not fit wraps out of sight instead of squeezing the others */}
          {workflow && (
            <span data-workflow-line title={workflowTitle} aria-label={workflowTitle} className="flex h-4 min-w-0 items-center gap-1 text-[11px] leading-4 text-ai">
              <Icon name="workflow" size={10} stroke={2.2} className="shrink-0" />
              {/* its count rolls to the new one (#3434); the name and phase fade in when they change */}
              <span className="truncate tabular-nums"><Rolling value={workflow} /></span>
            </span>
          )}
          {detail.length > 0 && <span className="flex h-4 min-w-0 flex-wrap gap-x-1 overflow-hidden whitespace-nowrap text-[11px] leading-4 text-ink-3">
            {detail.map((d, i) => (
              <span key={i} title={d.title} className={cx("flex shrink-0 items-center gap-1 tabular-nums", d.danger ? "font-medium text-orange" : d.warn && "font-medium text-orange")}>
                {i > 0 && <span aria-hidden="true">·</span>}
                {d.text}
              </span>
            ))}
          </span>}
        </span>
      ) : (
        <span className={cx("flex-1 truncate text-[13px]", titleMin, titleFirst, active ? "font-medium text-ink" : st === "offline" ? "text-ink-3" : "text-ink-2", st === "unread" && !active && "font-medium text-ink")}>
          {label ?? sessionTitle(s)}
        </span>
      )}
      {host && <span className={cx("shrink-0 rounded bg-hover px-1 text-[10px] leading-4 text-ink-3", aside)}>{host}</span>}
      {showTodoTag && s.todo && (
        <span
          className={cx("flex shrink-0 items-center justify-center rounded bg-hover px-1 py-0.5 text-ink-3", aside)}
          title={t("sidebar.todo_tag", { title: s.todo.title })}
          aria-label={t("sidebar.todo_tag", { title: s.todo.title })}
        >
          <Icon name="todo" size={10} stroke={2} />
        </span>
      )}
      {source && <SourceTag s={s} className={aside} />}
      {/* the row's ⋯ takes the end on hover: what is there steps aside instead of showing half under it */}
      {tail && <span className={cx("max-w-[72px] min-w-6 shrink-[20] truncate text-[11px] text-ink-3", aside)}>{tail}</span>}
      {!tail && (st === "offline" || !showState) && <span className={cx("shrink-0 text-[11px] text-ink-3", aside)}>{ago(s.updated_at)}</span>}
      {(s.pending_interactions ?? 0) > 0 && <span className={cx("flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-field px-1 text-[10px] font-semibold text-attention tabular-nums", aside)}>{s.pending_interactions}</span>}
    </a>
  );
  if (!onMenu) return link;
  // the ⋯ shows over the row's end on hover (a pointer); on the open row and on a touch screen it stays, in the room
  // the row leaves for it
  return (
    <div className="group/row relative">
      {link}
      <button
        type="button"
        aria-label={menuLabel}
        aria-haspopup="menu"
        title={t("sidebar.more")}
        tabIndex={-1}
        onClick={(e) => onMenu(s, e.currentTarget.parentElement ?? e.currentTarget)}
        className={cx("absolute top-1/2 right-1 z-20 size-6 -translate-y-1/2 items-center justify-center rounded-[6px] text-ink-3 hover:text-ink group-hover/row:flex [@media(hover:none)]:right-0 [@media(hover:none)]:flex [@media(hover:none)]:size-11 [@media(hover:none)]:bg-transparent", active ? "flex hover:bg-hover" : "hidden bg-hover hover:bg-hover-2")}
      >
        <Icon name="more" size={14} />
      </button>
    </div>
  );
}, sameProps);

// Personal agents: the Lark gateway's personal agents with a view menu (group / sort / show), a search field, pins and custom
// sections — the prefs live on the hub so iOS sees the same pins.
type MenuPage = "root" | "group" | "sort" | "show";
function agentViewItems(page: MenuPage, setPage: (p: MenuPage) => void, p: AgentPrefs, providers: string[]): MenuItem[] {
  const v = viewOf(p);
  const back: MenuItem = { key: "back", label: t("sidebar.back"), keepOpen: true, onSelect: () => setPage("root") };
  if (page === "group")
    return [back, ...(Object.keys(GROUP_LABEL) as GroupBy[]).map((g, i) => ({ key: g, label: GROUP_LABEL[g], divider: i === 0, checked: v.group === g, onSelect: () => updateAgentPrefs((x) => setView(x, { group: g })) }))];
  if (page === "sort")
    return [back, ...(Object.keys(SORT_LABEL) as SortBy[]).map((s, i) => ({ key: s, label: SORT_LABEL[s], divider: i === 0, checked: v.sort === s, onSelect: () => updateAgentPrefs((x) => setView(x, { sort: s })) }))];
  if (page === "show")
    return [
      back,
      ...(Object.keys(SHOW_LABEL) as ShowMode[]).map((s, i) => ({ key: s, label: SHOW_LABEL[s], divider: i === 0, checked: v.show === s, keepOpen: true, onSelect: () => updateAgentPrefs((x) => setView(x, { show: s })) })),
      ...providers.map((pr, i) => ({ key: `p:${pr}`, label: providerLabel(pr), hint: i === 0 ? t("sidebar.sources") : undefined, divider: i === 0, checked: !v.providers.length || v.providers.includes(pr), keepOpen: true, onSelect: () => updateAgentPrefs((x) => (viewOf(x).providers.length ? toggleProvider(x, pr) : setView(x, { providers: providers.filter((y) => y !== pr) }))) })),
    ];
  const showHint = v.show !== "all" ? SHOW_LABEL[v.show] : v.providers.length ? t("sidebar.source_count", { count: v.providers.length }) : SHOW_LABEL.all;
  return [
    { key: "group", label: t("sidebar.group_by"), hint: GROUP_LABEL[v.group], submenu: true, onSelect: () => setPage("group") },
    { key: "sort", label: t("sidebar.sort_by"), hint: SORT_LABEL[v.sort], submenu: true, onSelect: () => setPage("sort") },
    { key: "show", label: t("sidebar.show"), hint: showHint, submenu: true, onSelect: () => setPage("show") },
    {
      key: "section",
      label: t("sidebar.new_group"),
      icon: "plus",
      divider: true,
      onSelect: () => {
        const name = prompt(t("sidebar.group_name"))?.trim();
        if (name) updateAgentPrefs((x) => addSection(x, name));
      },
    },
    { key: "reset", label: t("sidebar.reset_view"), divider: true, disabled: isDefaultView(p), onSelect: () => updateAgentPrefs(resetView) },
    { key: "all", label: t("sidebar.view_all"), onSelect: () => navigate({ name: "agents" }) },
  ];
}


// Row actions (⋯ and right click): pin, move to a custom section, remove a section, remove from the list.
export function agentRowItems(s: AgentSession, p: AgentPrefs, deps: HideDeps = personalHideDeps): MenuItem[] {
  const pinned = p.pins.includes(s.id);
  const cur = p.assign[s.id];
  return [
    { key: "pin", label: pinned ? t("sidebar.unpin") : t("sidebar.pin"), icon: "pin", onSelect: () => updateAgentPrefs((x) => togglePin(x, s.id)) },
    ...p.sections.map((sec, i) => ({ key: `sec:${sec.id}`, label: t("sidebar.move_to_group", { name: sec.name }), divider: i === 0, checked: cur === sec.id, onSelect: () => updateAgentPrefs((x) => assignSection(x, s.id, cur === sec.id ? null : sec.id)) })),
    ...(cur ? [{ key: "unsec", label: t("sidebar.remove_from_group"), onSelect: () => updateAgentPrefs((x) => assignSection(x, s.id, null)) }] : []),
    { key: "hide", label: t("lib-agents.remove"), icon: "archive", divider: true, onSelect: () => void removeAgentSession(s, deps) },
  ];
}

// memoized: its props are stable (reg, hover, go) or a string, and it reads the agents from their own store, so a
// sessions push does not reach it
const personalIdentity = (s: AgentSession) => s.id;
const PersonalAgents = memo(function PersonalAgents(props: { reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const bounded = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  return bounded ? <BoundedPersonalAgents {...props} /> : <LegacyPersonalAgents {...props} />;
});

function BoundedPersonalAgents({ reg, hover, activeKey, go }: { reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const prefs = useAgentPrefs();
  const [rowMenu, setRowMenu] = useState<{ s: AgentSession; el: HTMLElement } | null>(null);
  const onMenu = useCallback((s: AgentSession, el: HTMLElement) => setRowMenu({ s, el }), []);
  const [folded, toggle] = useFolded("personal-agents", false, { holds: activeKey.startsWith("a:") });
  const page = useSyncExternalStore(personalSessions.subscribe, personalSessions.getSnapshot, personalSessions.getSnapshot);
  const going = useGoingIds();
  if (page.loaded && !page.total && !page.error) return null;
  return <Section title={t("sidebar.personal_agents")} folded={folded} onToggle={toggle} right={<GroupCount n={page.total} label={t("sidebar.agent_count", { count: page.total })} />}>
    {!folded && <PagedSessionList root={personalSessions} path="/agents/sessions" identity={personalIdentity} compare={personalOrder} event="agents" personal
      renderRow={(s) => (going.has(s.id) ? null : <AgentRow s={s} reg={reg} hover={hover} active={activeKey === `a:${s.id}`} go={go} pinned={prefs.pins.includes(s.id)} onMenu={onMenu} />)} />}
    {rowMenu && <Menu anchor={rowMenu.el} onClose={() => setRowMenu(null)} align="right" width={200} items={agentRowItems(rowMenu.s, prefs)} />}
  </Section>;
}

const LegacyPersonalAgents = memo(function LegacyPersonalAgents({ reg, hover, activeKey, go }: { reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const { sessions: listed, loaded, error } = useAgentList();
  // a row removed here leaves at once, before the hub's re-pull
  const going = useGoingIds();
  const sessions = useMemo(() => (going.size ? listed.filter((s) => !going.has(s.id)) : listed), [listed, going]);
  const [retrying, setRetrying] = useState(false);
  const retry = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await refreshAgentSessions();
    } finally {
      setRetrying(false);
    }
  };
  const prefs = useAgentPrefs();
  const [folded, toggle] = useFolded("personal-agents", false, { holds: activeKey.startsWith("a:") });
  const [q, setQ] = useState("");
  const [searching, setSearching] = useState(false);
  const [all, setAll] = useState<Record<string, boolean>>({});
  const menu = useMenu();
  const [page, setPage] = useState<MenuPage>("root");
  const [rowMenu, setRowMenu] = useState<{ s: AgentSession; el: HTMLElement } | null>(null);
  // the same function on every render, so the memoized rows skip a re-render
  const onRowMenu = useCallback((s: AgentSession, el: HTMLElement) => setRowMenu({ s, el }), []);
  if (loaded && !sessions.length && !error) return null;
  const groups = arrangeSessions(sessions, prefs, q);
  const unread = totalUnread(sessions);
  const providers = [...new Set(sessions.map((s) => s.provider))];
  const filtered = !isDefaultView(prefs) || !!q;
  const sectionOf = (key: string) => (key.startsWith("sec:") ? key.slice(4) : "");
  return (
    <Section
      title={t("sidebar.personal_agents")}
      folded={folded}
      onToggle={toggle}
      right={
        <>
          {unread ? <span title={t("sidebar.unread_messages_count", { count: unread })} className="flex h-4 min-w-4 items-center justify-center rounded-full bg-accent-tint px-1 text-[10px] font-semibold text-accent-ink tabular-nums"><span aria-hidden>{unread}</span><span className="sr-only">{t("sidebar.unread_messages_count", { count: unread })}</span></span> : sessions.length > 0 && <GroupCount n={sessions.length} label={t("sidebar.agent_count", { count: sessions.length })} />}
          <button type="button" title={t("sidebar.search_personal")} aria-label={t("sidebar.search_personal")} onClick={() => setSearching((x) => !x || !!q)} className={cx("touch-hit relative z-10 flex size-5 items-center justify-center rounded-[5px] text-ink-3 hover:bg-hover hover:text-ink-2", (searching || q) && "text-ink-2")}>
            <Icon name="search" size={12} />
          </button>
          <button
            type="button"
            title={t("sidebar.filter_sort")}
            aria-label={t("sidebar.filter_sort")}
            aria-haspopup="menu"
            onClick={(e) => {
              setPage("root");
              menu.open(e);
            }}
            className={cx("touch-hit relative z-10 flex size-5 items-center justify-center rounded-[5px] hover:bg-hover hover:text-ink-2", filtered ? "text-accent" : "text-ink-3", menu.anchor && "bg-hover")}
          >
            <Icon name="filter" size={12} />
          </button>
          {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} align="right" width={220} items={agentViewItems(page, setPage, prefs, providers)} />}
        </>
      }
    >
      {!loaded && <p className="px-2 py-1 text-[12px] text-ink-3">{t("sidebar.loading")}</p>}
      {error && <div className="relative z-10 px-2 py-1">
        <p role="alert" className="text-[12px] break-words text-red">{t("sidebar.personal_failed", { reason: error })}</p>
        <button type="button" disabled={retrying} onClick={() => void retry()} className="touch-hit mt-1 rounded-control px-2 py-1 text-xs leading-normal whitespace-nowrap text-accent-ink hover:bg-hover disabled:cursor-wait disabled:opacity-50">
          {t(retrying ? "sidebar.loading" : "sidebar.retry")}
        </button>
      </div>}
      {(searching || q) && (
        <input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              setQ("");
              setSearching(false);
            }
          }}
          placeholder={t("sidebar.search_personal")}
          className="relative z-10 mx-1 mb-1 h-7 rounded-control bg-inset px-2 text-[12px] text-ink shadow-hairline outline-none placeholder:text-ink-3"
        />
      )}
      {loaded && !error && !groups.length && <p className="px-2 py-1 text-[12px] text-ink-3">{t("sidebar.no_match")}</p>}
      {groups.map((g) => {
        const more = !q && !all[g.key] && g.key !== "pinned" && g.sessions.length > 5;
        const isOpen = (s: AgentSession) => activeKey === `a:${s.id}`;
        const rows = more ? firstAndCurrent(g.sessions, 5, isOpen) : g.sessions;
        const sec = sectionOf(g.key);
        return (
          <div key={g.key} className="flex flex-col gap-px">
            {g.label && (groups.length > 1 || g.key === "pinned" || sec) && (
              <div className="group/g flex items-center gap-1.5 px-2 pt-1.5 pb-0.5 text-[11px] text-ink-3">
                {g.key === "pinned" && <Icon name="pin" size={10} />}
                <span className="min-w-0 flex-1 truncate">{g.label}</span>
                {g.unread > 0 && <span className="tabular-nums text-accent-ink">{g.unread}</span>}
                {sec && (
                  <button type="button" title={t("sidebar.delete_group")} aria-label={t("sidebar.delete_group_named", { name: g.label })} onClick={() => confirm(t("sidebar.delete_group_confirm", { name: g.label })) && updateAgentPrefs((x) => removeSection(x, sec))} className="relative z-10 opacity-0 group-hover/g:opacity-100 hover:text-ink-2">
                    <Icon name="x" size={10} />
                  </button>
                )}
              </div>
            )}
            {rows.map((s) => (
              <AgentRow key={s.id} s={s} reg={reg} hover={hover} active={isOpen(s)} go={go} pinned={prefs.pins.includes(s.id)} onMenu={onRowMenu} />
            ))}
            {g.sessions.length > 5 && !q && g.key !== "pinned" && (
              <button type="button" onClick={() => setAll((x) => ({ ...x, [g.key]: !x[g.key] }))} className="relative z-10 w-full rounded-[7px] px-2 py-1 text-left text-[12px] text-ink-3 hover:text-ink-2">
                {more ? t("sidebar.show_more", { count: g.sessions.length - rows.length }) : t("sidebar.show_less")}
              </button>
            )}
          </div>
        );
      })}
      {rowMenu && <Menu anchor={rowMenu.el} onClose={() => setRowMenu(null)} align="right" width={200} items={agentRowItems(rowMenu.s, prefs)} />}
    </Section>
  );
});

// memoized (props by identity: `s` is kept while the agent is unchanged, the functions are stable)
export const AgentRow = memo(function AgentRow({ s, reg, hover, active, go, pinned, onMenu }: { s: AgentSession; reg: RegFn; hover: (k: string | null) => void; active: boolean; go: GoFn; pinned: boolean; onMenu: (s: AgentSession, el: HTMLElement) => void }) {
  const k = `a:${s.id}`;
  // one ref for the row's life, so a re-render does not let go of it and register it again (useGlide's reg)
  const regRow = useMemo(() => reg(k), [reg, k]);
  const to: Route = { name: "agents", id: s.id };
  const unread = s.unreadCount > 0;
  return (
    <div className="group/row relative" onContextMenu={(e) => {
      e.preventDefault();
      onMenu(s, e.currentTarget);
    }}>
      <a
        ref={regRow}
        href={href(to)}
        onClick={go(to)}
        onMouseEnter={() => hover(k)}
        onFocus={() => hover(k)}
        onBlur={() => hover(null)}
        aria-current={active ? "page" : undefined}
        title={`${s.title || t("lib-format.untitled_session")} · ${providerLabel(s.provider)}`}
        className={cx("relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row transition-transform duration-150 active:scale-[0.98] [@media(hover:none)]:pr-11 [@media(pointer:coarse)]:min-h-11", active && cx(CURRENT_ROW, "pr-7"))}
      >
        <AgentAvatar s={s} size={16} />
        <span className={cx("min-w-0 flex-1 truncate text-[13px]", active || unread ? "font-medium text-ink" : "text-ink-2")}>{s.title || t("lib-format.untitled_session")}</span>
        <span className={cx("flex shrink-0 items-center", !active && "group-hover/row:invisible")}>
          {unread ? (
            <span className="flex h-4 min-w-4 items-center justify-center rounded-full bg-accent px-1 text-[10px] font-semibold text-white tabular-nums">{s.unreadCount}</span>
          ) : (
            s.rankTimeMs > 0 && <span className="text-[11px] text-ink-3">{ago(new Date(s.rankTimeMs).toISOString())}</span>
          )}
        </span>
      </a>
      <button
        type="button"
        title={pinned ? t("sidebar.unpin") : t("sidebar.pin")}
        aria-label={pinned ? t("sidebar.unpin") : t("sidebar.pin")}
        onClick={() => updateAgentPrefs((x) => togglePin(x, s.id))}
        className={cx("absolute top-1/2 right-1 z-20 flex size-5 -translate-y-1/2 items-center justify-center rounded-[5px] hover:bg-hover group-hover/row:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:hidden", active ? "opacity-100" : "opacity-0", pinned ? "text-accent" : "text-ink-3")}
      >
        <Icon name="pin" size={11} />
      </button>
      {/* a touch screen has no hover pin and no right click: the row's menu ("Pin" is in it) behind a ⋯ that stays */}
      <button
        type="button"
        aria-label={t("sidebar.personal_actions")}
        aria-haspopup="menu"
        title={t("sidebar.more")}
        onClick={(e) => onMenu(s, e.currentTarget.parentElement ?? e.currentTarget)}
        className="absolute top-1/2 right-0 z-20 hidden size-11 -translate-y-1/2 items-center justify-center text-ink-3 [@media(hover:none)]:flex"
      >
        <Icon name="more" size={14} />
      </button>
    </div>
  );
});

// ── the rail: the sidebar folded to its icons on a wide screen (#2524) ──

/** The badge of a folded page, as a dot on its icon. */
const DOT_CLASS = { attention: "bg-attention", red: "bg-red", orange: "bg-orange", green: "bg-green", accent: "bg-accent" } as const;

/** The button that folds the sidebar to the rail and opens it again (⌘⌥B by default; the hint follows a rebinding). */
export function CollapseButton({ collapsed, chord }: { collapsed: boolean; chord?: string }) {
  const label = collapsed ? t("sidebar.expand") : t("sidebar.collapse");
  return (
    <button
      type="button"
      onClick={toggleSidebarColumn}
      aria-label={label}
      aria-expanded={!collapsed}
      title={chord ? `${label} (${formatChord(chord)})` : label}
      className="touch-hit relative z-10 flex size-8 shrink-0 items-center justify-center rounded-control text-ink-3 transition-[background-color,color,transform] duration-150 hover:bg-hover hover:text-ink active:scale-[0.94]"
    >
      <Icon name="sidebar" size={15} stroke={1.8} />
    </button>
  );
}

/** The folded "Pages" title's right side: a dot when a page in it has a badge, so folding hides nothing to look at. */
function PagesTally({ pages }: { pages: NavPage[] }) {
  const counted = pages.filter((p) => p.badge !== undefined);
  const tones = [...new Set(counted.map((p) => p.badgeTone ?? "accent"))];
  if (!tones.length) return null;
  const label = counted.map((p) => `${p.label} ${p.badge}`).join(t("sidebar.list_separator"));
  return (
    <span title={label} className="flex items-center gap-1">
      <span className="sr-only">{label}</span>
      {tones.map((tone) => <span key={tone} aria-hidden className={cx("size-1.5 rounded-full", DOT_CLASS[tone])} />)}
    </span>
  );
}

const RAIL_ITEM = "touch-hit relative flex size-9 shrink-0 items-center justify-center rounded-[9px] transition-[background-color,color,transform] duration-150 active:scale-[0.94]";

/**
 * The sidebar folded on a wide screen: one column of icons, the open page filled, a page's count turned into a dot on
 * its icon (the count is in its label and tooltip). Why a rail rather than nothing: the pages stay one click away, and
 * the way back is on screen, so the fold can be kept across reloads (lib/layout.ts). The Agents' list and the
 * conversations are what fold away; ⌘K finds any of them.
 */
export function SidebarRail({ route, onSearch, onNewAgent }: { route: Route; onSearch: () => void; onNewAgent: () => void }) {
  const { phase, online, total, main, pages, more } = useNavPages();
  const hasAssistant = useStore((s) => Boolean(s.assistant));
  const [moreMenu, setMoreMenu] = useState<HTMLElement | null>(null);
  const bindings = useBindings();
  const activeKey = useActiveKey(route);
  const go = useGo();
  const status = connStatus(phase, online, total);
  const st = main ? stateOf(main) : undefined;
  const pending = main?.pending_interactions ?? 0;
  const link = (k: string, to: Route, label: string, icon: React.ReactNode, dot?: React.ReactNode, title?: string) => {
    const active = activeKey === k;
    return (
      <a
        key={k}
        href={href(to)}
        onClick={go(to)}
        aria-label={label}
        title={title ?? label}
        aria-current={active ? "page" : undefined}
        className={cx(RAIL_ITEM, active ? cx(CURRENT_ROW, "text-ink") : "text-ink-3 hover:bg-hover hover:text-ink")}
      >
        {icon}
        {dot}
      </a>
    );
  };
  return (
    <aside className="flex h-full w-full flex-col items-center bg-canvas py-2.5">
      <CollapseButton collapsed chord={bindings["left-sidebar"]} />
      <button type="button" onClick={onSearch} aria-label={t("sidebar.search_sessions")} title={`${t("sidebar.search_sessions")} (${jumpHint(bindings)})`} className={cx(RAIL_ITEM, "mt-1 text-ink-3 hover:bg-hover hover:text-ink")}>
        <Icon name="search" size={15} stroke={1.8} />
      </button>
      <span aria-hidden className="my-2 h-px w-6 shrink-0 bg-line" />
      <nav aria-label={t("sidebar.pages")} className="flex min-h-0 w-full flex-1 flex-col items-center gap-0.5 overflow-y-auto overscroll-contain">
        {hasAssistant && link("j:new", { name: "persona-new" }, t("sidebar.new_chat"), <Icon name="compose" size={15} stroke={1.8} />)}
        {link(
          "persona",
          { name: "persona" },
          pending > 0 ? `Persona ${pending}` : "Persona",
          <AppMark size={20} radius={5} className="shadow-hairline" />,
          pending > 0 ? <span aria-hidden className="absolute top-1 right-1 size-2 rounded-full bg-attention ring-2 ring-canvas" />
            : st === "working" || st === "needs_you" || st === "unread" ? <span aria-hidden className="absolute top-0.5 right-0.5 flex rounded-full ring-2 ring-canvas"><StatusMark state={st} /></span> : null,
          bindings.persona ? `${t("sidebar.persona_main")} (${formatChord(bindings.persona)})` : t("sidebar.persona_main"),
        )}
        {pages.map((p) => link(
          p.k,
          p.to,
          p.badge !== undefined ? `${p.label} ${p.badge}` : p.label,
          <Icon name={p.icon} size={16} stroke={1.8} />,
          p.badge !== undefined ? <span aria-hidden className={cx("absolute top-1.5 right-1.5 size-2 rounded-full ring-2 ring-canvas", DOT_CLASS[p.badgeTone ?? "accent"])} /> : null,
          [p.title ?? p.label, p.badge ?? p.hint].filter((x) => x !== undefined).join(" · "),
        ))}
        {more.length > 0 && (
          <button
            type="button"
            onClick={(e) => setMoreMenu(moreMenu ? null : e.currentTarget)}
            aria-haspopup="menu"
            aria-expanded={!!moreMenu}
            aria-label={t("sidebar.more_pages_count", { count: more.length })}
            title={t("sidebar.more_pages_count", { count: more.length })}
            className={cx(RAIL_ITEM, more.some((p) => p.k === activeKey) ? cx(CURRENT_ROW, "text-ink") : "text-ink-3 hover:bg-hover hover:text-ink")}
          >
            <Icon name="more" size={16} stroke={1.8} />
            {more.some((p) => p.badge !== undefined) && <span aria-hidden className={cx("absolute top-1.5 right-1.5 size-2 rounded-full ring-2 ring-canvas", DOT_CLASS[more.find((p) => p.badge !== undefined)!.badgeTone ?? "accent"])} />}
          </button>
        )}
        {moreMenu && <Menu anchor={moreMenu} onClose={() => setMoreMenu(null)} width={220} header={t("sidebar.more_pages")} items={morePagesMenu(more, activeKey)} />}
        <button type="button" onClick={onNewAgent} aria-label={t("sidebar.new_agent")} title={t("sidebar.new_agent")} className={cx(RAIL_ITEM, "mt-1 text-ink-3 hover:bg-hover hover:text-ink")}>
          <Icon name="plus" size={15} stroke={1.8} />
        </button>
      </nav>
      <span role="img" aria-label={status} title={status} className="relative mt-2 flex shrink-0">
        <AppMark size={26} radius={7} className="shadow-hairline" />
        <span className={cx("absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full ring-2 ring-canvas", phaseTone(phase).color)} style={phaseTone(phase).breathing ? BREATHING : undefined} />
      </span>
    </aside>
  );
}
