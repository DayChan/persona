// A session's items: the newest page over HTTP, then live over the WebSocket (docs/protocol/protocol.md "Continuity of items").
import { useCallback, useEffect, useRef, useState } from "react";
import { api, errorText, isAbort, sessionPath } from "../../lib/api";
import { sessionLoadError } from "../../lib/session-link";
import { getState, listenSession, send, type ItemsEvent } from "../../lib/store";
import { afterPull, afterPush, FRESH_TALLY, newestItemAt, notLive as tallyNotLive, shouldPull, type StaleTally } from "../../lib/stale-page";
import { mergeItems } from "../../lib/turns";
import { chosenManager } from "../../lib/manager-choice";
import { discardFeedCache, readFeedCache, writeFeedCache, type FeedCache } from "../../lib/feed-cache";
import type { Item, ItemsPage } from "../../lib/types";
import { createTextThrottle, type TextThrottle } from "../../ui/stream/throttle";
import { unlandedLive } from "../../lib/live-text";

const PAGE = "turns=2&fold_work=1&lazy_images=1";
/** How often a page checks whether it is behind (a pure comparison; nothing is asked unless it is, lib/stale-page.ts). */
const STALE_TICK_MS = 2000;
/** When each session was last asked for what its stream may have lost: kept across remounts, so reopening a page does not ask again at once. */
const lastPulls = new Map<string, number>();
/** After an older page fails, loadEarlier waits this long before asking again (scrolling at the top calls it on every tick), doubling up to the cap. */
const EARLIER_RETRY_MS = 3000;
const EARLIER_RETRY_MAX_MS = 30_000;

export interface Transcript {
  items: Item[];
  loading: boolean;
  error: string;
  hasMore: boolean;
  loadingEarlier: boolean;
  loadEarlier: () => Promise<void>;
  expandFold: (fold: Item) => Promise<void>;
  /** The reply as it is written, before the transcript has it: the headless assistant's, or an agent's as Claude shows it
   *  (a Claude session Persona started; docs/integrations/claude-integration.md "Live replies"). "" for none. */
  streaming: string;
  /** The stream keeps losing items (the page got them by asking): say so, with a reload. */
  notLive: boolean;
  reload: () => void;
}

/** One delta: the reply so far is throttled; "" (the turn's end) goes out at once and drops what was pending. */
export function applyDelta(t: TextThrottle, text: string): void {
  if (text) t.push(text);
  else t.flush("");
}

export interface TranscriptCache {
  scope: string;
  manager?: string;
}

function cachedPage(mid: string, sid: string, cache?: TranscriptCache): FeedCache | null {
  if (!cache) return null;
  const page = readFeedCache(cache.scope);
  return page?.machineID === mid && page.sessionID === sid && (page.manager ?? "") === (cache.manager ?? "") ? page : null;
}

/** `updatedAt`: the session's `updated_at` from the list (a subagent's from its parent's), what the stale-page check compares.
 *  `assistant`: a Persona conversation, whose live text is the headless assistant's whole reply so far (any reply item
 *  ends it); otherwise it is an agent's message as Claude shows it, which the transcript replaces by its start. */
