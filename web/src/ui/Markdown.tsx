import { cloneElement, isValidElement, memo, startTransition, useEffect, useMemo, useRef, useState, type ReactElement } from "react";
import ReactMarkdown, { defaultUrlTransform, type Components } from "react-markdown";
import remarkGfm from "./remark-gfm-parse";
import remarkCjkFriendly from "remark-cjk-friendly/parseOnly";
import type { PluggableList } from "unified";
import { clickLink, linkHref, linkRoute, prefetchLink } from "../lib/links";
import { fileDownloadURL } from "../lib/api";
import { isMachineRef, resolveRef } from "../lib/files";
import { useLinkManager } from "../lib/manager-choice";
import { openPanel } from "../lib/panel";
import remarkMath from "remark-math";
import { copyText, toast } from "./atoms";
import { tableHtml, tableToTsv, writeTableClipboard } from "./tableCopy";
import { t } from "../i18n";
import { Icon } from "./icons";
import { MermaidBlock } from "./Mermaid";
import { AssistantCard, CARD_LANGUAGES } from "./AssistantCard";
import { rehypePathLinks, textPaths, writtenText } from "./PathLinks";
import { usePathLinks } from "../lib/path-presence";
import { StreamChunk } from "./stream/Chunk";
import { splitChunks } from "./stream/chunks";
import { codeHighlight, hasLanguageFence, HighlightWanted, useAskHighlight, useBlockHighlight, useHighlight } from "./stream/highlight";
import { useMath } from "./stream/math";
import { useReplyMotion } from "./stream/motion";
import { repairStreamingMarkdown } from "./stream/repair";
import { remarkSoftBreaks, replyBreaks } from "./reply-breaks";
import { useThrottledText } from "./stream/throttle";
import { whenIdle } from "./stream/idle";
import { createWordFadeState, rememberShown, remainingMs, shownBefore, type WordFadeState } from "./stream/wordFade";
import { cachedMarkdown, markdownCache, markdownKey } from "./markdown-cache";
import "./stream/stream.css";

