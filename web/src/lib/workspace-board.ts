import { t } from "../i18n";
import { api, ApiError, errorText } from "./api";
import { sameValue } from "./stable-lines";
import type { Session } from "./types";

export interface BoardColumn { id: string; title: string }
export interface BoardCard {
  id: string; machine_id: string; workspace_id: string; title: string; cwd: string;
  source: string; source_id?: string; branch?: string; column_id: string; pinned: boolean; archived: boolean;
}
export interface Workspace {
  id: string; machine_id: string; source: string; source_id: string; title: string; cwd: string;
  branch?: string; online?: boolean; available?: boolean; last_seen?: string;
}
export interface BoardState { revision: number; columns: BoardColumn[]; cards: BoardCard[]; can_write?: boolean }
export interface WorkspaceList { workspaces: Workspace[]; errors?: { machine_id: string; error: string }[] }
export interface BoardMutation {
  action: "add_card" | "update_card" | "move_card" | "add_column" | "update_column" | "move_column" | "delete_column";
  id?: string; column_id?: string; before_id?: string; title?: string; pinned?: boolean; archived?: boolean;
  machine_id?: string; workspace_id?: string; cwd?: string; move_to?: string;
}

/** A removed selection falls back to the first persisted workflow column. */
export function selectedColumn(board: BoardState, id: string): string {
  return board.columns.some((c) => c.id === id) ? id : board.columns[0]?.id ?? "";
}

/** Workflow is metadata. Agent state, discovery order and availability never assign a column. */
export function columnCards(board: BoardState, column: string, opts: { search?: string; machine?: string; archived?: boolean } = {}): BoardCard[] {
  const q = opts.search?.trim().toLocaleLowerCase() ?? "";
  const cards = board.cards.filter((c) => c.column_id === column && c.archived === Boolean(opts.archived)
    && (!opts.machine || c.machine_id === opts.machine)
    && (!q || `${c.title} ${c.cwd} ${c.branch ?? ""} ${c.machine_id}`.toLocaleLowerCase().includes(q)));
  return [...cards.filter((c) => c.pinned), ...cards.filter((c) => !c.pinned)];
}
/** Match the hub's CanonicalCwd for remote POSIX, Windows drive and UNC syntax. */
export function directory(p: string): string | null {
  if (!p || p.includes("\0")) return null;
  const clean = (path: string) => {
    const parts: string[] = [];
    for (const part of path.split("/")) {
      if (!part || part === ".") continue;
      if (part === "..") parts.pop(); else parts.push(part);
    }
    return `/${parts.join("/")}`;
  };
  if (/^[a-z]:/i.test(p)) return /^[a-z]:[\\/]/i.test(p) ? p.slice(0, 2).toLowerCase() + clean(p.slice(2).replaceAll("\\", "/")) : null;
  if (p.startsWith("\\\\") || p.startsWith("//")) {
    const parts = p.slice(2).replaceAll("\\", "/").split("/");
    if (parts.length < 2 || parts.slice(0, 2).some((v) => !v || v === "." || v === "..")) return null;
    const root = `//${parts[0].toLowerCase()}/${parts[1].toLowerCase()}`;
    const rest = clean(parts.slice(2).join("/"));
    return root + (rest === "/" ? "" : rest);
  }
  return p.startsWith("/") ? clean(p) : null;
}
export function sameDirectory(a: string, b: string): boolean {
  const canonical = directory(a);
  return canonical !== null && canonical === directory(b);
}
export function cardSessions(card: BoardCard, sessions: readonly Session[]): Session[] {
  return sessions.filter((s) => s.machine_id === card.machine_id && sameDirectory(s.workspace_cwd || s.cwd, card.cwd));
}
export function boundCard(board: BoardState, workspace: Workspace): BoardCard | undefined {
  return board.cards.find((c) => c.machine_id === workspace.machine_id && sameDirectory(c.cwd, workspace.cwd));
}
function insertBefore<T extends { id: string }>(items: T[], item: T, before?: string): T[] {
  const rest = items.filter((v) => v.id !== item.id);
  const i = before ? rest.findIndex((v) => v.id === before) : -1;
  rest.splice(i < 0 ? rest.length : i, 0, item);
  return rest;
}
/** Only project reversible metadata edits; additions wait for trustworthy IDs and canonical paths from the hub. */
export function optimisticBoard(board: BoardState, m: BoardMutation): BoardState {
  const card = board.cards.find((c) => c.id === m.id);
  const column = board.columns.find((c) => c.id === m.id);
  if (m.action === "move_card" && card && m.id !== m.before_id) return { ...board, cards: insertBefore(board.cards, { ...card, column_id: m.column_id! }, m.before_id) };
  if (m.action === "update_card" && card) return { ...board, cards: board.cards.map((c) => c.id !== m.id ? c : { ...c,
    ...(m.title !== undefined ? { title: m.title } : {}), ...(m.pinned !== undefined ? { pinned: m.pinned } : {}), ...(m.archived !== undefined ? { archived: m.archived } : {}) }) };
  if (m.action === "move_column" && column && m.id !== m.before_id) return { ...board, columns: insertBefore(board.columns, column, m.before_id) };
  if (m.action === "update_column") return { ...board, columns: board.columns.map((c) => c.id === m.id ? { ...c, title: m.title! } : c) };
  return board;
}

