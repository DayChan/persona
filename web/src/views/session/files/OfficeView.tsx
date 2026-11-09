// Office documents in the web's file viewer (docs/web/web.md "Office", #1345): docx through docx-preview, pptx through our
// own reader (lib/pptx.ts), and anything else, or either when it fails, as a PDF the machine's LibreOffice writes
// (file/convert). The rendered pages go into a frame with no scripts and no network (srcdoc, empty sandbox, CSP):
// a document is someone else's content, and nothing in it may run as this page or call out.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { api, ApiError, enc, errorText, fileDownloadURL, isAbort, pastHub } from "../../../lib/api";
import { base64Bytes, extOf, OFFICE_RENDERED, type FileContent } from "../../../lib/files";
import { SANS, withFallback } from "../../../lib/fonts";
import { bytes } from "../../../lib/format";
import { IWORK_PREVIEWS, readZipEntry, type ReadRange } from "../../../lib/zip-range";
import { Button, Loading } from "../../../ui/atoms";
import { fileBytes } from "./file-data";
import { Empty } from "./parts";
import { t } from "../../../i18n";
import { tRich } from "../../../i18n/rich";

const MAX_OFFICE_BYTES = 100 << 20; // bigger documents go to LibreOffice or download
const PAD = 24; // the frame's margin around the pages

type Rendered = { html: string; css: string; width: number; count: number; unit: "pages" | "slides" };
const IWORK = ["pages", "key", "numbers"];

/** Byte ranges of a machine file: by HTTP Range from file/download, or out of the base64 an older hub sent. */
function rangeReader(mid: string, f: FileContent, cwd: string | undefined, signal: AbortSignal): ReadRange {
  if (!f.stream && typeof f.data === "string") {
    const all = base64Bytes(f.data);
    return async (s, e) => all.slice(s, e);
  }
  return async (s, e) => {
    const res = await fetch(fileDownloadURL(mid, f.path, cwd), { headers: { Range: `bytes=${s}-${e - 1}` }, signal });
    if (res.status !== 206 && res.status !== 200) throw new Error(`HTTP ${res.status}`);
    const got = new Uint8Array(await res.arrayBuffer());
    return res.status === 200 ? got.slice(s, e) : got;
  };
}

type State =
  | { kind: "loading"; label: string }
  | { kind: "preview"; url: string; pdf: boolean }
  | { kind: "rendered"; doc: Rendered }
  | { kind: "pdf"; url: string }
  | { kind: "error"; title: string; detail: string; convertible: boolean; missing?: boolean };

