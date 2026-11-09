// UI version 6's components on the web (docs/extensions/extensions.md "UI version 6"), drawn by ViewBody.tsx: a picture (image), a
// row's or a tile's switch / slider / stepper, a row's extra actions (right click, a long press, the … button), grids
// and carousels of tiles, sparklines, a named color's badge, and the new fields (date, time, datetime, multichoice,
// multiline). The readings and the arithmetic are in lib/extension-ui-v6.ts; what a control's change does (shown at
// once, put back when it fails) is settings/ExtensionView.tsx's.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { rowIcon, type UIAction, type UIItem } from "../../lib/extension-ui";
import {
  aspectRatio, colorOf, controlText, controlValue, hue, hueTint, imageSrc, isHubImage, multilineRows, multiText, multiValues, sliderStep, snap, sparkColor, sparkPoints,
  type UIImageSource, type UIRowAction, type UIRowControl, type UISpark, type UITile,
} from "../../lib/extension-ui-v6";
import { t } from "../../i18n";
import { cx, Dialog, Button, Menu, Spinner } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { SearchField } from "../../ui/SearchField";
import { contextMenuProps } from "../../ui/context-menu";
import type { MenuAt, MenuItem } from "../../ui/menu-model";
import { Pill, Switch } from "../settings/SettingsParts";
import { ImageViewer } from "../chat/ImageViewer";
import { Slider } from "../../ui/Slider";
import { NEW_ICON_GLYPH } from "./icon-map";

/** What UI version 6's components are drawn with, besides a view's handlers. */
export interface V6Handlers {
  /** The value to show for a control, by its name, while the user's change is on its way (else the extension's). */
  controls?: Record<string, string>;
  /** The controls whose change has been on its way for more than a second (a small spinner shows). */
  slow?: Record<string, boolean>;
  /** A control was changed: switch "true" / "false", slider and stepper the number. Absent: controls only show. */
  onControl?: (c: UIRowControl, value: string) => void;
  /** A hub picture did not load (its token may have run out): the page is drawn again, which signs anew. */
  onImageFailed?: () => void;
}

/** An icon of the fixed vocabulary as one of the web's glyphs (the UI version 6 ones included); unknown: none. */
export function glyphOf(name: string | undefined, base: Record<string, IconName>): IconName | undefined {
  const known = rowIcon(name);
  if (!known) return undefined;
  return base[known] ?? NEW_ICON_GLYPH[known as keyof typeof NEW_ICON_GLYPH];
}

// ── small parts ─────────────────────────────────────────

