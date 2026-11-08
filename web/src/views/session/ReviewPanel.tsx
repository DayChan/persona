// Reviewing a session's changes (docs/web/review.md, #151): every file the session changed, each with its diff, drawn
// by the same code review an extension's code page has (views/extensions/DiffReview.tsx: words that changed, whitespace,
// viewed marks, folded generated and big files, keys, ranges, suggestions). A comment on lines waits in the review
// (kept per session in this browser) or goes to the message box at once; Send review sends the summary and every
// comment as one message through the session's composer, as typing it would. Logic in lib/session-review.ts.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, enc, errorText, isAbort } from "../../lib/api";
import { changeId, LineCache } from "../../lib/code-review";
import { addCodeComment, commentKey } from "../../lib/comment-target";
import { addQuote, sendDraft } from "../../lib/drafts";
import type { UIAction, UIActionResult, UIView } from "../../lib/extension-ui";
import type { FileContent, GitCommit } from "../../lib/files";
import { splitLines } from "../../lib/files";
import { shortPath } from "../../lib/format";
import { whenComposer } from "../../lib/links";
import {
  loadReview, loadReviewFile, orderedComments, pendingComment, pendingReview, reviewDiff, reviewItems, reviewMessage, reviewQuote, reviewTotals, setPendingReview, usePendingReview,
  type GitReview, type GitReviewFile, type ReviewScope, type ReviewTarget,
} from "../../lib/session-review";
import { useFsLive } from "../../lib/use-fs-live";
import { Button, cx, IconButton, Loading, Menu, toast, useMenu } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import type { MenuItem } from "../../ui/menu-model";
import { t } from "../../i18n";
import { DiffView, KeysHelp, ReviewBar, ReviewKeys, ReviewProvider, ReviewToolbar } from "../extensions/DiffReview";
import { PanelHeader } from "../SidePanel";
import { Empty, Segmented } from "./files/parts";

/** The panel sits beside the chat (it covers it below this width). */
const besideChat = () => typeof matchMedia === "undefined" || matchMedia("(min-width: 1024px)").matches;

const targetKey = (session: string) => `persona.review.target:${session}`;

function storedTarget(session: string, scope?: ReviewScope): ReviewTarget {
  if (scope) return { scope };
  try {
    const v = JSON.parse(localStorage.getItem(targetKey(session)) ?? "null") as ReviewTarget | null;
    if (v && (v.scope === "uncommitted" || v.scope === "branch" || (v.scope === "commit" && v.base))) return v;
  } catch {
    // another build's value
  }
  return { scope: "uncommitted" };
}

type Failure = { code?: string; message: string };

