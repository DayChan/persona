import { APP_SCHEMES } from "./compatibility";
// The assistant's cards in a reply's text (docs/assistant/assistant-views.md "Card identity"): where the ```persona-view blocks are and
// what each one's key is, by the same rules as the hub (sdk/go/wire/assistantblocks.go, the shared vectors in
// schema/extension/examples/assistant-text/fences.json): standalone CommonMark fences only (backticks or tildes, a
// longer outer fence, any case of the language, LF / CRLF / CR), an example inside another code block does not count,
// a block that does not read still takes its number, and one never closed shows its fallback only.
// Also: the plain checks a card must pass before it is drawn (CheckAssistantView's client side), the open targets an
// action may name (CheckAssistantOpen), and the strict <persona-action> tag the hub ends a card's reply with.

import { KNOWN_ITEMS } from "./extension-ui";

export interface AssistantBlock {
  /** The n-th persona-view block of the text, from 0; -1 for a persona-card block (not numbered). */
  index: number;
  start: number;
  end: number;
  language: string;
  raw: string;
  complete: boolean;
}

/** At most this many bytes of a card (AssistantViewMost). */
export const CARD_MOST = 64 << 10;
/** At most this many named pages (AssistantPagesMost). */
export const PAGES_MOST = 8;
/** The most characters of a fallback, a reply or an HTML bridge text. */
export const TEXT_MOST = 2000;

function fence(line: string): { char: string; n: number; info: string } | null {
  line = line.replace(/\n$/, "").replace(/\r$/, "");
  const spaces = line.length - line.replace(/^ +/, "").length;
  if (spaces > 3) return null;
  line = line.slice(spaces);
  if (line.length < 3 || (line[0] !== "`" && line[0] !== "~")) return null;
  const char = line[0];
  let n = 0;
  while (n < line.length && line[n] === char) n++;
  if (n < 3) return null;
  let info = line.slice(n).trim();
  if (char === "`" && info.includes("`")) return null;
  const first = info.split(/\s+/).filter(Boolean)[0];
  if (first) info = first;
  return { char, n, info: info.toLowerCase() };
}

/** The persona-view and persona-card blocks of a text, in order (AssistantViewBlocks). */
export function assistantBlocks(text: string): AssistantBlock[] {
  const out: AssistantBlock[] = [];
  let char = "";
  let length = 0;
  let start = 0;
  let content = 0;
  let index = 0;
  let language = "";
  let position = 0;
  const card = (l: string) => l === "persona-view" || l === "persona-card";
  while (position < text.length) {
    let end = text.length;
    const m = /[\r\n]/.exec(text.slice(position));
    if (m) {
      end = position + m.index + 1;
      if (text[end - 1] === "\r" && end < text.length && text[end] === "\n") end++;
    }
    const line = text.slice(position, end);
    const f = fence(line);
    if (!char) {
      if (f) [char, length, language, start, content] = [f.char, f.n, f.info, position, position + line.length];
    } else if (f && f.char === char && f.n >= length && f.info === "") {
      if (card(language)) {
        const ordinal = language === "persona-view" ? index++ : -1;
        out.push({ index: ordinal, start, end: position + line.length, language, raw: text.slice(content, position), complete: true });
      }
      char = "";
    }
    position += line.length;
  }
  if (char && card(language)) out.push({ index: language === "persona-view" ? index : -1, start, end: text.length, language, raw: text.slice(content), complete: false });
  return out;
}

/** A text block's key: `<item id>/<n>` (AssistantBlockKey). Opaque past here: never split on / or #. */
export const blockKey = (itemId: string, index: number) => `${itemId}/${index}`;

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

/** The same JSON, whatever its spacing: how a rendered block is told apart from the others of its text. */
export function sameJSON(a: string, b: string): boolean {
  try {
    return JSON.stringify(JSON.parse(a)) === JSON.stringify(JSON.parse(b));
  } catch {
    return false;
  }
}

/**
 * Which numbered block of `text` a drawn code block is: the one with the same JSON. Undefined when none or more than
 * one is (a block nested in a list, two identical cards): it is drawn, but no answer is kept for it.
 */
