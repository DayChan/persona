// A session's Git, read-only (docs/apple/ios.md "Git", iOS GitPane): branch, upstream ↑↓ and stashes; the changes with
// their diffs (staged / unstaged when a file has both); the history, older pages on demand, each commit's message and
// per-file diffs.
import { useCallback, useEffect, useRef, useState } from "react";
import { api, enc, errorText, isAbort } from "../../lib/api";
import {
  diffTotals,
  dirName,
  fileName,
  gitLetter,
  hasUnstaged,
  isStaged,
  refNames,
  splitDiff,
  syncBadge,
  type GitCommit,
  type GitDiff,
  type GitFile,
  type GitShow,
  type GitStatus,
} from "../../lib/files";
import { ago, shortPath } from "../../lib/format";
import { useStore } from "../../lib/store";
import { openPanel, type GitSpot } from "../../lib/panel";
import { REVIEW_FEATURE } from "../../lib/session-review";
import { useFsLive } from "../../lib/use-fs-live";
import { Button, copyText, cx, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { PanelHeader } from "../../ui/PanelHeader";
import { CompareView, compareKind } from "./files/CompareView";
import { DiffLines } from "./files/DiffLines";
import { BackHeader, Empty, GitBadge, Segmented } from "./files/parts";
import { formatDate, t } from "../../i18n";
import { BranchesView, CommitBox, PushButton, StageAll, StageBox, StashesView, useGitWrite, type GitWriter } from "./GitWrite";
import { PRCard, PRCreateView, PRView, useGitPR } from "./GitPR";

// `at`: a line to show and mark when the view opens (a comment card's click, lib/comments.ts openSpot)
type View =
  | { kind: "diff"; file: GitFile; staged: boolean; at?: GitSpot }
  | { kind: "commit"; commit: GitCommit; at?: GitSpot }
  | { kind: "commitFile"; commit: GitCommit; file: GitFile; diff: string; truncated: boolean; at?: GitSpot }
  | { kind: "branches" }
  | { kind: "stashes" }
  | { kind: "pr" }
  | { kind: "prCreate" };

const PAGE = 50;

export function GitPanel({ mid, sid, cwd, at, onClose }: { mid: string; sid: string; cwd: string; at?: GitSpot; onClose: () => void }) {
  const base = `/machines/${enc(mid)}/git`;
  const [status, setStatus] = useState<GitStatus | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [older, setOlder] = useState<GitCommit[]>([]);
  const [noMore, setNoMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [views, setViews] = useState<View[]>([]);
  const [reloads, setReloads] = useState(0);
  const lastReload = useRef(reloads);
  const head = useRef<string | undefined>(undefined);
  // The status changed on the computer (lib/fs-live.ts): read it again without the panel showing a spinner.
  const [live, setLive] = useState(0);
  // Whether a status is on screen: the status read's failure then cannot replace it, so it is said in a toast.
  const shown = useRef(false);
  useEffect(() => {
    shown.current = status !== null;
  }, [status]);

  useFsLive(mid, cwd, [], true, (c) => c.git && setLive((n) => n + 1));
  // The writes (#6121), on a hub that has them: each answers the status as it is after it.
  const writable = useStore((s) => s.features.includes("git_write"));
  const reviews = useStore((s) => s.features.includes(REVIEW_FEATURE));
  const writer = useGitWrite(base, cwd, (s) => {
    if (head.current !== s.head?.hash) {
      setOlder([]);
      setNoMore(false);
    }
    head.current = s.head?.hash;
    setStatus(s);
  });
  // The branch's pull request (#6121), read through gh on that computer.
  const pr = useGitPR(base, cwd, status, writable);

  useEffect(() => {
    if (!cwd) return;
    const ac = new AbortController();
    const silent = live > 0 && reloads === lastReload.current;
    lastReload.current = reloads;
    if (!silent) setLoading(true);
    api<GitStatus>(`${base}/status?cwd=${enc(cwd)}`, { signal: ac.signal })
      .then((s) => {
        setStatus(s);
        // a silent read keeps the older pages the reader loaded, unless a commit moved the history under them
        if (!silent || head.current !== s.head?.hash) {
          setOlder([]);
          setNoMore(false);
        }
        head.current = s.head?.hash;
        setError("");
      })
      .catch((e) => {
        if (isAbort(e)) return;
        setError(errorText(e));
        // A reader's reload over a status already shown says it failed; a silent fs-live read stays quiet (the next
        // change reads again, and a toast per background hiccup would be noise).
        if (shown.current && !silent) toast(errorText(e), "error");
      })
      .finally(() => !ac.signal.aborted && setLoading(false));
    return () => ac.abort();
  }, [base, cwd, reloads, live]);

  const loadMore = async () => {
    if (!status) return;
    const requestedHead = head.current;
    setLoadingMore(true);
    try {
      const r = await api<{ commits: GitCommit[]; has_more?: boolean }>(`${base}/log?cwd=${enc(cwd)}&skip=${status.commits.length + older.length}&limit=${PAGE}`);
      if (head.current !== requestedHead) return;
      setOlder((o) => [...o, ...r.commits]);
      setNoMore(r.has_more === false || r.commits.length < PAGE);
    } catch (e) {
      // The status is on screen, so the error state would never show: say it, unless HEAD moved under the page.
      if (!isAbort(e) && head.current === requestedHead) toast(errorText(e), "error");
    } finally {
      setLoadingMore(false);
    }
  };

  // opened at a spot: the diff it names, once the status is read (a commit's list is read by its own view)
  const opened = useRef<GitSpot | undefined>(undefined);
  useEffect(() => {
    if (!at || opened.current === at || !status?.is_repo) return;
    opened.current = at;
    if (at.diff.kind === "commit") {
      const sha = at.diff.sha;
      setViews([{ kind: "commit", commit: { hash: sha, short: sha.slice(0, 8), subject: "", author: "" }, at }]);
      return;
    }
    const file = status.files.find((f) => f.path === at.path);
    if (file) setViews([{ kind: "diff", file, staged: at.diff.kind === "staged", at }]);
    else toast(t("chat-comments.jump_code_gone"));
  }, [at, status]);
  const push = (v: View) => setViews((vs) => [...vs, v]);
  const back = () => setViews((vs) => vs.slice(0, -1));
  const top = views[views.length - 1];
  const reload = () => {
    setReloads((n) => n + 1);
    pr.reload();
  };

  return (
    <>
      <div className={cx("flex min-h-0 flex-1 flex-col", top && "hidden")}>
        <PanelHeader
          icon="git"
          title="Git"
          subtitle={status?.is_repo ? [status.branch || t("session-gitpanel.detached_head"), syncBadge(status)].filter(Boolean).join(" ") : shortPath(cwd)}
          onClose={onClose}
          actions={
            <>
              <button
                type="button"
                title={t("session-gitpanel.files")}
                onClick={() => openPanel({ kind: "files", mid, sid, cwd })}
                className="flex h-7 shrink-0 items-center gap-1.5 rounded-[8px] px-2 text-[12px] font-medium whitespace-nowrap text-ink-2 transition-colors hover:bg-hover hover:text-ink"
              >
                <Icon name="folder" size={14} />
                {t("session-gitpanel.files")}
              </button>
              <IconButton icon="retry" label={t("session-gitpanel.reload")} onClick={reload} disabled={loading && Boolean(status)} />
            </>
          }
        />
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
          {status?.is_repo ? (
            <div className="px-3 pt-3 pb-6 sm:px-4" style={{ animation: "fade-in 160ms ease-out both" }}>
              <Summary s={status} writer={writable ? writer : undefined} onBranches={() => push({ kind: "branches" })} onStashes={() => push({ kind: "stashes" })} />
              {writable && <PRCard s={status} pr={pr} onOpen={() => push({ kind: "pr" })} onCreate={() => push({ kind: "prCreate" })} />}
              <Changes s={status} writer={writable ? writer : undefined} onOpen={(f) => push({ kind: "diff", file: f, staged: isStaged(f) && !hasUnstaged(f) })} onReview={reviews ? () => openPanel({ kind: "review", mid, sid, cwd, scope: "uncommitted" }) : undefined} />
              {writable && status.files.length > 0 && <CommitBox s={status} writer={writer} />}
              <Section title={t("session-gitpanel.commits")}>
                {[...status.commits, ...older].map((c) => (
                  <CommitRow key={c.hash} c={c} onOpen={() => push({ kind: "commit", commit: c })} />
                ))}
                {!noMore && status.commits.length >= 30 && (
                  <button type="button" onClick={loadMore} disabled={loadingMore} className="flex h-10 w-full items-center justify-center gap-2 text-[12.5px] font-medium whitespace-nowrap text-accent-ink transition-colors hover:bg-hover disabled:text-ink-3">
                    {loadingMore && <Spinner />}
                    {t("session-gitpanel.load_older_commits")}
                  </button>
                )}
              </Section>
            </div>
          ) : error && !status ? (
            <Empty icon="info" title={t("session-gitpanel.status_failed")} detail={error} action={<Button icon="retry" onClick={reload}>{t("session-gitpanel.retry")}</Button>} />
          ) : status && !status.is_repo ? (
            <Empty icon="git" title={t("session-gitpanel.not_repo")} detail={shortPath(cwd)} />
          ) : (
            <div className="flex h-40 items-center justify-center">
              <Loading label={t("session-gitpanel.reading")} />
            </div>
          )}
        </div>
      </div>
      {top?.kind === "diff" && (
        <DiffView key={`${top.file.path}:${top.at?.line ?? ""}`} mid={mid} sid={sid} root={status?.root ?? cwd} base={base} cwd={cwd} file={top.file} staged={top.staged} at={top.at} refreshed={live} onStaged={(staged) => setViews((vs) => [...vs.slice(0, -1), { ...top, staged, at: undefined }])} onBack={back} onClose={onClose} />
      )}
      {top?.kind === "branches" && status && <BranchesView base={base} cwd={cwd} s={status} writer={writer} onBack={back} onClose={onClose} />}
      {top?.kind === "stashes" && status && <StashesView s={status} writer={writer} onBack={back} onClose={onClose} />}
      {top?.kind === "pr" && status && <PRView s={status} pr={pr} mid={mid} sid={sid} onBack={back} onClose={onClose} />}
      {top?.kind === "prCreate" && status && <PRCreateView s={status} pr={pr} onBack={back} onClose={onClose} onDone={() => setViews((vs) => [...vs.slice(0, -1), { kind: "pr" }])} />}
      {!top && writer.dialog}
      {pr.dialog}
      {top?.kind === "commit" && <CommitView key={top.commit.hash} base={base} cwd={cwd} commit={top.commit} at={top.at} onOpen={push} onBack={back} onClose={onClose} />}
      {top?.kind === "commitFile" && (
        <>
          <BackHeader title={fileName(top.file.path)} subtitle={`${top.commit.short} · ${dirName(top.file.path) || t("session-gitpanel.repo_root")}`} onBack={back} onClose={onClose} actions={<IconButton icon="copy" label={t("session-gitpanel.copy_diff")} onClick={() => copyText(top.diff)} />} />
          {compareKind(top.file.path) ? (
            <CompareView mid={mid} cwd={cwd} root={status?.root ?? cwd} path={top.file.path} before={`${top.commit.hash}^`} after={top.commit.hash} />
          ) : (
            <DiffLines diff={top.diff} truncated={top.truncated} comment={{ mid, sid, cwd, path: top.file.path, diff: { kind: "commit", sha: top.commit.hash }, at: top.at && { line: top.at.line, to: top.at.to, old: top.at.old } }} />
          )}
        </>
      )}
    </>
  );
}

function Section({ title, right, children }: { title: React.ReactNode; right?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="mt-5">
      <div className="mb-1.5 flex items-end gap-2 px-1 text-[12px] font-medium text-ink-3">
        <h3 className="flex-1">{title}</h3>
        {right}
      </div>
      <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">{children}</div>
    </section>
  );
}

function SyncChip({ dir, n }: { dir: "up" | "down"; n: number }) {
  return (
    <span
      title={dir === "up" ? t("session-gitpanel.ahead", { count: n }) : t("session-gitpanel.behind", { count: n })}
      className={cx("inline-flex h-5 shrink-0 items-center gap-0.5 rounded-full px-1.5 text-[11px] font-medium tabular-nums", dir === "up" ? "bg-field text-ink-2" : "bg-orange-tint text-orange")}
    >
      <Icon name="up" size={11} stroke={2.4} style={dir === "down" ? { transform: "rotate(180deg)" } : undefined} />
      {n}
    </span>
  );
}

function Summary({ s, writer, onBranches, onStashes }: { s: GitStatus; writer?: GitWriter; onBranches: () => void; onStashes: () => void }) {
  const ahead = s.ahead ?? 0,
    behind = s.behind ?? 0;
  const branch = s.branch || t("session-gitpanel.detached_head");
  return (
    <div className="rounded-card bg-surface px-4 py-3 shadow-card">
      <div className="flex items-center gap-2">
        <Icon name="git" size={15} className="shrink-0 text-accent" />
        {writer ? (
          <button type="button" onClick={onBranches} title={t("session-gitwrite.branch_hint")} className="-mx-1 flex min-w-0 flex-1 items-center gap-1 rounded-[6px] px-1 text-left hover:bg-hover">
            <span className="min-w-0 truncate text-[14px] font-semibold text-ink">{branch}</span>
            <Icon name="chevron" size={12} stroke={2.4} className="shrink-0 text-ink-3" />
          </button>
        ) : (
          <span className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink">{branch}</span>
        )}
        {ahead > 0 && <SyncChip dir="up" n={ahead} />}
        {behind > 0 && <SyncChip dir="down" n={behind} />}
        {writer && <PushButton s={s} writer={writer} />}
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-1.5 text-[12px] text-ink-3">
        <span className="truncate">{s.upstream ? s.upstream : t("session-gitpanel.no_upstream")}</span>
        {s.upstream && s.gone && <span title={t("session-gitpanel.upstream_gone_hint")}>· {t("session-gitpanel.upstream_gone")}</span>}
        {s.upstream && !s.gone && !ahead && !behind && <span>· {t("session-gitpanel.in_sync")}</span>}
        {writer ? (
          <button type="button" onClick={onStashes} className="whitespace-nowrap hover:text-ink hover:underline" title={t("session-gitpanel.stashes_hint")}>
            · {(s.stashes ?? 0) > 0 ? t("session-gitpanel.stashes", { count: s.stashes ?? 0 }) : t("session-gitwrite.stashes")}
          </button>
        ) : (
          (s.stashes ?? 0) > 0 && <span title={t("session-gitpanel.stashes_hint")}>· {t("session-gitpanel.stashes", { count: s.stashes ?? 0 })}</span>
        )}
      </div>
      {s.root && <div className="mt-1 truncate font-mono text-[11px] text-ink-3" title={s.root}>{shortPath(s.root)}</div>}
    </div>
  );
}

function Changes({ s, writer, onOpen, onReview }: { s: GitStatus; writer?: GitWriter; onOpen: (f: GitFile) => void; onReview?: () => void }) {
  const totals = diffTotals(s.files);
  return (
    <Section
      title={s.files.length ? t("session-gitpanel.changes_count", { count: s.files.length }) : t("session-gitpanel.changes")}
      right={
        s.files.length > 0 && (
          <>
            {onReview && (
              <button type="button" onClick={onReview} data-git-review="" className="-my-1 flex h-6 items-center gap-1 rounded-[6px] px-1.5 text-[12px] font-medium whitespace-nowrap text-accent-ink transition-colors hover:bg-hover">
                <Icon name="pullRequest" size={12} />
                {t("session-gitpanel.review")}
              </button>
            )}
            <span className="font-mono text-[11.5px] tabular-nums">
              <span className="text-green">+{totals.added}</span> <span className="text-red">−{totals.deleted}</span>
            </span>
            {writer && <StageAll s={s} writer={writer} />}
          </>
        )
      }
    >
      {s.files.length === 0 ? (
        <div className="flex h-11 items-center gap-2 px-4 text-[13px] text-ink-3">
          <Icon name="check" size={14} stroke={2.4} className="text-green" />
          {t("session-gitpanel.clean")}
        </div>
      ) : (
        s.files.map((f) => <FileRow key={f.path} f={f} stage check={writer && <StageBox f={f} writer={writer} />} onOpen={() => onOpen(f)} />)
      )}
    </Section>
  );
}

/** A changed file: its letter, name, where it is (and staged / partly staged), +/− lines. */
function FileRow({ f, stage, check, onOpen }: { f: GitFile; stage?: boolean; check?: React.ReactNode; onOpen: () => void }) {
  const folder = dirName(f.path);
  const staged = stage && isStaged(f);
  const row = (
    <button type="button" onClick={onOpen} className={cx("flex w-full items-center gap-3 py-2 text-left transition-colors hover:bg-hover", check ? "pr-4 pl-2" : "px-4")} title={f.path}>
      <GitBadge letter={gitLetter(f)} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] font-medium text-ink">{fileName(f.path)}</span>
        <span className="flex min-w-0 gap-1.5 text-[11.5px] whitespace-nowrap text-ink-3">
          {staged && <span className="shrink-0 text-ink-2">{hasUnstaged(f) ? t("session-gitpanel.partly_staged") : t("session-gitpanel.staged")}</span>}
          {f.orig_path && <span className="shrink-0">← {fileName(f.orig_path)}</span>}
          {folder && <span className="min-w-0 truncate">{folder}</span>}
        </span>
      </span>
      {f.binary ? (
        <span className="shrink-0 text-[11.5px] text-ink-3">{t("session-gitpanel.binary")}</span>
      ) : f.added + f.deleted > 0 ? (
        <span className="shrink-0 font-mono text-[11.5px] tabular-nums">
          <span className="text-green">+{f.added}</span> <span className="text-red">−{f.deleted}</span>
        </span>
      ) : null}
      <Icon name="chevronRight" size={13} stroke={2.2} className="shrink-0 text-ink-3" />
    </button>
  );
  // the stage checkbox (#6121) sits beside the row, not in its button
  return check ? (
    <div className="flex items-center gap-1 pl-4">
      {check}
      <div className="min-w-0 flex-1">{row}</div>
    </div>
  ) : (
    row
  );
}

function CommitRow({ c, onOpen }: { c: GitCommit; onOpen: () => void }) {
  const refs = refNames(c.refs);
  return (
    <button type="button" onClick={onOpen} className="flex w-full flex-col gap-1 px-4 py-2.5 text-left transition-colors hover:bg-hover">
      <span className="line-clamp-2 text-[13px] font-medium text-ink">{c.subject}</span>
      <span className="flex min-w-0 items-center gap-1.5 text-[11.5px] whitespace-nowrap text-ink-3">
        <span className="font-mono text-accent-ink">{c.short}</span>
        <span className="truncate">{c.author}</span>
        {c.date && <span title={new Date(c.date).toLocaleString("zh-CN")}>· {ago(c.date)}</span>}
      </span>
      {refs.length > 0 && (
        <span className="flex flex-wrap gap-1">
          {refs.map((r) => (
            // a local branch outlined, a remote one (origin/…) plain: refs are facts, not buttons
            <span key={r} className={cx("inline-flex h-5 items-center rounded-full px-1.5 text-[11px] whitespace-nowrap", r.includes("/") ? "bg-field text-ink-2" : "text-ink-2 shadow-[inset_0_0_0_1px_var(--line-strong)]")}>
              {r}
            </span>
          ))}
        </span>
      )}
    </button>
  );
}

function DiffView({
  mid,
  sid,
  root,
  base,
  cwd,
  file,
  staged,
  at,
  refreshed,
  onStaged,
  onBack,
  onClose,
}: {
  mid: string;
  sid: string;
  root: string;
  base: string;
  cwd: string;
  file: GitFile;
  staged: boolean;
  at?: GitSpot;
  /** Counts the Git changes seen on the computer: the diff is read again, in place, when it moves. */
  refreshed: number;
  onStaged: (staged: boolean) => void;
  onBack: () => void;
  onClose: () => void;
}) {
  const [diff, setDiff] = useState<GitDiff | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(
    (signal?: AbortSignal, silent = false) => {
      if (!silent) setDiff(null);
      setError("");
      if (compareKind(file.path)) return; // compared as versions, not as text
      api<GitDiff>(`${base}/diff?cwd=${enc(cwd)}&path=${enc(file.path)}&staged=${staged ? 1 : 0}`, { signal })
        .then(setDiff)
        .catch((e) => !isAbort(e) && setError(errorText(e)));
    },
    [base, cwd, file.path, staged],
  );
  useEffect(() => {
    const ac = new AbortController();
    load(ac.signal);
    return () => ac.abort();
  }, [load]);
  const seen = useRef(refreshed);
  useEffect(() => {
    if (seen.current === refreshed) return;
    seen.current = refreshed;
    const ac = new AbortController();
    load(ac.signal, true);
    return () => ac.abort();
  }, [refreshed, load]);
  const both = isStaged(file) && hasUnstaged(file);
  return (
    <>
      <BackHeader
        title={fileName(file.path)}
        subtitle={dirName(file.path) || t("session-gitpanel.repo_root")}
        onBack={onBack}
        onClose={onClose}
        actions={<IconButton icon="copy" label={t("session-gitpanel.copy_diff")} onClick={() => diff && copyText(diff.diff)} disabled={!diff} />}
      />
      {both && (
        <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-2">
          <Segmented<"0" | "1"> label={t("session-gitpanel.which_changes")} value={staged ? "1" : "0"} onChange={(v) => onStaged(v === "1")} options={[["0", t("session-gitpanel.unstaged")], ["1", t("session-gitpanel.staged")]]} />
        </div>
      )}
      {compareKind(file.path) ? (
        // documents and pictures: the two versions themselves (unstaged: index → disk; staged: HEAD → index)
        <CompareView key={staged ? "s" : "w"} mid={mid} cwd={cwd} root={root} path={file.path} before={staged ? "HEAD" : "INDEX"} after={staged ? "INDEX" : "WORKTREE"} deleted={!staged && file.worktree === "D"} />
      ) : diff ? (
        <DiffLines diff={diff.diff} truncated={diff.truncated} comment={{ mid, sid, cwd, path: file.path, diff: { kind: staged ? "staged" : "unstaged" }, at: at && { line: at.line, to: at.to, old: at.old } }} />
      ) : error ? (
        <Empty icon="info" title={t("session-gitpanel.diff_failed")} detail={error} action={<Button icon="retry" onClick={() => load()}>{t("session-gitpanel.retry")}</Button>} />
      ) : (
        <div className="flex flex-1 items-center justify-center">
          <Loading label={t("session-gitpanel.reading")} />
        </div>
      )}
    </>
  );
}

function CommitView({ base, cwd, commit, at, onOpen, onBack, onClose }: { base: string; cwd: string; commit: GitCommit; at?: GitSpot; onOpen: (v: View) => void; onBack: () => void; onClose: () => void }) {
  const [show, setShow] = useState<GitShow | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const ac = new AbortController();
    api<GitShow>(`${base}/show?cwd=${enc(cwd)}&hash=${enc(commit.hash)}`, { signal: ac.signal })
      .then(setShow)
      .catch((e) => !isAbort(e) && setError(errorText(e)));
    return () => ac.abort();
  }, [base, cwd, commit.hash]);
  const chunks = show ? splitDiff(show.diff) : {};
  // opened at a spot in this commit: on to the file's diff as soon as the commit is read
  const went = useRef(false);
  useEffect(() => {
    if (!at || !show || went.current) return;
    went.current = true;
    const f = show.files.find((x) => x.path === at.path);
    if (!f) {
      toast(t("chat-comments.jump_code_gone"));
      return;
    }
    const d = splitDiff(show.diff)[f.path];
    onOpen({ kind: "commitFile", commit: show.commit, file: f, diff: d ?? "", truncated: show.truncated && d === undefined, at });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [show]);
  const body = show?.commit.body ?? commit.body;
  const when = commit.date ? new Date(commit.date) : null;
  return (
    <>
      <BackHeader title={commit.short} subtitle={commit.subject} onBack={onBack} onClose={onClose} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-3 pb-6 sm:px-4">
        <div className="rounded-card bg-surface px-4 py-3 shadow-card">
          <h3 className="text-[14px] leading-snug font-semibold text-ink">{commit.subject}</h3>
          {body && <p className="mt-1.5 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2 select-text">{body}</p>}
          <div className="mt-2 text-[11.5px] text-ink-3">
            {commit.author}
            {when && !Number.isNaN(when.getTime()) && ` · ${formatDate(when, { dateStyle: "medium", timeStyle: "short" })}`}
          </div>
          <button type="button" onClick={() => copyText(commit.hash)} title={t("session-gitpanel.copy_hash")} className="mt-1.5 -ml-1 flex max-w-full items-center gap-1.5 rounded-[6px] px-1 py-0.5 font-mono text-[11px] text-accent-ink hover:bg-hover">
            <Icon name="copy" size={12} className="shrink-0" />
            <span className="truncate">{commit.hash}</span>
          </button>
        </div>
        {show ? (
          <Section title={t("session-gitpanel.file_count", { count: show.files.length })}>
            {show.files.map((f) => {
              const d = chunks[f.path];
              return <FileRow key={f.path} f={f} onOpen={() => onOpen({ kind: "commitFile", commit, file: f, diff: d ?? "", truncated: show.truncated && d === undefined })} />;
            })}
          </Section>
        ) : error ? (
          <p className="mt-4 px-1 text-[12.5px] text-red">{error}</p>
        ) : (
          <div className="flex h-24 items-center justify-center">
            <Loading label={t("session-gitpanel.reading")} />
          </div>
        )}
      </div>
    </>
  );
}
