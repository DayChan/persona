// Feishu chat Pins (docs/extensions/lark-migration.md "Pinned messages in a chat", iOS LarkChatPins.swift): the list of a chat's pinned
// messages, jumping to one in the chat, adding and removing. The Lark gateway marks Pins read when the list is read, so it is
// read only when the list is opened or a Pin was changed, never ahead of time. The list lives in memory while it is
// open (the hub answers no-store). An unknown write result is never retried: the list is read again to see.
import { t } from "../i18n";
import { api, ApiError, enc, errorText } from "./api";
import type { LarkMessage, LarkPage } from "./inbox";
import { larkPath } from "./lark-accounts";
import { HIDDEN_DISPLAY, showsBody, type LarkMessageDisplay } from "./lark-display";

export interface ChatPin {
  id: string;
  message_id: string;
  message?: LarkMessage | null;
  operator_name?: string;
  created_at?: string;
}

export interface ChatPinsPage {
  account_id: string;
  items: ChatPin[];
  next_cursor?: string;
  total: number;
}

export interface ChatPinTarget {
  message: LarkMessage;
  /** A reply in a thread: the thread's first message; the page is then the thread's replies. */
  root?: LarkMessage;
  page: LarkPage;
}

export const HUB_UPGRADE = t("lib-lark-pins.hub_upgrade");

/** A hub from before Pins has no such route: a bare 404 / 405 (not one about a missing message) means update the hub. */
export function pinRouteError(e: unknown): unknown {
  if (e instanceof ApiError && (e.status === 404 || e.status === 405) && ["", "not_found", "method_not_allowed"].includes((e.code ?? "").toLowerCase())) {
    return new ApiError(426, HUB_UPGRADE, { code: "lark_hub_upgrade_required" });
  }
  return e;
}

async function pinRequest<T>(path: string, init?: Parameters<typeof api>[1]): Promise<T> {
  try {
    return await api<T>(path, init);
  } catch (e) {
    throw pinRouteError(e);
  }
}

export const accountChanged = () => new ApiError(409, t("lib-lark-pins.account_changed"), { code: "lark_account_changed" });
export const resultUnknown = () => new ApiError(502, t("lib-lark-pins.result_unknown"), { code: "lark_pin_result_unknown" });
const incomplete = () => new ApiError(502, t("lib-lark-pins.page_incomplete"), { code: "lark_pin_page_incomplete" });

/** `ol`: the Feishu account the chat is in (`ol_account`, lark-accounts.ts); undefined: the hub's own. */
export function fetchChatPins(feed: string, account: string | undefined, cursor: string | undefined, ol?: number, manager?: string): Promise<ChatPinsPage> {
  const q = new URLSearchParams({ limit: "50" });
  if (account) q.set("account_id", account);
  if (cursor) q.set("cursor", cursor);
  return pinRequest<ChatPinsPage>(larkPath(`/lark/conversations/${enc(feed)}/pins?${q}`, ol, manager));
}

export async function locateChatPin(feed: string, message: string, account: string, topic: boolean, ol?: number, manager?: string): Promise<ChatPinTarget> {
  const r = await pinRequest<{ account_id: string; target: ChatPinTarget }>(
    larkPath(`/lark/conversations/${enc(feed)}/pin-messages/${enc(message)}?account_id=${enc(account)}&topic=${topic ? "1" : "0"}`, ol, manager),
  );
  if (r.account_id !== account || r.target?.message?.id !== message) throw accountChanged();
  return r.target;
}

/** Adds (pin undefined) or removes a Pin; resolves only once the hub confirmed the new state. */
export async function changeChatPin(feed: string, message: string, pin: string | undefined, account: string, ol?: number, manager?: string): Promise<void> {
  const r = await pinRequest<{ account_id: string; pinned: boolean }>(larkPath(`/lark/conversations/${enc(feed)}/pins`, ol, manager), {
    method: "POST",
    json: pin ? { account_id: account, message_id: message, pin_id: pin, remove: true } : { account_id: account, message_id: message, remove: false },
  });
  if (r.account_id !== account) throw accountChanged();
  if (r.pinned !== !pin) throw resultUnknown();
}

/** The message may get a Pin: the hub says so (from the Lark gateway's capabilities), and it is a real, settled message. */
export function canAddPin(m: LarkMessage): boolean {
  return m.can_pin === true && !m.recalled && !m.deleted && m.local_state !== "pending" && m.local_state !== "failed" && !m.sending && !m.failed && m.type !== "SYSTEM";
}

export const canRemovePin = (p: ChatPin) => p.message?.can_unpin === true;

