// The table viewer (docs/web/web.md "Sheets"; iOS / Mac: #1294): CSV / TSV / xlsx / xls / ods / numbers as a grid with
// column letters and row numbers that stay put, the first row frozen as a header, columns that fit their text and
// can be dragged wider, a selection to copy (⌘C pastes into any spreadsheet) with its sum, and a tab per sheet. Only
// the cells on screen are drawn, so a sheet of half a million rows scrolls like a short one.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  cellName,
  columnAt,
  columnName,
  columnWidths,
  coveredCells,
  expandToMerges,
  looksNumeric,
  MIN_COL,
  normalize,
  offsetsOf,
  rangeSummary,
  rangeTSV,
  type Range,
  type Sheet,
} from "../../../lib/sheets";
import { copyText, cx } from "../../../ui/atoms";
import { useStoredFlag } from "./parts";
import { formatNumber, t } from "../../../i18n";

const RH = 26; // px per row
const HEAD = 24; // the column letters' row
const OVER_ROWS = 12;
const OVER_COLS = 3;
const STEP_ROWS = 8; // rows of scrolling between redraws
const STEP_X = 256; // px of sideways scrolling between redraws

type Cell = { r: number; c: number };

/**
 * The part of the grid worth drawing at a scroll position: body rows [r0, r1) (after the frozen ones) and the span
 * [x0, x1) px across the columns, each covering what is on screen within the current step. It moves in steps, so
 * scrolling redraws once per step and not on every pixel.
 */
function windowAt(top: number, left: number, w: number, h: number) {
  const first = Math.floor(top / (STEP_ROWS * RH)) * STEP_ROWS;
  const x0 = Math.floor(left / STEP_X) * STEP_X;
  return { r0: Math.max(0, first - OVER_ROWS), r1: first + STEP_ROWS + Math.ceil(h / RH) + OVER_ROWS, x0, x1: x0 + STEP_X + w };
}

