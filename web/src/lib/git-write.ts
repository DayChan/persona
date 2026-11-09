// The Git panel's writes (#6121, docs/protocol/protocol.md "git/stage … git/stash"): what they answer and what a refusal means.
// Each is a click the panel confirms first when it leaves the computer or may lose work; the node never forces.
import { api, ApiError, enc, errorText } from "./api";
import type { GitCommit, GitStatus } from "./files";
import { t } from "../i18n";

export interface GitBranch {
  name: string;
  remote?: boolean;
  current?: boolean;
  upstream?: string;
  hash: string;
  subject: string;
  date: string;
}

export interface GitStashEntry {
  index: number;
  /** Missing on older nodes: never write by index alone. */
  oid?: string;
  message: string;
  date: string;
}

/** What a write answers: the repository as it is after it. */
export interface GitWriteResult {
  status: GitStatus;
  commit?: GitCommit;
  output?: string;
  stashes?: GitStashEntry[];
  /** A pull moved the branch (false: it was already up to date). */
  updated?: boolean;
}

/** `pull` is the fast-forward pull every client has (`git/pull`, #1887); the rest are the `git_write` feature's. */
export type GitWriteOp = "stage" | "commit" | "push" | "switch" | "stash" | "pull";

export function hasStashIdentity(e: GitStashEntry): boolean {
  return typeof e.oid === "string" && /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(e.oid);
}

export function gitWrite(base: string, op: GitWriteOp, body: Record<string, unknown>): Promise<GitWriteResult> {
  return api<GitWriteResult>(`${base}/${op}`, { method: "POST", json: body });
}

export function gitBranches(base: string, cwd: string, signal?: AbortSignal): Promise<{ branches: GitBranch[] }> {
  return api(`${base}/branches?cwd=${enc(cwd)}`, { signal });
}

/** A refusal said for the user: what happened, git's own words, the files it is about. */
export interface GitRefusal {
  title: string;
  detail: string;
  files: string[];
}

const KNOWN = new Set([
  "git_not_repo",
  "git_detached",
  "git_nothing_staged",
  "git_hook_failed",
  "git_identity",
  "git_no_upstream",
  "git_no_remote",
  "git_push_rejected",
  "git_auth",
  "git_conflict",
  "git_busy",
  "git_no_branch",
  "git_no_stash",
  "git_stash_stale",
  "git_stash_unsupported",
  "git_not_pushed",
  "git_no_pr",
  "git_not_github",
  "no_gh",
  "not_signed_in",
]);

export function gitRefusal(e: unknown): GitRefusal {
  if (e instanceof ApiError) {
    const files = Array.isArray(e.body.files) ? e.body.files.filter((f): f is string => typeof f === "string") : [];
    const code = e.code ?? "";
    if (KNOWN.has(code)) return { title: t(`session-gitwrite.refused_${code}`), detail: e.message, files };
    // A node from before #6121 does not know the method: Persona there needs updating.
    if (/unknown method git\./.test(e.message)) return { title: t("session-gitwrite.refused_old_node"), detail: e.message, files };
    return { title: t("session-gitwrite.refused"), detail: errorText(e), files };
  }
  return { title: t("session-gitwrite.refused"), detail: errorText(e), files: [] };
}

/** Whether every change of f is staged (its checkbox is on), partly (mixed) or not at all. */
export function stageState(f: { index: string; worktree: string }): "all" | "some" | "none" {
  const inIndex = f.index !== "." && f.index !== "?";
  const inTree = f.worktree !== "." && f.worktree !== "?" ? true : f.index === "?";
  if (inIndex && !inTree) return "all";
  return inIndex ? "some" : "none";
}
