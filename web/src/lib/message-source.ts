import { t } from "../i18n";
import type { Injected, Item, MessageSource } from "./types";

const LEADING_CONTEXT = /^\s*<persona-context\b([^>]*)>([\s\S]*?)<\/persona-context>\s*/;
const RELAY_TITLE = /\btitle="来自对话「(.*?)」"/u; // i18n-ignore: canonical relay template
const RELAY_BODY = /^这条消息不是用户本人发的，是 Persona 的另一段对话（([^\s（）]+)）用 (conversation_send|conversation_new) 发来的，第 ([0-9]+) 跳（最多 ([0-9]+) 跳）。\n按里面说的做。要把结果告诉那段对话就用 conversation_send 发回 ([^\s；]+)；只是确认收到就不要回，免得两段对话来回发。$/u; // i18n-ignore: canonical relay template

function* leadingContexts(raw: string): Generator<{ attrs: string; text: string }> {
  let text = raw;
  for (let match = LEADING_CONTEXT.exec(text); match; match = LEADING_CONTEXT.exec(text)) {
    yield { attrs: match[1], text: match[2].trim() };
    text = text.slice(match[0].length);
  }
}

function sourceMetadata(raw: string): { found: boolean; source?: MessageSource } {
  for (const context of leadingContexts(raw)) {
    if (!/\bkind="message-source"/.test(context.attrs)) continue;
    try {
      const data: unknown = JSON.parse(context.text);
      if (!data || typeof data !== "object" || Array.isArray(data)) return { found: true };
      const meta = data as Record<string, unknown>;
      if (meta.author === "user" || typeof meta.kind !== "string" || !meta.kind.trim()) return { found: true };
      return { found: true, source: {
        kind: meta.kind,
        ...(typeof meta.conversation_id === "string" ? { conversation_id: meta.conversation_id } : {}),
        ...(typeof meta.title === "string" ? { title: meta.title } : {}),
      } };
    } catch { return { found: true }; }
  }
  return { found: false };
}

/** The first leading metadata block protects user quotations and malformed explicit metadata from guessing. */
export function userSourceSentinel(text: string): boolean {
  const metadata = sourceMetadata(text);
  return metadata.found && !metadata.source;
}

const unescape = (value: string) => value.replace(/&quot;/g, '"').replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");

/** Legacy identity requires the complete leading machine template, including matching sender/reply ids. */
export function legacyConversationSource(raw: string): MessageSource | undefined {
  for (const context of leadingContexts(raw)) {
    const title = RELAY_TITLE.exec(context.attrs);
    const body = RELAY_BODY.exec(context.text);
    if (/\bkind="context"/.test(context.attrs) && title && body && body[1] === body[5]) return { kind: "conversation", conversation_id: body[1], title: unescape(title[1]) };
  }
}

/** Only the verified relay control envelope is omitted; selected/knowledge contexts remain inspectable. */
export function isRelayContext(context: { kind: string; title?: string; text: string }): boolean {
  const body = RELAY_BODY.exec(context.text.trim());
  return context.kind === "context" && /^来自对话「.*」$/u.test(context.title ?? "") && Boolean(body && body[1] === body[5]); // i18n-ignore: canonical relay title
}

/** Explicit provenance wins, then durable metadata, injected compatibility, and complete legacy relay templates. */
export function sourceOf(item: Item, injected: Injected | null): MessageSource | undefined {
  if (item.source && typeof item.source.kind === "string" && item.source.kind.trim()) return item.source;
  if (item.client_msg_id || item.from) return undefined;
  const metadata = sourceMetadata(item.text ?? "");
  if (metadata.found) return metadata.source;
  if (injected?.state === "supervise") return { kind: "supervision", title: injected.title };
  if (injected?.kind === "followup") return {
    kind: injected.state === "daily" || injected.state === "interval" ? "patrol" : "followup",
    title: injected.title,
  };
  return legacyConversationSource(item.text ?? "");
}

export function sourceLabel(source: MessageSource): string {
  return source.title?.trim() || t(`chat-messages.source_${["conversation", "patrol", "followup", "supervision"].includes(source.kind) ? source.kind : "other"}`);
}

/** Equal titles alone must not merge different originating conversations. */
export function sourceKey(source: MessageSource): string {
  return JSON.stringify([source.kind, source.conversation_id ?? "", source.title ?? ""]);
}
