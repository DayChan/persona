// What the chat's ⋯ menu changes on a session: notifications (mutes), the run modes, the name
// (docs/protocol/protocol.md `/mutes`, `/modes`, `/rename`; iOS Pins.togglingMute, SessionModes).
import { t } from "../i18n";
import type { AgentMutes } from "./agent-groups";
import { sessionCan } from "./format";
import type { Session } from "./types";

export interface ModeOption {
  id: string;
  title: string;
  detail: string;
}

export const PERMISSION_MODES: ModeOption[] = [
  { id: "default", title: t("lib-session-actions.mode_default"), detail: t("lib-session-actions.mode_default_detail") },
  { id: "acceptEdits", title: t("lib-session-actions.mode_accept_edits"), detail: t("lib-session-actions.mode_accept_edits_detail") },
  { id: "plan", title: t("lib-session-actions.mode_plan"), detail: t("lib-session-actions.mode_plan_detail") },
  { id: "auto", title: t("lib-session-actions.mode_auto"), detail: t("lib-session-actions.mode_auto_detail") },
  { id: "bypassPermissions", title: t("lib-session-actions.mode_bypass_permissions"), detail: t("lib-session-actions.mode_bypass_permissions_detail") },
];

export const EFFORTS: ModeOption[] = [
  { id: "low", title: t("lib-session-actions.effort_low"), detail: t("lib-session-actions.effort_low_detail") },
  { id: "medium", title: t("lib-session-actions.effort_medium"), detail: t("lib-session-actions.effort_medium_detail") },
  { id: "high", title: t("lib-session-actions.effort_high"), detail: t("lib-session-actions.effort_high_detail") },
  { id: "xhigh", title: t("lib-session-actions.effort_xhigh"), detail: t("lib-session-actions.effort_xhigh_detail") },
  { id: "max", title: t("lib-session-actions.effort_max"), detail: t("lib-session-actions.effort_max_detail") },
];

/** Codex's reasoning efforts (node/codex_effort.go); a thread offers those of its model, in `Session.efforts`. */
export const CODEX_EFFORTS: ModeOption[] = [
  { id: "minimal", title: t("lib-session-actions.codex_effort_minimal"), detail: t("lib-session-actions.codex_effort_minimal_detail") },
  { id: "low", title: t("lib-session-actions.codex_effort_low"), detail: t("lib-session-actions.codex_effort_low_detail") },
  { id: "medium", title: t("lib-session-actions.codex_effort_medium"), detail: t("lib-session-actions.codex_effort_medium_detail") },
  { id: "high", title: t("lib-session-actions.codex_effort_high"), detail: t("lib-session-actions.codex_effort_high_detail") },
  { id: "xhigh", title: t("lib-session-actions.codex_effort_xhigh"), detail: t("lib-session-actions.codex_effort_xhigh_detail") },
  { id: "max", title: t("lib-session-actions.codex_effort_max"), detail: t("lib-session-actions.codex_effort_max_detail") },
  { id: "ultra", title: t("lib-session-actions.codex_effort_ultra"), detail: t("lib-session-actions.codex_effort_ultra_detail") },
];

/** Modes a running session can switch to: "Allow all" is chosen only when it starts. */
export const SWITCHABLE_MODES = PERMISSION_MODES.filter((m) => m.id !== "bypassPermissions");

export const BYPASS_MODE = "bypassPermissions";

/** What "Allow all" means, said in red where it is on (iOS's warning). */
export const BYPASS_WARNING = t("lib-session-actions.bypass_warning");

/** An assistant conversation (the main one too) switches to any mode, "Allow all" included: the hub restarts its
 * Claude with the new mode (#2416). So does an agent whose Claude was started with a flag that puts "Allow all" in
 * its mode cycle (`bypass`: Session.bypass_available, or it is in that mode now; #1379). Other agents keep
 * SWITCHABLE_MODES. */
const ALL_MODES = PERMISSION_MODES.map((m) => (m.id === BYPASS_MODE ? { ...m, detail: t("lib-session-actions.mode_bypass_permissions_assistant_detail") } : m));

