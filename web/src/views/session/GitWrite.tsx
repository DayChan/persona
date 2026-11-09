// The Git panel's writes (#6121): stage a file, commit what is staged, push, switch or make a branch, stash. Each is
// a click; the ones that leave the computer (push) or may lose work (dropping a stash) are confirmed first, and a
// refusal says what happened, git's own words and the files it is about. The node never forces anything.
import { useEffect, useMemo, useRef, useState } from "react";
import { type GitFile, type GitStatus } from "../../lib/files";
import { gitBranches, gitRefusal, hasStashIdentity, gitWrite, stageState, type GitBranch, type GitRefusal, type GitStashEntry, type GitWriteOp } from "../../lib/git-write";
import { ago } from "../../lib/format";
import { Button, cx, Dialog, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { SearchField } from "../../ui/SearchField";
import { BackHeader, Empty } from "./files/parts";
import { t } from "../../i18n";

/** Runs one write at a time for the panel: what is running, and the refusal of the last one until it is read. */
export function useGitWrite(base: string, cwd: string, onStatus: (s: GitStatus) => void) {
  const [busy, setBusy] = useState<string | null>(null);
  const [refusal, setRefusal] = useState<GitRefusal | null>(null);
  const run = async (what: string, op: GitWriteOp, body: Record<string, unknown>, done?: string) => {
    if (busy) return null;
    setBusy(what);
    try {
      const r = await gitWrite(base, op, { cwd, ...body });
      if (r.status) onStatus(r.status);
      if (done) toast(done, "ok");
      return r;
    } catch (e) {
      setRefusal(gitRefusal(e));
      return null;
    } finally {
      setBusy(null);
    }
  };
  const dialog = <RefusalDialog refusal={refusal} onClose={() => setRefusal(null)} />;
  return { busy, run, dialog };
}

export type GitWriter = ReturnType<typeof useGitWrite>;

export function RefusalDialog({ refusal, onClose }: { refusal: GitRefusal | null; onClose: () => void }) {
  return (
    <Dialog open={refusal !== null} onClose={onClose} title={refusal?.title}>
      {refusal && (
        <>
          {refusal.files.length > 0 && (
            <ul className="mb-3 max-h-40 overflow-y-auto rounded-control bg-field px-3 py-2 font-mono text-[12px] text-ink-2">
              {refusal.files.map((f) => (
                <li key={f} className="truncate" title={f}>
                  {f}
                </li>
              ))}
            </ul>
          )}
          <pre className="max-h-60 overflow-auto rounded-control bg-field px-3 py-2 font-mono text-[11.5px] leading-[1.5] whitespace-pre-wrap text-ink-3">{refusal.detail}</pre>
          <div className="mt-4 flex justify-end">
            <Button tone="primary" onClick={onClose}>
              {t("session-gitwrite.ok")}
            </Button>
          </div>
        </>
      )}
    </Dialog>
  );
}

/** A file's stage checkbox: on when all of it is staged, a dash when part of it is. */
export function StageBox({ f, writer }: { f: GitFile; writer: GitWriter }) {
  const state = stageState(f);
  const label = state === "all" ? t("session-gitwrite.unstage_file", { name: f.path }) : t("session-gitwrite.stage_file", { name: f.path });
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={state === "all" ? true : state === "some" ? "mixed" : false}
      aria-label={label}
      title={label}
      disabled={writer.busy !== null}
      onClick={(e) => {
        e.stopPropagation();
        void writer.run(`stage:${f.path}`, "stage", { paths: [f.path], unstage: state === "all" });
      }}
      className={cx(
        "flex size-[18px] shrink-0 items-center justify-center rounded-[5px] border transition-colors disabled:opacity-50",
        state === "none" ? "border-ink-3 bg-surface hover:border-accent" : "border-accent bg-accent text-white",
      )}
    >
      {writer.busy === `stage:${f.path}` ? <Spinner size={10} /> : state === "all" ? <Icon name="check" size={12} stroke={3} /> : state === "some" ? <span className="h-[2px] w-2 rounded-full bg-white" /> : null}
    </button>
  );
}

/** Stage or unstage every change at once (the Changes header). */
export function StageAll({ s, writer }: { s: GitStatus; writer: GitWriter }) {
  const all = s.files.every((f) => stageState(f) === "all");
  return (
    <button
      type="button"
      disabled={writer.busy !== null}
      onClick={() => void writer.run("stage:all", "stage", { paths: s.files.map((f) => f.path), unstage: all })}
      className="text-[12px] font-medium whitespace-nowrap text-accent-ink hover:underline disabled:text-ink-3"
    >
      {all ? t("session-gitwrite.unstage_all") : t("session-gitwrite.stage_all")}
    </button>
  );
}