export function ReviewPanel({ mid, sid, cwd, scope, at, onClose }: { mid: string; sid: string; cwd: string; scope?: ReviewScope; at?: { path: string; line: number; old?: boolean }; onClose: () => void }) {
  const session = `${mid}/${sid}`;
  const [target, setTargetState] = useState<ReviewTarget>(() => storedTarget(session, scope));
  const setTarget = (t: ReviewTarget) => {
    setTargetState(t);
    try {
      localStorage.setItem(targetKey(session), JSON.stringify(t));
    } catch {
      // private mode
    }
  };
  const [res, setRes] = useState<GitReview | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [loading, setLoading] = useState(true);
  const [reloads, setReloads] = useState(0);
  const [stale, setStale] = useState(false);
  const [loaded, setLoaded] = useState<Map<string, GitReviewFile>>(() => new Map());
  const [busy, setBusy] = useState("");
  const [help, setHelp] = useState(false);
  const [inside, setInside] = useState(false);
  const caches = useRef(new Map<string, LineCache>());
  const files = useRef(new Map<string, string[] | null>());
  const pending = usePendingReview(session);
  const key = commentKey(mid, sid);
  const you = t("review.you");

  useEffect(() => {
    if (target.scope === "commit" && !target.base) return;
    const ac = new AbortController();
    setLoading(true);
    loadReview(mid, cwd, target, ac.signal)
      .then((r) => {
        setRes(r);
        setFailure(null);
        setStale(false);
        setLoaded(new Map());
        caches.current = new Map();
        files.current = new Map();
      })
      .catch((e) => {
        if (isAbort(e)) return;
        setRes(null);
        setFailure({ code: e instanceof ApiError ? (e.body.code as string | undefined) : undefined, message: errorText(e) });
      })
      .finally(() => !ac.signal.aborted && setLoading(false));
    return () => ac.abort();
  }, [mid, cwd, target.scope, target.base, reloads]);

  // the agent goes on working: say the files moved, read them again when asked (not under the reader's eyes)
  useFsLive(mid, cwd, [], true, (c) => c.git && setStale(true));

  const reload = () => setReloads((n) => n + 1);
  const items = useMemo(() => (res ? reviewItems(res, loaded, key ? pending.comments : [], you) : []), [res, loaded, pending.comments, key, you]);
  const itemByPath = useMemo(() => new Map(items.map((it) => [it.path ?? "", it])), [items]);
  const fileOf = useCallback((path: string) => (res ? (loaded.get(path) ?? res.files.find((f) => f.path === path)) : undefined), [res, loaded]);
  const count = key ? pending.comments.length : 0;
  const view = useMemo<UIView>(
    () => ({
      sections: [{ items }],
      ...(key && res
        ? {
            review: {
              count,
              name: "body",
              placeholder: t("review.summary"),
              value: pending.note,
              buttons: [
                { title: t("review.send"), action: { id: "send" } },
                { title: t("review.compose"), action: { id: "compose" } },
              ],
              discard: { id: "discard" },
            },
          }
        : {}),
    }),
    [items, key, res, count, pending.note],
  );

  // a comment's card brought the review back: its file and line
  const went = useRef(false);
  useEffect(() => {
    if (!at || went.current || !res) return;
    went.current = true;
    requestAnimationFrame(() => {
      const d = document.querySelector(`[data-diff-path="${CSS.escape(at.path)}"]`);
      const line = d?.querySelector(`[data-ln="${at.old ? "old" : "new"}"][data-n="${at.line}"]`);
      (line ?? d)?.scrollIntoView({ block: line ? "center" : "start" });
    });
  }, [at, res]);

  /** The lines of a file on disk (the new side), read once per review. */
  const fileLines = async (path: string): Promise<string[] | null> => {
    if (files.current.has(path)) return files.current.get(path)!;
    const root = res?.root ?? cwd;
    try {
      const f = await api<FileContent>(`/machines/${enc(mid)}/file?path=${enc(`${root.replace(/\/+$/, "")}/${path}`)}&cwd=${enc(cwd)}`);
      const lines = typeof f.text === "string" ? splitLines(f.text) : null;
      files.current.set(path, lines);
      return lines;
    } catch {
      return null;
    }
  };

  const fetch = async (a: UIAction, v: Record<string, string> = {}): Promise<UIActionResult | null> => {
    const path = v.path ?? "";
    if (a.id === "context") {
      const lines = await fileLines(path);
      if (!lines) return null;
      return { lines: { path, side: "new", start: 1, lines, total: lines.length } };
    }
    if (a.id === "load") {
      const f = fileOf(path);
      if (!f || !res) return null;
      try {
        const got = await loadReviewFile(mid, cwd, res, f);
        if (!got) return null;
        // too big even on its own: said, not offered again
        setLoaded((m) => new Map(m).set(path, got.big ? { ...got, hunks: [], huge: true } : got));
        return {};
      } catch (e) {
        toast(errorText(e), "error");
        return null;
      }
    }
    return null;
  };

  const shownLine = (path: string) => (n: number) => {
    const it = itemByPath.get(path);
    return it ? caches.current.get(changeId(path, it.revision, it.hunks))?.get(n) : undefined;
  };
  const pick = (v: Record<string, string>) => ({ side: (v.side === "old" ? "old" : "new") as "old" | "new", line: Number(v.line), ...(v.start_line ? { start_line: Number(v.start_line) } : {}) });
  const where = { mid, sid, cwd };

  const send = async (note: string) => {
    if (!key || !res) return;
    const list = orderedComments(pendingReview(session).comments, res.files);
    if (!list.length && !note.trim()) return;
    // kept until it has gone: a send that fails leaves the review as it was, the summary too
    setPendingReview(session, { comments: pendingReview(session).comments, note });
    const text = reviewMessage(list, note, where);
    setBusy("send");
    whenComposer(
      key,
      async (c) => {
        const ok = c.ready?.() === false ? false : await c.send(text);
        setBusy("");
        if (ok) {
          setPendingReview(session, { comments: [] });
          toast(t("review.sent", { count: list.length }), "ok");
          if (!besideChat()) onClose(); // a phone: back to the chat it went to
        } else toast(t("review.not_sent"), "error");
      },
      () => {
        setBusy("");
        toast(t("review.no_box"), "error");
      },
      1500,
    );
  };

  const compose = (note: string) => {
    if (!key || !res) return;
    const list = orderedComments(pendingReview(session).comments, res.files);
    for (const c of list) addQuote(key, reviewQuote(c, where), false);
    if (note.trim()) sendDraft(key, { type: "append", text: note.trim() });
    setPendingReview(session, { comments: [] });
    toast(t("review.composed", { count: list.length }), "ok");
    if (!besideChat()) onClose();
  };

  const act = (a: UIAction, v: Record<string, string> = {}) => {
    switch (a.id) {
      case "draft": {
        const f = fileOf(v.path ?? "");
        if (!f || !res || !v.body?.trim()) return;
        const c = pendingComment(f, pick(v), v.body.trim(), reviewDiff(res), shownLine(f.path));
        const now = pendingReview(session);
        setPendingReview(session, { ...now, comments: [...now.comments, c] });
        return;
      }
      case "comment": {
        const f = fileOf(v.path ?? "");
        if (!f || !res || !key || !v.body?.trim()) return;
        const c = pendingComment(f, pick(v), v.body.trim(), reviewDiff(res), shownLine(f.path));
        addCodeComment(key, reviewQuote(c, where), c.body);
        return;
      }
      case "remove": {
        const now = pendingReview(session);
        setPendingReview(session, { ...now, comments: now.comments.filter((c) => c.id !== v.thread) });
        return;
      }
      case "load":
        setBusy("load");
        void fetch(a, v).finally(() => setBusy(""));
        return;
      case "send":
        void send(v.body ?? "");
        return;
      case "compose":
        compose(v.body ?? "");
        return;
      case "discard":
        if (confirm(t("review.discard_confirm", { count }))) setPendingReview(session, { comments: [] });
    }
  };

  const totals = res ? reviewTotals(res.files) : null;
  const subtitle = res
    ? [res.head?.branch || (res.head ? t("review.detached") : ""), t("review.files", { count: res.files.length }), totals && res.files.length ? `+${totals.added} −${totals.deleted}` : ""].filter(Boolean).join(" · ")
    : shortPath(cwd);
  const filesMenu = useMenu();
  const fileItems: MenuItem[] = (res?.files ?? []).map((f) => ({
    key: f.path,
    label: f.path,
    hint: f.binary ? t("review.binary") : `+${f.added} −${f.deleted}`,
    onSelect: () => document.querySelector(`[data-diff-path="${CSS.escape(f.path)}"]`)?.scrollIntoView({ block: "start" }),
  }));

  return (
    <div
      className="flex min-h-0 flex-1 flex-col outline-none"
      tabIndex={-1}
      data-review-panel=""
      onFocus={() => setInside(true)}
      onBlur={(e) => !e.currentTarget.contains(e.relatedTarget as Node | null) && setInside(false)}
      onPointerDown={(e) => {
        // the keys (n / p, j / k, v…) are the review's while the reader is in it
        if (e.target === e.currentTarget || !(e.target as HTMLElement).closest("input,textarea,button,a,[tabindex]")) e.currentTarget.focus({ preventScroll: true });
      }}
    >
      <PanelHeader
        icon="pullRequest"
        title={t("review.title")}
        subtitle={subtitle}
        onClose={onClose}
        actions={
          <>
            {res && res.files.length > 1 && (
              <>
                <IconButton icon="list" label={t("review.jump")} onClick={filesMenu.open} />
                {filesMenu.anchor && <Menu items={fileItems} anchor={filesMenu.anchor} onClose={filesMenu.close} align="right" width={320} header={t("review.files", { count: res.files.length })} />}
              </>
            )}
            <IconButton icon="retry" label={t("review.reload")} onClick={reload} disabled={loading && Boolean(res)} />
          </>
        }
      />
      <ScopeBar mid={mid} cwd={cwd} target={target} res={res} onChange={setTarget} />
      {stale && res && (
        <div className="flex shrink-0 items-center gap-2 border-b border-line bg-accent-tint/50 px-3 py-1.5 text-[12px] text-accent-ink" data-review-stale="">
          <Icon name="info" size={13} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate">{t("review.stale")}</span>
          <button type="button" onClick={reload} className="h-6 shrink-0 rounded-[6px] px-2 font-medium whitespace-nowrap transition-colors hover:bg-hover">
            {t("review.reload")}
          </button>
        </div>
      )}
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
        {target.scope === "commit" && !target.base ? (
          <Empty icon="commit" title={t("review.pick_commit")} detail={t("review.pick_commit_detail")} />
        ) : !res && loading ? (
          <div className="flex h-40 items-center justify-center">
            <Loading label={t("review.reading")} />
          </div>
        ) : failure ? (
          <Failed failure={failure} cwd={cwd} target={target} onRetry={reload} onScope={(s) => setTarget({ scope: s })} />
        ) : res && res.files.length === 0 ? (
          <Clean res={res} onScope={(s) => setTarget({ scope: s })} />
        ) : res ? (
          <ReviewProvider view={view} page={`${session}:${target.scope}:${res.base.hash ?? ""}:${reloads}`} fetch={fetch} nativeCaches={caches.current}>
            <div className={cx("@container flex flex-col gap-3 px-3 pt-3 pb-4 transition-opacity", loading && "opacity-60")} data-review-files="">
              <ReviewToolbar view={view} onHelp={() => setHelp(true)} />
              {!key && <p className="rounded-control bg-field px-3 py-2 text-[12px] text-ink-3">{t("review.read_only")}</p>}
              {res.truncated && <p className="rounded-control bg-orange-tint px-3 py-2 text-[12px] text-orange">{t("review.truncated", { count: res.files.length })}</p>}
              {items.map((it) => (
                // a comment box as wide as the panel's column (its cards are 1.5rem in from it), not the page's
                <DiffView key={it.key} item={it} act={act} busy={busy} fetch={fetch} note={noteOf(fileOf(it.path ?? ""))} room="calc(100cqw - 1.5rem)" askToLoad />
              ))}
              <ReviewBar view={view} act={act} busy={busy} />
            </div>
            {inside && <ReviewKeys onHelp={() => setHelp(true)} />}
            <KeysHelp open={help} onClose={() => setHelp(false)} />
          </ReviewProvider>
        ) : null}
      </div>
    </div>
  );
}

