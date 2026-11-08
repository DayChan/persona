// Bounded collection views: filters/counts belong to the server; only the loaded prefix lives in React.
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { t } from "../../i18n";
import { api, errorText } from "../../lib/api";
import { onHubEvent } from "../../lib/store";
import { SessionList, type ListCounts, type ListFilters } from "../../lib/session-list";
import { SESSION_LIST_METRICS } from "../../ui/list-metrics";
import { SearchField } from "../../ui/SearchField";
import { STATE_LABEL } from "../../lib/state-labels";
import type { AgentState } from "../../lib/types";
import { Button } from "../../ui/atoms";

export interface PagedSessionListProps<T> {
  root: SessionList<T>;
  path: string;
  identity: (row: T) => string;
  compare: (a: T, b: T) => number;
  removeIdentity?: (id: string) => string;
  event: "sessions_list" | "agents";
  renderRow: (row: T) => ReactNode;
  personal?: boolean;
  label?: (dimension: string, value: string) => string;
  /** "By repo" is the full, client-side tree (Sidebar's By repo), not a paged filter: offered only when the caller has it. */
  onRepo?: () => void;
}

function scrollParent(el: HTMLElement | null): HTMLElement | null {
  for (let p = el?.parentElement; p; p = p.parentElement) {
    if (/auto|scroll/.test(getComputedStyle(p).overflowY)) return p;
  }
  return null;
}

