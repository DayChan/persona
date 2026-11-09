import { storedValue, storeValue } from "../../lib/compatibility";
// A session's files (docs/apple/ios.md "Files", iOS FilesPane): from the session's folder (or `path`) folder by folder, a
// breadcrumb back to any level; folders first with size, time and Git marks; hidden files off unless asked for. A
// file opens in the viewer inside the panel (back returns to the list where it was). On a wide screen the folders
// open in place as a tree instead (files/FilesTree.tsx, Moshi's form; the menu switches back); either view refreshes
// itself while shown (lib/fs-live.ts). Both views change files too (files/FileOps.tsx, #4883): files dropped or chosen
// upload into the folder, and a row's menu (right click, its ⋯) downloads, renames, moves or moves it to the trash;
// "Select" picks several for one of those.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, enc, errorText, isAbort } from "../../lib/api";
import { t } from "../../i18n";
import { entryPayload, ENTRY_DRAG_TYPE } from "../../lib/entry-drag";
import { aheadBehind, below, gitMark, joinPath, type FsEntry, type FsListing, type GitStatus } from "../../lib/files";
import { ancestors } from "../../lib/files-tree";
import { ago, baseName, bytes, shortPath } from "../../lib/format";
import { openPanel } from "../../lib/panel";
import { notePlace } from "../../lib/panel-memory";
import { useFsLive } from "../../lib/use-fs-live";
import { NARROW, useMedia } from "../../lib/viewport";
import { Button, copyText, cx, IconButton, Loading, Menu, useMenu } from "../../ui/atoms";
import type { MenuAt } from "../../ui/menu-model";
import { Icon } from "../../ui/icons";
import { scrollBehavior } from "../../ui/stream/motion";
import { PanelHeader } from "../../ui/PanelHeader";
import { FileView } from "./files/FileView";
import { DropHint, dragHasFiles, RowCheck, rowClick, rowFrameProps, RowMore, SelectionBar, useFileOps, type OpTarget, type RowOps } from "./files/FileOps";
import { FilesTree } from "./files/FilesTree";
import { Empty, FileGlyph, GitDot, GitBadge, useStoredFlag } from "./files/parts";

type Listed = { at: string; listing?: FsListing; error?: string };