/** What a file shown without its lines says instead of "no changes". */
function noteOf(f: GitReviewFile | undefined): string | undefined {
  if (!f || f.hunks?.length) return undefined;
  if (f.refused) return t("review.refused");
  if (f.huge) return t("review.too_big");
  if (f.status === "added" && !f.added) return t("review.empty_file");
  if (f.status === "deleted" && !f.deleted) return t("review.empty_file");
  if (f.status === "type_changed" || f.status === "modified") return t("review.mode_only");
  return undefined;
}

/** The three scopes, and what the files are compared with: HEAD, where the branch left the default branch, a commit
 *  (picked from the recent ones). */
function ScopeBar({ mid, cwd, target, res, onChange }: { mid: string; cwd: string; target: ReviewTarget; res: GitReview | null; onChange: (t: ReviewTarget) => void }) {
  const menu = useMenu();
  const [commits, setCommits] = useState<GitCommit[] | null>(null);
  const anchor = useRef<HTMLElement | null>(null);
  const openCommits = async (el: HTMLElement | null) => {
    menu.openAt(el);
    if (commits) return;
    try {
      const r = await api<{ commits: GitCommit[] }>(`/machines/${enc(mid)}/git/log?cwd=${enc(cwd)}&limit=30`);
      setCommits(r.commits);
    } catch (e) {
      menu.close();
      toast(errorText(e), "error");
    }
  };
  const base = res && res.scope === target.scope ? res.base : null;
  const against =
    target.scope === "uncommitted"
      ? base?.empty
        ? t("review.against_empty")
        : t("review.against_head", { sha: base?.short ?? "HEAD" })
      : target.scope === "branch"
        ? base?.ref
          ? t("review.against_branch", { ref: base.ref, sha: base.short ?? "" })
          : ""
        : "";
  const items: MenuItem[] = commits
    ? commits.map((c, i) => ({
        key: c.hash,
        label: c.subject || c.short,
        hint: c.short,
        radio: true,
        checked: target.base === c.hash,
        // the newest: nothing since it but what is not committed
        detail: i === 0 ? t("review.newest_commit") : undefined,
        onSelect: () => onChange({ scope: "commit", base: c.hash }),
      }))
    : [{ key: "loading", label: t("review.reading"), disabled: true, onSelect: () => {} }];
  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1.5 border-b border-line px-3 py-2">
      <Segmented<ReviewScope>
        label={t("review.scope")}
        value={target.scope}
        options={[
          ["uncommitted", t("review.uncommitted")],
          ["branch", t("review.branch")],
          ["commit", t("review.since")],
        ]}
        onChange={(s) => {
          if (s === "commit") void openCommits(anchor.current);
          else onChange({ scope: s });
        }}
      />
      {target.scope === "commit" ? (
        <button
          ref={(el) => {
            anchor.current = el;
          }}
          type="button"
          onClick={(e) => void openCommits(e.currentTarget)}
          className="flex h-7 min-w-0 flex-1 items-center gap-1.5 rounded-[8px] px-2 text-left text-[12px] text-ink-2 transition-colors hover:bg-hover"
          data-review-commit=""
        >
          <Icon name="commit" size={13} className="shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1 truncate">{base?.short ? t("review.since_commit", { sha: base.short, subject: base.subject ?? "" }) : t("review.pick_commit")}</span>
          <Icon name="chevron" size={11} stroke={2.2} className="shrink-0 text-ink-3" />
        </button>
      ) : (
        <span
          ref={(el) => {
            anchor.current = el;
          }}
          className="min-w-0 flex-1 truncate text-[11.5px] text-ink-3" title={base?.subject}>
          {against}
        </span>
      )}
      {menu.anchor && <Menu items={items} anchor={menu.anchor} onClose={menu.close} align="right" width={300} header={t("review.since_header")} />}
    </div>
  );
}