/** A small trend line: values only, no axes, in its color (or tone; else the quiet ink). */
export function Sparkline({ spark, width = 56, height = 20, className }: { spark: UISpark; width?: number; height?: number; className?: string }) {
  const { line, area } = sparkPoints(spark.values, width, height);
  if (!line) return null;
  const color = sparkColor(spark);
  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden="true" className={cx("shrink-0 overflow-visible", className)}>
      {spark.fill && <polygon points={area} fill={color} fillOpacity={0.14} />}
      <polyline points={line} fill="none" stroke={color} strokeWidth={1.6} strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

const BADGE_TONE: Record<string, "green" | "orange" | "red" | undefined> = { positive: "green", caution: "orange", danger: "red" };

/** A badge: a named color's (UI version 6) tinted in it, else its tone's. */
export function Badge({ badge }: { badge: { text: string; tone?: string; color?: string } }) {
  if (!badge.text) return null;
  if (colorOf(badge.color))
    return (
      <span className="inline-flex h-5 shrink-0 items-center whitespace-nowrap rounded-full px-1.5 text-[11px] font-medium text-ink" style={{ background: hueTint(badge.color), boxShadow: `inset 0 0 0 1px color-mix(in srgb, ${hue(badge.color)} 45%, transparent)` }}>
        {/* the text stays in the ink (a named color is never text's); a dot of the color says which */}
        <span aria-hidden className="mr-1 inline-block size-1.5 rounded-full" style={{ background: hue(badge.color) }} />
        {badge.text}
      </span>
    );
  return <Pill tone={BADGE_TONE[badge.tone ?? ""]} animated>{badge.text}</Pill>;
}

// ── a control in a row or a tile ─────────────────────────

/** The value shown for a control: the user's while it is on its way, else what the extension drew. */
export const shownValue = (c: UIRowControl, h: V6Handlers) => h.controls?.[c.name] ?? controlValue(c);

/** A switch or a stepper at a row's end (a slider has a line of its own: SliderLine). */
export function TrailingControl({ control: c, label, h }: { control: UIRowControl; label: string; h: V6Handlers }) {
  const value = shownValue(c, h);
  const slow = h.slow?.[c.name];
  const disabled = c.disabled || !h.onControl;
  if (c.kind === "switch")
    return (
      <span className="flex shrink-0 items-center gap-2">
        {slow && <Spinner />}
        <Switch on={value === "true"} disabled={disabled} label={label} onChange={(on) => h.onControl?.(c, on ? "true" : "false")} />
      </span>
    );
  if (c.kind === "stepper") return <Stepper control={c} value={Number(value)} label={label} disabled={disabled} slow={slow} onSend={(v) => h.onControl?.(c, String(v))} />;
  return null;
}

/**
 * − value +: each tap shows the next value at once; the last one is sent 0.6 s after the taps stop (as the arrow keys'
 * presses). The value is read out as it changes.
 */
function Stepper({ control: c, value, label, disabled, slow, onSend }: { control: UIRowControl; value: number; label: string; disabled: boolean; slow?: boolean; onSend: (v: number) => void }) {
  const [local, setLocal] = useState<number | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const pending = useRef<number | null>(null);
  const send = useRef(onSend);
  send.current = onSend;
  // leaving the page with taps not sent yet: they are sent
  useEffect(
    () => () => {
      clearTimeout(timer.current);
      if (pending.current !== null) send.current(pending.current);
    },
    [],
  );
  const shown = local ?? value;
  const step = c.step && c.step > 0 ? c.step : 1;
  const bump = (dir: 1 | -1) => {
    const next = snap(shown + dir * step, c);
    if (next === shown) return;
    setLocal(next);
    pending.current = next;
    clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      pending.current = null;
      setLocal(null);
      send.current(next);
    }, 600);
  };
  const atMin = typeof c.min === "number" && shown <= c.min;
  const atMax = typeof c.max === "number" && shown >= c.max;
  const btn = "touch-hit flex size-7 items-center justify-center rounded-full text-ink-2 shadow-hairline transition-colors enabled:hover:bg-hover disabled:opacity-40";
  return (
    <span role="group" aria-label={label} className="flex shrink-0 items-center gap-1.5">
      {slow && <Spinner />}
      <button type="button" className={btn} disabled={disabled || atMin} aria-label={t("extensions-viewbody.decrease", { name: label })} onClick={() => bump(-1)}>
        <Icon name="minus" size={13} stroke={2.2} />
      </button>
      <span aria-live="polite" className="min-w-[56px] text-center text-[13px] whitespace-nowrap text-ink tabular-nums">
        {controlText(c, shown)}
      </span>
      <button type="button" className={btn} disabled={disabled || atMax} aria-label={t("extensions-viewbody.increase", { name: label })} onClick={() => bump(1)}>
        <Icon name="plus" size={13} stroke={2.2} />
      </button>
    </span>
  );
}

/**
 * A slider's line under its row: the value moves with the thumb, and is sent when the pointer lets go (the arrow
 * keys: 0.6 s after the last press), not on every step of the drag.
 */
export function SliderLine({ control: c, label, h }: { control: UIRowControl; label: string; h: V6Handlers }) {
  const extension = Number(shownValue(c, h));
  const [drag, setDrag] = useState<number | null>(null);
  const shown = drag ?? extension;
  const disabled = c.disabled || !h.onControl;
  const commit = (v: number) => {
    setDrag(null);
    const next = snap(v, c);
    if (String(next) !== shownValue(c, h)) h.onControl?.(c, String(next));
  };
  return (
    <div className="flex items-center gap-3 px-4 pb-2.5">
      <Slider
        value={shown}
        min={c.min ?? 0}
        max={c.max ?? 100}
        step={sliderStep(c)}
        disabled={disabled}
        label={label}
        valueText={controlText(c, shown)}
        onInput={setDrag}
        onCommit={commit}
        className="min-w-0 flex-1"
      />
      {h.slow?.[c.name] && <Spinner />}
    </div>
  );
}

