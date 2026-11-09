// Feishu message display settings (display tweaks, docs/extensions/lark-migration.md "Message display tweaks", iOS LarkMessageDisplay.swift):
// the Lark gateway's per-account switches for showing a recalled message's retained content ("Keep recalled messages") and a message's earlier
// versions ("Edit history"). They only decide what is shown: the hub sends retained content whatever they say, so anything
// unknown (not read yet, a failed read, an account that changed) hides it. A write carries the account and the old value;
// a conflict or an unknown result is never retried, the settings are read again.
import { useEffect, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { api, ApiError } from "./api";
import type { LarkMessage, LarkPart } from "./inbox";
import { larkPath, olAccount } from "./lark-accounts";
import { fromDefaultManager, itemHeaders, messageManager, useItemManager } from "./manager-choice";
import { isResumed } from "./reconnect";
import { onHubEvent } from "./store";

export interface LarkMessageDisplay {
  account_id: string;
  anti_recall: boolean;
  show_edit_history: boolean;
}

export type LarkDisplayKey = "anti_recall" | "show_edit_history";

/** One earlier version of an edited message (`edit_history`, docs/protocol/protocol.md LarkMessage). */
export type LarkEditVersion = NonNullable<LarkMessage["edit_history"]>[number];

/** What is shown while the settings are unknown: nothing retained. */
export const HIDDEN_DISPLAY: LarkMessageDisplay = { account_id: "", anti_recall: false, show_edit_history: false };

function isSystem(m: LarkMessage) {
  return m.type === "SYSTEM" || (m.parts ?? []).some((p) => p.kind === "text" && p.metadata?.system);
}

/** The message's content is shown: it was not recalled, or "Keep recalled messages" is on and the Lark gateway kept what it said. */
export function showsBody(d: LarkMessageDisplay, m: LarkMessage): boolean {
  if (!m.recalled) return true;
  const kept = (m.parts ?? []).some((p) => p.kind !== "text" || (p.text ?? "").trim() !== "") || (m.text ?? "").trim() !== "";
  return d.anti_recall && m.retained === true && !isSystem(m) && kept;
}

/** "Edited" opens the earlier versions: "Edit history" is on, the content shows, and versions were kept. */
export function showsHistory(d: LarkMessageDisplay, m: LarkMessage): boolean {
  return d.show_edit_history && showsBody(d, m) && (m.edit_history ?? []).length > 0;
}

/** A quoted message in one line: what it said (marked "Recalled" when recalled and kept), or that it was recalled. */
export function quoteSummary(d: LarkMessageDisplay, q: NonNullable<LarkMessage["reply_to"]>): string {
  if (!q.recalled) return q.text ?? "";
  const text = (q.text ?? "").trim();
  if (!(d.anti_recall && q.retained === true && text)) return t("lib-lark-display.recalled");
  return t("lib-lark-display.recalled_kept", { text });
}

/** What an earlier version shows. An encrypted one only its public face (`olk.publicParts`): the web does not open
 * encrypted content, and the version's own text may be the decrypted body. */
export function versionParts(v: LarkEditVersion): LarkPart[] {
  if (v.olk !== undefined && v.olk !== null) {
    const cover = (v.olk as { publicParts?: unknown }).publicParts;
    const parts = Array.isArray(cover) ? (cover as LarkPart[]) : [];
    return parts.length ? parts : [{ kind: "text", text: t("lib-lark-display.encrypted") }];
  }
  return v.parts?.length ? v.parts : [{ kind: "text", text: v.text }];
}

// ── the settings ─────────────────────────────────────────

export interface LarkDisplayState {
  snapshot?: LarkMessageDisplay;
  loading: boolean;
  saving: boolean;
  problem?: string;
}

export interface LarkDisplayDeps {
  read: () => Promise<LarkMessageDisplay>;
  write: (body: { account_id: string; key: LarkDisplayKey; value: boolean; expected: boolean }) => Promise<LarkMessageDisplay>;
}

export function liveLarkDisplayDeps(ol?: number, manager?: string): LarkDisplayDeps {
  return {
    read: () => api<LarkMessageDisplay>(larkPath("/lark/message-display", ol, manager)),
    write: (body) => api<LarkMessageDisplay>(larkPath("/lark/message-display", ol, manager), { method: "POST", json: body }),
  };
}

const notLoggedIn = (e: unknown) => e instanceof ApiError && e.code === "lark_not_logged_in";

function readProblem(e: unknown): string {
  if (notLoggedIn(e)) return t("lib-lark-display.not_logged_in");
  return e instanceof ApiError && e.status === 404 ? t("lib-lark-display.upgrade_read") : t("lib-lark-display.read_failed");
}

function writeProblem(e: unknown): string {
  if (notLoggedIn(e)) return t("lib-lark-display.not_logged_in");
  switch (e instanceof ApiError ? e.status : 0) {
    case 409: return t("lib-lark-display.conflict");
    case 403: return t("lib-lark-display.forbidden");
    case 404: return t("lib-lark-display.upgrade_write");
    default: return t("lib-lark-display.write_unknown");
  }
}

/** The settings of one Feishu account as this page knows them (iOS LarkMessageDisplayModel). A read asked for while
 * another runs reads again after it; a reply that a later change made stale is dropped. */
export class LarkDisplayModel {
  private s: LarkDisplayState = { loading: false, saving: false };
  private listeners = new Set<() => void>();
  private pending = false;
  private running?: Promise<void>;
  constructor(private deps: LarkDisplayDeps) {}

  get state(): LarkDisplayState {
    return this.s;
  }
  /** What the chat follows: the settings once known, else nothing retained. */
  get policy(): LarkMessageDisplay {
    return this.s.snapshot ?? HIDDEN_DISPLAY;
  }
  subscribe = (l: () => void) => {
    this.listeners.add(l);
    return () => {
      this.listeners.delete(l);
    };
  };
  private set(patch: Partial<LarkDisplayState>) {
    this.s = { ...this.s, ...patch };
    this.listeners.forEach((l) => l());
  }

  refresh(): Promise<void> {
    this.pending = true;
    if (this.s.saving) return Promise.resolve(); // the write reads again when it is done
    if (this.running) return this.running;
    this.set({ loading: true });
    this.running = this.readPending().finally(() => {
      this.running = undefined;
      this.set({ loading: false });
    });
    return this.running;
  }

  private async readPending() {
    while (this.pending) {
      this.pending = false;
      try {
        const next = await this.deps.read();
        if (this.pending) continue; // a change came in meanwhile: only the next read counts
        if (!next?.account_id) throw new Error("no account");
        this.set({ snapshot: next, problem: undefined });
      } catch (e) {
        if (this.pending) continue;
        this.set({ snapshot: undefined, problem: readProblem(e) });
      }
    }
  }

  async change(key: LarkDisplayKey, value: boolean): Promise<void> {
    const current = this.s.snapshot;
    const expected = current?.[key];
    if (!current || this.s.saving || this.s.loading || typeof expected !== "boolean") return;
    this.pending = false;
    this.set({ saving: true });
    try {
      const next = await this.deps.write({ account_id: current.account_id, key, value, expected });
      if (!next?.account_id) throw new Error("no account");
      if (!this.pending) this.set({ snapshot: next, problem: undefined });
    } catch (e) {
      this.set({ snapshot: undefined, problem: writeProblem(e) });
    }
    this.set({ saving: false });
    if (this.pending) await this.refresh();
  }
}

const models = new Map<string, LarkDisplayModel>();

/** A model's key: the account, and the manager whose Lark it is ("": the default's). */
const modelKey = (ol: number | undefined, manager: string | undefined) => `${itemHeaders(manager) ? manager : ""}|${ol ?? ""}`;

/** The page's one model for a Feishu account (`ol`, lark-accounts.ts; undefined: the hub's own) of manager's Lark
 *  (an inbox item's, docs/assistant/managers.md "Clients"; undefined: the default's). */
export function larkDisplayModel(ol?: number, manager?: string): LarkDisplayModel {
  const key = modelKey(ol, manager);
  let m = models.get(key);
  if (!m) {
    m = new LarkDisplayModel(liveLarkDisplayDeps(ol, manager));
    models.set(key, m);
  }
  return m;
}

/** A `lark` push that says the settings changed (docs/protocol/protocol.md `settings_changed`): read that account's again,
 *  of the manager the push is from. */
export function onLarkPush(msg: Record<string, unknown>) {
  if (msg.settings_changed !== true) return;
  models.get(modelKey(olAccount(msg.ol_account), fromDefaultManager(msg) ? undefined : messageManager(msg)))?.refresh();
}

/** The settings for one chat or the settings page, read when it shows, when the page comes back to the front, and
 * when the hub says they changed. Use it once per screen; its messages follow with `useLarkDisplay`. */
export function useLarkDisplayState(ol?: number): LarkDisplayState {
  const model = larkDisplayModel(ol, useItemManager());
  useEffect(() => {
    void model.refresh();
    const back = () => document.visibilityState === "visible" && void model.refresh();
    document.addEventListener("visibilitychange", back);
    const off = onHubEvent("lark", onLarkPush);
    // A reconnect the hub could not resume: a `settings_changed` push missed meanwhile is not replayed.
    const offHello = onHubEvent("hello", (msg) => !isResumed(msg) && void model.refresh());
    return () => {
      document.removeEventListener("visibilitychange", back);
      off();
      offHello();
    };
  }, [model]);
  return useSyncExternalStore(model.subscribe, () => model.state, () => model.state);
}

/** What a message follows (see `LarkDisplayModel.policy`); reads nothing itself. */
export function useLarkDisplay(ol?: number): LarkMessageDisplay {
  const model = larkDisplayModel(ol, useItemManager());
  return useSyncExternalStore(model.subscribe, () => model.policy, () => model.policy);
}