function Clean({ res, onScope }: { res: GitReview; onScope: (s: ReviewScope) => void }) {
  const [title, detail] =
    res.scope === "uncommitted"
      ? [t("review.clean_uncommitted"), t("review.clean_uncommitted_detail")]
      : res.scope === "branch"
        ? [t("review.clean_branch", { ref: res.base.ref ?? "" }), t("review.clean_branch_detail")]
        : [t("review.clean_since", { sha: res.base.short ?? "" }), undefined];
  return (
    <Empty
      icon="check"
      title={title}
      detail={detail}
      action={res.scope === "uncommitted" ? <Button icon="branch" onClick={() => onScope("branch")}>{t("review.review_branch")}</Button> : res.scope === "branch" ? <Button onClick={() => onScope("uncommitted")}>{t("review.review_uncommitted")}</Button> : undefined}
    />
  );
}

function Failed({ failure, cwd, target, onRetry, onScope }: { failure: Failure; cwd: string; target: ReviewTarget; onRetry: () => void; onScope: (s: ReviewScope) => void }) {
  switch (failure.code) {
    case "not_git":
      return <Empty icon="git" title={t("review.not_git")} detail={shortPath(cwd)} />;
    case "node_outdated":
      return <Empty icon="warning" title={t("review.outdated")} detail={t("review.outdated_detail")} />;
    case "no_default_branch":
      return <Empty icon="branch" title={t("review.no_default_branch")} detail={t("review.no_default_branch_detail")} action={<Button onClick={() => onScope("uncommitted")}>{t("review.review_uncommitted")}</Button>} />;
    case "no_merge_base":
      return <Empty icon="branch" title={t("review.no_merge_base")} detail={failure.message} action={<Button onClick={() => onScope("uncommitted")}>{t("review.review_uncommitted")}</Button>} />;
    case "bad_base":
      return <Empty icon="commit" title={target.scope === "commit" ? t("review.bad_commit") : t("review.bad_base")} detail={failure.message} action={<Button onClick={() => onScope("uncommitted")}>{t("review.review_uncommitted")}</Button>} />;
  }
  return <Empty icon="info" title={t("review.failed")} detail={failure.message} action={<Button icon="retry" onClick={onRetry}>{t("review.retry")}</Button>} />;
}

