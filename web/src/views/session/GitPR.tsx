// The current branch's pull request in the Git panel (#6121): a card under the summary, a page with its checks, its
// open review threads (each handed to the agent as a comment for the next message) and merging, and a form to open
// one. Opening, merging and setting it to merge when ready all leave the computer, so each is confirmed first; a
// refusal says what happened in gh's own words. The branch is never deleted.
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, isAbort } from "../../lib/api";
import { addCodeComment, commentKey } from "../../lib/comment-target";
import { addQuote } from "../../lib/drafts";
import type { GitStatus } from "../../lib/files";
import { ago } from "../../lib/format";
import { checkCounts, gitPR, gitPRCreate, gitPRMerge, mergeReady, threadQuote, threadWhere, type GitCheck, type GitPR, type GitPRAnswer, type GitPRThread, type MergeMethod } from "../../lib/git-pr";
import { gitRefusal, type GitRefusal } from "../../lib/git-write";
import { openLink } from "../../lib/links";
import { Button, cx, Dialog, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { BackHeader, Empty, Segmented } from "./files/parts";
import { RefusalDialog } from "./GitWrite";
import { t } from "../../i18n";

/** The branch's pull request, read when the panel opens and again when what was pushed changes; and its writes. */
export function useGitPR(base: string, cwd: string, s: GitStatus | null, on: boolean) {
  const [answer, setAnswer] = useState<GitPRAnswer | null>(null);
  const [failed, setFailed] = useState<{ code: string; detail: string } | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [refusal, setRefusal] = useState<GitRefusal | null>(null);
  const [reads, setReads] = useState(0);
  // gh asks GitHub each time: read again only when the branch, its upstream or what is on it moved
  const pushed = s?.is_repo && s.branch ? `${s.branch}|${s.upstream ?? ""}|${s.gone ? "gone" : ""}|${s.ahead ?? 0}|${s.head?.hash ?? ""}` : "";
  useEffect(() => {
    if (!on || !pushed) return;
    const ac = new AbortController();
    setLoading(true);
    gitPR(base, cwd, ac.signal)
      .then((a) => {
        setAnswer(a);
        setFailed(null);
      })
      .catch((e) => {
        if (isAbort(e)) return;
        setFailed({ code: e instanceof ApiError ? (e.code ?? "") : "", detail: gitRefusal(e).detail });
      })
      .finally(() => !ac.signal.aborted && setLoading(false));
    return () => ac.abort();
  }, [base, cwd, on, pushed, reads]);
  const reload = useCallback(() => setReads((n) => n + 1), []);
  const run = async (what: string, call: () => Promise<GitPRAnswer>, done?: string) => {
    if (busy) return null;
    setBusy(what);
    try {
      const a = await call();
      setAnswer(a);
      setFailed(null);
      if (done) toast(done, "ok");
      return a;
    } catch (e) {
      setRefusal(gitRefusal(e));
      return null;
    } finally {
      setBusy(null);
    }
  };
  const create = (body: { title: string; body: string; base?: string; draft?: boolean }) =>
    run("create", () => gitPRCreate(base, { cwd, ...body }), t("session-gitpr.created"));
  const merge = (pr: GitPR, how: { method?: MergeMethod; auto?: boolean; auto_off?: boolean }, done: string) =>
    run(how.auto_off ? "auto_off" : how.auto ? "auto" : "merge", () => gitPRMerge(base, { cwd, number: pr.number, ...how }), done);
  const dialog = <RefusalDialog refusal={refusal} onClose={() => setRefusal(null)} />;
  return { answer: on && pushed ? answer : null, failed: on && pushed ? failed : null, loading, busy, reload, create, merge, dialog };
}

export type GitPRState = ReturnType<typeof useGitPR>;

// what kept gh from answering that is not worth a card: no GitHub remote, no branch
const QUIET = new Set(["git_not_github", "git_detached", "git_not_repo"]);

/** Whether the branch can have a pull request opened: on a branch, pushed (and still there), nothing left to push. */
function pushedAll(s: GitStatus): boolean {
  return Boolean(s.branch && s.upstream && !s.gone && (s.ahead ?? 0) === 0);
}

/** The card under the summary: the pull request in a line (state, checks, open threads), or a way to open one. */
export function PRCard({ s, pr, onOpen, onCreate }: { s: GitStatus; pr: GitPRState; onOpen: () => void; onCreate: () => void }) {
  if (pr.failed) {
    if (QUIET.has(pr.failed.code)) return null;
    const hint = pr.failed.code === "no_gh" ? t("session-gitpr.no_gh") : pr.failed.code === "not_signed_in" ? t("session-gitpr.not_signed_in") : t("session-gitpr.read_failed");
    return (
      <div className="mt-2 flex items-center gap-2 rounded-card bg-surface px-4 py-2.5 shadow-card">
        <Icon name="pullRequest" size={14} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-3" title={pr.failed.detail}>
          {hint}
        </span>
        <IconButton icon="retry" label={t("session-gitpr.reload")} onClick={pr.reload} disabled={pr.loading} />
      </div>
    );
  }
  const a = pr.answer;
  if (!a) return null;
  if (!a.pr) {
    const can = pushedAll(s);
    return (
      <div className="mt-2 flex items-center gap-2 rounded-card bg-surface px-4 py-2.5 shadow-card">
        <Icon name="pullRequest" size={14} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-3">{can ? t("session-gitpr.none") : t("session-gitpr.push_first")}</span>
        <Button tone="secondary" icon="pullRequest" disabled={!can || pr.busy !== null} onClick={onCreate}>
          {t("session-gitpr.create")}
        </Button>
      </div>
    );
  }
  const p = a.pr;
  return (
    <button type="button" onClick={onOpen} className="mt-2 flex w-full items-center gap-3 rounded-card bg-surface px-4 py-2.5 text-left shadow-card transition-colors hover:bg-hover">
      <Icon name={p.state === "MERGED" ? "merge" : "pullRequest"} size={15} className={cx("shrink-0", stateTone(p))} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] font-medium text-ink">
          <span className="text-ink-3">#{p.number}</span> {p.title}
        </span>
        <span className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-3">
          <StateChip pr={p} />
          {p.checks.length > 0 && <ChecksLine checks={p.checks} />}
          {p.threads.length > 0 && <span className="truncate whitespace-nowrap text-orange">· {t("session-gitpr.threads_open", { count: p.threads.length })}</span>}
        </span>
      </span>
      {pr.loading ? <Spinner /> : <Icon name="chevronRight" size={13} stroke={2.2} className="shrink-0 text-ink-3" />}
    </button>
  );
}

