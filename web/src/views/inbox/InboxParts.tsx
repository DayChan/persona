// Small pieces the inbox pages share: avatars (chat, mail, document, Moments) with their source mark, tags, counts,
// the AI twin's draft card, a document row's status menu, and the few glyphs the shared icon set does not have.
import { useState, type CSSProperties, type ReactNode } from "react";
import {
  agentDraft, docChangeLine, isDone, isLark, itemKey, movingKey, setDocStatus, useInbox, withToken,
  type DocRow, type DocStatus, type InboxItem,
} from "../../lib/inbox";
import { connectorBrand } from "../../lib/connectors";
import { hubMediaURL } from "../../lib/lark-summary";
import { useItemManager, withManager } from "../../lib/manager-choice";
import { t } from "../../i18n";
import { Button, cx, Menu, Spinner, useMenu } from "../../ui/atoms";
import { BrandMark } from "../../ui/BrandMark";
import { Icon } from "../../ui/icons";
import { useClamped } from "../../ui/use-clamped";
import larkMark from "./lark.svg";

// ── glyphs not in ui/icons (Beautiful UI grammar: 24 grid, round caps, currentColor) ──

const GLYPHS = {
  sheet: <g><rect x="3" y="3" width="18" height="18" rx="2.5" /><path d="M3 9h18M3 15h18M9 3v18" /></g>,
  base: <g><rect x="3" y="3" width="7.5" height="7.5" rx="1.8" /><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.8" /><rect x="3" y="13.5" width="7.5" height="7.5" rx="1.8" /><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.8" /></g>,
  doc: <g><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><path d="M14 2v6h6M8 13h8M8 17h5" /></g>,
  moments: <g><circle cx="12" cy="12" r="9" /><circle cx="12" cy="12" r="3.2" /><path d="M12 3v5.8M20.2 15.6l-5.4-2.4M3.8 15.6l5.4-2.4" /></g>,
  hand: <g><path d="M18 11V6a2 2 0 0 0-4 0v5M14 10V4a2 2 0 0 0-4 0v6M10 10.5V6a2 2 0 0 0-4 0v8" /><path d="M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.9-6-2.3l-3.6-3.6a2 2 0 0 1 2.8-2.8L7 15" /></g>,
  question: <g><path d="M21 12a8 8 0 0 1-11.6 7.1L3 21l1.9-6.4A8 8 0 1 1 21 12z" /><path d="M9.6 9.6a2.4 2.4 0 0 1 4.7.8c0 1.6-2.3 2-2.3 3.4M12 16.3h.01" /></g>,
  undo: <g><path d="M9 14 4 9l5-5" /><path d="M4 9h10.5a5.5 5.5 0 0 1 0 11H11" /></g>,
  reply: <g><path d="M9 17 4 12l5-5" /><path d="M20 18v-2a4 4 0 0 0-4-4H4" /></g>,
};
export type GlyphName = keyof typeof GLYPHS;

export function Glyph({ name, size = 15, stroke = 1.9, className, style }: { name: GlyphName; size?: number; stroke?: number; className?: string; style?: CSSProperties }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={stroke} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className={className} style={style}>
      {GLYPHS[name]}
    </svg>
  );
}

/** ✦ before an AI line (the summary, the AI twin's note). */
export function Spark({ size = 11 }: { size?: number }) {
  return <Icon name="spark" size={size} className="mr-1 inline-block shrink-0 align-[-1px] text-accent" />;
}

// Feishu's own document colors: sheets green, Bases purple, docs blue. There is no purple token: a blue / red mix
// of the theme's own colors stands in.
const PURPLE = "color-mix(in srgb, var(--accent) 55%, var(--red))";
const tint = (c: string, pct = 16) => `color-mix(in srgb, ${c} ${pct}%, transparent)`;

function docTone(type?: string): CSSProperties {
  const c = type === "sheet" ? "var(--green)" : type === "base" ? PURPLE : "var(--accent)";
  return { color: c, background: tint(c) };
}

