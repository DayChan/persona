import { t } from "../i18n";
import { isEcho, type Seen } from "./echo";
import { ApiError, errorText, isAbort, isTransient, sessionPath } from "./api";
import { parseUserText } from "./turns";
import { imagePayload, quotePath, releaseAttachments, sendFile, type LocalAttachment } from "./attachments";
import { uuid } from "./format";
import { apiOp } from "./ops";
import { currentPlatform, modName, type Platform } from "./shortcuts";
import { href, navigate } from "./router";
import { getState, putLaunch, upsertSession } from "./store";
import { ultracodeErrorText, ultracodeRefused } from "./ultracode";
import type { Session } from "./types";

export interface AgentLaunch {
  id: string;
  machineId: string;
  cwd: string;
  launcher: string;
  /** Picked in "New agent" for this start only (launcher-models.ts startPicks); none: the launcher's own. */
  model?: string;
  effort?: string;
  /** Claude Code's permission mode for this start ("New agent"'s "Permission mode"); none: the launcher's own. */
  permissionMode?: string;
  /** The engine host to open in (#2576), when the user picked one other than the machine's default; none: the default. */
  host?: string;
  /** Turn ultracode on once Claude is up (#3428); only ever true: off is what a new Claude is (docs/protocol/protocol.md). */
  ultracode?: boolean;
  text: string;
  attachments: LocalAttachment[];
  todoId?: string;
  phase: "creating" | "uploading" | "sending" | "failed" | "ready";
  session?: Session;
  outgoing?: { text: string; images: { mime: string; data: string }[] };
  uploaded: Record<string, string>;
  error?: string;
  /** The start's op id (the hub does it once per id); `startPromptOpId` is for the Codex start with a first message. */
  startOpId: string;
  startPromptOpId: string;
  /** The first message's client_msg_id, its op id too. */
  messageId: string;
  /** The hub answered that it doesn't know how this went: only the person can say whether to go on. */
  uncertain?: boolean;
  /** The start failed in a gap and the hub may have it: Retry repeats it unchanged, under the same op id. */
  repeatOnly?: boolean;
  clearable?: string;
  draft?: string;
}

export function launchAgent(input: Pick<AgentLaunch, "machineId" | "cwd" | "launcher" | "model" | "effort" | "permissionMode" | "host" | "ultracode" | "text" | "attachments" | "todoId">) {
  const launch: AgentLaunch = { ...input, id: `local-${uuid()}`, phase: "creating", uploaded: {}, ...launchOpIds() };
  putLaunch(launch);
  navigate({ name: "session", mid: launch.machineId, sid: launch.id });
  // Let the local conversation paint before serialization, uploads and remote startup.
  setTimeout(() => void deliverLaunch(launch.id), 0);
  return launch.id;
}

/** Fresh op ids: the hub keeps its answer to an id, so a try after a refusal needs new ones (a gap keeps the old ones). */
function launchOpIds() {
  return { startOpId: uuid(), startPromptOpId: uuid(), messageId: uuid() };
}

/** One step of a start under its op id. A repeat can come back 202: the first try still running (ask again; the hub
 *  waits about 10 s each time), or lost to a hub restart (nobody knows how it went). */
async function launchOp<T>(path: string, opId: string, signal: AbortSignal, json: unknown): Promise<T> {
  for (;;) {
    const r = await apiOp<T & { state?: string }>(path, { method: "POST", signal, json, opId });
    if (r.state === "unknown") throw new ApiError(202, t("lib-new-agent.outcome_unknown"), { delivered: "unknown" });
    if (r.state !== "pending") return r;
  }
}