// ── a row's (or a tile's) extra actions ──────────────────

/**
 * The menu of a row's extra actions: a right click on the row (at the pointer), the menu key, a long press (half a
 * second) on a touch screen, and the … button at the row's end (always there: a mouse has no long press).
 * A destructive one is red and last; its confirm is asked by the page, as any action's.
 */
export function useRowMenu(actions: UIRowAction[] | undefined, glyphs: Record<string, IconName>, onPress: (a: UIAction, destructive?: boolean) => void, enabled = true) {
  const [at, setAt] = useState<MenuAt | null>(null);
  const press = useRef<{ timer: ReturnType<typeof setTimeout>; x: number; y: number } | null>(null);
  const opened = useRef(false);
  const has = !!actions?.length && enabled;
  const items: MenuItem[] = (actions ?? []).map((a, i) => ({
    key: `${i}:${a.title}`,
    label: a.title,
    icon: glyphOf(a.icon, glyphs),
    danger: a.role === "destructive",
    divider: a.role === "destructive" && i > 0,
    onSelect: () => onPress(a.action, a.role === "destructive"),
  }));
  const ctx = contextMenuProps((m) => setAt(m));
  const cancel = () => {
    if (press.current) clearTimeout(press.current.timer);
    press.current = null;
  };
  const props = has
    ? {
        ...ctx,
        onPointerDown: (e: React.PointerEvent<HTMLElement>) => {
          opened.current = false;
          if (e.pointerType !== "touch") return;
          const { clientX: x, clientY: y } = e;
          cancel();
          press.current = { x, y, timer: setTimeout(() => ((opened.current = true), setAt({ point: { x, y } })), 500) };
        },
        onPointerMove: (e: React.PointerEvent<HTMLElement>) => {
          if (press.current && Math.hypot(e.clientX - press.current.x, e.clientY - press.current.y) > 8) cancel();
        },
        onPointerUp: cancel,
        onPointerCancel: cancel,
        // the tap that ended a long press does not also press the row
        onClickCapture: (e: React.MouseEvent<HTMLElement>) => {
          if (opened.current) {
            e.preventDefault();
            e.stopPropagation();
            opened.current = false;
          }
        },
      }
    : {};
  const button = has ? (
    <button
      type="button"
      aria-label={t("extensions-viewbody.more_actions")}
      aria-haspopup="menu"
      title={t("extensions-viewbody.more_actions")}
      onClick={(e) => setAt({ anchor: e.currentTarget })}
      className="touch-hit flex size-7 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors group-hover/row:text-ink-2 hover:bg-hover hover:text-ink"
    >
      <Icon name="more" size={14} />
    </button>
  ) : null;
  const menu = at ? <Menu items={items} anchor={at.anchor ?? null} point={at.point} align="right" onClose={() => setAt(null)} /> : null;
  return { props, button, menu };
}

// ── image ───────────────────────────────────────────────