// Chat avatars without a picture: never red or orange, so nothing looks like "Needs you".
const MONO: CSSProperties[] = [
  { color: "var(--accent-ink)", background: "var(--accent-tint)" },
  { color: "var(--green)", background: "var(--green-tint)" },
  { color: PURPLE, background: tint(PURPLE) },
  { color: "var(--ink-2)", background: "var(--field)" },
];

function hash(s: string) {
  let h = 0;
  for (const ch of s) h = (h * 31 + (ch.codePointAt(0) ?? 0)) | 0;
  return Math.abs(h);
}

/** `manager`: whose item the picture is of (a list row's; inside an item's view, ItemManager tells), so its URL names
 *  that manager (docs/assistant/managers.md "Clients"). */
export function Monogram({ name, size = 40, square, neutral, src, manager }: { name: string; size?: number; square?: boolean; neutral?: boolean; src?: string; manager?: string }) {
  const owner = useItemManager();
  // The picture that failed to load: the letter instead of a broken image, until src changes.
  const [failed, setFailed] = useState<string>();
  const shape = square ? "rounded-[10px]" : "rounded-full";
  if (src && failed !== src) return <img src={withToken(withManager(src, manager ?? owner))} alt="" width={size} height={size} onError={() => setFailed(src)} className={cx("shrink-0 object-cover shadow-hairline", shape)} style={{ width: size, height: size }} />;
  const first = [...name.trim()][0] ?? "?";
  return (
    <span aria-hidden className={cx("flex shrink-0 items-center justify-center font-semibold", shape)} style={{ width: size, height: size, fontSize: Math.round(size * 0.4), ...(neutral ? MONO[3] : MONO[hash(name) % MONO.length]) }}>
      {first.toUpperCase()}
    </span>
  );
}

/** The 40px avatar of a row: chat / sender, or a tinted square for a document or a Moments post; a small mark of
 * where it came from in the corner (Feishu's own, mail a neutral gray). */
export function ItemAvatar({ it, size = 40 }: { it: InboxItem; size?: number }) {
  const icon = Math.round(size * 0.45);
  let face: ReactNode;
  if (it.doc) {
    face = (
      <span className="flex shrink-0 items-center justify-center rounded-[10px]" style={{ width: size, height: size, ...docTone(it.doc.type) }}>
        <Glyph name={it.doc.type === "sheet" ? "sheet" : it.doc.type === "base" ? "base" : "doc"} size={icon} stroke={2} />
      </span>
    );
  } else if (it.moment) {
    face = (
      <span className="flex shrink-0 items-center justify-center rounded-[10px] bg-orange-tint text-orange" style={{ width: size, height: size }}>
        <Glyph name="moments" size={icon} stroke={2} />
      </span>
    );
  } else if (isLark(it)) {
    face = <Monogram name={it.title} size={size} square={it.lark?.is_group} src={hubMediaURL(it.avatar)} manager={it.manager} />;
  } else {
    face = <Monogram name={it.sender || it.title} size={size} neutral />;
  }
  const badge = Math.max(14, Math.round(size * 0.42));
  // A connector's own mark on white in the corner; one without a mark a neutral gray, like mail.
  if (it.connector) {
    const brand = connectorBrand(it.source);
    return (
      <span className="relative shrink-0" style={{ width: size, height: size, opacity: isDone(it) ? 0.6 : 1 }}>
        {face}
        <span
          className={cx("absolute flex items-center justify-center rounded-full shadow-[0_0_0_2px_var(--surface)]", brand ? "bg-surface" : "bg-ink-3 text-surface")}
          style={{ width: badge, height: badge, right: -3, bottom: -3 }}
          title={it.account_name || it.source}
        >
          {brand ? <BrandMark brand={brand} size={badge} fit="badge" /> : <Icon name="link" size={Math.round(badge * 0.62)} stroke={2.4} />}
        </span>
      </span>
    );
  }
  return (
    <span className="relative shrink-0" style={{ width: size, height: size, opacity: isDone(it) ? 0.6 : 1 }}>
      {face}
      <span
        className={cx("absolute flex items-center justify-center rounded-full shadow-[0_0_0_2px_var(--surface)]", isLark(it) ? "bg-surface" : "bg-ink-3 text-surface")}
        style={{ width: badge, height: badge, right: -3, bottom: -3 }}
        title={isLark(it) ? t("inbox-inboxparts.source_lark") : t("inbox-inboxparts.source_mail")}
      >
        {isLark(it) ? <img src={larkMark} alt="" className="size-[68%]" /> : <Icon name="mail" size={Math.round(badge * 0.62)} stroke={2.4} />}
      </span>
    </span>
  );
}

