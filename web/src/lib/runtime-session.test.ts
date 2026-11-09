import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "./api";
import { exactSession, SessionDetailLoader, UnsupportedSessionLookup, type DetailState } from "./runtime-session";
import type { Session } from "./types";

vi.mock("./api", async (original) => ({ ...await original<typeof import("./api")>(), api: vi.fn() }));
const row = (title = "Full session"): Session => ({ machine_id: "demo", session_id: "row/with/slashes", title, cwd: "/demo", status: "idle", state: "idle", background: [{ id: "bg", kind: "shell" }], subagents: [], workflows: [], queued: ["Queued demo instruction"], permission_mode: "default", effort: "high" });
const page = (sessions: Session[] = [row()]) => ({ sessions, remove: [], has_more: false, revision: "checkpoint", total: sessions.length });
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done; }); return { promise, resolve }; };
const tick = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
let states: DetailState[];
let commit: ReturnType<typeof vi.fn<(row: Session) => void>>;
const loader = (lookup = true, cached: Session | null = row()) => new SessionDetailLoader("demo", "row/with/slashes", () => lookup, () => cached ?? undefined, commit, (state) => states.push(state));
beforeEach(() => { vi.clearAllMocks(); states = []; commit = vi.fn(); vi.mocked(api).mockResolvedValue(page()); });

describe("exact full-session query", () => {
  it("uses exact machine/session identity and full projection without cursor/since", async () => {
    const full = await exactSession("demo", "row/with/slashes");
    expect(full).toEqual(row());
    const params = new URL(vi.mocked(api).mock.calls[0][0], "http://demo").searchParams;
    expect(Object.fromEntries(params)).toEqual({ summary: "0", limit: "50", machine_id: "demo", session_id: "row/with/slashes" });
  });
  it("returns missing without substituting an unrelated row", async () => {
    vi.mocked(api).mockResolvedValueOnce(page([]));
    expect(await exactSession("demo", "row/with/slashes")).toBeUndefined();
    vi.mocked(api).mockResolvedValueOnce(page([{ ...row(), session_id: "other" }]));
    await expect(exactSession("demo", "row/with/slashes")).rejects.toBeInstanceOf(UnsupportedSessionLookup);
  });
  it("rejects a hub ignoring the parameter and never traverses its cursor", async () => {
    vi.mocked(api).mockResolvedValueOnce({ ...page([row(), row("other")]), has_more: true, next_cursor: "unrelated", total: 8000 });
    await expect(exactSession("demo", "row/with/slashes")).rejects.toBeInstanceOf(UnsupportedSessionLookup);
    expect(api).toHaveBeenCalledTimes(1);
  });
  it("does not treat authorization failures as a compatibility fallback", async () => {
    const error = new ApiError(403, "Forbidden", { code: "forbidden" });
    vi.mocked(api).mockRejectedValueOnce(error);
    await expect(exactSession("demo", "row/with/slashes")).rejects.toBe(error);
  });
});

describe("a failed background read", () => {
  it("keeps its error while reading again, retries with backoff and clears on an answer", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(api).mockRejectedValueOnce(new Error("first failure")).mockRejectedValueOnce(new Error("second failure"));
      const model = loader();
      await model.load();
      expect(states.at(-1)).toMatchObject({ loading: false, error: "first failure" });
      expect(api).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(1999);
      expect(api).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(1);
      expect(api).toHaveBeenCalledTimes(2);
      expect(states.filter((s) => s.loading).at(-1)?.error).toBe("first failure");
      expect(states.at(-1)?.error).toBe("second failure");
      await vi.advanceTimersByTimeAsync(4000);
      expect(api).toHaveBeenCalledTimes(3);
      expect(states.at(-1)).toMatchObject({ loading: false, error: "" });
      await vi.advanceTimersByTimeAsync(120_000);
      expect(api).toHaveBeenCalledTimes(3);
    } finally { vi.useRealTimers(); }
  });
  it("stops retrying when the chat goes away", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(api).mockRejectedValueOnce(new Error("failure"));
      const model = loader();
      await model.load();
      model.stop();
      await vi.advanceTimersByTimeAsync(120_000);
      expect(api).toHaveBeenCalledTimes(1);
    } finally { vi.useRealTimers(); }
  });
});