export function blockOf(text: string, code: string): AssistantBlock | undefined {
  const found = assistantBlocks(text).filter((b) => b.language === "persona-view" && sameJSON(b.raw, code));
  return found.length === 1 ? found[0] : undefined;
}

/** A card's plain-text fallback, when there is one under the exact field name (never HTML or raw JSON). */
export function cardFallback(raw: unknown): string {
  if (!isObject(raw) || typeof raw.fallback !== "string") return "";
  return raw.fallback.trim().slice(0, TEXT_MOST);
}

/** The JSON a card is, read and held to the hub's rules, or why it is not one (it then shows its fallback). */
export function checkCard(code: string): { raw: Record<string, unknown> } | { problem: string; fallback: string } {
  if (new TextEncoder().encode(code).length > CARD_MOST) return { problem: "too_large", fallback: "" };
  let raw: unknown;
  try {
    raw = JSON.parse(code);
  } catch {
    return { problem: "json", fallback: "" };
  }
  const fallback = cardFallback(raw);
  if (!isObject(raw)) return { problem: "object", fallback };
  if (!fallback) return { problem: "fallback", fallback };
  if (typeof raw.type === "string") {
    const { fallback: fb, ...item } = raw;
    raw = { fallback: fb, sections: [{ items: [item] }] };
  }
  const view = raw as Record<string, unknown>;
  if (!Array.isArray(view.sections)) return { problem: "sections", fallback };
  const pages = isObject(view.pages) ? view.pages : {};
  if (Object.keys(pages).length > PAGES_MOST) return { problem: "pages", fallback };
  const ids = new Set<string>();
  const check = (page: unknown, root: boolean): string => {
    if (!isObject(page)) return "page";
    if (!root && page.pages !== undefined) return "nested_pages";
    for (const s of Array.isArray(page.sections) ? page.sections : []) {
      for (const it of isObject(s) && Array.isArray(s.items) ? s.items : []) {
        if (!isObject(it)) continue;
        if (it.type === "field" && it.kind === "secret") return "secret";
        for (const a of [it.action, isObject(it.button) ? it.button.action : undefined]) {
          if (!isObject(a)) continue;
          if (typeof a.id !== "string" || !a.id || ids.has(a.id)) return "action_id";
          ids.add(a.id);
          const p = actionProblem(a, pages);
          if (p) return p;
        }
      }
    }
    return "";
  };
  const problem = check(view, true) || Object.values(pages).map((p) => check(p, false)).find(Boolean) || (cardKnown(view) ? "" : "component");
  return problem ? { problem, fallback } : { raw: view };
}

/** A component this web draws in a card (html shows its fallback); any other, the whole card shows its fallback, like the hub refuses it. */
const cardItem = (it: Record<string, unknown>) => typeof it.type === "string" && (it.type === "html" || KNOWN_ITEMS.includes(it.type));

/** Whether every component of a card (its pages too) is one this web draws: a live card from a newer hub may not be. */
export function cardKnown(raw: Record<string, unknown>): boolean {
  const pages = isObject(raw.pages) ? Object.values(raw.pages) : [];
  return [raw, ...pages].every((page) => {
    if (!isObject(page)) return true;
    if (typeof page.type === "string") return cardItem(page);
    const sections = Array.isArray(page.sections) ? page.sections : [];
    return sections.every((s) => !isObject(s) || !Array.isArray(s.items) || s.items.every((it) => !isObject(it) || cardItem(it)));
  });
}

/** checkAssistantAction: exactly one of reply, open, push (a root page) or pop; submit only with reply. */
function actionProblem(a: Record<string, unknown>, pages: Record<string, unknown>): string {
  const push = isObject(a.push) ? a.push : undefined;
  const kinds = [typeof a.reply === "string" && a.reply !== "", typeof a.open === "string" && a.open !== "", !!push, a.pop === true].filter(Boolean).length;
  if (kinds !== 1) return "action_kind";
  if (a.submit === true && !a.reply) return "action_submit";
  if (typeof a.reply === "string" && a.reply && !a.reply.trim()) return "action_reply";
  if (typeof a.open === "string" && a.open && openProblem(a.open)) return "action_open";
  if (push && (typeof push.page !== "string" || !push.page || push.ui || push.state || !(push.page in pages))) return "action_push";
  return "";
}

