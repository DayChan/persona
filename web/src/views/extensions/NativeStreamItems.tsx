import { useEffect, useRef, useState } from "react";
import type { Component, ImageComponent, RowComponent, RowControl, Tile, View, UIStreamEvent } from "../../lib/extension-wire";
import type { UIAction, UIItem } from "../../lib/extension-ui";
import { imageURL, isHttps, toneOf } from "../../lib/extension-ui";
import { openLink } from "../../lib/links";
import { t } from "../../i18n";
import { Button, cx, Dialog, IconButton } from "../../ui/atoms";
import { Icon, supportsIcon } from "../../ui/icons";
import { NATIVE, NATIVE_COLOR, NativeSpark, nativeColor, nativeIcon, nativeActiveStyle } from "../../ui/ExtensionNative";
import { Pill } from "../settings/SettingsParts";
import { Avatar, PILL } from "./CodeViews";
import { type ViewHandlers } from "./ViewBody";
import { scopeLocal, useNativeState } from "./native-state";

import type { NativeMedia } from "./native-media";
export type { NativeMedia } from "./native-media";
export type DisplayedNativeAction = Pick<UIStreamEvent, "session" | "epoch" | "version" | "key" | "action">;
export type NativeNavigation = (action: UIAction, displayed: Readonly<DisplayedNativeAction>) => void;
export function nativeRenderGap(view: View, media?: NativeMedia, navigate?: NativeNavigation, readResult = false): string | undefined {
  const inspect = (value: unknown): string | undefined => {
    if (!value || typeof value !== "object") return;
    for (const [name, child] of Object.entries(value)) {
      if (name === "values") continue; // scalar callback maps are data, not native component properties
      if (name === "color" && "text" in value) return "badge.color";
      if ((name === "reply" || name === "open") && typeof child === "string" || name === "pop" && child === true || name === "page" && typeof child === "string") return "assistant-only action navigation";
      if (name === "icon" && typeof child === "string" && !supportsIcon(iconOf(child))) return `icon.${child}`;
      if (name === "context" && child && !readResult) return "diff.context requires result adapter";
      if (name === "push" && !navigate) return "action.push requires navigation adapter";
      if ((name === "color" || name === "icon_color") && typeof child === "string" && !NATIVE_COLOR[child]) return `color.${child}`;
      if (["src", "image", "avatar", "thumbnail"].includes(name) && child && typeof child === "object" && "ref" in child && !media) return "hub image requires provenance adapter";
      const gap = inspect(child); if (gap) return gap;
    }
  };
  return inspect(view);
}
const iconOf = nativeIcon;
const badge = (b?: { text: string; tone?: string }) => b && <Pill tone={PILL[toneOf(b.tone)]}>{b.text}</Pill>;

function Picture({ item, media }: { item: ImageComponent; media?: NativeMedia }) {
  const [zoom, setZoom] = useState(false);
  const [failed, setFailed] = useState(false);
  const url = item.src.url ? imageURL(item.src.url) : media?.(item.src);
  useEffect(() => setFailed(false), [url]);
  const aspectRatio = item.aspect === "auto" && item.src.width && item.src.height ? `${item.src.width} / ${item.src.height}` : NATIVE.aspect[item.aspect ?? "landscape"];
  const image = !url || failed ? <span role="img" aria-label={item.alt} className="flex h-24 items-center justify-center bg-inset text-ink-3">{item.alt}</span> : <img src={url} alt={item.alt} referrerPolicy="no-referrer" onError={() => setFailed(true)} className={cx("w-full", item.fit === "fit" ? "object-contain" : "object-cover")} style={{ aspectRatio }} />;
  return <figure className="overflow-hidden rounded-card bg-inset">
    {item.no_zoom ? image : <button type="button" className="block w-full" disabled={!url || failed} onClick={() => setZoom(true)} aria-label={item.alt}>{image}</button>}
    {item.caption !== undefined && <figcaption className="px-3 py-2 text-xs text-ink-3">{item.caption}</figcaption>}
    <Dialog open={zoom} onClose={() => setZoom(false)} title={item.alt}>{image}</Dialog>
  </figure>;
}