/** A picture (UI version 6): a frame of its aspect drawn before it loads, its alt when it does not; a click shows it full screen. */
export function ImageItem({ item, compact, onImageFailed }: { item: UIItem; compact?: boolean; onImageFailed?: () => void }) {
  const src = imageSrc(item.src);
  const [failed, setFailed] = useState<string | undefined>(undefined);
  const [natural, setNatural] = useState<number | null>(null);
  const [open, setOpen] = useState(false);
  const alt = item.alt ?? "";
  const ratio = item.aspect === "auto" && natural && !(item.src?.width && item.src?.height) ? natural : aspectRatio(item.aspect ?? "landscape", item.src);
  const broken = !src || failed === src;
  const zoom = !item.no_zoom && !broken;
  const img = src && !broken && (
    <img
      src={src}
      alt={alt}
      loading="lazy"
      decoding="async"
      referrerPolicy="no-referrer"
      draggable={false}
      onLoad={(e) => e.currentTarget.naturalHeight && setNatural(e.currentTarget.naturalWidth / e.currentTarget.naturalHeight)}
      onError={() => {
        setFailed(src);
        if (isHubImage(item.src)) onImageFailed?.();
      }}
      className={cx("size-full", item.fit === "fit" ? "object-contain" : "object-cover")}
    />
  );
  return (
    <figure className={cx("px-4", compact ? "py-2" : "py-2.5")}>
      {/* on a wide page a tall picture stops at 480px high (centered), as a phone shows it */}
      <div className="relative mx-auto overflow-hidden rounded-card bg-inset shadow-hairline" style={{ aspectRatio: String(ratio), maxWidth: `${Math.round(480 * ratio)}px` }}>
        {broken ? (
          <div role="img" aria-label={alt} className="flex size-full flex-col items-center justify-center gap-1.5 px-4 text-center text-ink-3">
            <Icon name="image" size={18} />
            <span className="line-clamp-2 text-[11.5px]">{alt}</span>
          </div>
        ) : zoom ? (
          <button type="button" onClick={() => setOpen(true)} aria-label={t("extensions-viewbody.view_image", { alt })} className="block size-full cursor-zoom-in">
            {img}
          </button>
        ) : (
          img
        )}
      </div>
      {item.caption && <figcaption className="mt-1.5 text-[11.5px] leading-relaxed break-words text-ink-3">{item.caption}</figcaption>}
      {open && src && <ImageViewer images={[{ id: item.key ?? src, src, label: alt }]} start={0} onClose={() => setOpen(false)} />}
    </figure>
  );
}

/** A small picture (a thumbnail, an avatar, a tile's): the hub's or an https one; null when it cannot be loaded. */
export function useImage(src: UIImageSource | undefined, onImageFailed?: () => void) {
  const url = imageSrc(src);
  const [failed, setFailed] = useState<string | undefined>(undefined);
  const ok = !!url && failed !== url;
  return {
    url: ok ? url : undefined,
    onError: () => {
      setFailed(url);
      if (isHubImage(src)) onImageFailed?.();
    },
  };
}

// ── grid, carousel, tile ────────────────────────────────

type TileHandlers = V6Handlers & {
  glyphs: Record<string, IconName>;
  busy: string;
  onPress: (a: UIAction, destructive?: boolean) => void;
  canPress?: (a: UIAction) => boolean;
};

/**
 * Tiles in columns that fit: each at least 150px, as many as fit, never more than max_columns (4 when absent). The
 * columns are a CSS grid's (a container's width, not the window's: a card is narrow on a wide page).
 */
export function Grid({ item, compact, ...h }: TileHandlers & { item: UIItem; compact?: boolean }) {
  const tiles = (item.tiles ?? []).slice(0, compact ? 4 : 60);
  const max = Math.min(6, Math.max(1, Math.round(item.max_columns ?? 4)));
  const gap = 8;
  return (
    <div
      role="list"
      className={cx("grid", compact ? "px-4 py-2" : "")}
      style={{ gap, gridTemplateColumns: `repeat(auto-fill, minmax(max(150px, calc((100% - ${(max - 1) * gap}px) / ${max})), 1fr))` }}
    >
      {tiles.map((tile, i) => (
        <div role="listitem" key={tile.key ?? i} className="min-w-0">
          <TileView tile={tile} compact={compact} {...h} />
        </div>
      ))}
    </div>
  );
}

const TILE_WIDTH: Record<string, string> = { narrow: "w-[140px]", regular: "w-[200px]", wide: "w-[80%] max-w-[560px]" };

/**
 * Tiles in one line that scrolls sideways, each snapping to the start; the next one shows a little at the edge. On a
 * pointer that hovers, ‹ › buttons at the sides scroll by a page (reachable from the keyboard too).
 */
