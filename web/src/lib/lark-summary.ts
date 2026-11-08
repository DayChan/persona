// Cards the Lark gateway summarises itself (a card part's `metadata.variant`): forwarded chats, group / contact cards,
// meetings, calendar events, votes, tasks, red packets (#3032, iOS LarkSummaryCards.swift, the Lark gateway's SummaryCardView).
// Pure reading of the metadata; the drawing is views/inbox/LarkSummaryCard.tsx.
import type { LarkPart } from "./inbox";

type Meta = Record<string, unknown>;

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const arr = (v: unknown): Meta[] => (Array.isArray(v) ? v.filter((x): x is Meta => !!x && typeof x === "object") : []);

/** The summary variant of a card part's metadata, or "" for a card drawn from its element tree. */
export function summaryVariant(meta: Meta | undefined): string {
  return meta ? str(meta.variant) : "";
}

/** ISO timestamps in summary-card fields read as local times; anything else is shown as is. */
export function fieldValue(s: string, locale?: string): string {
  if (s.length < 20 || !s.includes("T")) return s;
  const ts = Date.parse(s);
  if (!Number.isFinite(ts) || ts < 86400_000) return s;
  return new Intl.DateTimeFormat(locale, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(ts);
}

/** A card's facts with a value, label first. */
export function summaryFields(meta: Meta, locale?: string): { label: string; value: string }[] {
  return arr(meta.fields)
    .map((f) => ({ label: str(f.label), value: str(f.value) }))
    .filter((f) => f.label && f.value)
    .map((f) => ({ label: f.label, value: fieldValue(f.value, locale) }));
}

const ENTITIES: Record<string, string> = {
  "&nbsp;": "\u00a0", "&ensp;": "\u2002", "&emsp;": "\u2003", "&lt;": "<", "&gt;": ">", "&quot;": "\"",
  "&#39;": "'", "&apos;": "'", "&sim;": "~", "&amp;": "&",
};

/** Decodes the HTML character references Feishu leaves in card text. */
export function entities(s: string): string {
  if (!s.includes("&")) return s;
  return s
    .replace(/&(nbsp|ensp|emsp|lt|gt|quot|#39|apos|sim|amp);/gi, (m) => ENTITIES[m.toLowerCase()] ?? m)
    .replace(/&#(x?)([0-9a-f]{1,7});/gi, (m, hex: string, n: string) => {
      const code = parseInt(n, hex ? 16 : 10);
      return code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : m;
    });
}

/** A description's text: line breaks for its block tags, the rest of the markup dropped. */
export function stripHTML(s: string): string {
  if (!s.includes("<")) return entities(s);
  return entities(s.replace(/<br\s*\/?>|<\/p>|<\/div>/gi, "\n").replace(/<[^>]+>/g, "")).trim();
}

/** A message inside a merge-forward card: the Lark gateway's own MessageView shape (camelCase), media pointing at the hub. */
export interface ForwardedMessage {
  id: string;
  senderName?: string;
  senderAvatarUrl?: string;
  createdAt?: string;
  parts?: LarkPart[];
  textPreview?: string;
  text?: string;
  isRecalled?: boolean;
  hasRetainedRecallContent?: boolean;
  olk?: unknown;
}

/** Every forwarded message of a merge-forward card. */
export function forwardedMessages(meta: Meta): ForwardedMessage[] {
  return arr(meta.messages).map((m, i) => ({ ...(m as unknown as ForwardedMessage), id: str(m.id) || `fw-${i}` }));
}

/** The previews a merge-forward card shows (its first few messages when the Lark gateway gave no previews). */
export function forwardPreviews(meta: Meta): Meta[] {
  const messages = arr(meta.messages);
  return (messages.length ? messages : arr(meta.previews)).slice(0, 4);
}

/** How many messages a merge-forward holds. */
export function forwardCount(meta: Meta): number {
  const n = meta.messageCount;
  return typeof n === "number" && n >= 0 ? n : arr(meta.messages).length || arr(meta.previews).length;
}

/** A forwarded message's own text on one line: its preview, else its parts' text. Recalled and encrypted ones are
 * said by the caller (they read the interface language). */
export function forwardedText(m: Meta): string {
  const own = str(m.textPreview) || str(m.text);
  const text = own || arr(m.parts).map((p) => str(p.text)).join("");
  return text.replace(/\*\*|__|`/g, "").replace(/\s+/g, " ").trim();
}

/** Lark gateway media paths (old /api/media/, agent API /agent/v1/media/) mapped onto the hub's /api/v1/lark/media/.
 * Forwarded messages carry them, and so do inbox items stored before the extension rewrote them (#1353). */
export function hubMediaURL(s: string | undefined): string | undefined {
  if (!s) return undefined;
  for (const prefix of ["/api/media/", "/agent/v1/media/"]) {
    if (s.startsWith(prefix)) return "/api/v1/lark/media/" + s.slice(prefix.length);
  }
  return s;
}

export type SummaryTone = "accent" | "red" | "orange" | "green";

/** The accent bar's color of a variant. */
export function summaryTone(variant: string): SummaryTone {
  switch (variant) {
    case "hongbao":
      return "red";
    case "calendar":
      return "orange";
    case "todo":
    case "task":
      return "green";
    default:
      return "accent";
  }
}

/** The meeting number a card offers to copy. */
export function meetingNumber(meta: Meta): string | undefined {
  const copy = arr(meta.actions).find((a) => a.type === "copyMeetingNumber");
  return str(copy?.value) || undefined;
}

/** A vote's options and whether more than one may be picked. */
export function voteOptions(meta: Meta): { options: string[]; multiple: boolean } {
  const vote = meta.vote as Meta | undefined;
  if (!vote || typeof vote !== "object") return { options: [], multiple: false };
  const max = typeof vote.maxPick === "number" ? vote.maxPick : 1;
  return { options: arr(vote.options).map((o) => str(o.content)).filter(Boolean), multiple: max > 1 };
}