// A link ends at whitespace or any non-ASCII character (Chinese text, full-width punctuation), without trailing
// .,;:!? or an unbalanced ) ] (docs/lessons/ios.md: where a link ends). GFM's autolinks only end at whitespace, so
// bare URLs are written as <url> before markdown sees them. Code is left alone.
const BARE = /(^|[^\x21-\x7e]|[\s(])(https?:\/\/[\x21-\x7e]+)/g;

export function trimURL(url: string): string {
  let u = url.replace(/[.,;:!?'"*_~]+$/, "");
  for (const [open, close] of [
    ["(", ")"],
    ["[", "]"],
  ]) {
    while (u.endsWith(close) && u.split(open).length < u.split(close).length) u = u.slice(0, -1).replace(/[.,;:!?]+$/, "");
  }
  return u;
}

// Fenced code (``` or ~~~, also inside lists and quotes and on a list item's first line; an unclosed one runs to the
// end of its quote or list item, or of the text while streaming), indented code (4 spaces after a blank line, outside
// lists) and code spans (a run of n backticks up to the next run of n). Returns text and code alternately, so odd
// indices are code: left alone by normalizeMath and linkify.
const QUOTES = /^(?:[ \t]*>[ \t]?)*/;
const FENCE = /^((?:[ \t]*>[ \t]?)*)([ \t]*(?:(?:[-*+]|\d{1,9}[.)])[ \t]+)?[ \t]*)(`{3,}|~{3,})/;
const LIST_ITEM = /^(?:[ \t]*>)*[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]/;
const INDENTED = /^( {4}|\t)/;

const quoteDepth = (line: string) => (QUOTES.exec(line)![0].match(/>/g) ?? []).length;

// A line without its first `depth` quote markers.
function unquote(line: string, depth: number): string {
  for (let k = 0; k < depth; k++) {
    const m = /^[ \t]*>[ \t]?/.exec(line);
    if (!m) break;
    line = line.slice(m[0].length);
  }
  return line;
}

export function splitCode(md: string): string[] {
  const parts = [""];
  const add = (s: string, code: boolean) => {
    if (parts.length % 2 === 0 === code) parts[parts.length - 1] += s;
    else parts.push(s);
  };
  let text = "";
  const flush = () => {
    for (let i = 0; i < text.length; ) {
      const j = text.indexOf("`", i);
      if (j !== i) {
        const end = j < 0 ? text.length : j;
        add(text.slice(i, end), false);
        i = end;
        continue;
      }
      const n = /^`+/.exec(text.slice(i))![0].length;
      const close = new RegExp(`(?<!\`)\`{${n}}(?!\`)`, "g");
      close.lastIndex = i + n;
      const m = close.exec(text);
      add(text.slice(i, m ? m.index + n : i + n), !!m);
      i = m ? m.index + n : i + n;
    }
    text = "";
  };
  const lines = md.split(/(?<=\n)/);
  let blank = true;
  let inList = false;
  for (let i = 0; i < lines.length; ) {
    const line = lines[i];
    if (line.trim() && !/^[ \t]/.test(line)) inList = LIST_ITEM.test(line) || (inList && !blank);
    const fence = FENCE.exec(line);
    if (fence && !(fence[3][0] === "`" && line.slice(fence[0].length).includes("`"))) {
      // The fence ends at a closing fence, or where its container does: a line with fewer quote markers, or (in a
      // list item) a line indented less than the item's content.
      const depth = quoteDepth(fence[1]);
      const indent = inList || LIST_ITEM.test(line) ? fence[2].length : 0;
      const end = new RegExp(`^[ \\t]*\\${fence[3][0]}{${fence[3].length},}[ \\t]*$`);
      let j = i + 1;
      let closed = false;
      for (; j < lines.length; j++) {
        const l = lines[j].replace(/\n$/, "");
        if (quoteDepth(l) < depth) break;
        const rest = unquote(l, depth);
        if (indent && rest.trim() && /^[ \t]*/.exec(rest)![0].length < indent) break;
        if (end.test(rest)) {
          closed = true;
          break;
        }
      }
      const stop = closed ? j + 1 : j;
      flush();
      add(lines.slice(i, stop).join(""), true);
      i = stop;
      blank = false;
      continue;
    }
    // A whitespace-only line is blank, not code: every block of indented code starts with a line that has text.
    if (blank && !inList && INDENTED.test(line) && line.trim()) {
      let j = i;
      while (j < lines.length && (INDENTED.test(lines[j]) || !lines[j].trim())) j++;
      while (j > i && !lines[j - 1].trim()) j--;
      flush();
      add(lines.slice(i, j).join(""), true);
      i = j;
      continue;
    }
    blank = !line.trim();
    text += line;
    i++;
  }
  flush();
  return parts;
}

// What starts a line inside a quote or list item, and what a new line needs to stay in it: quote markers are kept,
// a list marker becomes spaces.
const PREFIX = /^(?:[ \t]*>[ \t]?)*[ \t]*(?:(?:[-*+]|\d{1,9}[.)])[ \t]+)?/;
const continuation = (prefix: string) => prefix.replace(/[-*+]|\d{1,9}[.)]/, (m) => " ".repeat(m.length));

