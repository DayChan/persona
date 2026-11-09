// An extension's sign-in and its command-line tools (#4507, docs/protocol/protocol.md "Extension sign-in"): the hub asks the extension's
// program (auth.status / auth.step / auth.logout) and keeps its state; the page draws a "Sign in" group on the extension's
// detail, a sign-in dialog of steps (fields, a device code, a QR code), and lists the tools in "About". Only with a hub
// that has `extension_auth`; the hub pushes `extension_auth` whenever a sign-in state changes.
import { api, enc } from "./api";
import type { PromptQR } from "./connectors";
import { extIdOf, extRoot } from "./extension-api";
import type { AuthPrompt as WireAuthPrompt, AuthState } from "./extension-wire";
import { messageManager } from "./manager-choice";
import { onHubEvent } from "./store";
import { t } from "../i18n";

/** The hub's sign-in state of an extension (ExtensionAuth); `unknown` while its program does not run. */
export interface ExtensionAuth {
  title: string;
  description?: string;
  /** A page of the extension for older apps' sign-in: this page does not list it. */
  page?: string;
  state: AuthState | "unknown";
  account?: string;
  message?: string;
  expires_at?: string;
  checked_at?: string;
}

/** A command-line program the extension's version uses; reused is the machine's own copy. */
export interface ExtensionTool {
  name: string;
  title?: string;
  version: string;
  reused?: boolean;
}

/** A sign-in step as the hub answers it: the extension's, with url's QR code drawn by the hub. */
export type AuthPrompt = WireAuthPrompt & { qr?: PromptQR };

// ── the hub ─────────────────────────────────────────────

const base = (id: string) => `${extRoot()}/${enc(id)}/-/auth`;

export const getAuth = (id: string) => api<ExtensionAuth>(base(id));
/** One step: "" starts over; a device code / QR step is asked again with no values. */
export const authStep = (id: string, step: string, values: Record<string, string> = {}) =>
  api<AuthPrompt>(`${base(id)}/step`, { method: "POST", json: { step, values } });
export const authLogout = (id: string) => api<ExtensionAuth>(`${base(id)}/logout`, { method: "POST", json: {} });

/** Calls l with each extension_auth the hub pushes (extension id, its new state, and the manager whose extension it is:
 *  the same id may be installed under several managers, docs/assistant/managers.md "Clients"). */
export function onAuthChange(l: (id: string, auth: ExtensionAuth, manager: string) => void): () => void {
  return onHubEvent("extension_auth", (msg) => {
    const id = extIdOf(msg as { extension?: unknown; plugin?: unknown });
    const auth = (msg as { auth?: unknown }).auth;
    if (id && auth && typeof auth === "object") l(id, auth as ExtensionAuth, messageManager(msg));
  });
}

/** Puts a pushed state into whatever holds the extension (a list's item, a detail). */
export function withAuth<T extends { id: string; auth?: ExtensionAuth }>(x: T, id: string, auth: ExtensionAuth): T {
  return x.id === id ? { ...x, auth } : x;
}

// An install that ends with `next: "auth"` opens the extension's detail; the detail then starts signing in.
const asked = new Set<string>();
/** The detail of id should start signing in when it opens. */
export const askSignIn = (id: string) => void asked.add(id);
/** Whether it should (still asked: read it while rendering, then takeSignIn). */
export const signInAsked = (id: string) => asked.has(id);
/** Whether it should, once: the ask is used up. */
export const takeSignIn = (id: string) => asked.delete(id);

// ── words and rules ─────────────────────────────────────

/** The state in words: "Not signed in", "Waiting for confirmation", "Signed in: <account>", "Expired", "Error: <message>"; unknown is its program not running. */
export function authStateText(a: ExtensionAuth): string {
  switch (a.state) {
    case "signed_out":
      return t("lib-extension-auth.signed_out");
    case "pending":
      return t("lib-extension-auth.pending");
    case "signed_in":
      return a.account ? t("lib-extension-auth.signed_in_as", { account: a.account }) : t("lib-extension-auth.signed_in");
    case "expired":
      return t("lib-extension-auth.expired");
    case "error":
      return a.message ? t("lib-extension-auth.error_because", { message: a.message }) : t("lib-extension-auth.error");
    case "unknown":
      return t("lib-extension-auth.unknown");
    default:
      return a.state;
  }
}

