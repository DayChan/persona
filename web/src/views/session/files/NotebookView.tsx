// A Jupyter notebook in the file viewer (docs/web/web.md "Jupyter", #1407; iOS / Mac: #1296): Markdown cells rendered
// (math, Mermaid, pasted pictures), code cells colored for the kernel's language with their In [n], and each output in
// the one form lib/notebook.ts picked. HTML outputs (pandas tables, widgets' fallbacks) go into a frame with no scripts
// and no network, like the Office previews: a notebook is someone else's content.
import { useLayoutEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { extendStates, languageOf, lexLine, splitLines, type LexState, type Token } from "../../../lib/files";
import { languageFile, withAttachmentRefs, type Cell, type Notebook, type Output } from "../../../lib/notebook";
import { cx } from "../../../ui/atoms";
import { Markdown } from "../../../ui/Markdown";
import { TOKEN_CLASS } from "./file-data";
import { t } from "../../../i18n";

export function NotebookView({ nb }: { nb: Notebook }) {
  const file = languageFile(nb.language);
  const code = nb.cells.filter((c) => c.type === "code").length;
  return (
    <>
      <div className="flex min-h-8 shrink-0 items-center gap-2 border-b border-line px-3 py-1.5 text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums">
        <span>{nb.kernel ?? nb.language}</span>
        <span>·</span>
        <span>
          {t("session-files-notebookview.cells", { count: nb.cells.length, code })}
        </span>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4">
        <div className="mx-auto flex max-w-[900px] flex-col gap-3">
          {nb.cells.map((c, i) => (
            <CellView key={i} cell={c} file={file} />
          ))}
        </div>
      </div>
    </>
  );
}

const PROMPT = "w-[64px] shrink-0 pt-[7px] pr-2 text-right font-mono text-[11.5px] tabular-nums select-none";

function CellView({ cell, file }: { cell: Cell; file: string }) {
  if (cell.type === "markdown") return <MarkdownCell cell={cell} />;
  if (cell.type === "raw")
    return (
      <div className="flex">
        <span className={PROMPT} />
        <pre className="min-w-0 flex-1 overflow-x-auto rounded-[8px] bg-inset px-3 py-2 font-mono text-[12.5px] whitespace-pre-wrap text-ink-2">{cell.source}</pre>
      </div>
    );
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex">
        <span className={cx(PROMPT, "text-accent-ink")}>[{cell.count ?? " "}]:</span>
        <CodeBlock source={cell.source} file={file} />
      </div>
      {cell.outputs.map((o, i) => (
        <div key={i} className="flex">
          <span className={cx(PROMPT, "text-red/80")}>{i === 0 && (o.kind === "text" || o.kind === "image" || o.kind === "html") && cell.count != null ? `[${cell.count}]:` : ""}</span>
          <div className="min-w-0 flex-1">
            <OutputView o={o} />
          </div>
        </div>
      ))}
    </div>
  );
}

function CodeBlock({ source, file }: { source: string; file: string }) {
  const lines = useMemo(() => splitLines(source), [source]);
  const rows = useMemo(() => {
    const lang = languageOf(file, lines[0]);
    const states: LexState[] = [];
    if (lang) extendStates(lines, lang, states, lines.length);
    return lines.map((line, i) => (lang ? lexLine(line, lang, states[i] ?? null).tokens : []));
  }, [lines, file]);
  return (
    <pre className="min-w-0 flex-1 overflow-x-auto rounded-[8px] border border-line bg-inset px-3 py-2 font-mono text-[12.5px] leading-[20px] text-ink">
      {lines.map((line, i) => (
        <div key={i} className="whitespace-pre">
          {colored(line, rows[i])}
          {line === "" && "​"}
        </div>
      ))}
    </pre>
  );
}

function colored(text: string, tokens: Token[]): React.ReactNode[] {
  const parts: React.ReactNode[] = [];
  let at = 0;
  for (const t of tokens) {
    if (t.from > at) parts.push(text.slice(at, t.from));
    parts.push(
      <span key={t.from} className={TOKEN_CLASS[t.kind]}>
        {text.slice(t.from, t.to)}
      </span>,
    );
    at = t.to;
  }
  if (at < text.length) parts.push(text.slice(at));
  return parts;
}