/** `user` (a user message, or an item the hub tied to a message) is the copy of a started conversation's first message. */
export function acknowledgeLaunch(mid: string, sid: string, user: Seen) {
  for (const launch of Object.values(getState().launches)) {
    if (launch.phase !== "ready" || launch.machineId !== mid || launch.session?.session_id !== sid || !launch.outgoing) continue;
    // the conversation's first message: nothing older in it can say the same, so no time to compare
    if (isEcho({ id: launch.messageId, text: parseUserText(launch.outgoing.text).text, images: launch.outgoing.images, at: 0 }, user)) putLaunch({ ...launch, outgoing: undefined });
  }
}

const delivering = new Set<string>();

export async function deliverLaunch(id: string, clearDraft?: string) {
  let launch: AgentLaunch = getState().launches[id];
  if (!launch || delivering.has(id) || (launch.phase !== "failed" && launch.phase !== "creating")) return;
  delivering.add(id);
  const controller = new AbortController();
  const deadline = setTimeout(() => controller.abort(), 240_000);
  const update = (patch: Partial<AgentLaunch>) => {
    launch = { ...launch, ...patch };
    if (getState().launches[id]) putLaunch(launch);
  };
  update({ phase: launch.session ? "uploading" : "creating", error: undefined, uncertain: false, clearable: undefined, draft: undefined });
  try {
    const imageBytes = launch.attachments.filter((a) => a.mime.startsWith("image/")).reduce((total, a) => total + Math.ceil(a.file.size / 3) * 4, 0);
    if (imageBytes + new TextEncoder().encode(launch.text).byteLength > 31 * 1024 * 1024) throw new Error(t("lib-new-agent.attachments_too_large_start"));
    if (!launch.session) {
      let session: Session;
      try {
        const r = await launchOp<{ session: Session }>("/sessions", launch.startOpId, controller.signal, { machine_id: launch.machineId, cwd: launch.cwd, launcher: launch.launcher || undefined, model: launch.model || undefined, effort: launch.effort || undefined, permission_mode: launch.permissionMode || undefined, host: launch.host || undefined, ultracode: launch.ultracode === true ? true : undefined, async: true, name_hint: launch.text || undefined, todo_id: launch.todoId || undefined });
        session = r.session;
      } catch (e) {
        if (!(e instanceof ApiError) || e.code !== "codex_needs_prompt") throw e;
        if (launch.attachments.length) throw new Error(t("lib-new-agent.launcher_no_attachments"));
        if (!launch.text) throw new Error(t("lib-new-agent.launcher_needs_text"));
        // A Codex start takes the picked model but no effort.
        const r = await launchOp<{ session: Session }>("/sessions", launch.startPromptOpId, controller.signal, { machine_id: launch.machineId, cwd: launch.cwd, launcher: launch.launcher || undefined, model: launch.model || undefined, host: launch.host || undefined, prompt: launch.text, todo_id: launch.todoId || undefined });
        session = r.session;
        update({ outgoing: { text: "", images: [] } });
      }
      if (!getState().launches[id]) return;
      update({ session });
      upsertSession(session);
    }
    if (!launch.outgoing) {
      update({ phase: "uploading" });
      const paths: string[] = [];
      const images: { mime: string; data: string }[] = [];
      for (const att of launch.attachments) {
        if (att.mime.startsWith("image/")) {
          images.push(await imagePayload(att.file));
        } else {
          let path = launch.uploaded[att.id];
          if (!path) {
            path = await sendFile(launch.machineId, att.file, { name: att.name, signal: controller.signal });
            update({ uploaded: { ...launch.uploaded, [att.id]: path } });
          }
          paths.push(quotePath(path));
        }
      }
      update({ outgoing: { text: [launch.text, ...paths].filter(Boolean).join("\n"), images } });
    }
    if (!getState().launches[id]) return;
    controller.signal.throwIfAborted();
    const out = launch.outgoing!;
    if (out.text || out.images.length) {
      update({ phase: "sending" });
      const payload = { ...out, client_msg_id: launch.messageId, clear_draft: clearDraft, todo_id: launch.todoId || undefined };
      if (new TextEncoder().encode(JSON.stringify(payload)).byteLength > 32 * 1024 * 1024) throw new Error(t("lib-new-agent.attachments_too_large_retry"));
      await launchOp(`${sessionPath(launch.machineId, launch.session!.session_id)}/messages`, launch.messageId, controller.signal, payload);
    }
    update({ phase: "ready" });
    releaseAttachments(launch.attachments);
    update({ attachments: [] });
    const local = { name: "session" as const, mid: launch.machineId, sid: launch.id };
    if (location.pathname === href(local)) navigate({ ...local, sid: launch.session!.session_id }, true);
  } catch (e) {
    const phase: AgentLaunch["phase"] = getState().launches[id]?.phase ?? "failed";
    const sending = phase === "sending";
    const creating = phase === "creating";
    // no answer: the same op id on Retry finds out whether the hub had it, so nothing starts or sends twice
    const gap = isTransient(e) || isAbort(e);
    // the hub answered that it doesn't know, and its receipt would say so again: going on takes new ids and the person's word
    const uncertain = (creating || sending) && !gap && e instanceof ApiError && (e.body.delivered === "unknown" || (e.status >= 500 && e.body.delivered !== "none"));
    // the hub won't start it with ultracode here (an old node, a CLI or engine that can't take it): asking again would
    // get the same answer, so "Retry" starts it without, and the card says so (#3428)
    const noUltracode = creating && launch.ultracode === true && ultracodeRefused(e);
    update({ phase: "failed", uncertain, repeatOnly: creating && gap, ...(gap ? {} : launchOpIds()),
      ...(noUltracode ? { ultracode: undefined } : {}),
      error: noUltracode ? t("ultracode.start_refused", { error: ultracodeErrorText(e) }) : t(creating ? "lib-new-agent.start_failed" : sending ? "lib-new-agent.first_message_failed" : "lib-new-agent.attachments_failed", { error: errorText(e) }),
      clearable: e instanceof ApiError && e.code === "input_draft" && typeof e.body.clearable === "string" ? e.body.clearable : undefined,
      draft: e instanceof ApiError && e.code === "input_draft" ? String(e.body.draft ?? "") : undefined,
    });
  } finally {
    clearTimeout(deadline);
    delivering.delete(id);
  }
}

