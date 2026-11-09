// The file viewer (docs/apple/ios.md "File viewer"): one file or folder on a machine, read through the hub. Markdown
// rendered (or its source), code with line numbers and a little coloring, JSON indented, images, PDFs, folders.
// Text files come a page (2 MB) at a time; only the lines on screen are rendered, so big files stay smooth.
import { useCallback, useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState } from "react";
import { api, enc, errorText, fileDownloadURL, fileThumbURL, isAbort } from "../../../lib/api";
import { delimitedSheet, type Sheet } from "../../../lib/sheets";
import type { SheetReply, SheetRequest } from "../../../lib/sheets.worker";
import {
  base64Bytes,
  columns,
  dirName,
  extendStates,
  extOf,
  fileName,
  isMachineRef,
  joinPath,
  languageOf,
  lexLine,
  markdownChunks,
  prettyJSON,
  resolveRef,
  simplifyHTML,
  splitLines,
  viewKind,
  type FileContent,
  type Lang,
  type LexState,
  type Token,
} from "../../../lib/files";
import { addCodeComment, commentKey, markedLines } from "../../../lib/comment-target";
import { codeQuote, rangeLabel, relativeTo } from "../../../lib/comments";
import { useQuotes } from "../../../lib/drafts";
import { bytes, shortPath } from "../../../lib/format";
import { Button, copyText, cx, IconButton, Loading, toast } from "../../../ui/atoms";
import { Icon } from "../../../ui/icons";
import { Markdown } from "../../../ui/Markdown";
import { ImageViewer, type ViewerImage } from "../../chat/ImageViewer";
import { PanelHeader } from "../../../ui/PanelHeader";
import { fileBytes, TOKEN_CLASS } from "./file-data";
import { GutterNumber, LineBox, useLinePick, type LinePick } from "./LineComment";
import { BackHeader, Empty, FileGlyph, Segmented, useWindow } from "./parts";
import { parseNotebook } from "../../../lib/notebook";
import { HtmlPreview } from "./HtmlPreview";
import { MediaPlayer } from "../../../ui/Media";
import { NotebookView } from "./NotebookView";
import { OfficeView } from "./OfficeView";
import { SheetView } from "./SheetView";
import { formatNumber, t } from "../../../i18n";

const LH = 20; // px per code line
const PAD = 8; // px above the first line
const MAX_LINE = 4000; // longer lines (minified JS / JSON) are cut for display
const MAX_JSON = 24 << 20; // JSON is read whole up to this, then indented
const COLOR_LINES = 100_000;
const COLOR_BYTES = 8 << 20;
const PRETTY_BYTES = 8 << 20; // bigger JSON shows as read, not re-indented
const MAX_TABLE_TEXT = 64 << 20; // a CSV is read whole up to this, then parsed
const MAX_NOTEBOOK = 64 << 20; // a notebook is read whole up to this (its outputs' pictures are inside)
const MAX_HTML = 16 << 20; // an HTML page is read whole up to this to preview
const MAX_TABLE_BYTES = 200 << 20; // a workbook bigger than this is not opened in the browser

// stream=1: a binary file comes described, its bytes from file/download by Range (docs/protocol/protocol.md "File preview"); an
// older hub ignores it and sends base64 as before.
export function fileURL(mid: string, path: string, cwd?: string, offset = 0) {
  return `/machines/${enc(mid)}/file?path=${enc(path)}&cwd=${enc(cwd ?? "")}${offset > 0 ? `&offset=${offset}` : ""}&stream=1`;
}

