// @vitest-environment jsdom
// Machine detail: hovering a chart moves the moment all four charts share without re-rendering the page, the now
// tiles or the core bars; picking another range lets go of it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DetailMetrics } from "../../lib/machine-detail";
import type { Machine } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const fake = vi.hoisted(() => ({ machines: [] as Machine[] }));
// How often each part renders, counted through a helper only it calls, once a render.
const renders = vi.hoisted(() => ({ page: 0, monitor: 0, tiles: 0 }));
vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: true, machines: fake.machines }) };
});
vi.mock("../../lib/mesh", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/mesh")>();
  return { ...real, hubLabel: (...a: Parameters<typeof real.hubLabel>) => (renders.page++, real.hubLabel(...a)) };
});
vi.mock("../../lib/machine-detail", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/machine-detail")>();
  return {
    ...real,
    coresNow: (...a: Parameters<typeof real.coresNow>) => (renders.monitor++, real.coresNow(...a)),
    // NowTiles reads memory and disk with it: two calls a render
    usedOfTotal: (...a: Parameters<typeof real.usedOfTotal>) => (renders.tiles++, real.usedOfTotal(...a)),
  };
});

const { MachineDetail } = await import("./MachineDetail");
const { RANGES } = await import("../../lib/machine-detail");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const WIDTH = 480;
const PLOT = WIDTH - 52;

function metrics(range: keyof typeof RANGES): DetailMetrics {
  const step = RANGES[range].step;
  const now = Date.now() / 1000;
  return {
    machine_id: "box",
    online: true,
    system: { cores: 4, memory: 8e9 },
    latest: { t: now, cpu: 40, cores: [10, 20, 30, 40], load: 1.2, mem_used: 4e9, mem_total: 8e9, net_rx: 1e5, net_tx: 2e4, disk_used: 1e11, disk_total: 5e11 },
    points: Array.from({ length: 360 }, (_, i) => ({ t: now - (360 - i) * step, cpu: 30 + 20 * Math.sin(i / 9), mem: 4e9, rx: 1e5, tx: 2e4, load: 1 + Math.sin(i / 11) })),
  };
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const hitRect = (svg: SVGSVGElement) => svg.querySelector<SVGRectElement>(':scope > rect[fill="transparent"]');
const charts = () => [...host.querySelectorAll<SVGSVGElement>("svg[role=img]")].filter((s) => s.getAttribute("aria-label")?.length && hitRect(s) && s.querySelector("polyline"));
const held = () => charts().map((s) => s.querySelector(':scope > g[pointer-events="none"] text')?.textContent ?? null);
const move = (svg: SVGSVGElement, x: number) =>
  act(() => {
    hitRect(svg)!.dispatchEvent(new PointerEvent("pointermove", { bubbles: true, clientX: x }));
  });

beforeEach(async () => {
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(WIDTH);
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue({ left: 0, top: 0, right: PLOT, bottom: 152, width: PLOT, height: 152, x: 0, y: 0, toJSON() {} } as DOMRect);
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://hub.example");
      const range = (url.searchParams.get("range") ?? "1h") as keyof typeof RANGES;
      if (url.pathname.endsWith("/machines/box/metrics")) return Promise.resolve(new Response(JSON.stringify({ metrics: metrics(range) }), { status: 200, headers: { "Content-Type": "application/json" } }));
      return Promise.resolve(new Response(JSON.stringify({ error: "not found" }), { status: 404 }));
    }),
  );
  fake.machines = [{ id: "box", name: "box", online: true } as Machine];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<MachineDetail mid="box" onToggleSidebar={() => {}} />));
  await flush();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("MachineDetail chart hover", () => {
  it("moves the shared moment without re-rendering the page, the tiles or the cores", () => {
    expect(charts()).toHaveLength(4);
    expect(held()).toEqual([null, null, null, null]);
    renders.page = renders.monitor = renders.tiles = 0;

    const seen = new Set<string | null>();
    for (let i = 0; i < 20; i++) {
      move(charts()[i % 4], 10 + i * 20);
      const now = held();
      // every chart holds the same moment
      expect(new Set(now).size).toBe(1);
      seen.add(now[0]);
    }
    expect(seen.has(null)).toBe(false);
    expect(seen.size).toBeGreaterThan(10);
    // Before: the page held the moment, so 20 moves rendered the page 20 times, the monitor 20 and the now tiles 20.
    expect(renders).toEqual({ page: 0, monitor: 0, tiles: 0 });
  });

  it("lets go of the moment when another range is picked", async () => {
    move(charts()[0], 200);
    expect(held().every((label) => label !== null)).toBe(true);
    const sixHours = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === RANGES["6h"].label)!;
    act(() => sixHours.click());
    // the old range's charts stay (dimmed) until the new one arrives, with nothing held
    expect(held()).toEqual([null, null, null, null]);
    await flush();
    expect(charts()).toHaveLength(4);
    expect(held()).toEqual([null, null, null, null]);
    move(charts()[1], 120);
    expect(held().every((label) => label !== null)).toBe(true);
  });
});