// Bare URLs and <autolinks>: a $ in them (https://…/odata/$metadata) is part of the address, not math.
const URL_TEXT = /<https?:\/\/[^\s<>]*>|(^|[^\x21-\x7e]|[\s(])(https?:\/\/[\x21-\x7e]+)/g;
// What normalizeMath holds out of the $ rules while it works: formulas written with \( \) / \[ \] (explicit, so "the
// \(n\)th term" stays math) and URLs with a $.
const HELD = /\uE000(\d+)\uE001/g;
// Stands in for code and held URLs when looking for table rows and their math.
const MASK = "\uE002";

// remark-math only knows $…$ / $$…$$, and CommonMark turns \( \) \[ \] into plain brackets, so the LaTeX
// delimiters models often use are rewritten to dollars first. remark-math renders $$…$$ on one line as inline
// math, so a line holding only that is split into a display block. Code is left alone.
export function normalizeMath(md: string): string {
  let done = "";
  let mask = "";
  const held: { s: string; url?: boolean }[] = [];
  const hold = (s: string, url?: boolean) => `\uE000${held.push({ s, url }) - 1}\uE001`;
  const restore = (s: string, masked = false): string =>
    s.replace(HELD, (_, n: string) => {
      const h = held[+n];
      return masked && h.url ? MASK.repeat(h.s.length) : restore(h.s, masked);
    });
  splitCode(md).forEach((part, i) => {
    if (i % 2) {
      done += part;
      mask += part.replace(/[^\n]/g, MASK);
      return;
    }
    const midLine = done !== "" && !done.endsWith("\n");
    part = part
      .replace(URL_TEXT, (m, pre: string | undefined, url: string | undefined) => {
        if (url === undefined) return m.includes("$") ? hold(m, true) : m;
        const u = trimURL(url).replace(/[$}]+$/, "");
        return u.includes("$") ? `${pre}${hold(u, true)}${url.slice(u.length)}` : m;
      })
      .replace(
        /^((?:[ \t]*>[ \t]?)*[ \t]*(?:(?:[-*+]|\d{1,9}[.)])[ \t]+)?)\$\$([^$\n]+)\$\$[ \t]*$/gm,
        (m, pre: string, tex: string, off: number) =>
          off === 0 && midLine ? m : `${pre}$$\n${continuation(pre)}${tex.trim()}\n${continuation(pre)}$$`,
      )
      .replace(/(?<!\\)\\\[([\s\S]+?)\\\]/g, (m, tex: string, off: number, all: string) => {
        const before = done + all.slice(0, off);
        const line = before.slice(before.lastIndexOf("\n") + 1);
        const pre = PREFIX.exec(line)![0];
        const c = continuation(pre);
        // Lines after the first carry the quote / list prefix already: each output line gets it exactly once.
        const depth = (pre.match(/>/g) ?? []).length;
        const body = tex
          .split("\n")
          .map((l) => unquote(l, depth).trim())
          .filter(Boolean)
          .join(`\n${c}`);
        if (!body) return m;
        const head = pre.length === line.length ? "" : `\n${c}`;
        const next = all[off + m.length];
        const tail = next === undefined || next === "\n" ? "" : `\n${c}`;
        return hold(`${head}$$\n${c}${body}\n${c}$$${tail}`);
      })
      .replace(/(?<!\\)\\\(([\s\S]+?)\\\)/g, (m, tex: string) => (tex.trim() ? hold(`$${dollarsInMath(tex.trim())}$`) : m));
    const out = escapeLoneDollars(part);
    done += restore(out);
    mask += restore(out, true);
  });
  return pipesInTableMath(done, mask);
}

// remark-math ends $…$ at a \$ too, so KaTeX's \$ is written as a character code.
const dollarsInMath = (tex: string) => tex.replace(/\\([\s\S])/g, (m, c: string) => (c === "$" ? '{\\char"24}' : m));

// Pandoc's rule for single dollars, plus no letter or digit right outside: $…$ is math only when the opening $
// follows no letter/digit and is followed by a non-space, and the closing $ follows a non-space and is followed by no
// letter/digit. Any other $ is escaped, so "$5 and $10", "US$5" and "$HOME/$USER" stay text and an unclosed $ doesn't
// pair with the next formula's $.
const DISPLAY = /(\$\$[\s\S]*?\$\$)/g;
const ALNUM = /[A-Za-z0-9]/;

function escapeLoneDollars(md: string): string {
  return md
    .split(DISPLAY)
    .map((part, n) => {
      if (n % 2) return part;
      let out = "";
      for (let i = 0; i < part.length; ) {
        if (part[i] === "\\") {
          out += part.slice(i, i + 2);
          i += 2;
          continue;
        }
        if (part[i] !== "$") {
          out += part[i++];
          continue;
        }
        const close = closingDollar(part, i + 1);
        if (
          close > i + 1 &&
          /\S/.test(part[i + 1]) &&
          /\S/.test(part[close - 1]) &&
          !ALNUM.test(part[i - 1] ?? "") &&
          !ALNUM.test(part[close + 1] ?? "")
        ) {
          out += `$${dollarsInMath(part.slice(i + 1, close))}$`;
          i = close + 1;
        } else {
          out += "\\$";
          i++;
        }
      }
      return out;
    })
    .join("");
}

// The $ ending a formula that starts before `from`: on the same line, skipping \-escaped characters (LaTeX's \$).
function closingDollar(s: string, from: number): number {
  for (let i = from; i < s.length && s[i] !== "\n"; i++) {
    if (s[i] === "\\") i++;
    else if (s[i] === "$") return i;
  }
  return -1;
}

// GFM splits table cells on every |, even inside $…$, so | in math on a table row is written as \vert. A table is a
// line with a | followed by a delimiter row (outer pipes optional), and runs to a blank line; a line starting with |
// counts too (a table still streaming in). `mask` is `md` with code and URLs blanked out, so neither a code span
// splitting the row nor a $ in code or a URL changes what is math.
const DELIMITER_ROW = /^[ \t]*\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$/;