/** A row opens its message unless the message is gone. */
export const canOpenPin = (p: ChatPin) => Boolean(p.message) && !p.message?.deleted;

/** One line of what was pinned (the hub sends only what may be shown: the public face of an encrypted message). A
 * recalled one says what it said only with "Keep recalled messages" on and its content kept (lark-display.ts), marked "Recalled". */
export function pinSummary(p: ChatPin, display: LarkMessageDisplay = HIDDEN_DISPLAY): string {
  const m = p.message;
  if (!m || m.deleted) return t("lib-lark-pins.message_unavailable");
  if (!showsBody(display, m)) return t("lib-lark-pins.message_recalled");
  const text = (m.text || (m.parts ?? []).map((x) => x.text ?? "").join("")).trim();
  const fallback = m.type === "IMAGE" ? t("lib-lark-pins.fallback_image") : m.type === "FILE" ? t("lib-lark-pins.fallback_file") : t("lib-lark-pins.fallback_message");
  return m.recalled ? t("lib-lark-pins.recalled_kept", { text: text || fallback }) : text || fallback;
}

export function isPinNotice(e: unknown): boolean {
  return e instanceof ApiError && Boolean(e.code && (e.code.startsWith("lark_pin") || e.code.endsWith("upgrade_required") || e.code === "lark_account_changed"));
}

/** What the list says when something failed: the hub's own text for Pin problems and upgrades. */
export function pinProblemText(e: unknown): string {
  if (isPinNotice(e)) return (e as ApiError).message;
  if (e instanceof ApiError && e.code === "lark_not_logged_in") return t("lib-lark-pins.not_logged_in");
  return t("lib-lark-pins.load_failed", { error: errorText(e) });
}

/** After this failure the outcome of a write is not known: read the list before writing again. */
export function needsRecheck(e: unknown): boolean {
  return (e instanceof ApiError && e.code === "lark_pin_result_unknown") || e instanceof TypeError || (e instanceof ApiError && e.status === 504);
}

/** The pins, one copy per id, in the order the hub sent them. */
export function mergePins(have: readonly ChatPin[], batch: readonly ChatPin[]): ChatPin[] {
  const seen = new Set<string>();
  return [...have, ...batch].filter((p) => p?.id && !seen.has(p.id) && (seen.add(p.id), true));
}

export interface ChatPinsState {
  items: ChatPin[];
  total: number;
  nextCursor?: string;
  account?: string;
  loading: boolean;
  loadingMore: boolean;
  changing: boolean;
  locating?: string;
  problem?: string;
  pageProblem?: string;
  /** The account changed or Feishu signed out: nothing more until the chat is opened again. */
  blocked: boolean;
  requiresRecheck: boolean;
  loaded: boolean;
}

export interface ChatPinsDeps {
  page: (account: string | undefined, cursor: string | undefined) => Promise<ChatPinsPage>;
  change: (message: string, pin: string | undefined, account: string) => Promise<void>;
  locate: (message: string, account: string) => Promise<ChatPinTarget>;
}

export function liveChatPinsDeps(feed: string, topic: boolean, ol?: number, manager?: string): ChatPinsDeps {
  return {
    page: (account, cursor) => fetchChatPins(feed, account, cursor, ol, manager),
    change: (message, pin, account) => changeChatPin(feed, message, pin, account, ol, manager),
    locate: (message, account) => locateChatPin(feed, message, account, topic, ol, manager),
  };
}

/** The opened list (iOS LarkChatPinsModel). A refresh reads as many pages as are shown; replies that come after
 * stop() or after the account changed are dropped. */
export class ChatPinsModel {
  private s: ChatPinsState = { items: [], total: 0, loading: false, loadingMore: false, changing: false, blocked: false, requiresRecheck: false, loaded: false };
  private listeners = new Set<() => void>();
  private pages = 1;
  private revision = 0;
  private pendingRefresh = false;
  private stopped = false;
  private seenCursors = new Set<string>();
  constructor(private deps: ChatPinsDeps, account?: string) {
    this.s.account = account;
  }

  get state(): ChatPinsState {
    return this.s;
  }
  get busy(): boolean {
    return this.s.loading || this.s.loadingMore || this.s.changing || this.s.locating !== undefined;
  }
  subscribe = (l: () => void) => {
    this.listeners.add(l);
    return () => {
      this.listeners.delete(l);
    };
  };
  private set(patch: Partial<ChatPinsState>) {
    this.s = { ...this.s, ...patch };
    this.listeners.forEach((l) => l());
  }

  stop() {
    this.stopped = true;
    this.revision += 1;
  }