function Thumbnail({ url, title, aspect }: { url?: string; title: string; aspect?: string }) {
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [url]);
  const size = NATIVE.thumbnail[aspect ?? "square"];
  if (!url || failed) return <span role="img" aria-label={title} className={cx(size, "flex shrink-0 items-center justify-center rounded-control bg-inset text-ink-3")}><Icon name="image" /></span>;
  return <img src={url} alt="" onError={() => setFailed(true)} className={cx(size, "rounded-control object-cover")} referrerPolicy="no-referrer" />;
}
function Control({ control: c, handlers: h }: { control: RowControl; handlers: ViewHandlers }) {
  const local = scopeLocal(h.local, `control/${JSON.stringify([c.kind, c.name, c.action])}`);
  const [value, setValue] = useNativeState(local, "value", c.kind === "switch" ? String(c.on ?? false) : String(c.value ?? 0));
  const authoritative = `${c.kind}:${c.on ?? ""}:${c.value ?? ""}`;
  const previous = useRef(authoritative);
  useEffect(() => { if (previous.current !== authoritative) { previous.current = authoritative; local?.clear("value"); } }, [authoritative]);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const disabled = c.disabled || h.locked || !!h.busy || !h.canPress?.(c.action);
  const send = async (next: string) => {
    if (disabled) return;
    const previous = c.kind === "switch" ? String(c.on ?? false) : String(c.value ?? 0);
    const success = await h.onNativeAct?.(c.action, { [c.name]: next });
    if (success === false) setValue(previous);
  };
  const gesture = useRef<((next: string) => Promise<void>) | null>(null);
  const startGesture = () => { if (!disabled) gesture.current ??= send; };
  const sliderKey = (key: string) => ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End", "PageUp", "PageDown"].includes(key);
  const finishGesture = (next: string) => { const dispatch = gesture.current ?? send; gesture.current = null; void dispatch(next); };
  const min = c.min ?? 0, max = c.max ?? 100;
  const step = c.step ?? (c.kind === "slider" ? (max - min) / 100 : 1);
  const change = (next: string, delayed = false) => {
    if (disabled) return;
    setValue(next);
    if (delayed) { clearTimeout(timer.current); timer.current = setTimeout(() => void send(next), NATIVE.stepperDelay); }
    else void send(next);
  };
  if (c.kind === "switch") return <button type="button" role="switch" aria-label={c.name} aria-checked={value === "true"} disabled={disabled} onClick={() => change(String(value !== "true"))} className="rounded-control bg-field px-3 py-2 text-sm whitespace-nowrap text-ink shadow-hairline disabled:opacity-60">{t(value === "true" ? "extensions-native.on" : "extensions-native.off")}</button>;
  const number = Number(value);
  return <div className="flex min-w-0 items-center gap-2">
    {c.kind === "slider" ? <input aria-label={c.name} type="range" min={min} max={max} step={step} value={number} disabled={disabled}
      onPointerDown={startGesture} onKeyDown={(e) => { if (sliderKey(e.key)) startGesture(); }} onPointerCancel={() => { gesture.current = null; }} onBlur={() => { gesture.current = null; }}
      onChange={(e) => { if (!disabled) { startGesture(); setValue(e.target.value); } }} onPointerUp={(e) => finishGesture(e.currentTarget.value)} onKeyUp={(e) => { if (sliderKey(e.key)) finishGesture(e.currentTarget.value); }} /> : <>
      <IconButton icon="chevronLeft" label={t("extensions-native.decrease")} disabled={disabled || number <= min} onClick={() => change(String(Math.max(min, Number((number - step).toPrecision(12)))), true)} />
      <IconButton icon="plus" label={t("extensions-native.increase")} disabled={disabled || number >= max} onClick={() => change(String(Math.min(max, Number((number + step).toPrecision(12)))), true)} />
    </>}
    <output className="text-xs whitespace-nowrap text-ink-2 tabular-nums">{value === String(c.value ?? 0) && c.display !== undefined ? c.display : `${value}${c.unit ?? ""}`}</output>
  </div>;
}

