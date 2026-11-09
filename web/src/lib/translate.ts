// Settings › Translation (docs/apple/ios.md "Translation", the app's Translator.swift): when it is on, content not in the target language is
// translated by the hub (its translation model): mail (on by default) and the Feishu chats picked in settings (none
// by default: there are too many messages). The settings live in this browser; the phone's are its own.
//
// Whether a text needs translating is decided here. The app asks the system's language recognizer; a browser has
// none it can rely on, so this looks at the scripts the text is written in and, for Latin script, at common words.

import { useEffect, useSyncExternalStore } from "react";
import { api, enc } from "./api";
import { itemHeaders } from "./manager-choice";

// ── settings ─────────────────────────────────────────────

export interface TranslateChat {
  id: string;
  name: string;
}

export interface TranslatePrefs {
  enabled: boolean;
  target: string;
  mail: boolean;
  chats: TranslateChat[];
}

/** The targets in `server/internal/assistantwire/inbox.go` (`TranslateLanguages`), as the app lists them. */
export const LANGUAGES: { code: string; name: string }[] = [
  { code: "zh-Hans", name: "简体中文" }, // i18n-ignore: each language by its own name
  { code: "zh-Hant", name: "繁體中文" }, // i18n-ignore: each language by its own name
  { code: "en", name: "English" },
  { code: "ja", name: "日本語" }, // i18n-ignore: each language by its own name
  { code: "ko", name: "한국어" },
  { code: "fr", name: "Français" },
  { code: "de", name: "Deutsch" },
  { code: "es", name: "Español" },
  { code: "ru", name: "Русский" },
  { code: "pt", name: "Português" },
];

export const DEFAULT_TARGET = "zh-Hans";

export function languageName(code: string): string {
  return LANGUAGES.find((l) => l.code === code)?.name ?? code;
}

const KEYS = { enabled: "translate.enabled", target: "translate.target", mail: "translate.mail", chats: "translate.larkChats" };

export function parseChats(raw: string | null): TranslateChat[] {
  if (!raw) return [];
  try {
    const v = JSON.parse(raw);
    return Array.isArray(v) ? v.filter((c) => c && typeof c.id === "string").map((c) => ({ id: c.id, name: typeof c.name === "string" ? c.name : c.id })) : [];
  } catch {
    return [];
  }
}

function readPrefs(): TranslatePrefs {
  const target = localStorage.getItem(KEYS.target);
  return {
    enabled: localStorage.getItem(KEYS.enabled) === "1",
    target: target && LANGUAGES.some((l) => l.code === target) ? target : DEFAULT_TARGET,
    mail: localStorage.getItem(KEYS.mail) !== "0",
    chats: parseChats(localStorage.getItem(KEYS.chats)),
  };
}

let prefs: TranslatePrefs | null = null;
const prefListeners = new Set<() => void>();

export function getTranslatePrefs(): TranslatePrefs {
  return (prefs ??= readPrefs());
}

export function setTranslatePrefs(patch: Partial<TranslatePrefs>) {
  const next = { ...getTranslatePrefs(), ...patch };
  prefs = next;
  localStorage.setItem(KEYS.enabled, next.enabled ? "1" : "0");
  localStorage.setItem(KEYS.target, next.target);
  localStorage.setItem(KEYS.mail, next.mail ? "1" : "0");
  if (next.chats.length) localStorage.setItem(KEYS.chats, JSON.stringify(next.chats));
  else localStorage.removeItem(KEYS.chats);
  prefListeners.forEach((l) => l());
}

// Another tab of this browser changed them.
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (!e.key?.startsWith("translate.")) return;
    prefs = readPrefs();
    prefListeners.forEach((l) => l());
  });
}

const subscribePrefs = (l: () => void) => {
  prefListeners.add(l);
  return () => {
    prefListeners.delete(l);
  };
};

export function useTranslatePrefs(): TranslatePrefs {
  return useSyncExternalStore(subscribePrefs, getTranslatePrefs);
}

