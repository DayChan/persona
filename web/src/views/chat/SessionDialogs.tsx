// The chat's ⋯ menu dialogs: session info, rename (the agent / its space), delete, close, and the run-mode chips under the composer.
import { useEffect, useRef, useState } from "react";
import { api, enc, errorText, sessionPath } from "../../lib/api";
import { ago, externalLink, resumeCommand, sessionCan, sessionTitle, shortPath, tabLabel } from "../../lib/format";
import { openLink } from "../../lib/links";
import { BYPASS_MODE, BYPASS_WARNING, canSwitchToBypass, effortOptions, effortTitle, modeTitle, renameBody, switchableModes } from "../../lib/session-actions";
import { canSwitchModel, onModelPicker } from "../../lib/model-switch";
import { CODEX_ULTRA, ultracodeOn } from "../../lib/ultracode";
import { href, navigate, parse, showsSession } from "../../lib/router";
import { stripBase } from "../../lib/base";
import { dropSession, getSession, refresh, refreshAssistant, upsertSession } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Button, copyText, cx, Dialog, Loading, Menu, Switch, toast, useMenu } from "../../ui/atoms";
import { removeAgentWorktree, worktreeLoss, worktreeStatus, type WorktreeStatus } from "../../lib/worktree";
import { Icon, type IconName } from "../../ui/icons";
import { lazyOverlay } from "../LazyOverlay";
import { UltraMark } from "./Ultracode";
import { t } from "../../i18n";
import { AlwaysPermissions } from "./AlwaysPermissions";
import { DelegatedHistory } from "./Delegated";
import { appArchiveDeps, archiveAgent } from "../../lib/agent-archives";

// the model and effort popover loads on the chip's first tap, not with every chat's header
const ModelEffortPopover = lazyOverlay(() => import("./ModelEffortPopover").then((m) => m.ModelEffortPopover));

interface SessionInfo {
  session_id?: string;
  transcript_path?: string;
  cwd?: string;
  model?: string;
  launcher?: string;
  git_branch?: string;
  version?: string;
  permission_mode?: string;
  effort?: string;
  context_tokens?: number;
  output_tokens?: number;
  compactions?: number;
  turns?: number;
  started_at?: string;
  last_activity?: string;
}

const count = (n?: number) => (n === undefined ? "" : n >= 1000 ? `${(n / 1000).toFixed(n >= 100_000 ? 0 : 1)}K` : String(n));