function pipesInTableMath(md: string, mask: string): string {
  const masked = mask.split("\n");
  const row = (k: number) => masked[k].replace(QUOTES, "");
  let table = false;
  return md
    .split("\n")
    .map((line, k) => {
      const r = row(k);
      if (!r.trim()) table = false;
      else if (!table && r.includes("|") && k + 1 < masked.length && row(k + 1).includes("|") && DELIMITER_ROW.test(row(k + 1)))
        table = true;
      return table || /^[ \t]*\|/.test(r) ? pipesInMath(line, masked[k]) : line;
    })
    .join("\n");
}

function pipesInMath(line: string, mask: string): string {
  let out = "";
  for (let i = 0; i < line.length; ) {
    if (mask[i] === "\\") {
      out += line.slice(i, i + 2);
      i += 2;
      continue;
    }
    const close = mask[i] === "$" ? closingDollar(mask, i + 1) : -1;
    if (close < 0) {
      out += line[i++];
      continue;
    }
    out += `$${line.slice(i + 1, close).replace(/\\\|/g, "\\Vert{}").replace(/\|/g, "\\vert{}")}$`;
    i = close + 1;
  }
  return out;
}

// With `math` (text normalizeMath wrote, where every $ left unescaped delimits a formula) URLs inside formulas are
// left alone; a URL that starts before a $ is matched first, so https://…/$metadata stays whole. Without it (renderers
// that don't parse math) a $ is plain text.
const URL_OR_MATH = /(?<!\\)\$\$[\s\S]*?\$\$|(?<!\\)\$(?:\\[\s\S]|[^\\$])+?\$|(^|[^\x21-\x7e]|[\s(])(https?:\/\/[\x21-\x7e]+)/g;

export function linkify(md: string, { math = false }: { math?: boolean } = {}): string {
  return splitCode(md)
    .map((part, i) => {
      if (i % 2) return part;
      return part.replace(math ? URL_OR_MATH : BARE, (m, pre: string | undefined, url: string | undefined, off: number, all: string) => {
        if (url === undefined) return m; // a formula
        if (pre === "(" && all.slice(Math.max(0, off - 1), off + 1) === "](") return m; // [text](url)
        const u = trimURL(url);
        return `${pre}<${u}>${url.slice(u.length)}`;
      });
    })
    .join("");
}

// persona://session links survive (they move to that conversation); everything else as react-markdown decides
// (javascript: and other apps' schemes are dropped).
export function markdownUrl(url: string): string {
  return linkRoute(url).kind === "session" ? url : defaultUrlTransform(url);
}

// A link opens where it belongs (lib/links.ts): Feishu documents in the side panel (read as the chat's manager),
// conversations in place, pages in a new tab. ⌘ / Ctrl / middle click is always the browser's new tab.
// `line`: a path a message names at a line (src/a.ts:12, PathLinks.tsx) opens there.
function Link({ href, children, mid, cwd, line, title: hover }: { href?: string; children?: React.ReactNode; mid?: string; cwd?: string; line?: number; title?: string }) {
  const title = textOf(children).trim();
  const path = mid && href && isMachineRef(href) ? resolveRef(cwd ?? "", href) : "";
  const manager = useLinkManager();
  return (
    <a
      href={path && mid ? fileDownloadURL(mid, path, cwd, { inline: true }) : href ? linkHref(href) : undefined}
      title={hover}
      target="_blank"
      rel="noreferrer noopener"
      onClick={(e) => {
        if (path && mid && !e.defaultPrevented && e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey) {
          e.preventDefault();
          openPanel({ kind: "file", mid, path, cwd, line });
        } else clickLink(e, href, title && title !== href ? title : undefined, manager);
      }}
      onPointerEnter={() => prefetchLink(href, manager)}
    >
      {children}
    </a>
  );
}

// A formula KaTeX has not drawn yet (it loads when a reply first has one, ui/stream/math.ts): remark-math leaves it as a
// <code class="math-inline"> / <pre><code class="math-display">, shown as the TeX it was written in. Once KaTeX is in,
// rehype-katex turns those into formulas before they get here.
const hasClass = (cls: string | undefined, name: string) => (cls ?? "").split(/\s+/).includes(name);

export const markdownComponents: Components = {
  a: ({ href, children }) => <Link href={href}>{children}</Link>,
  pre: ({ children }) => {
    const code = () => textOf(children).replace(/\n$/, "");
    if (hasClass(codeClass(children), "math-display")) return <div className="whitespace-pre-wrap" data-math-pending="display">{`$$\n${code()}\n$$`}</div>;
    const lang = codeLanguage(children);
    if (lang === "mermaid") return <MermaidBlock code={code()} />;
    // the assistant's cards (#1850): drawn with the extensions' components; a block that does not read stays code
    if (CARD_LANGUAGES.includes(lang)) return <AssistantCard lang={lang} code={textOf(children)} fallback={<CodeBlock>{children}</CodeBlock>} />;
    return <CodeBlock>{children}</CodeBlock>;
  },
  code: ({ className, children, node: _node, ...rest }) =>
    hasClass(className, "math-inline") ? (
      <span data-math-pending="inline">${children}$</span>
    ) : (
      <code className={className} {...rest}>
        {children}
      </code>
    ),
  table: ({ children, node: _node, ...rest }) => <TableBlock {...rest}>{children}</TableBlock>,
};

/** The class of a block's <code>. */
function codeClass(children: React.ReactNode): string {
  const code = Array.isArray(children) ? children[0] : children;
  return code && typeof code === "object" && "props" in code ? String((code as { props: { className?: string } }).props.className ?? "") : "";
}

/** The language a fenced block names (```mermaid → "mermaid"), from its <code>'s class. */
function codeLanguage(children: React.ReactNode): string {
  return /(?:^|\s)language-([\w-]+)/.exec(codeClass(children))?.[1]?.toLowerCase() ?? "";
}

/** Whether text normalizeMath wrote has a formula: a $ it left unescaped outside code (lone ones it wrote as \$). */
export function hasMath(md: string): boolean {
  return md.includes("$") && splitCode(md).some((part, i) => i % 2 === 0 && /(?<!\\)\$/.test(part));
}

function textOf(node: React.ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (node && typeof node === "object" && "props" in node) return textOf((node as { props: { children?: React.ReactNode } }).props.children);
  return "";
}

// The same element at every render, so a block that colors itself does not draw its icon again.
const COPY_ICON = <Icon name="copy" size={13} />;

// A fenced block colors itself once the grammars are in and it is near the viewport (stream/highlight.ts, #2465): its
// <code> takes rehype-highlight's classes and tokens. Copying takes its text, which highlighting does not change.
function CodeBlock({ children }: { children: React.ReactNode }) {
  const pre = useRef<HTMLPreElement>(null);
  const code = Array.isArray(children) ? (children.length === 1 ? children[0] : null) : children;
  const lit = useBlockHighlight(pre, isValidElement(code) ? codeClass(code) : undefined, textOf(children));
  return (
    <div className="group/code relative">
      <pre ref={pre}>{lit && isValidElement(code) ? cloneElement(code as ReactElement<{ className?: string }>, lit) : children}</pre>
      <button
        type="button"
        aria-label={t("ui-markdown.copy_code")}
        title={t("ui-markdown.copy")}
        onClick={() => copyText(textOf(children).replace(/\n$/, ""))}
        className="absolute top-1.5 right-1.5 flex size-6 touch-hit items-center justify-center rounded-[6px] bg-surface text-ink-3 opacity-0 shadow-btn transition-opacity duration-150 group-hover/code:opacity-100 hover:text-ink focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
      >
        {COPY_ICON}
      </button>
    </div>
  );
}

// The same hover button as a code block, on the table's corner. The wrap is only a positioning box: the table
// keeps its width, scroll and margin (styles.css passes the prose edge rule through). The button is out of the
// table, so a copy does not pick it up as a cell, and it stays put while a wide table scrolls. It is the control's
// own 24px, not the 44px touch pad: it sits on a cell, and that pad would take the tap meant for a corner link.
// The first placement is the ResizeObserver's first report (after layout, before paint), not a read in the effect: that
// read laid out every table's exchange when a long chat opened, the ones skipped off screen too (#2905). A table in a
// skipped exchange is placed when the exchange is drawn; the button only shows on hover, so it is there before it is seen.
function TableBlock({ children, ref: _ref, ...rest }: React.ComponentProps<"table">) {
  const tableRef = useRef<HTMLTableElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const table = tableRef.current;
    const button = buttonRef.current;
    if (!table || !button) return;
    const place = () => {
      const host = button.parentElement;
      if (!host) return;
      const gap = Math.max(0, host.clientWidth - table.offsetWidth);
      // 0.375rem is top-1.5 / right-1.5, so the inset matches a code block's button at any root font size.
      button.style.top = `calc(${table.offsetTop}px + 0.375rem)`;
      button.style.right = `calc(${gap}px + 0.375rem)`;
    };
    if (typeof ResizeObserver === "undefined") {
      place();
      return;
    }
    const obs = new ResizeObserver(place);
    obs.observe(table);
    if (button.parentElement) obs.observe(button.parentElement);
    return () => obs.disconnect();
  }, []);
  return (
    <div className="md-table group/table relative">
      <table {...rest} ref={tableRef}>
        {children}
      </table>
      <button
        ref={buttonRef}
        type="button"
        aria-label={t("ui-markdown.copy_table")}
        title={t("ui-markdown.copy_table")}
        onClick={() => {
          const table = tableRef.current;
          if (!table) return;
          const tsv = tableToTsv(table);
          if (tsv === undefined) {
            toast(t("ui-atoms.copy_failed"), "error");
            return;
          }
          const html = tableHtml(table);
          void writeTableClipboard(tsv, html).then((ok) => {
            toast(ok ? t("ui-atoms.copied") : t("ui-atoms.copy_failed"), ok ? "ok" : "error");
          });
        }}
        className="absolute top-1.5 right-1.5 flex size-6 items-center justify-center rounded-[6px] bg-surface text-ink-3 opacity-0 shadow-btn pointer-events-none transition-opacity duration-150 group-hover/table:pointer-events-auto group-hover/table:opacity-100 hover:text-ink focus-visible:pointer-events-auto focus-visible:opacity-100 [@media(hover:none)]:pointer-events-auto [@media(hover:none)]:opacity-100"
      >
        <Icon name="copy" size={13} />
      </button>
    </div>
  );
}