/** The message and Commit under the changes: what is staged goes in, nothing else. */
export function CommitBox({ s, writer }: { s: GitStatus; writer: GitWriter }) {
  const [message, setMessage] = useState("");
  const staged = s.files.filter((f) => stageState(f) !== "none").length;
  const can = staged > 0 && message.trim() !== "" && Boolean(s.branch) && writer.busy === null;
  const commit = async () => {
    const r = await writer.run("commit", "commit", { message }, undefined);
    if (r?.commit) {
      setMessage("");
      toast(t("session-gitwrite.committed", { short: r.commit.short }), "ok");
    }
  };
  return (
    <div className="mt-2 rounded-card bg-surface p-3 shadow-card">
      <textarea
        value={message}
        onChange={(e) => setMessage(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing && can) void commit();
        }}
        rows={2}
        placeholder={t("session-gitwrite.message_placeholder")}
        aria-label={t("session-gitwrite.message_label")}
        className="block w-full resize-y rounded-control bg-field px-3 py-2 text-[13px] text-ink outline-none placeholder:text-ink-3 focus:ring-2 focus:ring-accent/40"
      />
      <div className="mt-2 flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-[12px] text-ink-3">
          {!s.branch ? t("session-gitwrite.detached_no_commit") : staged ? t("session-gitwrite.staged_count", { count: staged }) : t("session-gitwrite.nothing_staged")}
        </span>
        <Button tone="primary" icon="commit" busy={writer.busy === "commit"} disabled={!can} onClick={() => void commit()}>
          {t("session-gitwrite.commit")}
        </Button>
      </div>
    </div>
  );
}

/** Push in the summary: only with commits to send or a branch the remote does not have; confirmed first. */
export function PushButton({ s, writer }: { s: GitStatus; writer: GitWriter }) {
  const [asking, setAsking] = useState(false);
  const ahead = s.ahead ?? 0;
  if (!s.branch || (s.upstream && ahead === 0)) return null;
  const fresh = !s.upstream;
  const push = async () => {
    setAsking(false);
    await writer.run("push", "push", { set_upstream: fresh }, t("session-gitwrite.pushed", { branch: s.branch ?? "" }));
  };
  return (
    <>
      <Button tone="secondary" icon="upload" busy={writer.busy === "push"} disabled={writer.busy !== null} onClick={() => setAsking(true)}>
        {t("session-gitwrite.push")}
      </Button>
      <Dialog open={asking} onClose={() => setAsking(false)} title={t("session-gitwrite.push_title")}>
        <div className="py-1 text-[13.5px] leading-[1.6] text-ink-2">
          {fresh ? t("session-gitwrite.push_new", { branch: s.branch }) : t("session-gitwrite.push_confirm", { count: ahead, upstream: s.upstream ?? "" })}
        </div>
        <div className="mt-1 text-[12px] text-ink-3">{t("session-gitwrite.push_never_force")}</div>
        <div className="mt-4 flex justify-end gap-2">
          <Button tone="secondary" onClick={() => setAsking(false)}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" icon="upload" onClick={() => void push()}>
            {t("session-gitwrite.push")}
          </Button>
        </div>
      </Dialog>
    </>
  );
}

