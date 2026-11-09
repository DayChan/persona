// Messages other pages send for a conversation ("Ask Persona" from "Knowledge", "Hand to AI" from the inbox, a new Persona conversation):
// the page moves to the conversation at once and the message shows there as a sending bubble until the transcript has
// it, as the app's outbox does (AppModel.sendNow / askPersona). A failed one stays with the reason, "Retry" and "Delete",
// where it was sent (placeFailed). The conversation's own composer keeps its local sending bubbles (ChatView); one of
// those that fails comes here (keepFailed), so its words stay in the chat and not in the box, as in the apps (#1122).
import { t } from "../i18n";
import { api, ApiError, errorText, sessionPath } from "./api";
import { pickedLauncher, pickedModel, conversationKey } from "./assistant-chat";
import { startConversation } from "./assistant-start";
import { uuid } from "./format";
import { navigate } from "./router";
import { getState, refreshAssistant, setOutbox } from "./store";
import { chooseManager, chosenManager, noteConversation, otherManager, ownerHeaders } from "./manager-choice";
import { apiOp, isTransient } from "./ops";
import { echoed, isEcho, type Seen, type Sent } from "./echo";
import { parseUserText, type ChatLine, type UserMessage } from "./turns";
import type { AssistantInfo, Item } from "./types";

export interface OutboxImage {
  mime: string;
  data: string;
}

export interface OutboxEntry {
  /** The client_msg_id: a retry sends the same one, so the hub takes it once. */
  id: string;
  /** It went in another way (the prompt that started a conversation), not with `id`: only its words find it. */
  wordsOnly?: boolean;
  text: string;
  images: OutboxImage[];
  at: number;
  /** sent: the hub has it (or it started the conversation); the bubble waits for the transcript. */
  state: "sending" | "sent" | "failed";
  error?: string;
  /** The computer's input box holds text (input_draft): what "Clear and send" clears, when it can. */
  clearable?: string;
  /** The hub could not tell whether it went in: a retry is a new message. */
  newId?: boolean;
  launcher?: string;
  model?: string;
}

/** How long a sent message waits for the transcript before its bubble goes (the transcript may word it differently). */
export const SENT_GRACE_MS = 60_000;

export function outboxKey(mid: string, sid: string) {
  return conversationKey(mid, sid);
}

/** The bubble an entry shows as: contexts as quote cards, the images, like the transcript will. */
export function outboxMessage(e: OutboxEntry): UserMessage {
  return {
    id: `outbox:${e.id}`,
    ...parseUserText(e.text),
    images: e.images.map((image, i): Item => ({ id: `outbox:${e.id}#${i}`, seq: 0, role: "user", kind: "image", image })),
  };
}

type Described = Pick<OutboxEntry, "text" | "images" | "at"> & Partial<Pick<OutboxEntry, "id" | "wordsOnly">>;

const seenAs = (e: Described): Sent => ({ id: e.wordsOnly ? undefined : e.id, text: parseUserText(e.text).text, images: e.images, at: e.at });

/** The transcript has this message: one of its latest user messages, or an item the hub tied to it, is its copy (lib/echo.ts). */
export function arrived(e: Described, users: Seen[]): boolean {
  return users.some((u) => isEcho(seenAs(e), u));
}

/** What stays once the transcript moved: arrived ones go (one copy each), and sent ones past the grace; sending and
 *  failed stay, but a failed one the hub tied a copy to went in after all (delivered unknown, #1121): the copy is it.
 *  `users`: the latest user messages and the items the hub tied to a message (taggedSeen). `claimed`: the ones that
 *  already answered one, kept across calls. */
export function settle(list: OutboxEntry[], users: Seen[], now: number, claimed = new Set<string>()): OutboxEntry[] {
  const shown = echoed(list.filter((e) => e.state !== "failed"), users, seenAs, claimed);
  const tied = new Set(users.flatMap((u) => u.client_msg_id ?? []));
  return list.filter((e) => !shown.has(e) && !(e.state === "failed" && !e.wordsOnly && tied.has(e.id)) && (e.state !== "sent" || now - e.at < SENT_GRACE_MS));
}

// When this page wrote the messages it sent, by client_msg_id: a failed message goes before the first one written
// after it (placeFailed), by this clock rather than the computer's.
const written = new Map<string, number>();

/** A message this page sends now (ChatView's own, or an outbox entry): its time, for placeFailed. */
export function noteWritten(id: string, at: number) {
  if (written.size >= 500) written.delete(written.keys().next().value!);
  written.set(id, at);
}

/** When a line was written, for placeFailed: an exchange's message (this page's time for one sent from here; else the
 *  computer's, two minutes' slack for its clock, so a near tie leaves the failed message below); none for other lines. */
function lineWritten(line: ChatLine): number | undefined {
  if (line.kind !== "segment" || !line.seg.user) return undefined;
  const mine = line.seg.user.client_msg_id ? written.get(line.seg.user.client_msg_id) : undefined;
  if (mine !== undefined) return mine;
  const ts = Date.parse(line.seg.user.ts ?? "");
  return Number.isNaN(ts) ? undefined : ts - 120_000;
}

