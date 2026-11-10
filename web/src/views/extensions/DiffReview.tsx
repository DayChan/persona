// A file's diff for reviewing (UI version 4's diff with version 5's code review, docs/extensions/extensions.md, #3136): line
// numbers on both sides, one column or side by side, syntax highlighting with the words that changed marked, hidden
// lines shown 20 at a time or all (asked of the extension as lines, not a new page), viewed marks, generated and big
// files folded at first, comments on a line or a range picked on the numbers (alone, or as a draft of a pending
// review, with a suggestion), threads at their lines with suggestions drawn as a small diff, and the page around
// them: a toolbar (unresolved threads, whitespace, viewed so far, keys), the pending review's bar, keys (n / p, j / k…).
// Logic in lib/code-review.ts and lib/code-view.ts; the rest of the code components are CodeViews.tsx.
import { Fragment, memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  changeId, foldedAtFirst, gapRows, gaps, ignoreWhitespace, KEY_HELP, LineCache, nextOf, onlyWhitespace, overlay, pickRange, reveal,
  reviewCommand, setViewedHere, splitSuggestions, suggestionBlock, viewedHere, wordSpans, type Gap, type ReviewCommand, type Shown, type Span,
} from "../../lib/code-review";
import {
  gapBefore, highlightLines, hunkRows, hunkSides, languageOf, placeThreads, splitRows, statusMark, useDiffMode, useHighlighter,
  type Comment, type DiffRow, type Hunk, type Thread, type Token,
} from "../../lib/code-view";
import { toneOf, type UIAction, type UIItem, type UIView } from "../../lib/extension-ui";
import { ago } from "../../lib/format";
import { useMedia } from "../../lib/viewport";
import { t } from "../../i18n";
import { Button, cx, Dialog, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { scrollBehavior } from "../../ui/stream/motion";
import { Pill } from "../settings/SettingsParts";
import { Avatar, Box, card, LH, LoadLine, MARK, PILL, type CodeHandlers } from "./CodeViews";
import { ViewMarkdown } from "./ViewBody";
import { ReviewContext, useReview, type ReviewApi } from "./review-context";
import { scopeLocal, useNativeState, type NativeLocal } from "./native-state";
import "./review.css";

// ── the page around the diffs ─────────────────────────

const WS_KEY = "persona.diff.whitespace";

/** The diffs of a view by path, the files of its trees by path. */
function indexOf(view: UIView | null) {
  const diffs = new Map<string, UIItem>();
  const entries = new Map<string, boolean>();
  for (const s of view?.sections ?? [])
    for (const it of s.items) {
      if (it.type === "diff" && it.path) diffs.set(it.path, it);
      if (it.type === "file_tree") for (const e of it.entries ?? []) if (e.kind !== "dir") entries.set(e.path, !!e.viewed);
    }
  return { diffs, entries };
}

/** Whether a view is a page of diffs (the toolbar, the keys and the review bar are drawn for it). */
export const reviewing = (view: UIView | null | undefined) => !!view?.sections.some((s) => s.items.some((it) => it.type === "diff"));

/**
 * The review state of one page: viewed marks (the extension's own, changed at once and sent with mark_viewed; or kept
 * in this browser when it has none), whitespace, and the lines that came, per page.
 */
export function ReviewProvider({ view, page, fetch, children, local, nativeCaches }: { view: UIView | null; page: string; fetch: ReviewApi["fetch"]; children: React.ReactNode; local?: NativeLocal; nativeCaches?: Map<string, LineCache> }) {
  const [over, setOver] = useState<Map<string, boolean>>(() => new Map());
  const [nativeOver, setNativeOver] = useNativeState<Record<string, boolean>>(local, "viewed", {});
  const [here, setHere] = useState<Set<string>>(viewedHere);
  const [whitespace, setWs] = useNativeState(local, "whitespace", () => {
    try {
      return localStorage.getItem(WS_KEY) === "1";
    } catch {
      return false;
    }
  });
  const cache = useRef(new Map<string, LineCache>());
  useEffect(() => {
    setOver(new Map());
    cache.current = new Map();
  }, [page]);
  const index = useMemo(() => indexOf(view), [view]);
  const fetchRef = useRef(fetch);
  fetchRef.current = fetch;
  const api = useMemo<ReviewApi>(() => {
    const localId = (d: UIItem) => changeId(d.path ?? "", d.revision, d.hunks);
    return {
      fetch: (a, v) => fetchRef.current(a, v),
      viewed: (path) => {
        const d = index.diffs.get(path);
        const o = local && d ? nativeOver[localId(d)] : over.get(path);
        if (o !== undefined) return o;
        if (local && d) return !!d.viewed;
        if (d && !d.mark_viewed) return here.has(localId(d));
        return d ? !!d.viewed : !!index.entries.get(path);
      },
      setViewed: (path, v) => {
        const d = index.diffs.get(path);
        if (local && d) setNativeOver((m) => ({ ...m, [localId(d)]: v }));
        else setOver((m) => new Map(m).set(path, v));
        // Native mark_viewed dispatch belongs to the displayed diff's handler, never this provider's latest index.
        if (local) return;
        if (d?.mark_viewed) void fetchRef.current(d.mark_viewed, { path, viewed: String(v) });
        else if (d) {
          setViewedHere(localId(d), v);
          setHere(viewedHere());
        }
      },
      whitespace,
      setWhitespace: (on) => {
        setWs(on);
        try {
          localStorage.setItem(WS_KEY, on ? "1" : "0");
        } catch {
          // private mode
        }
      },
      lines: (path) => {
        const d = index.diffs.get(path);
        const key = nativeCaches && d ? localId(d) : path;
        const caches = nativeCaches ?? cache.current;
        if (!caches.has(key)) caches.set(key, new LineCache());
        return caches.get(key)!;
      },
      drafts: view?.review?.count ?? 0,
    };
  }, [over, here, index, whitespace, view?.review?.count, local, nativeOver, nativeCaches]);
  return <ReviewContext.Provider value={api}>{children}</ReviewContext.Provider>;
}

/** Scrolls to an element, under the sticky file head (straight there under reduced motion). */
function goTo(el: Element | undefined) {
  el?.scrollIntoView({ behavior: scrollBehavior(), block: "start" });
}

/** The elements of a kind on the page, and the one after or before the top of the screen. */
function step(selector: string, forward: boolean): Element | undefined {
  const els = [...document.querySelectorAll(selector)];
  const i = nextOf(els.map((e) => e.getBoundingClientRect().top), 72, forward);
  return i < 0 ? undefined : els[i];
}

/** The diff at the top of the screen (the last whose head has passed it), else the first. */
export function currentDiff(): HTMLElement | undefined {
  const els = [...document.querySelectorAll<HTMLElement>("[data-diff-path]")];
  let at = els[0];
  for (const e of els) if (e.getBoundingClientRect().top <= 80) at = e;
  return at;
}

/** Tells a diff to do something (a key pressed for it): its listener in DiffView. */
const tell = (el: Element | undefined, cmd: ReviewCommand) => el?.dispatchEvent(new CustomEvent("review", { detail: cmd }));

/** The keys of a page of diffs; ? opens their sheet. */
export function useReviewKeys(on: boolean, help: () => void) {
  const { whitespace, setWhitespace } = useReview();
  const [mode, setMode] = useDiffMode();
  useEffect(() => {
    if (!on) return;
    const onKey = (e: KeyboardEvent) => {
      const cmd = reviewCommand(e);
      if (!cmd) return;
      switch (cmd) {
        case "next-file":
        case "prev-file":
          goTo(step("[data-diff-path]", cmd === "next-file"));
          break;
        case "next-change":
        case "prev-change":
          step("[data-change]", cmd === "next-change")?.scrollIntoView({ behavior: scrollBehavior(), block: "center" });
          break;
        case "next-thread":
        case "prev-thread":
          step("[data-unresolved]", cmd === "next-thread")?.scrollIntoView({ behavior: scrollBehavior(), block: "center" });
          break;
        case "whitespace":
          setWhitespace(!whitespace);
          break;
        case "mode":
          setMode(mode === "split" ? "unified" : "split");
          break;
        case "filter": {
          const input = document.querySelector<HTMLInputElement>("[data-file-filter]");
          if (!input) return;
          input.focus();
          break;
        }
        case "help":
          help();
          break;
        default:
          tell(currentDiff(), cmd);
      }
      e.preventDefault();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [on, help, whitespace, setWhitespace, mode, setMode]);
}

/** The keys of a page of diffs, inside its ReviewProvider (so w reaches its whitespace). */
export function ReviewKeys({ onHelp }: { onHelp: () => void }) {
  useReviewKeys(true, onHelp);
  return null;
}

/** The keys' sheet. */
export function KeysHelp({ open, onClose }: { open: boolean; onClose: () => void }) {
  return (
    <Dialog open={open} onClose={onClose} width={380} title={t("extensions-review.keys")}>
      <dl className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-2 text-[13px]">
        {KEY_HELP.map(([keys, cmd]) => (
          <Fragment key={cmd}>
            <dt className="font-mono text-[12px] whitespace-nowrap text-ink">
              {keys.split(" / ").map((k, i) => (
                <Fragment key={k}>
                  {i > 0 && <span className="px-1 text-ink-3">/</span>}
                  <kbd className="rounded-[5px] bg-field px-1.5 py-0.5 shadow-inset-field">{k}</kbd>
                </Fragment>
              ))}
            </dt>
            <dd className="text-ink-2">{t(`extensions-review.key_${cmd.replace("-", "_")}`)}</dd>
          </Fragment>
        ))}
      </dl>
    </Dialog>
  );
}
/** Which diff is at the top of the screen (its place among the page's), following the scroll. */
function useCurrentFile(total: number): number {
  const [at, setAt] = useState(0);
  useEffect(() => {
    if (!total) return;
    let frame = 0;
    const on = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const els = [...document.querySelectorAll("[data-diff-path]")];
        const cur = currentDiff();
        setAt(Math.max(0, cur ? els.indexOf(cur) : 0));
      });
    };
    on();
    document.addEventListener("scroll", on, { capture: true, passive: true });
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("scroll", on, { capture: true });
    };
  }, [total]);
  return at;
}

