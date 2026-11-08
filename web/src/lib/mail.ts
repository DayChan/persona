// Mail beyond the inbox's own reply (docs/extensions/mail.md): writing a new mail or answering one by hand (the app's ComposeView),
// and marking a mail read once it is opened (MailDetailView). The accounts are read once per page load and shared.

import { t } from "../i18n";
import { useEffect, useState } from "react";
import { api, enc } from "./api";
import type { MailAccount } from "./inbox";
import { itemHeaders, useItemManager } from "./manager-choice";

export interface MailAddress {
  name?: string;
  email?: string;
}

export interface MailMessage {
  uid: number;
  message_id?: string;
  subject?: string;
  from?: MailAddress;
  to?: MailAddress[];
  cc?: MailAddress[];
  date?: string;
  text?: string;
  html?: string;
  seen?: boolean;
  references?: string[];
  attachments?: { id?: string; filename?: string; mime?: string; size?: number }[];
}

export function addressText(a?: MailAddress): string {
  return a ? (a.name && a.email ? `${a.name} <${a.email}>` : a.email || a.name || "") : "";
}

// ── writing ──────────────────────────────────────────────

export interface ComposeDraft {
  accountID: string;
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  body: string;
  inReplyTo?: string;
  references?: string[];
}

export function newDraft(accountID: string): ComposeDraft {
  return { accountID, to: "", cc: "", bcc: "", subject: "", body: "" };
}

/** Addresses typed into one field: separated by commas (either width), semicolons or new lines; `Name <a@b>` keeps the name. */
export function parseAddresses(s: string): MailAddress[] {
  return s
    .split(/[,，;；\n]/) // i18n-ignore: full-width separators people type
    .map((x) => x.trim())
    .filter(Boolean)
    .map((x) => {
      const m = /^(.*?)\s*<([^<>\s]+)>$/.exec(x);
      if (m) return m[1].replace(/^"|"$/g, "").trim() ? { name: m[1].replace(/^"|"$/g, "").trim(), email: m[2] } : { email: m[2] };
      return { email: x };
    });
}

const EMAIL = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/;

/** The addresses in a field that do not look like one (the dialog names them before sending). */
export function badAddresses(s: string): string[] {
  return parseAddresses(s)
    .map((a) => a.email ?? "")
    .filter((e) => !EMAIL.test(e));
}

function pad(n: number) {
  return String(n).padStart(2, "0");
}

/** 2026-10-28 14:05, the line above a quoted mail. */
function quoteDate(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A reply to `msg` from the account it came to: Re: subject, In-Reply-To / References, the first 40 lines quoted
 * (ComposeDraft.reply in the app). */
export function replyDraft(accountID: string, msg: MailMessage): ComposeDraft {
  const subject = msg.subject ?? "";
  const quoted = (msg.text ?? "").split("\n").slice(0, 40).map((l) => `> ${l}`).join("\n");
  const head = [quoteDate(msg.date), addressText(msg.from)].filter(Boolean).join(t("lib-mail.quote_head_separator"));
  return {
    accountID,
    to: msg.from?.email ?? "",
    cc: "",
    bcc: "",
    subject: /^re:/i.test(subject) ? subject : `Re: ${subject}`,
    body: `\n\n${t("lib-mail.quote_head", { head })}\n${quoted}`,
    inReplyTo: msg.message_id || undefined,
    references: [...(msg.references ?? []), ...(msg.message_id ? [msg.message_id] : [])],
  };
}

// Generated fields use bare addresses: display names containing separators cannot round-trip through the composer.
function recipientEmail(email?: string): string {
  const value = email?.trim() ?? "";
  return EMAIL.test(value) && !/[,;\uFF0C\uFF1B]/.test(value) ? value : "";
}

/** Reply to visible participants only, once each. Without the receiving identity, wait rather than include self.
 * A self-sent message uses its other recipients; if only Cc remains, promote the first one to To. */
export function replyAllDraft(accountID: string, msg: MailMessage, receivingEmail?: string): ComposeDraft | null {
  const mine = recipientEmail(receivingEmail);
  if (!mine) return null;
  const seen = new Set([mine.toLowerCase()]);
  const keep = (addresses: (MailAddress | undefined)[]) => addresses.flatMap((address) => {
    const email = recipientEmail(address?.email);
    const key = email.toLowerCase();
    if (!email || seen.has(key)) return [];
    seen.add(key);
    return [email];
  });
  const to = keep([msg.from, ...(msg.to ?? [])]);
  const cc = keep(msg.cc ?? []);
  if (!to.length && cc.length) to.push(cc.shift()!);
  return { ...replyDraft(accountID, msg), to: to.join(", "), cc: cc.join(", ") };
}

/** POST /api/v1/mail/send's body. The hub takes References as one string of ids (mail.SendRequest). */
export function sendBody(d: ComposeDraft) {
  const refs = (d.references ?? []).filter(Boolean);
  return {
    account_id: d.accountID,
    to: parseAddresses(d.to),
    cc: parseAddresses(d.cc),
    bcc: parseAddresses(d.bcc),
    subject: d.subject,
    text: d.body,
    ...(d.inReplyTo ? { in_reply_to: d.inReplyTo } : {}),
    ...(refs.length ? { references: refs.join(" ") } : {}),
  };
}

/** Sends as one of manager's accounts (another manager's mail is its own, docs/assistant/managers.md "Clients"; none: the default's). */
export async function sendMail(d: ComposeDraft, manager?: string): Promise<void> {
  await api("/mail/send", { method: "POST", json: sendBody(d), headers: itemHeaders(manager) });
}

/** Opened: the mail is read (best effort; the inbox's own state follows on its next push). */
export function markMailSeen(accountID: string, uid: number, mailbox: string, manager?: string): Promise<unknown> {
  return api(`/mail/messages/${enc(accountID)}/${uid}/flags`, { method: "POST", json: { mailbox, seen: true }, headers: itemHeaders(manager) }).catch(() => undefined);
}

// ── accounts ─────────────────────────────────────────────

// by manager ("": the default's): each manager keeps its own mail accounts
const known = new Map<string, MailAccount[]>();
const inFlight = new Map<string, Promise<MailAccount[]>>();
const accountsKey = (manager?: string) => (itemHeaders(manager) ? manager! : "");

/** The mail accounts of manager (none: the default's), read again on every call (calls while one is out share it);
 *  the last answer is kept. */
export function loadMailAccounts(manager?: string): Promise<MailAccount[]> {
  const key = accountsKey(manager);
  let p = inFlight.get(key);
  if (!p) {
    p = api<{ accounts?: MailAccount[] }>("/mail/accounts", { headers: itemHeaders(manager) })
      .then((r) => {
        const list = r.accounts ?? [];
        known.set(key, list);
        return list;
      })
      .finally(() => inFlight.delete(key));
    inFlight.set(key, p);
  }
  return p;
}

/** The accounts of the manager whose item is shown (ItemManager; none: the default's), the last known ones at once and
 *  the hub's answer when it comes. */
export function useMailAccounts(): MailAccount[] {
  const manager = useItemManager();
  const key = accountsKey(manager);
  const [state, setState] = useState(() => ({ key, list: known.get(key) ?? [] }));
  useEffect(() => {
    let live = true;
    loadMailAccounts(manager)
      .then((list) => live && setState({ key, list }))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [manager, key]);
  // A same-id account under another manager is not evidence of this item's receiving address.
  return state.key === key ? state.list : known.get(key) ?? [];
}

/** Tests only. */
export function resetMailForTests() {
  known.clear();
  inFlight.clear();
}
