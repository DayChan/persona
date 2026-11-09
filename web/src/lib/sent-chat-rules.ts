import { t } from "../i18n";
import { api, ApiError, credentialOwner, subscribeCredentialOwner } from "./api";
import { defaultManager, shownManager, onManagerChoice, onDefaultManager } from "./manager-choice";
import type { SentItem } from "./sent";

export interface ChatRuleState { can: boolean; exists: boolean; id?: string; reason?: string }
export interface ChatRule { id: string; connector: string; chat: string; at: string; row?: number; summary?: string }
export interface ChatRules { rules: ChatRule[]; most: number }
export const CHAT_RULES = "/connectors/sent/chat-rules";
// These settings routes belong to the default manager, not the assistant switcher's chosen manager.
// Retire callbacks on either context transition; api() retains the existing routing and 401 policy.
let managerGeneration = 0;
onManagerChoice(() => { managerGeneration++; });
onDefaultManager(() => { managerGeneration++; });
export const sentOwner = () => JSON.stringify([credentialOwner(), defaultManager(), shownManager(), managerGeneration]);
export function subscribeSentOwner(listener: () => void): () => void {
  const stops = [subscribeCredentialOwner(listener), onManagerChoice(listener), onDefaultManager(listener)];
  return () => stops.forEach(stop => stop());
}
export const listChatRules = (signal?: AbortSignal) => api<ChatRules>(CHAT_RULES, { signal });
export const addChatRule = (row: number) => api(CHAT_RULES, { method: "POST", json: { row } });
export const revokeChatRule = (id: string) => api(`${CHAT_RULES}/${encodeURIComponent(id)}`, { method: "DELETE" });

/** Display only: the hub constructs the actual rule from the row. Never infer eligibility here. */
export function chatDestination(row: SentItem): string {
  const value = row.target[row.connector === "github" ? "ref" : "conversation"];
  return typeof value === "string" ? value : "";
}
export function chatRuleReason(reason?: string): string {
  const key = ["recorded", "mail", "review", "reply", "route", "target", "command"].includes(reason ?? "") ? reason : "unknown";
  return t(`settings-sent.rule_reason_${key}`);
}
export function chatRuleProblem(error: unknown, action?: "add" | "revoke"): { text: string; unavailable: boolean } {
  if (error instanceof ApiError) {
    const key = error.status === 403 ? "rule_forbidden"
      : error.status === 503 && error.code === "no_database" ? "rule_no_database"
      : (error.status === 404 && !error.code) || error.status === 405 ? "rule_unsupported"
      : error.status === 404 && error.code === "not_found" ? (action === "add" ? "rule_pruned" : "rule_gone")
      : error.code === "too_many_rules" ? "rule_full"
      : error.status === 500 && error.code === "store_failed" && action === "revoke" ? "rule_store_failed" : "";
    if (key) return { text: t(`settings-sent.${key}`), unavailable: ["rule_forbidden", "rule_no_database", "rule_unsupported"].includes(key) };
    if (error.code === "no_chat_rule") return { text: chatRuleReason(typeof error.body.reason === "string" ? error.body.reason : undefined), unavailable: false };
  }
  return { text: t(action ? "settings-sent.rule_error" : "settings-sent.rule_read_error"), unavailable: false };
}