/** A view's diffs and their threads: how many files, how many viewed, the threads still open. */
function tally(view: UIView, viewed: (p: string) => boolean) {
  const diffs = view.sections.flatMap((s) => s.items).filter((it) => it.type === "diff");
  const open = diffs.reduce((n, d) => n + (d.threads ?? []).filter((th) => !th.resolved && !th.pending).length, 0);
  return { files: diffs.length, viewed: diffs.filter((d) => viewed(d.path ?? "")).length, open };
}

/** Over a page of diffs: viewed so far, the open threads (pressed: the next one), whitespace, the keys. */
export function ReviewToolbar({ view, onHelp }: { view: UIView; onHelp: () => void }) {
  const review = useReview();
  const { files, viewed, open } = tally(view, review.viewed);
  const wide = useMedia("(min-width: 1024px)");
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 text-[12.5px]">
      {files > 1 && (
        <span className="flex items-center gap-2 whitespace-nowrap text-ink-2" title={t("extensions-review.viewed_of", { viewed, files })}>
          <span className="relative h-1.5 w-16 overflow-hidden rounded-full bg-field shadow-inset-field" aria-hidden>
            <span className="absolute inset-y-0 left-0 rounded-full bg-green transition-[width] duration-300" style={{ width: `${(viewed / files) * 100}%` }} />
          </span>
          <span className="tabular-nums">{t("extensions-review.viewed_of", { viewed, files })}</span>
        </span>
      )}
      {open > 0 && (
        <button
          type="button"
          // the next one, past the last the first: picked, then scrolled to once (scrollIntoView answers undefined, so
          // `a?.scrollIntoView() ?? b?.scrollIntoView()` would run both and always end on the first)
          onClick={() => (step("[data-unresolved]", true) ?? document.querySelector("[data-unresolved]"))?.scrollIntoView({ behavior: scrollBehavior(), block: "center" })}
          className="flex h-7 items-center gap-1.5 rounded-[8px] px-2 whitespace-nowrap text-orange transition-colors hover:bg-hover"
          title={t("extensions-review.next_open")}
        >
          <Icon name="chat" size={13} />
          <span className="tabular-nums">{t("extensions-review.open_threads", { count: open })}</span>
          <Icon name="arrowDown" size={11} stroke={2.2} />
        </button>
      )}
      <span className="flex-1" />
      <button
        type="button"
        role="switch"
        aria-checked={review.whitespace}
        onClick={() => review.setWhitespace(!review.whitespace)}
        className={cx("flex h-7 items-center gap-1.5 rounded-[8px] px-2 whitespace-nowrap transition-colors hover:bg-hover", review.whitespace ? "text-accent-ink" : "text-ink-3")}
      >
        <span className={cx("flex size-3.5 items-center justify-center rounded-[4px]", review.whitespace ? "bg-accent text-white" : "border border-line-strong")}>
          {review.whitespace && <Icon name="check" size={10} stroke={3} />}
        </span>
        {t("extensions-review.ignore_whitespace")}
      </button>
      {wide && (
        <button type="button" onClick={onHelp} aria-label={t("extensions-review.keys")} title={`${t("extensions-review.keys")} (?)`} className="flex size-7 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-hover hover:text-ink">
          <Icon name="keyboard" size={14} />
        </button>
      )}
    </div>
  );
}

const BUTTON_TONE = { neutral: "secondary", positive: "accent", caution: "secondary", danger: "danger" } as const;

/** The pending review's summary and buttons (in the bar on a wide page, in a sheet on a narrow one). */
function ReviewForm({ view, act, busy, done }: { view: UIView; done?: () => void } & Pick<CodeHandlers, "act" | "busy">) {
  const r = view.review!;
  const [body, setBody] = useState(r.value ?? "");
  const press = (a: UIAction) => {
    act(a, { [r.name]: body });
    done?.();
  };
  return (
    <div className="flex flex-col gap-2.5">
      <Box value={body} onChange={setBody} placeholder={r.placeholder || t("extensions-review.summary")} onSubmit={() => r.buttons[0] && press(r.buttons[0].action)} disabled={!!busy} />
      <div className="flex flex-wrap items-center justify-end gap-2">
        {r.discard && (
          <Button tone="quiet" className="mr-auto" disabled={!!busy} onClick={() => press(r.discard!)}>
            {t("extensions-review.discard")}
          </Button>
        )}
        {r.buttons.map((b, i) => (
          <Button key={i} tone={i === 0 && toneOf(b.tone) === "neutral" ? "primary" : BUTTON_TONE[toneOf(b.tone)]} busy={busy === b.action.id} disabled={!!busy && busy !== b.action.id} onClick={() => press(b.action)} title={b.title}>
            {b.title}
          </Button>
        ))}
      </div>
    </div>
  );
}

/**
 * At the foot of a page of diffs: the pending review (how many drafts; opened, its summary and buttons) and, narrow,
 * the file at the top of the screen with the one before and after.
 */
