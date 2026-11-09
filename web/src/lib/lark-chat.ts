// What the Feishu chat page (views/inbox/LarkThread.tsx) does besides reading and sending, after the phone's chat
// (iOS LarkChatView and friends): card buttons (LarkCardButton), @ in a group (LarkMentions), read receipts of my
// messages (LarkReadReceipts), reactions (LarkReactionsMoments), profile cards (LarkPeople), drafts per chat
// (LarkDrafts), and the chat's pictures for the viewer. The pure parts are tested without a DOM; the hub calls are thin.
import { api, ApiError, enc, getToken } from "./api";
import { comparePosition, mergeMessages, type LarkMessage, type LarkPage, type LarkPart } from "./inbox";
import { larkPath } from "./lark-accounts";
import { withManager } from "./manager-choice";
import type { LarkPerson } from "./lark-people";

// ── card buttons ─────────────────────────────────────────

/** A button of a Feishu card: a link opens, a callback (`actionMethod` 2) is pressed as the user. */
export interface CardButton {
  /** What it says; "" for a button that only shows an icon. */
  label: string;
  /** Its tooltip when it has one (an icon-only button's accessible name). */
  tooltip?: string;
  /** The icon it leads with (`standard_icon` / `custom_icon` / `ud_icon`): a UD token, else a picture. */
  icon?: { token?: string; image?: string };
  /** The card's button style (`primary`, `danger`, `text`…; "default" when none). */
  kind: string;
  url?: string;
  action?: string;
  /** Turned off by the card, or neither a link nor a callback: drawn, not pressable (iOS LarkCardButton). */
  disabled: boolean;
}

function nodeText(n: Record<string, unknown>): string {
  const kids = Array.isArray(n.children) ? (n.children as Record<string, unknown>[]) : [];
  const own = typeof n.text === "string" ? n.text : n.text && typeof n.text === "object" && typeof (n.text as { content?: unknown }).content === "string" ? (n.text as { content: string }).content : "";
  if (own && !kids.length) return own;
  const inner = kids.map((k) => (k && typeof k === "object" ? nodeText(k) : "")).join("");
  return inner || own;
}

const ICON_TAGS = new Set(["standard_icon", "custom_icon", "ud_icon"]);

function iconOf(n: Record<string, unknown>): CardButton["icon"] {
  const kids = Array.isArray(n.children) ? (n.children as unknown[]) : [];
  const k = kids.find((c): c is Record<string, unknown> => Boolean(c) && typeof c === "object" && ICON_TAGS.has(String((c as { tag?: unknown }).tag)));
  if (!k) return undefined;
  const token = typeof k.token === "string" && k.token ? k.token : undefined;
  const img = k.image && typeof k.image === "object" ? (k.image as { url?: unknown }).url : undefined;
  const image = typeof img === "string" && (img.startsWith("/api/") || /^https:/i.test(img)) ? img : undefined;
  return { token, image };
}

/** The card's buttons in order (the `overflow` menu's options too), as iOS draws them: an icon-only one is kept, one
 * that does nothing is kept turned off; one with neither words nor an icon is left out. */
export function cardButtons(meta: Record<string, unknown> | undefined): CardButton[] {
  const out: CardButton[] = [];
  const add = (o: Record<string, unknown>, label: string, style?: unknown) => {
    const url = typeof o.url === "string" && /^https?:/i.test(o.url) ? o.url : undefined;
    const action = typeof o.actionId === "string" && o.actionMethod === 2 ? o.actionId : undefined;
    const tooltip = typeof o.tooltip === "string" && o.tooltip.trim() ? o.tooltip.trim() : undefined;
    const icon = iconOf(o);
    const text = label.trim();
    if (!text && !icon && !tooltip) return;
    const kind = style && typeof style === "object" && typeof (style as { type?: unknown }).type === "string" ? (style as { type: string }).type : "default";
    out.push({ label: text, kind, url, action, disabled: o.disabled === true || (!url && !action), ...(tooltip ? { tooltip } : {}), ...(icon ? { icon } : {}) });
  };
  const walk = (node: unknown) => {
    if (Array.isArray(node)) return node.forEach(walk);
    if (!node || typeof node !== "object") return;
    const o = node as Record<string, unknown>;
    if (o.tag === "button") return add(o, nodeText(o), o.style);
    if (o.tag === "overflow") {
      for (const opt of Array.isArray(o.options) ? o.options : []) if (opt && typeof opt === "object") add(opt as Record<string, unknown>, nodeText(opt as Record<string, unknown>));
      return;
    }
    for (const [k, v] of Object.entries(o)) if (k !== "header" && v && typeof v === "object") walk(v);
  };
  if (meta) walk(meta.body ?? meta.elements);
  return out;
}