export function SheetView({ sheets, note }: { sheets: Sheet[]; note?: string }) {
  const [active, setActive] = useState(0);
  const sheet = sheets[Math.min(active, sheets.length - 1)];
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* a new sheet starts with its own widths, selection and scroll */}
      <Grid key={active} sheet={sheet} />
      {(sheet.cut || note) && (
        <div className="shrink-0 border-t border-line px-3 py-1.5 text-[11.5px] text-ink-3">
          {[note, sheet.cut ? t("session-files-sheetview.truncated", { count: formatNumber(sheet.rows.length) }) : ""].filter(Boolean).join(" · ")}
        </div>
      )}
      {sheets.length > 1 && (
        <div role="tablist" aria-label={t("session-files-sheetview.sheets")} className="flex h-9 shrink-0 items-center gap-1 overflow-x-auto border-t border-line bg-inset px-2">
          {sheets.map((s, i) => (
            <button
              key={i}
              type="button"
              role="tab"
              aria-selected={i === active}
              onClick={() => setActive(i)}
              className={cx(
                "h-7 max-w-48 shrink-0 truncate whitespace-nowrap rounded-[6px] px-2.5 text-[12px] font-medium transition-[background-color,color,box-shadow] duration-150",
                i === active ? "bg-surface text-ink shadow-btn" : "text-ink-3 hover:bg-hover hover:text-ink",
              )}
            >
              {s.name || t("session-files-sheetview.sheet_n", { n: i + 1 })}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function Grid({ sheet }: { sheet: Sheet }) {
  const { rows, merges } = sheet;
  const cols = Math.max(1, sheet.cols);
  const [freeze, setFreeze] = useStoredFlag("files.sheet.freeze", true);
  const frozen = freeze && rows.length > 1 ? 1 : 0;
  const [widths, setWidths] = useState(() => columnWidths(sheet));
  const offsets = useMemo(() => offsetsOf(widths), [widths]);
  const total = offsets[offsets.length - 1] ?? 0;
  const digits = String(Math.max(rows.length, 9)).length;
  const RN = Math.round(18 + digits * 7.5); // the row numbers' column
  const FR = frozen * RH;
  const bodyRows = Math.max(0, rows.length - frozen);
  const covered = useMemo(() => coveredCells(merges), [merges]);

  const scroller = useRef<HTMLDivElement>(null);
  const [win, setWin] = useState(() => windowAt(0, 0, 800, 600));
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    let raf = 0;
    const update = () => {
      raf = 0;
      const n = windowAt(el.scrollTop, el.scrollLeft, el.clientWidth, el.clientHeight);
      setWin((w) => (w.r0 === n.r0 && w.r1 === n.r1 && w.x0 === n.x0 && w.x1 === n.x1 ? w : n));
    };
    const later = () => {
      if (!raf) raf = requestAnimationFrame(update);
    };
    update();
    el.addEventListener("scroll", later, { passive: true });
    const ro = new ResizeObserver(later);
    ro.observe(el);
    return () => {
      el.removeEventListener("scroll", later);
      ro.disconnect();
      cancelAnimationFrame(raf);
    };
  }, []);

  // what is drawn, in body rows (after the frozen ones) and columns; the columns follow the current widths
  const b0 = win.r0;
  const b1 = Math.min(bodyRows, win.r1);
  const c0 = Math.max(0, columnAt(offsets, win.x0) - OVER_COLS);
  const c1 = Math.min(cols, columnAt(offsets, win.x1) + 1 + OVER_COLS);

  const [anchor, setAnchor] = useState<Cell>({ r: 0, c: 0 });
  const [focus, setFocus] = useState<Cell>({ r: 0, c: 0 });
  const sel: Range = useMemo(() => expandToMerges(normalize(anchor, focus), merges), [anchor, focus, merges]);
  const selRef = useRef(sel);
  selRef.current = sel;

  /** Brings a cell into view (keyboard moves). */
  const reveal = useCallback(
    (cell: Cell) => {
      const el = scroller.current;
      if (!el) return;
      const x0 = offsets[cell.c] ?? 0;
      const x1 = offsets[cell.c + 1] ?? x0;
      const visW = el.clientWidth - RN;
      if (x0 < el.scrollLeft) el.scrollLeft = x0;
      else if (x1 > el.scrollLeft + visW) el.scrollLeft = x1 - visW;
      if (cell.r < frozen) return;
      const y0 = (cell.r - frozen) * RH;
      const visH = el.clientHeight - HEAD - FR;
      if (y0 < el.scrollTop) el.scrollTop = y0;
      else if (y0 + RH > el.scrollTop + visH) el.scrollTop = y0 + RH - visH;
    },
    [offsets, RN, frozen, FR],
  );

  // pointer: press a cell, drag to extend, shift-press to extend from the anchor
  const cellAt = (e: { clientX: number; clientY: number }): Cell | null => {
    const el = scroller.current;
    if (!el) return null;
    const box = el.getBoundingClientRect();
    const x = e.clientX - box.left + el.scrollLeft - RN;
    const yView = e.clientY - box.top;
    if (x < -RN) return null;
    const c = Math.max(0, Math.min(cols - 1, columnAt(offsets, Math.max(0, x))));
    let r: number;
    if (yView < HEAD) r = -1;
    else if (yView < HEAD + FR) r = 0;
    else r = frozen + Math.floor((yView - HEAD - FR + el.scrollTop) / RH);
    return { r: Math.min(r, rows.length - 1), c: x < 0 ? -1 : c };
  };
  const drag = useRef<null | "cells" | "rows" | "cols">(null);
  const dragTo = useRef<Cell>({ r: 0, c: 0 }); // the cell the drag reached, so moves within it set no state
  const onPointerDown = (e: React.PointerEvent) => {
    if (e.button !== 0 || e.target === scroller.current || (e.target as HTMLElement).dataset.resize) return; // the scroller itself: its scrollbars
    const hit = cellAt(e);
    if (!hit || rows.length === 0) return;
    e.preventDefault(); // no native text selection: dragging it would scroll the grid on its own
    scroller.current?.focus({ preventScroll: true });
    const last = { r: rows.length - 1, c: cols - 1 };
    if (hit.r < 0 && hit.c < 0) {
      setAnchor({ r: 0, c: 0 });
      setFocus(last);
      return;
    }
    if (hit.r < 0) {
      drag.current = "cols";
      if (!e.shiftKey) setAnchor({ r: 0, c: hit.c });
      dragTo.current = { r: last.r, c: hit.c };
    } else if (hit.c < 0) {
      drag.current = "rows";
      if (!e.shiftKey) setAnchor({ r: hit.r, c: 0 });
      dragTo.current = { r: hit.r, c: last.c };
    } else {
      drag.current = "cells";
      if (!e.shiftKey) setAnchor(hit);
      dragTo.current = hit;
    }
    setFocus(dragTo.current);
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  };
  const onPointerMove = (e: React.PointerEvent) => {
    if (!drag.current) return;
    const hit = cellAt(e);
    if (!hit) return;
    const r = Math.max(0, hit.r);
    const c = Math.max(0, hit.c);
    const next = drag.current === "cols" ? { r: rows.length - 1, c } : drag.current === "rows" ? { r, c: cols - 1 } : { r, c };
    if (next.r === dragTo.current.r && next.c === dragTo.current.c) return; // a move inside the same cell draws nothing
    dragTo.current = next;
    setFocus(next);
  };
  const onPointerUp = () => {
    drag.current = null;
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key.toLowerCase() === "c") {
      e.preventDefault();
      void copyText(rangeTSV(rows, selRef.current));
      return;
    }
    if (mod && e.key.toLowerCase() === "a") {
      e.preventDefault();
      setAnchor({ r: 0, c: 0 });
      setFocus({ r: Math.max(0, rows.length - 1), c: cols - 1 });
      return;
    }
    const step: Record<string, [number, number]> = { ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1], Tab: [0, e.shiftKey ? -1 : 1], Enter: [e.shiftKey ? -1 : 1, 0] };
    const d = step[e.key];
    if (!d) return;
    e.preventDefault();
    const from = focus;
    const far = mod; // ⌘↓: to the end, like spreadsheets
    const next = {
      r: Math.max(0, Math.min(rows.length - 1, far && d[0] ? (d[0] > 0 ? rows.length - 1 : 0) : from.r + d[0])),
      c: Math.max(0, Math.min(cols - 1, far && d[1] ? (d[1] > 0 ? cols - 1 : 0) : from.c + d[1])),
    };
    if (!(e.shiftKey && e.key.startsWith("Arrow"))) setAnchor(next);
    setFocus(next);
    reveal(next);
  };

  // column widths: drag a header's right edge; double-click fits it to its text again
  const resize = useRef<{ c: number; x: number; w: number } | null>(null);
  const fitted = useMemo(() => columnWidths(sheet), [sheet]);
  const onResizeDown = (c: number) => (e: React.PointerEvent) => {
    e.stopPropagation();
    resize.current = { c, x: e.clientX, w: widths[c] };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  };
  const onResizeMove = (e: React.PointerEvent) => {
    const r = resize.current;
    if (!r) return;
    const w = Math.max(MIN_COL, Math.round(r.w + e.clientX - r.x));
    setWidths((ws) => (ws[r.c] === w ? ws : ws.map((x, i) => (i === r.c ? w : x))));
  };
  const onResizeUp = () => {
    resize.current = null;
  };

  useEffect(() => {
    scroller.current?.focus({ preventScroll: true });
  }, []);

  const summary = useMemo(() => rangeSummary(rows, sel), [rows, sel]);
  const one = merges.find((m) => m.r0 === sel.r0 && m.c0 === sel.c0 && m.r1 === sel.r1 && m.c1 === sel.c1);
  const single = (sel.r0 === sel.r1 && sel.c0 === sel.c1) || !!one; // a merged block reads as its one cell
  const value = rows[sel.r0]?.[sel.c0] ?? "";

  const drawCell = (r: number, c: number, top: number, key: string) => {
    if (covered.has(`${r}:${c}`)) return null;
    const m = merges.find((m) => m.r0 === r && m.c0 === c);
    const text = rows[r]?.[c] ?? "";
    const w = m ? offsets[m.c1 + 1] - offsets[c] : widths[c];
    const h = m ? (m.r1 - m.r0 + 1) * RH : RH;
    return (
      <div
        key={key}
        className={cx(
          "absolute truncate border-r border-b border-line px-2 text-[12.5px] leading-[25px] text-ink",
          looksNumeric(text) && "text-right tabular-nums",
          m && "flex items-center justify-center bg-surface",
          r < frozen && "font-semibold",
        )}
        style={{ left: offsets[c], top, width: w, height: h }}
      >
        {text}
      </div>
    );
  };

  const body: React.ReactNode[] = [];
  for (let b = b0; b < b1; b++) {
    const r = b + frozen;
    for (let c = c0; c < c1; c++) body.push(drawCell(r, c, b * RH, `${r}:${c}`));
  }
  // a merged block whose top-left cell is off screen but which reaches onto it
  for (const m of merges) {
    if (m.r0 < frozen) continue;
    const mb0 = m.r0 - frozen;
    const mb1 = m.r1 - frozen;
    const startsOff = mb0 < b0 || m.c0 < c0;
    if (startsOff && mb1 >= b0 && mb0 < b1 && m.c1 >= c0 && m.c0 < c1) body.push(drawCell(m.r0, m.c0, mb0 * RH, `m${m.r0}:${m.c0}`));
  }
  const frozenCells: React.ReactNode[] = [];
  if (frozen) for (let c = c0; c < c1; c++) frozenCells.push(drawCell(0, c, 0, `f${c}`));

  // the selection's outline, in the body and (where it covers the frozen row) in the header
  const selX = offsets[sel.c0];
  const selW = offsets[sel.c1 + 1] - selX;
  const bodySel = sel.r1 >= frozen && (
    <div
      className="pointer-events-none absolute border-2 border-accent bg-accent/8"
      style={{ left: selX, width: selW, top: (Math.max(sel.r0, frozen) - frozen) * RH - 1, height: (sel.r1 - Math.max(sel.r0, frozen) + 1) * RH + 1 }}
    />
  );
  const headSel = frozen > 0 && sel.r0 < frozen && (
    <div className="pointer-events-none absolute z-10 border-2 border-accent bg-accent/8" style={{ left: RN + selX, width: selW, top: HEAD - 1, height: RH + 1 }} />
  );

  const letters: React.ReactNode[] = [];
  for (let c = c0; c < c1; c++) {
    const on = c >= sel.c0 && c <= sel.c1;
    letters.push(
      <div
        key={c}
        className={cx(
          "absolute top-0 border-r border-b border-line text-center text-[11px] leading-[23px] font-medium select-none",
          on ? "bg-[color-mix(in_srgb,var(--accent)_14%,var(--inset))] text-accent-ink" : "bg-inset text-ink-3",
        )}
        style={{ left: RN + offsets[c], width: widths[c], height: HEAD }}
      >
        {columnName(c)}
        <span
          data-resize="1"
          aria-hidden
          onPointerDown={onResizeDown(c)}
          onPointerMove={onResizeMove}
          onPointerUp={onResizeUp}
          onDoubleClick={() => setWidths((ws) => ws.map((x, i) => (i === c ? fitted[c] : x)))}
          className="absolute top-0 -right-[3px] z-10 h-full w-[6px] cursor-col-resize hover:bg-accent/40"
        />
      </div>,
    );
  }
  const numbers: React.ReactNode[] = [];
  for (let b = b0; b < b1; b++) {
    const r = b + frozen;
    const on = r >= sel.r0 && r <= sel.r1;
    numbers.push(
      <div
        key={r}
        className={cx("absolute right-0 left-0 border-r border-b border-line pr-2 text-right text-[11px] leading-[25px] tabular-nums select-none", on ? "bg-[color-mix(in_srgb,var(--accent)_14%,var(--inset))] text-accent-ink" : "bg-inset text-ink-3")}
        style={{ top: b * RH, height: RH }}
      >
        {r + 1}
      </div>,
    );
  }

  if (rows.length === 0) return <div className="flex flex-1 items-center justify-center text-[13px] text-ink-3">{t("session-files-sheetview.empty")}</div>;

  return (
    <>
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-line px-3">
        <span className="shrink-0 rounded-[6px] bg-field px-2 py-0.5 font-mono text-[11.5px] text-ink-2 tabular-nums">
          {single ? cellName(sel.r0, sel.c0) : `${cellName(sel.r0, sel.c0)}:${cellName(sel.r1, sel.c1)}`}
        </span>
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink" title={value}>
          {single ? value : ""}
        </span>
        <span className="shrink-0 text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums">
          {single
            ? t("session-files-sheetview.size", { rows: formatNumber(rows.length), cols })
            : [t("session-files-sheetview.count", { n: formatNumber(summary.count) }), summary.sum !== undefined && t("session-files-sheetview.sum", { n: fmt(summary.sum) }), summary.avg !== undefined && t("session-files-sheetview.average", { n: fmt(summary.avg) })].filter(Boolean).join(" · ")}
        </span>
        <button
          type="button"
          aria-pressed={freeze}
          onClick={() => setFreeze(!freeze)}
          className={cx(
            "h-6 shrink-0 rounded-[6px] px-2 text-[12px] font-medium whitespace-nowrap transition-colors",
            freeze ? "bg-accent/12 text-accent-ink" : "text-ink-3 hover:bg-hover hover:text-ink",
          )}
        >
          {t("session-files-sheetview.freeze")}
        </button>
      </div>
      <div
        ref={scroller}
        tabIndex={0}
        role="grid"
        aria-rowcount={rows.length}
        aria-colcount={cols}
        onKeyDown={onKeyDown}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        className="relative min-h-0 flex-1 overflow-auto bg-surface outline-none select-none"
      >
        <div className="relative" style={{ width: RN + total, height: HEAD + FR + bodyRows * RH }}>
          <div className="sticky top-0 z-20" style={{ height: HEAD + FR, width: RN + total }}>
            <div className="sticky left-0 z-20 border-r border-b border-line bg-inset" style={{ width: RN, height: HEAD + FR }}>
              {frozen > 0 && (
                <div
                  className={cx("absolute right-0 left-0 border-t border-line pr-2 text-right text-[11px] leading-[25px] tabular-nums", sel.r0 === 0 ? "text-accent-ink" : "text-ink-3")}
                  style={{ top: HEAD, height: RH }}
                >
                  1
                </div>
              )}
            </div>
            {letters}
            {frozen > 0 && (
              <div className="absolute bg-surface shadow-[0_1px_0_var(--line-strong)]" style={{ left: RN, top: HEAD, width: total, height: FR }}>
                {frozenCells}
              </div>
            )}
            {headSel}
          </div>
          <div className="sticky left-0 z-10" style={{ width: RN, height: bodyRows * RH }}>
            {numbers}
          </div>
          <div className="absolute" style={{ left: RN, top: HEAD + FR, width: total, height: bodyRows * RH }}>
            {body}
            {bodySel}
          </div>
        </div>
      </div>
    </>
  );
}

function fmt(n: number) {
  return Math.abs(n) >= 1e15 || (Math.abs(n) < 1e-4 && n !== 0) ? n.toExponential(4) : n.toLocaleString(undefined, { maximumFractionDigits: 4 });
}