// A conversation's link components (paths on its machine open in the file panel), one object per machine and directory:
// a kept tree (markdown-cache.ts) and a fresh one then use the same component types, so swapping one for the other
// does not mount the links again.
const linkComponents = new Map<string, Components>();

function componentsFor(mid: string, cwd: string | undefined): Components {
  const k = `${mid}\0${cwd ?? "\u0001"}`;
  let c = linkComponents.get(k);
  if (!c) {
    c = {
      ...markdownComponents,
      a: ({ href, children, node }) => {
        const line = Number(node?.properties?.dataLine) || undefined;
        const title = typeof node?.properties?.title === "string" ? node.properties.title : undefined;
        return <Link href={href} mid={mid} cwd={cwd} line={line} title={title}>{children}</Link>;
      },
    };
    linkComponents.set(k, c);
    if (linkComponents.size > 64) linkComponents.delete(linkComponents.keys().next().value!);
  }
  return c;
}

// `compact`: a few lines inside a card (a task's receipt): body size in the card's color, headings as bold body text,
// tight blocks, so a parent's line-clamp shows as much as it can.
// With `mid` (a conversation's machine) the paths the text names that are there link to the file panel; while
// `streaming`, not the last word (a path still being written would be asked about at every step of it).
// `streaming`: the text is still being written (the assistant's delta): rendered at most every 100 ms or so, in chunks
// (only the last is parsed again), what the growing end leaves open closed for the moment, its words fading in.
// `fade`: a block that just arrived whole: its words fade in once, then it is rendered plain. Neither fades with
// "Reply animation" off or reduced motion; once streaming ends it is plain Markdown again, without the fade's spans.
// `lineBreaks`: a reply, read line by line as it was written (#2088, `reply-breaks.ts`): a single line break is one,
// and steps numbered on from 5 right under a line of text are a list.
export const Markdown = memo(function Markdown({ text: raw, small, compact, mid, cwd, streaming, fade, lineBreaks }: { text: string; small?: boolean; compact?: boolean; mid?: string; cwd?: string; streaming?: boolean; fade?: boolean; lineBreaks?: boolean }) {
  const components = useMemo<Components>(() => (mid ? componentsFor(mid, cwd) : markdownComponents), [mid, cwd]);
  const text = useThrottledText(raw, Boolean(streaming));
  const motion = useReplyMotion();
  const wordFade = useWordFade(Boolean(streaming) && motion, Boolean(fade && !streaming) && motion, text);
  const settled = streaming ? writtenText(text) : text;
  const paths = useMemo(() => (mid ? textPaths(settled) : []), [settled, mid]);
  const { linkable, key } = usePathLinks(mid, cwd, paths);
  // Code is highlighted by its blocks (CodeBlock), or with the switch off by rehype-highlight over the whole reply.
  const fenced = useMemo(() => hasLanguageFence(text), [text]);
  const inBlock = codeHighlight.inBlock;
  const highlight = useHighlight(fenced && !inBlock);
  useAskHighlight(fenced && inBlock);
  const md = useMemo(() => {
    const lines = lineBreaks ? replyBreaks(text) : text;
    return linkify(normalizeMath(streaming ? repairStreamingMarkdown(lines) : lines), { math: true });
  }, [text, streaming, lineBreaks]);
  const remarkPlugins = lineBreaks ? REMARK_LINES : REMARK;
  const wantsMath = useMemo(() => hasMath(md), [md]);
  const katex = useMath(wantsMath);
  const rehypePlugins = useMemo<PluggableList>(
    () => [...(katex ? [katex] : []), ...(highlight ? [highlight] : []), ...(key ? [rehypePathLinks(linkable)] : [])],
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key, highlight, katex],
  );
  const chunks = useMemo(() => (streaming ? splitChunks(md) : wordFade ? [{ start: 0, content: md }] : null), [md, streaming, wordFade]);
  // A finished block: its tree from the cache when this text was drawn with the same plugins and links before.
  const tree = useMemo(
    () =>
      chunks || !markdownCache.on
        ? null
        : cachedMarkdown(markdownKey(md, `${katex ? "katex" : ""}|${highlight ? "highlight" : ""}|${key}`, mid, cwd), { children: md, remarkPlugins, rehypePlugins, components, urlTransform: markdownUrl }),
    [chunks, md, katex, highlight, key, mid, cwd, remarkPlugins, rehypePlugins, components],
  );
  return (
    <HighlightWanted.Provider value={fenced && inBlock}>
      <div className={`prose-j ${small ? "prose-sm" : ""} ${compact ? "prose-compact" : ""}`}>
        {chunks ? (
          chunks.map((c) => (
            <StreamChunk key={c.start} start={c.start} content={c.content} remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={components} urlTransform={markdownUrl} fade={wordFade} plain={!streaming} wholeDocument={chunks.length === 1} />
          ))
        ) : tree ? (
          tree
        ) : (
          <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={components} urlTransform={markdownUrl}>
            {md}
          </ReactMarkdown>
        )}
      </div>
    </HighlightWanted.Provider>
  );
});