export function ReviewBar({ view, act, busy }: { view: UIView } & Pick<CodeHandlers, "act" | "busy">) {
  const wide = useMedia("(min-width: 1024px)");
  const files = useMemo(() => view.sections.flatMap((s) => s.items).filter((it) => it.type === "diff").length, [view]);
  const at = useCurrentFile(files);
  const [open, setOpen] = useState(false);
  const r = view.review;
  const drafts = r?.count ?? 0;
  if (!(r && (drafts > 0 || open)) && (wide || files < 2)) return null;
  const nav = !wide && files > 1 && (
    <div className="flex items-center gap-0.5">
      <button type="button" aria-label={t("extensions-review.prev_file")} onClick={() => goTo(step("[data-diff-path]", false))} className="flex size-8 items-center justify-center rounded-[8px] text-ink-2 transition-colors hover:bg-hover">
        <Icon name="chevronLeft" size={14} stroke={2.2} />
      </button>
      <span className="min-w-[4.5ch] text-center text-[12.5px] text-ink-2 tabular-nums">
        {at + 1} / {files}
      </span>
      <button type="button" aria-label={t("extensions-review.next_file")} onClick={() => goTo(step("[data-diff-path]", true))} className="flex size-8 items-center justify-center rounded-[8px] text-ink-2 transition-colors hover:bg-hover">
        <Icon name="chevronRight" size={14} stroke={2.2} />
      </button>
    </div>
  );
  return (
    <div className="sticky bottom-3 z-[3] mx-auto w-full max-w-[920px]">
      <div className="rounded-card bg-surface/95 px-3 py-2 shadow-raised backdrop-blur" style={{ animation: "fade-up 220ms both" }}>
        <div className="flex min-h-9 items-center gap-2">
          {nav}
          {r && (
            <>
              <span className="flex min-w-0 flex-1 items-center gap-2 text-[13px] whitespace-nowrap text-ink">
                <Icon name="chat" size={14} className="shrink-0 text-orange" />
                <span className="truncate">{drafts ? t(wide ? "extensions-review.drafts" : "extensions-review.drafts_short", { count: drafts }) : t("extensions-review.review")}</span>
              </span>
              {wide ? (
                !open && (
                  <Button tone="primary" onClick={() => setOpen(true)}>
                    {t("extensions-review.finish")}
                  </Button>
                )
              ) : (
                <Button tone="primary" onClick={() => setOpen(true)}>
                  {t("extensions-review.submit")}
                </Button>
              )}
            </>
          )}
          {!r && <span className="flex-1" />}
        </div>
        {wide && open && r && (
          <div className="pt-2 pb-1">
            <ReviewForm view={view} act={act} busy={busy} done={() => setOpen(false)} />
          </div>
        )}
      </div>
      {!wide && r && (
        <Dialog open={open} onClose={() => setOpen(false)} width={480} title={drafts ? t("extensions-review.drafts", { count: drafts }) : t("extensions-review.review")}>
          <ReviewForm view={view} act={act} busy={busy} done={() => setOpen(false)} />
        </Dialog>
      )}
    </div>
  );
}
// ── rows ──────────────────────────────────────────────

const ROW_BG: Record<DiffRow["kind"], string> = { add: "bg-diff-add-bg", del: "bg-diff-del-bg", context: "", note: "" };
const GUTTER_BG: Record<DiffRow["kind"], string> = { add: "bg-diff-add-bg", del: "bg-diff-del-bg", context: "bg-surface", note: "bg-surface" };
const WORD_BG: Record<DiffRow["kind"], string> = { add: "bg-diff-add-word", del: "bg-diff-del-word", context: "", note: "" };
const SIGN: Record<DiffRow["kind"], string> = { add: "+", del: "−", context: " ", note: "" };

/** A line's text: its highlighting, the words that changed marked. */
function Pieces({ tokens, text, spans, kind }: { tokens?: Token[] | null; text: string; spans?: Span[]; kind: DiffRow["kind"] }) {
  if (!text) return <> </>;
  const pieces = overlay(tokens, text, spans);
  return (
    <>
      {pieces.map((p, i) =>
        p.changed ? (
          <span key={i} className={cx("rounded-[2px]", WORD_BG[kind], p.cls)}>
            {p.text}
          </span>
        ) : p.cls ? (
          <span key={i} className={p.cls}>
            {p.text}
          </span>
        ) : (
          <Fragment key={i}>{p.text}</Fragment>
        ),
      )}
    </>
  );
}

type Side = "old" | "new";
type Spot = { side: Side; line: number };
/** Lines picked for a comment: one, or start_line to line. */
type LinePick = { side: Side; line: number; start_line?: number };
const inPick = (p: LinePick | null, side: Side, n?: number) => !!p && n !== undefined && p.side === side && n >= (p.start_line ?? p.line) && n <= p.line;

/** What a line number does: press (and drag over others) to pick lines for a comment. */
interface Picker {
  can: boolean;
  down: (s: Spot, shift: boolean) => void;
  over: (s: Spot) => void;
  /** The press is over (a key pressed it): open the box. */
  up: () => void;
}

/** A number in the gutter; pressing it picks its line for a comment, dragging over the others picks a range. */
function LineNo({ n, side, kind, width, picker, picked }: { n?: number; side: Side; kind: DiffRow["kind"]; width: string; picker: Picker; picked: boolean }) {
  const pressed = useRef(false);
  const cls = cx("group/ln shrink-0 pr-2 text-right tabular-nums select-none", picked ? "bg-accent-tint text-accent-ink" : cx(GUTTER_BG[kind], "text-ink-3"));
  if (n === undefined) return <span className={cls} style={{ width }} />;
  if (!picker.can)
    return (
      <span className={cls} style={{ width }}>
        {n}
      </span>
    );
  return (
    <button
      type="button"
      data-ln={side}
      data-n={n}
      onMouseDown={(e) => {
        if (e.button !== 0) return;
        e.preventDefault();
        pressed.current = true;
        picker.down({ side, line: n }, e.shiftKey);
      }}
      onMouseEnter={() => picker.over({ side, line: n })}
      onClick={(e) => {
        // pressed with the mouse: the mouseup opened the box; with a key (or a screen reader): open it now
        if (pressed.current) return void (pressed.current = false);
        picker.down({ side, line: n }, e.shiftKey);
        picker.up();
      }}
      aria-label={t("extensions-codeviews.comment_on", { n })}
      className={cx(cls, "relative cursor-pointer hover:text-accent-ink")}
      style={{ width }}
    >
      <span className="absolute top-1/2 left-1 flex size-4 -translate-y-1/2 items-center justify-center rounded-[4px] bg-accent text-white opacity-0 transition-opacity group-hover/ln:opacity-100">
        <Icon name="plus" size={10} stroke={2.6} />
      </span>
      {n}
    </button>
  );
}

const signCls = (k: DiffRow["kind"]) => cx("w-[2ch] shrink-0 text-center select-none", k === "add" ? "text-green" : k === "del" ? "text-red" : "text-ink-3");

/** One row of the one-column view. */
const UnifiedLine = memo(function UnifiedLine({ r, tokens, spans, width, picker, pick, change }: { r: DiffRow; tokens?: Token[] | null; spans?: Span[]; width: string; picker: Picker; pick: LinePick | null; change: boolean }) {
  if (r.kind === "note") return <div className="px-3 text-[11.5px] text-ink-3 italic">{r.text}</div>;
  const picked = inPick(pick, r.kind === "del" ? "old" : "new", r.kind === "del" ? r.old : r.new);
  return (
    <div className={cx("flex", picked ? "bg-accent-tint/60" : ROW_BG[r.kind])} data-change={change ? "" : undefined}>
      <LineNo n={r.old} side="old" kind={r.kind} width={width} picker={{ ...picker, can: picker.can && r.kind === "del" }} picked={picked && r.kind === "del"} />
      <LineNo n={r.new} side="new" kind={r.kind} width={width} picker={picker} picked={picked && r.kind !== "del"} />
      <span className={signCls(r.kind)}>{SIGN[r.kind]}</span>
      <span className="pr-6 whitespace-pre text-ink">
        <Pieces tokens={tokens} text={r.text} spans={spans} kind={r.kind} />
      </span>
    </div>
  );
});