export function Carousel({ item, compact, ...h }: TileHandlers & { item: UIItem; compact?: boolean }) {
  const tiles = (item.tiles ?? []).slice(0, compact ? 6 : 20);
  const box = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ start: true, end: true });
  const measure = () => {
    const el = box.current;
    if (!el) return;
    setEdges({ start: el.scrollLeft <= 2, end: el.scrollLeft + el.clientWidth >= el.scrollWidth - 2 });
  };
  useLayoutEffect(() => {
    measure();
    const el = box.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [tiles.length]);
  const page = (dir: 1 | -1) => box.current?.scrollBy({ left: dir * box.current.clientWidth * 0.8, behavior: "smooth" });
  const arrow = "absolute top-1/2 z-[1] hidden size-8 -translate-y-1/2 items-center justify-center rounded-full bg-surface text-ink-2 shadow-raised transition-opacity [@media(hover:hover)]:flex opacity-0 group-hover/carousel:opacity-100 focus-visible:opacity-100";
  return (
    <div className={cx(compact ? "py-2" : "")}>
      {item.title && <h4 className={cx("mb-1.5 truncate px-1 text-[12.5px] font-medium text-ink-2", compact && "px-4")}>{item.title}</h4>}
      <div className="group/carousel relative">
        <div
          ref={box}
          role="list"
          aria-label={item.title}
          onScroll={measure}
          className={cx("flex snap-x snap-mandatory gap-2 overflow-x-auto scrollbar-none", compact ? "scroll-px-4 px-4" : "-mx-1 scroll-px-1 px-1 py-1")}
        >
          {tiles.map((tile, i) => (
            <div role="listitem" key={tile.key ?? i} className={cx("shrink-0 snap-start", TILE_WIDTH[item.width ?? "regular"] ?? TILE_WIDTH.regular)}>
              <TileView tile={tile} compact={compact} {...h} />
            </div>
          ))}
        </div>
        {!edges.start && (
          <button type="button" aria-label={t("extensions-viewbody.scroll_back")} onClick={() => page(-1)} className={cx(arrow, "left-1")}>
            <Icon name="chevronLeft" size={14} stroke={2.2} />
          </button>
        )}
        {!edges.end && (
          <button type="button" aria-label={t("extensions-viewbody.scroll_on")} onClick={() => page(1)} className={cx(arrow, "right-1")}>
            <Icon name="chevronRight" size={14} stroke={2.2} />
          </button>
        )}
      </div>
    </div>
  );
}

/**
 * A tile: its picture across the top (16:9) or its icon in its color, the title, a state line, a value, a spark;
 * pressing it does its action. One that is on lays its color under it and around it; the text stays in the ink.
 */