/** Mail gets translated now. */
export function translatesMail(p: TranslatePrefs): boolean {
  return p.enabled && p.mail;
}

/** Messages of this Feishu chat get translated now. */
export function translatesChat(p: TranslatePrefs, feedID?: string): boolean {
  return Boolean(p.enabled && feedID && p.chats.some((c) => c.id === feedID));
}

/** The hub's translation of a mail item (done as it came in, for a phone that asked), when this browser shows them. */
export function hubTranslation(
  it: { mail?: unknown; translations?: Record<string, { subject: string; preview: string }> },
  p: TranslatePrefs,
): { subject: string; preview: string } | undefined {
  return translatesMail(p) && it.mail ? it.translations?.[p.target] : undefined;
}

// ── which language a text is in ─────────────────────────

/** The language part of a BCP 47 code: zh-Hans and zh-Hant are both zh. */
export function family(code: string): string {
  return code.split("-")[0].toLowerCase();
}

const LATIN_LANGS = ["en", "fr", "de", "es", "pt"] as const;

const STOPWORDS: Record<(typeof LATIN_LANGS)[number], Set<string>> = {
  en: new Set("the and is are was you your to of in it that for with on this we i be have has can could would will please thanks thank hi hello not do does what when".split(" ")),
  fr: new Set("le la les et est vous nous je de des une un pour que qui pas avec dans sur merci bonjour sont ce cette au aux".split(" ")),
  de: new Set("der die das und ist nicht ich sie wir ein eine mit für auf zu den dem danke bitte wie sind auch es".split(" ")),
  es: new Set("el la los las y es que de en un una por para con no gracias hola está son como pero del lo".split(" ")),
  pt: new Set("o a os as e é que de em um uma para com não obrigado obrigada você do da dos das mas como".split(" ")),
};

const ACCENTS: Record<(typeof LATIN_LANGS)[number], RegExp> = {
  en: /$^/,
  fr: /[èêëàâîïôûùœ]/g,
  de: /[äöüß]/g,
  es: /[ñ¿¡]/g,
  pt: /[ãõ]/g,
};

/**
 * The language a text is written in, as a family ("zh", "ja", "ko", "ru", "en", "fr", "de", "es", "pt"), "latin"
 * for Latin script whose language is unclear, "other" for another script, or null when there is too little to tell
 * (an emoji, "OK", a number).
 */
export function detectLanguage(text: string): string | null {
  const sample = text.slice(0, 1500);
  let han = 0, kana = 0, hangul = 0, cyrillic = 0, latin = 0, other = 0;
  for (const ch of sample) {
    if (/\p{Script=Han}/u.test(ch)) han++;
    else if (/[\p{Script=Hiragana}\p{Script=Katakana}]/u.test(ch)) kana++;
    else if (/\p{Script=Hangul}/u.test(ch)) hangul++;
    else if (/\p{Script=Cyrillic}/u.test(ch)) cyrillic++;
    else if (/\p{Script=Latin}/u.test(ch)) latin++;
    else if (/\p{L}/u.test(ch)) other++;
  }
  if (han + kana + hangul + cyrillic + latin + other < 6) return null;
  // A CJK character carries about as much as a short Latin word: weigh them so a Chinese sentence with a few English
  // terms in it is still Chinese.
  const scores: [string, number][] = [
    ["cjk", (han + kana) * 4],
    ["ko", hangul * 4],
    ["ru", cyrillic],
    ["latin", latin],
    ["other", other],
  ];
  const [best] = scores.reduce((a, b) => (b[1] > a[1] ? b : a));
  if (best === "cjk") return kana > 0 && kana * 10 >= han + kana ? "ja" : "zh";
  if (best !== "latin") return best;
  return latinLanguage(sample);
}