// "Session info" (iOS SessionInfoSheet): how it runs, how much it used, where it lives; ids and the resume command copy.
export function SessionInfoDialog({ session, machineName, open, onClose }: { session: Session; machineName: string; open: boolean; onClose: () => void }) {
  const [info, setInfo] = useState<SessionInfo | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    let live = true;
    setInfo(null);
    setError("");
    api<SessionInfo>(`${sessionPath(session.machine_id, session.session_id)}/info`)
      .then((r) => live && setInfo(r))
      .catch((e) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
  }, [open, session.machine_id, session.session_id]);

  const groups: { title: string; rows: [string, string | undefined, boolean?][] }[] = info
    ? [
        { title: t("chat-sessiondialogs.group_run"), rows: [[t("chat-sessiondialogs.model"), info.model], [t("chat-sessiondialogs.launcher"), info.launcher || session.launcher], [t("chat-sessiondialogs.permission_mode"), modeTitle(info.permission_mode)], [t("chat-sessiondialogs.effort"), effortTitle(info.effort)], [t("chat-sessiondialogs.version"), info.version, true]] },
        { title: t("chat-sessiondialogs.group_usage"), rows: [[t("chat-sessiondialogs.context"), count(info.context_tokens) && `${count(info.context_tokens)} tokens`], [t("chat-sessiondialogs.output"), count(info.output_tokens) && `${count(info.output_tokens)} tokens`], [t("chat-sessiondialogs.turns"), info.turns !== undefined ? String(info.turns) : ""], [t("chat-sessiondialogs.compaction"), info.compactions ? t("chat-sessiondialogs.compactions", { count: info.compactions }) : ""], [t("chat-sessiondialogs.started"), info.started_at && ago(info.started_at)], [t("chat-sessiondialogs.last_activity"), info.last_activity && ago(info.last_activity)]] },
        { title: t("chat-sessiondialogs.group_location"), rows: [[t("chat-sessiondialogs.machine"), machineName], [t("chat-sessiondialogs.folder"), info.cwd ? shortPath(info.cwd) : undefined, true], [t("chat-sessiondialogs.branch"), info.git_branch, true], [t("chat-sessiondialogs.transcript"), info.transcript_path ? shortPath(info.transcript_path) : undefined, true]] },
      ]
    : [];

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-sessiondialogs.info_title")}
      width={480}
      footer={
        <>
          <Button tone="quiet" icon="copy" onClick={() => copyText(session.session_id)}>
            {t("chat-sessiondialogs.copy_session_id")}
          </Button>
          {externalLink(session) && (
            <Button tone="secondary" icon="external" onClick={() => openLink(externalLink(session)!)}>
              {t("chat-sessiondialogs.open_in_lark")}
            </Button>
          )}
          {!session.assistant && (
            <Button tone="secondary" icon="terminal" onClick={() => copyText(resumeCommand(session))}>
              {t("chat-sessiondialogs.copy_resume_command")}
            </Button>
          )}
        </>
      }
    >
      {!info && !error && (
        <div className="flex justify-center py-6">
          <Loading />
        </div>
      )}
      {error && <p className="py-4 text-[13px] text-red">{error}</p>}
      <div className="flex flex-col gap-3">
        {open && session.assistant && <AlwaysPermissions key={session.session_id} session={session.session_id} />}
        {open && !session.assistant && <DelegatedHistory key={`${session.machine_id}/${session.session_id}`} session={session} />}
        {groups.map((g) => {
          const rows = g.rows.filter(([, v]) => v);
          if (!rows.length) return null;
          return (
            <section key={g.title}>
              <h3 className="mb-1 text-[11.5px] font-medium text-ink-3">{g.title}</h3>
              <div className="divide-y divide-line overflow-hidden rounded-[10px] bg-inset shadow-hairline">
                {rows.map(([k, v, mono]) => (
                  <div key={k} className="flex items-center gap-3 px-3 py-1.5 text-[12.5px]">
                    <span className="w-16 shrink-0 text-ink-3">{k}</span>
                    <span title={v} className={cx("min-w-0 flex-1 truncate text-ink", mono && "font-mono text-[12px]")}>
                      {v}
                    </span>
                  </div>
                ))}
              </div>
            </section>
          );
        })}
      </div>
    </Dialog>
  );
}