function NativeRow({ item: it, handlers: h, media }: { item: RowComponent | Tile; handlers: ViewHandlers; media?: NativeMedia }) {
  const [more, setMore] = useNativeState(h.local, "more", false);
  const touch = useRef<{ x: number; y: number; timer?: ReturnType<typeof setTimeout> } | null>(null);
  const cancelTouch = () => { clearTimeout(touch.current?.timer); touch.current = null; };
  useEffect(() => cancelTouch, []);
  const disabled = !!h.busy || h.locked || (it.action && !h.canPress?.(it.action));
  const thumbnail = "thumbnail" in it && it.thumbnail;
  const thumbnailURL = thumbnail ? thumbnail.ref ? media?.(thumbnail) : imageURL(thumbnail.url) : undefined;
  const lead = "avatar" in it && it.avatar ? <Avatar avatar={it.avatar} name={it.title} /> : thumbnail ? <Thumbnail url={thumbnailURL} title={it.title} aspect={thumbnail.aspect} /> : it.icon ? <Icon name={iconOf(it.icon)} className="shrink-0" style={{ color: nativeColor("icon_color" in it ? it.icon_color : "color" in it ? it.color : undefined) }} /> : null;
  const body = <>{lead}<span className="min-w-0 flex-1"><span className="block truncate text-sm text-ink">{it.title}</span>{it.subtitle !== undefined && <span className="block truncate text-xs text-ink-3">{it.subtitle}</span>}</span>{it.value !== undefined && <span className="text-sm text-ink-2 tabular-nums">{it.value}</span>}{badge(it.badge)}</>;
  return <div style={"active" in it && it.active ? nativeActiveStyle(it.color) : undefined} className={cx("min-w-0", "active" in it && it.active && "bg-accent-tint")} onContextMenu={(e) => { if (it.actions?.length) { e.preventDefault(); setMore(true); } }}
    onPointerDown={(e) => { if (e.pointerType === "touch" && it.actions?.length && !(e.target as HTMLElement).closest("input,[role=switch]")) touch.current = { x: e.clientX, y: e.clientY, timer: setTimeout(() => setMore(true), NATIVE.longPressDelay) }; }}
    onPointerMove={(e) => { if (touch.current && Math.abs(e.clientY - touch.current.y) > NATIVE.swipeDistance) cancelTouch(); }}
    onPointerCancel={cancelTouch}
    onPointerUp={(e) => { if (touch.current && touch.current.x - e.clientX >= NATIVE.swipeDistance && it.actions?.some((a) => a.swipe)) setMore(true); cancelTouch(); }}>
    {"image" in it && it.image && <Picture item={{ type: "image", src: it.image, alt: it.title, no_zoom: true }} media={media} />}
    <div className={NATIVE.row}>
      {it.action ? <button type="button" className="flex min-w-0 flex-1 items-center gap-3 text-left disabled:opacity-60" disabled={!!disabled} onClick={() => h.onPress(it.action!)}>{body}</button> : <div className="flex min-w-0 flex-1 items-center gap-3">{body}</div>}
      {it.control && <Control control={it.control} handlers={h} />}
      {it.actions?.length ? <IconButton icon="more" label={t("extensions-native.actions")} active={more} onClick={() => setMore((v) => !v)} /> : null}
    </div>
    {it.spark && <div className="px-4 pb-2"><NativeSpark spark={it.spark} label={it.title} /></div>}
    {more && <div className="flex flex-wrap gap-2 px-4 pb-3">{it.actions?.map((a, i) => <Button key={`${a.action.id}/${i}`} icon={a.icon ? iconOf(a.icon) : undefined} tone={a.role === "destructive" ? "danger" : "secondary"} disabled={!!h.busy || h.locked || !h.canPress?.(a.action)} onClick={() => h.onPress(a.action)}>{a.title}</Button>)}</div>}
  </div>;
}

