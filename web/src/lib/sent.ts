// Settings › Sent as you (#5278, docs/extensions/connectors.md "Sent as the user", docs/protocol/protocol.md "Connector API"): the ledger of what
// Persona wrote in the user's name through the connectors, as GET /api/v1/connectors/sent gives it, newest first.
// iOS and the Mac have their own page (apple/Persona/Views/Settings/SentView.swift), Android its own (android/feature/settings/src/main/kotlin/persona/feature/settings/SentScreen.kt).
//
// The pure parts (the wire shapes, where a send went, how it was let through, who asked, paging and refreshing the
// list, the failures in plain words) are plain functions for the tests; the page is settings/SentSettings.tsx.
import type { ChatRuleState } from "./sent-chat-rules";
import { formatDate, t } from "../i18n";
import { api, ApiError, errorText } from "./api";

// ── wire types ───────────────────────────────────────────

/** The connector_gate rule that allowed a send (let_through.kind "rule"). */
export interface SentRule {
  connector?: string;
  /** The tool's name, or for Lark "<METHOD> <path>"; "*" for any. */
  action?: string;
  /** The body fields the rule matched. */
  match?: Record<string, unknown>;
  decision?: string;
}

/** How the gate let a send through: rule, conversation_always, card or auto (an unknown kind is shown as it is). */
export interface SentLetThrough {
  kind?: string;
  rule?: SentRule;
  /** card: the card the user was asked on, and "assistant" when the assistant answered it for them. */
  interaction_id?: string;
  answered_by?: string;
  /** auto: the extension setting that let it send without asking. */
  setting?: string;
}

/** The conversation that asked: both ids when the request named one, `extension` for a send an extension recorded. */
export interface SentOrigin {
  machine_id?: string;
  session_id?: string;
  extension?: string;
}

/** One row of the ledger (db.SentAsUser). */
export interface SentItem {
  id: number;
  at: string;
  connector: string;
  action: string;
  /** Where it went; the fields depend on the connector (docs/extensions/connectors.md "Sent as the user"). */
  target: Record<string, unknown>;
  /** What it said, at most SENT_TEXT_MOST characters. */
  text: string;
  /** One line: the card's first line, or the action and where it went. */
  summary: string;
  origin: SentOrigin;
  let_through: SentLetThrough;
  message_ref?: string;
  recalled_at?: string;
  chat_rule?: ChatRuleState;
}

/** A page of rows; `next` is the `before` of the page after it, null on the last. */
export interface SentPage {
  items: SentItem[];
  next: number | null;
}

/** The most characters of a send's text the ledger keeps (connector.SentTextMost): a text of exactly this was cut. */
export const SENT_TEXT_MOST = 4000;

/** The id of the connector the node's Lark gate writes under: its sends are in the ledger though it is no registered connector. */
export const LARK = "lark";

// ── requests ─────────────────────────────────────────────

/** `GET /connectors/sent?before=&limit=&connector=`: `before` is a row id (what `next` gave), `limit` defaults to 50 on the hub. */
export function sentPath(o: { before?: number | null; limit?: number; connector?: string | null } = {}): string {
  const q = new URLSearchParams();
  if (o.before) q.set("before", String(o.before));
  if (o.limit) q.set("limit", String(o.limit));
  if (o.connector) q.set("connector", o.connector);
  const s = q.toString();
  return `/connectors/sent${s ? `?${s}` : ""}`;
}

const object = (v: unknown): Record<string, unknown> => (v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : {});
const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** A row as the page uses it: the three objects are objects whatever the hub sent, and a row with no id (which paging
 *  and the de-duplication need) is none. */
function row(raw: unknown): SentItem | null {
  const r = object(raw);
  if (typeof r.id !== "number") return null;
  return {
    id: r.id,
    at: str(r.at),
    connector: str(r.connector),
    action: str(r.action),
    target: object(r.target),
    text: str(r.text),
    summary: str(r.summary),
    origin: object(r.origin) as SentOrigin,
    let_through: object(r.let_through) as SentLetThrough,
    message_ref: str(r.message_ref) || undefined,
    recalled_at: str(r.recalled_at) || undefined,
    chat_rule: r.chat_rule && typeof r.chat_rule === "object" && !Array.isArray(r.chat_rule) ? {
      can: object(r.chat_rule).can === true, exists: object(r.chat_rule).exists === true,
      id: str(object(r.chat_rule).id) || undefined, reason: str(object(r.chat_rule).reason) || undefined,
    } : undefined,
  };
}

export function sentPage(raw: unknown): SentPage {
  const r = object(raw);
  const items = Array.isArray(r.items) ? r.items.map(row).filter((x): x is SentItem => x !== null) : [];
  return { items, next: typeof r.next === "number" ? r.next : null };
}