export interface BoardSnapshot {
  board: BoardState | null; discovery: WorkspaceList; loading: boolean; busy: boolean; readonly: boolean; error: string; discoveryError: string;
}
type Transport = <T>(path: string, init?: RequestInit & { json?: unknown }) => Promise<T>;
export class BoardModel {
  private state: BoardSnapshot = { board: null, discovery: { workspaces: [] }, loading: true, busy: false, readonly: false, error: "", discoveryError: "" };
  private listeners = new Set<() => void>();
  private reads = 0;
  constructor(private request: Transport = api) {}
  snapshot = () => this.state;
  subscribe = (fn: () => void) => { this.listeners.add(fn); return () => { this.listeners.delete(fn); }; };
  /** A patch that changes no field keeps the snapshot and notifies no one, so an unchanged poll does not re-render the board. */
  private set(patch: Partial<BoardSnapshot>) {
    if ((Object.keys(patch) as (keyof BoardSnapshot)[]).every((k) => Object.is(this.state[k], patch[k]))) return;
    this.state = { ...this.state, ...patch }; this.listeners.forEach((fn) => fn());
  }
  async refresh() {
    const read = ++this.reads;
    await Promise.allSettled([
      this.request<BoardState>("/workspace-board").then((board) => {
        if (read !== this.reads || this.state.busy) return;
        if (!this.state.board || board.revision >= this.state.board.revision) this.set({
          board: sameValue(board, this.state.board) ? this.state.board : board,
          readonly: board.can_write === undefined ? this.state.readonly : !board.can_write, error: "",
        });
      }).catch((e) => {
        if (read !== this.reads) return;
        this.set({ error: boardError(e), ...(e instanceof ApiError && e.status === 403 ? { readonly: true } : {}) });
      }),
      this.request<WorkspaceList>("/workspaces").then((discovery) => {
        if (read === this.reads) this.set({ discovery: sameValue(discovery, this.state.discovery) ? this.state.discovery : discovery, discoveryError: "" });
      }).catch((e) => { if (read === this.reads) this.set({ discoveryError: errorText(e) }); }),
    ]);
    if (read === this.reads) this.set({ loading: false });
  }
  async mutate(mutation: BoardMutation): Promise<boolean> {
    const previous = this.state.board;
    if (!previous || this.state.busy || this.state.readonly) return false;
    ++this.reads;
    this.set({ busy: true, error: "", board: optimisticBoard(previous, mutation) });
    let conflict = false;
    try {
      const board = await this.request<BoardState>("/workspace-board", { method: "POST", json: { revision: previous.revision, ...mutation } });
      this.set({ board, readonly: board.can_write === undefined ? this.state.readonly : !board.can_write });
      return true;
    } catch (e) {
      conflict = e instanceof ApiError && e.status === 409;
      this.set({ board: previous, error: boardError(e), ...(e instanceof ApiError && e.status === 403 ? { readonly: true } : {}) });
      return false;
    } finally {
      this.set({ busy: false });
      if (conflict) {
        await this.refresh();
        this.set({ error: t("lib-workspace-board.conflict_reloaded") });
      }
    }
  }
}
export function boardError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 403) return t("lib-workspace-board.read_only");
    if (e.status === 409) return t("lib-workspace-board.conflict_reloading");
    if (e.status === 404 || e.status === 503) return t("lib-workspace-board.unsupported");
  }
  return errorText(e);
}
