// Settings › Connectors › Lark bot (docs/extensions/bots.md, the app's FeishuBotModel): GET / PUT /api/v1/bots/feishu, pairing with a
// code sent to the bot from Feishu, and what the bot may do. Pure helpers here; the page is views/settings/FeishuBot.tsx.
import { t } from "../i18n";
import { api } from "./api";

export interface BotFeatures {
  main: boolean;
  cards: boolean;
  all_cards: boolean;
  pushes: boolean;
  sync: boolean;
  agents: boolean;
  /** The main conversation's tasks are told in the chat (#2768); a hub from before leaves it out. */
  reports?: boolean;
  /** /tasks keeps one standing "waiting for you" card, edited in place (#6052); a hub from before leaves it out. */
  board?: boolean;
  /** Only tasks' conclusions are pushed; a finished turn, waiting and interrupted stay in the to-do list and the briefing (#1226); a hub from before leaves it out. */
  only_conclusions?: boolean;
}

export interface FeishuBotStatus {
  enabled: boolean;
  app_id: string;
  /** The open platform's URL the hub resolved (feishu / lark / a custom one). */
  domain: string;
  has_secret: boolean;
  /** The credentials are hub.json's. */
  from_config: boolean;
  features: BotFeatures;
  connected: boolean;
  error?: string;
  last_event_at?: number;
  bot_name?: string;
  owner?: { name: string; paired_at: number } | null;
  pairing?: { code: string; expires_at: number } | null;
  /** Where the chat's messages go now. */
  target?: string;
  agents?: { title: string; machine_id: string; session_id: string }[] | null;
  /** Minutes an unanswered card waits before the one nudge under it, 0 off (#6084); a hub from before leaves it out (60). */
  nudge_after?: number;
}

/** The edit sent to PUT: only what is set changes. */
export interface FeishuBotUpdate {
  enabled?: boolean;
  app_id?: string;
  app_secret?: string;
  domain?: string;
  /** The switches to change: the hub merges them over its own (a hub before 0.1.12 replaces them all, see `featureEdit`). */
  features?: Partial<BotFeatures>;
  nudge_after?: number;
}

export const DEFAULT_FEATURES: BotFeatures = { main: true, cards: true, all_cards: false, pushes: false, sync: false, agents: false, reports: true, board: false, only_conclusions: false };

/** What a hub from before the setting does: nudge after an hour. */
export const DEFAULT_NUDGE_AFTER = 60;
const NUDGE_CHOICES = [0, 15, 30, 60, 120, 240, 480];

/** The nudge delays to pick from; the one in force is there even when it is not one of the usual (set by the API). */
export function nudgeChoices(current: number): number[] {
  return NUDGE_CHOICES.includes(current) ? NUDGE_CHOICES : [...NUDGE_CHOICES, current].sort((a, b) => a - b);
}

/** "Off", "30 min", "2 h". */
export function nudgeLabel(minutes: number): string {
  if (minutes <= 0) return t("lib-feishu-bot.nudge_off");
  return minutes % 60 === 0 ? t("lib-feishu-bot.nudge_hours", { hours: minutes / 60 }) : t("lib-feishu-bot.nudge_minutes", { minutes });
}

export const botStatus = () => api<FeishuBotStatus>("/bots/feishu");
export const updateBot = (u: FeishuBotUpdate) => api<FeishuBotStatus>("/bots/feishu", { method: "PUT", json: u });
export const pairBot = () => api<FeishuBotStatus>("/bots/feishu/pair", { method: "POST" });
export const unpairBot = () => api<FeishuBotStatus>("/bots/feishu/owner", { method: "DELETE" });
export const testBot = () => api<{ ok: boolean }>("/bots/feishu/test", { method: "POST" });

/** The platform picker's choice for what the hub resolved: feishu, lark, or the custom URL itself. */
export function domainChoice(resolved: string): string {
  if (resolved.includes("larksuite")) return "lark";
  if (!resolved || resolved.includes("open.feishu.cn")) return "feishu";
  return resolved;
}