// remark-cjk-friendly: **bold** right next to full-width CJK punctuation is still emphasis (CommonMark's flanking rules miss it).
const REMARK: PluggableList = [remarkGfm, remarkMath, remarkCjkFriendly];
const REMARK_LINES: PluggableList = [...REMARK, remarkSoftBreaks];

/**
 * The message's fade timeline: while it streams (a fresh one per stream), or for a block that arrived whole until its
 * last word has faded in (`once`, at mount only), after which it renders plain. Null: no fade.
 * A block drawn again (the stream it ends, an earlier mount of the same block: wordFade.ts shownBefore) takes over that
 * timeline: words already in stay in, and a block that is all in is plain from the start. The swap back to plain waits
 * for an idle moment and is a transition: it re-renders the whole block, and typing goes first.
 */
function useWordFade(streaming: boolean, once: boolean, text: string): WordFadeState | null {
  const [before] = useState(() => (once && !streaming ? shownBefore(text) : null));
  const [fadingOnce, setFadingOnce] = useState(once && !before?.done);
  const ref = useRef<WordFadeState | null>(null);
  if (!streaming && !(once && fadingOnce)) ref.current = null;
  else ref.current ??= (!streaming && before?.state) || createWordFadeState();
  if (ref.current) rememberShown(text, ref.current);
  useEffect(() => {
    if (streaming || !once || !fadingOnce || !ref.current) return;
    // after the commit: every word is on the timeline by now
    let cancelIdle = () => {};
    const t = window.setTimeout(() => {
      cancelIdle = whenIdle(() => startTransition(() => setFadingOnce(false)));
    }, remainingMs(ref.current) + 32);
    return () => {
      clearTimeout(t);
      cancelIdle();
    };
  }, [streaming, once, fadingOnce, text]);
  return ref.current;
}
