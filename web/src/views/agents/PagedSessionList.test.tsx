// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { api } from "../../lib/api";
import { SessionList, type ListCounts } from "../../lib/session-list";
import { STATE_LABEL } from "../../lib/state-labels";
import type { AgentState } from "../../lib/types";
import { SESSION_LIST_METRICS } from "../../ui/list-metrics";
import { PagedSessionList } from "./PagedSessionList";

vi.mock("../../lib/api", async (original) => ({ ...await original<typeof import("../../lib/api")>(), api: vi.fn() }));
vi.mock("../../lib/store", () => ({ onHubEvent: () => () => {} }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Row = { id: string };
const identity = (row: Row) => row.id;
const compare = (a: Row, b: Row) => a.id.localeCompare(b.id);
const initialState = { needs_you: 1, working: 2, unread: 3, idle: 4, offline: 5, unknown: 6 };
const counts = (state = initialState, revision = "initial"): ListCounts => ({
  total: Object.values(state).reduce((sum, n) => sum + n, 0), state, machine: {}, project: {}, group: {}, revision,
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

let host: HTMLDivElement;
let root: Root;
let list: SessionList<Row>;
const rootCounts = vi.fn<() => Promise<ListCounts>>();
const scopedCounts = vi.fn<() => Promise<ListCounts>>();
const chips = () => [...host.querySelectorAll<HTMLButtonElement>('[role="radio"]')];
const labels = () => chips().map((chip) => chip.textContent);
const expected = (state: Record<string, number>) => [t("paged-agents.all"), ...Object.entries(state)
  .filter(([, n]) => n > 0).map(([key, n]) => `${STATE_LABEL[key as AgentState]} ${n}`)];

beforeEach(() => {
  vi.useFakeTimers();
  rootCounts.mockReset().mockResolvedValue(counts());
  scopedCounts.mockReset().mockResolvedValue(counts());
  vi.mocked(api).mockImplementation((path) => {
    const params = new URL(path, "https://example.com").searchParams;
    return (params.has("counts") ? (params.has("q") ? scopedCounts() : rootCounts())
      : Promise.resolve({ sessions: [], remove: [], has_more: false, total: 21, revision: "rows" })) as ReturnType<typeof api>;
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  list = new SessionList<Row>("/sessions", identity, compare);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

async function show() {
  await list.load();
  await act(async () => root.render(<PagedSessionList root={list} path="/sessions" identity={identity} compare={compare}
    event="sessions_list" renderRow={(row) => <span>{row.id}</span>} />));
  expect(labels()).toEqual(expected(initialState));
}

async function search() {
  const input = host.querySelector<HTMLInputElement>(`input[aria-label="${t("paged-agents.search")}"]`)!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "review");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => { await vi.advanceTimersByTimeAsync(SESSION_LIST_METRICS.searchDelay); });
}

describe("paged state chips during count refreshes", () => {
  it("retains every chip and count while a new scope loads, then applies its successful counts", async () => {
    await show();
    const before = chips();
    const pending = deferred<ListCounts>();
    scopedCounts.mockReturnValue(pending.promise);
    await search();
    expect(scopedCounts).toHaveBeenCalled();
    expect(labels()).toEqual(expected(initialState));
    expect(chips()).toEqual(before);

    const next = { ...initialState, working: 0, unread: 7 };
    await act(async () => pending.resolve(counts(next, "scoped")));
    expect(labels()).toEqual(expected(next));
  });

  it("keeps scoped chips through root-count refreshes and failures until replacement counts arrive", async () => {
    await show();
    const scoped = { ...initialState, working: 8 };
    scopedCounts.mockResolvedValue(counts(scoped, "scoped"));
    await search();
    expect(labels()).toEqual(expected(scoped));
    const before = chips();

    const pending = deferred<ListCounts>();
    scopedCounts.mockReturnValue(pending.promise);
    rootCounts.mockResolvedValue(counts(initialState, "refresh"));
    await act(async () => { await list.load(); });
    expect(labels()).toEqual(expected(scoped));
    expect(chips()).toEqual(before);

    await act(async () => pending.reject(new Error("Counts unavailable")));
    expect(labels()).toEqual(expected(scoped));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("Counts unavailable");

    const next = { ...scoped, working: 9 };
    scopedCounts.mockResolvedValue(counts(next, "recovered"));
    rootCounts.mockResolvedValue(counts(initialState, "retry"));
    await act(async () => { await list.load(); });
    expect(labels()).toEqual(expected(next));
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("keeps unfiltered chips while the root rebaselines and accepts a successful empty count", async () => {
    await show();
    const before = chips();
    const pending = deferred<ListCounts>();
    rootCounts.mockReturnValue(pending.promise);
    let loading!: Promise<void>;
    await act(async () => { list.reset(); loading = list.load(); });
    expect(list.getSnapshot().counts).toBeNull();
    expect(labels()).toEqual(expected(initialState));
    expect(chips()).toEqual(before);

    await act(async () => {
      pending.resolve(counts({ needs_you: 0, working: 0, unread: 0, idle: 0, offline: 0, unknown: 0 }, "empty"));
      await loading;
    });
    expect(labels()).toEqual([t("paged-agents.all")]);
  });
});


describe("the By repo entry in the paged list", () => {
  it("offers By repo only when the caller has the full tree, and choosing it switches views instead of filtering", async () => {
    await act(async () => root.render(<PagedSessionList root={list} path="/sessions" identity={identity} compare={compare} event="sessions_list" renderRow={(row: Row) => <span>{row.id}</span>} />));
    const select = () => host.querySelector<HTMLSelectElement>(`select[aria-label="${t("paged-agents.group_by")}"]`)!;
    expect([...select().options].some((o) => o.value === "repo")).toBe(false);

    const onRepo = vi.fn();
    await act(async () => root.render(<PagedSessionList root={list} path="/sessions" identity={identity} compare={compare} event="sessions_list" renderRow={(row: Row) => <span>{row.id}</span>} onRepo={onRepo} />));
    const option = [...select().options].find((o) => o.value === "repo");
    expect(option?.textContent).toBe(t("lib-agent-groups.by_repo"));

    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
      setter.call(select(), "repo");
      select().dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(onRepo).toHaveBeenCalledTimes(1);
    // it did not become a paged filter: the list still shows the unfiltered, recently updated projection
    expect(select().value).toBe("");
  });
});