/** A UD icon token drawn with the closest of the page's icons (iOS LarkCardIcon.symbol); undefined when none fits. */
export function cardIconName(token: string | undefined): string | undefined {
  const t = token?.toLowerCase();
  if (!t) return undefined;
  for (const [k, name] of CARD_ICONS) if (t.includes(k)) return name;
  return undefined;
}

const CARD_ICONS: [string, string][] = [
  ["warning", "warning"], ["alert", "warning"], ["error", "x"], ["close", "x"], ["cancel", "x"],
  ["info", "info"], ["succeed", "check"], ["success", "check"], ["done", "check"], ["check", "check"], ["yes", "check"],
  ["time", "clock"], ["clock", "clock"], ["calendar", "calendar"], ["member", "person"], ["group", "person"], ["people", "person"],
  ["user", "person"], ["contacts", "person"], ["link", "link"], ["external", "external"], ["doc", "read"], ["file", "read"],
  ["folder", "folder"], ["bell", "bell"], ["notice", "bell"], ["star", "star"], ["like", "heart"], ["chat", "chat"],
  ["message", "chat"], ["mail", "mail"], ["search", "search"], ["setting", "gear"], ["edit", "pencil"], ["delete", "trash"],
  ["add", "plus"], ["refresh", "retry"], ["down", "chevron"], ["robot", "sparkles"], ["ai", "sparkles"], ["lock", "lock"],
  ["chart", "chart"], ["data", "chart"], ["image", "image"], ["tag", "tag"], ["pin", "pin"], ["more", "more"],
];

/** Presses a card's callback button as the user (`POST /lark/messages/{id}/card-actions/{action}`). */
export function pressCardAction(messageID: string, action: string, ol?: number, manager?: string): Promise<unknown> {
  return api(larkPath(`/lark/messages/${enc(messageID)}/card-actions/${enc(action)}`, ol, manager), { method: "POST", json: {}, token: getToken() });
}

/** A page read around one message (`around=`, after a card button) merged into the loaded window. It overlaps the
 * window, so nothing between goes missing; where it reaches past an end, that end's cursor is the new page's. */
export function mergeAround(cur: LarkPage, got: LarkPage): LarkPage {
  const before = cur.messages;
  const add = mergeMessages([], got.messages ?? []);
  const out: LarkPage = { ...cur, messages: mergeMessages(before, add) };
  if (!add.length) return out;
  const first = before[0]?.position;
  const last = before[before.length - 1]?.position;
  if (first === undefined || comparePosition(add[0].position, first) < 0) {
    out.has_older = got.has_older;
    out.older_cursor = got.older_cursor;
  }
  if (last === undefined || comparePosition(add[add.length - 1].position, last) > 0) {
    out.has_newer = got.has_newer;
    out.newer_cursor = got.newer_cursor;
  }
  return out;
}

// ── media ────────────────────────────────────────────────

/** The chat's pictures in order (the viewer goes through them all), as `src` after `resolve`. */
export function chatImages(messages: LarkMessage[], resolve: (url?: string) => string | undefined): { id: string; src: string }[] {
  const out: { id: string; src: string }[] = [];
  for (const m of messages) {
    if (m.recalled) continue;
    (m.parts ?? []).forEach((p, i) => {
      if (p.kind !== "image") return;
      const src = resolve(p.url);
      if (src) out.push({ id: `${m.id}#${i}`, src });
    });
  }
  return out;
}