export function FilesPanel({ mid, sid, cwd, path, file, onClose }: { mid: string; sid: string; cwd: string; path?: string; file?: string; onClose: () => void }) {
  // The root is the session's folder; a `path` inside it opens there with the folders above it in the breadcrumb.
  const start = useMemo(() => {
    const inside = path && cwd ? below(cwd, path) : null;
    return { root: inside || !path ? cwd : path, stack: inside ?? [] };
  }, [cwd, path]);
  const [stack, setStack] = useState<string[]>(start.stack);
  useEffect(() => setStack(start.stack), [start]);
  const dir = stack.reduce(joinPath, start.root);

  const [showHidden, setShowHidden] = useStoredFlag("persona.files.showHidden", false);
  // The tree is the wide screens' view, the drill-in the narrow ones'; the menu's choice wins over both.
  const narrow = useMedia(NARROW);
  const [viewPref, setViewPref] = useState<"tree" | "drill" | null>(() => {
    const v = storedValue("persona.files.view");
    return v === "tree" || v === "drill" ? v : null;
  });
  const tree = (viewPref ?? (narrow ? "drill" : "tree")) === "tree";
  const chooseView = (v: "tree" | "drill") => {
    storeValue("persona.files.view", v);
    setViewPref(v);
  };
  const reveal = useMemo(() => ancestors(start.root, start.stack), [start]);
  const [listed, setListed] = useState<Record<string, Listed>>({});
  const [reloads, setReloads] = useState(0);
  const [dirTick, setDirTick] = useState(0); // the folder shown changed on the computer (drill-in view)
  const [gitTick, setGitTick] = useState(0); // the folder's Git status did
  const [git, setGit] = useState<GitStatus | null>(null);
  const [open, setOpen] = useState<string[]>(file ? [file] : []); // files opened from the list, newest last
  // where it is, kept when the page leaves the session so coming back opens it here again (#5150)
  useEffect(() => notePlace({ mid, sid, cwd }, { path: dir === cwd ? undefined : dir, file: open.at(-1) }), [mid, sid, cwd, dir, open]);
  const menu = useMenu();

  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<Map<string, OpTarget>>(() => new Map());
  const [rowMenu, setRowMenu] = useState<{ x: OpTarget; at: MenuAt; open: () => void } | null>(null);
  const [dropDir, setDropDir] = useState<string | null>(null);
  const endSelect = useCallback(() => {
    setSelecting(false);
    setSelected(new Map());
  }, []);
  useEffect(() => endSelect(), [dir, tree, endSelect]); // a selection is of the folder shown
  // Changing files: what changed is listed again (the live watch would too, a moment later), and a selection is done.
  const ops = useFileOps({
    mid,
    cwd,
    onChanged: () => {
      setReloads((n) => n + 1);
      endSelect();
    },
  });
  const rowOps: RowOps = {
    selecting,
    isSelected: (p) => selected.has(p),
    toggle: (x) => {
      setSelecting(true);
      setSelected((m) => {
        const n = new Map(m);
        if (n.has(x.path)) n.delete(x.path);
        else n.set(x.path, x);
        return n;
      });
    },
    menu: (x, at, open) => setRowMenu({ x, at, open }),
  };
  useEffect(() => {
    if (!selecting) return;
    const key = (e: KeyboardEvent) => e.key === "Escape" && !document.querySelector("[role=dialog],[role=menu]") && endSelect();
    addEventListener("keydown", key);
    return () => removeEventListener("keydown", key);
  }, [selecting, endSelect]);

  useEffect(() => {
    if (!dir || tree) return;
    const ac = new AbortController();
    api<FsListing>(`/machines/${enc(mid)}/fs?path=${enc(dir)}&cwd=${enc(cwd)}`, { signal: ac.signal })
      .then((l) => setListed((m) => ({ ...m, [dir]: { at: dir, listing: l } })))
      .catch((e) => {
        if (!isAbort(e)) setListed((m) => ({ ...m, [dir]: { at: dir, listing: m[dir]?.listing, error: errorText(e) } }));
      });
    return () => ac.abort();
  }, [mid, cwd, dir, reloads, dirTick, tree]);

  useEffect(() => {
    if (!cwd) return;
    const ac = new AbortController();
    api<GitStatus>(`/machines/${enc(mid)}/git/status?cwd=${enc(cwd)}`, { signal: ac.signal })
      .then(setGit)
      .catch(() => {}); // marks are extra: without Git the list is the same
    return () => ac.abort();
  }, [mid, cwd, reloads, gitTick]);

  // The drill-in view watches the folder it shows; the tree watches the ones it has open (FilesTree). Both want Git.
  useFsLive(mid, cwd, tree || !dir ? [] : [dir], true, (c) => {
    if (c.git) setGitTick((n) => n + 1);
    if (!tree && dir && c.dirs.includes(dir)) setDirTick((n) => n + 1);
  });

  const go = useCallback((level: number) => setStack((s) => s.slice(0, level)), []);
  const enter = (e: FsEntry) => (e.dir ? setStack((s) => [...s, e.name]) : setOpen([joinPath(dir, e.name)]));

  // A path the viewer links to: a folder under the root opens in the list, anything else in the viewer.
  const openPath = (p: string) => {
    const rel = below(start.root, p);
    if (rel && listed[p]?.listing) {
      setOpen([]);
      setStack(rel);
    } else setOpen((o) => [...o, p]);
  };

  const here = listed[dir];
  const entries = (here?.listing?.entries ?? []).filter((e) => showHidden || !e.name.startsWith("."));
  const changes = git?.is_repo ? git.files.length : 0;
  const sync = aheadBehind(git);
  const viewing = open[open.length - 1];
  // where an upload goes when no folder row is meant: the folder shown (the tree's root)
  const here_dir = tree ? start.root : dir;

  return (
    <>
      <div className={cx("flex min-h-0 flex-1 flex-col", viewing && "hidden")}>
        <PanelHeader
          icon="folder"
          title={t("session-filespanel.title")}
          subtitle={shortPath(tree ? start.root : dir)}
          onClose={onClose}
          actions={
            <>
              <button
                type="button"
                title="Git"
                aria-label={(changes ? t("session-filespanel.git_changes", { count: changes }) : "Git") + (sync ? ` ${sync}` : "")}
                onClick={() => openPanel({ kind: "git", mid, sid, cwd })}
                className="flex h-7 shrink-0 items-center gap-1.5 rounded-[8px] px-2 text-[12px] font-medium whitespace-nowrap text-ink-2 transition-colors hover:bg-hover hover:text-ink"
              >
                <Icon name="git" size={14} />
                Git
                {sync && <span className="text-[11px] text-ink-3 tabular-nums">{sync}</span>}
                {changes > 0 && <span className="rounded-full bg-field px-1.5 text-[11px] text-ink-2 tabular-nums">{changes}</span>}
              </button>
              {ops.canUpload && here_dir && <IconButton icon="upload" label={t("files-ops.upload")} onClick={() => ops.choose(here_dir)} />}
              <IconButton icon="more" label={t("session-filespanel.more")} onClick={menu.open} active={Boolean(menu.anchor)} />
            </>
          }
        />
        {menu.anchor && (
          <Menu
            anchor={menu.anchor}
            onClose={menu.close}
            align="right"
            items={[
              { key: "tree", label: t("files-tree.treeView"), checked: tree, onSelect: () => chooseView(tree ? "drill" : "tree") },
              { key: "hidden", label: t("session-filespanel.show_hidden"), checked: showHidden, onSelect: () => setShowHidden(!showHidden) },
              { key: "copy", label: t("session-filespanel.copy_path"), icon: "copy", onSelect: () => copyText(tree ? start.root : dir) },
              { key: "reload", label: t("session-filespanel.reload"), icon: "retry", onSelect: () => setReloads((n) => n + 1) },
              ...(ops.canUpload && here_dir ? [{ key: "upload", label: t("files-ops.upload"), icon: "upload" as const, divider: true, onSelect: () => ops.choose(here_dir) }] : []),
              { key: "select", label: t("files-ops.select"), checked: selecting, divider: !(ops.canUpload && here_dir), onSelect: () => (selecting ? endSelect() : setSelecting(true)) },
            ]}
          />
        )}
        {rowMenu && <Menu items={ops.rowItems(rowMenu.x, rowMenu.open)} anchor={rowMenu.at.anchor ?? null} point={rowMenu.at.point} onClose={() => setRowMenu(null)} align="right" />}
        {selecting && <SelectionBar ops={ops} selected={[...selected.values()]} onClear={endSelect} />}
        {!tree && <Crumbs root={start.root} stack={stack} go={go} />}
        <div
          className="relative flex min-h-0 flex-1 flex-col"
          // files from the computer upload into the folder under the pointer (a folder row, else the one shown)
          onDragOver={(e) => {
            if (!ops.canUpload || !here_dir || !dragHasFiles(e)) return;
            e.preventDefault();
            e.dataTransfer.dropEffect = "copy";
            const over = (e.target as HTMLElement).closest<HTMLElement>("[data-drop-dir]")?.dataset.dropDir;
            setDropDir(over ?? here_dir);
          }}
          onDragLeave={(e) => !e.currentTarget.contains(e.relatedTarget as Node | null) && setDropDir(null)}
          onDrop={(e) => {
            if (!dropDir) return;
            e.preventDefault();
            ops.upload(Array.from(e.dataTransfer.files), dropDir);
            setDropDir(null);
          }}
        >
        {dropDir && <DropHint dir={dropDir} />}
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto" key={tree ? "tree" : dir}>
          {tree ? (
            start.root ? (
              <FilesTree
                mid={mid}
                sid={sid}
                cwd={cwd}
                root={start.root}
                reveal={reveal}
                showHidden={showHidden}
                git={git}
                reloads={reloads}
                onOpenFile={(p) => setOpen([p])}
                onShowHidden={() => setShowHidden(true)}
                rowOps={rowOps}
              />
            ) : (
              <Empty icon="folder" title={t("session-filespanel.no_dir")} />
            )
          ) : here?.listing ? (
            <div className="py-1" style={{ animation: "fade-in 160ms ease-out both" }}>
              {entries.length === 0 && (
                <Empty
                  icon="folder"
                  title={here.listing.entries.length ? t("session-filespanel.only_hidden") : t("session-filespanel.empty_folder")}
                  action={here.listing.entries.length ? <Button onClick={() => setShowHidden(true)}>{t("session-filespanel.show_hidden")}</Button> : undefined}
                />
              )}
              {entries.map((e) => (
                <EntryRow key={e.name} mid={mid} e={e} full={joinPath(dir, e.name)} mark={gitMark(git, joinPath(dir, e.name), e.dir)} onOpen={() => enter(e)} ops={rowOps} />
              ))}
              {here.listing.truncated && <p className="px-4 py-2 text-[11.5px] text-ink-3">{t("session-filespanel.truncated")}</p>}
            </div>
          ) : here?.error ? (
            <Empty icon="folder" title={t("session-filespanel.open_failed")} detail={here.error} action={<Button icon="retry" onClick={() => setReloads((n) => n + 1)}>{t("session-filespanel.retry")}</Button>} />
          ) : !dir ? (
            <Empty icon="folder" title={t("session-filespanel.no_dir")} />
          ) : (
            <div className="flex h-40 items-center justify-center">
              <Loading label={t("session-filespanel.reading")} />
            </div>
          )}
        </div>
        </div>
        {ops.ui}
      </div>
      {viewing && (
        <FileView
          key={viewing}
          mid={mid}
          sid={sid}
          path={viewing}
          cwd={cwd}
          onBack={() => setOpen((o) => o.slice(0, -1))}
          onClose={onClose}
          onOpenPath={openPath}
        />
      )}
    </>
  );
}