/** One row of the side by side view: the old line at the start, the new at the end. */
function SplitLine({ old, nw, tokOld, tokNew, spanOld, spanNew, width, picker, pick, change }: {
  old?: DiffRow; nw?: DiffRow; tokOld?: Token[] | null; tokNew?: Token[] | null; spanOld?: Span[]; spanNew?: Span[]; width: string; picker: Picker; pick: LinePick | null; change: boolean;
}) {
  const half = (row: DiffRow | undefined, side: Side, tok?: Token[] | null, spans?: Span[]) => {
    const n = side === "old" ? row?.old : row?.new;
    const kind = row ? row.kind : "context";
    // an unchanged line is commented on as the new one
    const canSide = !!row && (row.kind !== "context" || side === "new");
    const picked = inPick(pick, side, n) && canSide;
    return (
      <div className={cx("flex min-w-0", picked ? "bg-accent-tint/60" : row ? ROW_BG[kind] : "bg-hover/60", side === "new" && "border-l border-line")}>
        <LineNo n={n} side={side} kind={kind} width={width} picker={{ ...picker, can: picker.can && canSide }} picked={picked} />
        <span className={signCls(kind)}>{row ? SIGN[kind] : ""}</span>
        <span className="min-w-0 flex-1 overflow-hidden pr-4 whitespace-pre text-ink">{row && <Pieces tokens={tok} text={row.text} spans={spans} kind={kind} />}</span>
      </div>
    );
  };
  return (
    <div className="grid grid-cols-2" data-change={change ? "" : undefined}>
      {half(old, "old", tokOld, spanOld)}
      {half(nw, "new", tokNew, spanNew)}
    </div>
  );
}

// ── threads ───────────────────────────────────────────

/** A suggestion in a comment: the lines it is on, and what it would make them. */
function Suggestion({ from, to }: { from: string[]; to: string[] }) {
  return (
    <div className="mx-4 my-2 overflow-hidden rounded-control font-mono text-[12px] shadow-hairline" style={{ lineHeight: `${LH}px` }}>
      <div className="flex items-center gap-1.5 border-b border-line bg-surface px-3 py-1 font-sans text-[11.5px] text-ink-3">
        <Icon name="pencil" size={11} />
        {t("extensions-review.suggested_change")}
      </div>
      <div className="overflow-x-auto">
        <div className="min-w-max">
          {from.map((l, i) => (
            <div key={`o${i}`} className="flex bg-diff-del-bg">
              <span className={signCls("del")}>−</span>
              <span className="pr-4 whitespace-pre text-ink">{l || " "}</span>
            </div>
          ))}
          {to.map((l, i) => (
            <div key={`n${i}`} className="flex bg-diff-add-bg">
              <span className={signCls("add")}>+</span>
              <span className="pr-4 whitespace-pre text-ink">{l || " "}</span>
            </div>
          ))}
          {!to.length && <div className="px-3 py-0.5 font-sans text-[11.5px] text-ink-3 italic">{t("extensions-review.suggest_delete")}</div>}
        </div>
      </div>
    </div>
  );
}

function ReviewComment({ c, original }: { c: Comment; original: string[] }) {
  const parts = useMemo(() => splitSuggestions(c.body ?? ""), [c.body]);
  return (
    <div className="flex gap-2.5 px-4 py-2.5">
      <Avatar avatar={c.avatar} name={c.author} size={26} />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2 whitespace-nowrap">
          <span className="truncate text-[12.5px] font-semibold text-ink">{c.author}</span>
          {c.at && <time dateTime={c.at} title={new Date(c.at).toLocaleString()} className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{ago(c.at)}</time>}
          {c.badge?.text && <Pill tone={PILL[toneOf(c.badge.tone)]}>{c.badge.text}</Pill>}
        </div>
        {c.body && (
          <div className="-mx-4 -mb-2">
            {parts.map((p, i) => (p.kind === "suggestion" ? <Suggestion key={i} from={original} to={p.lines} /> : <ViewMarkdown key={i} text={p.text} compact />))}
          </div>
        )}
      </div>
    </div>
  );
}