function stateTone(p: GitPR): string {
  if (p.state === "MERGED") return "text-accent";
  if (p.state === "CLOSED") return "text-red";
  return p.draft ? "text-ink-3" : "text-green";
}

/** gh's review decision in words (APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED); one it adds later as it is. */
function reviewText(review: string): string {
  const key = `session-gitpr.review_${review.toLowerCase()}`;
  const said = t(key);
  return said === key ? review : said;
}

function StateChip({ pr }: { pr: GitPR }) {
  const key = pr.state === "MERGED" ? "merged" : pr.state === "CLOSED" ? "closed" : pr.draft ? "draft" : "open";
  const tone = { merged: "bg-accent-tint text-accent-ink", closed: "bg-red-tint text-red", draft: "bg-field text-ink-2", open: "bg-green-tint text-green" }[key];
  return <span className={cx("inline-flex h-[18px] shrink-0 items-center rounded-full px-1.5 text-[11px] font-medium whitespace-nowrap", tone)}>{t(`session-gitpr.state_${key}`)}</span>;
}

/** `✓ 3 · ✗ 1 · 2 running`: the checks in a few words. */
function ChecksLine({ checks }: { checks: readonly GitCheck[] }) {
  const n = checkCounts(checks);
  const parts: { icon: IconName; tone: string; text: string }[] = [];
  if (n.fail) parts.push({ icon: "x", tone: "text-red", text: t("session-gitpr.checks_failed", { count: n.fail }) });
  if (n.pending) parts.push({ icon: "clock", tone: "text-orange", text: t("session-gitpr.checks_pending", { count: n.pending }) });
  if (n.pass) parts.push({ icon: "check", tone: "text-green", text: t("session-gitpr.checks_passed", { count: n.pass }) });
  return (
    <>
      {parts.map((p) => (
        <span key={p.icon} className={cx("inline-flex shrink-0 items-center gap-0.5 whitespace-nowrap", p.tone)}>
          <Icon name={p.icon} size={11} stroke={2.6} />
          {p.text}
        </span>
      ))}
    </>
  );
}

const CHECK: Record<GitCheck["state"], { icon: IconName; tone: string }> = {
  pass: { icon: "check", tone: "text-green" },
  fail: { icon: "x", tone: "text-red" },
  pending: { icon: "clock", tone: "text-orange" },
  skipped: { icon: "arrowRight", tone: "text-ink-3" },
};