export async function listSent(o: { before?: number | null; limit?: number; connector?: string | null; signal?: AbortSignal } = {}): Promise<SentPage> {
  return sentPage(await api<unknown>(sentPath(o), { signal: o.signal }));
}

// ── failures ─────────────────────────────────────────────

export type SentProblemKind = "no_database" | "forbidden" | "unsupported" | "error";

/**
 * Why the list can't be read, in plain words. The hub's own answers come before errorText, which would read a 503 as "the
 * hub is unavailable" (`no_database` is the hub saying it has no ledger) and a 404 as "not found": 403 is a device without
 * full rights; a 404 with no code is a hub from before the ledger (managers.ts `unsupported`).
 */
export function sentProblem(e: unknown): { kind: SentProblemKind; text: string } {
  if (e instanceof ApiError) {
    if (e.status === 503 && e.code === "no_database") return { kind: "no_database", text: t("settings-sent.no_database") };
    if (e.status === 403) return { kind: "forbidden", text: t("settings-sent.forbidden") };
    if (e.status === 404 && !e.code) return { kind: "unsupported", text: t("settings-sent.unsupported") };
  }
  return { kind: "error", text: errorText(e) };
}

// ── the list ─────────────────────────────────────────────

/** `have` and `incoming` together, one row per id (the newer copy wins), newest first. */
export function mergeSent(have: SentItem[], incoming: SentItem[]): SentItem[] {
  const byId = new Map<number, SentItem>();
  for (const it of have) byId.set(it.id, it);
  for (const it of incoming) byId.set(it.id, it);
  return [...byId.values()].sort((a, b) => b.id - a.id);
}

/**
 * What reading the first page again leaves. Nothing older loaded: the page alone (rows the hub pruned go). Older pages
 * loaded: the page merged in and `next` kept (it is the older end's); unless the page does not reach back to the newest
 * row held (more came in than a page holds, so merging would leave a hole), then the page alone and the older pages go.
 * `next: undefined` means keep the cursor.
 */
export function refreshed(have: SentItem[], page: SentPage, loadedOlder: boolean): { items: SentItem[]; next: number | null | undefined; loadedOlder: boolean } {
  const oldest = page.items[page.items.length - 1]?.id ?? 0;
  const reaches = page.items.length > 0 && (page.next === null || oldest <= (have[0]?.id ?? 0));
  if (!loadedOlder || !reaches) return { items: page.items, next: page.next, loadedOlder: false };
  return { items: mergeSent(have, page.items), next: undefined, loadedOlder: true };
}

// ── connectors (the filter) ──────────────────────────────

export interface ConnectorChoice {
  id: string;
  name: string;
}

/** A connector's name: mail, GitHub and Lark (its writes through the node's gate are in the ledger though no connector is
 *  registered for them) by their usual words, any other, an extension's, as its id, which is what the ledger calls it. */
export function connectorName(id: string): string {
  switch (id) {
    case "mail":
      return t("lib-inbox.kind_mail");
    case "github":
      return "GitHub";
    case LARK:
      return t("lib-inbox.lark");
    default:
      return id;
  }
}

/**
 * The filter's chips: Lark, the connectors rows have shown (so one with no record is never offered: Messages only reads,
 * and the hub's list of connectors can't say which ones write) and the chosen one, so its chip never goes while it is
 * chosen. No duplicates, in that order. A connector whose rows are all on pages not loaded yet gets its chip when they are.
 */
export function connectorChoices(seen: string[], selected: string | null): ConnectorChoice[] {
  const out: ConnectorChoice[] = [];
  for (const id of [LARK, ...seen, ...(selected ? [selected] : [])]) {
    if (id && !out.some((c) => c.id === id)) out.push({ id, name: connectorName(id) });
  }
  return out;
}

/** The ids of `items`' connectors added to `seen`, in the order they first showed. */
export function seenConnectors(seen: string[], items: SentItem[]): string[] {
  const add = items.map((i) => i.connector).filter((id) => id && !seen.includes(id));
  return add.length ? [...new Set([...seen, ...add])] : seen;
}

// ── a row ────────────────────────────────────────────────

/** A target field's value as text: a list joined, a number or boolean as it is, anything nested as JSON. */
export function fieldText(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "string") return v.trim();
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  if (Array.isArray(v)) return v.map(fieldText).filter(Boolean).join(", ");
  return JSON.stringify(v);
}

/** The arguments connectors usually name their destination by, in the order the hub keeps them (extmgr targetFields). */
const COMMON = ["chat", "thread", "channel", "conversation", "recipient", "to", "reply_to", "subject", "repo", "number"];

/**
 * Where a send went, in a few words: mail the recipients and subject, GitHub the repo#number, Lark the conversation (else
 * the method and path the node's gate saw); any other connector's the common fields it named, up to three. "—" when there
 * is nothing to say (a connector that gave only the names of its arguments).
 */