// ── opening "New agent" from another page ──────────────

/** What "New agent" opens with when another page opens it ("Knowledge"'s "Start agent"): the first message, and where if known. */
export interface NewAgentPreset {
  prompt?: string;
  machineId?: string;
  cwd?: string;
}

const openers = new Set<(preset: NewAgentPreset) => void>();

/** Opens "New agent" (the app shell listens) with `preset` filled in. */
export function openNewAgent(preset: NewAgentPreset = {}) {
  openers.forEach((l) => l(preset));
}

export function onOpenNewAgent(l: (preset: NewAgentPreset) => void) {
  openers.add(l);
  return () => {
    openers.delete(l);
  };
}

/**
 * The keys that start "New agent" from the first-message box, for its placeholder: Mod ↵ (⌘ on Apple, Ctrl elsewhere, as
 * the shortcuts name it), and none on a touch-only device (a phone has no ⌘ key, so the hint would only take up room).
 */
export function startKeys(platform: Platform, touchOnly: boolean): string | null {
  return touchOnly ? null : `${modName(platform)} ↵`;
}

/** startKeys for this browser. */
export function browserStartKeys(): string | null {
  const touchOnly = typeof matchMedia === "function" && matchMedia("(hover: none) and (pointer: coarse)").matches;
  return startKeys(currentPlatform(), touchOnly);
}

/** The first message with a preset: its text first, then what was already being written (a draft isn't lost). */
export function presetPrompt(preset: string | undefined, draft: string): string {
  const p = (preset ?? "").trimEnd();
  if (!p) return draft;
  if (!draft.trim()) return `${p}\n\n`;
  if (draft.startsWith(p)) return draft;
  return `${p}\n\n${draft.trim()}`;
}