export function OfficeView({ mid, cwd, file, onDownload }: { mid: string; cwd?: string; file: FileContent; onDownload: () => void }) {
  const ext = extOf(file.name);
  const renderable = OFFICE_RENDERED.includes(ext) && typeof file.text !== "string";
  const iwork = IWORK.includes(ext) && typeof file.text !== "string";
  const [state, setState] = useState<State>({ kind: "loading", label: t("session-files-officeview.opening") });
  const [want, setWant] = useState<"render" | "preview" | "pdf">(renderable ? "render" : iwork ? "preview" : "pdf");

  useEffect(() => {
    const ac = new AbortController();
    const fail = (title: string, e: unknown, convertible: boolean) =>
      !isAbort(e) && !ac.signal.aborted && setState({ kind: "error", title, detail: errorText(e), convertible, missing: e instanceof ApiError && e.code === "no_converter" });
    if (want === "preview") {
      // iWork documents carry a preview Apple's apps wrote: a PDF of every page, or a picture of the first
      let url = "";
      setState({ kind: "loading", label: t("session-files-officeview.reading_preview") });
      readZipEntry(rangeReader(mid, file, cwd, ac.signal), file.size, IWORK_PREVIEWS)
        .then((got) => {
          if (ac.signal.aborted) return;
          if (!got) return setWant("pdf");
          const pdf = got.name.toLowerCase().endsWith(".pdf");
          url = URL.createObjectURL(new Blob([got.data as Uint8Array<ArrayBuffer>], { type: pdf ? "application/pdf" : got.name.toLowerCase().endsWith(".png") ? "image/png" : "image/jpeg" }));
          setState({ kind: "preview", url, pdf });
        })
        .catch((e) => (isAbort(e) || ac.signal.aborted ? undefined : setWant("pdf")));
      return () => {
        ac.abort();
        if (url) URL.revokeObjectURL(url);
      };
    }
    if (want === "render") {
      setState({ kind: "loading", label: t("session-files-officeview.opening_size", { size: bytes(file.size) }) });
      (async () => {
        if (file.size > MAX_OFFICE_BYTES) throw new Error(t("session-files-officeview.too_large", { size: bytes(file.size) }));
        const data = await fileBytes(mid, file, cwd, ac.signal);
        const doc = ext.startsWith("ppt") || ext === "potx" ? await renderPptx(data) : await renderDocx(data);
        if (!ac.signal.aborted) setState({ kind: "rendered", doc });
      })().catch((e) => fail(t("session-files-officeview.render_failed"), e, true));
    } else {
      setState({ kind: "loading", label: t("session-files-officeview.converting") });
      // the hub gives the machine's LibreOffice 2 minutes (hub/download.go)
      api<{ path: string }>(`/machines/${enc(mid)}/file/convert?path=${enc(file.path)}&cwd=${enc(cwd ?? "")}&to=pdf`, { signal: ac.signal, timeout: pastHub(120_000) })
        .then((r) => !ac.signal.aborted && setState({ kind: "pdf", url: fileDownloadURL(mid, r.path, "", { inline: true }) }))
        .catch((e) => fail(t("session-files-officeview.convert_failed"), e, false));
    }
    return () => ac.abort();
  }, [mid, cwd, file, ext, want]);

  const bar = (left: React.ReactNode, right?: React.ReactNode) => (
    <div className="flex min-h-9 shrink-0 items-center gap-2 border-b border-line px-3 py-1 text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums">
      <span className="min-w-0 flex-1 truncate">{left}</span>
      {right}
    </div>
  );

  if (state.kind === "loading")
    return (
      <div className="flex flex-1 items-center justify-center">
        <Loading label={state.label} />
      </div>
    );
  if (state.kind === "error") {
    const detail = state.missing ? (
      <>
        {t("session-files-officeview.no_libreoffice")}
        <br />
        {tRich("session-files-officeview.install_macos", { command: <code className="whitespace-nowrap">brew install --cask libreoffice</code> })}
        <br />
        {tRich("session-files-officeview.install_linux", { command: <code className="whitespace-nowrap">apt install libreoffice</code> })}
      </>
    ) : (
      state.detail
    );
    return (
      <Empty
        icon="info"
        title={state.missing ? t("session-files-officeview.needs_libreoffice") : state.title}
        detail={detail}
        action={
          <div className="flex gap-2">
            {state.convertible && (
              <Button icon="retry" onClick={() => setWant("pdf")}>
                {t("session-files-officeview.to_pdf")}
              </Button>
            )}
            <Button icon="arrowDown" onClick={onDownload}>
              {t("session-files-officeview.download")}
            </Button>
          </div>
        }
      />
    );
  }
  if (state.kind === "preview")
    return (
      <>
        {bar(
          state.pdf ? t("session-files-officeview.own_preview") : t("session-files-officeview.own_preview_image"),
          <Button tone="quiet" onClick={() => setWant("pdf")}>
            {t("session-files-officeview.to_pdf")}
          </Button>,
        )}
        {state.pdf ? (
          <iframe src={state.url} title={file.name} className="min-h-0 w-full flex-1 border-0 bg-inset" />
        ) : (
          <div className="flex min-h-0 flex-1 items-start justify-center overflow-auto bg-inset p-6">
            <img src={state.url} alt={file.name} className="max-w-full rounded-[6px] shadow-[0_1px_3px_rgba(0,0,0,.18),0_8px_24px_rgba(0,0,0,.12)]" />
          </div>
        )}
      </>
    );
  if (state.kind === "pdf")
    return (
      <>
        {bar(
          t("session-files-officeview.libreoffice_pdf"),
          (renderable || iwork) && (
            <Button tone="quiet" onClick={() => setWant(renderable ? "render" : "preview")}>
              {renderable ? t("session-files-officeview.render_preview") : t("session-files-officeview.builtin_preview")}
            </Button>
          ),
        )}
        <iframe src={state.url} title={file.name} className="min-h-0 w-full flex-1 border-0 bg-inset" />
      </>
    );
  return (
    <>
      {bar(
        `${t(state.doc.unit === "slides" ? "session-files-officeview.slides" : "session-files-officeview.pages", { count: state.doc.count })} · ${bytes(file.size)}`,
        <Button tone="quiet" onClick={() => setWant("pdf")}>
          {t("session-files-officeview.to_pdf")}
        </Button>,
      )}
      <DocFrame doc={state.doc} title={file.name} />
    </>
  );
}