function NativeField({ item: it, handlers: h }: { item: Extract<Component, { type: "field" }>; handlers: ViewHandlers }) {
  const value = h.values[it.name] ?? "";
  const set = (next: string) => !h.locked && h.setValue(it.name, next);
  const common = { value, disabled: h.locked, onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => set(e.target.value), className: NATIVE.field, placeholder: it.placeholder };
  const id = `native-${it.key}`;
  return <div className="px-4 py-3">
    <label className={NATIVE.label} htmlFor={id}>{it.label}</label>
    {it.kind === "multichoice" ? <fieldset id={id} className="flex flex-wrap gap-2" aria-label={it.label} disabled={h.locked}>{it.choices?.map((c) => {
      let selected: string[] = []; try { const parsed: unknown = JSON.parse(value || "[]"); if (Array.isArray(parsed) && parsed.every((v) => typeof v === "string")) selected = parsed; } catch { /* invalid display values stay unselected */ }
      return <label key={c.value} className="flex items-center gap-2 text-sm"><input type="checkbox" checked={selected.includes(c.value)} onChange={(e) => set(JSON.stringify(e.target.checked ? [...selected, c.value] : selected.filter((v) => v !== c.value)))} />{c.title ?? c.value}</label>;
    })}</fieldset> : it.kind === "multiline" ? <textarea id={id} {...common} /> : <input id={id} {...common} type={it.kind === "datetime" ? "datetime-local" : it.kind} min={it.earliest} max={it.latest} />}
    {it.detail !== undefined && <p className={NATIVE.detail}>{it.detail}</p>}
  </div>;
}

export function streamItemRenderer(media?: NativeMedia) {
  return (item: UIItem, h: ViewHandlers): React.ReactNode | undefined => {
    const it = item as Component;
    switch (it.type) {
      case "text": if (it.style === "title" || it.style === "headline") return <p className={cx("px-4 py-3 font-semibold text-ink", it.style === "title" ? "text-xl" : "text-lg")}>{it.text}</p>; break;
      case "empty": return <div className="flex flex-col items-center gap-2 px-4 py-6">{it.icon && <Icon name={iconOf(it.icon)} style={{ color: nativeColor(it.icon_color) }} />}<p className={NATIVE.label}>{it.title}</p>{it.detail !== undefined && <p className={NATIVE.detail}>{it.detail}</p>}{it.button && <Button disabled={h.locked || !!h.busy || !h.canPress?.(it.button.action)} onClick={() => h.onPress(it.button!.action)}>{it.button.title}</Button>}</div>;
      case "row": return <NativeRow item={it} handlers={h} media={media} />;
      case "grid": case "carousel": return <div className="p-3">
        {it.type === "carousel" && it.title !== undefined && <h3 className={NATIVE.label}>{it.title}</h3>}
        <div className={it.type === "grid" ? cx("grid gap-3", NATIVE.grid[it.max_columns ?? 4]) : "flex gap-3 overflow-x-auto"}>
          {it.tiles.map((tile) => <div key={tile.key} data-native-key={tile.key} className={cx(NATIVE.card, it.type === "carousel" && "shrink-0", it.type === "carousel" && NATIVE.tileWidth[it.width ?? "regular"])}><NativeRow item={tile} handlers={h.nodeHandlers!(tile.key!)} media={media} /></div>)}
        </div>
      </div>;
      case "image": return <div className="p-3"><Picture item={it} media={media} /></div>;
      case "field": if (["date", "time", "datetime", "multichoice", "multiline"].includes(it.kind)) return <NativeField item={it} handlers={h} />; break;
      case "metric": return <div className={NATIVE.row}>{it.icon && <Icon name={iconOf(it.icon)} style={{ color: nativeColor(it.icon_color) }} />}<div className="min-w-0 flex-1"><span className={NATIVE.detail}>{it.label}</span><div className={cx("text-xl font-semibold", NATIVE.tone[toneOf(it.tone)])}>{it.value}</div>{it.detail !== undefined && <p className={NATIVE.detail}>{it.detail}</p>}</div>{it.spark && <NativeSpark spark={it.spark} label={it.label} />}</div>;
      case "stat_grid": return <div className={cx("grid gap-3 p-4", NATIVE.grid[it.stats.length])}>{it.stats.map((stat, i) => <div key={i} className="min-w-0"><span className={NATIVE.detail}>{stat.label}</span><div className={cx("truncate text-xl font-semibold", NATIVE.tone[toneOf(stat.tone)])} title={stat.value}>{stat.value}</div>{stat.detail !== undefined && <p className={NATIVE.detail}>{stat.detail}</p>}{stat.spark && <NativeSpark spark={stat.spark} label={stat.label} />}</div>)}</div>;
      case "link": if (!isHttps(it.url)) return null; return <div className={NATIVE.row}><Button tone="quiet" onClick={() => openLink(it.url, it.title)}>{it.title || new URL(it.url).host}</Button></div>;
    }
    return undefined;
  };
}
