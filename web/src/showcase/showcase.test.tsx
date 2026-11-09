// The component showcase extension's pages (#1852) through the web's view: every page reads and draws, and every component the
// schema has is drawn by some page (none is left out as unknown).
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { INERT, ViewSections } from "../views/extensions/ViewBody";
import { SHOWCASE, SHOWCASE_V6, showcaseKind } from "./snapshots";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

describe("组件展示", () => {
  const names = Object.keys(SHOWCASE);

  it("has every page", () => {
    expect(names.length).toBeGreaterThan(30);
    expect(names).toContain("home");
    expect(names).toContain("list-more");
  });

  it.each(names)("draws %s", (name) => {
    const view = SHOWCASE[name];
    const html = renderToStaticMarkup(<ViewSections view={view} compact={showcaseKind(name) !== "page"} {...INERT} />);
    expect(html.length).toBeGreaterThan(50);
    // every titled section is there
    for (const s of view.sections) if (s.title) expect(html).toContain(s.title.replace(/&/g, "&amp;"));
  });

  it("knows every component", () => {
    const types = new Set(Object.values(SHOWCASE).flatMap((v) => v.sections.flatMap((s) => s.items.map((i) => i.type))));
    for (const t of ["text", "metric", "progress", "row", "button", "link", "field", "divider", "chart", "stat_grid", "segmented", "markdown", "table", "empty", "loading"]) {
      expect(types).toContain(t);
    }
  });

  // UI version 6 (#829): its pages, and every new component, chart, control, field and style drawn by one of them
  it("has UI version 6's pages, each drawing all it has", () => {
    expect(SHOWCASE_V6).toEqual(["carousel", "chart-v6", "controls", "grid", "home-demo", "image", "inputs-v6", "markets-demo", "row-actions", "spark", "styles"]);
    const items = SHOWCASE_V6.flatMap((n) => SHOWCASE[n].sections.flatMap((s) => s.items));
    const types = new Set(items.map((i) => i.type));
    for (const t of ["image", "grid", "carousel"]) expect(types).toContain(t);
    const kinds = new Set(items.map((i) => (i.type === "chart" || i.type === "field" ? i.kind : i.control?.kind)).filter(Boolean));
    for (const k of ["candlestick", "area", "donut", "pie", "date", "time", "datetime", "multichoice", "multiline", "switch", "slider", "stepper"]) expect(kinds).toContain(k);
    expect(items.some((i) => i.actions?.length)).toBe(true);
    expect(items.some((i) => i.spark)).toBe(true);
    expect(new Set(items.map((i) => i.style))).toContain("title");
    // a picture the extension serves reaches the web with the hub's path (here the fake hub's)
    const pics = items.filter((i) => i.type === "image" && i.src?.ref);
    expect(pics.length).toBeGreaterThan(0);
    for (const p of pics) expect(p.src?.path).toMatch(/^\/api\/v1\/extensions\/showcase\/-\/image\//);
  });
});