// ── open targets (CheckAssistantOpen) ──

export type OpenTarget =
  | { kind: "web"; url: string }
  | { kind: "session"; mid: string; sid: string }
  | { kind: "task"; id: string }
  | { kind: "kb"; path: string[] }
  | { kind: "file"; mid: string; path: string };

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;

/** Why `target` may not be opened ("" it may). */
export function openProblem(target: string): string {
  return openTarget(target) ? "" : "open";
}

/** What an open action opens, or undefined when it is not one the apps open (never handed to the system). */
export function openTarget(target: string): OpenTarget | undefined {
  if (CONTROL.test(target) || target.includes("\\")) return undefined;
  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(target)?.[1]?.toLowerCase();
  if (scheme === "https") {
    let u: URL;
    try {
      u = new URL(target);
    } catch {
      return undefined;
    }
    return u.username || u.password || !u.hostname ? undefined : { kind: "web", url: u.href };
  }
  if (!scheme || !APP_SCHEMES.has(scheme)) return undefined;
  const m = /^[a-z]+:\/\/([^/?#]*)(\/[^?#]*)?$/i.exec(target);
  if (!m || /[@:]/.test(m[1])) return undefined;
  const host = m[1].toLowerCase();
  const parts = (m[2] ?? "").replace(/^\//, "").split("/");
  const decoded: string[] = [];
  for (const p of parts) {
    let v: string;
    try {
      v = decodeURIComponent(p);
    } catch {
      return undefined;
    }
    if (!v || CONTROL.test(v) || v.includes("\\")) return undefined;
    if (host !== "file" && v.includes("/")) return undefined;
    decoded.push(v);
  }
  switch (host) {
    case "session":
      return decoded.length === 2 ? { kind: "session", mid: decoded[0], sid: decoded[1] } : undefined;
    case "task":
      return decoded.length === 1 ? { kind: "task", id: decoded[0] } : undefined;
    case "kb":
      return decoded.some((p) => p === "." || p === "..") ? undefined : { kind: "kb", path: decoded };
    case "file":
      return decoded.length === 2 && !decoded[0].includes("/") && decoded[1].startsWith("/") ? { kind: "file", mid: decoded[0], path: decoded[1] } : undefined;
  }
  return undefined;
}

// ── the tag a card's reply ends with ──

export interface CardReply {
  /** What the message says before the tag (the reply and the form's lines). */
  text: string;
  view: string;
  action: string;
  values: Record<string, string>;
}

const unescapeXML = (s: string) =>
  s.replace(/&(lt|gt|amp|quot|#34|#39|apos);/g, (_, e: string) => ({ lt: "<", gt: ">", amp: "&", quot: '"', "#34": '"', "#39": "'", apos: "'" })[e] ?? "");

// Exactly what the hub writes (viewReplyMessage): a last line, nothing after it, attributes XML-escaped, a JSON object
// whose only key is values (string → string).
const TAG = /\n<persona-action view="([^"<>]*)" action="([^"<>]*)">(\{[^<>]*\})<\/persona-action>$/;

/** A message sent from a card: its text without the tag and what was answered; undefined for any other message. */
export function cardReply(message: string): CardReply | undefined {
  const m = TAG.exec(message);
  if (!m) return undefined;
  let payload: unknown;
  try {
    payload = JSON.parse(m[3]);
  } catch {
    return undefined;
  }
  if (!isObject(payload) || Object.keys(payload).some((k) => k !== "values")) return undefined;
  const values = payload.values ?? {};
  if (!isObject(values) || Object.values(values).some((v) => typeof v !== "string")) return undefined;
  const view = unescapeXML(m[1]);
  const action = unescapeXML(m[2]);
  if (!view || !action) return undefined;
  return { text: message.slice(0, m.index), view, action, values: values as Record<string, string> };
}
