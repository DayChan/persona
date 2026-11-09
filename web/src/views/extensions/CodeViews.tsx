// UI version 4's code hosting components (docs/extensions/extensions.md "UI version 4", #2951), in the web's design system: a detail
// page's header, a breadcrumb, a file tree, a file's code, a file's diff (one column or side by side, inline threads, a
// comment box at any line, hidden lines to show: DiffReview.tsx), a discussion and a comment box with its buttons. What they send goes
// through `act` (ViewBody's onAct): the action with the values it carries and the component's own (a path, a line, the
// text typed). Logic in lib/code-view.ts; how a section holds them is ViewBody.tsx.
import { Fragment, useContext, useEffect, useMemo, useRef, useState } from "react";
import { NativeMediaContext } from "./native-media";
import type { Avatar as WireAvatar } from "../../lib/extension-wire";
import { ago } from "../../lib/format";
import { ancestors, buildTree, filterEntries, highlightLines, languageOf, statusMark, useHighlighter, type Comment, type Token, type TreeNode } from "../../lib/code-view";
import { findIn, isGenerated, lineLink, lineTyped, overlay, type Span } from "../../lib/code-review";
import { composing } from "../../lib/send-history";
import { useReview } from "./review-context";
import { initials, rowIcon, toneOf, type UIAction, type UIItem, type UITone } from "../../lib/extension-ui";
import { imageSrc } from "../../lib/extension-ui-v6";
import { t } from "../../i18n";
import { Button, cx, Spinner, toast } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { Pill } from "../settings/SettingsParts";
import { ROW_ICON, ViewMarkdown } from "./ViewBody";
import { nativeIcon } from "../../ui/ExtensionNative";
import { scopeLocal, useNativeState, type NativeLocal } from "./native-state";
import "./code-view.css";

/** What a code component needs from the page: to send an action with values, and which one is under way. */
export interface CodeHandlers {
  act: (a: UIAction, values?: Record<string, string>) => void;
  busy: string;
  /** Scroll to a file's diff on the page (a file tree without an action). */
  reveal?: (path: string) => void;
  local?: NativeLocal;
  canFetch?: () => boolean;
  fetch?: import("./review-context").ReviewApi["fetch"];
}

export const PILL: Record<UITone, "green" | "orange" | "red" | undefined> = { neutral: undefined, positive: "green", caution: "orange", danger: "red" };
export const MARK: Record<UITone, string> = { neutral: "text-ink-3", positive: "text-green", caution: "text-orange", danger: "text-red" };

export const card = "overflow-hidden rounded-card bg-surface shadow-card";
export const LH = 20; // a code line's height, px

export function Avatar({ avatar, name, size = 28 }: { avatar?: WireAvatar; name: string; size?: number }) {
  const [failed, setFailed] = useState(false);
  const media = useContext(NativeMediaContext);
  // UI version 6: the extension's own picture (a ref) through the hub's path, else an https url; a native resolver
  // (the owner's own media) gets a ref first when there is one.
  const url = avatar?.ref && media ? media(avatar) : imageSrc(avatar);
  return (
    <span
      className="flex shrink-0 items-center justify-center overflow-hidden rounded-full bg-accent-tint font-medium text-accent-ink"
      style={{ width: size, height: size, fontSize: Math.round(size * 0.42) }}
    >
      {url && !failed ? <img src={url} alt="" referrerPolicy="no-referrer" loading="lazy" onError={() => setFailed(true)} className="size-full object-cover" /> : initials(avatar?.text, name)}
    </span>
  );
}

const iconOf = (name?: string): IconName | undefined => {
  const icon = rowIcon(name);
  return icon ? ROW_ICON[icon] : undefined;
};

// ── header ────────────────────────────────────────────

