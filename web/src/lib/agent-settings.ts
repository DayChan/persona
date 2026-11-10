// Settings › Claude Code / Codex settings (the node's whitelist of ~/.claude/settings.json and ~/.codex/config.toml keys,
// iOS ClaudeSettingsView) and Settings › Connectors › Mail's add form (iOS MailAccountForm, docs/extensions/mail.md): the pure part.
import { hasKey, t } from "../i18n";
import type { Machine } from "./types";

// ── agent settings ──

export type AgentKind = "claude" | "codex" | "agy" | "omp";

export type SettingValue = boolean | string | null;

/** One editable option, as GET /machines/{mid}/claude-settings (or codex-settings) lists it. value null = unset. */
export interface AgentSetting {
  key: string;
  title: string;
  detail: string;
  type: "bool" | "enum" | "string" | string;
  options?: string[];
  default?: SettingValue;
  value?: SettingValue;
  /** A stored value the agent ignores; value is null so the control shows the default. */
  ignored_value?: string;
}

export const AGENT_TEXT: Record<AgentKind, { name: string; path: string; file: string; footer: string }> = {
  claude: {
    name: "Claude Code",
    path: "claude-settings",
    file: "~/.claude/settings.json",
    footer: t("lib-agent-settings.claude_footer"),
  },
  codex: {
    name: "Codex",
    path: "codex-settings",
    file: "~/.codex/config.toml",
    footer: t("lib-agent-settings.codex_footer"),
  },
  agy: {
    name: "Antigravity",
    path: "agy-settings",
    file: "~/.gemini/antigravity-cli/settings.json",
    footer: t("lib-agent-settings.agy_footer"),
  },
  omp: {
    name: "omp",
    path: "omp-settings",
    file: "~/.omp/agent/config.yml",
    footer: t("lib-agent-settings.omp_footer"),
  },
};

// Claude Code's options in plain words (#3599). The node sends a title and detail too (claudeconfig.go); the page's
// language wins when the catalog has the key, so a key the web does not know yet still reads as the node wrote it.
function catalogText(key: string, fallback: string): string {
  return hasKey(key) ? t(key) : fallback;
}

/** What a row is called. */
export function settingTitle(s: Pick<AgentSetting, "key" | "title">): string {
  return catalogText(`lib-agent-settings.setting_${s.key}`, s.title);
}

/** What a row does, in a sentence. */
export function settingDetail(s: Pick<AgentSetting, "key" | "detail" | "ignored_value">): string {
  if (s.key === "effortLevel" && s.ignored_value === "max") return t("lib-agent-settings.setting_effortLevel_ignored_max");
  return catalogText(`lib-agent-settings.setting_${s.key}_detail`, s.detail);
}

// Codex's option values under one prefix: { "on-request": "lib-agent-settings.approval_on_request" }.
function codexOptions(prefix: string, options: string[]): Record<string, string> {
  return Object.fromEntries(options.map((o) => [o, `lib-agent-settings.${prefix}_${o.replace(/-/g, "_")}`]));
}

// An option value's name: effort levels as the session menu calls them, the update channels and Codex's
// config.toml values (#3855) by name.
const OPTION_KEYS: Record<string, Record<string, string>> = {
  effortLevel: { low: "lib-session-actions.effort_low", medium: "lib-session-actions.effort_medium", high: "lib-session-actions.effort_high", xhigh: "lib-session-actions.effort_xhigh", max: "lib-session-actions.effort_max" },
  autoUpdatesChannel: { latest: "lib-agent-settings.channel_latest", stable: "lib-agent-settings.channel_stable" },
  // Claude Code's default mode (#3026), named as the session menu names the modes.
  "permissions.defaultMode": {
    default: "lib-session-actions.mode_default", acceptEdits: "lib-session-actions.mode_accept_edits", plan: "lib-session-actions.mode_plan",
    auto: "lib-session-actions.mode_auto", bypassPermissions: "lib-session-actions.mode_bypass_permissions",
  },
  model_reasoning_effort: Object.fromEntries(
    ["minimal", "low", "medium", "high", "xhigh", "max", "ultra"].map((o) => [o, `lib-session-actions.codex_effort_${o}`]),
  ),
  model_reasoning_summary: codexOptions("summary", ["auto", "concise", "detailed", "none"]),
  model_verbosity: codexOptions("verbosity", ["low", "medium", "high"]),
  approval_policy: codexOptions("approval", ["untrusted", "on-failure", "on-request", "never"]),
  sandbox_mode: codexOptions("sandbox", ["read-only", "workspace-write", "danger-full-access"]),
};

