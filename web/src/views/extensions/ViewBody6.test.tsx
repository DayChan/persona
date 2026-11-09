// @vitest-environment jsdom
// UI version 6's components as the web draws them (docs/extensions/extensions.md "UI version 6"): a picture loads from the hub's path
// with the token or from an https url without it, controls and the row menu sit beside the row's button (never in
// it), tiles say what they are to a screen reader, and the new fields are the browser's own inputs.
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { readView, type UIView } from "../../lib/extension-ui";
import { INERT, ViewSections } from "./ViewBody";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>([["persona.token", "tok"]]);
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const draw = (items: unknown[], extra: Partial<Parameters<typeof ViewSections>[0]> = {}) => {
  const view: UIView = readView({ sections: [{ items }] });
  return renderToStaticMarkup(<ViewSections view={view} {...INERT} {...extra} />);
};
const doc = (html: string) => new DOMParser().parseFromString(`<body>${html}</body>`, "text/html");

const PATH = "/api/v1/extensions/home/-/image/eyJhIjoxfQ.c2ln";

describe("UI 6：图片", () => {
  it("loads the hub's path with the token, an https url without it", () => {
    const html = draw([
      { type: "image", key: "a", src: { ref: "cam", path: PATH }, alt: "前门", caption: "刚刚", aspect: "wide" },
      { type: "image", key: "b", src: { url: "https://example.com/x.jpg" }, alt: "照片" },
    ]);
    const imgs = [...doc(html).querySelectorAll("img")].map((i) => i.getAttribute("src"));
    expect(imgs[0]).toMatch(/^.*\/api\/v1\/extensions\/home\/-\/image\/eyJhIjoxfQ\.c2ln\?token=/);
    expect(imgs[1]).toBe("https://example.com/x.jpg");
    expect(html).toContain("<figcaption");
    expect(doc(html).querySelector("img")?.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(html).toContain("aspect-ratio:1.7777");
  });

  it("can be opened full screen unless no_zoom", () => {
    expect(draw([{ type: "image", src: { url: "https://example.com/x.jpg" }, alt: "照片" }])).toContain("cursor-zoom-in");
    expect(draw([{ type: "image", src: { url: "https://example.com/x.jpg" }, alt: "照片", no_zoom: true }])).not.toContain("cursor-zoom-in");
  });

  it("loads a row's thumbnail from the hub's path", () => {
    const html = draw([{ type: "row", key: "r", title: "快递", thumbnail: { ref: "snap", path: PATH, aspect: "wide" } }]);
    expect(doc(html).querySelector("img")?.getAttribute("src")).toContain("?token=");
  });
});

describe("UI 6：行内控件和行动作", () => {
  it("puts a switch and the … menu beside the row's button, not inside it", () => {
    const html = draw([
      { type: "row", key: "l", title: "主灯", action: { id: "open" }, control: { kind: "switch", name: "l", on: true, action: { id: "t" } }, actions: [{ title: "改名", action: { id: "mv" } }] },
    ]);
    const d = doc(html);
    const sw = d.querySelector('[role="switch"]')!;
    expect(sw.getAttribute("aria-checked")).toBe("true");
    expect(sw.closest("button:not([role=switch])")).toBeNull();
    expect(d.querySelector('[aria-haspopup="menu"]')).not.toBeNull();
    // INERT: no handler, so the control only shows
    expect(sw.hasAttribute("disabled")).toBe(true);
  });

  it("shows the value the page holds for a control over the extension's", () => {
    const html = draw([{ type: "row", key: "l", title: "主灯", control: { kind: "switch", name: "l", on: false, action: { id: "t" } } }], { controls: { l: "true" }, onControl: () => {} });
    expect(doc(html).querySelector('[role="switch"]')!.getAttribute("aria-checked")).toBe("true");
  });

  it("draws a slider on its own line and a stepper with its value read out", () => {
    const html = draw([
      { type: "row", key: "b", title: "亮度", control: { kind: "slider", name: "b", value: 60, min: 0, max: 100, unit: "%", action: { id: "b" } } },
      { type: "row", key: "t", title: "温度", control: { kind: "stepper", name: "t", value: 22.5, min: 16, max: 30, step: 0.5, unit: "°C", action: { id: "t" } } },
    ], { onControl: () => {} });
    const d = doc(html);
    const range = d.querySelector('input[type="range"]')!;
    expect(range.getAttribute("aria-label")).toBe("亮度");
    expect(range.getAttribute("aria-valuetext")).toBe("60 %");
    expect(d.querySelector('[aria-live="polite"]')?.textContent).toBe("22.5 °C");
  });
});

describe("UI 6：网格和横向卡片", () => {
  it("caps the grid's columns at max_columns and reads each tile out", () => {
    const html = draw([{ type: "grid", key: "g", max_columns: 2, tiles: [{ key: "a", title: "客厅", subtitle: "3 盏灯", value: "24 °C", active: true, action: { id: "o" } }] }]);
    expect(html).toContain("calc((100% - 8px) / 2)");
    expect(doc(html).querySelector("button[aria-label]")?.getAttribute("aria-label")).toBe("客厅，3 盏灯，24 °C，已开启");
  });

  it("scrolls a carousel sideways, snapping each tile", () => {
    const html = draw([{ type: "carousel", key: "c", title: "场景", width: "narrow", tiles: [{ title: "回家" }, { title: "离家" }] }]);
    expect(html).toContain("snap-x");
    expect(html).toContain("w-[140px]");
    expect(doc(html).querySelectorAll('[role="listitem"]')).toHaveLength(2);
  });
});

describe("UI 6：输入", () => {
  it("uses the browser's date and time inputs with their limits", () => {
    const html = draw([
      { type: "field", name: "d", kind: "date", label: "日期", value: "2026-11-06", earliest: "2026-11-01", latest: "2027-11-01", detail: "说明" },
      { type: "field", name: "t", kind: "time", label: "时间" },
      { type: "field", name: "dt", kind: "datetime", label: "截止" },
    ], { values: { d: "2026-11-06" } });
    const d = doc(html);
    const date = d.querySelector('input[type="date"]')!;
    expect(date.getAttribute("min")).toBe("2026-11-01");
    expect(date.getAttribute("max")).toBe("2027-11-01");
    expect(d.querySelector('input[type="time"]')).not.toBeNull();
    expect(d.querySelector('input[type="datetime-local"]')).not.toBeNull();
    expect(html).toContain("说明");
  });

  it("checks a few choices as pills and writes a textarea for multiline", () => {
    const html = draw([
      { type: "field", name: "m", kind: "multichoice", label: "房间", choices: [{ value: "a", title: "厨房" }, { value: "b", title: "卧室" }] },
      { type: "field", name: "n", kind: "multiline", label: "备注" },
    ], { values: { m: '["b"]', n: "一\n二" } });
    const d = doc(html);
    const boxes = [...d.querySelectorAll('[role="checkbox"]')].map((b) => b.getAttribute("aria-checked"));
    expect(boxes).toEqual(["false", "true"]);
    expect(d.querySelector("textarea")?.getAttribute("rows")).toBe("3");
  });
});

describe("UI 6：样式", () => {
  it("draws title and headline as headings, and a named color's badge in that color", () => {
    const html = draw([
      { type: "text", text: "大标题", style: "title" },
      { type: "text", text: "小标题", style: "headline" },
      { type: "row", key: "r", title: "客厅", icon: "sofa", icon_color: "blue", badge: { text: "在家", color: "green" } },
    ]);
    const d = doc(html);
    expect(d.querySelector("h3")?.textContent).toBe("大标题");
    expect(d.querySelector("h4")?.textContent).toBe("小标题");
    expect(html).toContain("var(--hue-blue)");
    expect(html).toContain("var(--hue-green)");
  });
});