export function PagedSessionList<T>({ root, path, identity, compare, removeIdentity, event, renderRow, personal = false, label, onRepo }: PagedSessionListProps<T>) {
  const [query, setQuery] = useState("");
  const [q, setQ] = useState("");
  const [state, setState] = useState("");
  const [dimension, setDimension] = useState<"machine" | "project" | "group" | "">("");
  const [value, setValue] = useState("");
  const [groupQuery, setGroupQuery] = useState("");
  const groupOptionsId = useId();
  const [machine, setMachine] = useState("");
  const [provider, setProvider] = useState("");
  const [providerQuery, setProviderQuery] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => { setQ(query.trim()); setProvider(providerQuery.trim()); setValue(groupQuery.trim()); }, SESSION_LIST_METRICS.searchDelay);
    return () => clearTimeout(timer);
  }, [query, providerQuery, groupQuery]);
  const filters: ListFilters = useMemo(() => ({
    ...(q ? { q } : {}), ...(state ? { state } : {}), ...(provider ? { provider } : {}),
    ...(dimension && value ? { [dimension === "machine" ? "machine_id" : dimension]: value } : {}),
    ...(dimension === "group" && machine ? { machine_id: machine } : {}),
  }), [q, state, dimension, value, machine, provider]);
  const filtered = Object.keys(filters).length > 0;
  const summaryMode = root.isSummary();
  const list = useMemo(() => filtered ? new SessionList<T>(path, identity, compare, undefined, removeIdentity, { summary: summaryMode }) : root, [filtered, path, identity, compare, removeIdentity, summaryMode, root, filters]);
  const page = useSyncExternalStore(list.subscribe, list.getSnapshot, list.getSnapshot);
  const [facets, setFacets] = useState<ListCounts | null>(null);
  const [facetError, setFacetError] = useState("");
  const [scopeCounts, setScopeCounts] = useState<ListCounts | null>(null);
  const [scopeError, setScopeError] = useState("");
  const scopeQuery = useMemo(() => {
    const params = new URLSearchParams({ counts: "1", ...filters });
    params.delete("state");
    return params.toString();
  }, [filters]);
  const rootPage = useSyncExternalStore(root.subscribe, root.getSnapshot, root.getSnapshot);
  // Counts without state keep other state tabs reachable; machine scopes disambiguate computer-local groups.
  const facetFilters = useMemo(() => ({ ...(q ? { q } : {}), ...(provider ? { provider } : {}), ...(machine && dimension === "group" ? { machine_id: machine } : {}) }), [q, provider, machine, dimension]);
  useEffect(() => {
    let live = true;
    let sequence = 0;
    const controller = new AbortController();
    const read = () => {
      const request = ++sequence;
      if (!Object.keys(facetFilters).length) {
        setFacets(root.getSnapshot().counts);
        setFacetError("");
        return;
      }
      const params = new URLSearchParams({ counts: "1", ...facetFilters });
      void api<ListCounts>(`${path}?${params}`, { signal: controller.signal }).then((counts) => {
        if (live && request === sequence && counts.state && counts.group && counts.project && counts.machine) { setFacets(counts); setFacetError(""); }
      }, (e) => { if (live && request === sequence) setFacetError(errorText(e)); });
    };
    read();
    const off = onHubEvent(event, read);
    return () => { live = false; controller.abort(); off(); };
  }, [path, event, root, rootPage.counts, facetFilters]);
  useEffect(() => {
    // Keep the last counts while a refresh or a new scope is loading, including a root rebaseline.
    if (scopeQuery === "counts=1") {
      if (rootPage.counts) setScopeCounts(rootPage.counts);
      setScopeError("");
      return;
    }
    let sequence = 0;
    const abort = new AbortController();
    const read = () => {
      const request = ++sequence;
      void api<ListCounts>(`${path}?${scopeQuery}`, { signal: abort.signal }).then((counts) => {
        if (request === sequence && counts.state) { setScopeCounts(counts); setScopeError(""); }
      }, (e) => { if (request === sequence) setScopeError(errorText(e)); });
    };
    read();
    const off = onHubEvent(event, read);
    return () => { sequence++; abort.abort(); off(); };
  }, [scopeQuery, path, event, rootPage.counts]);
  useEffect(() => {
    if (filtered) void list.setFilters(filters).catch(() => {});
    else if (!list.getSnapshot().loaded) void list.load().catch(() => {});
    const off = filtered ? onHubEvent(event, () => list.invalidate()) : () => {};
    return () => { off(); if (filtered) list.reset(); };
  }, [list, filters, filtered, event]);

  const container = useRef<HTMLDivElement>(null);
  const [margin, setMargin] = useState(0);
  const getScrollElement = useCallback(() => scrollParent(container.current), []);
  const virtual = useVirtualizer({
    count: page.rows.length,
    getScrollElement,
    estimateSize: () => SESSION_LIST_METRICS.estimate,
    getItemKey: (i) => identity(page.rows[i]),
    overscan: SESSION_LIST_METRICS.overscan,
    scrollMargin: margin,
  });
  useLayoutEffect(() => {
    const el = container.current;
    const parent = getScrollElement();
    if (!el || !parent) return;
    const measure = () => setMargin(el.getBoundingClientRect().top - parent.getBoundingClientRect().top + parent.scrollTop);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(parent);
    if (el.parentElement) ro.observe(el.parentElement);
    return () => ro.disconnect();
  }, [getScrollElement, page.loaded, dimension, facetError]);
  const totalSize = virtual.getTotalSize();
  const scrolled = useRef(false);
  useEffect(() => {
    scrolled.current = false;
    const parent = getScrollElement();
    if (parent) parent.scrollTop = 0;
  }, [list, getScrollElement]);
  useEffect(() => {
    const parent = getScrollElement();
    if (!parent) return;
    const maybeMore = () => {
      if (scrolled.current && page.hasMore && !page.loading && !page.error &&
          parent.scrollTop + parent.clientHeight >= margin + totalSize - SESSION_LIST_METRICS.overscan * SESSION_LIST_METRICS.estimate) {
        void list.more().catch(() => {});
      }
    };
    const scroll = () => { scrolled.current = true; maybeMore(); };
    parent.addEventListener("scroll", scroll, { passive: true });
    maybeMore();
    return () => parent.removeEventListener("scroll", scroll);
  }, [list, getScrollElement, page.hasMore, page.loading, page.error, margin, totalSize]);

  const counts = facets ?? rootPage.counts;
  const stateTotals = scopeCounts?.state ?? {};
  const groups = useMemo(() => dimension ? Object.entries(counts?.[dimension] ?? {}).filter(([key]) => key && key.toLowerCase().includes(groupQuery.toLowerCase())).sort(([a], [b]) => a.localeCompare(b)).slice(0, SESSION_LIST_METRICS.groupOptions) : [], [dimension, counts, groupQuery]);
  const states = personal ? ["unread", "idle"] : ["needs_you", "working", "unread", "idle", "offline", "unknown"];
  const control = "relative z-10 min-w-0 rounded-control bg-inset px-2 py-1 text-xs text-ink shadow-hairline";
  return (
    <div data-paged-sessions={personal ? "personal" : "runtime"} data-loaded-sessions={page.rows.length} data-has-more={page.hasMore}>
      <div className="space-y-2 px-1 py-2">
        <SearchField value={query} onChange={setQuery} placeholder={t("paged-agents.search")} />
        <div role="radiogroup" aria-label={t("sidebar.quick_filters")} className="flex flex-wrap gap-1">
          {["", ...states.filter((s) => (stateTotals[s] ?? 0) > 0 || state === s)].map((s) => (
            <button key={s} type="button" role="radio" aria-checked={state === s} onClick={() => setState(s)} className={`${control} whitespace-nowrap ${state === s ? "bg-surface font-medium" : "text-ink-3"}`}>
              {s ? STATE_LABEL[s as AgentState] : t("paged-agents.all")}{s && scopeCounts ? ` ${stateTotals[s] ?? 0}` : ""}
            </button>
          ))}
        </div>
        <div className="flex flex-wrap gap-2">
          <select
            aria-label={t("paged-agents.group_by")}
            className={control}
            value={dimension}
            onChange={(e) => {
              // The repo tree cannot be a paged filter (it needs every session): it switches to the full By repo view.
              if (e.target.value === "repo") { onRepo?.(); return; }
              setDimension(e.target.value as typeof dimension); setValue(""); setGroupQuery(""); setMachine("");
            }}
          >
            <option value="">{t("paged-agents.recent")}</option>
            {!personal && <option value="machine">{t("paged-agents.machine")}</option>}
            {!personal && <option value="project">{t("paged-agents.project")}</option>}
            <option value="group">{t(personal ? "paged-agents.account" : "paged-agents.group")}</option>
            {onRepo && <option value="repo">{t("lib-agent-groups.by_repo")}</option>}
          </select>
          {dimension === "group" && !personal && <select className={control} aria-label={t("paged-agents.machine")} value={machine} onChange={(e) => { setMachine(e.target.value); setValue(""); setGroupQuery(""); }}>
            <option value="">{t("paged-agents.all_machines")}</option>
            {Object.entries(rootPage.counts?.machine ?? {}).filter(([id]) => id).map(([id, n]) => <option key={id} value={id}>{label?.("machine", id) ?? id} ({n})</option>)}
          </select>}
          {dimension && <>
            <input aria-label={t("paged-agents.group_filter")} placeholder={t("paged-agents.all")} list={groupOptionsId} className={`${control} max-w-full flex-1`} value={groupQuery} onChange={(e) => setGroupQuery(e.target.value)} />
            <datalist id={groupOptionsId}>{groups.map(([key, n]) => <option key={key} value={key}>{label?.(dimension, key) ?? key} ({n})</option>)}</datalist>
          </>}
          {personal && <input className={control} aria-label={t("paged-agents.provider")} placeholder={t("paged-agents.provider")} value={providerQuery} onChange={(e) => setProviderQuery(e.target.value)} />}
        </div>
        <p className="text-xs text-ink-3" data-session-total>{t("paged-agents.count", { count: page.total })}</p>
      </div>
      {(facetError || scopeError) && <p role="alert" className="px-2 text-xs text-red">{facetError || scopeError}</p>}
      <div ref={container} data-virtual-sessions className="relative" style={{ height: totalSize }}>
        {virtual.getVirtualItems().map((item) => (
          <div key={item.key} ref={virtual.measureElement} data-index={item.index} data-session-row className="absolute top-0 left-0 w-full" style={{ transform: `translateY(${item.start - margin}px)` }}>
            {renderRow(page.rows[item.index])}
          </div>
        ))}
      </div>
      {page.loading && <p role="status" className="px-2 py-2 text-xs text-ink-3">{t("sidebar.loading")}</p>}
      {page.error && <div className="px-2 py-2"><p role="alert" className="text-xs text-red">{page.error}</p><Button onClick={() => void list.retry().catch(() => {})}>{t("sidebar.retry")}</Button></div>}
      {page.loaded && !page.loading && !page.error && !page.rows.length && <p className="px-2 py-2 text-xs text-ink-3">{t("sidebar.no_match")}</p>}
      {page.hasMore && !page.loading && !page.error && <Button onClick={() => void list.more().catch(() => {})}>{t("paged-agents.more")}</Button>}
    </div>
  );
}
