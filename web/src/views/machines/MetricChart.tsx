// One chart of the machine details (docs/server/machines.md "Charts"): a title with the value now, or at the hovered moment; the lines over
// the range, each run between gaps its own line (no line across a time the machine sent nothing); a hairline grid,
// round ticks on the right and below; a crosshair that snaps to the nearest point. Every chart of the page follows
// the same moment (the charts' parent holds it). Drawn at the real pixel size, so the labels are not stretched.
// A pointer move changes only that moment: the grid and the lines are memoized layers drawn again only when the data,
// the size or the axis change, so a move redraws the crosshair, the read-out and the time labels it hides.
import { memo, useMemo } from "react";
import { hm, isEmpty, nearest, runs, scale, timeAt, timeTicks, yAxis, type DetailPoint, type Reader } from "../../lib/machine-detail";
import { cx } from "../../ui/atoms";
import { t } from "../../i18n";
import { useWidth } from "../../lib/use-width";

export interface ChartSeries {
  name: string;
  /** A CSS color (a token: var(--accent)…): the line and its dot, never the text. */
  color: string;
  read: Reader;
  /** The value now (shown when nothing is hovered). */
  now?: number;
}

const H = 132; // plot height
const AXIS_W = 52; // y labels on the right
const AXIS_H = 20; // time labels below
const TOP = 6;