  /** Open again after stop() (a remounted panel): earlier replies stay dropped. */
  start() {
    this.stopped = false;
  }

  async refresh(): Promise<void> {
    if (this.stopped || this.s.blocked) return;
    if (this.busy) {
      this.pendingRefresh = true;
      return;
    }
    do {
      this.pendingRefresh = false;
      this.set({ loading: true });
      const request = this.revision;
      try {
        let rows: ChatPin[] = [];
        let cursor: string | undefined;
        let owner = this.s.account;
        let total = 0;
        let read = 0;
        const cursors = new Set<string>();
        do {
          const page = await this.deps.page(owner, cursor);
          this.checkOwner(page.account_id, owner);
          owner = page.account_id;
          read += 1;
          total = page.total;
          rows = mergePins(rows, page.items ?? []);
          cursor = page.next_cursor || undefined;
          if (cursor) {
            if (cursors.has(cursor)) throw incomplete();
            cursors.add(cursor);
          }
        } while (read < this.pages && cursor);
        if (request !== this.revision) {
          this.set({ loading: false });
          continue; // a refresh asked for meanwhile still runs
        }
        this.seenCursors = cursors;
        this.pages = read;
        this.set({ account: owner, items: rows, total: Math.max(total, rows.length), nextCursor: cursor, problem: undefined, pageProblem: undefined, loaded: true, requiresRecheck: false, loading: false });
      } catch (e) {
        this.set({ loading: false });
        if (request === this.revision) this.failed(e);
      }
    } while (this.pendingRefresh && !this.s.blocked && !this.stopped);
  }

  async more(): Promise<void> {
    const cursor = this.s.nextCursor;
    if (this.busy || this.s.blocked || this.stopped || !cursor) return;
    this.set({ loadingMore: true, pageProblem: undefined });
    const request = this.revision;
    try {
      const page = await this.deps.page(this.s.account, cursor);
      this.checkOwner(page.account_id, this.s.account);
      if (request !== this.revision) {
        this.set({ loadingMore: false });
        return;
      }
      const next = page.next_cursor || undefined;
      if (next) {
        if (this.seenCursors.has(next)) throw incomplete();
        this.seenCursors.add(next);
      }
      const items = mergePins(this.s.items, page.items ?? []);
      this.pages += 1;
      this.set({ items, total: Math.max(page.total, items.length), nextCursor: next, loadingMore: false });
    } catch (e) {
      this.set({ loadingMore: false });
      if (request === this.revision) this.failed(e, true);
    }
    if (this.pendingRefresh) await this.refresh();
  }

  /** Adds (pin undefined) or removes a Pin, then reads the list again; true once the hub confirmed it. */
  async change(message: string, pin?: string): Promise<boolean> {
    const account = this.s.account;
    if (this.busy || this.s.blocked || this.s.requiresRecheck || this.stopped || !account) return false;
    this.set({ changing: true, problem: undefined });
    const request = this.revision;
    try {
      await this.deps.change(message, pin, account);
      if (request !== this.revision || this.stopped) {
        this.set({ changing: false });
        return false;
      }
      this.set({ changing: false, requiresRecheck: true });
      await this.refresh();
      return true;
    } catch (e) {
      this.set({ changing: false });
      if (request === this.revision) {
        this.set({ requiresRecheck: needsRecheck(e) });
        this.failed(e);
      }
      return false;
    }
  }

  /** Where a Pin's message is: found before the chat lets go of where it was; undefined leaves the list as it is. */
  async locate(pin: ChatPin): Promise<ChatPinTarget | undefined> {
    const account = this.s.account;
    if (this.busy || this.s.blocked || this.stopped || !account) return undefined;
    this.set({ locating: pin.id, problem: undefined });
    const request = this.revision;
    try {
      const target = await this.deps.locate(pin.message_id, account);
      if (target.message.id !== pin.message_id || this.stopped || request !== this.revision) return undefined;
      return target;
    } catch (e) {
      if (request === this.revision) this.failed(e);
      return undefined;
    } finally {
      this.set({ locating: undefined });
    }
  }

  private checkOwner(owner: string | undefined, expected: string | undefined) {
    if (!owner || (expected && expected !== owner)) throw accountChanged();
  }

  private failed(e: unknown, paging = false) {
    const code = e instanceof ApiError ? e.code : undefined;
    if (code === "lark_account_changed" || code === "lark_not_logged_in") {
      this.revision += 1;
      this.set({ items: [], nextCursor: undefined, total: 0, blocked: true });
    }
    const text = pinProblemText(e);
    this.set(paging ? { pageProblem: text } : { problem: text });
  }
}