export function targetText(item: Pick<SentItem, "connector" | "target">): string {
  const g = item.target ?? {};
  const f = (k: string) => fieldText(g[k]);
  switch (item.connector) {
    case "mail": {
      if (f("to")) return f("subject") ? `${f("to")} · ${f("subject")}` : f("to");
      break;
    }
    case "github": {
      if (f("ref")) return f("ref");
      break;
    }
    case LARK: {
      if (f("conversation")) return f("conversation");
      const call = [f("method"), f("path")].filter(Boolean).join(" ");
      if (call) return call;
      break;
    }
  }
  const shown = COMMON.map(f).filter(Boolean).slice(0, 3);
  return shown.length ? shown.join(" · ") : "—";
}

/** The target's fields for the details, as the hub keeps them (key order as given), empty values left out. */
export function targetFields(target: Record<string, unknown> | undefined): { key: string; value: string }[] {
  return Object.entries(target ?? {})
    .map(([key, v]) => ({ key, value: fieldText(v) }))
    .filter((f) => f.value !== "");
}

/** The first `most` lines of the text (blank ones skipped) for the row; with no text, the one-line summary. Empty: nothing was recorded. */
export function previewLines(item: Pick<SentItem, "text" | "summary">, most = 2): string[] {
  const lines = (item.text ?? "").split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  if (lines.length) return lines.slice(0, most);
  const summary = (item.summary ?? "").trim();
  return summary ? [summary] : [];
}

/** Whether the text was cut by the ledger: it is exactly as long as the ledger keeps (counted in characters, not UTF-16 units). */
export function textCut(text: string): boolean {
  return [...(text ?? "")].length >= SENT_TEXT_MOST;
}

export type AllowedTone = "neutral" | "orange";

/** A rule in a line, the way connector_gate writes it: `lark · POST /im/v1/messages (chat_id=…) → allow`. */
export function ruleText(r: SentRule | undefined): string {
  if (!r) return "";
  const head = [r.connector, r.action].filter(Boolean).join(" · ");
  const match = Object.entries(r.match ?? {}).map(([k, v]) => `${k}=${fieldText(v)}`).join(", ");
  return [head, match && `(${match})`, r.decision && `→ ${r.decision}`].filter(Boolean).join(" ");
}

/**
 * How the gate let it through: the badge's words and tone (orange where the user did not decide: the assistant answered
 * the card, or the extension sent on its own) and a sentence for the details. A kind this page does not know is shown as it is.
 */
export function allowedBy(lt: SentLetThrough | undefined): { label: string; tone: AllowedTone; detail: string } {
  const kind = lt?.kind ?? "";
  switch (kind) {
    case "rule":
      return { label: t("settings-sent.by_rule"), tone: "neutral", detail: ruleText(lt?.rule) || t("settings-sent.detail_rule") };
    case "conversation_always":
      return { label: t("settings-sent.by_always"), tone: "neutral", detail: t("settings-sent.detail_always") };
    case "card": {
      // `answered_by` is empty when the user answered (in the app: on the phone, the Mac, the web or a chat channel, which
      // the ledger does not say), "assistant" when the assistant did, and anyone else is named: not the user's own word
      const by = lt?.answered_by ?? "";
      if (by === "assistant") return { label: t("settings-sent.by_assistant"), tone: "orange", detail: t("settings-sent.detail_assistant") };
      if (by) return { label: t("settings-sent.by_named", { name: by }), tone: "orange", detail: t("settings-sent.detail_card_by", { name: by }) };
      return { label: t("settings-sent.by_card"), tone: "neutral", detail: t("settings-sent.detail_card") };
    }
    case "auto":
      return lt?.setting
        ? { label: t("settings-sent.by_auto_setting", { setting: lt.setting }), tone: "orange", detail: t("settings-sent.detail_auto_setting", { setting: lt.setting }) }
        : { label: t("settings-sent.by_auto"), tone: "orange", detail: t("settings-sent.detail_auto") };
    default:
      return { label: kind || "—", tone: "neutral", detail: "" };
  }
}

/** Who asked: the conversation to open (both ids named), and the extension when the row is one an extension recorded
 *  (what it names of the conversation is its own word). Neither: no conversation. */
export function originInfo(o: SentOrigin | undefined): { conversation: { mid: string; sid: string } | null; extension: string } {
  const mid = str(o?.machine_id).trim();
  const sid = str(o?.session_id).trim();
  return { conversation: mid && sid ? { mid, sid } : null, extension: str(o?.extension).trim() };
}

/** The moment, in full (the details; a row says how long ago). */
export function whenText(at: string): string {
  return formatDate(at, { dateStyle: "medium", timeStyle: "medium" });
}
