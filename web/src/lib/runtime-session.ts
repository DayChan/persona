// Complete metadata for one active chat. List projection/cursors never become chat detail state.
import { useEffect, useState } from "react";
import { api, ApiError, errorText, sessionPath } from "./api";
import { applyCompleteSessions, getSession, getState, onHubEvent, useStore } from "./store";
import { SESSIONS_LOOKUP_FEATURE } from "./runtime-list";
import type { ListPage } from "./session-list";
import type { Session } from "./types";
import { t } from "../i18n";

export class UnsupportedSessionLookup extends Error {}

export async function exactSession(mid: string, sid: string, signal?: AbortSignal): Promise<Session | undefined> {
  const params = new URLSearchParams({ summary: "0", limit: "50", machine_id: mid, session_id: sid });
  let page: ListPage<Session>;
  try { page = await api<ListPage<Session>>(`/sessions?${params}`, { signal }); }
  catch (error) {
    if (error instanceof ApiError && ([404, 405, 501].includes(error.status) || ["unsupported", "unsupported_feature", "not_supported"].includes(error.code ?? ""))) {
      throw new UnsupportedSessionLookup(t("runtime-session.unsupported"));
    }
    throw error;
  }
  // An old hub can ignore session_id. Never take its first unrelated row or follow that collection cursor.
  if (!page || !Array.isArray(page.sessions) || !Array.isArray(page.remove) || page.remove.length !== 0 || page.has_more !== false || page.next_cursor !== undefined || typeof page.revision !== "string" || !page.revision || page.sessions.length > 1 || page.total !== page.sessions.length) {
    throw new UnsupportedSessionLookup(t("runtime-session.invalid_lookup"));
  }
  const row = page.sessions[0];
  if (row && (row.machine_id !== mid || row.session_id !== sid || typeof row.waiting === "boolean")) throw new UnsupportedSessionLookup(t("runtime-session.invalid_lookup"));
  return row;
}

export interface DetailState { loading: boolean; error: string; missing: boolean; legacy: boolean }

/** The wait before a failed read's quiet retry: 2 s, doubling, then every minute. */
const detailRetryMs = (failures: number) => Math.min(60_000, 2_000 * 2 ** Math.max(0, failures - 1));

/** One request in flight, one trailing invalidation, and no writes from a prior chat/login generation. */
export class SessionDetailLoader {
  private generation = 0;
  private stopped = false;
  private inFlight: Promise<void> | undefined;
  private queued = false;
  private controller?: AbortController;
  private legacy = false;
  private last = "";
  private failures = 0;
  private retryTimer?: ReturnType<typeof setTimeout>;
  constructor(
    private mid: string,
    private sid: string,
    private lookup: () => boolean,
    private cache: () => Session | undefined,
    private commit: (row: Session) => void,
    private emit: (state: DetailState) => void,
  ) {}
  load(): Promise<void> {
    if (this.stopped) return Promise.resolve();
    if (this.inFlight) { this.queued = true; return this.inFlight; }
    const generation = this.generation;
    const task = Promise.resolve().then(async () => {
      do {
        if (this.stopped || generation !== this.generation) return;
        this.queued = false;
        this.controller = new AbortController();
        // an earlier failure's words stay until an answer replaces them
        this.emit({ loading: true, error: this.last, missing: false, legacy: this.legacy || !this.lookup() });
        if (this.stopped || generation !== this.generation) return;
        try {
          if (this.lookup() && !this.legacy) {
            const row = await exactSession(this.mid, this.sid, this.controller.signal);
            if (this.stopped || generation !== this.generation) return;
            if (row) this.commit(row);
            if (this.stopped || generation !== this.generation) return;
            this.answered();
            this.emit({ loading: false, error: row ? "" : t("runtime-session.missing"), missing: !row, legacy: false });
          } else {
            await this.readLegacy(generation);
          }
        } catch (error) {
          if (this.stopped || generation !== this.generation) return;
          if (error instanceof UnsupportedSessionLookup) {
            this.legacy = true;
            await this.readLegacy(generation).catch((failure) => {
              if (!this.stopped && generation === this.generation) this.failed(failure, true);
            });
          } else this.failed(error, this.legacy || !this.lookup());
        }
      } while (this.queued && !this.stopped && generation === this.generation);
    }).finally(() => { if (this.inFlight === task) this.inFlight = undefined; });
    this.inFlight = task;
    return task;
  }
  private async readLegacy(generation: number) {
    const cached = this.cache();
    if (cached && typeof cached.waiting !== "boolean") {
      this.answered();
      this.emit({ loading: false, error: "", missing: false, legacy: true });
      return;
    }
    // Older/unlisted chats already use this configuration route and the transcript. Never refetch /sessions here.
    await api(`${sessionPath(this.mid, this.sid)}/info`, { signal: this.controller?.signal });
    if (this.stopped || generation !== this.generation) return;
    this.answered();
    this.emit({ loading: false, error: cached ? "" : t("runtime-session.metadata_unavailable"), missing: false, legacy: true });
  }
  private answered() {
    this.last = "";
    this.failures = 0;
    clearTimeout(this.retryTimer);
  }
  private failed(error: unknown, legacy: boolean) {
    this.last = errorText(error);
    this.emit({ loading: false, error: this.last, missing: false, legacy });
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(() => void this.load(), detailRetryMs(++this.failures));
  }
  restart() {
    if (this.stopped) return;
    this.generation++;
    clearTimeout(this.retryTimer);
    this.failures = 0;
    this.controller?.abort();
    this.inFlight = undefined;
    this.queued = false;
    this.legacy = false;
    return this.load();
  }
  stop() {
    this.stopped = true;
    this.generation++;
    clearTimeout(this.retryTimer);
    this.queued = false;
    this.controller?.abort();
  }
}

export function useRuntimeSession(mid: string, sid: string, enabled = true) {
  const session = useStore(() => getSession(mid, sid));
  const lookup = useStore((state) => state.features.includes(SESSIONS_LOOKUP_FEATURE));
  const online = useStore((state) => state.conn === "online");
  const [detail, setDetail] = useState<DetailState>({ loading: false, error: "", missing: false, legacy: !lookup });
  const [retry, setRetry] = useState<() => void>(() => () => {});
  useEffect(() => {
    if (!online || !enabled) return;
    const loader = new SessionDetailLoader(mid, sid, () => getState().features.includes(SESSIONS_LOOKUP_FEATURE), () => getSession(mid, sid), (row) => applyCompleteSessions([row]), setDetail);
    let live = true;
    const read = () => { if (live && document.visibilityState !== "hidden") void loader.load(); };
    setRetry(() => read);
    read();
    const offHello = onHubEvent("hello", () => { if (live && document.visibilityState !== "hidden") void loader.restart(); });
    const offList = onHubEvent("sessions_list", read);
    const visible = () => { if (document.visibilityState === "visible") read(); };
    document.addEventListener("visibilitychange", visible);
    return () => { live = false; loader.stop(); offHello(); offList(); document.removeEventListener("visibilitychange", visible); };
  }, [mid, sid, lookup, online, enabled]);
  const unavailable = detail.missing ? { ...session, machine_id: mid, session_id: sid, title: session?.title ?? sid, cwd: session?.cwd ?? "", status: "idle", state: "unknown", read_only_reason: detail.error } as Session : session;
  return { session: unavailable, detail, retry };
}