/** The tone of its dot: green signed in, orange to sign in (or waiting), red an error, gray unknown. */
export function authTone(a: ExtensionAuth): "green" | "orange" | "red" | "idle" {
  switch (a.state) {
    case "signed_in":
      return "green";
    case "signed_out":
    case "expired":
    case "pending":
      return "orange";
    case "error":
      return "red";
    default:
      return "idle";
  }
}

/** Not signed in (or no longer): the list says so, and an install's next step is to. */
export const needsSignIn = (a?: ExtensionAuth) => a?.state === "signed_out" || a?.state === "expired";

/** The sign-in button's word: "Sign in again" once it has been signed in to. */
export const signInLabel = (a: ExtensionAuth) =>
  a.state === "signed_in" || a.state === "expired" ? t("lib-extension-auth.sign_in_again") : t("lib-extension-auth.sign_in");

/** Whether there is a sign-in to sign out of. */
export const canSignOut = (a: ExtensionAuth) => a.state === "signed_in";

/** What the list adds after an extension's state: " · Not signed in" when it has to be signed in to, else "". */
export const listAuthNote = (a?: ExtensionAuth) => (needsSignIn(a) ? t("lib-extension-auth.list_signed_out") : "");

/** The pages to list: an older app's sign-in page (auth.page) is left out, its sign-in is the group above. */
export function shownPages<T extends { id: string }>(pages: T[] | undefined, auth?: ExtensionAuth): T[] {
  return (pages ?? []).filter((u) => !auth?.page || u.id !== auth.page);
}

/** A tool's row in "About": its version, and "already on the machine" when it is the machine's own copy. */
export function toolText(x: ExtensionTool): string {
  return x.reused ? t("lib-extension-auth.tool_reused", { version: x.version }) : x.version;
}

/** A link the page may open: https only. */
export function safeLink(url?: string): string {
  if (!url) return "";
  try {
    return new URL(url).protocol === "https:" ? url : "";
  } catch {
    return "";
  }
}

/**
 * How the dialog draws a step: fields to fill in, a device code (its link, its code when there is one, its QR code), a
 * QR code (or, without its modules, the link it encodes), or one it cannot draw.
 */
export type AuthStepView = "fields" | "device_code" | "qr" | "qr_link" | "unsupported";

export function authStepView(p: AuthPrompt): AuthStepView {
  switch (p.kind ?? "") {
    case "":
      return p.fields?.length ? "fields" : "unsupported";
    case "device_code":
      return p.user_code || safeLink(p.url) || p.qr?.rows?.length ? "device_code" : "unsupported";
    case "qr":
      if (p.qr?.rows?.length) return "qr";
      return safeLink(p.url) ? "qr_link" : "unsupported";
    default:
      return "unsupported";
  }
}

/** Whether the dialog asks this step again by itself (a device code, a QR code), until it is done or the step changes. */
export function isPolled(p?: AuthPrompt | null): boolean {
  if (!p || p.done) return false;
  const v = authStepView(p);
  return v === "device_code" || v === "qr" || v === "qr_link";
}

/** How long to wait before asking a polled step again: its interval, else 3 s for a QR code and 5 s for a code. */
export function pollDelay(p: AuthPrompt): number {
  const fallback = p.kind === "qr" ? 3 : 5;
  return Math.max(p.interval && p.interval > 0 ? p.interval : fallback, 1) * 1000;
}

/**
 * Asks a polled step again (the same step, no values) every pollDelay until the answer is done or another step;
 * each answer goes to onPrompt (the same step with a new code too). A failed ask goes to onError and it keeps
 * asking (the hub may be restarting). Returns the stop.
 */
export function pollAuth(
  first: AuthPrompt,
  ask: (step: string) => Promise<AuthPrompt>,
  onPrompt: (p: AuthPrompt) => void,
  onError: (e: unknown) => void = () => {},
): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const step = first.step ?? "";
  const next = (p: AuthPrompt) => {
    if (stopped || !isPolled(p)) return;
    timer = setTimeout(async () => {
      if (stopped) return;
      let got: AuthPrompt;
      try {
        got = await ask(step);
      } catch (e) {
        if (stopped) return;
        onError(e);
        next(p);
        return;
      }
      if (stopped) return;
      onPrompt(got);
      if (!got.done && (got.step ?? "") === step) next(got);
    }, pollDelay(p));
  };
  next(first);
  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
  };
}
