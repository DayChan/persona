// Where Settings is (#1996): which category the URL opens, and the list of them a phone shows first. Wide, the
// categories are in the left sidebar and /settings is "General"; phone width, /settings is the list (as on iOS), a category is
// /settings?tab=… pushed over it, and Back goes back to the list.
import { t } from "../../i18n";
import { settingsTab } from "../../lib/channels";
import type { IconName } from "../../ui/icons";

export type Tab = "general" | "notifications" | "assistant" | "models" | "model-config" | "claude" | "codex" | "agy" | "omp" | "channels" | "connectors" | "sent" | "extensions" | "translate" | "shortcuts" | "devices" | "hub" | "managers";
export const TABS: Tab[] = ["general", "notifications", "assistant", "models", "model-config", "claude", "codex", "agy", "omp", "channels", "connectors", "sent", "extensions", "translate", "shortcuts", "devices", "hub", "managers"];
// The tab whose page opens one of its items, and the query parameter that names it.
const SUB: Partial<Record<Tab, string>> = { "model-config": "purpose", channels: "channel", extensions: "extension", connectors: "connector" };

// `hint`: one short line under the category on the phone's list (#3636), so what it holds is clear without opening it.
export const TAB_INFO: Record<Tab, { label: string; icon: IconName; hint: string }> = {
  general: { label: t("settings-nav.tab_general"), icon: "gear", hint: t("settings-nav.hint_general") },
  notifications: { label: t("settings-nav.tab_notifications"), icon: "bell", hint: t("settings-nav.hint_notifications") },
  assistant: { label: t("settings-nav.tab_assistant"), icon: "spark", hint: t("settings-nav.hint_assistant") },
  models: { label: t("settings-nav.tab_models"), icon: "layers", hint: t("settings-nav.hint_models") },
  "model-config": { label: t("settings-nav.tab_model_config"), icon: "gear", hint: t("settings-nav.hint_model_config") },
  claude: { label: "Claude Code", icon: "terminal", hint: t("settings-nav.hint_claude") },
  codex: { label: "Codex", icon: "terminal", hint: t("settings-nav.hint_codex") },
  agy: { label: "Antigravity", icon: "terminal", hint: t("settings-nav.hint_agy") },
  omp: { label: "omp", icon: "terminal", hint: t("settings-nav.hint_omp") },
  channels: { label: t("settings-nav.tab_channels"), icon: "chat", hint: t("settings-nav.hint_channels") },
  connectors: { label: t("settings-nav.tab_connectors"), icon: "link", hint: t("settings-nav.hint_connectors") },
  sent: { label: t("settings-nav.tab_sent"), icon: "send", hint: t("settings-nav.hint_sent") },
  extensions: { label: t("settings-nav.tab_extensions"), icon: "extension", hint: t("settings-nav.hint_extensions") },
  translate: { label: t("settings-nav.tab_translate"), icon: "translate", hint: t("settings-nav.hint_translate") },
  shortcuts: { label: t("settings-nav.tab_shortcuts"), icon: "keyboard", hint: t("settings-nav.hint_shortcuts") },
  devices: { label: t("settings-nav.tab_devices"), icon: "key", hint: t("settings-nav.hint_devices") },
  hub: { label: "hub", icon: "hub", hint: t("settings-nav.hint_hub") },
  managers: { label: t("settings-nav.tab_managers"), icon: "person", hint: t("settings-nav.hint_managers") },
};

/** The phone's list: groups of categories, retaining the desktop categories within each group. */
export const LIST_GROUPS: { title: string; tabs: Tab[] }[] = [
  { title: t("settings-nav.group_general"), tabs: ["general", "notifications", "assistant", "translate", "shortcuts"] },
  { title: t("settings-nav.group_models"), tabs: ["models", "model-config", "claude", "codex", "agy", "omp"] },
  { title: t("settings-nav.group_connections"), tabs: ["channels", "connectors", "sent", "extensions", "devices", "hub", "managers"] },
];

/** The category and item a URL's query names; `tab` is null when it names none (the phone's list). */
export function settingsAt(search: string): { tab: Tab | null; sub: string | null } {
  const q = new URLSearchParams(search);
  const t = settingsTab(q.get("tab")) as Tab;
  if (!TABS.includes(t)) return { tab: null, sub: null };
  const param = SUB[t];
  // ?tab=plugins&plugin=<id>: a link from before #1845
  const sub = param ? (q.get(param) ?? (t === "extensions" ? q.get("plugin") : null)) : null;
  if (t === "extensions" && sub === "-channels") return { tab: "channels", sub: null };
  if (t === "extensions" && sub?.startsWith("-channels/")) return { tab: "channels", sub: sub.slice("-channels/".length) };
  return { tab: t, sub };
}

/** The URL of a category (and its open item). "General" is plain /settings on a wide screen, where that opens it; on a
 *  phone /settings is the list, so it is named. */
export function settingsHref(tab: Tab, id: string | null, phone: boolean, manager?: string | null): string {
  if (tab === "general" && !phone) return "/settings";
  const param = SUB[tab];
  return `/settings?tab=${tab}${id && param ? `&${param}=${encodeURIComponent(id)}` : ""}${manager ? `&manager=${encodeURIComponent(manager)}` : ""}`;
}

/** The history state of a category opened from the phone's list: Back from it returns to the list. */
export const FROM_LIST = "personaSettingsList";

export function openedFromList(state: unknown): boolean {
  return Boolean(state && typeof state === "object" && (state as Record<string, unknown>)[FROM_LIST]);
}
