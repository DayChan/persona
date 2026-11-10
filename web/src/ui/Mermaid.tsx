// A ```mermaid block drawn as its diagram (lib/mermaid.ts), with the source a click away. While a reply is still
// streaming the code changes: it is drawn once it has been still for a moment, keeping the last drawing until then.
import { useEffect, useRef, useState } from "react";
import { cachedMermaid, mermaidProblem, renderMermaid } from "../lib/mermaid";
import { useCloseOnBack } from "../lib/back-close";
import { isNoHover, NARROW, useMedia } from "./media-query";
import { copyText, cx } from "./atoms";
import { t } from "../i18n";
import { Icon } from "./icons";
import { useDark } from "./use-dark";
import { useEscapeClose, useModalFocus } from "./overlay";

const SETTLE_MS = 350;

export function MermaidBlock({ code }: { code: string }) {
  const dark = useDark();
  const [svg, setSvg] = useState(() => (typeof document === "undefined" ? undefined : cachedMermaid(code, dark)));
  const [error, setError] = useState<{ summary: string; detail: string } | null>(null);
  const [source, setSource] = useState(false);
  const [big, setBig] = useState(false);

  useEffect(() => {
    const hit = cachedMermaid(code, dark);
    if (hit) {
      setSvg(hit);
      setError(null);
      return;
    }
    let live = true;
    const t = setTimeout(() => {
      renderMermaid(code, dark).then(
        (s) => {
          if (!live) return;
          setSvg(s);
          setError(null);
        },
        (e) => live && setError(mermaidProblem(e)),
      );
    }, SETTLE_MS);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [code, dark]);

  const showSource = source || (!svg && !!error);
  const tool = "flex size-6 touch-hit items-center justify-center rounded-[6px] bg-surface text-ink-3 shadow-btn hover:text-ink";
  return (
    <div className="group/code relative my-3" data-mermaid="">
      {showSource || !svg ? (
        <pre className={cx(!svg && !error && "opacity-60")}>
          <code>{code}</code>
        </pre>
      ) : (
        <div
          // on a touch screen (no hover to find "Enlarge" by) a tap on the diagram opens it big
          onClick={() => isNoHover() && setBig(true)}
          data-mermaid-diagram=""
          className="overflow-x-auto rounded-[10px] border border-line bg-surface p-3 [@media(hover:none)]:cursor-zoom-in [&_svg]:mx-auto [&_svg]:h-auto [&_svg]:max-h-[460px] [&_svg]:max-w-full"
          // Mermaid's strict mode has already sanitized this markup (DOMPurify)
          dangerouslySetInnerHTML={{ __html: svg }}
        />
      )}
      {error && (
        <details className="mt-1 text-[12px]">
          <summary className="cursor-pointer text-red select-none">{error.summary}</summary>
          <pre className="mt-1 overflow-x-auto rounded-[6px] bg-inset px-2 py-1.5 font-mono text-[11.5px] whitespace-pre text-ink-2">{error.detail}</pre>
        </details>
      )}
      <div className="absolute top-1.5 right-1.5 flex gap-1 opacity-0 transition-opacity duration-150 group-hover/code:opacity-100 focus-within:opacity-100 [@media(hover:none)]:opacity-100">
        {svg && (
          <button type="button" aria-label={showSource ? t("ui-mermaid.show_diagram") : t("ui-mermaid.show_source")} title={showSource ? t("ui-mermaid.show_diagram") : t("ui-mermaid.show_source")} onClick={() => setSource(!source)} className={tool}>
            <Icon name={showSource ? "image" : "terminal"} size={13} />
          </button>
        )}
        {svg && !showSource && (
          <button type="button" aria-label={t("ui-mermaid.zoom")} title={t("ui-mermaid.zoom")} onClick={() => setBig(true)} className={tool}>
            <Icon name="external" size={13} />
          </button>
        )}
        <button type="button" aria-label={t("ui-mermaid.copy_code")} title={t("ui-mermaid.copy")} onClick={() => copyText(code)} className={tool}>
          <Icon name="copy" size={13} />
        </button>
      </div>
      {big && svg && <Zoomed svg={svg} onClose={() => setBig(false)} />}
    </div>
  );
}

/** The diagram over the whole page at its own size (scroll for a big one); click outside it or Esc closes. A modal:
 *  the focus moves in, Tab stays in, and closing gives it back to "Enlarge". */
function Zoomed({ svg, onClose }: { svg: string; onClose: () => void }) {
  const dialog = useRef<HTMLDivElement>(null);
  useModalFocus(dialog);
  useCloseOnBack(true, onClose, useMedia(NARROW));
  useEscapeClose(true, onClose);
  return (
    <div
      ref={dialog}
      role="dialog"
      aria-modal="true"
      aria-label={t("ui-mermaid.diagram")}
      tabIndex={-1}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-8 outline-none"
      onClick={onClose}
      style={{ animation: "fade-in 150ms ease-out both" }}
    >
      <div
        className="max-h-full max-w-full overflow-auto rounded-[12px] bg-surface p-6 shadow-lg [&_svg]:h-auto [&_svg]:max-w-none"
        onClick={(e) => e.stopPropagation()}
        dangerouslySetInnerHTML={{ __html: svg }}
      />
    </div>
  );
}