function TileView({ tile, compact, glyphs, busy, onPress, canPress, ...h }: TileHandlers & { tile: UITile; compact?: boolean }) {
  const pic = useImage(tile.image, h.onImageFailed);
  const glyph = glyphOf(tile.icon, glyphs);
  const menu = useRowMenu(tile.actions, glyphs, onPress, !busy);
  const on = !!tile.active || (tile.control ? shownValue(tile.control, h) === "true" : false);
  const color = colorOf(tile.color) ? tile.color : undefined;
  const pressable = !!tile.action && (canPress ? canPress(tile.action) : true);
  const label = [tile.title, tile.subtitle, tile.value, on ? t("extensions-viewbody.tile_on") : ""].filter(Boolean).join(t("extensions-viewbody.read_sep"));
  const body = (
    <>
      {tile.image && (
        <span className="block aspect-video w-full overflow-hidden bg-inset">
          {pic.url ? <img src={pic.url} alt="" loading="lazy" decoding="async" referrerPolicy="no-referrer" onError={pic.onError} className="size-full object-cover" /> : <span className="flex size-full items-center justify-center text-ink-3"><Icon name="image" size={16} /></span>}
        </span>
      )}
      <span className="flex min-w-0 flex-col gap-0.5 p-3">
        {!tile.image && (
          <span
            className="mb-1.5 flex size-8 items-center justify-center rounded-[9px]"
            style={on ? { background: color ? hue(color) : "var(--accent)", color: "var(--surface)" } : { background: color ? hueTint(color) : "var(--inset)", color: color ? hue(color) : "var(--ink-2)" }}
          >
            <Icon name={glyph ?? "extension"} size={16} stroke={on ? 2.2 : 1.9} />
          </span>
        )}
        <span className="truncate text-[13px] font-medium text-ink">{tile.title}</span>
        {tile.subtitle && <span className="truncate text-[11.5px] text-ink-3">{tile.subtitle}</span>}
        {(tile.value || tile.spark) && (
          <span className="mt-1 flex min-w-0 items-end gap-2">
            {tile.value && <span className="min-w-0 flex-1 truncate text-[15px] leading-tight font-semibold text-ink tabular-nums">{tile.value}</span>}
            {tile.spark && <Sparkline spark={tile.spark} width={48} height={18} className={cx(!tile.value && "ml-auto")} />}
          </span>
        )}
      </span>
    </>
  );
  return (
    <div
      {...menu.props}
      className={cx("group/row relative h-full overflow-hidden rounded-card transition-[box-shadow,background-color] duration-200", compact ? "bg-inset shadow-hairline" : "bg-surface shadow-card")}
      style={on ? { background: `linear-gradient(${hueTint(color, "var(--accent-tint)")}, ${hueTint(color, "var(--accent-tint)")}), var(${compact ? "--inset" : "--surface"})`, boxShadow: `inset 0 0 0 1px ${color ? hue(color) : "var(--accent)"}` } : undefined}
    >
      {tile.action ? (
        <button
          type="button"
          aria-label={label}
          disabled={!!busy || !pressable}
          onClick={() => onPress(tile.action!)}
          className="flex h-full w-full flex-col text-left whitespace-nowrap transition-colors enabled:hover:bg-hover/60 disabled:cursor-default"
        >
          {body}
        </button>
      ) : (
        <div aria-label={label} role="group" className="flex h-full w-full flex-col whitespace-nowrap">
          {body}
        </div>
      )}
      {/* what sits on the tile's corner: its badge, its switch, its … menu (beside, not inside, its button) */}
      <span className={cx("absolute right-2 flex items-center gap-1", tile.image ? "top-2" : "top-2.5")}>
        {busy && tile.action && busy === tile.action.id && <Spinner />}
        {/* over a picture, on the surface so it reads on any photo */}
        {tile.badge?.text && (tile.image ? <span className="rounded-full bg-surface">{<Badge badge={tile.badge} />}</span> : <Badge badge={tile.badge} />)}
        {tile.control && <TrailingControl control={tile.control} label={tile.title} h={h} />}
      </span>
      {menu.button && <span className="absolute right-1.5 bottom-1.5">{menu.button}</span>}
      {menu.menu}
    </div>
  );
}

// ── fields ──────────────────────────────────────────────

const fieldClass =
  "h-8 rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

/** date, time, datetime: the browser's own pickers, whose values are the wire's (YYYY-MM-DD, HH:MM, YYYY-MM-DDTHH:MM). */
export function DateInput({ id, item, value, locked, onChange }: { id: string; item: UIItem; value: string; locked?: boolean; onChange: (v: string) => void }) {
  const type = item.kind === "date" ? "date" : item.kind === "time" ? "time" : "datetime-local";
  return (
    <input
      id={id}
      type={type}
      value={value}
      min={item.earliest || undefined}
      max={item.latest || undefined}
      disabled={locked}
      onChange={(e) => onChange(e.target.value)}
      className={cx(fieldClass, "w-full max-w-[240px] tabular-nums")}
    />
  );
}

/** multiline: a box that grows with what is typed, three lines to eight. */
export function MultilineInput({ id, item, value, locked, onChange }: { id: string; item: UIItem; value: string; locked?: boolean; onChange: (v: string) => void }) {
  return (
    <textarea
      id={id}
      value={value}
      rows={multilineRows(value)}
      disabled={locked}
      placeholder={item.placeholder}
      onChange={(e) => onChange(e.target.value)}
      className={cx(fieldClass, "block h-auto w-full resize-none py-1.5 leading-relaxed")}
    />
  );
}

