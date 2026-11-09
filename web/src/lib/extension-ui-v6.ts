// UI version 6 of an extension's declarative UI (docs/extensions/extensions.md "UI version 6"): pictures (an https url, or a ref the
// hub serves at a path of its own), a row's switch / slider / stepper, a row's extra actions, grids and carousels of
// tiles, new fields (date, time, datetime, multichoice, multiline), named colors, and the new charts' geometry
// (area, candlestick, donut, pie, sparks). What is drawn is in views/extensions; the reading and the arithmetic here.
import { withToken } from "./inbox";
import { isHttps, readAction, type ChartSeries, type UIAction, type UITone, type UIView } from "./extension-ui";

// ── named colors ────────────────────────────────────────

/** The named colors (schema Color): each is a token of the web's (--hue-<name>, light and dark), never a hex. */
export const COLORS = ["red", "orange", "amber", "yellow", "green", "teal", "blue", "indigo", "purple", "pink", "gray"] as const;
export type UIColor = (typeof COLORS)[number];

/** A color the web knows; undefined (the quiet ink) for any other. */
export const colorOf = (name?: string): UIColor | undefined => ((COLORS as readonly string[]).includes(name ?? "") ? (name as UIColor) : undefined);

/** The CSS color of a named color; `fallback` for an unknown one. */
export const hue = (name?: string, fallback = "var(--ink-3)"): string => {
  const c = colorOf(name);
  return c ? `var(--hue-${c})` : fallback;
};

/** A named color's tint behind something that is on (a tile, a badge): the color at 14% over the surface. */
export const hueTint = (name?: string, fallback = "var(--hover)"): string => {
  const c = colorOf(name);
  return c ? `color-mix(in srgb, var(--hue-${c}) 14%, transparent)` : fallback;
};

// ── pictures ────────────────────────────────────────────

/** Where a picture comes from: an https url (loaded without credentials) or the hub's path (with the device's). */
export interface UIImageSource {
  url?: string;
  ref?: string;
  path?: string;
  width?: number;
  height?: number;
}

/**
 * The only paths the web loads with its credentials: the hub's own image route (schema ImageSource.path). Anything
 * else under the name path is not loaded, so a view can never make the web send its token somewhere else.
 */