function latinLanguage(text: string): string {
  const lower = text.toLowerCase();
  const words = lower.match(/\p{L}+/gu) ?? [];
  const score = Object.fromEntries(LATIN_LANGS.map((l) => [l, 0])) as Record<(typeof LATIN_LANGS)[number], number>;
  for (const w of words) for (const l of LATIN_LANGS) if (STOPWORDS[l].has(w)) score[l]++;
  for (const l of LATIN_LANGS) score[l] += (lower.match(ACCENTS[l]) ?? []).length;
  const ranked = [...LATIN_LANGS].sort((a, b) => score[b] - score[a]);
  const [first, second] = ranked;
  return score[first] >= 2 && score[first] > score[second] ? first : "latin";
}

/**
 * Whether `text` is in another language than `target`. Too little text to tell is not; Latin script whose language
 * is unclear is foreign only to a target in another script (it might well be the target itself).
 */
export function isForeign(text: string, target: string): boolean {
  const lang = detectLanguage(text);
  if (!lang) return false;
  const t = family(target);
  if (lang === "latin") return !(LATIN_LANGS as readonly string[]).includes(t);
  return lang !== t;
}

const foreignMemo = new Map<string, boolean>();

/** `isForeign`, remembered per text (rows re-render often). */
export function isForeignCached(text: string, target: string): boolean {
  const k = target + "\u0000" + text;
  let v = foreignMemo.get(k);
  if (v === undefined) {
    v = isForeign(text, target);
    if (foreignMemo.size > 2000) foreignMemo.clear();
    foreignMemo.set(k, v);
  }
  return v;
}

/** A mail's HTML as rough text, enough to tell its language. */
export function strippedHTML(html: string): string {
  return html
    .replace(/<(style|script|head)[^>]*>[\s\S]*?<\/\1>/gi, " ")
    .replace(/<[^>]+>/g, " ")
    .replace(/&nbsp;/g, " ");
}

// ── the translations ─────────────────────────────────────

/** A mail's translation (POST /api/v1/translate/mail). */
export interface MailTranslation {
  subject: string;
  text: string;
  clipped?: boolean;
}

// The chat translations, least recently read first. A tab left open on chats for hours would keep every foreign
// message twice, so only the TEXTS_KEPT read last are kept, plus those of the lines on screen (pinned by useTranslation).
const texts = new Map<string, string>();
const failed = new Set<string>();
const pinned = new Map<string, number>();
const queued = new Map<string, { text: string; target: string }>();
let flushing: ReturnType<typeof setTimeout> | null = null;
const textListeners = new Set<() => void>();
let version = 0;

const textKey = (text: string, target: string) => target + "\u0000" + text;
const BATCH = 30;
export const TEXTS_KEPT = 2000;

/** The translation of a short text, if it came back already. */
export function translationOf(text: string, target: string): string | undefined {
  const k = textKey(text, target);
  const v = texts.get(k);
  if (v !== undefined) {
    texts.delete(k); // read now: the last to go
    texts.set(k, v);
  }
  return v;
}

function pin(k: string) {
  pinned.set(k, (pinned.get(k) ?? 0) + 1);
}

function unpin(k: string) {
  const n = (pinned.get(k) ?? 0) - 1;
  if (n > 0) pinned.set(k, n);
  else pinned.delete(k);
}

/** Drops the least recently read translations above TEXTS_KEPT, never one a line on screen shows. */
function trimTexts() {
  for (const k of texts.keys()) {
    if (texts.size <= TEXTS_KEPT) break;
    if (!pinned.has(k)) texts.delete(k);
  }
  for (const k of failed) {
    if (failed.size <= TEXTS_KEPT) break;
    failed.delete(k);
  }
}

/** Asks for a short text's translation; texts asked for within a moment go in one request (30 at a time). */
export function requestTranslation(text: string, target: string) {
  const k = textKey(text, target);
  if (texts.has(k) || queued.has(k) || failed.has(k)) return;
  queued.set(k, { text, target });
  flushing ??= setTimeout(() => void flush(), 300);
}