export function switchableModes(assistant: boolean | undefined, agent?: string, bypass?: boolean): ModeOption[] {
  return assistant || agent === "agy" || bypass ? ALL_MODES : SWITCHABLE_MODES;
}

/** Whether a running agent's Claude can be switched to "Allow all" (see switchableModes). */
export function canSwitchToBypass(s: Session): boolean {
  return Boolean(s.bypass_available || s.permission_mode === BYPASS_MODE);
}

export function modeTitle(id?: string): string {
  return id ? (PERMISSION_MODES.find((m) => m.id === id)?.title ?? id) : "";
}

export function effortTitle(id?: string): string {
  return id ? (EFFORTS.find((m) => m.id === id)?.title ?? CODEX_EFFORTS.find((m) => m.id === id)?.title ?? id) : "";
}

/** The two chips (Claude Code's TUI bottom bar). The node says where they work (`capabilities.modes`, #1843): a runtime
 *  that applies them (Orca, Antigravity), never a Claude outside Persona's terminals. An older node does not say, and the
 *  chips follow the agent: Claude Code in Persona's terminals, Antigravity; Codex, a compatible CLI and other sources not. */
export function hasModes(s: Session | undefined): boolean {
  if (!s || s.assistant) return false;
  if (typeof s.capabilities?.modes === "boolean") return s.capabilities.modes;
  if (s.agent === "agy") return true;
  if (s.source_kind) return false;
  return !s.agent || s.agent === "claude";
}

/** Only the effort chip: the node lists the levels it takes (`efforts`: a Codex thread on its app-server), and the phone may write to it. */
export function hasEffortOnly(s: Session | undefined): boolean {
  return Boolean(s && !s.assistant && !hasModes(s) && s.efforts?.length && sessionCan(s, "send"));
}

/** The effort menu's choices: the session's own levels when the node lists them, Claude Code's otherwise. */
export function effortOptions(s: Session): ModeOption[] {
  if (!s.efforts?.length) return EFFORTS;
  return s.efforts.map((id) => CODEX_EFFORTS.find((m) => m.id === id) ?? { id, title: id, detail: "" });
}

const key = (s: Pick<Session, "machine_id" | "session_id">) => `${s.machine_id}/${s.session_id}`;

/** Whether the hub sends this agent's notifications (the rule of GET /mutes). */
export function isMuted(s: Pick<Session, "machine_id" | "session_id" | "space_name">, m: AgentMutes): boolean {
  if (m.sessions.includes(key(s))) return true;
  if (m.allowed?.includes(key(s))) return false;
  return Boolean(s.space_name && m.spaces.includes(`${s.machine_id}/${s.space_name}`));
}

/** The mutes after flipping one agent: its own switch sits on top of its space's (iOS Pins.togglingMute). */
export function toggleMute(s: Pick<Session, "machine_id" | "session_id" | "space_name">, m: AgentMutes): AgentMutes {
  const k = key(s);
  const sessions = m.sessions.filter((x) => x !== k);
  const allowed = (m.allowed ?? []).filter((x) => x !== k);
  const spaceMuted = Boolean(s.space_name && m.spaces.includes(`${s.machine_id}/${s.space_name}`));
  if (isMuted(s, m)) return { spaces: m.spaces, sessions, allowed: spaceMuted ? [...allowed, k] : allowed };
  return { spaces: m.spaces, sessions: spaceMuted ? sessions : [...sessions, k], allowed };
}

/** The rename request (iOS RenameSessionAlert): only what changed; the herdr tab only for an agent in a herdr pane.
 * Returns null when nothing changed. */
export function renameBody(s: Session, tab: string, title: string): { tab_name?: string; title?: string } | null {
  const t = tab.trim(), n = title.trim();
  const body: { tab_name?: string; title?: string } = {};
  if (!s.assistant && s.slot_id && t && t !== (s.slot_name ?? "")) body.tab_name = t;
  if (n && n !== s.title) body.title = n;
  return Object.keys(body).length ? body : null;
}