export function MetricChart({
  title,
  points,
  series,
  t0,
  t1,
  step,
  percent,
  binary,
  format,
  axisFormat,
  hover,
  onHover,
  tz,
  reference,
  className,
}: {
  title: string;
  points: DetailPoint[];
  series: ChartSeries[];
  t0: number;
  t1: number;
  step: number;
  percent?: boolean;
  binary?: boolean;
  format: (v: number | undefined) => string;
  axisFormat: (v: number) => string;
  hover: number | null;
  onHover: (t: number | null) => void;
  tz: number;
  /** A level to mark with a dashed line and its label (the load chart's core count); the y axis reaches it. */
  reference?: { value: number; label: string };
  className?: string;
}) {
  const [box, width] = useWidth<HTMLDivElement>();
  const W = Math.max(0, width - AXIS_W);
  const lines = useMemo(() => series.map((s) => runs(points, s.read)), [points, series]);
  const max = useMemo(() => Math.max(0, ...lines.flat(2).map((p) => p.v)), [lines]);
  const axis = useMemo(() => yAxis(Math.max(max, reference?.value ?? 0), { percent, binary }), [max, reference?.value, percent, binary]);
  const x = scale(t0, t1, 0, W);
  const y = scale(0, axis.top, H, TOP);
  const ticks = useMemo(() => timeTicks(t0, t1, W < 280 ? 3 : 4, tz), [t0, t1, W, tz]);
  const picked = hover !== null ? nearest(points, hover, step) : undefined;
  const hoverX = picked ? x(picked.t) : hover !== null ? x(hover) : null;
  const clip = `clip-${title.replace(/\W/g, "") || "c"}-${series.length}`;

  // The read-out: the hovered moment's values (— in a gap), or now.
  const shown = series.map((s) => (hover !== null ? (picked ? s.read(picked) : undefined) : s.now));
  const summary = useMemo(() => {
    const peak = (i: number) => {
      const vals = lines[i].flat().map((p) => p.v);
      return vals.length ? Math.max(...vals) : undefined;
    };
    return series.map((s, i) => series.length > 1 ? t("machines-metricchart.summary_named", { name: s.name, now: format(s.now), peak: format(peak(i)) }) : t("machines-metricchart.summary", { now: format(s.now), peak: format(peak(i)) })).join(t("machines-metricchart.summary_sep"));
  }, [series, lines, format]);

  const move = (e: React.PointerEvent<SVGRectElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    onHover(timeAt(e.clientX - r.left, r.width, t0, t1));
  };
  const key = (e: React.KeyboardEvent) => {
    const real = points.filter((p) => !p.gap);
    if (!real.length) return;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      e.preventDefault();
      const at = picked ? real.indexOf(picked) : real.length - 1;
      const next = real[Math.min(real.length - 1, Math.max(0, at + (e.key === "ArrowLeft" ? -1 : 1) * (e.shiftKey ? 10 : 1)))];
      onHover(next.t);
    } else if (e.key === "Escape") onHover(null);
  };

  return (
    <section className={cx("rounded-card bg-surface px-4 pt-3 pb-2 shadow-card", className)}>
      <div className="flex items-baseline gap-2">
        <h3 className="truncate text-[12.5px] font-medium text-ink-2">{title}</h3>
        <span className="flex-1" />
        <span className="shrink-0 font-mono text-[11px] text-ink-3 tabular-nums">{hover !== null ? hm(picked?.t ?? hover, tz) : t("machines-metricchart.now")}</span>
      </div>
      <div className="mt-0.5 flex min-h-7 flex-wrap items-baseline gap-x-4 gap-y-0.5">
        {series.map((s, i) => (
          <span key={s.name} className="flex items-baseline gap-1.5 whitespace-nowrap">
            {series.length > 1 && (
              <>
                <span aria-hidden className="inline-block h-[2px] w-3 translate-y-[-3px] rounded-full" style={{ background: s.color }} />
                <span className="text-[11.5px] text-ink-3">{s.name}</span>
              </>
            )}
            <span className={cx("font-medium text-ink tabular-nums", series.length > 1 ? "text-[15px]" : "text-[20px] leading-7")}>{shown[i] === undefined && hover !== null ? "—" : format(shown[i])}</span>
          </span>
        ))}
        {hover !== null && !picked && <span className="text-[11.5px] text-ink-3">{t("machines-metricchart.no_data_at")}</span>}
      </div>
      <div ref={box} className="relative mt-1.5">
        {width > 0 && (
          <svg
            width={width}
            height={H + AXIS_H}
            role="img"
            aria-label={t("machines-metricchart.chart_label", { title, summary })}
            tabIndex={0}
            onKeyDown={key}
            onBlur={() => onHover(null)}
            className="block rounded-[4px] outline-none focus-visible:shadow-[0_0_0_2px_var(--accent)]"
          >
            <defs>
              <clipPath id={clip}>
                <rect x={0} y={0} width={W} height={H} />
              </clipPath>
            </defs>
            <YGrid ticks={axis.ticks} top={axis.top} W={W} axisFormat={axisFormat} />
            {ticks.map((t) => {
              const tx = x(t);
              // A label never runs off the plot, and gives way to the hovered time's.
              const anchor = tx < 16 ? "start" : tx > W - 16 ? "end" : "middle";
              const hidden = hoverX !== null && Math.abs(tx - hoverX) < 46;
              return (
                <g key={t}>
                  <line x1={tx} x2={tx} y1={TOP} y2={H} stroke="var(--line)" strokeWidth={1} shapeRendering="crispEdges" />
                  {!hidden && (
                    <text x={anchor === "start" ? 0 : anchor === "end" ? W : tx} y={H + 14} textAnchor={anchor} fontSize={10.5} fill="var(--ink-3)" className="tabular-nums">
                      {hm(t, tz)}
                    </text>
                  )}
                </g>
              );
            })}
            <Lines lines={lines} series={series} t0={t0} t1={t1} W={W} top={axis.top} clip={clip} refValue={reference?.value} refLabel={reference?.label} />
            {hoverX !== null && (
              <g pointerEvents="none">
                <line x1={hoverX} x2={hoverX} y1={TOP} y2={H} stroke="var(--ink-3)" strokeWidth={1} strokeDasharray={picked ? undefined : "3 3"} shapeRendering="crispEdges" />
                {picked &&
                  series.map((s) => {
                    const v = s.read(picked);
                    return v === undefined ? null : <circle key={s.name} cx={hoverX} cy={y(Math.min(v, axis.top))} r={4} fill={s.color} stroke="var(--surface)" strokeWidth={2} />;
                  })}
                <rect x={Math.min(Math.max(0, hoverX - 21), W - 42)} y={H + 2} width={42} height={16} rx={4} fill="var(--tooltip-bg)" />
                <text x={Math.min(Math.max(21, hoverX), W - 21)} y={H + 14} textAnchor="middle" fontSize={10.5} fill="var(--tooltip-fg)" className="tabular-nums">
                  {hm(picked?.t ?? hover ?? t1, tz)}
                </text>
              </g>
            )}
            {/* the hit area: the whole plot, so the pointer only has to be over the time it wants */}
            <rect x={0} y={0} width={W} height={H + AXIS_H} fill="transparent" onPointerMove={move} onPointerDown={move} onPointerLeave={() => onHover(null)} style={{ touchAction: "pan-y" }} />
          </svg>
        )}
        {width === 0 && <div style={{ height: H + AXIS_H }} />}
        {isEmpty(points) && width > 0 && (
          <p className="pointer-events-none absolute inset-x-0 top-[44%] -translate-y-1/2 text-center text-[12px] text-ink-3" style={{ paddingRight: AXIS_W }}>
            {t("machines-metricchart.no_data")}
          </p>
        )}
      </div>
    </section>
  );
}