/** How an enum option reads; an option with no name (a new one, or another agent's) stays as written. */
export function optionText(key: string, option: string): string {
  const k = OPTION_KEYS[key]?.[option];
  return k ? catalogText(k, option) : option;
}

/** The value in effect: what is set, else the default. */
export function effective(s: AgentSetting): SettingValue {
  return s.value ?? s.default ?? null;
}

export function isUnset(s: AgentSetting): boolean {
  return s.value === null || s.value === undefined;
}

/** An optimistic change clears the old stored-value warning until the node answers. */
export function withValue(list: AgentSetting[], key: string, value: SettingValue): AgentSetting[] {
  return list.map((s) => (s.key === key ? { ...s, value, ignored_value: undefined } : s));
}

/** What a text field sends: an empty field is "back to the default" (null), not an empty string written to the file. */
export function textValue(input: string): string | null {
  const v = input.trim();
  return v ? v : null;
}

/** The machine the page edits: the remembered one while it is online, else the first online one, else "". */
export function pickMachine(machines: Pick<Machine, "id" | "online">[], remembered: string): string {
  if (machines.some((m) => m.id === remembered && m.online)) return remembered;
  return machines.find((m) => m.online)?.id ?? "";
}

export interface ApplyResult {
  id: string;
  name: string;
  error?: string;
}

/** One line for a change sent to several machines: "applied to 3 machines", or which failed and why. */
export function applySummary(results: ApplyResult[]): { ok: boolean; text: string } {
  const failed = results.filter((r) => r.error);
  if (!failed.length) return { ok: true, text: results.length > 1 ? t("lib-agent-settings.applied_to", { count: results.length }) : t("lib-agent-settings.saved") };
  const done = results.length - failed.length;
  const why = failed.map((r) => t("lib-agent-settings.failure_item", { name: r.name, error: r.error ?? "" })).join(t("lib-agent-settings.failure_separator"));
  return { ok: false, text: done ? t("lib-agent-settings.partly_applied", { done, failed: failed.length, why }) : t("lib-agent-settings.not_applied", { why }) };
}

/** How a value reads in a message: on / off, the option's name (given the setting's key), or "default". */
export function valueText(v: SettingValue, key?: string): string {
  if (v === null || v === undefined || v === "") return t("lib-agent-settings.value_default");
  if (typeof v === "boolean") return v ? t("lib-agent-settings.value_on") : t("lib-agent-settings.value_off");
  return key ? optionText(key, v) : v;
}

// ── Claude Code › blocked config directories (GET /machines/{mid}/claude-dirs, docs/integrations/claude-integration.md "Blocking configuration directories") ──

/** One config directory of a computer (CLAUDE_CONFIG_DIR), as the hub lists it. */
export interface ClaudeDir {
  path: string;
  default?: boolean;
  blocked?: boolean;
  blocked_by?: string;
  source: "default" | "config" | "found" | string;
  running: number;
  sessions?: number;
}

export interface ClaudeDirsState {
  dirs: ClaudeDir[];
  blocked: string[];
  home?: string;
  live: boolean;
}

/** The path as the user reads it: the computer's home as ~. */
export function shortPath(path: string, home?: string): string {
  if (home && (path === home || path.startsWith(home + "/"))) return "~" + path.slice(home.length);
  return path;
}

/** What a row says under the path: where it came from, how many sessions, how many claudes run from it. */
export function dirMeta(d: ClaudeDir, home?: string): string {
  const parts: string[] = [];
  if (d.default) parts.push(t("lib-agent-settings.dir_default"));
  else if (d.source === "config") parts.push(t("lib-agent-settings.dir_config"));
  else parts.push(t("lib-agent-settings.dir_found"));
  if (d.sessions) parts.push(t("lib-agent-settings.dir_sessions", { count: d.sessions }));
  if (d.running) parts.push(t("lib-agent-settings.dir_running", { count: d.running }));
  if (d.blocked && d.blocked_by && !samePath(d.blocked_by, d.path, home)) parts.push(t("lib-agent-settings.dir_blocked_by", { rule: d.blocked_by }));
  return parts.join(" · ");
}

/** The patterns after a row's "Block" / "Unblock": blocking adds the directory's own path; unblocking lifts the rule that
 *  blocks it (a glob lifts every directory it covers — the row says which rule). */
export function toggledPatterns(state: ClaudeDirsState, d: ClaudeDir): string[] {
  if (!d.blocked) return state.blocked.includes(d.path) ? state.blocked : [...state.blocked, shortPath(d.path, state.home)];
  const rule = d.blocked_by ?? d.path;
  return state.blocked.filter((p) => p !== rule);
}