export function Tag({ children, urgent }: { children: ReactNode; urgent?: boolean }) {
  return (
    <span className={cx("inline-flex h-[18px] shrink-0 items-center rounded-chip px-1.5 text-[10.5px] font-medium whitespace-nowrap", urgent ? "bg-field text-attention" : "bg-field text-ink-2")}>
      {children}
    </span>
  );
}

/** A message's action bar, over its top right corner: on hover with a mouse; with no hover, a ⋯ there that folds the
 *  buttons out and back. */
export function MessageBar({ className, children }: { className: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <div className={cx("absolute z-10 hidden items-center gap-0.5 rounded-[6px] border border-line bg-surface/95 px-1 py-0.5 shadow-hairline backdrop-blur-sm group-hover/msg:flex [@media(hover:none)]:flex", !open && "[@media(hover:none)]:px-0", className)}>
      <div className={cx("contents", !open && "[@media(hover:none)]:hidden")} onClick={() => setOpen(false)}>{children}</div>
      <button type="button" aria-label={t("chat-messageactions.more")} title={t("chat-messageactions.more")} aria-expanded={open} onClick={() => setOpen(!open)} className="touch-hit hidden size-5 items-center justify-center rounded text-ink-3 [@media(hover:none)]:flex">
        <Icon name="more" size={12} />
      </button>
    </div>
  );
}

/** A filter or choice that turns on and off: the inbox's sources and kinds, the "Messages" page's Feishu accounts. */
export function Chip({ on, onClick, children, title }: { on: boolean; onClick: () => void; children: ReactNode; title?: string }) {
  return (
    <button
      type="button"
      title={title}
      aria-pressed={on}
      onClick={onClick}
      className={cx("h-7 rounded-control px-2.5 text-[12px] whitespace-nowrap transition-colors duration-150", on ? "bg-ink font-medium text-surface" : "bg-surface text-ink-2 shadow-btn hover:bg-hover hover:text-ink")}
    >
      {children}
    </button>
  );
}

export function Count({ n, accent }: { n: number; accent?: boolean }) {
  return (
    <span className={cx("inline-flex h-[18px] min-w-[18px] shrink-0 items-center justify-center rounded-full px-1.5 text-[10.5px] font-semibold tabular-nums", accent ? "bg-accent text-white" : "bg-field text-ink-2")}>
      {n > 99 ? "99+" : n}
    </span>
  );
}