/** Where failed messages go (#1122, ChatLines.placeFailed in the apps): one written before a message sent after it goes
 *  right before the first such message's exchange, so the chat reads in the order things were written. One with nothing
 *  after it, and every message still on its way, stay at the end (`rest`), in the order they were written. `before`: by
 *  the id of the line each goes before. */
export function placeFailed(lines: ChatLine[], list: OutboxEntry[]): { before: Map<string, OutboxEntry[]>; rest: OutboxEntry[] } {
  const ordered = [...list].sort((a, b) => a.at - b.at);
  const before = new Map<string, OutboxEntry[]>();
  if (!ordered.some((e) => e.state === "failed")) return { before, rest: ordered };
  const after = lines.flatMap((line) => {
    const at = lineWritten(line);
    return at === undefined ? [] : [{ id: line.id, at }];
  });
  const rest: OutboxEntry[] = [];
  for (const e of ordered) {
    const next = e.state === "failed" ? after.find((l) => l.at > e.at) : undefined;
    if (next) before.set(next.id, [...(before.get(next.id) ?? []), e]);
    else rest.push(e);
  }
  return { before, rest };
}

/** Why it didn't go, in words. */
export function failureText(e: unknown): string {
  if (e instanceof ApiError && e.code === "input_draft") return t("lib-outbox.input_draft");
  if (e instanceof ApiError && e.code === "dialog_open") return t("lib-outbox.dialog_open");
  if (e instanceof ApiError && e.body.delivered === "unknown") return t("lib-outbox.delivery_unknown");
  return errorText(e);
}

// ── the store's side ────────────────────────────────────

function list(key: string): OutboxEntry[] {
  return getState().outbox[key] ?? [];
}

function update(key: string, id: string, patch: Partial<OutboxEntry>) {
  setOutbox(key, list(key).map((e) => (e.id === id ? { ...e, ...patch } : e)));
}

const claimedBy = new Map<string, Set<string>>();

/** A fresh page for the tests: which transcript lines answered a sent message is kept for as long as the page runs
 *  (one answers no later message with the same words), so cases with the same chat and line ids start without it. */
export function resetOutboxClaimsForTests() {
  claimedBy.clear();
}

/** Drops what the transcript now has (ChatView calls it with the latest user messages and the tagged items). */
export function settleOutbox(mid: string, sid: string, users: Seen[]) {
  const key = outboxKey(mid, sid);
  const before = list(key);
  if (!before.length) return;
  const claimed = claimedBy.get(key) ?? new Set<string>();
  claimedBy.set(key, claimed);
  const after = settle(before, users, Date.now(), claimed);
  if (after.length !== before.length) setOutbox(key, after);
}

/** A failed entry's fields for the error its send ended with. */
function failedWith(err: unknown): Pick<OutboxEntry, "state" | "error" | "clearable" | "newId"> {
  return {
    state: "failed",
    error: failureText(err),
    clearable: err instanceof ApiError && err.code === "input_draft" && typeof err.body.clearable === "string" ? err.body.clearable : undefined,
    // the hub answered: it keeps that answer for this id, so a retry is another operation; a gap it never
    // answered is retried with the same id (the hub does it at most once)
    newId: !isTransient(err),
  };
}

/** A message this conversation's own composer sent that did not go (ChatView): from now on a failed bubble here, with
 *  why, "Retry" and "Delete", where it was sent; the box stays empty (#1122). */
export function keepFailed(mid: string, sid: string, sent: Pick<OutboxEntry, "id" | "text" | "images" | "at" | "launcher" | "model">, err: unknown) {
  const key = outboxKey(mid, sid);
  setOutbox(key, [...list(key).filter((e) => e.id !== sent.id), { ...sent, ...failedWith(err) }]);
}

async function deliver(mid: string, sid: string, e: OutboxEntry, clearDraft?: string): Promise<boolean> {
  const key = outboxKey(mid, sid);
  try {
    // stays sending through a gap (a hub restart); the client_msg_id is the op id
    await apiOp(`${sessionPath(mid, sid)}/messages`, {
      method: "POST",
      opId: e.id,
      json: { text: e.text, images: e.images.length ? e.images : undefined, client_msg_id: e.id, launcher: e.launcher || undefined, model: e.model || undefined, clear_draft: clearDraft },
    });
    update(key, e.id, { state: "sent", at: Date.now(), error: undefined, clearable: undefined });
    return true;
  } catch (err) {
    update(key, e.id, failedWith(err));
    return false;
  }
}

/** Shows the message in that conversation and sends it; false when it didn't go (the bubble says why). */
export function sendToSession(mid: string, sid: string, out: { text: string; images?: OutboxImage[] }, opts: { launcher?: string; model?: string } = {}): Promise<boolean> {
  return queueToSession(mid, sid, out, opts).sent;
}