export function Header({ item, native = false }: { item: UIItem; native?: boolean }) {
  return (
    <div className="flex gap-3 px-4 py-4 sm:px-5">
      {item.avatar && <Avatar avatar={item.avatar} name={item.title ?? ""} size={40} />}
      <div className="min-w-0 flex-1">
        <h2 className="text-[18px] leading-snug font-semibold break-words text-ink select-text sm:text-[20px]">{item.title}</h2>
        {item.subtitle && <p className="mt-0.5 truncate text-[12.5px] text-ink-3 select-text">{item.subtitle}</p>}
        {(item.badges?.length || item.facts?.length) && (
          <div className="mt-2.5 flex flex-wrap items-center gap-x-3.5 gap-y-1.5">
            {item.badges?.map((b, i) => (
              <Pill key={`b${i}`} tone={PILL[toneOf(b.tone)]}>
                {b.text}
              </Pill>
            ))}
            {item.facts?.map((f, i) => {
              const icon = native && f.icon ? nativeIcon(f.icon) : iconOf(f.icon);
              return (
                <span key={`f${i}`} className="flex min-w-0 items-center gap-1 text-[12.5px] whitespace-nowrap text-ink-2" title={f.label}>
                  {icon && <Icon name={icon} size={13} className="shrink-0 text-ink-3" />}
                  {f.label && !icon && <span className="text-ink-3">{f.label}</span>}
                  <span className="truncate font-medium text-ink select-text">{f.value}</span>
                </span>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

// ── breadcrumb ────────────────────────────────────────

export function Breadcrumb({ item, act, busy }: { item: UIItem } & CodeHandlers) {
  const crumbs = item.crumbs ?? [];
  return (
    <nav aria-label={t("extensions-codeviews.path")} className="flex min-w-0 items-center gap-0.5 overflow-x-auto px-3 py-2 text-[13px] whitespace-nowrap">
      {crumbs.map((c, i) => {
        const last = i === crumbs.length - 1;
        return (
          <Fragment key={i}>
            {i > 0 && <span className="px-0.5 text-ink-3">/</span>}
            {c.action && !last ? (
              <button
                type="button"
                disabled={!!busy}
                onClick={() => act(c.action!)}
                className="rounded-[6px] px-1 py-0.5 text-accent-ink transition-colors enabled:hover:bg-hover"
              >
                {c.title}
              </button>
            ) : (
              <span className={cx("px-1 py-0.5", last ? "font-semibold text-ink" : "text-ink-2")}>{c.title}</span>
            )}
          </Fragment>
        );
      })}
    </nav>
  );
}

// ── file tree ─────────────────────────────────────────

export function FileTree({ item, act, busy, reveal, onPicked, local }: { item: UIItem; onPicked?: () => void } & CodeHandlers) {
  const entries = item.entries ?? [];
  const review = useReview();
  const [q, setQ] = useNativeState(local, "filter", "");
  const shown = useMemo(() => filterEntries(entries, q), [entries, q]);
  const tree = useMemo(() => buildTree(shown), [shown]);
  const [closedPaths, setClosedPaths] = useNativeState<string[]>(local, "closed", []);
  const closed = new Set(closedPaths);
  const setClosed = (update: (paths: Set<string>) => Set<string>) => setClosedPaths((paths) => [...update(new Set(paths))]);
  const [asked, setAsked] = useNativeState(local, "asked", "");
  const selected = item.selected ?? "";
  // the selected file's directories stay open
  useEffect(() => {
    if (!selected) return;
    setClosed((c) => {
      const open = ancestors(selected).filter((a) => c.has(a));
      if (!open.length) return c;
      const next = new Set(c);
      open.forEach((a) => next.delete(a));
      return next;
    });
  }, [selected]);
  const files = entries.filter((e) => e.kind !== "dir").length;
  const toggle = (n: TreeNode) => {
    if (n.unloaded) {
      if (item.expand && !busy) {
        setAsked(n.path);
        act(item.expand, { path: n.path });
      }
      return;
    }
    setClosed((c) => {
      const next = new Set(c);
      if (next.has(n.path)) next.delete(n.path);
      else next.add(n.path);
      return next;
    });
  };
  const open = (path: string) => {
    if (item.action) act(item.action, { path });
    else reveal?.(path);
    onPicked?.();
  };
  const rows: React.ReactNode[] = [];
  const walk = (nodes: TreeNode[], depth: number) => {
    for (const n of nodes) {
      const pad = { paddingLeft: 10 + depth * 14 };
      if (n.dir) {
        const isOpen = !n.unloaded && (!closed.has(n.path) || !!q);
        rows.push(
          <button
            key={`d:${n.path}`}
            type="button"
            onClick={() => toggle(n)}
            aria-expanded={n.unloaded ? false : isOpen}
            className="flex h-7 w-full items-center gap-1.5 pr-2.5 text-left text-[12.5px] whitespace-nowrap text-ink-2 transition-colors hover:bg-hover"
            style={pad}
          >
            {busy && asked === n.path ? (
              <Spinner size={11} />
            ) : (
              <Icon name="chevronRight" size={11} stroke={2.2} className={cx("shrink-0 text-ink-3 transition-transform duration-150", isOpen && "rotate-90")} />
            )}
            <Icon name="folder" size={13} className="shrink-0 text-ink-3" />
            <span className="min-w-0 flex-1 truncate">{n.name}</span>
          </button>,
        );
        if (isOpen) walk(n.children, depth + 1);
        continue;
      }
      const e = n.entry!;
      const mark = statusMark(e.status);
      const on = n.path === selected;
      const seen = review.viewed(n.path) || !!e.viewed;
      const generated = isGenerated(n.path, e.generated);
      rows.push(
        <button
          key={`f:${n.path}`}
          type="button"
          onClick={() => open(n.path)}
          aria-current={on ? "true" : undefined}
          title={n.path}
          className={cx("flex h-7 w-full items-center gap-1.5 pr-2.5 text-left text-[12.5px] whitespace-nowrap transition-colors", on ? "bg-accent-tint text-ink" : "text-ink hover:bg-hover")}
          style={{ paddingLeft: 10 + depth * 14 + 17 }}
        >
          {seen ? (
            <span className="flex size-[13px] shrink-0 items-center justify-center rounded-[4px] bg-green-tint text-green" aria-label={t("extensions-review.viewed")}>
              <Icon name="check" size={9} stroke={3} />
            </span>
          ) : (
            <Icon name="read" size={13} className="shrink-0 text-ink-3" />
          )}
          <span className={cx("min-w-0 flex-1 truncate", (e.status === "deleted" || seen || generated) && "text-ink-3", e.status === "deleted" && "line-through")}>{n.name}</span>
          {!!e.comments && (
            <span className="flex shrink-0 items-center gap-0.5 text-[11px] text-ink-3 tabular-nums">
              <Icon name="chat" size={11} />
              {e.comments}
            </span>
          )}
          {(!!e.additions || !!e.deletions) && (
            <span className="shrink-0 font-mono text-[10.5px] tabular-nums">
              {!!e.additions && <span className="text-green">+{e.additions}</span>}
              {!!e.deletions && <span className="ml-1 text-red">−{e.deletions}</span>}
            </span>
          )}
          {e.badge?.text && <Pill tone={PILL[toneOf(e.badge.tone as UITone)]}>{e.badge.text}</Pill>}
          {mark && <span className={cx("w-3 shrink-0 text-center font-mono text-[10.5px] font-semibold", MARK[mark.tone])} aria-label={e.status}>{mark.letter}</span>}
        </button>,
      );
    }
  };
  walk(tree, 0);
  return (
    <div className="py-1.5">
      {files > 12 && (
        <div className="px-2 pb-1.5">
          <label className="relative block">
            <Icon name="filter" size={12} className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-ink-3" />
            <input
              type="search"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              data-file-filter=""
              onKeyDown={(e) => e.key === "Escape" && (e.currentTarget.blur(), setQ(""))}
              placeholder={t("extensions-codeviews.filter_files")}
              aria-label={t("extensions-codeviews.filter_files")}
              spellCheck={false}
              className="h-7 w-full rounded-control bg-field pr-2 pl-7 text-[12px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]"
            />
          </label>
        </div>
      )}
      <div role="tree" aria-label={t("extensions-codeviews.files")}>
        {rows}
      </div>
      {!rows.length && <p className="px-3 py-3 text-center text-[12px] text-ink-3">{q ? t("extensions-codeviews.no_match") : t("extensions-codeviews.no_files")}</p>}
    </div>
  );
}

// ── code ──────────────────────────────────────────────

function Tokens({ tokens, text }: { tokens?: Token[] | null; text: string }) {
  if (!tokens) return <>{text || " "}</>;
  if (!tokens.length) return <> </>;
  return (
    <>
      {tokens.map((tk, i) =>
        tk.cls ? (
          <span key={i} className={tk.cls}>
            {tk.text}
          </span>
        ) : (
          <Fragment key={i}>{tk.text}</Fragment>
        ),
      )}
    </>
  );
}

function copy(text: string) {
  void navigator.clipboard?.writeText(text).then(
    () => toast(t("extensions-codeviews.copied"), "ok"),
    () => {},
  );
}

const WRAP_KEY = "persona.code.wrap";

/** The text with the spans found marked (the one gone to stronger), over its highlighting. */
function Found({ tokens, text, spans, current }: { tokens?: Token[] | null; text: string; spans?: Span[]; current: boolean }) {
  if (!spans?.length) return <Tokens tokens={tokens} text={text} />;
  return (
    <>
      {overlay(tokens, text, spans).map((p, i) =>
        p.changed ? (
          <mark key={i} className={cx("rounded-[2px] text-ink", current ? "bg-orange/60" : "bg-orange/25", p.cls)}>
            {p.text}
          </mark>
        ) : (
          <span key={i} className={p.cls || undefined}>
            {p.text}
          </span>
        ),
      )}
    </>
  );
}

/**
 * A file: line numbers, highlighting, the highlighted lines, and the rest to load when it was cut. To read it: find
 * in it (⌘/Ctrl+F while in it, Enter for the next), go to a line, long lines wrapped or not (kept in this browser), and
 * lines picked on their numbers (shift for a range) to copy a link to them on the site (line_url, UI version 5).
 */
export function CodeView({ item, act, busy, local }: { item: UIItem } & CodeHandlers) {
  const text = item.text ?? "";
  const lines = useMemo(() => text.replace(/\n$/, "").split("\n"), [text]);
  const lang = languageOf(item.language, item.path);
  const ready = useHighlighter(!!lang);
  const tokens = useMemo(() => (ready ? highlightLines(text, lang) : null), [ready, text, lang]);
  const start = item.start_line ?? 1;
  const last = start + lines.length - 1;
  const digits = String(start + lines.length).length;
  const hl = item.highlight;
  const marked = useRef<HTMLDivElement>(null);
  const rowsRef = useRef<HTMLDivElement>(null);
  const [wrap, setWrap] = useNativeState(local, "wrap", () => {
    try {
      return localStorage.getItem(WRAP_KEY) === "1";
    } catch {
      return false;
    }
  });
  const [tool, setTool] = useNativeState<"" | "find" | "goto">(local, "tool", "");
  const [q, setQ] = useNativeState(local, "query", "");
  const [at, setAt] = useNativeState(local, "at", 0);
  const [pick, setPick] = useNativeState<{ start: number; end: number } | null>(local, "pick", null);
  const anchor = useRef<number | null>(null);
  const found = useMemo(() => (tool === "find" ? findIn(lines, q) : []), [tool, lines, q]);
  const spansAt = useMemo(() => new Map(found.map((f) => [f.line, f.spans])), [found]);
  const link = pick ? lineLink(item.line_url, pick.start, pick.end) : undefined;
  useEffect(() => {
    marked.current?.scrollIntoView?.({ block: "nearest" });
  }, [hl?.start, item.path]);
  const show = (i: number) => rowsRef.current?.children[i]?.scrollIntoView?.({ block: "center" });
  useEffect(() => {
    if (found.length) show(found[Math.min(at, found.length - 1)].line);
  }, [found, at]);
  const toggleWrap = () => {
    setWrap((w) => {
      try {
        localStorage.setItem(WRAP_KEY, w ? "0" : "1");
      } catch {
        // private mode
      }
      return !w;
    });
  };
  const pickLine = (n: number, shift: boolean) => {
    if (shift && anchor.current !== null) setPick({ start: Math.min(anchor.current, n), end: Math.max(anchor.current, n) });
    else {
      anchor.current = n;
      setPick((p) => (p && p.start === n && p.end === n ? null : { start: n, end: n }));
    }
  };
  const tbtn = "flex size-7 shrink-0 items-center justify-center rounded-[8px] transition-colors hover:bg-hover hover:text-ink";
  return (
    <div
      className={card}
      onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "f") {
          e.preventDefault();
          setTool("find");
        }
      }}
    >
      <div className="flex h-10 items-center gap-1 border-b border-line pr-2 pl-3.5">
        <Icon name="read" size={13} className="mr-1 shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink select-text">{item.path || t("extensions-codeviews.file")}</span>
        <span className="mr-1 hidden shrink-0 text-[11.5px] text-ink-3 tabular-nums sm:inline">{t("extensions-codeviews.lines", { count: lines.length })}</span>
        {link && (
          <button type="button" onClick={() => copy(link)} title={t("extensions-review.copy_link")} className={cx(tbtn, "w-auto gap-1 px-1.5 text-[11.5px] whitespace-nowrap text-accent-ink")}>
            <Icon name="link" size={13} />
            <span className="hidden sm:inline">{t("extensions-review.copy_link")}</span>
          </button>
        )}
        <button type="button" onClick={() => setTool((x) => (x === "find" ? "" : "find"))} aria-pressed={tool === "find"} aria-label={t("extensions-review.find")} title={`${t("extensions-review.find")} (⌘F)`} className={cx(tbtn, tool === "find" ? "text-accent-ink" : "text-ink-3")}>
          <Icon name="search" size={13} />
        </button>
        <button type="button" onClick={() => setTool((x) => (x === "goto" ? "" : "goto"))} aria-pressed={tool === "goto"} aria-label={t("extensions-review.go_to_line")} title={t("extensions-review.go_to_line")} className={cx(tbtn, tool === "goto" ? "text-accent-ink" : "text-ink-3")}>
          <Icon name="enter" size={13} />
        </button>
        <button type="button" onClick={toggleWrap} aria-pressed={wrap} aria-label={t("extensions-review.wrap")} title={t("extensions-review.wrap")} className={cx(tbtn, wrap ? "text-accent-ink" : "text-ink-3")}>
          <Icon name="wrap" size={13} />
        </button>
        <button type="button" onClick={() => copy(text)} aria-label={t("extensions-codeviews.copy")} title={t("extensions-codeviews.copy")} className={cx(tbtn, "text-ink-3")}>
          <Icon name="copy" size={13} />
        </button>
      </div>
      {tool && (
        <div className="flex h-10 items-center gap-2 border-b border-line px-3">
          <input
            autoFocus
            key={tool}
            type={tool === "goto" ? "number" : "search"}
            value={q}
            onChange={(e) => (setQ(e.target.value), setAt(0))}
            onKeyDown={(e) => {
              if (composing(e.nativeEvent)) return;
              if (e.key === "Escape") (setTool(""), setQ(""));
              if (e.key !== "Enter") return;
              e.preventDefault();
              if (tool === "goto") {
                const n = lineTyped(q, start, last);
                if (n !== null) (show(n - start), setPick({ start: n, end: n }), (anchor.current = n));
              } else if (found.length) setAt((a) => (a + (e.shiftKey ? found.length - 1 : 1)) % found.length);
            }}
            placeholder={tool === "goto" ? t("extensions-review.line_of", { first: start, last }) : t("extensions-review.find_in_file")}
            aria-label={tool === "goto" ? t("extensions-review.go_to_line") : t("extensions-review.find_in_file")}
            className="h-7 min-w-0 flex-1 rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]"
          />
          {tool === "find" && q && (
            <>
              <span className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{found.length ? `${Math.min(at, found.length - 1) + 1} / ${found.length}` : t("extensions-review.none_found")}</span>
              <button type="button" disabled={!found.length} onClick={() => setAt((a) => (a + found.length - 1) % found.length)} aria-label={t("extensions-review.prev_found")} className={cx(tbtn, "text-ink-3")}>
                <Icon name="up" size={12} stroke={2.2} />
              </button>
              <button type="button" disabled={!found.length} onClick={() => setAt((a) => (a + 1) % found.length)} aria-label={t("extensions-review.next_found")} className={cx(tbtn, "text-ink-3")}>
                <Icon name="arrowDown" size={12} stroke={2.2} />
              </button>
            </>
          )}
        </div>
      )}
      <div className={cx("code-hl bg-inset font-mono text-[12px]", !wrap && "overflow-x-auto")} style={{ lineHeight: `${LH}px` }}>
        <div ref={rowsRef} className={cx("py-1.5", !wrap && "min-w-max")}>
          {lines.map((l, i) => {
            const n = start + i;
            const on = (!!hl && n >= hl.start && n <= hl.end) || (!!pick && n >= pick.start && n <= pick.end);
            const cur = found.length > 0 && found[Math.min(at, found.length - 1)].line === i;
            return (
              <div key={i} ref={hl && n === hl.start ? marked : undefined} className={cx("flex", on && "bg-accent-tint")}>
                <button
                  type="button"
                  onClick={(e) => pickLine(n, e.shiftKey)}
                  aria-label={t("extensions-review.pick_line", { n })}
                  className={cx("sticky left-0 shrink-0 cursor-pointer self-start pr-3 pl-3 text-right tabular-nums select-none hover:text-accent-ink", on ? "text-accent-ink" : "bg-inset text-ink-3")}
                  style={{ width: `calc(${digits}ch + 24px)`, background: on ? "transparent" : undefined }}
                >
                  {n}
                </button>
                <span className={cx("pr-6 text-ink", wrap ? "min-w-0 flex-1 break-all whitespace-pre-wrap" : "whitespace-pre")}>
                  <Found tokens={tokens?.[i]} text={l} spans={spansAt.get(i)} current={cur} />
                </span>
              </div>
            );
          })}
        </div>
      </div>
      {item.truncated && item.load && (
        <LoadLine busy={busy === item.load.id} disabled={!!busy} onClick={() => act(item.load!, { path: item.path ?? "", from: String(start + lines.length) })} title={t("extensions-codeviews.load_rest")} />
      )}
    </div>
  );
}

export function LoadLine({ busy, disabled, onClick, title }: { busy: boolean; disabled: boolean; onClick: () => void; title: string }) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className="flex w-full items-center justify-center gap-2 border-t border-line px-4 py-2.5 text-[12.5px] whitespace-nowrap text-accent-ink transition-colors enabled:hover:bg-hover disabled:text-ink-3"
    >
      {busy && <Spinner />}
      {title}
    </button>
  );
}

// ── comments ──────────────────────────────────────────

export function CommentLine({ c }: { c: Comment }) {
  if (c.event)
    return (
      <div className="flex items-center gap-2 px-4 py-2 text-[12px] text-ink-3">
        <span className="flex size-6 shrink-0 items-center justify-center">
          <span className="size-1.5 rounded-full bg-line-strong" />
        </span>
        <span className="min-w-0 flex-1 break-words">
          <span className="font-medium text-ink-2">{c.author}</span> {c.body}
        </span>
        {c.at && <time dateTime={c.at} className="shrink-0 tabular-nums">{ago(c.at)}</time>}
      </div>
    );
  return (
    <div className="flex gap-2.5 px-4 py-2.5">
      <Avatar avatar={c.avatar} name={c.author} size={26} />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2 whitespace-nowrap">
          <span className="truncate text-[12.5px] font-semibold text-ink">{c.author}</span>
          {c.at && <time dateTime={c.at} title={new Date(c.at).toLocaleString()} className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{ago(c.at)}</time>}
          {c.badge?.text && <Pill tone={PILL[toneOf(c.badge.tone as UITone)]}>{c.badge.text}</Pill>}
        </div>
        {c.body && (
          <div className="-mx-4 -mb-2">
            <ViewMarkdown text={c.body} compact />
          </div>
        )}
      </div>
    </div>
  );
}

/** A text box that grows with what is typed; ⌘/Ctrl+Enter presses the first button. */
export function Box({ value, onChange, placeholder, autoFocus, onSubmit, disabled }: { value: string; onChange: (v: string) => void; placeholder?: string; autoFocus?: boolean; onSubmit?: () => void; disabled?: boolean }) {
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(320, Math.max(72, el.scrollHeight))}px`;
  }, [value]);
  return (
    <textarea
      ref={ref}
      value={value}
      autoFocus={autoFocus}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && onSubmit && !composing(e.nativeEvent)) {
          e.preventDefault();
          onSubmit();
        }
      }}
      placeholder={placeholder}
      aria-label={placeholder}
      className="block w-full resize-none rounded-control bg-field px-3 py-2 text-[13px] leading-relaxed text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60"
    />
  );
}

/** A small comment box: text and one button (a reply, an inline comment), with cancel when it can close. */
function MiniComposer({ placeholder, send, busy, disabled, onCancel, autoFocus, local }: { placeholder: string; send: (body: string) => void; busy: boolean; disabled: boolean; onCancel?: () => void; autoFocus?: boolean; local?: NativeLocal }) {
  const [body, setBody] = useNativeState(local, "body", "");
  const go = () => {
    if (!body.trim() || disabled) return;
    send(body.trim());
    if (!local) setBody("");
  };
  return (
    <div className="flex flex-col gap-2">
      <Box value={body} onChange={setBody} placeholder={placeholder} autoFocus={autoFocus} onSubmit={go} disabled={disabled && !busy} />
      <div className="flex items-center justify-end gap-2">
        {onCancel && (
          <Button tone="quiet" onClick={onCancel}>
            {t("extensions-codeviews.cancel")}
          </Button>
        )}
        <Button tone="primary" busy={busy} disabled={!body.trim() || disabled} onClick={go}>
          {t("extensions-codeviews.comment")}
        </Button>
      </div>
    </div>
  );
}

/** A discussion: its comments in order, and a reply box. */
export function CommentThread({ item, act, busy, local }: { item: UIItem } & CodeHandlers) {
  const comments = item.comments ?? [];
  const [replying, setReplying] = useNativeState(local, "replying", false);
  return (
    <div className={card}>
      {item.title && (
        <div className="flex items-center gap-2 border-b border-line px-4 py-2.5">
          <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink-2">{item.title}</span>
          {item.resolved && <Pill tone="green">{t("extensions-codeviews.resolved")}</Pill>}
        </div>
      )}
      <div className="divide-y divide-line">
        {comments.map((c, i) => (
          <CommentLine key={c.id ?? i} c={c} />
        ))}
      </div>
      {!comments.length && <p className="px-4 py-4 text-center text-[12.5px] text-ink-3">{t("extensions-codeviews.no_comments")}</p>}
      {item.reply && (
        <div className="border-t border-line px-4 py-3">
          {replying ? (
            <MiniComposer
              local={scopeLocal(local, "reply")}
              autoFocus
              placeholder={t("extensions-codeviews.reply")}
              busy={busy === item.reply.id}
              disabled={!!busy}
              onCancel={() => setReplying(false)}
              send={(body) => {
                act(item.reply!, { body });
                if (!local) setReplying(false);
              }}
            />
          ) : (
            <button type="button" onClick={() => setReplying(true)} className="h-8 w-full rounded-control bg-field px-3 text-left text-[12.5px] text-ink-3 shadow-inset-field transition-colors hover:text-ink-2">
              {t("extensions-codeviews.reply")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

const BUTTON_TONE: Record<UITone, "primary" | "secondary" | "danger" | "accent"> = { neutral: "secondary", positive: "accent", caution: "secondary", danger: "danger" };

/** A comment box with its buttons: the one pressed sends the text as the composer's name. */
export function Composer({ item, act, busy, draft }: { item: UIItem; draft?: { value: string; setValue: (value: string) => void } } & CodeHandlers) {
  const [localBody, setLocalBody] = useState(item.value ?? "");
  useEffect(() => setLocalBody(item.value ?? ""), [item.value]);
  // HTTP/assistant behavior stays unchanged; a stream owns drafts outside React run/section lifetimes.
  const body = draft?.value ?? localBody;
  const setBody = draft?.setValue ?? setLocalBody;
  const buttons = item.buttons ?? [];
  const press = (a: UIAction) => act(a, { [item.name ?? "body"]: body });
  return (
    <div className="flex flex-col gap-2.5 px-4 py-3">
      <Box value={body} onChange={setBody} placeholder={item.placeholder || t("extensions-codeviews.leave_comment")} onSubmit={() => buttons[0] && press(buttons[0].action)} disabled={!!busy} />
      <div className="flex flex-wrap items-center justify-end gap-2">
        <span className="mr-auto hidden text-[11.5px] text-ink-3 sm:inline">{t("extensions-codeviews.markdown_hint")}</span>
        {buttons.map((b, i) => (
          <Button key={i} tone={i === 0 && toneOf(b.tone) === "neutral" ? "primary" : BUTTON_TONE[toneOf(b.tone)]} busy={busy === b.action.id} disabled={!!busy && busy !== b.action.id} onClick={() => press(b.action)} title={b.title}>
            {b.title}
          </Button>
        ))}
      </div>
    </div>
  );
}
