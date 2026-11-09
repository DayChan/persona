// Where Settings is (#1996): which category the URL opens, and the list of them a phone shows first. Wide, the
// categories are in the left sidebar and /settings is "General"; phone width, /settings is the list (as on iOS), a category is
// /settings?tab=… pushed over it, and Back goes back to the list.
import { t } from "../../i18n";
import { settingsTab } from "../../lib/channels";
import type { IconName } from "../../ui/icons";

export type Tab = "general" | "persona" | "notifications" | "models" | "agents" | "channels" | "connectors" | "sent" | "extensions" | "shortcuts" | "devices" | "hub" | "managers";
export const TABS: Tab[] = ["general", "persona", "notifications", "models", "agents", "channels", "connectors", "sent", "extensions", "shortcuts", "devices", "hub", "managers"];
export const AGENT_KINDS = ["claude", "codex", "agy", "omp"] as const;
export type AgentTab = (typeof AGENT_KINDS)[number];
export type SettingsSection = "providers" | "model-config" | "translation" | AgentTab;
// The tab whose page opens one of its items, and the query parameter that names it.
const SUB: Partial<Record<Tab, string>> = { models: "purpose", channels: "channel", extensions: "extension", connectors: "connector" };

// Links from before the categories were combined keep their extra provider parameters. A fragment is the legacy
// category's destination within its new page, so a shared link still lands on the same section.
const LEGACY_TABS: Record<string, { tab: Tab; section: SettingsSection | null }> = {
  "model-config": { tab: "models", section: "model-config" },
  claude: { tab: "agents", section: "claude" },
  codex: { tab: "agents", section: "codex" },
  agy: { tab: "agents", section: "agy" },
  omp: { tab: "agents", section: "omp" },
  translate: { tab: "general", section: "translation" },
  assistant: { tab: "persona", section: null },
};

// `hint`: one short line under the category on the phone's list (#3636), so what it holds is clear without opening it.
export const TAB_INFO: Record<Tab, { label: string; icon: IconName; hint: string }> = {
  general: { label: t("settings-nav.tab_general"), icon: "gear", hint: t("settings-nav.hint_general") },
  persona: { label: "Persona", icon: "spark", hint: t("settings-nav.hint_persona") },
  notifications: { label: t("settings-nav.tab_notifications"), icon: "bell", hint: t("settings-nav.hint_notifications") },
  models: { label: t("settings-nav.tab_models"), icon: "layers", hint: t("settings-nav.hint_models") },
  agents: { label: t("settings-nav.tab_agents"), icon: "terminal", hint: t("settings-nav.hint_agents") },
  channels: { label: t("settings-nav.tab_channels"), icon: "chat", hint: t("settings-nav.hint_channels") },
  connectors: { label: t("settings-nav.tab_connectors"), icon: "link", hint: t("settings-nav.hint_connectors") },
  sent: { label: t("settings-nav.tab_sent"), icon: "send", hint: t("settings-nav.hint_sent") },
  extensions: { label: t("settings-nav.tab_extensions"), icon: "extension", hint: t("settings-nav.hint_extensions") },
  shortcuts: { label: t("settings-nav.tab_shortcuts"), icon: "keyboard", hint: t("settings-nav.hint_shortcuts") },
  devices: { label: t("settings-nav.tab_devices"), icon: "key", hint: t("settings-nav.hint_devices") },
  hub: { label: "hub", icon: "hub", hint: t("settings-nav.hint_hub") },
  managers: { label: t("settings-nav.tab_managers"), icon: "person", hint: t("settings-nav.hint_managers") },
};

/** The phone's list: groups of categories, retaining the desktop categories within each group. */
export const LIST_GROUPS: { title: string; tabs: Tab[] }[] = [
  { title: t("settings-nav.group_general"), tabs: ["general", "persona", "notifications", "shortcuts"] },
  { title: t("settings-nav.group_models"), tabs: ["models", "agents"] },
  { title: t("settings-nav.group_connections"), tabs: ["channels", "connectors", "sent", "extensions", "devices", "hub", "managers"] },
];

/** The category and item a URL's query names; `tab` is null when it names none (the phone's list). */
export function settingsAt(search: string, hash = ""): { tab: Tab | null; sub: string | null; section: SettingsSection | null } {
  const q = new URLSearchParams(search);
  const old = settingsTab(q.get("tab"));
  const legacy = old ? LEGACY_TABS[old] : undefined;
  const t = (legacy?.tab ?? old) as Tab;
  if (!TABS.includes(t)) return { tab: null, sub: null, section: null };
  const param = SUB[t];
  // ?tab=plugins&plugin=<id>: a link from before #1845
  const sub = param ? (q.get(param) ?? (t === "extensions" ? q.get("plugin") : null)) : null;
  if (t === "extensions" && sub === "-channels") return { tab: "channels", sub: null, section: null };
  if (t === "extensions" && sub?.startsWith("-channels/")) return { tab: "channels", sub: sub.slice("-channels/".length), section: null };
  const section = legacy?.section ?? sectionOfHash(hash, t);
  return { tab: t, sub, section };
}

/** The URL of a category (and its open item). "General" is plain /settings on a wide screen, where that opens it; on a
 *  phone /settings is the list, so it is named. */
export function settingsHref(tab: Tab, id: string | null, phone: boolean, manager?: string | null, section?: SettingsSection | null): string {
  const param = SUB[tab];
  const query = `/settings?tab=${tab}${id && param ? `&${param}=${encodeURIComponent(id)}` : ""}${manager ? `&manager=${encodeURIComponent(manager)}` : ""}`;
  return `${tab === "general" && !phone ? "/settings" : query}${section ? `#settings-${section}` : ""}`;
}

/** Replace a combined category's old URL in place, retaining provider and manager parameters. */
export function redirectLegacySettings(search: string, hash: string): string | null {
  const q = new URLSearchParams(search);
  const old = settingsTab(q.get("tab"));
  if (!old || !LEGACY_TABS[old]) return null;
  const at = settingsAt(search, hash);
  if (!at.tab) return null;
  q.set("tab", at.tab);
  if (at.tab !== "models") q.delete("purpose");
  return `/settings?${q.toString()}${at.section ? `#settings-${at.section}` : ""}`;
}

function sectionOfHash(hash: string, tab: Tab): SettingsSection | null {
  const section = hash.startsWith("#settings-") ? hash.slice("#settings-".length) : "";
  if (tab === "models" && (section === "providers" || section === "model-config")) return section;
  if (tab === "agents" && (AGENT_KINDS as readonly string[]).includes(section)) return section as AgentTab;
  if (tab === "general" && section === "translation") return "translation";
  return null;
}

/** The history state of a category opened from the phone's list: Back from it returns to the list. */
export const FROM_LIST = "personaSettingsList";

export function openedFromList(state: unknown): boolean {
  return Boolean(state && typeof state === "object" && (state as Record<string, unknown>)[FROM_LIST]);
}
