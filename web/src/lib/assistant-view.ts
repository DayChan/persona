// The assistant's cards drawn with the extensions' components (docs/extensions/extensions.md "Declarative UI", #1850): a ```persona-view
// block is a View (or one component) as an extension would send it; the older ```persona-card block (#886: stat, bar,
// line, stacked_bar, ring) is turned into a View first, by the rules of sdk/go/wire/personacard.go (the reference; the cases in
// schema/extension/examples/persona-card/ hold every app to the same), so both look the same here as on every other app.
// In the assistant nothing acts: buttons, fields and segmented are left out, the search box and "Load more" never show; links
// still open.
import { t } from "../i18n";
import { readView, type ChartSeries, type UIItem, type UISection, type UIView } from "./extension-ui";

/** What a fenced block in an assistant's reply draws as: a view, or nothing (it stays a code block). */
export function assistantView(lang: string, code: string): UIView | null {
  let raw: unknown;
  try {
    raw = JSON.parse(code);
  } catch {
    return null; // still being written, or not JSON: the code block shows it
  }
  try {
    if (lang === "persona-card") {
      const v = cardView(raw);
      return v ? inert(v) : null;
    }
    if (lang === "persona-view") {
      if (isObject(raw) && Array.isArray(raw.sections)) return inert(readView(raw));
      if (isObject(raw) && typeof raw.type === "string") return inert(readView({ sections: [{ items: [raw] }] }));
    }
  } catch {
    return null;
  }
  return null;
}

/** Without what would act: an assistant's card has no extension behind it. */
function inert(v: UIView): UIView | null {
  const sections = v.sections
    .map((s) => ({
      ...(s.title ? { title: s.title } : {}),
      ...(s.footer ? { footer: s.footer } : {}),
      ...(s.key ? { key: s.key } : {}),
      items: s.items
        .filter((it) => it.type !== "button" && it.type !== "field" && it.type !== "segmented")
        .map((it) => (it.action || it.button ? { ...it, action: undefined, button: undefined } : it)),
    }))
    .filter((s) => s.items.length > 0 || s.footer);
  return sections.length ? { ...(v.title ? { title: v.title } : {}), sections } : null;
}

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const num = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** A number as the cards write it: grouped by thousands, at most one decimal (2,250 · 7.5 · -1,200.3 · 0). */
export function cardNumber(v: number): string {
  let r = Math.round(v * 10) / 10;
  if (r === 0) r = 0; // not -0
  const neg = r < 0;
  if (neg) r = -r;
  let whole = Math.floor(r);
  let tenth = Math.round((r - whole) * 10);
  if (tenth === 10) (whole += 1), (tenth = 0);
  const grouped = String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return (neg && (whole > 0 || tenth > 0) ? "-" : "") + grouped + (tenth > 0 ? `.${tenth}` : "");
}

/** A number and its unit: "2,250 kcal". */
const amount = (v: number, unit: string) => cardNumber(v) + (unit ? ` ${unit}` : "");

interface Part {
  label: string;
  value: number;
  note: string;
}

/** The labelled numbers of a card (points, segments, rest), a missing value 0; one that is not a number does not read
 *  (the card does not). */
function parts(raw: unknown): Part[] | null {
  if (raw === undefined || raw === null) return [];
  if (!Array.isArray(raw)) return null;
  const out: Part[] = [];
  for (const p of raw) {
    if (!isObject(p)) return null;
    const v = p.value === undefined ? 0 : num(p.value);
    if (v === undefined) return null;
    out.push({ label: str(p.label), value: v, note: str(p.note) });
  }
  return out;
}

