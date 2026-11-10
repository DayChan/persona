// What the sidebar's files share: the row and menu types, a row's key, the section heading and count, the badge and
// dot colors, the pages hook and navigation. It imports none of them (Sidebar, SidebarRows, SidebarTree, SidebarRail).
import { useCallback, useLayoutEffect, useMemo, useRef } from "react";
import { t } from "../i18n";
import { resolveTab } from "../lib/herdr-tree";
import { useInboxBadge } from "../lib/inbox";
import { useLarkHome } from "../lib/lark-chats";
import { agentKey } from "../lib/pins";
import { NAV_PAGES, extensionNavKey, sidebarActiveKey } from "../lib/persona-nav";
import { arrangePages, useSidebarPrefs } from "../lib/sidebar-pages";
import { useExtensionSlot } from "../lib/extension-slots";
import { toneOf } from "../lib/extension-ui";
import { BADGE_TONE, navIcon, pageRoute } from "./extensions/ExtensionSlots";
import { href, navigate, type Route } from "../lib/router";
import { useAgentSessions, useCounts } from "../lib/sessions";
import { useLarkOn, useStore } from "../lib/store";
import { connPhase } from "../lib/connection";
import { useWorkspaceBoardSetting } from "../lib/workspace-board-setting";
import type { Session } from "../lib/types";
import { Collapse, cx, type MenuItem } from "../ui/atoms";
import { Icon, type IconName } from "../ui/icons";
import type { MenuNode } from "../lib/node-actions";
import { contextMenuProps } from "../ui/context-menu";
import type { MenuAt } from "../ui/menu-model";

/** The open row's own fill (#2283): it stays while the pointer is elsewhere in the list, a step darker than hover. */
export const CURRENT_ROW = "bg-hover-2";
/** The least an agent row's title gets behind a prefix (styles.css `--row-title-min`, about 4.5 characters). */
export const ROW_TITLE_MIN = "min-w-(--row-title-min)";

/** The row a route lights up in the sidebar: a tab's page is the pane it shows (its row in the tree is the tab's). */
export function useActiveKey(route: Route): string {
  const all = useAgentSessions();
  const assistant = useStore((s) => s.assistant);
  const tabPath = route.name === "tab" ? href(route) : "";
  // eslint-disable-next-line react-hooks/exhaustive-deps -- `route` is parsed anew each render; its path says it all
  const tabPane = useMemo(() => (route.name === "tab" ? resolveTab(all, route) : undefined), [all, tabPath]);
  return route.name === "tab" ? (tabPane ? rowKey(tabPane) : "") : sidebarActiveKey(route, assistant?.main);
}

// One function for the component's life, so the rows given it skip a re-render (memo); a click still calls the
// latest onNavigate (the phone's drawer closing).
export function useGo(onNavigate?: () => void): GoFn {
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
  const online = useStore((s) => s.machines.filter((m) => m.online).length);
  const total = useStore((s) => s.machines.length);
  // "Terminal" (docs/integrations/terminal.md "Standalone terminal") is there where the hub can list panes
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
    machines: { k: "machines", to: { name: "machines" }, icon: "machine", label: t("sidebar.nav_machines"), short: t("sidebar.nav_machines_short"), hint: total ? `${online}/${total}` : undefined },
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

  return { phase, online, total, main, pages, more, all };
}

/** The pages under "More" as menu rows (the phone's grid, the rail): a choice opens the page, then `onNavigate`
 *  (the phone's drawer closes, as it does for a tap on any other page). */
export function morePagesMenu(more: NavPage[], activeKey: string, onNavigate?: () => void): MenuItem[] {
  const open = (to: Route) => () => {
    navigate(to);
    onNavigate?.();
  };
  return [
    ...more.map((p): MenuItem => ({ key: p.k, label: p.label, icon: p.icon, hint: p.badge !== undefined ? String(p.badge) : p.hint, checked: p.k === activeKey || undefined, onSelect: open(p.to) })),
    { key: "-arrange", label: t("sidebar.arrange_pages"), icon: "gear", divider: true, onSelect: open({ name: "settings" }) },
  ];
}

export function Section({ title, children, folded, onToggle, right, onMenu, first, hint }: { title: React.ReactNode; children: React.ReactNode; folded: boolean; onToggle: () => void; right?: React.ReactNode; onMenu?: (at: MenuAt, el: HTMLElement) => void; first?: boolean; hint?: string }) {
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

/** A group title's count with what it counts: a tooltip, and the same words for a screen reader. */
export function GroupCount({ n, label, className }: { n: number; label: string; className?: string }) {
  return (
    <span title={label} className={cx("text-[10.5px] text-ink-3 tabular-nums", className)}>
      <span aria-hidden>{n}</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}

export type RegFn = (k: string) => (el: HTMLElement | null) => void;
/** An agent row's key in the sidebar (highlight, active): sidebarActiveKey's for a session. */
export const rowKey = (s: Pick<Session, "machine_id" | "session_id">) => `s:${agentKey(s)}`;
/** Whether the open row (`activeKey`) is one of these agents': its group opens for it (useFolded). */
export const holdsRow = (sessions: readonly Session[], activeKey: string) => sessions.some((s) => rowKey(s) === activeKey);
export type GoFn = (to: Route) => (e: React.MouseEvent) => void;
/** `at`: where a right click or the menu key opened it; without it (the ⋯) the row's menu toggles beside its button.
 *  A row's ⋯ lies beside its link, not in it, and gives as `anchor` its parent, the box holding both: the menu
 *  closes on a pointerdown outside its anchor, and a press on the ⋯ has to stay inside it for the click that
 *  follows to toggle. */
export type RowMenuFn = (s: Session, anchor: HTMLElement, at?: MenuAt) => void;
/** A machine's, a workspace's or a tab's menu (src/lib/node-actions.ts); `toggle`: from its ⋯, which closes it again
 *  (a row's ⋯ anchors it on the box holding the row and the ⋯, as in RowMenuFn). */
export type NodeMenuFn = (node: MenuNode, at: MenuAt, header?: string, toggle?: boolean) => void;

export const BADGE_CLASS = { attention: "bg-field text-attention", red: "bg-red-tint text-red", orange: "bg-orange-tint text-orange", green: "bg-green-tint text-green", accent: "bg-accent-tint text-accent-ink" } as const;

/** The badge of a folded page, as a dot on its icon. */
export const DOT_CLASS = { attention: "bg-attention", red: "bg-red", orange: "bg-orange", green: "bg-green", accent: "bg-accent" } as const;