function Crumbs({ root, stack, go }: { root: string; stack: string[]; go: (level: number) => void }) {
  const bar = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    bar.current?.scrollTo({ left: bar.current.scrollWidth, behavior: scrollBehavior() });
  }, [stack.length]);
  const names = [baseName(root) || "/", ...stack];
  return (
    <div className="flex h-10 shrink-0 items-center gap-1 border-b border-line px-2">
      <IconButton icon="chevronLeft" label={t("session-filespanel.up")} onClick={() => go(stack.length - 1)} disabled={!stack.length} iconSize={16} />
      <div ref={bar} className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto scrollbar-none" aria-label={t("session-filespanel.location")}>
        {names.map((n, i) => {
          const current = i === stack.length;
          return (
            <span key={i} className="flex shrink-0 items-center gap-0.5">
              {i > 0 && <Icon name="chevronRight" size={11} stroke={2.2} className="text-ink-3" />}
              <button
                type="button"
                onClick={() => !current && go(i)}
                aria-current={current ? "location" : undefined}
                title={i === 0 ? root : undefined}
                className={cx(
                  "h-6 rounded-full px-2 text-[12px] whitespace-nowrap transition-colors",
                  current ? "bg-field font-semibold text-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
                )}
              >
                {n}
              </button>
            </span>
          );
        })}
      </div>
    </div>
  );
}

