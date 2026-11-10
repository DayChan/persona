// @vitest-environment jsdom
// Hovering the machine charts redraws the hover overlay and the read-out only: the lines and areas (two toFixed a
// point, 360 points a range) and the grid stay as they were drawn. Four charts share one moment, as on the page.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { bytesAxis, load, loadAxis, percent, rate, type DetailPoint } from "../../lib/machine-detail";
import { MetricChart, type ChartSeries } from "./MetricChart";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const WIDTH = 480; // the box; the plot is WIDTH - 52 (the y labels)
const PLOT = WIDTH - 52;
const STEP = 10;
const T1 = 1_788_480_000;
const T0 = T1 - 3600;
// 360 points, a gap in the middle (two runs a line), values that keep the read-out formats busy.
const POINTS: DetailPoint[] = Array.from({ length: 360 }, (_, i) => {
  const t = T0 + (i + 1) * STEP;
  if (i >= 170 && i < 175) return { t, gap: true };
  return { t, cpu: 30 + 25 * Math.sin(i / 9), mem: 4e9 + 1e9 * Math.cos(i / 13), rx: 2e5 * (1 + Math.sin(i / 5)), tx: 5e4 * (1 + Math.cos(i / 7)), load: 1.5 + Math.sin(i / 11) };
});
const MEM_TOTAL = 8e9;
const CPU: ChartSeries[] = [{ name: "CPU", color: "var(--accent)", read: (p) => p.cpu, now: 42 }];
const MEM: ChartSeries[] = [{ name: "Memory", color: "var(--green)", read: (p) => (p.mem === undefined ? undefined : (100 * p.mem) / MEM_TOTAL), now: 55 }];
const NET: ChartSeries[] = [
  { name: "Down", color: "var(--orange)", read: (p) => p.rx, now: 1e5 },
  { name: "Up", color: "var(--accent)", read: (p) => p.tx, now: 2e4 },
];
const LOAD: ChartSeries[] = [{ name: "Load", color: "var(--chart-load)", read: (p) => p.load, now: 1.2 }];
const PERCENT_AXIS = (v: number) => `${v}%`;
const REFERENCE = { value: 2, label: "2 cores" };

function Charts() {
  const [hover, setHover] = useState<number | null>(null);
  const common = { points: POINTS, t0: T0, t1: T1, step: STEP, hover, onHover: setHover, tz: 0 };
  return (
    <>
      <MetricChart title="CPU" series={CPU} percent format={percent} axisFormat={PERCENT_AXIS} {...common} />
      <MetricChart title="Memory" series={MEM} percent format={percent} axisFormat={PERCENT_AXIS} {...common} />
      <MetricChart title="Network" series={NET} binary format={rate} axisFormat={bytesAxis} {...common} />
      <MetricChart title="Load" series={LOAD} format={load} axisFormat={loadAxis} reference={REFERENCE} {...common} />
    </>
  );
}

let host: HTMLDivElement;
let root: Root;
const svgs = () => [...host.querySelectorAll("svg")];
const hitRect = (svg: SVGSVGElement) => svg.querySelector<SVGRectElement>(':scope > rect[fill="transparent"]')!;
const overlay = (svg: SVGSVGElement) => svg.querySelector<SVGGElement>(':scope > g[pointer-events="none"]');
const move = (svg: SVGSVGElement, x: number) =>
  act(() => {
    hitRect(svg).dispatchEvent(new PointerEvent("pointermove", { bubbles: true, clientX: x }));
  });

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(WIDTH);
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue({ left: 0, top: 0, right: PLOT, bottom: 152, width: PLOT, height: 152, x: 0, y: 0, toJSON() {} } as DOMRect);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Charts />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("MetricChart hover", () => {
  it("redraws only the overlay and the read-out on a pointer move", () => {
    expect(svgs()).toHaveLength(4);
    const lines = () => svgs().map((s) => [...s.querySelectorAll("polyline, polygon")].map((p) => p.getAttribute("points")));
    const drawn = lines();
    expect(drawn.flat().length).toBeGreaterThanOrEqual(8);

    const toFixed = vi.spyOn(Number.prototype, "toFixed");
    for (let i = 0; i < 20; i++) move(svgs()[i % 4], 20 + i * 19);
    const calls = toFixed.mock.calls.length;
    toFixed.mockRestore();
    // Before: every chart rebuilt every points string on every move (71,505 toFixed calls for these 20 moves).
    expect(calls).toBeLessThan(100);
    expect(lines()).toEqual(drawn);
  });

  it("still follows the pointer in every chart", () => {
    const [cpu] = svgs();
    move(cpu, 100);
    const first = svgs().map((s) => [overlay(s)?.querySelector("line")?.getAttribute("x1"), overlay(s)?.querySelector("text")?.textContent]);
    move(cpu, 300);
    const second = svgs().map((s) => [overlay(s)?.querySelector("line")?.getAttribute("x1"), overlay(s)?.querySelector("text")?.textContent]);
    for (const [i, [x1, label]] of first.entries()) {
      expect(x1).toBeTruthy();
      expect(label).toMatch(/^\d\d:\d\d$/);
      // every chart holds the same moment
      expect([x1, label]).toEqual(first[0]);
      expect(second[i][0]).not.toBe(x1);
      expect(second[i][1]).not.toBe(label);
    }
    act(() => {
      // React's onPointerLeave listens to pointerout with the element left for
      hitRect(cpu).dispatchEvent(new PointerEvent("pointerout", { bubbles: true, relatedTarget: document.body }));
    });
    expect(svgs().map(overlay)).toEqual([null, null, null, null]);
  });
});