/** 240 KB, 1.2 MB (LarkFormat.fileSize); "" when unknown. */
export function fileSize(v: unknown): string {
  const n = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN;
  if (!Number.isFinite(n) || n <= 0) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

/** 0:09, 1:05 (LarkFormat.duration); "" when unknown. */
export function duration(ms: unknown): string {
  const n = typeof ms === "number" ? ms : typeof ms === "string" ? Number(ms) : NaN;
  if (!Number.isFinite(n) || n <= 0) return "";
  const s = Math.max(1, Math.round(n / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** A voice message (a `media` part with an audio type), not a video. */
export const isVoice = (p: LarkPart) => p.kind === "media" && String(p.metadata?.mime ?? "").startsWith("audio/");

// ── @ in a group ─────────────────────────────────────────

/** A chat's members for the @ picker (`GET …/members?q=`): the group nickname first, else the name. */
export async function chatMembers(feed: string, query: string, ol: number | undefined, signal?: AbortSignal, manager?: string): Promise<LarkPerson[]> {
  const r = await api<{ members?: LarkPerson[] }>(larkPath(`/lark/conversations/${enc(feed)}/members?q=${enc(query)}&limit=30`, ol, manager), { signal, token: getToken() });
  return (r.members ?? []).filter((m) => m.user_id && m.name);
}

/** "Everyone" (`user_id` "all"), offered first in a group when nothing or a part of its name is typed. */
export function withEveryone(people: LarkPerson[], query: string, everyone: string): LarkPerson[] {
  const q = query.trim().toLowerCase();
  if (q && !everyone.toLowerCase().includes(q) && !"all".startsWith(q)) return people;
  return [{ user_id: "all", name: everyone }, ...people.filter((p) => p.user_id !== "all")];
}

// ── read receipts ────────────────────────────────────────

export interface ReadState {
  message_id: string;
  read_count: number;
  unread_count: number;
}

/** What sits beside my message: "Read" / "Unread" in a one-to-one chat; in a group a ring filling as people read, a tick once
 * all have. Nothing without a receipt or with nobody to read it. */
export type ReadMark = { kind: "read" } | { kind: "unread" } | { kind: "all" } | { kind: "progress"; read: number; total: number };

export function readMark(s: ReadState | undefined, group: boolean): ReadMark | null {
  if (!s || s.read_count < 0 || s.unread_count < 0 || s.read_count + s.unread_count <= 0) return null;
  if (!group) return { kind: s.unread_count === 0 ? "read" : "unread" };
  if (s.unread_count === 0) return { kind: "all" };
  return { kind: "progress", read: s.read_count, total: s.read_count + s.unread_count };
}

/** Which of my messages to ask about (LarkReadReceipts): mine on screen, in Feishu already, not recalled; up to 100 in
 * one request, one request every 3 s at most; one everyone read is not asked again; an old hub (404) not at all. */
export class ReadReceipts {
  static readonly batch = 100;
  static readonly gap = 3000;
  states = new Map<string, ReadState>();
  private onScreen = new Set<string>();
  private due = new Set<string>();
  private lastAsked = -Infinity;
  unsupported = false;

  static tracks(m: LarkMessage): boolean {
    return Boolean(m.is_mine) && !m.recalled && !m.sending && !m.failed && m.type !== "SYSTEM" && Boolean(m.id) && !m.id.startsWith("temp-");
  }

  private final(id: string) {
    const s = this.states.get(id);
    return Boolean(s && s.unread_count === 0 && s.read_count > 0);
  }

  appeared(m: LarkMessage) {
    if (!ReadReceipts.tracks(m)) return;
    this.onScreen.add(m.id);
    if (!this.final(m.id)) this.due.add(m.id);
  }

  disappeared(id: string) {
    this.onScreen.delete(id);
    this.due.delete(id);
  }

  /** Something changed in the chat: mine on screen that not everyone read are asked about again. */
  changed() {
    for (const id of this.onScreen) if (!this.final(id)) this.due.add(id);
  }

  /** Ms until the next request may go (0: now); null when there is nothing to ask. */
  wait(now: number): number | null {
    if (this.unsupported || !this.due.size) return null;
    return Math.max(0, ReadReceipts.gap - (now - this.lastAsked));
  }

  take(now: number): string[] {
    const ids = [...this.due].sort().slice(0, ReadReceipts.batch);
    for (const id of ids) this.due.delete(id);
    this.lastAsked = now;
    return ids;
  }

  record(got: ReadState[]) {
    for (const s of got) this.states.set(s.message_id, s);
  }

  /** An old hub without the route is not asked again; other failures wait for the next change. */
  failed(status?: number) {
    if (status === 404 || status === 405) {
      this.unsupported = true;
      this.due.clear();
    }
  }
}

export async function readStates(feed: string, ids: string[], ol?: number, manager?: string): Promise<ReadState[]> {
  const r = await api<{ states?: ReadState[] }>(larkPath(`/lark/conversations/${enc(feed)}/read-states`, ol, manager), { method: "POST", json: { message_ids: ids }, token: getToken() });
  return r.states ?? [];
}

export const statusOf = (e: unknown) => (e instanceof ApiError ? e.status : undefined);

// ── reactions ────────────────────────────────────────────

export interface LarkReaction {
  key: string;
  count: number;
  mine?: boolean;
  users?: string[];
}

export function reactionsOf(m: LarkMessage): LarkReaction[] {
  return (m.reactions ?? []).filter((r) => r && typeof r.key === "string" && r.count > 0);
}

/** My reaction `key` put on or taken off (what shows before the hub answers); keys compare without case. */
export function toggledReactions(rs: LarkReaction[], key: string, me: string): { reactions: LarkReaction[]; remove: boolean } {
  const i = rs.findIndex((r) => r.key.toLowerCase() === key.toLowerCase());
  const r = i >= 0 ? rs[i] : undefined;
  if (r?.mine) {
    const left = { ...r, count: r.count - 1, mine: false, users: (r.users ?? []).filter((u) => u !== me) };
    return { reactions: left.count > 0 ? rs.map((x, j) => (j === i ? left : x)) : rs.filter((_, j) => j !== i), remove: true };
  }
  if (r) return { reactions: rs.map((x, j) => (j === i ? { ...x, count: x.count + 1, mine: true, users: [...(x.users ?? []), me] } : x)), remove: false };
  return { reactions: [...rs, { key, count: 1, mine: true, users: [me] }], remove: false };
}

/** The quick row: the hub's quick reactions (recent first), each once, six of them (LarkQuickReactions.bar). */
export function quickBar(keys: string[], count = 6): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const k of keys) {
    const key = k.trim();
    if (!key || seen.has(key.toLowerCase())) continue;
    seen.add(key.toLowerCase());
    out.push(key);
    if (out.length === count) break;
  }
  return out;
}

export function react(messageID: string, key: string, remove: boolean, ol?: number, manager?: string): Promise<unknown> {
  return api(larkPath(`/lark/messages/${enc(messageID)}/reactions`, ol, manager), { method: "POST", json: { key, remove }, token: getToken() });
}

export interface EmojiCatalog {
  emojis: Record<string, { label?: string; url?: string }>;
  groups: { title: string; keys: string[] }[];
}

// by manager ("": the default's): each manager reads its own Lark (docs/assistant/managers.md "Clients")
const catalogs = new Map<string, Promise<EmojiCatalog>>();
const quicks = new Map<string, Promise<string[]>>();

/** Once per page and manager; a failure is asked again next time. */
function readOnce<T>(cache: Map<string, Promise<T>>, manager: string | undefined, read: () => Promise<T>): Promise<T> {
  const key = manager ?? "";
  let p = cache.get(key);
  if (!p) {
    p = read().catch((e) => {
      cache.delete(key);
      throw e;
    });
    cache.set(key, p);
  }
  return p;
}

/** Feishu's emoji (key → picture) of the Lark the chat's manager reads. */
export function emojiCatalog(manager?: string): Promise<EmojiCatalog> {
  return readOnce(catalogs, manager, () => api<Partial<EmojiCatalog>>(withManager("/lark/emojis", manager), { token: getToken() })
    .then((r) => ({ emojis: r.emojis ?? {}, groups: r.groups ?? [] })));
}

export function quickReactions(manager?: string): Promise<string[]> {
  return readOnce(quicks, manager, () => api<{ keys?: string[] }>(withManager("/lark/reactions/quick", manager), { token: getToken() }).then((r) => r.keys ?? []));
}

// ── profile ──────────────────────────────────────────────

export interface LarkProfile {
  displayName?: string;
  enName?: string;
  alias?: string;
  groupNickname?: string;
  avatarUrl?: string;
  description?: string;
  userType?: string;
  timezone?: string;
  customStatus?: { title?: string };
  relation?: { external?: boolean; tenantName?: string };
  fields?: { key: string; label?: string; value?: unknown; url?: string }[];
}

export function profile(userID: string, feed: string | undefined, ol?: number, manager?: string): Promise<LarkProfile> {
  return api<LarkProfile>(larkPath(`/lark/users/${enc(userID)}${feed ? `?feed_id=${enc(feed)}` : ""}`, ol, manager), { token: getToken() });
}

/** The 1:1 chat with someone (made when there is none yet). */
export async function directChat(userID: string, ol?: number, manager?: string): Promise<string> {
  const r = await api<{ feed_id?: string }>(larkPath(`/lark/users/${enc(userID)}/chat`, ol, manager), { method: "POST", json: {}, token: getToken() });
  if (!r.feed_id) throw new Error("no feed_id");
  return r.feed_id;
}

/** The rows of a profile card: each field with words to show (Feishu's labels for the usual keys), in order. */
export function profileRows(p: LarkProfile, labels: Record<string, string>): { label: string; value: string; url?: string }[] {
  const out: { label: string; value: string; url?: string }[] = [];
  for (const f of p.fields ?? []) {
    const value = typeof f.value === "string" ? f.value : typeof f.value === "number" ? String(f.value) : "";
    const label = f.label || labels[f.key] || "";
    if (!value.trim() || !label) continue;
    out.push({ label, value, url: f.url && /^https?:/i.test(f.url) ? f.url : undefined });
  }
  return out;
}

// ── drafts ───────────────────────────────────────────────

/** What was typed into a chat's box and not sent: the words, the message it quotes, who it @s. */
export interface LarkDraft {
  text: string;
  replyTo?: { id: string; sender_name?: string; text?: string };
  mentions?: LarkPerson[];
}

const DRAFT_PREFIX = "persona.lark-draft.";
type DraftStore = Pick<Storage, "getItem" | "setItem" | "removeItem">;

function store(): DraftStore | undefined {
  try {
    return typeof localStorage === "undefined" ? undefined : localStorage;
  } catch {
    return undefined;
  }
}

/** One draft per chat and Feishu account. */
export const draftKey = (feed: string, ol?: number) => `${DRAFT_PREFIX}${ol ?? 0}:${feed}`;

export function isEmptyDraft(d: LarkDraft): boolean {
  return !d.text.trim() && !d.replyTo;
}

export function loadDraft(key: string, s: DraftStore | undefined = store()): LarkDraft {
  try {
    const v = JSON.parse(s?.getItem(key) ?? "null") as LarkDraft | null;
    if (!v || typeof v.text !== "string") return { text: "" };
    return { text: v.text, replyTo: v.replyTo?.id ? v.replyTo : undefined, mentions: Array.isArray(v.mentions) ? v.mentions : undefined };
  } catch {
    return { text: "" };
  }
}

/** Keeps the draft (an empty one removes it). */
export function saveDraft(key: string, d: LarkDraft, s: DraftStore | undefined = store()) {
  try {
    if (isEmptyDraft(d)) s?.removeItem(key);
    else s?.setItem(key, JSON.stringify({ text: d.text, replyTo: d.replyTo, mentions: d.mentions?.length ? d.mentions : undefined }));
  } catch {
    // a full or blocked storage: the draft lives as long as the box
  }
}
