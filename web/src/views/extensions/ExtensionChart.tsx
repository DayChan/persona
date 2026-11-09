// An extension's chart (docs/extensions/extensions.md "UI slots" "New items" chart, "UI version 6"): bars (side by side or stacked), lines,
// and in UI version 6 areas (stacked or not), candlesticks (with the volume under them) and donuts / pies, drawn with
// the web's own colors (a tone is a role color, a named color its token, the rest the series palette in order). Thin
// marks with a rounded free end, a 2px surface gap between touching bars, hairline grid, short numbers on the right;
// labels thinned so they never overlap. Hovering (or the arrow keys) shows the label and every series' value (a
// candle's open, high, low, close and volume; a slice's share). A compact chart (a card) draws no axis text: only the
// marks and the first / last label. The geometry is in lib/extension-ui.ts and lib/extension-ui-v6.ts.
import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { barSpans, chartExtent, compactNumber, edgeLabels, labelWidth, linear, niceTicks, pointText, seriesColors, sliceColors, thinLabels, type ChartPoint, type UIItem } from "../../lib/extension-ui";
import { arcPath, areaBands, candleColors, candleExtent, candleOf, shareText, slices } from "../../lib/extension-ui-v6";
import { t } from "../../i18n";
import { cx } from "../../ui/atoms";
import { ChartFrame, LABEL_H, plotHeight } from "./ChartFrame";
import { useTween } from "../../ui/motion";

const FONT = 10.5; // axis text, px
const TOP = 6;
const VOL_GAP = 6; // between a candlestick's prices and its volume

type XYKind = "bar" | "line" | "area" | "candlestick";

function useWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = useRef<T>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.clientWidth);
    const ro = new ResizeObserver(([e]) => setW(Math.round(e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

/** A bar from its base to its end, the free end rounded (square at the base). */
function barPath(x: number, w: number, base: number, end: number, r: number, round: boolean): string {
  const h = Math.abs(end - base);
  const rr = round ? Math.min(r, w / 2, h) : 0;
  if (end <= base) {
    // grows up
    return `M${x},${base}V${end + rr}Q${x},${end} ${x + rr},${end}H${x + w - rr}Q${x + w},${end} ${x + w},${end + rr}V${base}Z`;
  }
  return `M${x},${base}V${end - rr}Q${x},${end} ${x + rr},${end}H${x + w - rr}Q${x + w},${end} ${x + w},${end - rr}V${base}Z`;
}

const KIND_NAME: Record<string, () => string> = {
  bar: () => t("extensions-extensionchart.kind_bar"),
  line: () => t("extensions-extensionchart.kind_line"),
  area: () => t("extensions-extensionchart.kind_area"),
  candlestick: () => t("extensions-extensionchart.kind_candlestick"),
  donut: () => t("extensions-extensionchart.kind_donut"),
  pie: () => t("extensions-extensionchart.kind_pie"),
};

/** What a screen reader hears first: the chart's title, its kind, how many points and which series. */
function summaryOf(item: UIItem, kind: string, n: number): string {
  const series = item.series ?? [];
  const kindName = (KIND_NAME[kind] ?? KIND_NAME.bar)();
  const body =
    series.length > 1
      ? t("extensions-extensionchart.summary_series", { kind: kindName, count: n, series: series.map((s) => s.name).filter(Boolean).join(t("extensions-extensionchart.series_sep")) })
      : t("extensions-extensionchart.summary", { kind: kindName, count: n });
  return item.title ? t("extensions-extensionchart.titled", { title: item.title, text: body }) : body;
}

export function ExtensionChart({ item, compact }: { item: UIItem; compact?: boolean }) {
  if (item.kind === "donut" || item.kind === "pie") return <RoundChart item={item} compact={compact} />;
  return <XYChart item={item} compact={compact} />;
}

function XYChart({ item, compact }: { item: UIItem; compact?: boolean }) {
  const all = item.series ?? [];
  const kind: XYKind = item.kind === "line" || item.kind === "area" || item.kind === "candlestick" ? item.kind : "bar";
  // a candlestick has one series (the hub checks it); a point that is not a whole candle is drawn as its close
  const series = useMemo(() => (kind === "candlestick" ? all.slice(0, 1) : all), [kind, all]);
  const stacked = (kind === "bar" || kind === "area") && !!item.stacked && series.length > 1;
  const labels = useMemo(() => (series[0]?.points ?? []).map((p) => p.label), [series]);
  const n = labels.length;
  const colors = useMemo(() => seriesColors(series), [series]);
  const candles = candleColors(item.rising);
  const [box, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  // the marks go to their new values when the extension draws the same chart (same labels and series) with new numbers
  // (#2757); the axis is the new one at once. A candlestick is drawn from its own open / high / low / close, so it is
  // not tweened.
  const values = useMemo(() => series.flatMap((s) => s.points.map((p) => p.value)), [series]);
  const shape = `${kind}${stacked ? "+" : ""}\n${series.map((s) => s.points.length).join(",")}\n${labels.join("\n")}`;
  const tween = kind === "candlestick" ? null : useTween(values, shape);
  const marks = useMemo(() => {
    if (!tween || tween.length !== values.length) return series;
    let at = 0;
    return series.map((s) => ({ ...s, points: s.points.map((p) => ({ ...p, value: tween[at++] })) }));
  }, [series, tween, values]);

  const banded = kind === "bar" || kind === "candlestick";
  const volume = kind === "candlestick" && n > 0 && (series[0]?.points ?? []).every((p) => typeof p.volume === "number");
  const full = plotHeight(compact);
  // a candlestick's volume takes the bottom quarter, its prices the rest
  const volH = volume ? Math.round(full * (compact ? 0.3 : 0.25)) : 0;
  const H = full - (volume ? volH + VOL_GAP : 0);
  const [mn, mx] = useMemo(() => (kind === "candlestick" ? candleExtent(series[0]?.points ?? []) : chartExtent(series, kind, stacked)), [series, kind, stacked]);
  const axis = niceTicks(mn, mx, compact ? 2 : 4, kind !== "candlestick");
  // the numbers on the right take what the widest needs
  const maxVol = volume ? Math.max(1, ...(series[0]?.points ?? []).map((p) => p.volume ?? 0)) : 1;
  const axisW = compact ? 0 : Math.max(28, ...[...axis.ticks, ...(volume ? [maxVol] : [])].map((v) => labelWidth(compactNumber(v), FONT) + 8));
  const plotW = Math.max(0, width - axisW);
  const y = linear(axis.lo, axis.hi, H, TOP);
  const zero = y(Math.max(axis.lo, Math.min(axis.hi, 0)));
  const volTop = H + VOL_GAP;
  const vy = (v: number) => volTop + volH - (v / maxVol) * volH;

  // where each label sits: a band's middle (bars, candles) or a point (lines, areas: inset so the end dots stay inside)
  const band = n ? plotW / n : 0;
  const pad = compact ? 4 : 6;
  const xs = useMemo(
    () => labels.map((_, i) => (banded ? band * (i + 0.5) : n === 1 ? plotW / 2 : pad + (i * (plotW - 2 * pad)) / (n - 1))),
    [labels, banded, band, n, plotW, pad],
  );
  const shown = useMemo(() => (plotW > 0 ? (compact ? edgeLabels(labels, plotW, FONT) : thinLabels(labels, xs, plotW, FONT)) : []), [compact, labels, xs, plotW]);

  // bars: a group's width, each bar at most 24px (compact 16px), 2px apart; a candle at most 12px
  const m = stacked ? 1 : series.length;
  const barW = kind === "bar" ? Math.max(1, Math.min(compact ? 16 : 24, (band * (compact ? 0.66 : 0.62) - 2 * (m - 1)) / m)) : 0;
  const candleW = kind === "candlestick" ? Math.max(1, Math.min(12, band * 0.62)) : 0;
  const spans = useMemo(() => (kind === "bar" ? barSpans(marks, stacked) : []), [kind, marks, stacked]);
  const bands = useMemo(() => (kind === "area" ? areaBands(marks, stacked) : []), [kind, marks, stacked]);

  const nearest = (px: number) => {
    let best = 0;
    xs.forEach((x, i) => {
      if (Math.abs(x - px) < Math.abs(xs[best] - px)) best = i;
    });
    return best;
  };
  const move = (e: React.PointerEvent<SVGRectElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    setHover(nearest(e.clientX - r.left));
  };
  const key = (e: React.KeyboardEvent) => {
    if (!n) return;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      e.preventDefault();
      const at = hover ?? (e.key === "ArrowLeft" ? n : -1);
      setHover(Math.min(n - 1, Math.max(0, at + (e.key === "ArrowLeft" ? -1 : 1))));
    } else if (e.key === "Escape") setHover(null);
  };

  const summary = summaryOf(item, kind, n);
  const lineish = kind === "line" || kind === "area";
  const totalH = full + LABEL_H;

  return (
    <ChartFrame item={item} compact={compact} colors={colors}>
      <div ref={box} className="relative">
        {width > 0 && (
          <svg
            width={width}
            height={totalH}
            role="img"
            aria-label={summary}
            tabIndex={0}
            onKeyDown={key}
            onBlur={() => setHover(null)}
            className="block rounded-[4px] outline-none focus-visible:shadow-[0_0_0_2px_var(--accent)]"
          >
            {/* grid: hairlines at round values, short numbers on the right; a compact chart keeps only the zero line */}
            {!compact &&
              axis.ticks.map((v) => (
                <g key={v}>
                  <line x1={0} x2={plotW} y1={y(v)} y2={y(v)} stroke={v === 0 && kind !== "candlestick" ? "var(--line-strong)" : "var(--line)"} strokeWidth={1} shapeRendering="crispEdges" />
                  <text x={plotW + 6} y={y(v)} dy="0.35em" fontSize={FONT} fill="var(--ink-3)" className="tabular-nums">
                    {compactNumber(v)}
                  </text>
                </g>
              ))}
            {compact && kind !== "candlestick" && <line x1={0} x2={plotW} y1={zero} y2={zero} stroke="var(--line-strong)" strokeWidth={1} shapeRendering="crispEdges" />}

            {/* the hovered column, behind the marks */}
            {hover !== null && banded && <rect x={band * hover} y={TOP - 2} width={band} height={full - TOP + 2} rx={4} fill="var(--ink)" fillOpacity={0.05} pointerEvents="none" />}

            {kind === "bar" &&
              spans.map((col, i) => (
                <g key={i}>
                  {col.map((b) => {
                    const k = stacked ? 0 : b.series;
                    const x = xs[i] - (m * barW + 2 * (m - 1)) / 2 + k * (barW + 2);
                    // 2px of surface between stacked segments: each gives 1px at the side it shares
                    let base = y(b.from);
                    let end = y(b.to);
                    if (Math.abs(end - base) < 0.5) return null;
                    const dir = end < base ? -1 : 1;
                    if (stacked && b.from !== 0) base += dir;
                    if (stacked && !b.end) end -= dir;
                    if ((end - base) * dir <= 0) return null;
                    return <path key={b.series} d={barPath(x, barW, base, end, compact ? 3 : 4, b.end)} fill={colors[b.series]} />;
                  })}
                </g>
              ))}

            {kind === "line" &&
              marks.map((s, k) => {
                const pts = s.points.map((p, i) => `${xs[i].toFixed(1)},${y(p.value).toFixed(1)}`).join(" ");
                if (n === 1) return <circle key={k} cx={xs[0]} cy={y(s.points[0].value)} r={3} fill={colors[k]} />;
                return (
                  <g key={k}>
                    {marks.length === 1 && <polygon points={`${xs[0].toFixed(1)},${zero} ${pts} ${xs[n - 1].toFixed(1)},${zero}`} fill={colors[k]} fillOpacity={0.1} />}
                    <polyline points={pts} fill="none" stroke={colors[k]} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
                  </g>
                );
              })}

            {/* areas: each series filled from the one under it (stacked) or from zero, its line on top */}
            {kind === "area" &&
              bands.map((b, k) => {
                if (n === 1) return <circle key={k} cx={xs[0]} cy={y(b[0].to)} r={3} fill={colors[k]} />;
                const top = b.map((p, i) => `${xs[i].toFixed(1)},${y(p.to).toFixed(1)}`);
                const bottom = b.map((p, i) => `${xs[i].toFixed(1)},${y(p.from).toFixed(1)}`).reverse();
                return (
                  <g key={k}>
                    <polygon points={[...top, ...bottom].join(" ")} fill={colors[k]} fillOpacity={stacked ? 0.32 : series.length > 1 ? 0.14 : 0.2} />
                    <polyline points={top.join(" ")} fill="none" stroke={colors[k]} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
                  </g>
                );
              })}

            {/* candles: the wick from low to high, the body from open to close, rising in its color, falling in the other */}
            {kind === "candlestick" &&
              (series[0]?.points ?? []).map((p, i) => {
                const c = candleOf(p);
                const color = !c || c.up ? candles.up : candles.down;
                const x = xs[i];
                if (!c) return <circle key={i} cx={x} cy={y(p.value)} r={2} fill={color} />;
                const top = y(Math.max(c.open, c.close));
                const bottom = y(Math.min(c.open, c.close));
                return (
                  <g key={i} opacity={hover !== null && hover !== i ? 0.55 : 1}>
                    <line x1={x} x2={x} y1={y(c.high)} y2={y(c.low)} stroke={color} strokeWidth={1} shapeRendering="crispEdges" />
                    <rect x={x - candleW / 2} y={top} width={candleW} height={Math.max(1, bottom - top)} rx={candleW > 4 ? 1 : 0} fill={color} />
                  </g>
                );
              })}
            {volume &&
              (series[0]?.points ?? []).map((p, i) => {
                const c = candleOf(p);
                const top = vy(p.volume ?? 0);
                return <rect key={i} x={xs[i] - candleW / 2} y={top} width={candleW} height={Math.max(0, volTop + volH - top)} fill={!c || c.up ? candles.up : candles.down} fillOpacity={0.45} />;
              })}
            {volume && !compact && (
              <text x={plotW + 6} y={volTop + volH / 2} dy="0.35em" fontSize={FONT} fill="var(--ink-3)" className="tabular-nums">
                {compactNumber(maxVol)}
              </text>
            )}

            {/* the crosshair and the points at the hovered label (lines, areas) */}
            {hover !== null && lineish && (
              <g pointerEvents="none">
                <line x1={xs[hover]} x2={xs[hover]} y1={TOP} y2={H} stroke="var(--ink-3)" strokeWidth={1} shapeRendering="crispEdges" />
                {series.map((s, k) => (
                  <circle key={k} cx={xs[hover]} cy={y(kind === "area" ? (bands[k]?.[hover]?.to ?? 0) : (s.points[hover]?.value ?? 0))} r={4} fill={colors[k]} stroke="var(--surface)" strokeWidth={2} />
                ))}
              </g>
            )}

            {/* labels under the plot: thinned so none overlap; the first and last flush with the edges */}
            {shown.map((i) => {
              const w = labelWidth(labels[i], FONT);
              const anchor = xs[i] - w / 2 < 0 ? "start" : xs[i] + w / 2 > plotW ? "end" : "middle";
              const tx = anchor === "start" ? 0 : anchor === "end" ? plotW : xs[i];
              const dim = hover !== null && hover !== i;
              return (
                <text key={i} x={tx} y={full + 13} textAnchor={anchor} fontSize={FONT} fill="var(--ink-3)" opacity={dim ? 0.55 : 1} className="tabular-nums">
                  {labels[i]}
                </text>
              );
            })}

            {/* the hit area: the whole plot, the nearest label wins */}
            <rect x={0} y={0} width={plotW} height={totalH} fill="transparent" onPointerMove={move} onPointerDown={move} onPointerLeave={() => setHover(null)} style={{ touchAction: "pan-y" }} />
          </svg>
        )}
        {width === 0 && <div style={{ height: totalH }} />}
        {hover !== null && width > 0 && (kind === "candlestick" ? <CandleTip item={item} at={hover} x={xs[hover]} plotW={plotW} /> : <Tip item={item} at={hover} x={xs[hover]} plotW={plotW} colors={colors} kind={kind} />)}
        {/* the same numbers as a table, for screen readers (in a div: a table grows past sr-only's 1px) */}
        <div className="sr-only">{kind === "candlestick" ? <CandleTable item={item} /> : <SeriesTable item={item} />}</div>
      </div>
    </ChartFrame>
  );
}

function SeriesTable({ item }: { item: UIItem }) {
  const series = item.series ?? [];
  const labels = (series[0]?.points ?? []).map((p) => p.label);
  return (
    <table>
      <thead>
        <tr>
          <th scope="col">{item.title || t("extensions-extensionchart.label")}</th>
          {series.map((s, k) => (
            <th key={k} scope="col">
              {s.name || item.unit || t("extensions-extensionchart.value")}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {labels.map((l, i) => (
          <tr key={i}>
            <th scope="row">{l}</th>
            {series.map((s, k) => (
              <td key={k}>{s.points[i] ? pointText(s.points[i], item.unit) : ""}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** A number of a candle, written with the chart's unit (its display for the close). */
const priceText = (v: number | undefined, unit?: string) => (typeof v === "number" ? `${compactNumber(v)}${unit ? ` ${unit}` : ""}` : "");

function candleFacts(p: ChartPoint, unit?: string): [string, string][] {
  const c = candleOf(p);
  const out: [string, string][] = c
    ? [
        [t("extensions-extensionchart.open"), priceText(c.open, unit)],
        [t("extensions-extensionchart.high"), priceText(c.high, unit)],
        [t("extensions-extensionchart.low"), priceText(c.low, unit)],
        [t("extensions-extensionchart.close"), p.display || priceText(c.close, unit)],
      ]
    : [[t("extensions-extensionchart.close"), pointText(p, unit)]];
  if (typeof p.volume === "number") out.push([t("extensions-extensionchart.volume"), compactNumber(p.volume)]);
  return out;
}

function CandleTable({ item }: { item: UIItem }) {
  const points = item.series?.[0]?.points ?? [];
  const heads = points[0] ? candleFacts(points[0], item.unit).map(([k]) => k) : [];
  return (
    <table>
      <thead>
        <tr>
          <th scope="col">{item.title || t("extensions-extensionchart.label")}</th>
          {heads.map((h) => (
            <th key={h} scope="col">
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {points.map((p, i) => (
          <tr key={i}>
            <th scope="row">{p.label}</th>
            {candleFacts(p, item.unit).map(([k, v]) => (
              <td key={k}>{v}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

const tipClass = "pointer-events-none absolute top-0 z-10 min-w-[96px] rounded-[8px] border border-tooltip-border bg-tooltip-bg px-2.5 py-1.5 text-tooltip-fg shadow-overlay";
const tipStyle = (x: number, plotW: number): React.CSSProperties => ({ left: x, transform: x > plotW / 2 ? "translateX(calc(-100% - 12px))" : "translateX(12px)" });

/** The read-out at a label: the label, then each series' value (strong) and name (muted), keyed by its mark. */
function Tip({ item, at, x, plotW, colors, kind }: { item: UIItem; at: number; x: number; plotW: number; colors: string[]; kind: XYKind }) {
  const series = item.series ?? [];
  return (
    <div role="status" className={tipClass} style={tipStyle(x, plotW)}>
      <div className="text-[11px] whitespace-nowrap text-tooltip-muted">{series[0]?.points[at]?.label}</div>
      {series.map((s, k) => {
        const p = s.points[at];
        if (!p) return null;
        return (
          <div key={k} className="mt-0.5 flex items-center gap-1.5 whitespace-nowrap">
            {kind === "bar" || kind === "area" ? <span aria-hidden className="inline-block size-2 rounded-[2px]" style={{ background: colors[k] }} /> : <span aria-hidden className="inline-block h-[2px] w-2.5 rounded-full" style={{ background: colors[k] }} />}
            <span className="text-[12px] font-semibold tabular-nums">{pointText(p, item.unit)}</span>
            {s.name && <span className="text-[11px] text-tooltip-muted">{s.name}</span>}
          </div>
        );
      })}
    </div>
  );
}

/** A candle's read-out: its label, then open, high, low, close and the volume. */
function CandleTip({ item, at, x, plotW }: { item: UIItem; at: number; x: number; plotW: number }) {
  const p = item.series?.[0]?.points[at];
  if (!p) return null;
  return (
    <div role="status" className={tipClass} style={tipStyle(x, plotW)}>
      <div className="text-[11px] whitespace-nowrap text-tooltip-muted">{p.label}</div>
      <dl className="mt-0.5 grid grid-cols-[auto_auto] gap-x-3 gap-y-0.5 whitespace-nowrap">
        {candleFacts(p, item.unit).map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-[11px] text-tooltip-muted">{k}</dt>
            <dd className="text-right text-[12px] font-semibold tabular-nums">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

/**
 * A donut (its words in the hole) or a pie, its legend beside it (under it when narrow): each slice's label, value and
 * share. Hovering or focusing a slice (or its legend line) lifts it and dims the others.
 */
function RoundChart({ item, compact }: { item: UIItem; compact?: boolean }) {
  const points = (item.series?.[0]?.points ?? []).slice(0, 8);
  const donut = item.kind === "donut";
  const parts = useMemo(() => slices(points.map((p) => p.value)), [points]);
  const colors = useMemo(() => {
    const named = item.series?.[0]?.color ? seriesColors(item.series.slice(0, 1))[0] : undefined;
    const base = sliceColors(points.length);
    return named ? [named, ...base.slice(0, points.length - 1)] : base;
  }, [item.series, points.length]);
  const [hover, setHover] = useState<number | null>(null);
  const size = compact ? 84 : 132;
  const r = size / 2;
  const inner = donut ? r * 0.62 : 0;
  const lift = (i: number) => (hover === i ? 3 : 0);
  const summary = summaryOf(item, item.kind ?? "donut", points.length);
  const key = (e: React.KeyboardEvent) => {
    if (!points.length) return;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight" || e.key === "ArrowUp" || e.key === "ArrowDown") {
      e.preventDefault();
      const back = e.key === "ArrowLeft" || e.key === "ArrowUp";
      const at = hover ?? (back ? points.length : -1);
      setHover((at + (back ? -1 : 1) + points.length) % points.length);
    } else if (e.key === "Escape") setHover(null);
  };
  const hovered = hover !== null ? points[hover] : undefined;
  return (
    <div className={cx(compact ? "px-4 py-2" : "px-4 pt-3 pb-2.5")}>
      {item.title && <h4 className={cx("mb-1.5 min-w-0 truncate font-medium text-ink-2", compact ? "text-[11.5px]" : "text-[12.5px]")}>{item.title}</h4>}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-3">
        <div className="relative shrink-0" style={{ width: size, height: size }}>
          <svg
            width={size}
            height={size}
            viewBox={`-4 -4 ${size + 8} ${size + 8}`}
            role="img"
            aria-label={summary}
            tabIndex={0}
            onKeyDown={key}
            onBlur={() => setHover(null)}
            className="overflow-visible rounded-full outline-none focus-visible:shadow-[0_0_0_2px_var(--accent)]"
          >
            {parts.map((sl, i) => {
              if (sl.share <= 0) return null;
              const mid = (sl.a0 + sl.a1) / 2;
              const dx = Math.sin(mid) * lift(i), dy = -Math.cos(mid) * lift(i);
              // a hairline of the surface between slices
              return (
                <path
                  key={i}
                  d={arcPath(r, r, r, inner, sl.a0, sl.a1)}
                  transform={`translate(${dx.toFixed(2)},${dy.toFixed(2)})`}
                  fill={colors[i]}
                  stroke="var(--surface)"
                  strokeWidth={parts.filter((p) => p.share > 0).length > 1 ? 1.5 : 0}
                  strokeLinejoin="round"
                  opacity={hover !== null && hover !== i ? 0.45 : 1}
                  onPointerEnter={() => setHover(i)}
                  onPointerDown={() => setHover(i)}
                  onPointerLeave={() => setHover(null)}
                  style={{ transition: "transform 150ms, opacity 150ms" }}
                />
              );
            })}
          </svg>
          {/* the hole: the donut's words, or the slice hovered */}
          {donut && (hovered || item.center) && (
            <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center px-3 text-center" style={{ padding: r - inner + 4 }}>
              {hovered ? (
                <>
                  <span className="max-w-full truncate text-[10.5px] text-ink-3">{hovered.label}</span>
                  <span className={cx("max-w-full truncate font-semibold text-ink tabular-nums", compact ? "text-[11.5px]" : "text-[13px]")}>{shareText(parts[hover!].share)}</span>
                </>
              ) : (
                <span className={cx("max-w-full truncate font-semibold text-ink tabular-nums", compact ? "text-[12px]" : "text-[15px]")}>{item.center}</span>
              )}
            </div>
          )}
        </div>
        <ul className="flex min-w-[140px] flex-1 flex-col gap-1">
          {points.map((p, i) => (
            <li
              key={i}
              onPointerEnter={() => setHover(i)}
              onPointerLeave={() => setHover(null)}
              className={cx("flex items-center gap-2 rounded-[6px] px-1 text-[12px] whitespace-nowrap transition-opacity", hover !== null && hover !== i && "opacity-55")}
            >
              <span aria-hidden className="inline-block size-2 shrink-0 rounded-[2px]" style={{ background: colors[i] }} />
              <span className="min-w-0 flex-1 truncate text-ink-2">{p.label}</span>
              <span className="shrink-0 text-ink tabular-nums">{pointText(p, item.unit)}</span>
              <span className="w-10 shrink-0 text-right text-ink-3 tabular-nums">{shareText(parts[i]?.share ?? 0)}</span>
            </li>
          ))}
        </ul>
      </div>
      {item.detail && <p className={cx("mt-1.5 text-ink-3", compact ? "text-[11px]" : "text-[11.5px]")}>{item.detail}</p>}
    </div>
  );
}
