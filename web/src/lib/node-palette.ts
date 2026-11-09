// The command palette's view of the node the user is on (#2329, lib/palette.ts registerContextCommands): in `>` command
// mode its menu items are listed as commands, under "Workspace · <the pane's title>" and "Workspace · <its workspace>"
// like Moshi's. They are the same items the node's context menu has (lib/node-actions.ts): a rename asks for the name in
// a small dialog, a close asks once more, a failure is a toast. Registered when this module is first imported.
import { t } from "../i18n";
import { errorText } from "./api";
import { defaultNodeDeps, nodeActions, sessionNode, termPaneNode, termSpaceNode, termTabNode, type MenuNode } from "./node-actions";
import { registerContextCommands, type PaletteCommand, type PaletteContext } from "./palette";
import { getState, refresh } from "./store";
import type { TermSpace } from "./terminals";
import { sessionTitle } from "./format";
import { toast } from "../ui/atoms";
import type { MenuItem } from "../ui/menu-model";

// ── what the palette cannot read by itself ───────────────

const trees = new Map<string, readonly TermSpace[]>();
const treeListeners = new Set<() => void>();

/** The "Terminal" page tells which panes a machine has, so its route can be resolved to a node here. */
export function setKnownTree(mid: string, spaces: readonly TermSpace[]) {
  trees.set(mid, spaces);
}
/** The node's menu changed the tree on a computer: the "Terminal" page reads it again. */
export const onNodeChanged = (f: () => void) => {
  treeListeners.add(f);
  return () => void treeListeners.delete(f);
};

/** The rows of a submenu that is loaded when it opens (the launchers for "Run agent here…"), kept per node so the palette
 *  can list them: asked for the first time it starts the load, and the next listing has them. */
const loadedChildren = new Map<string, MenuItem[] | "loading">();
function childrenOf(key: string, load: () => Promise<MenuItem[]>): MenuItem[] {
  const have = loadedChildren.get(key);
  if (Array.isArray(have)) return have;
  if (!have) {
    loadedChildren.set(key, "loading");
    load().then((list) => loadedChildren.set(key, list), () => loadedChildren.delete(key));
  }
  return [];
}

// ── asking for a name, asking once more ──────────────────

export type NodeAsk =
  | { kind: "text"; title: string; label: string; value: string; resolve: (v: string | null) => void }
  | { kind: "confirm"; title: string; body: string; label: string; resolve: (v: boolean) => void };

let asking: NodeAsk | null = null;
const askListeners = new Set<() => void>();
const setAsk = (a: NodeAsk | null) => {
  asking = a;
  askListeners.forEach((f) => f());
};
export const getAsk = () => asking;
export const subscribeAsk = (f: () => void) => {
  askListeners.add(f);
  return () => void askListeners.delete(f);
};
export const answerAsk = () => setAsk(null);
const askText = (title: string, label: string, value: string) => new Promise<string | null>((resolve) => setAsk({ kind: "text", title, label, value, resolve }));
const askConfirm = (title: string, body: string, label: string) => new Promise<boolean>((resolve) => setAsk({ kind: "confirm", title, body, label, resolve }));

// ── the nodes of where the user is ───────────────────────

interface Where {
  section: string;
  node: MenuNode;
}

function findPane(spaces: readonly TermSpace[], id: string) {
  for (const sp of spaces)
    for (const tab of sp.tabs) {
      const pane = tab.panes.find((p) => p.id === id);
      if (pane) return { sp, tab, pane };
    }
  return undefined;
}

/** Whether the "Terminal" page's list says this workspace's host can make worktrees (an agent session's workspace has no flag of its own). */
function canWorktreeIn(mid: string, host: string | undefined, workspace: string | undefined): boolean {
  return (trees.get(mid) ?? []).some((sp) => sp.id === workspace && sp.host === (host || "herdr") && sp.canWorktree === true);
}