/** The grid: hairline, solid, recessive, with the y labels on the right. */
const YGrid = memo(function YGrid({ ticks, top, W, axisFormat }: { ticks: number[]; top: number; W: number; axisFormat: (v: number) => string }) {
  const y = scale(0, top, H, TOP);
  return (
    <>
      {ticks.map((v) => (
        <g key={v}>
          <line x1={0} x2={W} y1={y(v)} y2={y(v)} stroke="var(--line)" strokeWidth={1} shapeRendering="crispEdges" />
          <text x={W + 6} y={y(v)} dy="0.35em" fontSize={10.5} fill="var(--ink-3)" className="tabular-nums">
            {axisFormat(v)}
          </text>
        </g>
      ))}
    </>
  );
});

/** The reference's dashed line, the lines (each run its own, an area under a lone series) and the reference's label.
 * Two toFixed a point and about 360 points a range: memoized, so a hover never builds the points strings again. */
const Lines = memo(function Lines({ lines, series, t0, t1, W, top, clip, refValue, refLabel }: {
  lines: { t: number; v: number }[][][];
  series: ChartSeries[];
  t0: number;
  t1: number;
  W: number;
  top: number;
  clip: string;
  refValue?: number;
  refLabel?: string;
}) {
  const x = scale(t0, t1, 0, W);
  const y = scale(0, top, H, TOP);
  const showRef = refValue !== undefined && refValue <= top;
  return (
    <>
      {showRef && <line x1={0} x2={W} y1={y(refValue)} y2={y(refValue)} stroke="var(--ink-3)" strokeWidth={1} strokeDasharray="3 3" shapeRendering="crispEdges" pointerEvents="none" />}
      <g clipPath={`url(#${clip})`}>
        {lines.map((rs, i) =>
          rs.map((r, j) => {
            const s = series[i];
            if (r.length === 1) return <circle key={`${i}-${j}`} cx={x(r[0].t)} cy={y(r[0].v)} r={1.5} fill={s.color} />;
            const pts = r.map((p) => `${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join(" ");
            return (
              <g key={`${i}-${j}`}>
                {series.length === 1 && <polygon points={`${x(r[0].t).toFixed(1)},${H} ${pts} ${x(r[r.length - 1].t).toFixed(1)},${H}`} fill={s.color} fillOpacity={0.1} />}
                <polyline points={pts} fill="none" stroke={s.color} strokeWidth={series.length > 1 ? 1.6 : 2} strokeLinejoin="round" strokeLinecap="round" />
              </g>
            );
          }),
        )}
      </g>
      {/* the reference's label over the lines, with a halo: a load near the core count ran right through it */}
      {showRef && (
        <text x={4} y={y(refValue) - 4} fontSize={10.5} fill="var(--ink-2)" stroke="var(--surface)" strokeWidth={3} strokeLinejoin="round" paintOrder="stroke" className="tabular-nums" pointerEvents="none">
          {refLabel}
        </text>
      )}
    </>
  );
});