/** Inline comments at a line: folded to its first one when resolved; a reply, resolve or reopen. */
function ThreadCard({ thread, act, busy, original, local }: { thread: Thread; original: string[]; local?: NativeLocal } & Pick<CodeHandlers, "act" | "busy">) {
  const [open, setOpen] = useNativeState(local, "open", !thread.resolved);
  const [replying, setReplying] = useNativeState(local, "replying", false);
  const [body, setBody] = useNativeState(local, "body", "");
  const first = thread.comments[0];
  const range = thread.start_line && thread.start_line < thread.line ? `${thread.start_line}–${thread.line}` : "";
  const send = () => {
    if (!body.trim() || !thread.reply) return;
    act(thread.reply as UIAction, { ...(thread.id ? { thread: thread.id } : {}), body: body.trim() });
    if (!local) { setBody(""); setReplying(false); }
  };
  return (
    <div
      className={cx("mx-3 my-2 overflow-hidden rounded-control bg-surface font-sans shadow-card sm:max-w-[760px]", thread.pending && "ring-1 ring-orange/40")}
      data-thread={thread.id ?? ""}
      data-unresolved={!thread.resolved && !thread.pending ? "" : undefined}
    >
      <div className="flex items-center border-b border-line">
        <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="flex min-w-0 flex-1 items-center gap-2 px-3 py-1.5 text-left text-[11.5px] whitespace-nowrap text-ink-3 transition-colors hover:bg-hover">
          <Icon name="chevronRight" size={10} stroke={2.2} className={cx("shrink-0 transition-transform", open && "rotate-90")} />
          <span className="min-w-0 flex-1 truncate">
            {range && <span className="mr-1.5 tabular-nums">{t("extensions-review.lines_range", { range })}</span>}
            {open ? t("extensions-codeviews.thread_lines", { count: thread.comments.length }) : `${first?.author ?? ""}: ${first?.body ?? ""}`}
          </span>
          {thread.pending && <Pill tone="orange">{t("extensions-review.draft")}</Pill>}
          {thread.outdated && <Pill tone="orange">{t("extensions-codeviews.outdated")}</Pill>}
          {thread.resolved && <Pill tone="green">{t("extensions-codeviews.resolved")}</Pill>}
        </button>
        {thread.remove && thread.id && (
          <button
            type="button"
            disabled={!!busy}
            onClick={() => act(thread.remove as UIAction, { thread: thread.id! })}
            className="mr-1.5 flex h-6 shrink-0 items-center gap-1 rounded-[6px] px-1.5 text-[11.5px] whitespace-nowrap text-ink-2 transition-colors enabled:hover:bg-hover enabled:hover:text-red disabled:text-ink-3"
          >
            <Icon name="trash" size={11} stroke={2.2} />
            {t("extensions-review.delete_draft")}
          </button>
        )}
        {thread.resolve && thread.id && (
          <button
            type="button"
            disabled={!!busy}
            onClick={() => act(thread.resolve as UIAction, { thread: thread.id!, resolved: String(!thread.resolved) })}
            className="mr-1.5 flex h-6 shrink-0 items-center gap-1 rounded-[6px] px-1.5 text-[11.5px] whitespace-nowrap text-ink-2 transition-colors enabled:hover:bg-hover disabled:text-ink-3"
          >
            {busy === thread.resolve.id ? <Spinner size={10} /> : <Icon name={thread.resolved ? "undo" : "check"} size={11} stroke={2.2} />}
            {thread.resolved ? t("extensions-review.reopen") : t("extensions-review.resolve")}
          </button>
        )}
      </div>
      {open && (
        <>
          <div className="divide-y divide-line">
            {thread.comments.map((c, i) => (
              <ReviewComment key={c.id ?? i} c={c} original={original} />
            ))}
          </div>
          {thread.reply && (
            <div className="border-t border-line px-3 py-2.5">
              {replying ? (
                <div className="flex flex-col gap-2">
                  <Box value={body} onChange={setBody} placeholder={t("extensions-codeviews.reply")} autoFocus onSubmit={send} disabled={!!busy && busy !== thread.reply.id} />
                  <div className="flex items-center justify-end gap-2">
                    <Button tone="quiet" onClick={() => setReplying(false)}>
                      {t("extensions-codeviews.cancel")}
                    </Button>
                    <Button tone="primary" busy={busy === thread.reply.id} disabled={!body.trim() || !!busy} onClick={send}>
                      {t("extensions-codeviews.comment")}
                    </Button>
                  </div>
                </div>
              ) : (
                <button type="button" onClick={() => setReplying(true)} className="h-8 w-full rounded-control bg-field px-3 text-left text-[12.5px] text-ink-3 shadow-inset-field transition-colors hover:text-ink-2">
                  {t("extensions-codeviews.reply")}
                </button>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}

/**
 * The box for a comment on the lines picked: send it alone (comment) or keep it as a draft of the review (draft; first
 * once a review is under way), or start a suggestion with the lines picked.
 */
function LineComposer({ pick, body, setBody, item, act, busy, original, onCancel, local }: {
  pick: LinePick; body: string; setBody: (b: string) => void; item: UIItem; original: string[]; onCancel: () => void; local?: NativeLocal;
} & Pick<CodeHandlers, "act" | "busy">) {
  const { drafts } = useReview();
  const values = (b: string) => ({
    path: item.path ?? "",
    side: pick.side,
    line: String(pick.line),
    ...(pick.start_line ? { start_line: String(pick.start_line) } : {}),
    body: b,
  });
  const go = (a: UIAction | undefined) => {
    if (!a || !body.trim()) return;
    act(a, values(body.trim()));
    if (!local) { onCancel(); setBody(""); }
  };
  const draftFirst = !!item.draft && drafts > 0;
  const primary = draftFirst ? item.draft : item.comment;
  const range = pick.start_line ? `${pick.start_line}–${pick.line}` : String(pick.line);
  return (
    <div className="flex flex-col gap-2">
      <Box value={body} onChange={setBody} autoFocus placeholder={t("extensions-review.comment_on_lines", { range })} onSubmit={() => go(primary)} disabled={!!busy} />
      <div className="flex flex-wrap items-center justify-end gap-2">
        {item.suggestions && pick.side === "new" && original.length > 0 && (
          <Button tone="quiet" icon="pencil" className="mr-auto" onClick={() => setBody((body ? body.replace(/\n*$/, "\n\n") : "") + suggestionBlock(original))}>
            {t("extensions-review.suggest")}
          </Button>
        )}
        <Button tone="quiet" onClick={onCancel}>
          {t("extensions-codeviews.cancel")}
        </Button>
        {item.draft && (
          <Button tone={draftFirst ? "primary" : "secondary"} busy={busy === item.draft.id} disabled={!body.trim() || !!busy} onClick={() => go(item.draft)}>
            {draftFirst ? t("extensions-review.add_to_review") : t("extensions-review.start_review")}
          </Button>
        )}
        {item.comment && (
          <Button tone={draftFirst ? "secondary" : "primary"} busy={busy === item.comment.id} disabled={!body.trim() || !!busy} onClick={() => go(item.comment)}>
            {item.draft ? t("extensions-review.comment_now") : t("extensions-codeviews.comment")}
          </Button>
        )}
      </div>
    </div>
  );
}
// ── a file's diff ─────────────────────────────────────

const START: Shown = { top: 0, bottom: 0 };

/** The bar over lines hidden between hunks: show 20 more below the hunk before, above the one after, or all. */
function GapBar({ hidden, first, last, header, loading, onShow }: { hidden: number; first: boolean; last: boolean; header?: string; loading: boolean; onShow: (d: "up" | "down" | "all") => void }) {
  const few = hidden <= 20;
  const dirs: ("up" | "down" | "all")[] = few ? ["all"] : [...(first ? [] : (["down"] as const)), ...(last ? [] : (["up"] as const)), "all"];
  const label = (d: "up" | "down" | "all") => (d === "up" ? t("extensions-codeviews.expand_up") : d === "down" ? t("extensions-codeviews.expand_down") : t("extensions-review.expand_all"));
  return (
    <div className="flex h-8 items-center bg-accent-tint/50 font-sans text-[11.5px] text-accent-ink">
      <span className="sticky left-0 flex min-w-0 items-center gap-1 px-2">
        {dirs.map((d) => (
          <button key={d} type="button" disabled={loading} onClick={() => onShow(d)} aria-label={label(d)} title={label(d)} className="flex size-6 shrink-0 items-center justify-center rounded-[6px] transition-colors enabled:hover:bg-hover">
            <Icon name={d === "up" ? "up" : d === "down" ? "arrowDown" : "unfold"} size={12} stroke={2.2} />
          </button>
        ))}
        {loading && <Spinner size={11} />}
        <span className="truncate whitespace-nowrap text-ink-3">
          {Number.isFinite(hidden) ? t("extensions-codeviews.hidden_lines", { count: hidden }) : t("extensions-review.rest_of_file")}
          {header && <span className="ml-2 font-mono whitespace-pre">{header}</span>}
        </span>
      </span>
    </div>
  );
}

const FOLD_NOTE: Record<string, string> = { generated: "extensions-review.folded_generated", big: "extensions-review.folded_big", viewed: "extensions-review.folded_viewed" };

/** One file's diff. */
export function DiffView({ item, act, busy, local, fetch, canFetch, note, room, askToLoad }: {
  item: UIItem;
  /** A session review's (#151): said in place of "no changes" for a diff with no lines (a file it does not show). */
  note?: string;
  /** The widest a comment box or thread may be: the page's by default, a side panel's own width there. */
  room?: string;
  /** A diff without its lines waits for Load diff instead of loading when it comes near (a big one). */
  askToLoad?: boolean;
} & CodeHandlers) {
  const sharedReview = useReview();
  const review = fetch ? { ...sharedReview, fetch } : sharedReview;
  const path = item.path ?? "";
  const raw = useMemo(() => item.hunks ?? [], [item.hunks]);
  const [mode, setMode] = useDiffMode();
  const roomy = useMedia("(min-width: 1024px)");
  const coarse = useMedia("(pointer: coarse)");
  const split = mode === "split" && roomy;
  const viewed = review.viewed(path);
  const [reason] = useState(() => foldedAtFirst({ ...item, viewed }));
  const [open, setOpen] = useNativeState(local, "open", !reason);
  const root = useRef<HTMLDivElement>(null);
  // a diff that came without its lines opens once they come
  const had = useRef(raw.length > 0);
  useEffect(() => {
    if (raw.length && !had.current) {
      had.current = true;
      setOpen(true);
    }
  }, [raw.length]);

  // hidden lines: what is shown of each gap, and the file's lines as they come
  const cache = review.lines(path);
  const [shown, setShown] = useNativeState<Record<number, Shown>>(local, "shown", {});
  const [gapBusy, setGapBusy] = useState<number | null>(null);
  // a new file is all in its diff: nothing hidden
  const gapList = useMemo(() => (item.context && item.status !== "added" ? gaps(raw, cache.total) : []), [item.context, item.status, raw, cache.total, shown]); // eslint-disable-line react-hooks/exhaustive-deps
  const showGap = async (g: Gap, d: "up" | "down" | "all") => {
    const r = reveal(g, shown[g.at] ?? START, d);
    if (r.need && item.context && !cache.has(r.need.from, r.need.to)) {
      setGapBusy(g.at);
      const res = await review.fetch(item.context, { path, side: "new", from: String(r.need.from), to: r.need.to === undefined ? "end" : String(r.need.to) });
      setGapBusy(null);
      if (!res?.lines) return;
      cache.put(res.lines.start, res.lines.lines, res.lines.total);
      if (local && !Number.isFinite(r.shown.top)) r.shown.top = Math.max(0, (res.lines.total ?? res.lines.start + res.lines.lines.length - 1) - g.from + 1);
    }
    // reveal uses Infinity for an unknown trailing end. Keyed native state is JSON-only;
    // retain the finite extent returned by the manager instead of persisting that sentinel.
    const next = local && !Number.isFinite(r.shown.top)
      ? { ...r.shown, top: Math.max(0, (cache.total ?? g.from - 1) - g.from + 1) }
      : r.shown;
    setShown((x) => ({ ...x, [g.at]: next }));
  };

  // the rows, whitespace ignored or not; the words that changed; highlighting
  const ws = review.whitespace;
  const rowsOf = useMemo(() => raw.map((h) => (ws ? ignoreWhitespace(hunkRows(h)) : hunkRows(h))), [raw, ws]);
  const spansOf = useMemo(() => (open ? rowsOf.map(wordSpans) : []), [rowsOf, open]);
  const lang = languageOf(item.language, path);
  const ready = useHighlighter(open && !!lang && raw.length > 0);
  const lit = useMemo(
    () =>
      ready && open
        ? raw.map((h) => {
            const sd = hunkSides(h);
            return { old: highlightLines(sd.old, lang), new: highlightLines(sd.new, lang) };
          })
        : null,
    [ready, open, raw, lang],
  );
  const litGap = useRef(new Map<string, (Token[] | null)[] | null>());
  const gapTokens = (rows: DiffRow[]) => {
    if (!ready || !rows.length) return null;
    const k = `${rows[0].new}:${rows.length}`;
    if (!litGap.current.has(k)) litGap.current.set(k, highlightLines(rows.map((r) => r.text).join("\n"), lang));
    return litGap.current.get(k) ?? null;
  };
  const tokensOf = (h: number, r: DiffRow, side: Side): Token[] | null | undefined => {
    const at = side === "old" ? r.oldAt : r.newAt;
    return at === undefined ? undefined : lit?.[h]?.[side]?.[at];
  };

  // the new side's lines, for suggestions: the diff's, then those that came
  const newLines = useMemo(() => {
    const m = new Map<number, string>();
    for (const h of raw) for (const r of hunkRows(h)) if (r.new !== undefined) m.set(r.new, r.text);
    return m;
  }, [raw]);
  const original = (p: { side: string; line: number; start_line?: number }): string[] => {
    if (p.side !== "new") return [];
    const out: string[] = [];
    for (let n = p.start_line ?? p.line; n <= p.line; n++) {
      const l = newLines.get(n) ?? cache.get(n);
      if (l === undefined) return [];
      out.push(l);
    }
    return out;
  };

  // threads at their lines (the diff's, and the hidden ones shown); the rest at the end
  const revealed = useMemo(() => {
    const set = new Set<string>();
    for (const g of gapList) {
      const { top, bottom } = gapRows(g, shown[g.at] ?? START, cache);
      for (const r of [...top, ...bottom]) (set.add(`new:${r.new}`), set.add(`old:${r.old}`));
    }
    return set;
  }, [gapList, shown, cache]);
  const { at, rest } = useMemo(() => {
    const placed = placeThreads(item.threads ?? [], raw);
    const keep: Thread[] = [];
    for (const th of placed.rest) {
      const key = `${th.side === "old" ? "old" : "new"}:${th.line}`;
      if (!th.outdated && revealed.has(key)) placed.at.set(key, [...(placed.at.get(key) ?? []), th]);
      else keep.push(th);
    }
    return { at: placed.at, rest: keep };
  }, [item.threads, raw, revealed]);

  // picking lines for a comment: press a number, drag over others or shift-press one; on a touch screen press another
  const canComment = !!(item.comment || item.draft);
  const [pick, setPick] = useNativeState<LinePick | null>(local, "pick", null);
  const [box, setBox] = useNativeState<LinePick | null>(local, "box", null);
  const [draftBody, setDraftBody] = useNativeState(local, "draft", "");
  const anchor = useRef<Spot | null>(null);
  const dragging = useRef(false);
  const pickRef = useRef<LinePick | null>(null);
  pickRef.current = pick;
  const boxRef = useRef<LinePick | null>(null);
  boxRef.current = box;
  const picker = useMemo<Picker>(
    () => ({
      can: canComment,
      down: (sp, shift) => {
        const b = boxRef.current;
        if ((shift || coarse) && (shift ? anchor.current : b && b.side === sp.side)) {
          const from = shift ? anchor.current! : { side: b!.side, line: b!.start_line ?? b!.line };
          const p = pickRange(from, sp);
          setPick(p);
          setBox(p);
          return;
        }
        anchor.current = sp;
        dragging.current = true;
        pickRef.current = { side: sp.side, line: sp.line };
        setPick(pickRef.current);
      },
      over: (sp) => {
        if (!dragging.current || !anchor.current) return;
        pickRef.current = pickRange(anchor.current, sp);
        setPick(pickRef.current);
      },
      up: () => {
        if (!dragging.current) return;
        dragging.current = false;
        setBox(pickRef.current);
      },
    }),
    [canComment, coarse, local],
  );
  useEffect(() => {
    const up = () => picker.up();
    document.addEventListener("mouseup", up);
    return () => document.removeEventListener("mouseup", up);
  }, [picker]);
  const closeBox = useCallback(() => {
    setBox(null);
    setPick(null);
  }, [local]);

  // keys sent to this diff (useReviewKeys): viewed, fold, comment on the first changed line in view
  const setViewed = (v: boolean) => {
    if (local && busy) return;
    if (local && item.mark_viewed && fetch) {
      const was = viewed;
      void fetch(item.mark_viewed, { path, viewed: String(v) }).then((result) => { if (!result) review.setViewed(path, was); });
    }
    review.setViewed(path, v);
    if (v) {
      setOpen(false);
      if (root.current && root.current.getBoundingClientRect().top < 0) root.current.scrollIntoView({ block: "start" });
    }
  };
  const onCmd = useRef<(cmd: ReviewCommand) => void>(() => {});
  onCmd.current = (cmd) => {
    if (cmd === "viewed") {
      setViewed(!viewed);
      if (!viewed) goTo(step("[data-diff-path]", true));
    } else if (cmd === "fold") setOpen((o) => !o);
    else if (cmd === "comment" && canComment) {
      setOpen(true);
      const nums = [...(root.current?.querySelectorAll<HTMLElement>("[data-ln]") ?? [])];
      const el = nums.find((e) => e.getBoundingClientRect().top >= 80) ?? nums[0];
      if (el) {
        const p = { side: el.dataset.ln as Side, line: Number(el.dataset.n) };
        setPick(p);
        setBox(p);
      }
    }
  };
  useEffect(() => {
    const el = root.current;
    const on = (e: Event) => onCmd.current((e as CustomEvent<ReviewCommand>).detail);
    el?.addEventListener("review", on);
    return () => el?.removeEventListener("review", on);
  }, []);

  // a diff given without its lines loads them when it comes near (not a generated one: pressed for)
  const [loadState, setLoadState] = useNativeState<"" | "loading" | "failed" | "asked">(local, `load/${JSON.stringify(item.load)}`, "");
  const autoLoad = !raw.length && !!item.load && !item.binary && reason !== "generated" && !askToLoad;
  useEffect(() => {
    const el = root.current;
    if (!autoLoad || !el || loadState || local && busy || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver(
      (es) => {
        if (!es.some((e) => e.isIntersecting)) return;
        const observation = `observed-load/${JSON.stringify(item.load)}`;
        if (local && (busy || canFetch?.() === false)) return;
        if (local?.get<boolean>(observation)) return;
        local?.set(observation, true);
        io.disconnect();
        setLoadState("loading");
        void review.fetch(item.load!, { path }).then((r) => setLoadState(r ? local ? "asked" : "" : "failed"));
      },
      { rootMargin: "800px 0px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [autoLoad, loadState, item.load, path, review, busy, canFetch]);

  const digits = useMemo(() => String(raw.reduce((m, h) => Math.max(m, h.old_start + h.lines.length, h.new_start + h.lines.length), cache.total ?? 99)).length, [raw, cache.total]);
  const width = `calc(${digits}ch + 14px)`;
  const mark = statusMark(item.status);
  const threads = item.threads?.length ?? 0;
  const allWhitespace = ws && raw.length > 0 && onlyWhitespace(raw);
  // what goes under a row: the threads at its lines, and the comment box when the lines picked end there
  const under = (spots: Spot[], k: string) => {
    const out: React.ReactNode[] = [];
    for (const sp of spots) {
      for (const th of at.get(`${sp.side}:${sp.line}`) ?? [])
        out.push(<ThreadCard key={`t:${th.id ?? sp.line}`} thread={th} local={scopeLocal(local, `thread/${th.id ?? `${th.side}:${th.line}`}`)} act={act} busy={busy} original={original({ side: th.side, line: th.line, start_line: th.start_line })} />);
      if (box && box.side === sp.side && box.line === sp.line)
        out.push(
          <div key="box" className="border-y border-line bg-surface px-3 py-2.5 font-sans">
            <div className="sm:max-w-[760px]">
              <LineComposer local={local} pick={box} body={draftBody} setBody={setDraftBody} item={item} act={act} busy={busy} original={original(box)} onCancel={closeBox} />
            </div>
          </div>,
        );
    }
    return out.length ? (
      <div key={`u:${k}`} className="sticky left-0 w-full max-w-[calc(100vw-2rem)]" style={room ? { maxWidth: room } : undefined}>
        {out}
      </div>
    ) : null;
  };
  const spotsOf = (r: DiffRow): Spot[] =>
    r.kind === "del" ? [{ side: "old", line: r.old! }] : r.kind === "add" ? [{ side: "new", line: r.new! }] : r.kind === "context" ? [{ side: "new", line: r.new! }, { side: "old", line: r.old! }] : [];

  // rows drawn: a hunk's (h its place, for its highlighting and words) or a gap's (h -1, highlighted on their own)
  const drawRows = (rows: DiffRow[], h: number, k: string, spans?: Map<number, Span[]>) => {
    const gapLit = h < 0 ? gapTokens(rows) : null;
    const tok = (r: DiffRow, i: number, side: Side) => (h < 0 ? gapLit?.[i] : tokensOf(h, r, side));
    if (!split)
      return rows.map((r, i) => (
        <Fragment key={`${k}:${i}`}>
          <UnifiedLine r={r} tokens={tok(r, i, r.kind === "del" ? "old" : "new")} spans={spans?.get(i)} width={width} picker={picker} pick={pick} change={r.kind !== "context" && r.kind !== "note" && (i === 0 || rows[i - 1].kind === "context")} />
          {under(spotsOf(r), `${k}:${i}`)}
        </Fragment>
      ));
    const index = new Map(rows.map((r, i) => [r, i]));
    return splitRows(rows).map((sr, i, all) => {
      if (sr.note !== undefined)
        return (
          <div key={`${k}:${i}`} className="px-3 text-[11.5px] text-ink-3 italic">
            {sr.note}
          </div>
        );
      const oi = sr.old ? index.get(sr.old)! : -1;
      const ni = sr.new ? index.get(sr.new)! : -1;
      const changed = sr.old?.kind !== "context";
      const prevChanged = i > 0 && all[i - 1].old?.kind !== "context" && all[i - 1].note === undefined;
      const spots: Spot[] = sr.old?.kind === "context" ? spotsOf(sr.old) : [...(sr.old ? spotsOf(sr.old) : []), ...(sr.new ? spotsOf(sr.new) : [])];
      return (
        <Fragment key={`${k}:${i}`}>
          <SplitLine
            old={sr.old}
            nw={sr.new}
            tokOld={sr.old ? tok(sr.old, oi, "old") : undefined}
            tokNew={sr.new ? tok(sr.new, ni, "new") : undefined}
            spanOld={oi >= 0 ? spans?.get(oi) : undefined}
            spanNew={ni >= 0 ? spans?.get(ni) : undefined}
            width={width}
            picker={picker}
            pick={pick}
            change={changed && !prevChanged}
          />
          {under(spots, `${k}:${i}`)}
        </Fragment>
      );
    });
  };

  // the lines hidden before hunk i (i = hunks: after the last), asked of the extension
  const gapView = (i: number) => {
    const g = gapList[i];
    if (!g) return null;
    const { top, bottom, hidden } = gapRows(g, shown[g.at] ?? START, cache);
    const header = raw[i]?.header;
    return (
      <>
        {drawRows(top, -1, `g${i}t`)}
        {hidden > 0 && <GapBar hidden={hidden} first={i === 0} last={i === raw.length} header={header} loading={gapBusy === i || !!local && !!busy} onShow={(d) => void showGap(g, d)} />}
        {drawRows(bottom, -1, `g${i}b`)}
        {hidden === 0 && header && top.length + bottom.length === 0 && i < raw.length && (
          <div className="flex h-7 items-center bg-accent-tint/40 text-[11.5px] text-ink-3">
            <span className="sticky left-0 truncate px-3 whitespace-pre">{header}</span>
          </div>
        )}
      </>
    );
  };
  // an extension without context: its expand draws the page again (UI version 4)
  const oldGap = (h: number) => {
    const n = gapBefore(raw, h);
    if (!item.expand || n === 0) return null;
    return (
      <div className="flex h-8 items-center bg-accent-tint/50 font-sans text-[11.5px] text-accent-ink">
        <span className="sticky left-0 flex items-center gap-1 px-2">
          {(["up", ...(h > 0 && n > 20 ? ["down"] : [])] as const).map((dir) => (
            <button
              key={dir}
              type="button"
              disabled={!!busy}
              onClick={() => act(item.expand!, { path, hunk: String(h), direction: dir })}
              aria-label={dir === "up" ? t("extensions-codeviews.expand_up") : t("extensions-codeviews.expand_down")}
              title={dir === "up" ? t("extensions-codeviews.expand_up") : t("extensions-codeviews.expand_down")}
              className="flex size-6 items-center justify-center rounded-[6px] transition-colors enabled:hover:bg-hover"
            >
              <Icon name={dir === "up" ? "up" : "arrowDown"} size={12} stroke={2.2} />
            </button>
          ))}
          <span className="whitespace-nowrap text-ink-3">{t("extensions-codeviews.hidden_lines", { count: n })}</span>
        </span>
      </div>
    );
  };
  const hunkHeader = (h: Hunk, i: number) =>
    !item.context && h.header && (!item.expand || gapBefore(raw, i) === 0) ? (
      <div className="flex h-7 items-center bg-accent-tint/40 text-[11.5px] text-ink-3">
        <span className="sticky left-0 truncate px-3 whitespace-pre">{h.header}</span>
      </div>
    ) : null;

  const body = () => {
    if (item.binary) return <p className="px-4 py-4 text-center text-[12.5px] text-ink-3">{t("extensions-codeviews.binary")}</p>;
    if (!raw.length) {
      if (item.status === "renamed" && !item.load) return <p className="px-4 py-4 text-center text-[12.5px] text-ink-3">{t("extensions-review.renamed_only")}</p>;
      if (!item.load) return <p className="px-4 py-4 text-center text-[12.5px] text-ink-3">{note ?? t("extensions-codeviews.no_changes")}</p>;
      if (loadState === "loading")
        return (
          <div className="flex items-center justify-center gap-2 px-4 py-3 text-[12.5px] text-ink-3">
            <Spinner />
            {t("extensions-review.loading_diff")}
          </div>
        );
      return <LoadLine busy={busy === item.load.id} disabled={!!busy} onClick={() => act(item.load!, { path })} title={reason === "generated" ? t("extensions-review.load_generated") : t("extensions-codeviews.load_diff")} />;
    }
    if (allWhitespace) return <p className="px-4 py-4 text-center text-[12.5px] text-ink-3">{t("extensions-review.whitespace_only")}</p>;
    return (
      <div className="code-hl overflow-x-auto font-mono text-[12px]" style={{ lineHeight: `${LH}px` }}>
        <div className={cx(split ? "min-w-full" : "min-w-max")}>
          {raw.map((h, hi) => (
            <Fragment key={hi}>
              {item.context ? gapView(hi) : oldGap(hi)}
              {hunkHeader(h, hi)}
              <div className={cx(rowsOf[hi].length > 300 && "code-chunk")}>{drawRows(rowsOf[hi], hi, `h${hi}`, spansOf[hi])}</div>
            </Fragment>
          ))}
          {item.context
            ? gapView(raw.length)
            : item.expand && (
                <div className="flex h-8 items-center bg-accent-tint/50 font-sans text-[11.5px] text-accent-ink">
                  <span className="sticky left-0 flex items-center gap-1 px-2">
                    <button
                      type="button"
                      disabled={!!busy}
                      onClick={() => act(item.expand!, { path, hunk: String(raw.length - 1), direction: "down" })}
                      aria-label={t("extensions-codeviews.expand_down")}
                      title={t("extensions-codeviews.expand_down")}
                      className="flex size-6 items-center justify-center rounded-[6px] transition-colors enabled:hover:bg-hover"
                    >
                      <Icon name="arrowDown" size={12} stroke={2.2} />
                    </button>
                  </span>
                </div>
              )}
        </div>
        {rest.length > 0 && (
          <div className="border-t border-line py-1 font-sans">
            <p className="px-4 pt-1.5 text-[11.5px] text-ink-3">{t("extensions-codeviews.elsewhere")}</p>
            {rest.map((th, i) => (
              <ThreadCard key={th.id ?? i} thread={th} local={scopeLocal(local, `thread/${th.id ?? `${th.side}:${th.line}`}`)} act={act} busy={busy} original={[]} />
            ))}
          </div>
        )}
      </div>
    );
  };

  const folded = !open && raw.length > 0 && reason && reason !== "collapsed" ? FOLD_NOTE[reason] : undefined;
  return (
    <div ref={root} className={cx(card, "code-lazy scroll-mt-3")} data-diff-path={path}>
      <div className={cx("sticky top-0 z-[2] flex h-10 items-center gap-2 bg-surface px-2.5", open && "border-b border-line")}>
        <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} aria-label={open ? t("extensions-codeviews.collapse") : t("extensions-codeviews.expand")} className="flex size-7 shrink-0 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-hover hover:text-ink">
          <Icon name="chevronRight" size={12} stroke={2.2} className={cx("transition-transform duration-150", open && "rotate-90")} />
        </button>
        {mark && (
          <span className={cx("w-3 shrink-0 text-center font-mono text-[11px] font-semibold", MARK[mark.tone])} aria-label={item.status}>
            {mark.letter}
          </span>
        )}
        <span className={cx("min-w-0 flex-1 truncate font-mono text-[12px] select-text", viewed ? "text-ink-3" : "text-ink")} title={item.old_path && item.old_path !== path ? `${item.old_path} → ${path}` : path} dir="rtl" style={{ textAlign: "left" }}>
          <bdi>
            {item.old_path && item.old_path !== path && <span className="text-ink-3">{item.old_path} → </span>}
            {path}
          </bdi>
        </span>
        {item.status === "renamed" && item.similarity !== undefined && <span className="hidden shrink-0 text-[11px] text-ink-3 tabular-nums sm:inline">{t("extensions-review.similar", { n: item.similarity })}</span>}
        {reason === "generated" && <span className="hidden shrink-0 sm:inline"><Pill>{t("extensions-review.generated")}</Pill></span>}
        {threads > 0 && (
          <span className="flex shrink-0 items-center gap-0.5 text-[11.5px] text-ink-3 tabular-nums">
            <Icon name="chat" size={12} />
            {threads}
          </span>
        )}
        <span className="shrink-0 font-mono text-[11.5px] tabular-nums">
          <span className="text-green">+{item.additions ?? 0}</span> <span className="text-red">−{item.deletions ?? 0}</span>
        </span>
        <button
          type="button"
          role="checkbox"
          disabled={local ? !!busy : undefined}
          aria-checked={viewed}
          onClick={() => setViewed(!viewed)}
          title={`${t("extensions-review.viewed")} (v)`}
          className={cx("flex h-7 shrink-0 items-center gap-1.5 rounded-[8px] px-1.5 text-[11.5px] whitespace-nowrap transition-colors hover:bg-hover", viewed ? "text-green" : "text-ink-3")}
        >
          <span className={cx("flex size-3.5 items-center justify-center rounded-[4px]", viewed ? "bg-green-tint text-green" : "border border-line-strong")}>{viewed && <Icon name="check" size={10} stroke={3} />}</span>
          <span className="hidden sm:inline">{t("extensions-review.viewed")}</span>
        </button>
        {roomy && open && raw.length > 0 && (
          <div role="radiogroup" aria-label={t("extensions-codeviews.view_mode")} className="ml-1 hidden shrink-0 rounded-[8px] bg-field p-0.5 lg:flex">
            {(["unified", "split"] as const).map((m) => (
              <button
                key={m}
                type="button"
                role="radio"
                aria-checked={mode === m}
                onClick={() => setMode(m)}
                className={cx("h-6 rounded-[6px] px-2 text-[11.5px] whitespace-nowrap transition-colors", mode === m ? "bg-surface text-ink shadow-btn" : "text-ink-3 hover:text-ink")}
              >
                {m === "unified" ? t("extensions-codeviews.unified") : t("extensions-codeviews.split")}
              </button>
            ))}
          </div>
        )}
      </div>
      {folded && (
        <button type="button" onClick={() => setOpen(true)} className="flex w-full items-center justify-center gap-1.5 border-t border-line px-4 py-2.5 text-[12.5px] whitespace-nowrap text-ink-3 transition-colors hover:bg-hover hover:text-ink-2">
          {t(folded)}
          <span className="text-accent-ink">{t("extensions-review.show_diff")}</span>
        </button>
      )}
      {(open || !raw.length) && body()}
    </div>
  );
}
