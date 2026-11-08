// What the Files panel changes on the machine (#4883, aligned with the apps' #4858): upload into a folder (dropped or
// chosen, with progress and cancel; a name already there asks replace or keep both), download, rename, move and move
// to the trash (asked first, then "Moved to trash · Undo"). Both views share it: the drill-in list (FilesPanel) and the
// tree (FilesTree). Each action shows only when the hub says it can (`upload_dir`, `file_edit`).
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { api, credentialOwner, enc, errorText, fileDownloadURL, getToken, isAbort, subscribeCredentialOwner } from "../../../lib/api";
import { t } from "../../../i18n";
import {
  FILE_EDIT_FEATURE,
  isNameTaken,
  moveEntries,
  nameProblem,
  opErrorText,
  parentOf,
  renameEntry,
  restoreEntries,
  trashEntries,
  UPLOAD_DIR_FEATURE,
  uploadToDir,
  within,
  type Conflict,
  type UploadCredential,
} from "../../../lib/file-ops";
import { joinPath, type FsListing } from "../../../lib/files";
import { baseName, bytes, shortPath } from "../../../lib/format";
import { hubHas, useStore } from "../../../lib/store";
import { contextMenuProps } from "../../../ui/context-menu";
import type { MenuAt, MenuItem } from "../../../ui/menu-model";
import { Button, copyText, cx, Dialog, IconButton, Loading, type ToastAction } from "../../../ui/atoms";
import { Icon } from "../../../ui/icons";
import { FileGlyph } from "./parts";

/** A row the actions are about. */
export interface OpTarget {
  path: string;
  name: string;
  dir: boolean;
}

type UploadState = "queued" | "sending" | "conflict" | "failed" | "done";
interface Upload {
  id: number;
  file: File;
  dir: string;
  credential: UploadCredential;
  sent: number;
  state: UploadState;
  conflict?: Conflict;
  error?: string;
  ac?: AbortController;
}

/** A question waiting for the user: replace or keep both, for an upload or a move. */
interface NameAsk {
  name: string;
  dir: string;
  /** More that are waiting on the same question ("Apply to the remaining items"). */
  more: number;
  answer: (c: Conflict | null, all: boolean) => void;
}

export interface FileOps {
  canUpload: boolean;
  canEdit: boolean;
  /** Sends files into `dir` (dropped or chosen). */
  upload: (files: File[], dir: string) => void;
  /** Opens the file chooser for `dir`. */
  choose: (dir: string) => void;
  /** The row's menu: open, download, rename, move, copy path, move to the trash. */
  rowItems: (target: OpTarget, open: () => void) => MenuItem[];
  move: (targets: OpTarget[]) => void;
  trash: (targets: OpTarget[]) => void;
  download: (targets: OpTarget[]) => void;
  /** The tray, the dialogs and the hidden chooser: rendered once by the panel. */
  ui: React.ReactNode;
}

/** What a row of either view does with the actions: a checkbox while selecting, a menu on right click and on its ⋯. */
export interface RowOps {
  selecting: boolean;
  isSelected: (path: string) => boolean;
  toggle: (x: OpTarget) => void;
  menu: (x: OpTarget, at: MenuAt, open: () => void) => void;
}

/** Whether a drag carries files from the computer (not a row of the panel, lib/entry-drag.ts). */
export const dragHasFiles = (e: React.DragEvent) => Array.from(e.dataTransfer.types).includes("Files");