/** The pages in a script-less frame, scaled down to the panel's width (never up). */
function DocFrame({ doc, title }: { doc: Rendered; title: string }) {
  const box = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 600, h: 600 });
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setSize({ w: el.clientWidth, h: el.clientHeight }));
    ro.observe(el);
    setSize({ w: el.clientWidth, h: el.clientHeight });
    return () => ro.disconnect();
  }, []);
  const dark = document.documentElement.classList.contains("dark");
  const natural = doc.width + PAD * 2;
  const scale = Math.min(1, size.w / natural);
  const srcDoc = `<!doctype html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:">
<style>:root{--muted:${dark ? "#9aa0a6" : "#5f6368"};color-scheme:${dark ? "dark" : "light"}}html,body{margin:0;background:${dark ? "#161719" : "#eceef1"}}body{padding:${PAD}px}${doc.css}</style>
</head><body>${doc.html}</body></html>`;
  return (
    <div ref={box} className="relative min-h-0 flex-1 overflow-hidden bg-inset">
      <iframe
        title={title}
        // no allow-scripts, no allow-same-origin: links may open a new tab, nothing else
        sandbox="allow-popups allow-popups-to-escape-sandbox"
        srcDoc={srcDoc}
        className="absolute top-0 left-0 border-0"
        style={{ width: size.w / scale, height: size.h / scale, transform: `scale(${scale})`, transformOrigin: "0 0" }}
      />
    </div>
  );
}

/** Web links open in a new tab; any other kind of link (javascript:, file:, the document's own anchors) is dropped. */
function tameLinks(root: Element) {
  for (const a of root.querySelectorAll("a[href]")) {
    const href = a.getAttribute("href") ?? "";
    if (/^https?:\/\//i.test(href)) {
      a.setAttribute("target", "_blank");
      a.setAttribute("rel", "noopener noreferrer");
    } else a.removeAttribute("href");
  }
}

/** A length in a style (pt / px / in / cm) in px. */
function lengthPx(v: string): number {
  const n = parseFloat(v);
  if (!Number.isFinite(n)) return 0;
  if (v.endsWith("pt")) return (n * 4) / 3;
  if (v.endsWith("in")) return n * 96;
  if (v.endsWith("cm")) return (n * 96) / 2.54;
  if (v.endsWith("mm")) return (n * 96) / 25.4;
  return n;
}

const DOCX_CSS = `
.docx-wrapper{background:transparent!important;padding:0!important;display:flex;flex-direction:column;align-items:center;font-family:${SANS}}
.docx-wrapper>section.docx{box-shadow:0 1px 3px rgba(0,0,0,.18),0 8px 24px rgba(0,0,0,.12)!important;margin-bottom:${PAD}px!important}
a{color:#0563c1}
`;

/** Puts fallbacks on every font-family docx-preview wrote, in its style sheets and on its elements. */
function fontFallbacks(css: string, body: Element): string {
  for (const el of body.querySelectorAll<HTMLElement>("[style*=font-family]")) el.style.fontFamily = withFallback(el.style.fontFamily);
  return css.replace(/font-family:\s*([^;}]+)/g, (_, f: string) => `font-family: ${withFallback(f.replace(/\s*!important\s*$/, ""))}${/!important\s*$/.test(f) ? " !important" : ""}`);
}

async function renderDocx(data: ArrayBuffer): Promise<Rendered> {
  const { renderAsync } = await import("docx-preview");
  // detached containers: the pages are serialized into the frame, never shown in this page
  const body = document.createElement("div");
  const styles = document.createElement("div");
  await renderAsync(data, body, styles, {
    className: "docx",
    inWrapper: true,
    breakPages: true,
    ignoreLastRenderedPageBreak: true,
    useBase64URL: true, // the frame cannot read this page's blob: URLs
    renderHeaders: true,
    renderFooters: true,
    renderFootnotes: true,
    renderEndnotes: true,
    renderComments: false,
    experimental: true,
  });
  tameLinks(body);
  const css = fontFallbacks([...styles.querySelectorAll("style")].map((st) => st.textContent ?? "").join("\n"), body).replace(/</g, "\\3c ");
  const pages = [...body.querySelectorAll("section.docx")] as HTMLElement[];
  const width = Math.max(600, ...pages.map((p) => lengthPx(p.style.width)));
  return { html: body.innerHTML, css: css + DOCX_CSS, width: Math.round(width), count: pages.length, unit: "pages" };
}

async function renderPptx(data: ArrayBuffer): Promise<Rendered> {
  const [{ default: JSZip }, { readPptx, deckHTML, DECK_CSS }] = await Promise.all([import("jszip"), import("../../../lib/pptx")]);
  const zip = await JSZip.loadAsync(data);
  const deck = await readPptx(zip, (s) => new DOMParser().parseFromString(s, "application/xml"));
  if (!deck.slides.length) throw new Error(t("session-files-officeview.no_slides"));
  return { html: deckHTML(deck), css: DECK_CSS, width: deck.width, count: deck.slides.length, unit: "slides" };
}