/**
 * multichoice: a few (6 or fewer) as pills that check and uncheck; more as a line saying how many are chosen that
 * opens them in a list with a search box. The value is the chosen ones' JSON array, in the choices' order.
 */
export function MultiChoice({ id, item, value, locked, onChange }: { id: string; item: UIItem; value: string; locked?: boolean; onChange: (v: string) => void }) {
  const choices = item.choices ?? [];
  const chosen = multiValues(value);
  const toggle = (v: string) => onChange(multiText(chosen.includes(v) ? chosen.filter((x) => x !== v) : [...chosen, v], choices));
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  if (choices.length <= 6)
    return (
      <div id={id} role="group" aria-label={item.label} className="flex flex-wrap gap-1.5">
        {choices.map((c) => {
          const on = chosen.includes(c.value);
          return (
            <button
              key={c.value}
              type="button"
              role="checkbox"
              aria-checked={on}
              disabled={locked}
              onClick={() => toggle(c.value)}
              className={cx(
                "flex h-7 items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 text-[12px] transition-[background-color,box-shadow,color] duration-150 disabled:opacity-60",
                on ? "bg-accent-tint font-medium text-accent-ink shadow-[inset_0_0_0_1px_var(--accent)]" : "text-ink-2 shadow-hairline hover:bg-hover",
              )}
            >
              {on && <Icon name="check" size={12} stroke={2.2} />}
              {c.title || c.value}
            </button>
          );
        })}
      </div>
    );
  const titles = choices.filter((c) => chosen.includes(c.value)).map((c) => c.title || c.value);
  const needle = q.trim().toLowerCase();
  const shown = needle ? choices.filter((c) => (c.title || c.value).toLowerCase().includes(needle)) : choices;
  return (
    <>
      <button id={id} type="button" disabled={locked} onClick={() => setOpen(true)} className={cx(fieldClass, "flex w-full max-w-[320px] items-center gap-2 text-left whitespace-nowrap")}>
        <span className="min-w-0 flex-1 truncate">{titles.length ? titles.join(t("extensions-viewbody.list_sep")) : <span className="text-ink-3">{t("extensions-viewbody.none_chosen")}</span>}</span>
        <span className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{t("extensions-viewbody.chosen_count", { count: titles.length })}</span>
        <Icon name="chevron" size={12} className="shrink-0 text-ink-3" />
      </button>
      {open && (
        <Dialog
          open
          onClose={() => setOpen(false)}
          width={380}
          title={item.label}
          footer={
            <Button tone="primary" onClick={() => setOpen(false)}>
              {t("extensions-viewbody.done")}
            </Button>
          }
        >
          <SearchField value={q} onChange={setQ} placeholder={t("extensions-viewbody.search_choices")} />
          <div role="group" aria-label={item.label} className="mt-2 max-h-[50vh] overflow-y-auto">
            {shown.map((c) => {
              const on = chosen.includes(c.value);
              return (
                <button
                  key={c.value}
                  type="button"
                  role="checkbox"
                  aria-checked={on}
                  onClick={() => toggle(c.value)}
                  className="flex h-9 w-full items-center gap-2.5 rounded-control px-2 text-left text-[13px] whitespace-nowrap text-ink transition-colors hover:bg-hover"
                >
                  <span className={cx("flex size-4 shrink-0 items-center justify-center rounded-[4px]", on ? "bg-accent text-white" : "shadow-[inset_0_0_0_1.5px_var(--line-strong)]")}>
                    {on && <Icon name="check" size={11} stroke={2.6} />}
                  </span>
                  <span className="min-w-0 flex-1 truncate">{c.title || c.value}</span>
                </button>
              );
            })}
            {!shown.length && <p className="px-2 py-3 text-[12px] text-ink-3">{t("extensions-viewbody.no_match")}</p>}
          </div>
        </Dialog>
      )}
    </>
  );
}