export function useTranscript(mid: string, sid: string, opts: { starting?: boolean; updatedAt?: string; cache?: TranscriptCache; assistant?: boolean } = {}): Transcript {
  const [initial] = useState(() => cachedPage(mid, sid, opts.cache));
  const [items, setItems] = useState<Item[]>(initial?.items ?? []);
  const [loading, setLoading] = useState(!initial);
  const [error, setError] = useState("");
  const [hasMore, setHasMore] = useState(initial?.hasMore ?? false);
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [streaming, setStreaming] = useState("");
  const [nonce, setNonce] = useState(0);
  const [notLive, setNotLive] = useState(false);
  const cacheScope = opts.cache?.scope;
  const cacheManager = opts.cache?.manager ?? "";
  // This is only the latest page and its live additions, never older pages or expanded folds.
  const snapshot = useRef<{ scope: string; page: FeedCache } | null>(initial && cacheScope ? { scope: cacheScope, page: initial } : null);
  const updatedAt = useRef(opts.updatedAt);
  updatedAt.current = opts.updatedAt;
  const covered = useRef(0); // the last transcript line we have without a gap
  const history = useRef("");
  const itemsRef = useRef<Item[]>([]);
  itemsRef.current = items;
  const gapTimer = useRef<number | undefined>(undefined);
  // Bumped whenever the shown conversation starts over (another session, or the file was rewritten): an older page or a
  // fold asked for before that is dropped when it lands, so it never merges into the conversation shown by then.
  const generation = useRef(0);
  const earlierInFlight = useRef(false); // set at once, unlike loadingEarlier, so scroll events before a render do not ask twice
  // The back-off after a failed older page: when loadEarlier may ask again, and how long the last wait was.
  const earlierRetryAt = useRef(0);
  const earlierWait = useRef(0);
  // The live text (the whole message so far: the assistant's about every 80 ms, an agent's a batch of lines at a time;
  // "" when it ends) re-renders the chat at most once per throttle interval (ui/stream/throttle.ts), the last one never
  // dropped; the end and the reply's item at once. Only the newest items touch it: an older page or an opened fold is
  // never the reply being written.
  const deltas = useRef<TextThrottle>(null!);
  deltas.current ??= createTextThrottle(setStreaming);
  const assistant = Boolean(opts.assistant);
  // an agent's last delta as it came, and what of it the page shows (lib/live-text.ts)
  const liveRaw = useRef("");
  const liveShown = useRef("");

  const apply = useCallback((batch: Item[]) => {
    if (!batch.length) return;
    setItems((have) => mergeItems(have, batch));
  }, []);

  useEffect(() => {
    let dead = false;
    const ctl = new AbortController();
    generation.current++;
    earlierInFlight.current = false;
    setLoadingEarlier(false);
    const kept = snapshot.current;
    const cached = cacheScope && kept?.scope === cacheScope && kept.page.machineID === mid && kept.page.sessionID === sid && (kept.page.manager ?? "") === cacheManager
      ? kept.page : cachedPage(mid, sid, opts.cache);
    setItems(cached?.items ?? []);
    setLoading(!cached);
    setError("");
    setHasMore(cached?.hasMore ?? false);
    deltas.current.flush("");
    liveRaw.current = liveShown.current = "";
    covered.current = 0;
    history.current = cached?.history ?? "";
    earlierRetryAt.current = 0;
    earlierWait.current = 0;
    setNotLive(false);
    let tally: StaleTally = FRESH_TALLY;
    let lastArrival = Date.now();
    let ready = false; // the first page is in
    let pulling = false;
    let checkedFor: string | undefined;
    const pullKey = `${mid}/${sid}`;
    let latest = cached?.items ?? [];
    let latestMore = cached?.hasMore ?? false;
    let pendingItems: Item[] = []; // pushes after the HTTP request started win over its answer
    let pendingHistory = "";
    const save = () => {
      if (!cacheScope || (chosenManager() ?? "") !== cacheManager) return;
      // The store may have accepted a new main before React unmounts this chat. Its old request must not
      // recreate the discarded cache in that interval, even though the credential scope is unchanged.
      const assistant = getState().assistant;
      if (assistant && (assistant.main !== sid || assistant.machine_id !== mid)) return;
      const page: FeedCache = { machineID: mid, sessionID: sid, manager: cacheManager, items: latest, hasMore: latestMore, history: history.current || undefined };
      snapshot.current = { scope: cacheScope, page };
      writeFeedCache(cacheScope, page);
    };
    const remember = (batch: Item[]) => {
      if (!ready) pendingItems = mergeItems(pendingItems, batch);
      latest = mergeItems(latest, batch);
      if (ready) save();
    };
    const rewritten = () => {
      dead = true; // invalidate in-flight reads before React runs the next effect
      generation.current++;
      if (cacheScope) discardFeedCache(cacheScope);
      snapshot.current = null;
      setNonce((x) => x + 1);
    };

    // The live text against the newest items, after they changed (`batch`) or a delta came: the assistant's ends at
    // its reply's item; an agent's shows what of its message the latest one does not hold (lib/live-text.ts).
    const showLive = (batch?: Item[]) => {
      if (assistant) {
        if (batch?.some((i) => i.role === "assistant" && i.kind === "text")) deltas.current.flush("");
        return;
      }
      const want = liveRaw.current && unlandedLive(liveRaw.current, latest);
      if (want === liveShown.current) return;
      liveShown.current = want;
      if (!want || batch) deltas.current.flush(want); // the end, and what items took off, at once
      else deltas.current.push(want);
    };
    const onDelta = (text: string) => {
      if (assistant) return applyDelta(deltas.current, text);
      liveRaw.current = text;
      showLive();
    };

    const subscribe = () => send({ type: "subscribe", machine_id: mid, session_id: sid, since_seq: covered.current || undefined, lazy_images: true, fold_work: true });

    // Items after what we have, a page at a time (past 8 the newest page is cheaper); how many of them were new.
    const pull = async (): Promise<number> => {
      let fresh = 0;
      for (let n = 0; n < 8 && !dead; n++) {
        const p = await api<ItemsPage>(`${sessionPath(mid, sid)}/items?after_seq=${covered.current}&lazy_images=1`, { signal: ctl.signal });
        if (dead) return fresh;
        if (p.history && history.current && p.history !== history.current) {
          rewritten(); // the file was rewritten: start over
          return fresh;
        }
        const got = p.items ?? [];
        // Not what the stream brought while this was on its way: those are merged as they are, and not a miss.
        fresh += got.filter((i) => i.seq > covered.current).length;
        apply(got);
        remember(got);
        showLive(got);
        if (p.to_seq) covered.current = Math.max(covered.current, p.to_seq);
        lastArrival = Date.now();
        if (!p.more_after) return fresh;
      }
      if (!dead) setNonce((x) => x + 1);
      return fresh;
    };
    const fillGap = async () => void (await pull());

    // The safety net (lib/stale-page.ts): the list says the transcript grew past what we hold and no push came.
    const checkStale = () => {
      if (dead || !ready) return;
      const now = Date.now();
      const asked = updatedAt.current;
      if (!shouldPull({ updatedAt: asked, lastItemAt: newestItemAt(itemsRef.current), lastArrival, lastPull: lastPulls.get(pullKey) ?? 0, checkedFor, now, visible: document.visibilityState === "visible", pulling })) return;
      lastPulls.set(pullKey, now);
      pulling = true;
      pull()
        .then((fresh) => {
          checkedFor = asked;
          tally = afterPull(tally, fresh > 0 ? "found" : "empty");
        })
        .catch((e) => {
          if (!isAbort(e)) tally = afterPull(tally, "failed");
        })
        .finally(() => {
          pulling = false;
          if (!dead) setNotLive(tallyNotLive(tally));
        });
    };
    const staleTimer = window.setInterval(checkStale, STALE_TICK_MS);
    document.addEventListener("visibilitychange", checkStale);

    const onItems = (e: ItemsEvent) => {
      if (dead) return;
      if (e.history && history.current && e.history !== history.current) {
        rewritten(); // the file was rewritten: start over
        return;
      }
      if (e.history) history.current = e.history;
      if (!ready && e.history) pendingHistory = e.history;
      remember(e.items ?? []);
      showLive(e.items ?? []);
      lastArrival = Date.now();
      if (e.items?.length && tally !== FRESH_TALLY) {
        tally = afterPush(tally); // the stream works
        setNotLive(false);
      }
      if (e.from_seq !== undefined && covered.current && e.from_seq > covered.current) {
        apply(e.items ?? []);
        // Started once per gap: a busy turn pushes every second or so, and restarting it on each push put the fill
        // off until the agent paused (#5355). The fill reads everything after `covered`, so later pushes need no timer.
        gapTimer.current ??= window.setTimeout(() => {
          gapTimer.current = undefined;
          void fillGap().catch(() => {});
        }, 1500);
        return;
      }
      apply(e.items ?? []);
      if (e.to_seq) covered.current = Math.max(covered.current, e.to_seq);
      if (e.more_after) {
        gapTimer.current ??= window.setTimeout(() => {
          gapTimer.current = undefined;
          void fillGap().catch(() => {});
        }, 1500);
      }
    };

    const off = listenSession(mid, sid, {
      items: onItems,
      delta: onDelta,
      reconnected: subscribe,
    });

    (async () => {
      try {
        const p = await api<ItemsPage>(`${sessionPath(mid, sid)}/items?${PAGE}`, { signal: ctl.signal });
        if (dead) return;
        if (pendingHistory && p.history && pendingHistory !== p.history) {
          rewritten(); // a push already belongs to a newer transcript than this response
          return;
        }
        const batch = p.items ?? [];
        const replace = !batch.length || Boolean(p.history && history.current && p.history !== history.current);
        setItems((have) => mergeItems(mergeItems(replace ? [] : have, batch), pendingItems));
        latest = mergeItems(mergeItems(replace ? [] : latest, batch), pendingItems);
        latestMore = Boolean(p.has_more);
        setHasMore(latestMore);
        if (p.history) history.current = p.history;
        save();
        showLive(batch);
        covered.current = Math.max(covered.current, p.to_seq ?? Math.max(0, ...batch.map((i) => i.seq)));
      } catch (e) {
        if (dead) return;
        // a session that is still starting has no transcript yet
        if (!opts.starting) setError(sessionLoadError(errorText(e)));
      } finally {
        if (!dead) setLoading(false);
        lastArrival = Date.now();
        ready = true;
      }
      if (!dead) subscribe();
    })();

    return () => {
      dead = true;
      ctl.abort();
      off();
      deltas.current.cancel();
      clearTimeout(gapTimer.current);
      gapTimer.current = undefined; // the next session's first gap starts its own
      clearInterval(staleTimer);
      document.removeEventListener("visibilitychange", checkStale);
      send({ type: "unsubscribe", machine_id: mid, session_id: sid });
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mid, sid, nonce, cacheScope, cacheManager, assistant]);

  const loadEarlier = useCallback(async () => {
    const first = itemsRef.current[0];
    if (!first || earlierInFlight.current || !hasMore || Date.now() < earlierRetryAt.current) return;
    const gen = generation.current;
    earlierInFlight.current = true;
    setLoadingEarlier(true);
    try {
      const p = await api<ItemsPage>(`${sessionPath(mid, sid)}/items?${PAGE}&before_seq=${first.seq}`);
      if (gen !== generation.current) return;
      apply(p.items ?? []);
      setHasMore(Boolean(p.has_more));
      earlierRetryAt.current = 0;
      earlierWait.current = 0;
    } catch (e) {
      if (gen !== generation.current) return;
      // Kept in state for the caller to show; the next try waits instead of firing on every scroll tick.
      earlierWait.current = Math.min(earlierWait.current ? earlierWait.current * 2 : EARLIER_RETRY_MS, EARLIER_RETRY_MAX_MS);
      earlierRetryAt.current = Date.now() + earlierWait.current;
      setError(sessionLoadError(errorText(e)));
    } finally {
      // a start-over already cleared both for the conversation shown now
      if (gen === generation.current) {
        earlierInFlight.current = false;
        setLoadingEarlier(false);
      }
    }
  }, [mid, sid, hasMore, apply]);

  const expandFold = useCallback(
    async (fold: Item) => {
      if (!fold.fold) return;
      const gen = generation.current;
      let from = fold.seq;
      const out: Item[] = [];
      for (let n = 0; n < 20; n++) {
        const p = await api<ItemsPage>(`${sessionPath(mid, sid)}/items?from_seq=${from}&through_seq=${fold.fold.through_seq}&lazy_images=1`);
        if (gen !== generation.current) return;
        out.push(...(p.items ?? []));
        if (!p.more || !p.items?.length) break;
        from = p.items[p.items.length - 1].seq + 1;
      }
      apply(out);
    },
    [mid, sid, apply],
  );

  return { items, loading, error, hasMore, loadingEarlier, loadEarlier, expandFold, streaming, notLive, reload: () => setNonce((x) => x + 1) };
}