// "Rename": the herdr tab and the session's name (Claude's /rename), each only when changed (iOS RenameSessionAlert).
export function RenameDialog({ session, open, onClose }: { session: Session; open: boolean; onClose: () => void }) {
  const [tab, setTab] = useState("");
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!open) return;
    setTab(session.slot_name ?? "");
    setTitle(session.title ?? "");
    // only when it opens: a push while typing must not overwrite the field
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const hasTab = !session.assistant && Boolean(session.slot_id);
  const body = renameBody(session, tab, title);
  const save = async () => {
    // the form submits on Enter while the field stays editable: a second Enter would rename (type /rename) twice
    if (busy) return;
    if (!body) return onClose();
    setBusy(true);
    try {
      await api(`${sessionPath(session.machine_id, session.session_id)}/rename`, { method: "POST", json: body });
      // the list shows the new name at once; the hub's push confirms it
      upsertSession({ ...session, ...(body.title ? { title: body.title } : {}), ...(body.tab_name ? { slot_name: body.tab_name } : {}) });
      if (session.assistant) void refreshAssistant().catch(() => {});
      toast(t("chat-sessiondialogs.renamed"), "ok");
      onClose();
    } catch (e) {
      toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-sessiondialogs.rename")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy} disabled={!body} onClick={() => void save()}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        {hasTab && <Field label={t("chat-sessiondialogs.tab_name")} value={tab} onChange={setTab} autoFocus />}
        <Field label={t("chat-sessiondialogs.session_name")} value={title} onChange={setTitle} autoFocus={!hasTab} />
        <p className="text-[12px] leading-relaxed text-ink-3">{session.assistant ? t("chat-sessiondialogs.rename_hint_assistant") : t("chat-sessiondialogs.rename_hint_agent")}</p>
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

// "Rename space": herdr's workspace (and the pins / mutes that name it).
export function RenameSpaceDialog({ session, open, onClose }: { session: Session; open: boolean; onClose: () => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) setName(session.space_name ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const next = name.trim();
  const save = async () => {
    if (busy) return; // a second Enter while the first rename is out
    if (!session.space_id || !next || next === session.space_name) return onClose();
    setBusy(true);
    try {
      await api(`/machines/${enc(session.machine_id)}/spaces/${enc(session.space_id)}/rename`, { method: "POST", json: { name: next } });
      void refresh().catch(() => {});
      toast(t("chat-sessiondialogs.space_renamed"), "ok");
      onClose();
    } catch (e) {
      toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-sessiondialogs.rename_space")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy} disabled={!next || next === session.space_name} onClick={() => void save()}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <Field label={t("chat-sessiondialogs.space_name")} value={name} onChange={setName} autoFocus />
        <p className="text-[12px] leading-relaxed text-ink-3">{t("chat-sessiondialogs.rename_space_hint")}</p>
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

function Field({ label, value, onChange, autoFocus }: { label: string; value: string; onChange: (v: string) => void; autoFocus?: boolean }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[12px] text-ink-3">{label}</span>
      <input
        value={value}
        autoFocus={autoFocus}
        onChange={(e) => onChange(e.target.value)}
        className="h-9 rounded-control bg-surface px-3 text-[13px] text-ink shadow-btn outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]"
      />
    </label>
  );
}

// The two chips under the composer, like Claude Code's bottom bar: permission mode and effort (POST …/modes). A Codex
// thread on its app-server gets the effort alone, among its own levels (effortOnly; `Session.efforts`). An assistant
// conversation may also switch to "Allow everything" (#2416). On a Claude Code session the effort chip opens the model and effort
// popover instead of the effort menu (issue #2434; the chip itself reads as before), and `/model` Enter in the composer
// opens it too. Claude Code's ultracode switch (#3428) is a row under the popover's efforts (a session that has it always
// gets the popover, so the effort menu has no such row); while it is on, the chip carries the workflow mark, as a Codex
// thread on its "ultra" effort does (the same thing: several agents, a lot of quota).
export function ModeChips({ session, effortOnly, assistant, withInfo }: { session: Session; effortOnly?: boolean; assistant?: boolean; withInfo?: boolean }) {
  const modeMenu = useMenu();
  const effortMenu = useMenu();
  const effortChip = useRef<HTMLButtonElement>(null);
  const [popover, setPopover] = useState<HTMLElement | null>(null);
  const withModel = !effortOnly && canSwitchModel(session);
  const key = `${session.machine_id}/${session.session_id}`;
  useEffect(() => (withModel ? onModelPicker(key, () => setPopover(effortChip.current)) : undefined), [withModel, key]);
  useEffect(() => {
    if (!withModel) setPopover(null);
  }, [withModel]);
  const mark = ultracodeOn(session) ? {} : effortOnly && session.effort === CODEX_ULTRA ? { label: t("ultracode.codex_ultra_title"), title: t("ultracode.codex_ultra_title") } : undefined;
  // the popover applies the model and then the effort: the second must build on the session the first changed
  const latest = useRef(session);
  latest.current = session;
  const set = async (patch: { permission_mode?: string; effort?: string; model?: string }): Promise<boolean> => {
    const base = latest.current;
    const before = { permission_mode: base.permission_mode, effort: base.effort, model: base.model };
    upsertSession({ ...base, ...patch });
    try {
      await api(`${sessionPath(base.machine_id, base.session_id)}/modes`, { method: "POST", json: patch });
      return true;
    } catch (e) {
      upsertSession({ ...latest.current, ...Object.fromEntries(Object.keys(patch).map((k) => [k, before[k as keyof typeof before]])) });
      toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
      return false;
    }
  };
  const mode = session.permission_mode;
  const danger = mode === BYPASS_MODE;
  return (
    // A Persona conversation has its launcher chip after these and no text to give way: these keep their width up to
    // the row less 5.5rem (the row's 0.5rem gap and 5rem of launcher chip), so that chip truncates first (#3575). At
    // phone width that chip is an icon (#4142): 3rem leaves its 2.5rem and the gap, 5rem also the footer's 22px ⓘ
    // button and its gap (`withInfo`, #3533). An agent's chips keep their width on a wide screen, where the path beside
    // them truncates first; at phone width (the path hidden) they may shrink, their labels truncating, so the ⓘ after
    // them never slides under the composer's stop or send button (the ultracode mark, #3428, made the English row too wide).
    <span className={cx("flex shrink-0 items-center gap-1", assistant && "min-w-0 max-w-[calc(100%-5.5rem)]", assistant && (withInfo ? "max-sm:max-w-[calc(100%-5rem)]" : "max-sm:max-w-[calc(100%-3rem)]"), !assistant && "max-sm:min-w-0 max-sm:shrink")}>
      {!effortOnly && <Chip label={mode ? modeTitle(mode) : t("chat-sessiondialogs.permission_default")} narrow={{ icon: "shield", label: modeShort(mode) }} danger={danger} onClick={modeMenu.open} active={Boolean(modeMenu.anchor)} title={danger ? BYPASS_WARNING : t("chat-sessiondialogs.permission_mode")} />}
      <Chip
        buttonRef={effortChip}
        label={session.effort ? t("chat-sessiondialogs.effort_chip", { effort: effortTitle(session.effort) }) : t("chat-sessiondialogs.effort_default")}
        narrow={{ icon: "gauge", label: session.effort ? effortTitle(session.effort) : t("chat-sessiondialogs.short_default") }}
        onClick={withModel ? (e) => setPopover((a) => (a ? null : e.currentTarget)) : effortMenu.open}
        active={Boolean(withModel ? popover : effortMenu.anchor)}
        title={withModel ? t("composer.model_effort") : t("chat-sessiondialogs.effort")}
        mark={mark}
      />
      {modeMenu.anchor && !effortOnly && (
        <Menu
          anchor={modeMenu.anchor}
          onClose={modeMenu.close}
          side="top"
          width={300}
          header={t("chat-sessiondialogs.permission_mode")}
          items={switchableModes(assistant || session.assistant, session.agent, canSwitchToBypass(session)).map((m) => ({
            key: m.id,
            label: m.title,
            detail: m.detail,
            radio: true,
            checked: mode === m.id,
            // an agent switched to "Allow all" while it runs (#1379): in red, and asked again first
            ...(m.id === BYPASS_MODE && mode !== BYPASS_MODE && !(assistant || session.assistant || session.agent === "agy") ? { danger: true, confirm: t("chat-sessiondialogs.bypass_confirm"), confirmDetail: BYPASS_WARNING } : {}),
            onSelect: () => void set({ permission_mode: m.id }),
          }))}
        />
      )}
      {effortMenu.anchor && !withModel && (
        <Menu
          anchor={effortMenu.anchor}
          onClose={effortMenu.close}
          side="top"
          width={280}
          header={t("chat-sessiondialogs.effort")}
          items={effortOptions(session).map((m) => ({ key: m.id, label: m.title, detail: m.detail, radio: true, checked: session.effort === m.id, onSelect: () => void set({ effort: m.id }) }))}
        />
      )}
      {popover && withModel && <ModelEffortPopover session={session} anchor={popover} onClose={() => setPopover(null)} apply={set} />}
    </span>
  );
}

// The permission chip's word below 340 px (#4316): enough to tell whether the agent asks first.
const MODE_SHORT: Record<string, string> = {
  default: "chat-sessiondialogs.mode_short_default",
  acceptEdits: "chat-sessiondialogs.mode_short_accept_edits",
  plan: "chat-sessiondialogs.mode_short_plan",
  auto: "chat-sessiondialogs.mode_short_auto",
  [BYPASS_MODE]: "chat-sessiondialogs.mode_short_bypass",
};

export function modeShort(mode?: string): string {
  if (!mode) return t("chat-sessiondialogs.short_default");
  return MODE_SHORT[mode] ? t(MODE_SHORT[mode]) : modeTitle(mode);
}

/** `phoneIcon`: at phone width the label gives way to this icon (still read out, and in full in the menu it opens), so
 *  the chips beside it keep their words (#4142); wider, the chip is as before.
 *  `narrow`: below 340 px (a 320 px phone, #4316) the label gives way to this icon and short word, which still say the
 *  setting (the chevron goes, for room); the full label stays for screen readers. From 340 px up the chip is as before. */
export function Chip({ label, onClick, active, danger, title, buttonRef, phoneIcon, narrow, mark }: { label: string; onClick: (e: React.MouseEvent<HTMLElement>) => void; active?: boolean; danger?: boolean; title: string; buttonRef?: React.Ref<HTMLButtonElement>; phoneIcon?: IconName; narrow?: { icon: IconName; label: string }; /** The workflow mark after the label (ultracode on, a Codex "ultra" effort; #3428): an icon, no words. */ mark?: { label?: string; title?: string } }) {
  return (
    <button
      ref={buttonRef}
      type="button"
      title={title}
      aria-haspopup="menu"
      onClick={onClick}
      className={cx(
        // min-w-0 / max-w-full: on a phone the row has less room than the labels; the label truncates and the chevron
        // stays inside the box, clear of the send button (#3575)
        "touch-hit flex h-5.5 min-w-0 max-w-full items-center gap-0.5 whitespace-nowrap rounded-full px-2 text-[11px] font-medium transition-colors",
        danger ? "bg-red-tint text-red" : active ? "bg-hover text-ink" : "bg-field text-ink-2 hover:bg-hover hover:text-ink",
      )}
    >
      {phoneIcon && <Icon name={phoneIcon} size={12} className="shrink-0 sm:hidden" />}
      {narrow && <Icon name={narrow.icon} size={12} className="shrink-0 xs:hidden" />}
      {narrow && <span aria-hidden className="min-w-0 truncate xs:hidden">{narrow.label}</span>}
      <span className={cx("min-w-0 truncate", phoneIcon && "max-sm:sr-only", narrow && "max-xs:sr-only")}>{label}</span>
      {mark && <UltraMark size={10} label={mark.label} title={mark.title} className="ml-0.5" />}
      <Icon name="chevron" size={9} stroke={2.4} className={cx("shrink-0", narrow && "max-xs:hidden")} />
    </button>
  );
}

// "Delete chat" (issue #801): the hub takes it out of every app's list; the transcript stays on the computer and the
// conversation comes back once it is used again (claude --resume).
export function DeleteSessionDialog({ session, machineName, open, onClose }: { session: Session; machineName: string; open: boolean; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const wt = useAgentWorktree(session, open);
  const [cleanup, setCleanup] = useState<Cleanup>(KEEP);
  useEffect(() => {
    if (open) setCleanup(KEEP); // each time: keeping is the default
  }, [open]);
  const go = async () => {
    setBusy(true);
    try {
      await api(sessionPath(session.machine_id, session.session_id), { method: "DELETE" });
      dropSession(session.machine_id, session.session_id);
      if (location.pathname === href({ name: "session", mid: session.machine_id, sid: session.session_id })) navigate({ name: "home" }, true);
      toast(t("chat-sessiondialogs.deleted"), "ok");
      onClose();
      await runCleanup(session.machine_id, wt, cleanup);
    } catch (e) {
      toast(t("chat-sessiondialogs.delete_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-sessiondialogs.delete_title")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="danger" busy={busy} disabled={!cleanupReady(wt, cleanup)} onClick={go}>
            {t("chat-sessiondialogs.delete")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">
        {t("chat-sessiondialogs.delete_detail", { title: sessionTitle(session), machine: machineName })}
      </p>
      {wt && <WorktreeCleanup st={wt} value={cleanup} onChange={setCleanup} />}
    </Dialog>
  );
}

// ── the worktree a closed or deleted agent worked in (#6115) ──

/** What to do with it: keep it (the default), or remove it, its branch too if asked; `confirmed` that what it loses goes. */
export interface Cleanup {
  remove: boolean;
  deleteBranch: boolean;
  confirmed: boolean;
}

const KEEP: Cleanup = { remove: false, deleteBranch: false, confirmed: false };

/** The worktree the agent's folder is, when Persona made it; null otherwise (and while the computer is asked). */
function useAgentWorktree(s: Session, open: boolean): WorktreeStatus | null {
  const [st, setSt] = useState<WorktreeStatus | null>(null);
  useEffect(() => {
    setSt(null);
    if (!open || !s.cwd) return;
    const c = new AbortController();
    void worktreeStatus(s.machine_id, s.cwd, c.signal).then((x) => {
      if (!c.signal.aborted) setSt(x?.ours && x.path ? x : null);
    });
    return () => c.abort();
  }, [open, s.machine_id, s.cwd]);
  return st;
}

/** Removing it loses something the user has not confirmed yet: the button waits. */
export function cleanupReady(st: WorktreeStatus | null, c: Cleanup): boolean {
  if (!st || !c.remove) return true;
  const loss = worktreeLoss(st, c.deleteBranch);
  return c.confirmed || (loss.dirty === 0 && loss.unmerged === 0);
}

/** After the agent is closed or deleted: removes its worktree when asked, forced only for what the user confirmed. */
async function runCleanup(mid: string, st: WorktreeStatus | null, c: Cleanup) {
  if (!st?.path || !c.remove) return;
  const loss = worktreeLoss(st, c.deleteBranch);
  try {
    const r = await removeAgentWorktree(mid, st.path, { deleteBranch: c.deleteBranch, force: loss.dirty > 0 || loss.unmerged > 0 });
    toast(t(r.branch_deleted ? "chat-sessiondialogs.wt_removed_branch" : "chat-sessiondialogs.wt_removed"), "ok");
  } catch (e) {
    toast(t("chat-sessiondialogs.wt_remove_failed", { error: errorText(e) }), "error");
  }
}

/** Keep or remove the worktree, and what removing it loses (said with the counts, and to be confirmed). */
export function WorktreeCleanup({ st, value, onChange }: { st: WorktreeStatus; value: Cleanup; onChange: (c: Cleanup) => void }) {
  const loss = worktreeLoss(st, value.deleteBranch);
  const losing = value.remove && (loss.dirty > 0 || loss.unmerged > 0);
  const pick = (remove: boolean) => onChange({ ...value, remove, confirmed: false });
  return (
    <div data-worktree-cleanup className="mt-3 rounded-control bg-inset px-3 py-2.5">
      <div className="flex items-center gap-2 text-[12.5px] font-medium text-ink">
        <Icon name="branch" size={13} stroke={2.2} className="shrink-0 text-ink-2" />
        <span className="min-w-0 flex-1 truncate">{t("chat-sessiondialogs.wt_title")}</span>
      </div>
      <p className="mt-0.5 truncate pl-[21px] font-mono text-[11.5px] text-ink-3" title={st.path}>
        {st.branch ? `${st.branch} · ` : ""}
        {shortPath(st.path ?? "")}
      </p>
      <div role="radiogroup" aria-label={t("chat-sessiondialogs.wt_title")} className="mt-2 flex gap-1.5 pl-[21px]">
        {([false, true] as const).map((remove) => (
          <button
            key={String(remove)}
            type="button"
            role="radio"
            aria-checked={value.remove === remove}
            onClick={() => pick(remove)}
            className={cx("h-7 rounded-full px-3 text-[12px] font-medium whitespace-nowrap transition-colors", value.remove === remove ? "bg-surface text-ink shadow-btn" : "text-ink-2 hover:bg-hover")}
          >
            {t(remove ? "chat-sessiondialogs.wt_remove" : "chat-sessiondialogs.wt_keep")}
          </button>
        ))}
      </div>
      {value.remove && (
        <div className="mt-2 flex flex-col gap-1.5 pl-[21px]">
          {st.branch && (
            <div className="flex items-center gap-2 text-[12px] text-ink-2">
              <span className="min-w-0 flex-1">{t("chat-sessiondialogs.wt_delete_branch", { branch: st.branch })}</span>
              <Switch on={value.deleteBranch} onChange={(on) => onChange({ ...value, deleteBranch: on, confirmed: false })} label={t("chat-sessiondialogs.wt_delete_branch", { branch: st.branch })} />
            </div>
          )}
          {loss.dirty > 0 && <p className="text-[11.5px] leading-snug text-red" data-wt-dirty>{t("chat-sessiondialogs.wt_dirty", { count: loss.dirty })}</p>}
          {loss.unmerged > 0 && <p className="text-[11.5px] leading-snug text-red" data-wt-unmerged>{t("chat-sessiondialogs.wt_unmerged", { count: loss.unmerged, main: st.main_branch || "main" })}</p>}
          {losing && (
            <div className="flex items-center gap-2 text-[12px] font-medium text-ink">
              <span className="min-w-0 flex-1">{t("chat-sessiondialogs.wt_confirm_loss")}</span>
              <Switch on={value.confirmed} onChange={(on) => onChange({ ...value, confirmed: on })} label={t("chat-sessiondialogs.wt_confirm_loss")} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// "Close Agent": quits the agent and closes its tab. The chat's ⋯ and the Agents list's row menu both ask here (#3405).
// `onClosed`: what to do once it is closed; without it, the page leaves the agent's chat only when that chat is open.
export function CloseAgentDialog({ open, onClose, onClosed, session, machineName }: { open: boolean; onClose: () => void; onClosed?: () => void; session: Session; machineName: string }) {
  const [busy, setBusy] = useState(false);
  const wt = useAgentWorktree(session, open);
  const [cleanup, setCleanup] = useState<Cleanup>(KEEP);
  useEffect(() => {
    if (open) setCleanup(KEEP); // each time: keeping is the default
  }, [open]);
  const go = async () => {
    // the agent went away while the dialog was open: nothing left to close, so don't leave a button that does nothing
    if (!sessionCan(getSession(session.machine_id, session.session_id), "terminal")) {
      onClose();
      return;
    }
    setBusy(true);
    try {
      await api(`${sessionPath(session.machine_id, session.session_id)}/close`, { method: "POST" });
      toast(t("chat-chatview.closed"), "ok");
      onClose();
      if (onClosed) onClosed();
      else if (showsSession(parse(stripBase(location.pathname), location.search), session.machine_id, session.session_id)) navigate({ name: "home" });
      await runCleanup(session.machine_id, wt, cleanup);
    } catch (e) {
      toast(t("chat-chatview.close_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-chatview.close_agent_title")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="danger" busy={busy} disabled={!cleanupReady(wt, cleanup)} onClick={go}>
            {t("chat-chatview.close_agent")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">
        {t("chat-chatview.close_agent_detail", { machine: machineName, where: [session.space_name, tabLabel(session)].filter(Boolean).join(" › ") || sessionTitle(session) })}
      </p>
      {wt && <WorktreeCleanup st={wt} value={cleanup} onChange={setCleanup} />}
    </Dialog>
  );
}

// "Archive and close" (lib/agent-archives.ts): archives the agent and closes its tab and process on its computer
// (session.retire); its transcript stays, and a message resumes it. Asked first because it stops a process; the
// worktree is never removed here (the archive is meant to come back). The chat's ⋯ and the row menu both ask here.
export function ArchiveCloseDialog({ open, onClose, session, machineName }: { open: boolean; onClose: () => void; session: Session; machineName: string }) {
  const [busy, setBusy] = useState(false);
  const go = async () => {
    // the latest row (its title); if it got busy meanwhile, the hub refuses (409) and the toast says why
    const now = getSession(session.machine_id, session.session_id) ?? session;
    setBusy(true);
    try {
      await archiveAgent(now, true, appArchiveDeps);
      onClose();
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-sessiondialogs.archive_close_title")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" icon="archive" busy={busy} onClick={go}>
            {t("chat-sessiondialogs.archive_close")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{t("chat-sessiondialogs.archive_close_detail", { title: sessionTitle(session), machine: machineName })}</p>
    </Dialog>
  );
}