/** Branches: switch to one (a remote one gets a local branch tracking it) or make one from here. */
export function BranchesView({ base, cwd, s, writer, onBack, onClose }: { base: string; cwd: string; s: GitStatus; writer: GitWriter; onBack: () => void; onClose: () => void }) {
  const [branches, setBranches] = useState<GitBranch[] | null>(null);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [asking, setAsking] = useState<string | null>(null);
  useEffect(() => {
    const ac = new AbortController();
    gitBranches(base, cwd, ac.signal)
      .then((r) => setBranches(r.branches))
      .catch((e) => !ac.signal.aborted && setError(gitRefusal(e).detail));
    return () => ac.abort();
  }, [base, cwd, s.branch]);
  const name = query.trim();
  const shown = useMemo(() => (branches ?? []).filter((b) => b.name.toLowerCase().includes(name.toLowerCase())), [branches, name]);
  const exists = (branches ?? []).some((b) => !b.remote && b.name === name);
  const canCreate = name !== "" && !exists && /^[A-Za-z0-9._/-]+$/.test(name);
  const dirty = s.files.length;
  const go = async (branch: string, create: boolean) => {
    setAsking(null);
    const r = await writer.run(`switch:${branch}`, "switch", { branch, create }, t(create ? "session-gitwrite.created" : "session-gitwrite.switched", { branch }));
    if (r) onBack();
  };
  // with uncommitted changes a switch asks first: they go along (or the switch is refused for the files it would overwrite)
  const pick = (branch: string, create: boolean) => (dirty ? setAsking(`${create ? "+" : ""}${branch}`) : void go(branch, create));
  const target = asking?.replace(/^\+/, "") ?? "";
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <BackHeader title={t("session-gitwrite.branches")} subtitle={s.branch || t("session-gitpanel.detached_head")} onBack={onBack} onClose={onClose} />
      <div className="px-3 pt-3 sm:px-4">
        <SearchField value={query} onChange={setQuery} placeholder={t("session-gitwrite.branch_search")} label={t("session-gitwrite.branch_search")} />
        {canCreate && (
          <button type="button" disabled={writer.busy !== null} onClick={() => pick(name, true)} className="mt-2 flex h-10 w-full items-center gap-2 rounded-card bg-surface px-4 text-left text-[13px] font-medium text-accent-ink shadow-card hover:bg-hover disabled:text-ink-3">
            <Icon name="plus" size={14} stroke={2.2} />
            <span className="min-w-0 flex-1 truncate">{t("session-gitwrite.branch_create", { branch: name })}</span>
            {writer.busy === `switch:${name}` && <Spinner />}
          </button>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-3 pb-6 sm:px-4">
        {branches === null ? (
          error ? <Empty icon="info" title={t("session-gitwrite.branches_failed")} detail={error} /> : <div className="flex h-32 items-center justify-center"><Loading /></div>
        ) : (
          <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
            {shown.map((b) => (
              <button
                key={(b.remote ? "r:" : "l:") + b.name}
                type="button"
                disabled={b.current || writer.busy !== null}
                onClick={() => pick(b.remote ? b.name.slice(b.name.indexOf("/") + 1) : b.name, false)}
                className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors enabled:hover:bg-hover"
              >
                <Icon name="branch" size={14} className={cx("shrink-0", b.current ? "text-accent" : "text-ink-3")} />
                <span className="min-w-0 flex-1">
                  <span className={cx("block truncate text-[13px] font-medium", b.current ? "text-accent-ink" : "text-ink")}>{b.name}</span>
                  <span className="block truncate text-[11.5px] text-ink-3">
                    {b.subject} · {ago(b.date)}
                  </span>
                </span>
                {b.current ? (
                  <span className="shrink-0 text-[11.5px] text-ink-3">{t("session-gitwrite.current")}</span>
                ) : writer.busy === `switch:${b.name}` ? (
                  <Spinner />
                ) : b.remote ? (
                  <span className="shrink-0 text-[11.5px] text-ink-3">{t("session-gitwrite.remote")}</span>
                ) : null}
              </button>
            ))}
            {shown.length === 0 && !canCreate && <div className="px-4 py-3 text-[13px] text-ink-3">{t("session-gitwrite.no_branches")}</div>}
          </div>
        )}
      </div>
      <Dialog open={asking !== null} onClose={() => setAsking(null)} title={t(asking?.startsWith("+") ? "session-gitwrite.create_title" : "session-gitwrite.switch_title", { branch: target })}>
        <div className="py-1 text-[13.5px] leading-[1.6] text-ink-2">{t("session-gitwrite.switch_dirty", { count: dirty })}</div>
        <div className="mt-4 flex justify-end gap-2">
          <Button tone="secondary" onClick={() => setAsking(null)}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" icon="branch" onClick={() => void go(target, Boolean(asking?.startsWith("+")))}>
            {asking?.startsWith("+") ? t("session-gitwrite.create") : t("session-gitwrite.switch")}
          </Button>
        </div>
      </Dialog>
      {writer.dialog}
    </div>
  );
}

/** Stashes: put the uncommitted changes away (untracked files too, if asked), apply or pop one back, drop one. */
export function StashesView({ s, writer, onBack, onClose }: { s: GitStatus; writer: GitWriter; onBack: () => void; onClose: () => void }) {
  const [list, setList] = useState<GitStashEntry[] | null>(null);
  const [message, setMessage] = useState("");
  const [untracked, setUntracked] = useState(true);
  const [dropping, setDropping] = useState<GitStashEntry | null>(null);
  const loaded = useRef(false);
  const refresh = async () => {
    setDropping(null);
    const r = await writer.run("stash:list", "stash", { op: "list" });
    if (r) setList(r.stashes ?? []);
  };
  useEffect(() => {
    if (loaded.current) return;
    loaded.current = true;
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const act = async (op: "push" | "apply" | "pop" | "drop", e?: GitStashEntry) => {
    setDropping(null);
    if (op !== "push" && (!e || !hasStashIdentity(e))) return;
    const done = { push: "session-gitwrite.stashed", apply: "session-gitwrite.applied", pop: "session-gitwrite.popped", drop: "session-gitwrite.dropped" }[op];
    const r = await writer.run(`stash:${op}:${e?.index ?? ""}`, "stash", op === "push" ? { op, message, untracked } : { op, index: e?.index, oid: e?.oid }, t(done));
    if (r) {
      setList(r.stashes ?? []);
      if (op === "push") setMessage("");
    } else if (op !== "push") {
      // Refresh after refusal; the user must reselect before another write.
      await refresh();
    }
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <BackHeader title={t("session-gitwrite.stashes")} subtitle={s.branch || t("session-gitpanel.detached_head")} onBack={onBack} onClose={onClose} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-3 pb-6 sm:px-4">
        <div className="rounded-card bg-surface p-3 shadow-card">
          <input
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder={t("session-gitwrite.stash_message")}
            aria-label={t("session-gitwrite.stash_message")}
            className="block h-9 w-full rounded-control bg-field px-3 text-[13px] text-ink outline-none placeholder:text-ink-3 focus:ring-2 focus:ring-accent/40"
          />
          <div className="mt-2 flex items-center gap-2">
            <label className="flex min-w-0 flex-1 items-center gap-2 text-[12px] text-ink-2">
              <input type="checkbox" checked={untracked} onChange={(e) => setUntracked(e.target.checked)} className="accent-(--color-accent)" />
              <span className="truncate">{t("session-gitwrite.stash_untracked")}</span>
            </label>
            <Button tone="primary" icon="archive" busy={writer.busy === "stash:push:"} disabled={writer.busy !== null || s.files.length === 0} onClick={() => void act("push")}>
              {t("session-gitwrite.stash_push")}
            </Button>
          </div>
        </div>
        <div className="mt-5 mb-1.5 flex items-center justify-between gap-2 px-1 text-[12px] font-medium text-ink-3">
          <span>{t("session-gitwrite.stash_list")}</span>
          <Button tone="quiet" disabled={writer.busy !== null} onClick={() => void refresh()}>{t("session-gitwrite.stash_refresh")}</Button>
        </div>
        {list?.some((e) => !hasStashIdentity(e)) && <p className="mb-3 text-[13px] text-ink-2">{t("session-gitwrite.stash_old_node")}</p>}
        <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
          {list === null ? (
            <div className="flex h-16 items-center justify-center">
              <Loading />
            </div>
          ) : list.length === 0 ? (
            <div className="px-4 py-3 text-[13px] text-ink-3">{t("session-gitwrite.no_stashes")}</div>
          ) : (
            list.map((e) => (
              <div key={`${e.oid ?? "legacy"}:${e.index}`} className="flex items-center gap-2 px-4 py-2.5">
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] font-medium text-ink" title={e.message}>
                    {e.message}
                  </span>
                  <span className="block text-[11.5px] text-ink-3">
                    stash@{"{"}
                    {e.index}
                    {"}"} · {ago(e.date)}
                  </span>
                </span>
                <Button tone="quiet" busy={writer.busy === `stash:apply:${e.index}`} disabled={writer.busy !== null || !hasStashIdentity(e)} onClick={() => void act("apply", e)}>
                  {t("session-gitwrite.stash_apply")}
                </Button>
                <Button tone="quiet" busy={writer.busy === `stash:pop:${e.index}`} disabled={writer.busy !== null || !hasStashIdentity(e)} onClick={() => void act("pop", e)}>
                  {t("session-gitwrite.stash_pop")}
                </Button>
                <IconButton icon="trash" label={t("session-gitwrite.stash_drop")} disabled={writer.busy !== null || !hasStashIdentity(e)} onClick={() => setDropping(e)} />
              </div>
            ))
          )}
        </div>
      </div>
      <Dialog open={dropping !== null} onClose={() => setDropping(null)} title={t("session-gitwrite.drop_title")}>
        <div className="py-1 text-[13.5px] leading-[1.6] text-ink-2">{t("session-gitwrite.drop_confirm", { message: dropping?.message ?? "" })}</div>
        <div className="mt-4 flex justify-end gap-2">
          <Button tone="secondary" onClick={() => setDropping(null)}>
            {t("common.cancel")}
          </Button>
          <Button tone="danger" icon="trash" onClick={() => dropping && void act("drop", dropping)}>
            {t("session-gitwrite.stash_drop")}
          </Button>
        </div>
      </Dialog>
      {writer.dialog}
    </div>
  );
}