/** The AI twin's reply under a row: "Send" sends it as it is, "Edit" opens it in the draft dialog. */
export function DraftCard({ it, onEdit, onSend }: { it: InboxItem; onEdit: () => void; onSend: () => void }) {
  const draft = agentDraft(it);
  const sending = useInbox((s) => s.sending.includes(itemKey(it)));
  // the four lines may cut it: "Show all" shows the rest in place, so it is read before "Send" (#4742)
  const [open, setOpen] = useState(false);
  const { ref: textRef, cut: long } = useClamped<HTMLParagraphElement>(open);
  if (!draft) return null;
  return (
    <div className="rounded-[10px] bg-inset p-3 shadow-hairline">
      <p className="flex items-center text-[11.5px] font-medium text-accent-ink">
        <Spark />
        {t("inbox-inboxparts.draft_title")}
      </p>
      <p ref={textRef} className={cx("mt-1.5 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink", !open && "line-clamp-4")} data-draft-text>{draft}</p>
      {long && (
        <button
          type="button"
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            setOpen((o) => !o);
          }}
          aria-expanded={open}
          className="-ml-1 mt-0.5 rounded-[6px] px-1 text-[12px] font-medium whitespace-nowrap text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2"
        >
          {open ? t("inbox-inboxparts.draft_collapse") : t("inbox-inboxparts.draft_expand")}
        </button>
      )}
      {(it.agent?.changes ?? []).map((c) => (
        <p key={`${c.ref}|${c.field}`} className="mt-1 flex items-center gap-1.5 truncate text-[11.5px] text-ink-2">
          <Icon name="chevronRight" size={11} stroke={2.4} className="shrink-0 text-ink-3" />
          <span className="truncate">{docChangeLine(c, it.doc)}</span>
        </p>
      ))}
      <div className="mt-2.5 flex justify-end gap-2">
        <Button tone="secondary" onClick={onEdit}>{t("inbox-inboxparts.edit")}</Button>
        <Button tone="accent" icon="send" busy={sending} onClick={onSend} title={t("inbox-inboxparts.send_draft")}>{t("inbox-inboxparts.send")}</Button>
      </div>
    </div>
  );
}

export function AgentWorking({ it }: { it: InboxItem }) {
  const queued = it.agent?.state === "queued";
  return (
    <p className="flex min-w-0 items-center gap-2 text-[11.5px] text-ink-2">
      <Spinner size={11} />
      <span className="truncate">{queued ? t("inbox-inboxparts.agent_queued") : it.agent?.task ? t("inbox-inboxparts.agent_working_task", { task: it.agent.task }) : t("inbox-inboxparts.agent_working")}</span>
    </p>
  );
}

/** A document's option colors are its own (data, not theme): the dot takes the hue, the capsule a faint tint. */
function optionColor(status: DocStatus, value: string): string | undefined {
  const c = status.options.find((o) => o.name === value)?.color;
  return c && /^#[0-9a-f]{6}$/i.test(c) ? c : undefined;
}

/** A status column's value as a capsule; picking another value writes it into the document. */
export function StatusMenu({ it, row, status, showName }: { it: InboxItem; row: DocRow; status: DocStatus; showName?: boolean }) {
  const menu = useMenu();
  const moving = useInbox((s) => s.moving.includes(movingKey(it, row, status)));
  const color = optionColor(status, status.value);
  const value = status.value || t("inbox-inboxparts.status_unset");
  return (
    <>
      <button
        type="button"
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          menu.open(e);
        }}
        disabled={moving || isDone(it)}
        aria-label={t("inbox-inboxparts.status_change", { name: status.name, value })}
        className="inline-flex h-6 max-w-full shrink-0 items-center gap-1.5 rounded-full px-2 text-[11.5px] font-medium whitespace-nowrap text-ink transition-[filter] enabled:hover:brightness-95 disabled:opacity-60 dark:enabled:hover:brightness-110"
        style={{ background: color ? tint(color, 30) : "var(--field)" }}
      >
        {moving ? <Spinner size={9} /> : <span className="size-1.5 shrink-0 rounded-full" style={{ background: color ? `color-mix(in srgb, ${color} 75%, var(--ink))` : "var(--ink-3)" }} />}
        <span className="truncate">{showName ? t("inbox-inboxparts.status_value", { name: status.name, value }) : value}</span>
        <Icon name="chevron" size={11} stroke={2.4} className="shrink-0 text-ink-3" />
      </button>
      {menu.anchor && (
        <Menu
          anchor={menu.anchor}
          onClose={menu.close}
          header={status.name}
          width={180}
          items={status.options.map((o) => ({ key: o.name, label: o.name, checked: o.name === status.value, onSelect: () => void setDocStatus(it, row, status, o.name) }))}
        />
      )}
    </>
  );
}