export function useFileOps({ mid, cwd, onChanged }: { mid: string; cwd: string; onChanged: (dirs: string[]) => void }): FileOps {
  const features = useStore((s) => s.features);
  const canUpload = hubHas(features, UPLOAD_DIR_FEATURE);
  const canEdit = hubHas(features, FILE_EDIT_FEATURE);
  const changed = useRef(onChanged);
  changed.current = onChanged;

  // ── uploads: one at a time, in the order they came ──
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner);
  const [uploads, setUploads] = useState<Upload[]>([]);
  const ownedUploads = uploads.filter((u) => u.credential.owner === owner);
  const active = useRef(new Map<number, AbortController>());
  const nextId = useRef(0);
  const patch = useCallback((id: number, p: Partial<Upload>) => setUploads((us) => us.map((u) => (u.id === id ? { ...u, ...p } : u))), []);
  const enqueue = useCallback((files: File[], dir: string, selectedOwner: string) => {
    const token = getToken();
    if (!files.length || !token || selectedOwner !== credentialOwner()) return;
    const credential = { owner: selectedOwner, token };
    const added = files.map((file) => ({ id: ++nextId.current, file, dir, credential, sent: 0, state: "queued" as const }));
    setUploads((us) => selectedOwner === credentialOwner() ? [...us, ...added] : us);
  }, []);
  const upload = useCallback((files: File[], dir: string) => enqueue(files, dir, owner), [enqueue, owner]);
  useEffect(() => {
    setUploads((us) => us.filter((u) => u.credential.owner === owner));
  }, [owner]);
  const sending = ownedUploads.find((u) => u.state === "sending");
  const next = ownedUploads.find((u) => u.state === "queued");
  useEffect(() => {
    if (sending || !next || next.credential.owner !== credentialOwner() || active.current.has(next.id)) return;
    const ac = new AbortController();
    active.current.set(next.id, ac);
    const current = () => !ac.signal.aborted && next.credential.owner === credentialOwner() && active.current.get(next.id) === ac;
    patch(next.id, { state: "sending", ac, sent: 0, error: undefined });
    uploadToDir(mid, next.file, { dir: next.dir, cwd, credential: next.credential, conflict: next.conflict, signal: ac.signal, onProgress: (sent) => { if (current()) patch(next.id, { sent }); } })
      .then(() => {
        if (!current()) return;
        patch(next.id, { state: "done", sent: next.file.size, ac: undefined });
        changed.current([next.dir]);
      })
      .catch((e) => {
        if (next.credential.owner !== credentialOwner() || active.current.get(next.id) !== ac) return;
        if (isAbort(e)) return setUploads((us) => us.filter((u) => u.id !== next.id)); // cancelled: it goes
        if (!current()) return;
        if (isNameTaken(e) && !next.conflict) return patch(next.id, { state: "conflict", ac: undefined });
        patch(next.id, { state: "failed", error: opErrorText(e), ac: undefined });
      })
      .finally(() => { if (active.current.get(next.id) === ac) active.current.delete(next.id); });
  }, [mid, cwd, sending, next, patch]);
  // a finished one leaves the tray a moment later
  useEffect(() => {
    if (!uploads.some((u) => u.state === "done")) return;
    const id = setTimeout(() => setUploads((us) => us.filter((u) => u.state !== "done")), 1600);
    return () => clearTimeout(id);
  }, [uploads]);
  // what everyone waits on goes when the panel does
  useEffect(() => () => {
    active.current.forEach((ac) => ac.abort());
    active.current.clear();
  }, []);

  // ── the name question: the first upload waiting on it, else a move's ──
  const [moveAsk, setMoveAsk] = useState<NameAsk | null>(null);
  const conflicted = ownedUploads.filter((u) => u.state === "conflict");
  const uploadAsk: NameAsk | null = conflicted.length
    ? {
        name: conflicted[0].file.name,
        dir: conflicted[0].dir,
        more: conflicted.length - 1,
        answer: (c, all) => {
          if (owner !== credentialOwner()) return;
          const ids = new Set(all ? conflicted.map((u) => u.id) : [conflicted[0].id]);
          setUploads((us) => (c ? us.map((u) => (ids.has(u.id) ? { ...u, state: "queued", conflict: c } : u)) : us.filter((u) => !ids.has(u.id))));
        },
      }
    : null;
  const ask = uploadAsk ?? moveAsk;

  // ── the chooser ──
  const input = useRef<HTMLInputElement>(null);
  const choice = useRef<{ dir: string; owner: string } | null>(null);
  const choose = useCallback((dir: string) => {
    if (owner !== credentialOwner() || !getToken()) return;
    choice.current = { dir, owner };
    input.current?.click();
  }, [owner]);

  // ── what was done, at the panel's foot: the page's toasts sit over the chat box beside it ──
  const [notice, setNotice] = useState<Notice | null>(null);
  const noticeId = useRef(0);
  const say = useCallback((text: string, tone: Notice["tone"], action?: ToastAction) => setNotice({ id: ++noticeId.current, text, tone, action }), []);

  // ── move, trash, download ──
  const [moving, setMoving] = useState<OpTarget[] | null>(null);
  const [trashing, setTrashing] = useState<OpTarget[] | null>(null);
  const [trashBusy, setTrashBusy] = useState(false);

  const doMove = useCallback(
    async (targets: OpTarget[], dir: string, conflict?: Conflict) => {
      const from = [...new Set(targets.map((x) => parentOf(x.path)))];
      try {
        await moveEntries(mid, targets.map((x) => x.path), cwd, dir, conflict);
        setMoving(null);
        changed.current([...from, dir]);
        say(t("files-ops.moved", { count: targets.length, dir: shortPath(dir) }), "ok");
      } catch (e) {
        if (isNameTaken(e) && !conflict) {
          setMoveAsk({
            name: typeof (e as { body?: { name?: unknown } }).body?.name === "string" ? String((e as { body: { name: string } }).body.name) : targets[0].name,
            dir,
            more: 0,
            answer: (c) => {
              setMoveAsk(null);
              if (c) void doMove(targets, dir, c);
            },
          });
          return;
        }
        throw e;
      }
    },
    [mid, cwd, say],
  );

  const doTrash = useCallback(async () => {
    if (!trashing) return;
    setTrashBusy(true);
    const targets = trashing;
    try {
      const done = await trashEntries(mid, targets.map((x) => x.path), cwd);
      setTrashing(null);
      const dirs = [...new Set(targets.map((x) => parentOf(x.path)))];
      changed.current(dirs);
      const ids = done.map((d) => d.trash_id).filter(Boolean);
      say(
        targets.length === 1 ? t("files-ops.trashed_named", { name: targets[0].name }) : t("files-ops.trashed", { count: targets.length }),
        "ok",
        ids.length
          ? {
              label: t("files-ops.undo"),
              run: () => {
                restoreEntries(mid, ids)
                  .then(() => {
                    changed.current(dirs);
                    say(t("files-ops.restored", { count: ids.length }), "ok");
                  })
                  .catch((e) => say(t("files-ops.restore_failed", { error: opErrorText(e) }), "error"));
              },
            }
          : undefined,
      );
    } catch (e) {
      say(t("files-ops.trash_failed", { error: opErrorText(e) }), "error");
    } finally {
      setTrashBusy(false);
    }
  }, [mid, cwd, trashing, say]);

  const download = useCallback(
    (targets: OpTarget[]) => {
      // one link each: the browser saves them (a folder has no download)
      for (const x of targets.filter((x) => !x.dir)) {
        const a = document.createElement("a");
        a.href = fileDownloadURL(mid, x.path, cwd);
        a.download = x.name;
        a.rel = "noopener";
        document.body.append(a);
        a.click();
        a.remove();
      }
    },
    [mid, cwd],
  );

  const rowItems = useCallback(
    (x: OpTarget, open: () => void): MenuItem[] => {
      const items: MenuItem[] = [{ key: "open", label: x.dir ? t("files-ops.open_folder") : t("files-ops.open"), icon: x.dir ? "folder" : "file", onSelect: open }];
      if (!x.dir) items.push({ key: "download", label: t("files-ops.download"), icon: "download", onSelect: () => download([x]) });
      if (x.dir && canUpload) items.push({ key: "upload", label: t("files-ops.upload_here"), icon: "upload", onSelect: () => choose(x.path) });
      if (canEdit) {
        items.push(
          {
            key: "rename",
            label: t("files-ops.rename"),
            icon: "pencil",
            divider: true,
            onSelect: () => {},
            rename: {
              value: x.name,
              commit: async (name) => {
                const problem = nameProblem(name);
                if (problem) throw new Error(problem);
                try {
                  await renameEntry(mid, x.path, cwd, name.trim());
                } catch (e) {
                  throw new Error(opErrorText(e));
                }
                changed.current([parentOf(x.path)]);
              },
            },
          },
          { key: "move", label: t("files-ops.move"), icon: "arrowRight", onSelect: () => setMoving([x]) },
        );
      }
      items.push({ key: "copy", label: t("files-ops.copy_path"), icon: "copy", divider: !canEdit, onSelect: () => copyText(x.path) });
      if (canEdit) items.push({ key: "trash", label: t("files-ops.trash"), icon: "trash", danger: true, divider: true, onSelect: () => setTrashing([x]) });
      return items;
    },
    [mid, cwd, canUpload, canEdit, download, choose],
  );

  const ui = (
    <>
      <input
        ref={input}
        type="file"
        multiple
        hidden
        data-file-ops-input
        aria-hidden
        tabIndex={-1}
        onChange={(e) => {
          if (choice.current) enqueue(Array.from(e.target.files ?? []), choice.current.dir, choice.current.owner);
          choice.current = null;
          e.target.value = "";
        }}
      />
      <UploadTray
        uploads={ownedUploads}
        // a sending one is aborted (and goes when its request does), a waiting or failed one goes at once
        onCancel={(u) => (u.ac ? u.ac.abort() : setUploads((us) => us.filter((x) => x.id !== u.id)))}
        onRetry={(u) => { if (u.credential.owner === credentialOwner()) patch(u.id, { state: "queued", error: undefined }); }}
      />
      <PanelNotice notice={notice} onDone={(id) => setNotice((n) => (n?.id === id ? null : n))} />
      <NameDialog ask={ask} />
      <MoveDialog mid={mid} cwd={cwd} targets={moving} onClose={() => setMoving(null)} onMove={doMove} />
      <TrashDialog targets={trashing} busy={trashBusy} onClose={() => !trashBusy && setTrashing(null)} onTrash={doTrash} />
    </>
  );

  return { canUpload, canEdit, upload, choose, rowItems, move: setMoving, trash: setTrashing, download, ui };
}
/** The uploads at the panel's foot: each with its progress and a cancel; a failed one says why and can go again. */
function UploadTray({ uploads, onCancel, onRetry }: { uploads: Upload[]; onCancel: (u: Upload) => void; onRetry: (u: Upload) => void }) {
  const shown = uploads.filter((u) => u.state !== "conflict");
  if (!shown.length) return null;
  const left = shown.filter((u) => u.state === "queued" || u.state === "sending").length;
  return (
    <section aria-label={t("files-ops.uploads")} className="shrink-0 border-t border-line bg-inset px-3 py-2">
      <p className="pb-1 text-[11.5px] font-medium text-ink-2" aria-live="polite">
        {left ? t("files-ops.uploading", { count: left }) : t("files-ops.uploads_done")}
      </p>
      <ul className="flex max-h-40 flex-col gap-1 overflow-y-auto">
        {shown.map((u) => {
          const pct = u.file.size ? Math.min(100, Math.round((u.sent / u.file.size) * 100)) : u.state === "done" ? 100 : 0;
          return (
            <li key={u.id} className="flex items-center gap-2">
              <FileGlyph name={u.file.name} />
              <span className="min-w-0 flex-1">
                <span className="flex items-baseline gap-2">
                  <span className="truncate text-[12.5px] text-ink" title={joinPath(u.dir, u.file.name)}>
                    {u.file.name}
                  </span>
                  <span className="ml-auto shrink-0 text-[11px] text-ink-3 tabular-nums">
                    {u.state === "done" ? t("files-ops.upload_done") : u.state === "queued" ? t("files-ops.upload_waiting") : u.state === "failed" ? "" : `${pct}%`}
                  </span>
                </span>
                {u.state === "failed" ? (
                  <span className="block truncate text-[11.5px] text-red" title={u.error}>
                    {u.error}
                  </span>
                ) : (
                  <span
                    role="progressbar"
                    aria-label={u.file.name}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={pct}
                    aria-valuetext={`${pct}% · ${bytes(u.sent)} / ${bytes(u.file.size)}`}
                    className="mt-1 block h-1 overflow-hidden rounded-full bg-field"
                  >
                    <span className={cx("block h-full rounded-full transition-[width] duration-200", u.state === "done" ? "bg-green" : "bg-accent")} style={{ width: `${pct}%` }} />
                  </span>
                )}
              </span>
              {u.state === "failed" && <IconButton icon="retry" label={t("files-ops.upload_retry", { name: u.file.name })} onClick={() => onRetry(u)} size={26} iconSize={13} />}
              {u.state !== "done" && <IconButton icon="x" label={t("files-ops.upload_cancel", { name: u.file.name })} onClick={() => onCancel(u)} size={26} iconSize={13} />}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

interface Notice {
  id: number;
  text: string;
  tone: "ok" | "error";
  action?: ToastAction;
}

/** A line at the panel's foot for what was just done ("Moved to trash · Undo"). It goes after a while, held while the
 *  pointer or the focus is on it (WCAG 2.2.1), or with its ×; its button runs and it goes. */
function PanelNotice({ notice, onDone }: { notice: Notice | null; onDone: (id: number) => void }) {
  const [held, setHeld] = useState(false);
  useEffect(() => {
    if (!notice || held) return;
    const id = setTimeout(() => onDone(notice.id), notice.action ? 8000 : notice.tone === "error" ? 6000 : 3000);
    return () => clearTimeout(id);
  }, [notice, held, onDone]);
  return (
    <div role="status" aria-live="polite" className="shrink-0">
      {notice && (
        <div
          key={notice.id}
          onMouseEnter={() => setHeld(true)}
          onMouseLeave={() => setHeld(false)}
          onFocus={() => setHeld(true)}
          onBlur={(e) => !e.currentTarget.contains(e.relatedTarget as Node | null) && setHeld(false)}
          className="flex items-center gap-2 border-t border-line bg-inset px-3 py-2 text-[12.5px] text-ink"
          style={{ animation: "fade-in 160ms ease-out both" }}
        >
          <Icon name={notice.tone === "error" ? "warning" : "check"} size={13} stroke={2.4} className={cx("shrink-0", notice.tone === "error" ? "text-red" : "text-green")} />
          <span className="min-w-0 flex-1 truncate" title={notice.text}>
            {notice.text}
          </span>
          {notice.action && (
            <Button
              tone="quiet"
              onClick={() => {
                notice.action!.run();
                onDone(notice.id);
              }}
            >
              {notice.action.label}
            </Button>
          )}
          <IconButton icon="x" label={t("files-ops.dismiss")} onClick={() => onDone(notice.id)} size={24} iconSize={12} />
        </div>
      )}
    </div>
  );
}

/** "x already exists": replace it, keep both, or skip; with more waiting, the same for the rest too. */
function NameDialog({ ask }: { ask: NameAsk | null }) {
  const [all, setAll] = useState(false);
  useEffect(() => setAll(false), [ask?.name, ask?.dir]);
  const answer = (c: Conflict | null) => ask?.answer(c, all);
  return (
    <Dialog
      open={Boolean(ask)}
      onClose={() => answer(null)}
      title={t("files-ops.exists_title")}
      footer={
        <>
          <Button tone="quiet" onClick={() => answer(null)}>
            {t("files-ops.skip")}
          </Button>
          <Button onClick={() => answer("keep")}>{t("files-ops.keep_both")}</Button>
          <Button tone="primary" onClick={() => answer("replace")}>
            {t("files-ops.replace")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{ask && t("files-ops.exists_body", { name: ask.name, dir: shortPath(ask.dir) })}</p>
      {ask && ask.more > 0 && (
        <label className="mt-3 flex items-center gap-2 text-[12.5px] text-ink-2">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} className="accent-[var(--accent)]" />
          {t("files-ops.same_for_rest", { count: ask.more })}
        </label>
      )}
    </Dialog>
  );
}

/** "Move to...": a folder picker on the machine, from the folder the first one is in; up, into a folder, then move here. */
function MoveDialog({
  mid,
  cwd,
  targets,
  onClose,
  onMove,
}: {
  mid: string;
  cwd: string;
  targets: OpTarget[] | null;
  onClose: () => void;
  onMove: (targets: OpTarget[], dir: string) => Promise<void>;
}) {
  const [at, setAt] = useState("");
  const [listing, setListing] = useState<{ at: string; dirs?: string[]; error?: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const first = targets?.[0]?.path;
  useEffect(() => {
    if (first) setAt(parentOf(first));
    setFailure("");
  }, [first]);
  useEffect(() => {
    if (!targets || !at) return;
    const ac = new AbortController();
    setListing(null);
    api<FsListing>(`/machines/${enc(mid)}/fs?path=${enc(at)}&cwd=${enc(cwd)}`, { signal: ac.signal })
      .then((l) => setListing({ at, dirs: l.entries.filter((e) => e.dir && !e.name.startsWith(".")).map((e) => e.name) }))
      .catch((e) => !isAbort(e) && setListing({ at, error: errorText(e) }));
    return () => ac.abort();
  }, [mid, cwd, at, targets]);
  const sources = useMemo(() => new Set((targets ?? []).map((x) => parentOf(x.path))), [targets]);
  // not where they all are already, nor into one of the folders moved
  const blocked = (targets ?? []).some((x) => x.dir && within(x.path, at)) || (sources.size === 1 && sources.has(at));
  const move = async () => {
    if (!targets) return;
    setBusy(true);
    setFailure("");
    try {
      await onMove(targets, at);
    } catch (e) {
      setFailure(opErrorText(e));
    } finally {
      setBusy(false);
    }
  };
  const title = targets?.length === 1 ? t("files-ops.move_one_title", { name: targets[0].name }) : t("files-ops.move_title", { count: targets?.length ?? 0 });
  return (
    <Dialog
      open={Boolean(targets)}
      onClose={() => !busy && onClose()}
      title={title}
      width={420}
      footer={
        <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("files-ops.cancel")}
          </Button>
          <Button tone="primary" onClick={move} busy={busy} disabled={blocked || !listing?.dirs}>
            {t("files-ops.move_here")}
          </Button>
        </>
      }
    >
      <div className="flex items-center gap-1 pb-2">
        <IconButton icon="chevronLeft" label={t("files-ops.up")} onClick={() => setAt(parentOf(at))} disabled={at === "/"} iconSize={16} />
        <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink" title={at} aria-live="polite">
          {shortPath(at) || "/"}
        </span>
      </div>
      <div aria-label={t("files-ops.folders")} role="group" className="h-56 overflow-y-auto rounded-[10px] border border-line">
        {!listing || listing.at !== at ? (
          <div className="flex h-full items-center justify-center">
            <Loading label={t("files-ops.reading")} />
          </div>
        ) : listing.error ? (
          <p className="p-3 text-[12.5px] text-red">{listing.error}</p>
        ) : listing.dirs!.length === 0 ? (
          <p className="p-3 text-[12.5px] text-ink-3">{t("files-ops.no_folders")}</p>
        ) : (
          listing.dirs!.map((name) => {
            const path = joinPath(at, name);
            const self = (targets ?? []).some((x) => x.dir && within(x.path, path));
            return (
              <button
                key={name}
                type="button"
                disabled={self}
                onClick={() => setAt(path)}
                className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] text-ink transition-colors hover:bg-hover disabled:opacity-40"
              >
                <FileGlyph name={name} dir />
                <span className="min-w-0 flex-1 truncate">{name}</span>
                <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
              </button>
            );
          })
        )}
      </div>
      {failure && (
        <p role="alert" className="pt-2 text-[12px] text-red">
          {failure}
        </p>
      )}
    </Dialog>
  );
}

/** "Move to trash?": what goes, and that it can be put back. */
function TrashDialog({ targets, busy, onClose, onTrash }: { targets: OpTarget[] | null; busy: boolean; onClose: () => void; onTrash: () => void }) {
  const one = targets?.length === 1 ? targets[0] : null;
  return (
    <Dialog
      open={Boolean(targets)}
      onClose={onClose}
      title={one ? t("files-ops.trash_one_title", { name: one.name }) : t("files-ops.trash_title", { count: targets?.length ?? 0 })}
      width={400}
      footer={
        <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("files-ops.cancel")}
          </Button>
          <Button tone="danger" onClick={onTrash} busy={busy}>
            {t("files-ops.trash")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{one?.dir ? t("files-ops.trash_folder_body") : t("files-ops.trash_body")}</p>
      {targets && targets.length > 1 && (
        <ul className="mt-2 max-h-32 overflow-y-auto text-[12.5px] text-ink">
          {targets.map((x) => (
            <li key={x.path} className="flex items-center gap-2 py-0.5">
              <FileGlyph name={x.name} dir={x.dir} />
              <span className="truncate">{x.name}</span>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}

/** The bar a selection brings: how many, and what can be done to them all. */
export function SelectionBar({ ops, selected, onClear }: { ops: FileOps; selected: OpTarget[]; onClear: () => void }) {
  const files = selected.filter((x) => !x.dir);
  return (
    <div role="toolbar" aria-label={t("files-ops.selection")} className="flex h-10 shrink-0 items-center gap-1 border-b border-line bg-inset px-2">
      <IconButton icon="x" label={t("files-ops.clear_selection")} onClick={onClear} iconSize={14} />
      <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink" aria-live="polite">
        {t("files-ops.selected", { count: selected.length })}
      </span>
      <IconButton icon="download" label={t("files-ops.download")} onClick={() => ops.download(files)} disabled={!files.length} iconSize={14} />
      {ops.canEdit && <IconButton icon="arrowRight" label={t("files-ops.move")} onClick={() => ops.move(selected)} disabled={!selected.length} iconSize={14} />}
      {ops.canEdit && <IconButton icon="trash" label={t("files-ops.trash")} onClick={() => ops.trash(selected)} disabled={!selected.length} iconSize={14} />}
    </div>
  );
}

/** What covers a folder while files are dragged over it: where they will go. */
export function DropHint({ dir }: { dir: string }) {
  return (
    <div aria-hidden className="pointer-events-none absolute inset-1 z-10 flex items-center justify-center rounded-[12px] border-2 border-dashed border-accent bg-accent-tint/50">
      <span className="flex items-center gap-1.5 rounded-full bg-surface px-3 py-1.5 text-[12.5px] font-medium text-ink shadow-btn">
        <Icon name="upload" size={14} />
        {t("files-ops.drop_here", { dir: baseName(dir) || dir })}
      </span>
    </div>
  );
}

/** A row's own handlers: its menu on right click and the menu key, the folder files dropped on it go to, its selection
 *  state for a screen reader. */
export function rowFrameProps(ops: RowOps | undefined, x: OpTarget, open: () => void) {
  if (!ops) return {};
  return {
    ...contextMenuProps((at) => ops.menu(x, at, open)),
    "data-drop-dir": x.dir ? x.path : parentOf(x.path),
    "aria-selected": ops.selecting ? ops.isSelected(x.path) : undefined,
  };
}

/** A click on a row while selecting (or with ⌘ / Ctrl) picks it instead of opening it. */
export function rowClick(ops: RowOps | undefined, x: OpTarget, open: () => void) {
  return (e: React.MouseEvent) => {
    if (ops && (ops.selecting || e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      ops.toggle(x);
    } else open();
  };
}

/** The checkbox at a row's start while selecting. */
export function RowCheck({ ops, x }: { ops: RowOps | undefined; x: OpTarget }) {
  if (!ops?.selecting) return null;
  return (
    <input
      type="checkbox"
      checked={ops.isSelected(x.path)}
      onChange={() => ops.toggle(x)}
      aria-label={t("files-ops.select_row", { name: x.name })}
      className="ml-3 size-3.5 shrink-0 accent-[var(--accent)]"
    />
  );
}

/** The ⋯ at a row's end (hover devices show it on hover, and on focus): the same menu as a right click. */
export function RowMore({ ops, x, open }: { ops: RowOps | undefined; x: OpTarget; open: () => void }) {
  if (!ops) return null;
  return <IconButton icon="more" label={t("files-ops.row_more", { name: x.name })} onClick={(e) => ops.menu(x, { anchor: e.currentTarget }, open)} size={26} iconSize={13} />;
}