/** The pull request's page: what it is, its checks, its open review threads, merging. */
export function PRView({ s, pr, mid, sid, onBack, onClose }: { s: GitStatus; pr: GitPRState; mid: string; sid: string; onBack: () => void; onClose: () => void }) {
  const p = pr.answer?.pr;
  const [handed, setHanded] = useState<Set<number>>(new Set());
  const key = commentKey(mid, sid);
  const hand = (list: GitPRThread[], at: number[]) => {
    if (!p || !key) return;
    if (list.length === 1) {
      const q = threadQuote(p, list[0]!);
      addCodeComment(key, q, q.comment!.body);
    } else {
      for (const th of list) addQuote(key, threadQuote(p, th), false);
      toast(t("session-gitpr.handed_all", { count: list.length }), "ok");
    }
    setHanded((h) => new Set([...h, ...at]));
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <BackHeader
        title={p ? `#${p.number}` : t("session-gitpr.title")}
        subtitle={p ? `${p.head} → ${p.base}` : s.branch}
        onBack={onBack}
        onClose={onClose}
        actions={
          <>
            {p && <IconButton icon="external" label={t("session-gitpr.open_github")} onClick={() => openLink(p.url)} />}
            <IconButton icon="retry" label={t("session-gitpr.reload")} onClick={pr.reload} disabled={pr.loading} />
          </>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-3 pb-6 sm:px-4">
        {!p ? (
          pr.loading ? (
            <div className="flex h-32 items-center justify-center">
              <Loading />
            </div>
          ) : (
            <Empty icon="pullRequest" title={t("session-gitpr.gone")} />
          )
        ) : (
          <>
            <div className="rounded-card bg-surface px-4 py-3 shadow-card">
              <div className="text-[14px] leading-[1.45] font-semibold text-ink">{p.title}</div>
              <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-[12px] text-ink-3">
                <StateChip pr={p} />
                {p.review && <span className="whitespace-nowrap">· {reviewText(p.review)}</span>}
                {p.auto_merge && p.state === "OPEN" && <span className="whitespace-nowrap text-accent-ink">· {t("session-gitpr.auto_on")}</span>}
              </div>
            </div>
            {p.checks.length > 0 && (
              <PRSection title={t("session-gitpr.checks")} right={<ChecksLine checks={p.checks} />}>
                {p.checks.map((c) => (
                  <button key={c.name} type="button" disabled={!c.url} onClick={() => c.url && openLink(c.url)} className="flex w-full items-center gap-3 px-4 py-2 text-left transition-colors enabled:hover:bg-hover">
                    <Icon name={CHECK[c.state].icon} size={13} stroke={2.6} className={cx("shrink-0", CHECK[c.state].tone)} />
                    <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{c.name}</span>
                    <span className="shrink-0 text-[11.5px] text-ink-3">{t(`session-gitpr.check_${c.state}`)}</span>
                  </button>
                ))}
              </PRSection>
            )}
            <PRSection
              title={t("session-gitpr.threads", { count: p.threads.length })}
              right={
                p.threads.length > 1 && key ? (
                  <button type="button" onClick={() => hand(p.threads, p.threads.map((_, i) => i))} className="text-[12px] font-medium whitespace-nowrap text-accent-ink hover:underline">
                    {t("session-gitpr.hand_all")}
                  </button>
                ) : undefined
              }
            >
              {p.threads.length === 0 ? (
                <div className="px-4 py-3 text-[13px] text-ink-3">{t("session-gitpr.no_threads")}</div>
              ) : (
                p.threads.map((th, i) => <ThreadRow key={`${th.path}:${th.line ?? 0}:${i}`} th={th} canHand={key !== null} handed={handed.has(i)} onHand={() => hand([th], [i])} />)
              )}
            </PRSection>
            {p.state === "OPEN" && <MergeBox pr={p} state={pr} />}
          </>
        )}
      </div>
    </div>
  );
}

function PRSection({ title, right, children }: { title: React.ReactNode; right?: React.ReactNode; children: React.ReactNode }) {
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

/** One open review thread: where, the comments (long ones folded), and Hand to the agent. */
function ThreadRow({ th, canHand, handed, onHand }: { th: GitPRThread; canHand: boolean; handed: boolean; onHand: () => void }) {
  const [open, setOpen] = useState(false);
  const body = useRef<HTMLDivElement>(null);
  const [long, setLong] = useState(false);
  useEffect(() => {
    const el = body.current;
    if (el) setLong(el.scrollHeight > el.clientHeight + 4);
  }, [th]);
  return (
    <div className="px-4 py-3">
      <div className="flex items-center gap-2">
        <Icon name="file" size={13} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink-2" title={threadWhere(th)}>
          {threadWhere(th)}
        </span>
        {th.outdated && <span className="shrink-0 rounded-full bg-field px-1.5 text-[11px] text-ink-3">{t("session-gitpr.outdated")}</span>}
      </div>
      <div ref={body} className={cx("mt-2 space-y-2", !open && "max-h-[8.5em] overflow-hidden")}>
        {th.comments.map((c, i) => (
          <div key={i} className="text-[12.5px] text-ink-2">
            <div className="text-[11.5px] text-ink-3">
              <span className="font-medium text-ink-2">@{c.author}</span> · {ago(c.at)}
            </div>
            <Markdown text={c.body} small compact />
          </div>
        ))}
      </div>
      <div className="mt-2 flex items-center gap-2">
        {(long || open) && (
          <button type="button" onClick={() => setOpen((o) => !o)} className="text-[12px] whitespace-nowrap text-ink-3 hover:text-ink">
            {open ? t("session-gitpr.fold") : t("session-gitpr.unfold")}
          </button>
        )}
        <span className="flex-1" />
        {canHand && (
          <Button tone={handed ? "quiet" : "secondary"} icon={handed ? "check" : "agent"} onClick={onHand} title={t("session-gitpr.hand_hint")}>
            {handed ? t("session-gitpr.handed") : t("session-gitpr.hand")}
          </Button>
        )}
      </div>
    </div>
  );
}

/** Merge now, or set it to merge once its checks and reviews allow (and take that back); each confirmed first. */
function MergeBox({ pr, state }: { pr: GitPR; state: GitPRState }) {
  const [method, setMethod] = useState<MergeMethod>("squash");
  const [asking, setAsking] = useState<"merge" | "auto" | "auto_off" | null>(null);
  const ready = mergeReady(pr);
  const how = t(`session-gitpr.method_${method}`);
  const go = async () => {
    const what = asking;
    setAsking(null);
    if (what === "merge") await state.merge(pr, { method }, t("session-gitpr.merged", { number: pr.number }));
    else if (what === "auto") await state.merge(pr, { method, auto: true }, t("session-gitpr.auto_set"));
    else if (what === "auto_off") await state.merge(pr, { auto_off: true }, t("session-gitpr.auto_cleared"));
  };
  return (
    <PRSection title={t("session-gitpr.merge_section")}>
      <div className="p-3">
        {ready === "no" ? (
          <div className="text-[12.5px] text-ink-3">{t("session-gitpr.draft_no_merge")}</div>
        ) : (
          <>
            <div className="flex items-center gap-2">
              <span className="min-w-0 flex-1 truncate text-[12px] text-ink-3">{t("session-gitpr.method")}</span>
              <Segmented
                value={method}
                onChange={setMethod}
                label={t("session-gitpr.method")}
                options={[
                  ["squash", t("session-gitpr.method_squash")],
                  ["merge", t("session-gitpr.method_merge")],
                  ["rebase", t("session-gitpr.method_rebase")],
                ]}
              />
            </div>
            <div className="mt-2 text-[12px] leading-[1.5] text-ink-3">
              {ready === "conflict" ? t("session-gitpr.conflict", { base: pr.base }) : ready === "later" ? (pr.auto_merge ? t("session-gitpr.auto_waiting") : t("session-gitpr.not_ready")) : t("session-gitpr.ready", { base: pr.base })}
            </div>
            <div className="mt-3 flex flex-wrap justify-end gap-2">
              {pr.auto_merge ? (
                <Button tone="secondary" busy={state.busy === "auto_off"} disabled={state.busy !== null} onClick={() => setAsking("auto_off")}>
                  {t("session-gitpr.auto_off")}
                </Button>
              ) : (
                ready !== "now" && (
                  <Button tone="secondary" icon="clock" busy={state.busy === "auto"} disabled={state.busy !== null} onClick={() => setAsking("auto")}>
                    {t("session-gitpr.auto")}
                  </Button>
                )
              )}
              {ready === "now" && (
                <Button tone="primary" icon="merge" busy={state.busy === "merge"} disabled={state.busy !== null} onClick={() => setAsking("merge")}>
                  {t("session-gitpr.merge")}
                </Button>
              )}
            </div>
          </>
        )}
      </div>
      <Dialog open={asking !== null} onClose={() => setAsking(null)} title={asking === "auto_off" ? t("session-gitpr.auto_off_title") : asking === "auto" ? t("session-gitpr.auto_title") : t("session-gitpr.merge_title", { number: pr.number })}>
        <div className="py-1 text-[13.5px] leading-[1.6] text-ink-2">
          {asking === "auto_off" ? t("session-gitpr.auto_off_confirm") : asking === "auto" ? t("session-gitpr.auto_confirm", { base: pr.base, method: how }) : t("session-gitpr.merge_confirm", { number: pr.number, base: pr.base, method: how })}
        </div>
        {asking !== "auto_off" && <div className="mt-1 text-[12px] text-ink-3">{t("session-gitpr.keeps_branch", { branch: pr.head })}</div>}
        <div className="mt-4 flex justify-end gap-2">
          <Button tone="secondary" onClick={() => setAsking(null)}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" icon={asking === "auto_off" ? undefined : asking === "auto" ? "clock" : "merge"} onClick={() => void go()}>
            {asking === "auto_off" ? t("session-gitpr.auto_off") : asking === "auto" ? t("session-gitpr.auto") : t("session-gitpr.merge")}
          </Button>
        </div>
      </Dialog>
    </PRSection>
  );
}

/** Opening a pull request for the branch: title (its last commit's subject to start), description, base, draft. */
export function PRCreateView({ s, pr, onBack, onClose, onDone }: { s: GitStatus; pr: GitPRState; onBack: () => void; onClose: () => void; onDone: () => void }) {
  const [title, setTitle] = useState(s.head?.subject ?? "");
  const [body, setBody] = useState("");
  const [baseBranch, setBaseBranch] = useState("");
  const [draft, setDraft] = useState(false);
  const [asking, setAsking] = useState(false);
  const can = title.trim() !== "" && pushedAll(s) && pr.busy === null;
  const go = async () => {
    setAsking(false);
    const a = await pr.create({ title: title.trim(), body, ...(baseBranch.trim() ? { base: baseBranch.trim() } : {}), draft });
    if (a?.pr) onDone();
  };
  const field = "block w-full rounded-control bg-field px-3 text-[13px] text-ink outline-none placeholder:text-ink-3 focus:ring-2 focus:ring-accent/40";
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <BackHeader title={t("session-gitpr.create_title")} subtitle={s.branch} onBack={onBack} onClose={onClose} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-3 pb-6 sm:px-4">
        <div className="space-y-2 rounded-card bg-surface p-3 shadow-card">
          <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder={t("session-gitpr.pr_title")} aria-label={t("session-gitpr.pr_title")} className={cx(field, "h-9")} />
          <textarea value={body} onChange={(e) => setBody(e.target.value)} rows={6} placeholder={t("session-gitpr.pr_body")} aria-label={t("session-gitpr.pr_body")} className={cx(field, "resize-y py-2")} />
          <div className="flex items-center gap-2">
            <span className="shrink-0 text-[12px] text-ink-3">{t("session-gitpr.base")}</span>
            <input value={baseBranch} onChange={(e) => setBaseBranch(e.target.value)} placeholder={t("session-gitpr.base_default")} aria-label={t("session-gitpr.base")} className={cx(field, "h-8 min-w-0 flex-1 font-mono text-[12px]")} />
          </div>
          <div className="flex items-center gap-2 pt-1">
            <label className="flex min-w-0 flex-1 items-center gap-2 text-[12px] text-ink-2">
              <input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} className="accent-(--color-accent)" />
              <span className="truncate">{t("session-gitpr.draft")}</span>
            </label>
            <Button tone="primary" icon="pullRequest" busy={pr.busy === "create"} disabled={!can} onClick={() => setAsking(true)}>
              {t("session-gitpr.create")}
            </Button>
          </div>
        </div>
        {!pushedAll(s) && <div className="mt-2 px-1 text-[12px] text-ink-3">{t("session-gitpr.push_first")}</div>}
      </div>
      <Dialog open={asking} onClose={() => setAsking(false)} title={t("session-gitpr.create_confirm_title")}>
        <div className="py-1 text-[13.5px] leading-[1.6] text-ink-2">{t(draft ? "session-gitpr.create_confirm_draft" : "session-gitpr.create_confirm", { branch: s.branch ?? "", base: baseBranch.trim() || t("session-gitpr.base_default") })}</div>
        <div className="mt-4 flex justify-end gap-2">
          <Button tone="secondary" onClick={() => setAsking(false)}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" icon="pullRequest" onClick={() => void go()}>
            {t("session-gitpr.create")}
          </Button>
        </div>
      </Dialog>
    </div>
  );
}