/** Why a typed pattern cannot be added ("" = it can). */
export function patternError(input: string, existing: string[]): string {
  const p = input.trim().replace(/\/+$/, "");
  if (!p) return t("lib-agent-settings.pattern_empty");
  if (!p.startsWith("/") && !p.startsWith("~/")) return t("lib-agent-settings.pattern_prefix");
  if (p === "~" || p === "/") return t("lib-agent-settings.pattern_root");
  if (existing.includes(p)) return t("lib-agent-settings.pattern_exists");
  return "";
}

/** The patterns with a typed one added (trimmed, no trailing /). */
export function withPattern(existing: string[], input: string): string[] {
  return [...existing, input.trim().replace(/\/+$/, "")];
}

/** Rules that block none of the listed directories (a glob nothing matches yet, a directory not seen): listed on
 *  their own so they can be deleted; the others are lifted from the rows they block. */
export function looseRules(state: ClaudeDirsState): string[] {
  return state.blocked.filter((p) => !state.dirs.some((d) => d.blocked && (d.blocked_by === p || samePath(p, d.path, state.home))));
}

/** A pattern written as this directory's path, with ~ or without. */
function samePath(pattern: string, path: string, home?: string): boolean {
  return pattern === path || pattern === shortPath(path, home);
}

// ── Connectors › Mail › Add ──

export type MailKind = "gmail" | "outlook" | "feishu" | "imap";

/** The kinds, in the phone's order (iOS MailAccountForm.Kind). Gmail and Outlook sign in with Google / Microsoft. */
export const MAIL_KINDS: { k: MailKind; label: string }[] = [
  { k: "gmail", label: "Gmail" },
  { k: "outlook", label: "Outlook" },
  { k: "feishu", label: t("lib-agent-settings.mail_kind_feishu") },
  { k: "imap", label: t("lib-agent-settings.mail_kind_imap") },
];

export interface MailForm {
  kind: MailKind;
  email: string;
  password: string;
  imapHost: string;
  imapPort: string;
  smtpHost: string;
  smtpPort: string;
}

export const EMPTY_MAIL_FORM: MailForm = { kind: "imap", email: "", password: "", imapHost: "", imapPort: "993", smtpHost: "", smtpPort: "465" };

/** POST /api/v1/mail/accounts's body (docs/protocol/protocol.md "Mail API"). */
export interface AddMailAccount {
  name: string;
  email: string;
  provider: "gmail" | "feishu" | "imap";
  auth: "password";
  password: string;
  imap_host?: string;
  imap_port?: number;
  smtp_host?: string;
  smtp_port?: number;
  smtp_security?: "tls" | "starttls";
}

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

const port = (p: string) => {
  const n = Number(p.trim());
  return Number.isInteger(n) && n > 0 && n < 65536 ? n : undefined;
};

/** Gmail and Outlook sign in on a provider's page that calls back into the phone app (a custom scheme the browser can't
 *  catch), so they are added on the phone; the web only sets Outlook's client ID up. */
export function signsInOnPhone(k: MailKind): k is "gmail" | "outlook" {
  return k === "gmail" || k === "outlook";
}

/** What is wrong with the form, field by field (empty = can be sent). Outlook is never sent from here, nor is Gmail
 *  unless the hub has no Google sign-in (`gmailAppPassword`: then it goes in with an app password). */
export function mailErrors(f: MailForm, gmailAppPassword = false): Partial<Record<keyof MailForm, string>> {
  const e: Partial<Record<keyof MailForm, string>> = {};
  if (f.kind === "gmail" && !gmailAppPassword) return { kind: t("lib-agent-settings.mail_gmail_phone_only") };
  if (f.kind === "outlook") return { kind: t("lib-agent-settings.mail_outlook_phone_only") };
  if (!f.email.trim()) e.email = t("lib-agent-settings.mail_email_empty");
  else if (!EMAIL.test(f.email.trim())) e.email = t("lib-agent-settings.mail_email_invalid");
  if (!f.password)
    e.password = t(
      f.kind === "feishu" ? "lib-agent-settings.mail_password_feishu" : f.kind === "gmail" ? "lib-agent-settings.mail_password_gmail" : "lib-agent-settings.mail_password_empty",
    );
  if (f.kind === "imap") {
    if (f.imapHost.trim() && !port(f.imapPort)) e.imapPort = t("lib-agent-settings.mail_port_invalid");
    if (f.smtpHost.trim() && !port(f.smtpPort)) e.smtpPort = t("lib-agent-settings.mail_port_invalid");
  }
  return e;
}

/** The request for a password account; null for Outlook, and for Gmail on a hub that signs in with Google (both on the
 * phone). Servers left empty are guessed by the hub from the domain; Lark's are fixed by the hub; Gmail's are spelled
 * out, since a Google Workspace address has a domain no preset knows. SMTP on 587 is STARTTLS, anything else TLS. */
