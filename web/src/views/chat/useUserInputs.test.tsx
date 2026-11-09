// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useUserInputs } from "./useUserInputs";
import type { UserInputIndex } from "../../lib/user-input-index";

const pending = vi.hoisted(() => [] as { path: string; resolve: (value: unknown) => void; signal?: AbortSignal }[]);
vi.mock("../../lib/api", () => ({
  sessionPath: (mid: string, sid: string) => `/sessions/${mid}/${sid}`,
  api: (path: string, opts: { signal?: AbortSignal }) => new Promise((resolve) => pending.push({ path, resolve, signal: opts.signal })),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let destroy: (() => void) | undefined;
afterEach(() => { destroy?.(); destroy = undefined; pending.length = 0; vi.useRealTimers(); });
const answer = (history: string, total = 2): UserInputIndex => ({ total, history, inputs: Array.from({ length: total }, (_, i) => ({ id: `input-${i}#0`, seq: i + 1 })) });

describe("independent user-input metadata", () => {
  it("fences conversation/history changes and refreshes when a genuine input arrives", async () => {
    const host = document.createElement("div"); const root = createRoot(host);
    let got: UserInputIndex | null = null;
    const read = (): UserInputIndex | null => got;
    function Probe({ sid, history, newest }: { sid: string; history: string; newest: string }) { got = useUserInputs("example", sid, history, newest, true); return null; }
    const render = (sid: string, history: string, newest = "last") => act(() => root.render(<Probe sid={sid} history={history} newest={newest} />));
    destroy = () => act(() => root.unmount());
    render("a", "old"); render("b", "new");
    expect(pending[0].signal?.aborted).toBe(true);
    await act(async () => pending[0].resolve(answer("old")));
    expect(got).toBeNull();
    await act(async () => pending[1].resolve(answer("new")));
    expect(read()?.total).toBe(2);
    render("b", "new", "new-input");
    await act(async () => pending[2].resolve(answer("new", 3)));
    expect(read()?.total).toBe(3);
    render("b", "rewritten");
    expect(got).toBeNull();
    await act(async () => pending[3].resolve(answer("new", 3)));
    expect(got).toBeNull();
  });
  it("refreshes the transcript when the complete index discovers a different historical identity", async () => {
    const host = document.createElement("div"); const root = createRoot(host);
    const reload = vi.fn();
    function Probe() { useUserInputs("example", "synthetic", "tail-history", "last", true, reload); return null; }
    act(() => root.render(<Probe />)); destroy = () => act(() => root.unmount());
    await act(async () => pending[0].resolve(answer("complete-history")));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("retries unsupported or incomplete indexes instead of permanently freezing the tail count", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div"); const root = createRoot(host);
    let got: UserInputIndex | null = null;
    const read = (): UserInputIndex | null => got;
    function Probe() { got = useUserInputs("example", "synthetic", "fixture", "last", true); return null; }
    act(() => root.render(<Probe />)); destroy = () => act(() => root.unmount());
    await act(async () => pending[0].resolve({ items: [], has_more: false }));
    expect(got).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(pending).toHaveLength(2);
    await act(async () => pending[1].resolve(answer("fixture", 8)));
    expect(read()?.total).toBe(8);
  });

  it("does not read indexes on touch-only or unmounted chats", async () => {
    const host = document.createElement("div"); const root = createRoot(host);
    function Probe() { useUserInputs("example", "synthetic", "fixture", "last", false); return null; }
    act(() => root.render(<Probe />)); destroy = () => act(() => root.unmount());
    expect(pending).toHaveLength(0);
  });
});
