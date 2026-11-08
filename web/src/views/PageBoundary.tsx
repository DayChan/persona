// A page that fails to render does not take the app down (#1996): the pages that load on demand can fail to load, the
// usual way being a hub redeployed under an open page (the old hashed chunk is gone). That one reloads the page once
// (it comes back with the new chunks); anything else, or a second time, shows what went wrong with a way to retry.
import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "../ui/atoms";
import { t } from "../i18n";
import { reportBoundary } from "../lib/error-reports";

const RELOADED = "persona.chunkReload";

/** Whether an error is a chunk that would not load (a dynamic import whose file is gone, or a network failure). */
export function isChunkError(e: unknown): boolean {
  const msg = e instanceof Error ? `${e.name} ${e.message}` : String(e);
  return /dynamically imported module|Importing a module script failed|error loading dynamically|ChunkLoadError/i.test(msg);
}

/** Whether to reload for this error: a chunk error, and not one we already reloaded for in the last minute. */
export function reloadsFor(e: unknown, lastReload: number | null, now: number): boolean {
  return isChunkError(e) && (lastReload === null || now - lastReload > 60_000);
}

function lastReload(): number | null {
  try {
    const v = Number(sessionStorage.getItem(RELOADED));
    return Number.isFinite(v) && v > 0 ? v : null;
  } catch {
    return null;
  }
}

export class PageBoundary extends Component<{ children: ReactNode; page: string }, { error: unknown }> {
  state = { error: null as unknown };

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    if (!reloadsFor(error, lastReload(), Date.now())) {
      // shown to the user: a render crash (fatal), or a chunk that still would not load after the reload (warning). The
      // first chunk failure, which only reloads, is the expected cost of a redeploy and not reported.
      reportBoundary(error, "page_boundary", isChunkError(error), info.componentStack);
      return;
    }
    try {
      sessionStorage.setItem(RELOADED, String(Date.now()));
    } catch {
      // no storage: reload anyway; the next failure shows the message (the timestamp is not there to stop a loop,
      // but a reload that fails the same way lands here again only after the user acts)
    }
    location.reload();
  }

  componentDidUpdate(prev: { page: string }) {
    // another page: the error was this page's
    if (prev.page !== this.props.page && this.state.error) this.setState({ error: null });
  }

  render() {
    if (!this.state.error) return this.props.children;
    const chunk = isChunkError(this.state.error);
    return (
      <div role="alert" className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
        <p className="text-[14px] font-medium text-ink">{chunk ? t("pageboundary.chunk_title") : t("pageboundary.error_title")}</p>
        <p className="max-w-[320px] text-[12.5px] text-ink-3">{chunk ? t("pageboundary.chunk_body") : t("pageboundary.error_body")}</p>
        <Button onClick={() => location.reload()}>{t("pageboundary.reload")}</Button>
      </div>
    );
  }
}