/** A persona-card as the apps draw it (sdk/go/wire/personacard.go); null when they cannot: it stays a code block. */
export function cardView(raw: unknown): UIView | null {
  if (!isObject(raw)) return null;
  const segments = parts(raw.segments);
  const rest = isObject(raw.rest) ? parts([raw.rest]) : [];
  const points = parts(raw.points);
  if (!segments || !rest || !points) return null;
  const all = [...segments, ...rest];
  let sum = all.reduce((s, p) => s + p.value, 0);
  const value = num(raw.value);
  const title = str(raw.title);
  const unit = str(raw.unit);
  const note = str(raw.note);
  switch (raw.type) {
    case "stat":
      if (value === undefined) return null;
      break;
    case "stacked_bar":
      if (!all.length || sum <= 0 || all.some((p) => p.value < 0)) return null;
      break;
    case "ring": {
      // what the total leaves over the parts is a part of its own, last
      const total = isObject(raw.total) ? num(raw.total.value) : undefined;
      if (total !== undefined && total > sum) {
        all.push({ label: t("lib-assistant-view.remaining"), value: total - sum, note: "" });
        sum = total;
      }
      if (all.length < 2 || sum <= 0 || all.some((p) => p.value < 0)) return null;
      break;
    }
    case "line":
      if (points.length < 2) return null;
      break;
    case "bar":
      if (!points.length) return null;
      break;
    default:
      return null;
  }
  const s: UISection = { ...(title ? { title } : {}), items: [] };
  const subtitle = str(raw.subtitle);
  if (subtitle) s.items.push({ type: "text", text: subtitle, style: "secondary" });
  switch (raw.type) {
    case "stat":
      delete s.title;
      s.items.push({ type: "metric", label: title || t("lib-assistant-view.value"), value: amount(value!, unit), ...(note ? { detail: note } : {}) });
      break;
    case "stacked_bar": {
      const total = isObject(raw.total) ? num(raw.total.value) : undefined;
      const u = isObject(raw.total) ? str(raw.total.unit) || unit : unit;
      s.items.push({ type: "metric", label: t("lib-assistant-view.total"), value: amount(total ?? sum, u), ...(note ? { detail: note } : {}) });
      const label = title || t("lib-assistant-view.total");
      const shown = all.length > 4 ? all.slice(0, 3) : all;
      const series: ChartSeries[] = shown.map((p, i) => ({
        name: p.label,
        ...(rest.length && i === all.length - 1 ? { tone: "neutral" as const } : {}),
        points: [{ label, value: p.value, display: amount(p.value, u) }],
      }));
      if (all.length > 4) {
        const other = all.slice(3).reduce((x, p) => x + p.value, 0);
        series.push({ name: t("lib-assistant-view.other"), tone: "neutral", points: [{ label, value: other, display: amount(other, u) }] });
      }
      s.items.push({ type: "chart", kind: "bar", series, ...(u ? { unit: u } : {}), stacked: true });
      for (const p of all) s.items.push({ type: "row", value: amount(p.value, u), title: p.label, ...(p.note ? { subtitle: p.note } : {}) });
      break;
    }
    case "ring": {
      const total = isObject(raw.total) ? num(raw.total.value) : undefined;
      const u = isObject(raw.total) ? str(raw.total.unit) || unit : unit;
      const shown = all.length > 8 ? [...all.slice(0, 7), { label: t("lib-assistant-view.other"), value: all.slice(7).reduce((x, p) => x + p.value, 0), note: "" }] : all;
      const center = amount(total ?? sum, u);
      const item: UIItem = { type: "chart", kind: "donut", series: [{ points: shown.map((p) => ({ label: p.label, value: p.value, display: amount(p.value, u) })) }] };
      if (u) item.unit = u;
      if (note) item.detail = note;
      if ([...center].length <= 20) item.center = center;
      s.items.push(item);
      for (const p of all) s.items.push({ type: "row", value: amount(p.value, u), title: p.label, ...(p.note ? { subtitle: p.note } : {}) });
      break;
    }
    default: {
      if (value !== undefined) s.items.push({ type: "metric", label: t("lib-assistant-view.current"), value: amount(value, unit) });
      const pts = points.slice(-120).map((p) => ({ label: p.label, value: p.value, display: amount(p.value, unit) }));
      const item: UIItem = { type: "chart", kind: raw.type === "bar" ? "bar" : "line", series: [{ points: pts }] };
      if (unit) item.unit = unit;
      if (note) item.detail = note;
      s.items.push(item);
    }
  }
  return { sections: [s] };
}