describe("active-chat snapshot lifecycle", () => {
  it("loads all full task/configuration fields on initial entry and invalidation", async () => {
    const model = loader();
    await model.load();
    expect(commit).toHaveBeenLastCalledWith(row());
    vi.mocked(api).mockResolvedValueOnce(page([{ ...row("Updated title"), background: undefined, subagents: undefined, workflows: undefined, queued: undefined }]));
    await model.load();
    expect(commit.mock.calls[1][0].background).toBeUndefined();
    expect(commit.mock.calls[1][0].queued).toBeUndefined();
    expect(states.at(-1)?.loading).toBe(false);
  });
  it("coalesces a burst into one in-flight and one trailing query", async () => {
    const pending = deferred<ReturnType<typeof page>>();
    vi.mocked(api).mockReturnValueOnce(pending.promise);
    const model = loader();
    const first = model.load();
    await tick();
    expect(model.load()).toBe(first);
    expect(model.load()).toBe(first);
    pending.resolve(page());
    await first;
    expect(api).toHaveBeenCalledTimes(2);
    expect(commit).toHaveBeenCalledTimes(2);
  });
  it("keeps existing data on missing or failed lookup", async () => {
    const model = loader();
    vi.mocked(api).mockResolvedValueOnce(page([]));
    await model.load();
    expect(commit).not.toHaveBeenCalled();
    expect(states.at(-1)?.missing).toBe(true);
    vi.mocked(api).mockRejectedValueOnce(new Error("Temporary failure"));
    await model.load();
    expect(commit).not.toHaveBeenCalled();
    expect(states.at(-1)?.error).toBe("Temporary failure");
  });
  it("drops a previous chat's response after unmount", async () => {
    const pending = deferred<ReturnType<typeof page>>();
    vi.mocked(api).mockReturnValueOnce(pending.promise);
    const model = loader();
    const task = model.load();
    await tick();
    model.stop();
    pending.resolve(page());
    await task;
    expect(commit).not.toHaveBeenCalled();
  });
  it("reconnect restarts the generation and ignores an old epoch response", async () => {
    const pending = deferred<ReturnType<typeof page>>();
    vi.mocked(api).mockReturnValueOnce(pending.promise).mockResolvedValueOnce(page([row("New epoch")]));
    const model = loader();
    const old = model.load();
    await tick();
    await model.restart();
    pending.resolve(page([row("Stale epoch")]));
    await old;
    expect(commit).toHaveBeenCalledTimes(1);
    expect(commit.mock.calls[0][0].title).toBe("New epoch");
  });
  it("a stop from a state subscriber prevents launching the request", async () => {
    let model: SessionDetailLoader;
    model = new SessionDetailLoader("demo", "row/with/slashes", () => true, () => row(), commit, () => model.stop());
    await model.load();
    expect(api).not.toHaveBeenCalled();
  });
});

describe("older-hub fallback never reloads the whole collection", () => {
  it("uses the existing complete cache without any lookup request", async () => {
    await loader(false).load();
    expect(api).not.toHaveBeenCalled();
    expect(states.at(-1)?.legacy).toBe(true);
  });
  it("uses only the existing per-session info route for an unlisted old chat", async () => {
    await loader(false, null).load();
    expect(vi.mocked(api).mock.calls.map(([path]) => path)).toEqual(["/sessions/demo/row%2Fwith%2Fslashes/info"]);
    expect(commit).not.toHaveBeenCalled();
  });
  it("retains cached details when an old hub ignores session_id", async () => {
    vi.mocked(api).mockResolvedValueOnce({ ...page([row(), row("Unrelated")]), total: 8000, has_more: true, next_cursor: "bad" });
    await loader().load();
    expect(api).toHaveBeenCalledTimes(1);
    expect(commit).not.toHaveBeenCalled();
    expect(states.at(-1)?.legacy).toBe(true);
  });
  it("retains cache on unsupported lookup without requesting the complete list", async () => {
    vi.mocked(api).mockRejectedValueOnce(new ApiError(404, "Unsupported", { code: "unsupported_feature" }));
    await loader().load();
    expect(api).toHaveBeenCalledTimes(1);
    expect(commit).not.toHaveBeenCalled();
    expect(states.at(-1)?.legacy).toBe(true);
  });
  it("never reads info or legacy lists after a permission failure", async () => {
    vi.mocked(api).mockRejectedValueOnce(new ApiError(403, "Forbidden", { code: "forbidden" }));
    await loader().load();
    expect(api).toHaveBeenCalledTimes(1);
    expect(states.at(-1)?.legacy).toBe(false);
    expect(states.at(-1)?.error).toBeTruthy();
  });
  it("reads the latest capability after a downgrade/reconnect", async () => {
    let supported = true;
    const pending = deferred<ReturnType<typeof page>>();
    vi.mocked(api).mockReturnValueOnce(pending.promise);
    const model = new SessionDetailLoader("demo", "row/with/slashes", () => supported, () => row(), commit, (state) => states.push(state));
    const old = model.load();
    await tick();
    supported = false;
    await model.restart();
    pending.resolve(page());
    await old;
    expect(api).toHaveBeenCalledTimes(1);
    expect(commit).not.toHaveBeenCalled();
    expect(states.at(-1)?.legacy).toBe(true);
  });
});