export function mailRequest(f: MailForm, gmailAppPassword = false): AddMailAccount | null {
  if (f.kind === "outlook" || (f.kind === "gmail" && !gmailAppPassword)) return null;
  const email = f.email.trim();
  const req: AddMailAccount = { name: email, email, provider: f.kind, auth: "password", password: f.password };
  if (f.kind === "gmail") return { ...req, imap_host: "imap.gmail.com", imap_port: 993, smtp_host: "smtp.gmail.com", smtp_port: 465, smtp_security: "tls" };
  if (f.kind === "imap") {
    if (f.imapHost.trim()) {
      req.imap_host = f.imapHost.trim();
      req.imap_port = port(f.imapPort);
    }
    if (f.smtpHost.trim()) {
      req.smtp_host = f.smtpHost.trim();
      req.smtp_port = port(f.smtpPort);
      req.smtp_security = f.smtpPort.trim() === "587" ? "starttls" : "tls";
    }
  }
  return req;
}

/**
 * Where a QQ / NetEase address typed under Other IMAP makes its authorization code (the provider and its web mail), null for any
 * other. The hub says the same steps for the same domains (server/internal/mail/preset.go, Guide; iOS CodeGuide):
 * change them together.
 */
export function mailCodeGuide(f: Pick<MailForm, "kind" | "email" | "imapHost" | "smtpHost">): { provider: "qq" | "netease"; webMail: string } | null {
  // Servers typed: they may not be the provider's.
  if (f.kind !== "imap" || f.imapHost.trim() || f.smtpHost.trim()) return null;
  const at = f.email.lastIndexOf("@");
  if (at < 0) return null;
  const domain = f.email.slice(at + 1).trim().toLowerCase();
  switch (domain) {
    case "qq.com":
    case "foxmail.com":
    case "vip.qq.com":
      return { provider: "qq", webMail: "https://mail.qq.com" };
    case "163.com":
    case "126.com":
    case "yeah.net":
      return { provider: "netease", webMail: `https://mail.${domain}` };
    case "vip.163.com":
    case "vip.126.com":
      return { provider: "netease", webMail: `https://${domain}` };
    case "188.com":
      return { provider: "netease", webMail: "https://www.188.com" };
    default:
      return null;
  }
}

export interface MailAccount {
  id: string;
  name?: string;
  email: string;
  provider?: string;
  auth?: string;
  imap_host?: string;
  status?: string;
  error?: string;
}

/** The line under an account's address: its error, how it signs in, or its server. */
export function mailAccountLine(a: MailAccount): string {
  if (a.status === "error") return a.error || t("lib-agent-settings.mail_unreachable");
  if (a.auth === "oauth2") return a.provider === "outlook" ? t("lib-agent-settings.mail_line_outlook") : t("lib-agent-settings.mail_line_gmail");
  if (a.provider === "feishu") return t("lib-agent-settings.mail_line_feishu");
  return a.imap_host ? `IMAP · ${a.imap_host}` : "IMAP";
}

/** The badge after an account's address: its provider's name for the ones signed in with a provider (iOS MailProviderMark). */
export function mailProviderBadge(a: MailAccount): string | null {
  if (a.provider === "gmail") return "Gmail";
  if (a.provider === "outlook") return "Outlook";
  if (a.provider === "feishu") return t("lib-agent-settings.mail_kind_feishu");
  return null;
}

/** GET / PUT /api/v1/mail/oauth/microsoft/config (docs/extensions/mail.md "Outlook: Sign In with Microsoft"): whether Outlook sign-in can start,
 *  with which client ID (not a secret) from where (app | config), and the redirect URI to register in Microsoft Entra. */
export interface MicrosoftConfig {
  configured: boolean;
  client_id?: string;
  source?: string;
  redirect_uri: string;
}

const CLIENT_ID = /^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$/;

/** What is wrong with a typed client ID ("" = fine), the same rule as the hub's. */
export function clientIDError(id: string): string {
  const v = id.trim();
  if (!v) return t("lib-agent-settings.mail_client_id_empty");
  return CLIENT_ID.test(v) ? "" : t("lib-agent-settings.mail_client_id_invalid");
}

/** "Connectors"' summary of the accounts: "2 accounts", "2 · with errors", "not added". */
export function mailSummary(accounts: MailAccount[] | null): string {
  if (!accounts) return "";
  if (!accounts.length) return t("lib-agent-settings.mail_none");
  if (accounts.some((a) => a.status === "error")) return t("lib-agent-settings.mail_with_error", { count: accounts.length });
  return t("lib-agent-settings.mail_accounts", { count: accounts.length });
}