/** sendToSession, also naming the entry it made: for a place that offers its own retry of it (the overview's Continue). */
export function queueToSession(mid: string, sid: string, out: { text: string; images?: OutboxImage[] }, opts: { launcher?: string; model?: string } = {}): { id: string; sent: Promise<boolean> } {
  const key = outboxKey(mid, sid);
  const e: OutboxEntry = { id: uuid(), text: out.text, images: out.images ?? [], at: Date.now(), state: "sending", launcher: opts.launcher, model: opts.model };
  noteWritten(e.id, e.at);
  setOutbox(key, [...list(key), e]);
  return { id: e.id, sent: deliver(mid, sid, e) };
}

/** One entry of a conversation's outbox, as it stands. */
export function outboxEntry(mid: string, sid: string, id: string): OutboxEntry | undefined {
  return list(outboxKey(mid, sid)).find((e) => e.id === id);
}

/** A message that already went in another way (the prompt that started a conversation): its bubble until the transcript has it. */
export function noteSent(mid: string, sid: string, out: { text: string; images?: OutboxImage[] }) {
  const key = outboxKey(mid, sid);
  setOutbox(key, [...list(key), { id: uuid(), wordsOnly: true, text: out.text, images: out.images ?? [], at: Date.now(), state: "sent" }]);
}

export function retryOutbox(mid: string, sid: string, id: string, clearDraft?: string): Promise<boolean> {
  return retryOutboxEntry(mid, sid, id, clearDraft).sent;
}

/** retryOutbox, also naming the entry the retry is (a new id when the hub answered the last try). */
export function retryOutboxEntry(mid: string, sid: string, id: string, clearDraft?: string): { id: string; sent: Promise<boolean> } {
  const key = outboxKey(mid, sid);
  const e = list(key).find((x) => x.id === id);
  if (!e || e.state === "sending") return { id, sent: Promise.resolve(false) };
  // the hub said it may have gone in: the user looked and wants it sent again, as a new message
  const next: OutboxEntry = { ...e, id: e.newId ? uuid() : e.id, newId: false, state: "sending", error: undefined, at: Date.now() };
  noteWritten(next.id, next.at);
  setOutbox(key, list(key).map((x) => (x.id === id ? next : x)));
  return { id: next.id, sent: deliver(mid, sid, next, clearDraft) };
}

export function discardOutbox(mid: string, sid: string, id: string) {
  const key = outboxKey(mid, sid);
  setOutbox(key, list(key).filter((e) => e.id !== id));
}

/**
 * "Ask Persona": the text goes to Persona's main conversation (AppModel.askPersona), which opens at once with the message
 * on its way; with no main conversation yet, this one starts it (and becomes it). Throws only when starting fails.
 */
export async function askPersona(text: string): Promise<void> {
  let a = getState().assistant;
  if (!a?.main) {
    await refreshAssistant().catch(() => {});
    a = getState().assistant;
  }
  if (a?.main) {
    void sendToMain(a.machine_id, a.main, text);
    return;
  }
  const chosen = chosenManager() ?? undefined;
  const r = await startConversation({ prompt: text, main: true });
  // the manager the page showed when it started (it named that one): the conversation's requests go there
  noteConversation(r.session.session_id, chosen);
  noteSent(r.session.machine_id, r.session.session_id, { text });
  void refreshAssistant().catch(() => {});
  navigate({ name: "persona", sid: r.session.session_id });
}

/**
 * askPersona for one manager's main conversation (an inbox item's `manager`, none: the default's), whatever the assistant
 * page shows (Android's mainConversationOf): its main conversation is read from that manager, the page moves to that
 * manager and the conversation, and the text goes there; with none yet, the text starts it. Throws when the manager
 * can't be read or the conversation can't start, before the page moves; `sent` then says whether the text got there
 * (a failed bubble says why).
 */
export async function askPersonaOf(owner: string | undefined, text: string): Promise<{ sent: Promise<boolean> }> {
  const headers = ownerHeaders(owner);
  const a = await api<AssistantInfo>("/assistant", { headers });
  if (a.machine_id && a.main) {
    adoptConversation(owner, a.main);
    return { sent: sendToMain(a.machine_id, a.main, text) };
  }
  const r = await startConversation({ prompt: text, main: true }, headers);
  adoptConversation(owner, r.session.session_id);
  noteSent(r.session.machine_id, r.session.session_id, { text });
  void refreshAssistant().catch(() => {});
  navigate({ name: "persona", sid: r.session.session_id });
  return { sent: Promise.resolve(true) };
}

/** Opens a main conversation and sends text there with the launcher and model picked for it. */
function sendToMain(mid: string, sid: string, text: string): Promise<boolean> {
  navigate({ name: "persona", sid });
  const key = outboxKey(mid, sid);
  return sendToSession(mid, sid, { text }, { launcher: pickedLauncher(key), model: pickedModel(key) });
}

/** owner's conversation sid: its requests go to owner, and the assistant page shows owner (none: the default). */
export function adoptConversation(owner: string | undefined, sid: string) {
  noteConversation(sid, otherManager(owner));
  chooseManager(owner || null);
}