function MarkdownCell({ cell }: { cell: Cell }) {
  const box = useRef<HTMLDivElement>(null);
  const text = useMemo(() => withAttachmentRefs(cell.source), [cell.source]);
  // pictures pasted into the cell: react-markdown lets no data: URL through, so they are put in after it renders
  useLayoutEffect(() => {
    for (const img of box.current?.querySelectorAll<HTMLImageElement>('img[src^="#nb-attachment="]') ?? []) {
      const name = decodeURIComponent(img.getAttribute("src")!.slice("#nb-attachment=".length));
      const src = cell.attachments[name];
      if (src) img.src = src;
    }
  }, [text, cell.attachments]);
  return (
    <div className="flex">
      <span className={PROMPT} />
      <div ref={box} className="min-w-0 flex-1 text-[14px]">
        <Markdown text={text} />
      </div>
    </div>
  );
}

function OutputView({ o }: { o: Output }) {
  switch (o.kind) {
    case "stream":
      return <pre className={cx("overflow-x-auto px-1 py-1 font-mono text-[12.5px] leading-[19px] whitespace-pre-wrap", o.name === "stderr" ? "rounded-[6px] bg-red-tint text-red" : "text-ink")}>{o.text}</pre>;
    case "text":
    case "json":
      return <pre className="overflow-x-auto px-1 py-1 font-mono text-[12.5px] leading-[19px] whitespace-pre-wrap text-ink">{o.text}</pre>;
    case "image":
      return <img src={o.src} alt="" className={cx("max-w-full", o.mime === "image/svg+xml" && "rounded-[6px] bg-white")} />;
    case "markdown":
      return (
        <div className="text-[14px]">
          <Markdown text={o.text} />
        </div>
      );
    case "error":
      return (
        <div className="max-w-full overflow-x-auto rounded-[8px] bg-red-tint px-3 py-2 font-mono text-[12.5px] leading-[19px] text-red [scrollbar-width:thin]">
          {/* the traceback ends with the error itself, as Jupyter shows it */}
          {o.traceback ? (
            // its columns as the terminal laid them out: scroll sideways, never wrap or cut
            <pre className="w-max min-w-full whitespace-pre">{o.traceback}</pre>
          ) : (
            <div className="font-semibold">
              {o.name}
              {o.value && `: ${o.value}`}
            </div>
          )}
        </div>
      );
    case "html":
      return <HtmlOutput html={o.html} />;
  }
}

/** Whether the page is in dark mode now (the class views/theme.ts puts on <html>). */
function useDark(): boolean {
  return useSyncExternalStore(
    (on) => {
      const mo = new MutationObserver(on);
      mo.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
      return () => mo.disconnect();
    },
    () => document.documentElement.classList.contains("dark"),
    () => false,
  );
}

/** An HTML output in a frame with no scripts and no network; sized from what it holds, resizable, in the page's theme
 * (pandas tables carry no colors of their own). */
function HtmlOutput({ html }: { html: string }) {
  const dark = useDark();
  const rows = (html.match(/<tr[\s>]/gi) ?? []).length;
  const height = Math.min(560, rows ? 16 + rows * 26 : Math.max(60, Math.min(400, Math.ceil(html.length / 12))));
  const c = dark
    ? { scheme: "dark", text: "#e6e6e8", bg: "#232427", line: "#3a3b40", head: "#2b2c30", stripe: "#27282b", link: "#58a6ff" }
    : { scheme: "light", text: "#1f2328", bg: "#ffffff", line: "#d0d7de", head: "#f6f8fa", stripe: "#fbfbfc", link: "#0969da" };
  const srcDoc = `<!doctype html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'">
<style>html{color-scheme:${c.scheme}}body{margin:0;padding:4px 2px;font:13px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif;color:${c.text};background:${c.bg}}
a{color:${c.link}}table{border-collapse:collapse;font-variant-numeric:tabular-nums}th,td{border:1px solid ${c.line};padding:4px 10px;text-align:right}th{background:${c.head}}
tbody tr:nth-child(odd){background:${c.stripe}}thead th{text-align:center}</style></head><body>${html}</body></html>`;
  return (
    <div className="resize-y overflow-hidden rounded-[8px] border border-line bg-surface" style={{ height }}>
      <iframe title={t("session-files-notebookview.output")} sandbox="" srcDoc={srcDoc} className="h-full w-full border-0" />
    </div>
  );
}
