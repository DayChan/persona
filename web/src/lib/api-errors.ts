// A model API error in a chat (iOS APIErrorInfo): read from the text Claude Code writes when it gives up
// ("API Error: 502 …", an answer) or from the hub's notice ("API error: Overloaded (gave up after 10 retries)", in Chinese). What a person
// needs first is in front: why, in words, the status code, and whether it is likely to pass by itself.

import { t } from "../i18n";

export interface APIErrorInfo {
  status?: number;
  /** One line, in words. */
  reason: string;
  /** Likely to pass if tried again later (5xx, rate limits, timeouts); false when someone has to act (login, a bad
   *  request); undefined when the text does not say. */
  transient?: boolean;
  requestId?: string;
  session?: string;
  /** What to fix, when the words say (not_logged_in / auth_failed / model_route): the hub's turn_error_code for the
   *  same turn wins over it. */
  code?: string;
  raw: string;
}

/** Two errors are "the same" for folding when these match. */
export function errorSignature(e: APIErrorInfo): string {
  return `${e.status ?? 0}|${e.reason}`;
}

/** The starts of Claude Code's answer when the model call itself failed, besides "API Error:" (the hub's
 *  APIErrorMarkers): the turn did not run. */
const MODEL_MARKERS = ["There's an issue with the selected model", "Failed to authenticate", "OAuth token has expired", "Invalid API key", "Not logged in ·"];

function fixCode(s: string): string | undefined {
  if (s.includes("safeguards flagged this message") || s.includes("[frontier_llm]")) return "safeguards";
  if (s.trimStart().startsWith("not logged in")) return "not_logged_in";
  if (s.includes("issue with the selected model")) return "model_route";
  if (s.includes("invalid api key") || s.includes("failed to authenticate") || s.includes("oauth token has expired")) return "auth_failed";
  return undefined;
}

const STATUS_RE = /^\s*(\d{3})\b/;
const REQUEST_RE = /\b(?:req(?:uest)?(?:[ _-]?id)?)[:=\s"]+([A-Za-z0-9_-]{8,})/i;
const SESSION_RE = /\bsession(?:[ _-]?id)?[:=\s"]+([A-Za-z0-9_:-]{6,})/i;

export function parseAPIError(text?: string): APIErrorInfo | null {
  const t = (text ?? "").trim();
  let body: string;
  if (t.startsWith("API Error:")) body = t.slice("API Error:".length);
  // i18n-ignore: the hub's notice for a model API error starts with these words
  else if (t.startsWith("API 错误：")) body = t.slice("API 错误：".length);
  // what Claude Code answers when it could not call the model at all (the hub's APIErrorMarkers, assistantwire/handler.go)
  else if (MODEL_MARKERS.some((m) => t.startsWith(m))) body = t;
  else return null;
  const m = STATUS_RE.exec(body);
  const status = m ? Number(m[1]) : undefined;
  const [reason, transient] = explain(body.toLowerCase(), status);
  return {
    status,
    reason,
    transient,
    requestId: REQUEST_RE.exec(body)?.[1],
    session: SESSION_RE.exec(body)?.[1],
    code: fixCode(body.toLowerCase()),
    raw: t,
  };
}

function explain(s: string, status?: number): [string, boolean | undefined] {
  if (fixCode(s) === "safeguards") return [t("lib-api-errors.safeguards"), false];
  if (s.trimStart().startsWith("not logged in")) return [t("lib-api-errors.not_logged_in"), false];
  if (s.includes("invalid api key")) return [t("lib-api-errors.bad_key"), false];
  if (s.includes("issue with the selected model")) return [t("lib-api-errors.bad_model"), false];
  if (s.includes("no available gpu")) return [t("lib-api-errors.no_gpu"), true];
  if (s.includes("overloaded") || status === 529) return [t("lib-api-errors.overloaded"), true];
  if (status === 401 || status === 403 || s.includes("authentication") || s.includes("/login") || s.includes("login expire")) {
    return [t("lib-api-errors.login_expired"), false];
  }
  if (status === 429 || s.includes("rate limit") || s.includes("rate_limit")) return [t("lib-api-errors.rate_limited"), true];
  if (s.includes("timed out") || s.includes("timeout")) return [t("lib-api-errors.timeout"), true];
  if (s.includes("connection")) return [t("lib-api-errors.no_connection"), true];
  if (status === 400) return [t("lib-api-errors.bad_request"), false];
  if (status !== undefined && status >= 500 && status < 600) return [t("lib-api-errors.unavailable"), true];
  const temporary = s.includes("temporary") || s.includes("try again");
  return [t("lib-api-errors.failed"), temporary ? true : undefined];
}

/** Whether the card's "Retry" can help: a login or a bad request is not fixed by trying again. */
export function retryable(e: APIErrorInfo): boolean {
  return e.transient !== false;
}

// i18n-ignore: what a person types to ask for a retry, in any language
const RETRIES = new Set(["继续", "重试", "再试一次", "continue", "retry", "go on"]);

/** A message that only asks to try again ("Continue"): between two of the same error it folds into the card. */
export function isRetryText(text: string): boolean {
  return RETRIES.has(text.trim().toLowerCase());
}