function EntryRow({ mid, e, full, mark, onOpen, ops }: { mid: string; e: FsEntry; full: string; mark: string | null; onOpen: () => void; ops?: RowOps }) {
  const hidden = e.name.startsWith(".");
  const x: OpTarget = { path: full, name: e.name, dir: e.dir };
  const picked = ops?.selecting && ops.isSelected(full);
  return (
    <div
      {...rowFrameProps(ops, x, onOpen)}
      className={cx("group/row relative flex items-center transition-colors hover:bg-hover", picked && "bg-hover")}
      // dragged into a terminal or the chat box it becomes `@path ` (lib/entry-drag.ts)
      draggable
      onDragStart={(ev) => {
        ev.dataTransfer.setData(ENTRY_DRAG_TYPE, entryPayload({ mid, path: full, dir: e.dir }));
        ev.dataTransfer.effectAllowed = "copy";
      }}
    >
      <RowCheck ops={ops} x={x} />
      <button type="button" onClick={rowClick(ops, x, onOpen)} className={cx("flex min-w-0 flex-1 items-center gap-3 py-2 pr-2 text-left", ops?.selecting ? "pl-3" : "pl-4")} title={full}>
        <FileGlyph name={e.name} dir={e.dir} />
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1">
            <span className={cx("truncate text-[13px]", e.ignored ? "text-ink-3" : hidden ? "text-ink-2" : "text-ink")}>{e.name}</span>
            {e.symlink && <Icon name="link" size={11} className="shrink-0 text-ink-3" />}
          </span>
          <span className="flex gap-2 text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums">
            {!e.dir && <span>{bytes(e.size)}</span>}
            {e.mtime && <span title={new Date(e.mtime).toLocaleString("zh-CN")}>{ago(e.mtime)}</span>}
          </span>
        </span>
        {mark && (e.dir ? <GitDot letter={mark} /> : <GitBadge letter={mark} />)}
        {e.dir && <Icon name="chevronRight" size={13} stroke={2.2} className="shrink-0 text-ink-3" />}
      </button>
      {/* hover devices only: on a touch screen it would be an invisible target at the row's end */}
      <span className="hidden items-center pr-2 opacity-0 transition-opacity group-hover/row:opacity-100 focus-within:opacity-100 [@media(hover:hover)]:flex">
        <IconButton icon="copy" label={t("session-filespanel.copy_path")} onClick={() => copyText(full)} size={26} iconSize={13} />
        <RowMore ops={ops} x={x} open={onOpen} />
      </span>
    </div>
  );
}