export interface BotCredentials {
  appID: string;
  appSecret: string;
  domain: string;
}

/** The form filled from what the hub has (the secret is never read back). */
export function credentialsOf(st: FeishuBotStatus): BotCredentials {
  return { appID: st.app_id, appSecret: "", domain: domainChoice(st.domain) };
}

export function credentialsChanged(st: FeishuBotStatus | null, c: BotCredentials): boolean {
  if (!st) return c.appID.trim() !== "";
  return c.appID.trim() !== st.app_id || c.appSecret.trim() !== "" || c.domain !== domainChoice(st.domain);
}

/** The PUT for "Verify and save": an empty secret keeps the saved one. */
export function credentialsUpdate(c: BotCredentials): FeishuBotUpdate {
  const secret = c.appSecret.trim();
  return { app_id: c.appID.trim(), domain: c.domain, ...(secret ? { app_secret: secret } : {}) };
}

/** The switches after turning one: "Questions from all agents" needs "Questions and approvals". */
export function withFeature(f: BotFeatures, key: keyof BotFeatures, on: boolean): BotFeatures {
  const next = { ...f, [key]: on };
  if (key === "cards" && !on) next.all_cards = false;
  if (key === "all_cards" && on) next.cards = true;
  return next;
}

/**
 * What turning one switch sends: that switch alone, so one changed elsewhere since this page loaded keeps its value (the
 * hub merges the switches it is sent over its own). The pair that goes together names both ("all cards" brings "cards"
 * along, "cards" off takes "all cards" with it): the hub does not tie them. A hub from before 0.1.12 replaces the whole
 * object it is sent, and says so by leaving `reports` out of its status: it gets every switch, as it always did.
 */
export function featureEdit(f: BotFeatures, key: keyof BotFeatures, on: boolean): Partial<BotFeatures> {
  if (f.reports === undefined) return withFeature(f, key, on);
  const edit: Partial<BotFeatures> = { [key]: on };
  if (key === "cards" && !on) edit.all_cards = false;
  if (key === "all_cards" && on) edit.cards = true;
  return edit;
}

/** What the status line says. */
export function botStateText(st: FeishuBotStatus | null, loading = false): string {
  if (!st) return loading ? t("lib-feishu-bot.state_checking") : t("lib-feishu-bot.state_unreadable");
  if (!st.enabled) return t("lib-feishu-bot.state_off");
  if (!st.has_secret) return t("lib-feishu-bot.state_no_credentials");
  if (!st.connected) return st.error ? t("lib-feishu-bot.state_error", { error: st.error }) : t("lib-feishu-bot.state_connecting");
  return st.owner ? t("lib-feishu-bot.state_connected") : t("lib-feishu-bot.state_connected_unpaired");
}

/** The status dot's tone (none: off or unknown). */
export function botTone(st: FeishuBotStatus | null): "green" | "red" | "orange" | undefined {
  if (!st?.enabled) return undefined;
  if (!st.connected) return "red";
  return st.owner ? "green" : "orange";
}

/** The connectors list's one-word summary. */
export function botSummary(st: FeishuBotStatus | null): string {
  if (!st) return "";
  if (!st.enabled) return t("lib-feishu-bot.summary_off");
  if (!st.connected) return t("lib-feishu-bot.summary_disconnected");
  return st.owner ? t("lib-feishu-bot.summary_connected") : t("lib-feishu-bot.summary_unpaired");
}

/** A pairing code still valid at `now` (ms). */
export function livePairing(st: FeishuBotStatus | null, now = Date.now()) {
  const p = st?.pairing;
  return p && p.expires_at * 1000 > now ? p : null;
}

/** "Valid for 9 min 3 s". */
export function pairingLeft(expiresAt: number, now = Date.now()): string {
  const left = Math.max(0, Math.floor(expiresAt - now / 1000));
  return t("lib-feishu-bot.pairing_left", { minutes: Math.floor(left / 60), seconds: left % 60 });
}
