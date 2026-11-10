// "Rewind to here" (#2436, Moshi's "Rewind to here"): go back in a Claude session to before one of your messages. The node drives
// Claude Code's own /rewind in the session's terminal (node/rewind.go), so what the app has to say is which message:
// its text, which of the messages with that text counted from the end, and how many messages came after it. Both the
// list in Claude's TUI and the app's transcript are read from the end, so a page of older messages not loaded yet changes
// nothing. The node checks all three against its screen and stops, changing nothing, when they disagree.
import { api, ApiError, errorText, sessionPath } from "./api";
import { t } from "../i18n";
import { stateOf } from "./format";
import type { Item, Session } from "./types";
import { stateName } from "./state-labels";
import { injectedOf } from "./injected";
import { sourceOf } from "./message-source";

export type RewindRestore = "conversation" | "code_and_conversation";

export interface RewindTarget {
  /** The message as the transcript has it (Claude's own words for it, tags and all). */
  prompt: string;
  /** It is the Nth message with this text counting from the end (1 = the last one). */
  occurrence: number;
  /** How many of your messages came after it. */
  promptsAfter: number;
}

export interface RewindResult {
  prompt: string;
  restore: RewindRestore;
  /** The files went back too (not when nothing changed after the message). */
  code_restored: boolean;
  /** Claude's input box was emptied: the message is in the app's composer instead. */
  input_cleared: boolean;
}

/** One message of yours, as Claude Code lists it in /rewind: the lines of one transcript entry are one message. */
interface Prompt {
  id: string; // the first item of the entry: UserMessage.id
  raw: string;
}

const entryOf = (it: Pick<Item, "id">) => it.id.split("#")[0];

/** Your messages in order. Images alone are a message too (Claude lists them), with no text; a message queued while
 *  Claude worked is not in the list yet. */
export function promptsOf(items: Item[]): Prompt[] {
  const out: Prompt[] = [];
  let entry = "";
  for (const it of items) {
    if (it.role !== "user" || it.queued || !(it.kind === "text" || (it.kind === "image" && !it.tool_use_id))) continue;
    if (out.length && entryOf(it) === entry) {
      if (it.kind === "text" && it.text) out[out.length - 1].raw += (out[out.length - 1].raw ? "\n" : "") + it.text;
      continue;
    }
    entry = entryOf(it);
    out.push({ id: it.id, raw: it.kind === "text" ? (it.text ?? "") : "" });
  }
  return out;
}

/** Only the actual last prompt can be recalled. Do not skip image-only, injected or incomplete inputs and rewind
 *  an earlier turn instead. Binary images cannot be restored by the text draft path. */
export function lastEditablePrompt(items: Item[]): Prompt | null {
  const last = promptsOf(items).at(-1);
  if (!last?.raw.trim()) return null;
  const blocks = items.filter((it) => entryOf(it) === entryOf(last));
  if (blocks.some((it) => it.kind === "image" || it.full_size || it.from || injectedOf(it) || sourceOf(it, null))) return null;
  return last;
}

const squash = (s: string) => s.replace(/\s+/g, "");

/** Which message a menu item stands for, in the words the node needs; null when it cannot be told apart (no words). */
export function rewindTarget(items: Item[], messageId: string): RewindTarget | null {
  const all = promptsOf(items);
  const at = all.findIndex((p) => p.id === messageId);
  if (at < 0 || !all[at].raw.trim()) return null;
  const same = squash(all[at].raw);
  return {
    prompt: all[at].raw,
    occurrence: 1 + all.slice(at + 1).filter((p) => squash(p.raw) === same).length,
    promptsAfter: all.length - 1 - at,
  };
}

/** Why "Rewind to here" is off for this session ("" when it is on). Short: it is the hint on the menu row. */
export function rewindBlock(session: Session | undefined, busy: boolean): string {
  if (!session || session.assistant || session.source_kind || (session.agent && session.agent !== "claude")) return t("rewind.block_claude_only");
  const state = stateOf(session);
  if (state === "offline" || session.starting) return stateName("offline");
  if (state === "needs_you" || (session.pending_interactions ?? 0) > 0) return stateName("needs_you");
  if (busy || state === "working") return stateName("working");
  return "";
}

/** The hub's answer to a rewind that did not happen, in words for the toast. */
export function rewindFailure(e: unknown): string {
  if (e instanceof ApiError && (e.status === 404 || e.status === 405) && e.code !== "gone") return t("rewind.hub_unsupported");
  if (e instanceof ApiError && e.status === 403) return t("rewind.needs_full_device");
  return errorText(e);
}

/** Asks the node to go back to before the message. Rejects with the reason in the error's message. */
export async function rewindSession(mid: string, sid: string, target: RewindTarget, restore: RewindRestore): Promise<RewindResult> {
  try {
    return await api<RewindResult>(`${sessionPath(mid, sid)}/rewind`, {
      method: "POST",
      json: { prompt: target.prompt, occurrence: target.occurrence, prompts_after: target.promptsAfter, restore },
    });
  } catch (e) {
    throw new Error(rewindFailure(e));
  }
}

/** What the toast says after a rewind went through. */
export function rewindDone(res: RewindResult, asked: RewindRestore): { text: string; tone: "ok" | "info" } {
  if (asked === "code_and_conversation" && !res.code_restored) return { text: t("rewind.done_no_code_changes"), tone: "ok" };
  return { text: t(res.code_restored ? "rewind.done_code_and_conversation" : "rewind.done_conversation"), tone: "ok" };
}