export function nodesFor(ctx: PaletteContext): Where[] {
  const state = getState();
  const machine = (mid: string) => state.machines.find((m) => m.id === mid);
  if (ctx.route.name === "terminal" && ctx.route.mid && ctx.route.id) {
    const mid = ctx.route.mid;
    const found = findPane(trees.get(mid) ?? [], ctx.route.id);
    const online = machine(mid)?.online !== false;
    if (!found) return [];
    const { sp, tab, pane } = found;
    const one = tab.panes.length === 1;
    return [
      { section: `Workspace · ${pane.title}`, node: one ? termTabNode(mid, sp, tab, online) : termPaneNode(mid, sp, tab, pane, online) },
      { section: `Workspace · ${sp.label}`, node: termSpaceNode(mid, sp, online) },
    ];
  }
  if (ctx.pane) {
    const s = state.sessions[ctx.pane.mid]?.find((x) => x.session_id === ctx.pane!.sid);
    if (!s || s.assistant) return [];
    const online = machine(s.machine_id)?.online !== false;
    const out: Where[] = [{ section: `Workspace · ${sessionTitle(s)}`, node: sessionNode(s, online) }];
    if (s.space_id && s.space_name)
      out.push({ section: `Workspace · ${s.space_name}`, node: { kind: "workspace", mid: s.machine_id, online, host: s.space_kind, label: s.space_name, workspaceId: s.workspace_id ?? s.space_id, paneId: s.pane_id, cwd: s.cwd, canWorktree: canWorktreeIn(s.machine_id, s.space_kind, s.workspace_id ?? s.space_id) } });
    return out;
  }
  return [];
}

// ── items as commands ────────────────────────────────────

function failed(e: unknown) {
  toast(errorText(e), "error");
}

/** The item's work, with what the menu would ask first: a name for a rename, a yes for a close or a push. */
async function perform(item: MenuItem) {
  try {
    if (item.rename) {
      const name = await askText(item.label.replace(/…$/, ""), item.rename.placeholder ?? t("lib-node-palette.name_placeholder"), item.rename.value);
      const next = name?.trim();
      if (next && next !== item.rename.value.trim()) await item.rename.commit(next);
      return;
    }
    if (item.confirm && !(await askConfirm(item.label, item.confirmDetail ?? t("lib-node-palette.close_confirm"), item.confirm))) return;
    await item.onSelect();
  } catch (e) {
    failed(e);
  }
}

export function itemsToCommands(section: string, node: MenuNode, items: MenuItem[]): PaletteCommand[] {
  const where = node.tabId ?? node.paneId ?? node.workspaceId ?? node.mid;
  const out: PaletteCommand[] = [];
  const add = (item: MenuItem, title: string, key: string, run: () => void) =>
    out.push({ id: `node:${node.kind}:${where}:${key}`, section, title, icon: item.icon, danger: item.danger, keywords: `${section} ${item.label}`, run });
  const name = (item: MenuItem) => item.label.replace(/…$/, "");
  for (const item of items) {
    const kids = typeof item.children === "function" ? childrenOf(`${node.mid}/${where}/${item.key}`, item.children) : item.children;
    if (kids) for (const c of kids) add({ ...c, icon: c.icon ?? item.icon }, `${name(item)} · ${c.label}`, `${item.key}:${c.key}`, () => void perform(c));
    else add(item, name(item), item.key, () => void perform(item));
  }
  return out;
}

function changed() {
  void refresh().catch(() => {});
  treeListeners.forEach((f) => f());
}

export function nodeCommands(ctx: PaletteContext): PaletteCommand[] {
  const deps = defaultNodeDeps(changed);
  return nodesFor(ctx).flatMap(({ section, node }) => {
    const items = nodeActions(node, deps, { omit: ["node-fold", "node-refresh", "node-shell"] });
    return itemsToCommands(section, node, items);
  });
}

registerContextCommands(nodeCommands);