async function flush() {
  while (queued.size) {
    const target = queued.values().next().value!.target;
    const picked = [...queued.entries()].filter(([, v]) => v.target === target).slice(0, BATCH);
    for (const [k] of picked) queued.delete(k);
    try {
      const r = await api<{ texts?: string[] }>("/translate", { method: "POST", json: { texts: picked.map(([, v]) => v.text), target } });
      const out = r.texts ?? [];
      picked.forEach(([k], i) => {
        if (out[i]) texts.set(k, out[i]);
        else failed.add(k);
      });
    } catch {
      picked.forEach(([k]) => failed.add(k)); // not again this visit; the original stays readable
    }
    trimTexts();
    version++;
    textListeners.forEach((l) => l());
  }
  flushing = null;
}

const subscribeTexts = (l: () => void) => {
  textListeners.add(l);
  return () => {
    textListeners.delete(l);
  };
};

/** Re-renders when a batch of translations came back. */
function useTranslations(): number {
  return useSyncExternalStore(subscribeTexts, () => version);
}

/**
 * The translation of a short text a line on screen shows, once it came back (when `applies`): asked for as the line
 * mounts and kept while it stays mounted.
 */
export function useTranslation(text: string, target: string, applies: boolean): string | undefined {
  useTranslations();
  useEffect(() => {
    if (!applies) return;
    const k = textKey(text, target);
    pin(k);
    requestTranslation(text, target);
    return () => unpin(k);
  }, [applies, text, target]);
  return applies ? translationOf(text, target) : undefined;
}

// The mail translations, least recently asked first. MailBody keeps its own copy of the one it shows, so dropping
// one here only means asking the hub again when that mail opens again.
const mails = new Map<string, Promise<MailTranslation>>();
export const MAILS_KEPT = 50;

/** One mail's subject and body, translated by the hub from the message it reads itself; asked once per mail and target. */
export function translateMail(account: string, uid: number, mailbox: string, target: string, manager?: string): Promise<MailTranslation> {
  const k = [manager ?? "", account, mailbox, uid, target].join("\u0000");
  let p = mails.get(k);
  if (p) {
    mails.delete(k); // asked now: the last to go
  } else {
    // the mail is the manager's that reads it (an inbox item's, docs/assistant/managers.md "Clients")
    const asked = api<MailTranslation>("/translate/mail", { method: "POST", json: { account_id: account, uid, mailbox, target }, headers: itemHeaders(manager) });
    // a failure can be asked again next time (unless it was dropped and asked again already)
    asked.catch(() => mails.get(k) === asked && mails.delete(k));
    p = asked;
  }
  mails.set(k, p);
  for (const old of mails.keys()) {
    if (mails.size <= MAILS_KEPT) break;
    mails.delete(old);
  }
  return p;
}

/** The Feishu chats to pick from: the pinned ones, then the recent ones. */
export async function larkChatChoices(token: string): Promise<{ id: string; name: string; avatar?: string; is_group?: boolean }[]> {
  type Conv = { id: string; name?: string; avatar?: string; is_group?: boolean };
  const [pinned, recent] = await Promise.all([
    api<{ conversations?: Conv[] }>("/lark/pinned", { token }).catch(() => ({ conversations: [] as Conv[] })),
    api<{ conversations?: Conv[] }>(`/lark/conversations?scope=${enc("chats")}&limit=60`, { token }),
  ]);
  const out: Conv[] = [];
  for (const c of [...(pinned.conversations ?? []), ...(recent.conversations ?? [])]) if (!out.some((x) => x.id === c.id)) out.push(c);
  return out.map((c) => ({ id: c.id, name: c.name || c.id, avatar: c.avatar, is_group: c.is_group }));
}

/** Tests only. */
export function translateCacheSizesForTests() {
  return { texts: texts.size, failed: failed.size, pinned: pinned.size, mails: mails.size };
}

/** Tests only. */
export function resetTranslateForTests() {
  prefs = null;
  texts.clear();
  failed.clear();
  pinned.clear();
  queued.clear();
  mails.clear();
  foreignMemo.clear();
  if (flushing) clearTimeout(flushing);
  flushing = null;
}