const IMAGE_PATH = /^\/api\/v1\/extensions\/[^/?#]+\/-\/image\/[A-Za-z0-9_.-]+$/;

/** Whether a path is one the hub wrote for a picture. */
export const isImagePath = (path?: string): boolean => !!path && path.length <= 2048 && IMAGE_PATH.test(path) && !path.includes("/../");

/**
 * The address an <img> loads: the hub's path with the device token (as the inbox's pictures), else an https url
 * as it is; undefined when neither (a ref the hub did not rewrite: an older hub, or the proxy not there yet).
 */
export function imageSrc(src: UIImageSource | undefined, token?: string): string | undefined {
  if (!src || typeof src !== "object") return undefined;
  if (typeof src.path === "string" && isImagePath(src.path)) return token === undefined ? withToken(src.path) : withToken(src.path, token);
  if (typeof src.url === "string" && isHttps(src.url)) return src.url;
  return undefined;
}

/** Whether a picture can be loaded at all (the hub's path, or an https url): what reading a view keeps. */
export const imageLoadable = (src: UIImageSource | undefined): boolean =>
  !!src && typeof src === "object" && ((typeof src.path === "string" && isImagePath(src.path)) || (typeof src.url === "string" && isHttps(src.url)));

/** Whether an <img> address is the hub's (a failure may be an expired token: the page is drawn again to sign anew). */
export const isHubImage = (src: UIImageSource | undefined): boolean => !!src && isImagePath(src.path);

const ASPECTS: Record<string, number> = { square: 1, landscape: 4 / 3, wide: 16 / 9, portrait: 3 / 4 };

/** The frame's width / height: the aspect's, auto from the picture's own size (4:3 until it is known). */
export function aspectRatio(aspect?: string, src?: UIImageSource): number {
  if (aspect === "auto") {
    const w = src?.width, h = src?.height;
    return typeof w === "number" && typeof h === "number" && w > 0 && h > 0 ? w / h : 4 / 3;
  }
  return ASPECTS[aspect ?? ""] ?? 4 / 3;
}

// ── a row's control and extra actions, tiles ─────────────

export interface UIRowControl {
  kind: "switch" | "slider" | "stepper" | string;
  name: string;
  on?: boolean;
  value?: number;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  display?: string;
  disabled?: boolean;
  action: UIAction;
}

export interface UIRowAction {
  title: string;
  icon?: string;
  role?: "normal" | "destructive" | string;
  swipe?: boolean;
  action: UIAction;
}

export interface UISpark {
  values: number[];
  color?: UIColor | string;
  tone?: UITone;
  fill?: boolean;
}

export interface UITile {
  key?: string;
  title: string;
  subtitle?: string;
  value?: string;
  icon?: string;
  image?: UIImageSource;
  color?: UIColor | string;
  badge?: { text: string; tone?: UITone; color?: UIColor | string };
  active?: boolean;
  spark?: UISpark;
  control?: UIRowControl;
  actions?: UIRowAction[];
  action?: UIAction;
}

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

/** A control the web can draw (a known kind, a name and an action); undefined otherwise. */
export function readControl(raw: unknown): UIRowControl | undefined {
  if (!isObject(raw) || typeof raw.name !== "string" || !raw.name || !["switch", "slider", "stepper"].includes(String(raw.kind))) return undefined;
  const action = readAction(raw.action);
  if (!action) return undefined;
  const num = (k: string) => (typeof raw[k] === "number" && Number.isFinite(raw[k]) ? (raw[k] as number) : undefined);
  const c: UIRowControl = { kind: raw.kind as string, name: raw.name, action };
  if (typeof raw.on === "boolean") c.on = raw.on;
  for (const k of ["value", "min", "max", "step"] as const) if (num(k) !== undefined) c[k] = num(k);
  if (typeof raw.unit === "string" && raw.unit) c.unit = raw.unit;
  if (typeof raw.display === "string" && raw.display) c.display = raw.display;
  if (raw.disabled === true) c.disabled = true;
  if (c.kind === "slider" && !(typeof c.min === "number" && typeof c.max === "number" && c.min < c.max)) return undefined;
  return c;
}

/** A row's (or a tile's) extra actions: those with a title and an action, four at most. */
export function readRowActions(raw: unknown): UIRowAction[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const out = raw.flatMap((a) => {
    if (!isObject(a) || typeof a.title !== "string" || !a.title) return [];
    const action = readAction(a.action);
    if (!action) return [];
    return [{ title: a.title, action, ...(typeof a.icon === "string" ? { icon: a.icon } : {}), ...(a.role === "destructive" ? { role: "destructive" } : {}), ...(a.swipe === true ? { swipe: true } : {}) }];
  });
  // a destructive one last, as every app orders them
  out.sort((a, b) => Number(a.role === "destructive") - Number(b.role === "destructive"));
  return out.length ? out.slice(0, 4) : undefined;
}

/** A spark with at least two finite values; undefined otherwise. */
export function readSpark(raw: unknown): UISpark | undefined {
  if (!isObject(raw) || !Array.isArray(raw.values)) return undefined;
  const values = raw.values.filter((v): v is number => typeof v === "number" && Number.isFinite(v)).slice(0, 60);
  if (values.length < 2) return undefined;
  return { values, ...(typeof raw.color === "string" ? { color: raw.color } : {}), ...(typeof raw.tone === "string" ? { tone: raw.tone as UITone } : {}), ...(raw.fill === true ? { fill: true } : {}) };
}

function readTile(raw: unknown): UITile[] {
  if (!isObject(raw) || typeof raw.title !== "string" || !raw.title) return [];
  const t = { ...(raw as unknown as UITile) };
  t.action = readAction(raw.action);
  if (!t.action) delete t.action;
  const control = readControl(raw.control);
  // a tile's control is a switch only (the hub checks it; the web draws nothing else there)
  if (control?.kind === "switch") t.control = control;
  else delete t.control;
  const actions = readRowActions(raw.actions);
  if (actions) t.actions = actions;
  else delete t.actions;
  const spark = readSpark(raw.spark);
  if (spark) t.spark = spark;
  else delete t.spark;
  return [t];
}

/** The UI version 6 parts of an item, read in place: what does not read is dropped, never drawn half. */
export function readV6(out: Record<string, unknown>, raw: Record<string, unknown>) {
  if ("control" in raw) {
    const c = readControl(raw.control);
    if (c) out.control = c;
    else delete out.control;
  }
  if ("actions" in raw) {
    const a = readRowActions(raw.actions);
    if (a) out.actions = a;
    else delete out.actions;
  }
  if ("spark" in raw) {
    const s = readSpark(raw.spark);
    if (s) out.spark = s;
    else delete out.spark;
  }
  if (Array.isArray(raw.stats)) out.stats = raw.stats.map((st) => (isObject(st) && "spark" in st ? { ...st, spark: readSpark(st.spark) } : st));
  if (Array.isArray(raw.tiles)) out.tiles = raw.tiles.flatMap(readTile).slice(0, raw.type === "carousel" ? 20 : 60);
}

/** Every control of a view, by its name: what to show for it and what the extension last drew. */
export function controlsOf(view: UIView | null | undefined): Map<string, UIRowControl> {
  const out = new Map<string, UIRowControl>();
  for (const s of view?.sections ?? [])
    for (const it of s.items ?? []) {
      if (it.control) out.set(it.control.name, it.control);
      for (const t of it.tiles ?? []) if (t.control) out.set(t.control.name, t.control);
    }
  return out;
}

/** A control's value as the extension drew it, as sent on the wire: a switch "true" / "false", else the number. */
export function controlValue(c: UIRowControl): string {
  if (c.kind === "switch") return c.on ? "true" : "false";
  return String(c.value ?? c.min ?? 0);
}

/** A slider's step: its own, else a hundredth of its range. */
export const sliderStep = (c: UIRowControl): number => (typeof c.step === "number" && c.step > 0 ? c.step : ((c.max ?? 1) - (c.min ?? 0)) / 100 || 1);

/** A number on a control's grid (min + k·step), held to min…max, without float dust (22.499999 → 22.5). */
export function snap(v: number, c: UIRowControl): number {
  const step = c.kind === "slider" ? sliderStep(c) : c.step && c.step > 0 ? c.step : 1;
  const base = c.min ?? 0;
  let x = base + Math.round((v - base) / step) * step;
  if (typeof c.min === "number") x = Math.max(c.min, x);
  if (typeof c.max === "number") x = Math.min(c.max, x);
  const decimals = Math.min(6, Math.max(decimalsOf(step), decimalsOf(base)));
  return Number(x.toFixed(decimals));
}

function decimalsOf(n: number): number {
  const s = String(n);
  const e = s.indexOf("e-");
  if (e >= 0) return Number(s.slice(e + 2));
  const dot = s.indexOf(".");
  return dot < 0 ? 0 : s.length - dot - 1;
}

/** How a slider's or a stepper's value is written: the extension's display for the value it drew, else the number and unit. */
export function controlText(c: UIRowControl, value: number): string {
  if (c.display && value === c.value) return c.display;
  return `${value}${c.unit ? ` ${c.unit}` : ""}`;
}

// ── fields ──────────────────────────────────────────────

/** A multichoice field's value (a JSON array of the chosen values) as a list; anything else reads as none chosen. */
export function multiValues(value?: string): string[] {
  if (!value) return [];
  try {
    const v: unknown = JSON.parse(value);
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

/** The value a multichoice field sends: the chosen ones in the choices' order, as JSON. */
export function multiText(chosen: readonly string[], order: readonly { value: string }[]): string {
  const set = new Set(chosen);
  return JSON.stringify(order.map((c) => c.value).filter((v) => set.has(v)));
}

/** The lines a multiline field grows to: 3 at least, 8 at most. */
export const multilineRows = (text: string): number => Math.min(8, Math.max(3, text.split("\n").length));

// ── charts ──────────────────────────────────────────────

/** A sparkline's points in a w × h box (1px inset so the stroke is not cut), and the area under it. */
export function sparkPoints(values: readonly number[], w: number, h: number): { line: string; area: string } {
  const n = values.length;
  if (n < 2) return { line: "", area: "" };
  const lo = Math.min(...values), hi = Math.max(...values);
  const y = (v: number) => (hi === lo ? h / 2 : 1 + (1 - (v - lo) / (hi - lo)) * (h - 2));
  const pts = values.map((v, i) => `${((i / (n - 1)) * (w - 2) + 1).toFixed(1)},${y(v).toFixed(1)}`);
  return { line: pts.join(" "), area: `1,${h} ${pts.join(" ")} ${w - 1},${h}` };
}

/** The color of a spark: its color, else its tone's role color, else the quiet ink. */
export function sparkColor(s: UISpark): string {
  if (colorOf(s.color)) return hue(s.color);
  if (s.tone === "positive") return "var(--green)";
  if (s.tone === "caution") return "var(--orange)";
  if (s.tone === "danger") return "var(--red)";
  return "var(--ink-3)";
}

/** Each label's stacked bounds of an area chart (each series on the ones before it); not stacked, from zero. */
export function areaBands(series: readonly ChartSeries[], stacked: boolean): { from: number; to: number }[][] {
  const n = series[0]?.points.length ?? 0;
  const acc = new Array<number>(n).fill(0);
  return series.map((s) =>
    Array.from({ length: n }, (_, i) => {
      const v = s.points[i]?.value ?? 0;
      if (!stacked) return { from: 0, to: v };
      const from = acc[i];
      acc[i] += v;
      return { from, to: acc[i] };
    }),
  );
}

/** A candle's four numbers (value is the close when close is missing); null when it is not a candle. */
export function candleOf(p: { value: number; open?: number; high?: number; low?: number; close?: number }) {
  const close = typeof p.close === "number" ? p.close : p.value;
  if (typeof p.open !== "number" || typeof p.high !== "number" || typeof p.low !== "number") return null;
  return { open: p.open, high: p.high, low: p.low, close, up: close >= p.open };
}

/** The lowest low and the highest high of a candlestick's points (a little room above and below). */
export function candleExtent(points: readonly { value: number; open?: number; high?: number; low?: number; close?: number }[]): [number, number] {
  const lows: number[] = [], highs: number[] = [];
  for (const p of points) {
    const c = candleOf(p);
    lows.push(c ? c.low : p.value);
    highs.push(c ? c.high : p.value);
  }
  if (!lows.length) return [0, 1];
  return [Math.min(...lows), Math.max(...highs)];
}

/** A rising and a falling candle's colors: rising green (the default) or red, the falling one the other. */
export function candleColors(rising?: string): { up: string; down: string } {
  return rising === "red" ? { up: "var(--red)", down: "var(--green)" } : { up: "var(--green)", down: "var(--red)" };
}

/**
 * An arc of a pie (inner 0) or a donut, from angle a0 to a1 (radians, 0 at 12 o'clock, clockwise), around (cx, cy).
 * A whole circle is drawn as two halves (an SVG arc cannot start and end at the same point).
 */
export function arcPath(cx: number, cy: number, outer: number, inner: number, a0: number, a1: number): string {
  if (a1 - a0 >= Math.PI * 2 - 1e-6) {
    const mid = a0 + Math.PI;
    return arcPath(cx, cy, outer, inner, a0, mid) + arcPath(cx, cy, outer, inner, mid, a0 + Math.PI * 2);
  }
  const pt = (r: number, a: number) => `${(cx + r * Math.sin(a)).toFixed(2)},${(cy - r * Math.cos(a)).toFixed(2)}`;
  const large = a1 - a0 > Math.PI ? 1 : 0;
  if (inner <= 0) return `M${cx},${cy}L${pt(outer, a0)}A${outer},${outer} 0 ${large} 1 ${pt(outer, a1)}Z`;
  return `M${pt(outer, a0)}A${outer},${outer} 0 ${large} 1 ${pt(outer, a1)}L${pt(inner, a1)}A${inner},${inner} 0 ${large} 0 ${pt(inner, a0)}Z`;
}

/** The slices of a donut or a pie: each point's angles and its share (negative values count as none). */
export function slices(values: readonly number[]): { a0: number; a1: number; share: number }[] {
  const vs = values.map((v) => (Number.isFinite(v) && v > 0 ? v : 0));
  const total = vs.reduce((a, b) => a + b, 0);
  let at = 0;
  return vs.map((v) => {
    const share = total > 0 ? v / total : 0;
    const a0 = at;
    at += share * Math.PI * 2;
    return { a0, a1: at, share };
  });
}

/** A share as a percent the legend writes: 12.5%, 3%, <1% for a sliver. */
export function shareText(share: number): string {
  if (share <= 0) return "0%";
  if (share < 0.01) return "<1%";
  const p = share * 100;
  return `${p >= 10 ? Math.round(p) : Number(p.toFixed(1))}%`;
}