/** Where the browser can show a binary file itself (an image, a PDF): file/download inline, or the old base64. */
function inlineSrc(mid: string, f: FileContent, cwd?: string): string {
  if (typeof f.text === "string") return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(f.text)}`; // an SVG, read as text
  return f.stream ? fileDownloadURL(mid, f.path, cwd, { inline: true }) : `data:${f.mime};base64,${f.data ?? ""}`;
}

/** Parses a table file in a worker: a workbook's bytes, or delimited text (small ones right here). */
function parseSheets(req: SheetRequest, signal: AbortSignal): Promise<Sheet[]> {
  if ("text" in req && req.text.length < 256 << 10) return Promise.resolve([delimitedSheet(req.name, req.text)]);
  return new Promise((resolve, reject) => {
    const w = new Worker(new URL("../../../lib/sheets.worker.ts", import.meta.url), { type: "module" });
    const done = () => w.terminate();
    signal.addEventListener("abort", () => {
      done();
      reject(new DOMException("aborted", "AbortError"));
    });
    w.onmessage = (e: MessageEvent<SheetReply>) => {
      done();
      if ("error" in e.data) reject(new Error(e.data.error));
      else resolve(e.data.sheets);
    };
    w.onerror = (e) => {
      done();
      reject(new Error(e.message || t("session-files-fileview.sheet_parse_failed")));
    };
    w.postMessage(req, "bytes" in req ? [req.bytes] : []);
  });
}

// ── loading ───────────────────────────────────────────────

export interface FileDoc {
  file: FileContent | null;
  error: string;
  /** A later page proved that the file changed. Only reload can resume this read. */
  changed: boolean;
  /** A retryable continuation failure; already accepted pages remain available. */
  pageError: string;
  text: string;
  /** Where the next page starts; 0: no further page is available. */
  next: number;
  loadingMore: boolean;
  more: () => Promise<boolean>;
  /** A bounded preview: a cap may return a prefix explicitly marked incomplete. Other failures reject. */
  read: (cap: number) => Promise<{ text: string; complete: boolean }>;
  /** Returns complete text, or rejects on change, failure, retirement or a read cap. */
  all: (cap?: number) => Promise<string>;
  reload: () => void;
}

export function useFileDoc(mid: string, path: string, cwd?: string): FileDoc {
  const [, bump] = useReducer((x: number) => x + 1, 0);
  const [reloads, setReloads] = useState(0);
  // Each identity/reload owns its pages and callbacks. An old all()/more() must never borrow a new document.
  const s = useMemo(() => ({
    mid, path, cwd, reloads,
    gen: 0, active: true, file: null as FileContent | null, pages: [] as string[], next: 0,
    pending: null as Promise<boolean> | null, complete: false, revision: "", changed: false, error: "", pageError: "",
  }), [mid, path, cwd, reloads]);

  useEffect(() => {
    const gen = ++s.gen;
    s.active = true;
    const ac = new AbortController();
    api<FileContent>(fileURL(s.mid, s.path, s.cwd), { signal: ac.signal })
      .then((f) => {
        if (!s.active || gen !== s.gen) return;
        // Go omits an empty Text field. Only complete zero-byte text metadata can stand in for that missing field.
        const emptyText = f.text === undefined && f.size === 0 && !f.offset && !f.next && !f.truncated && !f.is_dir && !f.stream && f.data === undefined
          && (f.mime?.startsWith("text/") || /^application\/(?:[\w.-]+\+)?json(?:;|$)/.test(f.mime ?? ""));
        const file = emptyText ? { ...f, text: "" } : f;
        Object.assign(s, {
          file, pages: typeof file.text === "string" ? [file.text] : [], next: f.next ?? 0,
          revision: f.revision || "", complete: !f.next && !f.truncated,
        });
        bump();
      })
      .catch((e) => {
        if (!isAbort(e) && s.active && gen === s.gen) {
          s.error = errorText(e);
          bump();
        }
      });
    return () => { s.active = false; ++s.gen; ac.abort(); };
  }, [s]);

  const more = useCallback(async () => {
    if (!s.active || !s.file || s.changed || s.next <= 0 || s.pending) return false;
    const gen = s.gen;
    const offset = s.next;
    s.pageError = "";
    s.pending = (async () => {
      try {
        const f = await api<FileContent>(fileURL(s.mid, s.path, s.cwd, offset));
        if (!s.active || gen !== s.gen) return false;
        // Compare every known revision, including across legacy pages without one. Missing metadata is not proof
        // of equality: accept it for compatibility, but do not label the combined text with a verified revision.
        if (s.revision && f.revision && s.revision !== f.revision) {
          s.changed = true;
          return false;
        }
        const next = f.next ?? 0;
        if (typeof f.text !== "string" || (next > 0 && next <= offset) || (!next && f.truncated)) {
          throw new Error(t("session-files-fileview.read_incomplete"));
        }
        s.revision ||= f.revision || "";
        if (!f.revision && s.file?.revision) s.file = { ...s.file, revision: undefined };
        s.pages = [...s.pages, f.text];
        s.next = next;
        s.complete = next <= 0;
        return true;
      } catch (e) {
        if (s.active && gen === s.gen) s.pageError = errorText(e);
        return false;
      } finally {
        if (s.active && gen === s.gen) {
          s.pending = null;
          bump();
        }
      }
    })();
    bump();
    return s.pending;
  }, [s]);

  const read = useCallback(async (cap: number) => {
    const gen = s.gen;
    const check = () => {
      if (!s.active || gen !== s.gen) throw new DOMException("aborted", "AbortError");
      if (s.changed) throw new Error(t("session-files-fileview.file_changed_detail"));
    };
    check();
    if (typeof s.file?.text !== "string") throw new Error(t("session-files-fileview.read_incomplete"));
    while (s.next > 0 && s.next < cap) {
      const accepted = await (s.pending ?? more()); // share the admitted page, including its failure
      check();
      if (!accepted) throw new Error(s.pageError || t("session-files-fileview.read_incomplete"));
    }
    check();
    if (!s.complete && s.next <= 0) throw new Error(t("session-files-fileview.read_incomplete"));
    return { text: s.pages.join(""), complete: s.complete };
  }, [more, s]);

  const all = useCallback(async (cap = 64 << 20) => {
    const gen = s.gen;
    const result = await read(cap);
    if (!s.active || gen !== s.gen) throw new DOMException("aborted", "AbortError");
    if (!result.complete) throw new Error(t("session-files-fileview.read_incomplete"));
    return result.text;
  }, [read, s]);

  const reload = useCallback(() => {
    s.active = false; // retire even a response delivered before React commits the reload
    ++s.gen;
    setReloads((n) => n + 1);
  }, [s]);
  const text = useMemo(() => s.pages.join(""), [s.pages]);
  return { file: s.file, error: s.error, changed: s.changed, pageError: s.pageError, text, next: s.next, loadingMore: !!s.pending, more, read, all, reload };
}

// ── the viewer ────────────────────────────────────────────

type Mode = "rendered" | "source";

/**
 * One file (or folder) with its header: back (in a stack) or the file's icon, the name and path, the Markdown / JSON
 * switch, copy path, download. `onOpenPath` opens another path it links to (a folder's entry, a Markdown link).
 */
export function FileView({
  mid,
  sid,
  path,
  cwd,
  line,
  lineTo,
  onBack,
  onClose,
  onOpenPath,
}: {
  mid: string;
  /** The session whose message box takes the comments made on this file's lines (else the box last used). */
  sid?: string;
  path: string;
  cwd?: string;
  line?: number;
  /** The last line of the range marked, when it is one. */
  lineTo?: number;
  onBack?: () => void;
  onClose: () => void;
  onOpenPath: (path: string) => void;
}) {
  const doc = useFileDoc(mid, path, cwd);
  const f = doc.file;
  const kind = f ? viewKind(f) : null;
  const [mode, setMode] = useState<Mode>(line ? "source" : "rendered");
  const [saving, setSaving] = useState(false);

  // JSON is indented from the whole text: read the rest first (as the app does), up to a cap.
  const cap = kind === "notebook" ? MAX_NOTEBOOK : kind === "html" ? MAX_HTML : MAX_JSON;
  const wantAll = (kind === "json" || kind === "notebook" || kind === "html") && doc.next > 0 && (f?.size ?? 0) <= cap;
  const { all } = doc;
  useEffect(() => {
    if (wantAll) void all(cap).catch(() => {}); // the document exposes change/failure recovery
  }, [wantAll, all, cap]);
  const notebook = useMemo(() => {
    if (kind !== "notebook" || doc.next > 0) return null;
    try {
      return { nb: parseNotebook(doc.text), error: "" };
    } catch (e) {
      return { nb: null, error: e instanceof Error ? e.message : String(e) };
    }
  }, [kind, doc.next, doc.text]);
  const json = useMemo(() => {
    if (kind !== "json" || doc.next > 0) return null;
    if (doc.text.length > PRETTY_BYTES) return { pretty: null, error: "", big: true };
    const pretty = prettyJSON(doc.text);
    if (pretty !== null) return { pretty, error: "" };
    try {
      JSON.parse(doc.text);
      return { pretty: null, error: "" };
    } catch (e) {
      return { pretty: null, error: e instanceof Error ? e.message : String(e) };
    }
  }, [kind, doc.next, doc.text]);

  const download = async () => {
    if (!f || doc.changed) return;
    // A paged text file goes through file/download too: the pages read here may stop at a cap or a failed page, which
    // would save a truncated copy. Only a node that sends `revision` with stream=1 is known to serve file.chunk (both
    // are newer than paging); an older one would answer 409 node_outdated, so it keeps the Blob of the pages read.
    const pagedWhole = typeof f.data !== "string" && doc.next > 0 && !!f.revision;
    if (f.stream || pagedWhole) {
      // the hub sends it as an attachment, by Range: the browser saves it without holding it here
      const a = document.createElement("a");
      a.href = fileDownloadURL(mid, f.path, cwd);
      a.download = f.name || fileName(path);
      document.body.appendChild(a);
      a.click();
      a.remove();
      return;
    }
    setSaving(true);
    try {
      const blob =
        typeof f.data === "string"
          ? new Blob([base64Bytes(f.data)], { type: f.mime || "application/octet-stream" })
          : new Blob([await doc.all()], { type: `${f.mime || "text/plain"};charset=utf-8` });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = f.name || fileName(path);
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 30_000);
    } catch (e) {
      if (!isAbort(e)) toast(t("session-files-fileview.download_failed", { error: errorText(e) }), "error");
    } finally {
      setSaving(false);
    }
  };

  const name = f?.name || fileName(path);
  const full = f?.path || path;
  const where = kind === "directory" ? shortPath(full) : shortPath(dirName(full)) || full; // the name is the title
  const actions = (
    <>
      {kind === "markdown" && <Segmented label={t("session-files-fileview.view_mode")} value={mode} onChange={setMode} options={[["rendered", t("session-files-fileview.mode_preview")], ["source", t("session-files-fileview.mode_source")]]} />}
      {kind === "json" && json?.pretty != null && <Segmented label={t("session-files-fileview.view_mode")} value={mode} onChange={setMode} options={[["rendered", t("session-files-fileview.mode_formatted")], ["source", t("session-files-fileview.mode_raw")]]} />}
      {kind === "image" && typeof f?.text === "string" && <Segmented label={t("session-files-fileview.view_mode")} value={mode} onChange={setMode} options={[["rendered", t("session-files-fileview.mode_image")], ["source", t("session-files-fileview.mode_source")]]} />}
      {kind === "html" && <Segmented label={t("session-files-fileview.view_mode")} value={mode} onChange={setMode} options={[["rendered", t("session-files-fileview.mode_preview")], ["source", t("session-files-fileview.mode_source")]]} />}
      {kind === "notebook" && notebook?.nb && <Segmented label={t("session-files-fileview.view_mode")} value={mode} onChange={setMode} options={[["rendered", t("session-files-fileview.mode_notebook")], ["source", t("session-files-fileview.mode_source_file")]]} />}
      {kind === "sheet" && typeof f?.text === "string" && <Segmented label={t("session-files-fileview.view_mode")} value={mode} onChange={setMode} options={[["rendered", t("session-files-fileview.mode_sheet")], ["source", t("session-files-fileview.mode_source_file")]]} />}
      <IconButton icon="copy" label={t("session-files-fileview.copy_path")} onClick={() => copyText(f?.path || path)} />
      {kind !== "directory" && <IconButton icon="arrowDown" label={saving ? t("session-files-fileview.downloading") : t("session-files-fileview.download")} onClick={download} disabled={!f || saving || doc.changed} />}
    </>
  );

  let body: React.ReactNode;
  if (doc.changed) {
    body = <Empty icon="info" title={t("session-files-fileview.file_changed")} detail={t("session-files-fileview.file_changed_detail")}
      action={<Button icon="retry" onClick={doc.reload}>{t("session-files-fileview.reload")}</Button>} />;
  } else if (doc.error) {
    body = (
      <Empty
        icon="info"
        title={t("session-files-fileview.open_failed")}
        detail={doc.error}
        action={
          <Button icon="retry" onClick={doc.reload}>
            {t("session-files-fileview.retry")}
          </Button>
        }
      />
    );
  } else if (!f || !kind) {
    body = (
      <div className="flex flex-1 items-center justify-center">
        <Loading label={t("session-files-fileview.reading")} />
      </div>
    );
  } else if (kind === "directory") {
    body = <FolderList file={f} onOpen={onOpenPath} />;
  } else if (kind === "image" && !(typeof f.text === "string" && mode === "source")) {
    body = <ImageView file={f} src={inlineSrc(mid, f, cwd)} fallback={/^image\/hei[cf]/.test(f.mime ?? "") || /\.(heic|heif)$/i.test(f.name) ? fileThumbURL(mid, f.path, cwd, 1024) : undefined} />;
  } else if (kind === "video" || kind === "audio") {
    body = <MediaView mid={mid} cwd={cwd} file={f} kind={kind} />;
  } else if (kind === "pdf") {
    body = <PdfView file={f} src={f.stream ? inlineSrc(mid, f, cwd) : ""} />;
  } else if (kind === "html" && mode === "rendered" && !(doc.next > 0 && !wantAll)) {
    body =
      doc.next > 0 ? (
        <div className="flex flex-1 items-center justify-center">
          <Loading label={t("session-files-fileview.reading_size", { size: bytes(f.size) })} />
        </div>
      ) : (
        <HtmlPreview html={doc.text} title={f.name} mid={mid} path={f.path} />
      );
  } else if (kind === "notebook" && mode === "rendered" && !notebook?.error && !(doc.next > 0 && !wantAll)) {
    body = notebook?.nb ? (
      <NotebookView nb={notebook.nb} />
    ) : (
      <div className="flex flex-1 items-center justify-center">
        <Loading label={t("session-files-fileview.reading_notebook", { size: bytes(f.size) })} />
      </div>
    );
  } else if (kind === "office") {
    body = <OfficeView mid={mid} cwd={cwd} file={f} onDownload={download} />;
  } else if (kind === "sheet" && mode === "rendered") {
    body = <SheetLoader mid={mid} cwd={cwd} doc={doc} file={f} onDownload={download} />;
  } else if (kind === "binary") {
    body = (
      <Empty
        icon="read"
        title={t("session-files-fileview.no_preview")}
        detail={`${f.mime || t("session-files-fileview.binary_file")} · ${bytes(f.size)}`}
        action={
          <Button icon="arrowDown" onClick={download} busy={saving}>
            {t("session-files-fileview.download")}
          </Button>
        }
      />
    );
  } else if (kind === "markdown" && mode === "rendered") {
    body = <MarkdownView doc={doc} file={f} mid={mid} onOpenPath={onOpenPath} />;
  } else {
    const shown = kind === "json" && mode === "rendered" && json?.pretty != null ? json.pretty : doc.text;
    const note =
      kind === "html" && doc.next > 0 && !wantAll
        ? t("session-files-fileview.page_too_big")
        : kind === "notebook" && notebook?.error
        ? t("session-files-fileview.notebook_error", { error: notebook.error })
        : kind === "notebook" && doc.next > 0 && !wantAll
          ? t("session-files-fileview.notebook_too_big")
          : kind === "json" && wantAll
        ? t("session-files-fileview.reading_whole", { read: bytes(doc.next), size: bytes(f.size) })
        : kind === "json" && doc.next > 0
          ? t("session-files-fileview.too_big_text")
          : json && "big" in json
            ? t("session-files-fileview.too_big_unformatted")
            : json?.error
              ? t("session-files-fileview.invalid_json", { error: json.error })
              : "";
    body = <CodeView doc={doc} file={f} text={shown} line={line} lineTo={lineTo} note={note} where={{ mid, sid, cwd }} />;
  }

  return (
    <>
      {onBack ? (
        <BackHeader title={name} subtitle={where} actions={actions} onBack={onBack} onClose={onClose} />
      ) : (
        <PanelHeader icon={kind === "directory" ? "folder" : kind === "image" ? "image" : "read"} title={name} subtitle={where} actions={actions} onClose={onClose} />
      )}
      <div className="flex min-h-0 flex-1 flex-col" key={path}>
        {doc.pageError && !doc.changed && (kind !== "sheet" || mode === "source") && <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-line px-3 py-2 text-xs text-ink-2">
          <span className="min-w-0 flex-1">{t("session-files-fileview.more_failed", { error: doc.pageError })}</span>
          <Button icon="retry" busy={doc.loadingMore} onClick={() => { if (wantAll) void all(cap).catch(() => {}); else void doc.more(); }}>{t("session-files-fileview.retry")}</Button>
        </div>}
        {body}
      </div>
    </>
  );
}

// ── code ──────────────────────────────────────────────────

function CodeView({ doc, file, text, line, lineTo, note, where }: { doc: FileDoc; file: FileContent; text: string; line?: number; lineTo?: number; note: string; where: { mid: string; sid?: string; cwd?: string } }) {
  const name = file.name;
  const lines = useMemo(() => splitLines(text), [text]);
  const lang = useMemo<Lang | null>(() => (text.length <= COLOR_BYTES && lines.length <= COLOR_LINES ? languageOf(name, lines[0]) : null), [name, text.length, lines]);
  const plainBecauseBig = !lang && (text.length > COLOR_BYTES || lines.length > COLOR_LINES) && languageOf(name, lines[0]) !== null;
  // The lexer's state at each line start, filled as far as the window has gone (a page appended keeps the lines before).
  const states = useMemo<LexState[]>(() => [], [lang, text]); // new text or language: start over
  const width = useMemo(() => lines.reduce((m, l) => Math.max(m, l.length > MAX_LINE ? MAX_LINE + 1 : columns(l)), 0), [lines]);
  const digits = String(Math.max(lines.length, 9)).length;
  const scroller = useRef<HTMLDivElement>(null);
  const [start, end] = useWindow(scroller, lines.length, LH, PAD);
  if (lang) extendStates(lines, lang, states, end);

  // Open at a line: read on until it is there, then put it a third of the way down and mark it.
  const placed = useRef(false);
  const { more, next, loadingMore, pageError } = doc;
  useEffect(() => {
    if (!line || placed.current) return;
    if (line > lines.length && next > 0 && !loadingMore && !pageError) void more();
  }, [line, lines.length, next, loadingMore, pageError, more]);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!line || placed.current || !el || line > lines.length) return;
    placed.current = true;
    el.scrollTop = Math.max(0, PAD + (line - 1) * LH - el.clientHeight / 3);
  }, [line, lines.length]);

  // comments on lines (LineComment.tsx): the + in the gutter, the box under the code, the cards' numbers beside their lines
  const key = commentKey(where.mid, where.sid);
  const pick = useLinePick(Boolean(key));
  const quotes = useQuotes(key ?? "").list;
  const marked = useMemo(() => markedLines(quotes, (s) => !s.diff && s.path === file.path), [quotes, file.path]);
  const picked = pick.sweeping ?? pick.range;

  const notes = [note, file.truncated && !file.next ? t("session-files-fileview.truncated_start") : "", plainBecauseBig ? t("session-files-fileview.too_big_uncolored") : ""].filter(Boolean);
  const rows: React.ReactNode[] = [];
  for (let i = start; i < end; i++) {
    const raw = lines[i];
    const cut = raw.length > MAX_LINE;
    const shown = cut ? raw.slice(0, MAX_LINE) : raw;
    const tokens = lang ? lexLine(shown, lang, states[i] ?? null).tokens : [];
    const n = i + 1;
    rows.push(
      <CodeRow
        key={i}
        n={n}
        digits={digits}
        text={shown}
        tokens={tokens}
        cut={cut}
        mark={line !== undefined && n >= line && n <= (lineTo ?? line)}
        picked={Boolean(picked && n >= picked.from && n <= picked.to)}
        gutter={key ? { pick, marked: marked.get(n) } : undefined}
      />,
    );
  }

  return (
    <>
      <div className="flex min-h-8 shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-b border-line px-3 py-1.5 text-[11.5px] text-ink-3">
        <span className="whitespace-nowrap tabular-nums">{t("session-files-fileview.lines", { count: lines.length, n: formatNumber(lines.length) })}</span>
        <span>·</span>
        <span className="whitespace-nowrap">{bytes(file.size)}</span>
        {lang?.id && (
          <>
            <span>·</span>
            <span className="whitespace-nowrap">{lang.id}</span>
          </>
        )}
        {notes.map((n) => (
          <span key={n} className="min-w-0 text-orange">
            {n}
          </span>
        ))}
      </div>
      {lines.length === 0 ? (
        <Empty icon="read" title={t("session-files-fileview.empty_file")} />
      ) : (
        <div ref={scroller} className="min-h-0 flex-1 overflow-auto bg-inset font-mono text-[12px]" style={{ lineHeight: `${LH}px` }} data-testid="file-code">
          <div style={{ minWidth: `calc(${digits}ch + ${width}ch + 25px + 36px)`, paddingTop: PAD, paddingBottom: PAD }}>
            <div style={{ height: start * LH }} />
            {rows}
            <div style={{ height: (lines.length - end) * LH }} />
            {doc.next > 0 && <MoreButton doc={doc} file={file} />}
          </div>
        </div>
      )}
      {key && pick.range && (
        <LineBox
          where={`${relativeTo(where.cwd, file.path)}:${rangeLabel(pick.range.from, pick.range.to)}`}
          onCancel={pick.clear}
          onAdd={(body) => {
            const r = pick.range!;
            const quote = codeQuote({ file: relativeTo(where.cwd, file.path), path: file.path, from: r.from, to: r.to, lines: lines.slice(r.from - 1, r.to), lang: extOf(file.name), mid: where.mid, sid: where.sid, cwd: where.cwd });
            addCodeComment(key, quote, body);
            pick.clear();
          }}
        />
      )}
    </>
  );
}

const NO_PICK: LinePick = { range: null, sweeping: null, down: () => {}, enter: () => {}, clear: () => {} };

function CodeRow({ n, digits, text, tokens, cut, mark, picked, gutter }: { n: number; digits: number; text: string; tokens: Token[]; cut: boolean; mark: boolean; picked: boolean; gutter?: { pick: LinePick; marked?: number } }) {
  const parts: React.ReactNode[] = [];
  let at = 0;
  for (const t of tokens) {
    if (t.from > at) parts.push(text.slice(at, t.from));
    parts.push(
      <span key={t.from} className={TOKEN_CLASS[t.kind]}>
        {text.slice(t.from, t.to)}
      </span>,
    );
    at = t.to;
  }
  if (at < text.length) parts.push(text.slice(at));
  return (
    <div className={cx("group/row flex", (mark || picked) && "bg-accent-tint")} style={{ height: LH }} data-line={n}>
      <GutterNumber
        n={n}
        pick={gutter?.pick ?? NO_PICK}
        marked={gutter?.marked}
        enabled={Boolean(gutter)}
        className={cx("sticky left-0 shrink-0 border-r border-line pr-3 pl-3 text-right tabular-nums select-none", mark || picked ? "font-semibold text-accent-ink" : "bg-inset text-ink-3")}
        style={{ width: `calc(${digits}ch + 25px)`, ...(mark || picked ? { background: "linear-gradient(var(--accent-tint), var(--accent-tint)), var(--inset)" } : {}) }}
      />
      <span className="pr-6 pl-3 whitespace-pre text-ink">
        {parts}
        {cut && <span className="text-ink-3 select-none">{t("session-files-fileview.line_cut", { max: MAX_LINE })}</span>}
      </span>
    </div>
  );
}

function MoreButton({ doc, file }: { doc: FileDoc; file: FileContent }) {
  return (
    <div className="sticky left-0 flex w-full max-w-[100vw] items-center gap-3 px-3 pt-3 pb-2 font-sans">
      <Button icon="arrowDown" onClick={() => void doc.more()} busy={doc.loadingMore}>
        {t("session-files-fileview.load_more")}
      </Button>
      <span className="text-[11.5px] whitespace-nowrap text-ink-3">
        {t("session-files-fileview.read_progress", { read: bytes(doc.next), size: bytes(file.size) })}
      </span>
    </div>
  );
}

// ── Markdown ──────────────────────────────────────────────

const images = new Map<string, Promise<string | null>>();

/** An image a Markdown file refers to, read from the same machine (cached for the page's life). */
function machineImage(mid: string, path: string): Promise<string | null> {
  const key = `${mid}\u0000${path}`;
  let p = images.get(key);
  if (!p) {
    if (images.size > 64) images.clear();
    p = api<FileContent>(fileURL(mid, path))
      .then((f) => (typeof f.data === "string" && f.mime?.startsWith("image/") ? `data:${f.mime};base64,${f.data}` : null))
      .catch(() => null);
    images.set(key, p);
  }
  return p;
}

function MarkdownView({ doc, file, mid, onOpenPath }: { doc: FileDoc; file: FileContent; mid: string; onOpenPath: (p: string) => void }) {
  const chunks = useMemo(() => markdownChunks(simplifyHTML(doc.text)), [doc.text]);
  const box = useRef<HTMLDivElement>(null);
  const base = dirName(file.path);

  // Relative images point at the machine, not at this page: swap them for the bytes read from there before paint.
  useLayoutEffect(() => {
    for (const img of box.current?.querySelectorAll("img") ?? []) {
      const src = img.getAttribute("src");
      if (!src || img.dataset.machine || !isMachineRef(src)) continue;
      const p = resolveRef(base, src);
      img.dataset.machine = p;
      img.removeAttribute("src");
      img.title = p;
      void machineImage(mid, p).then((url) => {
        if (url && img.dataset.machine === p) img.src = url;
      });
    }
  }, [chunks, mid, base]);

  // Links to files next to it open in the viewer; in-page anchors stay put.
  const click = (e: React.MouseEvent) => {
    if (e.defaultPrevented) return; // the link already went where it belongs (lib/links.ts: a document, a conversation)
    const a = (e.target as HTMLElement).closest("a");
    const href = a?.getAttribute("href");
    if (!href) return;
    if (href.startsWith("#")) e.preventDefault();
    else if (isMachineRef(href)) {
      e.preventDefault();
      onOpenPath(resolveRef(base, href));
    }
  };

  return (
    <div className="min-h-0 flex-1 overflow-y-auto" onClick={click} data-testid="file-markdown">
      <div ref={box} className="mx-auto flex max-w-[760px] flex-col gap-4 px-5 py-5 sm:px-7">
        {chunks.map((c, i) => (
          <div key={i} style={{ contentVisibility: "auto", containIntrinsicSize: "auto 480px" }}>
            <Markdown text={c} />
          </div>
        ))}
        {doc.next > 0 && (
          <div className="flex items-center gap-3 pt-2">
            <Button icon="arrowDown" onClick={() => void doc.more()} busy={doc.loadingMore}>
              {t("session-files-fileview.load_more")}
            </Button>
            <span className="text-[11.5px] whitespace-nowrap text-ink-3">
              {t("session-files-fileview.read_progress", { read: bytes(doc.next), size: bytes(file.size) })}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}

// ── tables ────────────────────────────────────────────────

/** Reads a bounded CSV/TSV preview or a workbook's whole bytes and parses it off the main thread. */
function SheetLoader({ mid, cwd, doc, file, onDownload }: { mid: string; cwd?: string; doc: FileDoc; file: FileContent; onDownload: () => void }) {
  const [state, setState] = useState<{ sheets?: Sheet[]; error?: string; note?: string }>({});
  const [attempt, retry] = useReducer((n: number) => n + 1, 0);
  const { read } = doc;
  useEffect(() => {
    const ac = new AbortController();
    setState({});
    (async () => {
      let req: SheetRequest;
      let note = "";
      if (typeof file.text === "string") {
        const { text, complete } = await read(MAX_TABLE_TEXT);
        if (ac.signal.aborted) return;
        if (!complete) note = t("session-files-fileview.sheet_partial", { size: bytes(text.length) });
        req = { name: file.name, text };
      } else {
        if (file.size > MAX_TABLE_BYTES) throw new Error(t("session-files-fileview.sheet_too_large", { size: bytes(file.size) }));
        req = { name: file.name, bytes: await fileBytes(mid, file, cwd, ac.signal) };
      }
      const sheets = await parseSheets(req, ac.signal);
      if (!ac.signal.aborted) setState({ sheets, note });
    })().catch((e) => {
      if (!isAbort(e) && !ac.signal.aborted) setState({ error: errorText(e) });
    });
    return () => ac.abort();
  }, [mid, cwd, file, read, attempt]);
  // a Numbers file SheetJS cannot read still has the preview Numbers wrote into it
  if (state.error && extOf(file.name) === "numbers") return <OfficeView mid={mid} cwd={cwd} file={file} onDownload={onDownload} />;
  if (state.error) return <Empty icon="info" title={t("session-files-fileview.sheet_open_failed")} detail={state.error}
    action={<Button icon="retry" onClick={retry}>{t("session-files-fileview.retry")}</Button>} />;
  if (!state.sheets)
    return (
      <div className="flex flex-1 items-center justify-center">
        <Loading label={typeof file.text === "string" ? t("session-files-fileview.reading_sheet") : t("session-files-fileview.reading_sheet_size", { size: bytes(file.size) })} />
      </div>
    );
  if (!state.sheets.length) return <Empty icon="info" title={t("session-files-fileview.no_sheets")} />;
  return <SheetView sheets={state.sheets} note={state.note} />;
}

// ── images, PDFs, folders ─────────────────────────────────

/**
 * An image; `fallback` is what to show when this browser cannot draw it (HEIC outside Safari): the picture the machine
 * draws for its thumbnail.
 */
export function ImageView({ file, src: original, fallback }: { file: FileContent; src: string; fallback?: string }) {
  const [dims, setDims] = useState<[number, number] | null>(null);
  const [big, setBig] = useState(false);
  const [stage, setStage] = useState<"original" | "fallback" | "failed">("original");
  const src = stage === "fallback" && fallback ? fallback : original;
  const failed = () => setStage((s) => (s === "original" && fallback ? "fallback" : "failed"));
  if (stage === "failed")
    return <Empty icon="image" title={t("session-files-fileview.image_failed")} detail={t("session-files-fileview.image_failed_detail", { type: file.mime || t("session-files-fileview.image"), size: bytes(file.size) })} />;
  // A tiny image (an icon) is drawn bigger, pixel for pixel, so it can be seen.
  const scale = dims ? Math.max(1, Math.min(8, Math.floor(160 / Math.max(dims[0], dims[1])))) : 1;
  return (
    <>
      <div className="flex min-h-8 shrink-0 items-center gap-2 border-b border-line px-3 py-1.5 text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums">
        {dims && (
          <>
            <span>
              {dims[0]} × {dims[1]}
            </span>
            <span>·</span>
          </>
        )}
        <span>{bytes(file.size)}</span>
        <span>·</span>
        <span>{file.mime}</span>
        {stage === "fallback" && <span className="truncate">· {t("session-files-fileview.image_fallback")}</span>}
        {scale > 1 && <span className="ml-auto">{t("session-files-fileview.zoomed", { scale })}</span>}
      </div>
      <div
        className="flex min-h-0 flex-1 items-center justify-center overflow-auto p-6"
        style={{ backgroundImage: "conic-gradient(var(--field) 25%, var(--inset) 0 50%, var(--field) 0 75%, var(--inset) 0)", backgroundSize: "20px 20px" }}
      >
        <button type="button" onClick={() => setBig(true)} aria-label={t("session-files-fileview.view_large")} className="max-h-full max-w-full cursor-zoom-in">
          <img
            src={src}
            alt={file.name}
            onLoad={(e) => setDims([e.currentTarget.naturalWidth, e.currentTarget.naturalHeight])}
            onError={failed}
            className="block max-h-full max-w-full shadow-hairline"
            style={dims && scale > 1 ? { width: dims[0] * scale, imageRendering: "pixelated" } : undefined}
          />
        </button>
      </div>
      {big && <ImageViewer images={[viewerImage(file, src, stage === "fallback")]} start={0} onClose={() => setBig(false)} />}
    </>
  );
}

/**
 * What the viewer shows for an image file. On the fallback the picture is the PNG the machine drew (file/thumb), so "Download"
 * saves it as a .png with that type; the caption on top stays the file's own name.
 */
export function viewerImage(file: FileContent, src: string, fallback: boolean): ViewerImage {
  if (!fallback) return { id: file.path, src, mime: file.mime, label: file.name, name: file.name };
  return { id: file.path, src, mime: "image/png", label: file.name, name: `${file.name.replace(/\.[^.]+$/, "") || file.name}.png` };
}

/** A video or an audio file, playing from file/download by Range (or the old base64 when it is all there). */
function MediaView({ mid, cwd, file, kind }: { mid: string; cwd?: string; file: FileContent; kind: "video" | "audio" }) {
  if (!file.stream && file.truncated)
    return <Empty icon="info" title={t("session-files-fileview.hub_too_old_media")} detail={t("session-files-fileview.hub_too_old_media_detail", { size: bytes(file.size) })} />;
  return (
    <>
      <div className="flex min-h-8 shrink-0 items-center gap-2 border-b border-line px-3 py-1.5 text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums">
        <span>{bytes(file.size)}</span>
        <span>·</span>
        <span>{file.mime}</span>
      </div>
      <div className={cx("flex min-h-0 flex-1 items-center justify-center overflow-auto p-6", kind === "video" ? "bg-black/90" : "bg-inset")}>
        {kind === "video" ? <MediaPlayer mid={mid} path={file.path} cwd={cwd} kind="video" className="max-h-full max-w-full" /> : (
          <div className="flex w-full max-w-[560px] flex-col items-center gap-4 rounded-[16px] bg-surface p-6 shadow-hairline">
            <span className="flex size-16 items-center justify-center rounded-full bg-accent-tint text-accent-ink">
              <Icon name="play" size={26} />
            </span>
            <div className="max-w-full truncate text-[14px] font-medium text-ink">{file.name}</div>
            <MediaPlayer mid={mid} path={file.path} cwd={cwd} kind="audio" />
          </div>
        )}
      </div>
    </>
  );
}

/** A PDF in the browser's own viewer: straight from file/download (it reads by Range), or a blob of the old base64. */
function PdfView({ file, src }: { file: FileContent; src: string }) {
  const [url, setUrl] = useState(src);
  useEffect(() => {
    if (src) {
      setUrl(src);
      return;
    }
    const u = URL.createObjectURL(new Blob([base64Bytes(file.data ?? "")], { type: "application/pdf" }));
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [file, src]);
  return url ? <iframe src={url} title={file.name} className="min-h-0 w-full flex-1 border-0 bg-inset" /> : null;
}

function FolderList({ file, onOpen }: { file: FileContent; onOpen: (p: string) => void }) {
  const entries = file.entries ?? [];
  if (!entries.length) return <Empty icon="folder" title={t("session-files-fileview.empty_folder")} />;
  return (
    <div className="min-h-0 flex-1 overflow-y-auto py-1">
      {entries.map((e) => {
        const dir = e.endsWith("/");
        const n = e.replace(/\/$/, "");
        return (
          <button
            key={e}
            type="button"
            onClick={() => onOpen(joinPath(file.path, n))}
            className="flex h-10 w-full items-center gap-3 px-4 text-left text-[13px] text-ink transition-colors hover:bg-hover"
          >
            <FileGlyph name={n} dir={dir} />
            <span className="min-w-0 flex-1 truncate">{n}</span>
          </button>
        );
      })}
      {entries.length >= 500 && <p className="px-4 py-2 text-[11.5px] text-ink-3">{t("session-files-fileview.folder_truncated")}</p>}
    </div>
  );
}
