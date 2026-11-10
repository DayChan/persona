import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { managerHeaderFor } from "./manager-choice";
import { applyMachine, dropAnswered, endInteraction, getState, hubHas, larkOn, putTodo, refresh, refreshAssistant, refreshTodos, reset, setSpacesSupported } from "./store";

describe("待办 store 与 404 兼容 (C2)", () => {
  beforeEach(() => {
    reset();
    vi.stubGlobal("localStorage", { getItem: () => "mock-token", setItem: () => {}, removeItem: () => {} });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    reset();
  });

  it("当 GET /api/v1/todos 返回 404 时，设 todosSupported=false，不报错", async () => {
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url.includes("/todos")) {
        return Promise.resolve(new Response(JSON.stringify({ error: "not found" }), { status: 404 }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetch);

    await expect(refresh()).resolves.not.toThrow();
    const st = getState();
    expect(st.todosSupported).toBe(false);
    expect(st.todosLoaded).toBe(true);
    expect(st.todos).toEqual([]);
  });

  it("refresh() 页面加载/重连时若旧 hub 返回无 spaces 的待办列表，spacesSupported 判为 false (#881 review)", async () => {
    const legacyTodos = [
      { id: "todo-legacy", title: "旧版待办", created_at: "", updated_at: "" },
    ];
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url.includes("/todos")) {
        return Promise.resolve(new Response(JSON.stringify({ todos: legacyTodos }), { status: 200 }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetch);

    await refresh();
    expect(getState().todosSupported).toBe(true);
    expect(getState().todosLoaded).toBe(true);
    expect(getState().spacesSupported).toBe(false);
  });

  it("当 GET /api/v1/todos 成功返回待办列表时，todosSupported=true 且存入 todos", async () => {
    const mockTodos = [
      { id: "todo-1", title: "测试待办", created_at: "2026-11-02T10:00:00Z", updated_at: "", spaces: [] },
    ];
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url.includes("/todos")) {
        return Promise.resolve(new Response(JSON.stringify({ todos: mockTodos }), { status: 200 }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetch);

    await refresh();
    const st = getState();
    expect(st.todosSupported).toBe(true);
    expect(st.todosLoaded).toBe(true);
    expect(st.spacesSupported).toBe(true);
    expect(st.todos).toHaveLength(1);
    expect(st.todos[0].id).toBe("todo-1");
  });

  it("refreshTodos 独立重拉待办，若 404 设 todosSupported=false 与 spacesSupported=false", async () => {
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url.includes("/todos")) {
        return Promise.resolve(new Response(JSON.stringify({ error: "not found" }), { status: 404 }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetch);

    await refreshTodos();
    expect(getState().todosSupported).toBe(false);
    expect(getState().spacesSupported).toBe(false);
  });

  it("支持主动标记 spacesSupported=false（例如收到 /spaces 404 时）(#874 item 1)", () => {
    expect(getState().spacesSupported).toBe(true);
    setSpacesSupported(false);
    expect(getState().spacesSupported).toBe(false);
  });

  it("spacesSupported 被置成 false 后，通过 refreshTodos 重新计算恢复为 true (#881 review)", async () => {
    setSpacesSupported(false);
    expect(getState().spacesSupported).toBe(false);

    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url.includes("/todos")) {
        return Promise.resolve(new Response(JSON.stringify({ todos: [] }), { status: 200 }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetch);

    await refreshTodos();
    expect(getState().spacesSupported).toBe(true);
  });

  it("当现代 hub 返回空待办列表时，spacesSupported 保持原样为 true，且 todosLoaded 为 true (#881 review)", async () => {
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url.includes("/todos")) {
        return Promise.resolve(new Response(JSON.stringify({ todos: [] }), { status: 200 }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetch);

    await refreshTodos();
    expect(getState().todosSupported).toBe(true);
    expect(getState().todosLoaded).toBe(true);
    expect(getState().spacesSupported).toBe(true);
  });

  it("当旧 hub 返回没有 spaces 属性的待办列表时，spacesSupported 判定为 false (#874 item 1)", async () => {
    const legacyTodos = [
      { id: "todo-legacy", title: "旧版待办", created_at: "", updated_at: "" },
    ];
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url.includes("/todos")) {
        return Promise.resolve(new Response(JSON.stringify({ todos: legacyTodos }), { status: 200 }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetch);

    await refreshTodos();
    expect(getState().todosSupported).toBe(true);
    expect(getState().spacesSupported).toBe(false);
  });
});

describe("hub features", () => {
  it("answers `unknown` until the hub names any", () => {
    expect(hubHas([], "lark", true)).toBe(true);
    expect(hubHas([], "connectors")).toBe(false);
    expect(hubHas(["terminal_stream"], "lark", true)).toBe(false);
    expect(hubHas(["terminal_stream", "lark"], "lark")).toBe(true);
  });

  it("has Feishu through the Lark gateway only with lark, as before while the hub has not said", () => {
    expect(larkOn({ features: [] })).toBe(true);
    expect(larkOn({ features: ["plugins", "lark_attachments", "lark_native_encryption"] })).toBe(false);
    expect(larkOn({ features: ["plugins", "lark", "lark_moments"] })).toBe(true);
  });
});

describe("待回答的卡片：刷新不复活已结束的卡片 (#1673)", () => {
  const ix = { id: "ix_q", machine_id: "m", session_id: "s", kind: "question" as const, questions: [{ question: "发吗？", options: [{ label: "发" }] }] };

  beforeEach(() => {
    reset();
    vi.stubGlobal("localStorage", { getItem: () => "mock-token", setItem: () => {}, removeItem: () => {} });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
    reset();
  });

  /** /interactions answers at once with `pending`; /sessions waits for release() (a busy hub takes seconds). */
  function slowSessions(pending: unknown[]) {
    let release = () => {};
    const held = new Promise<void>((r) => (release = r));
    vi.stubGlobal("fetch", vi.fn().mockImplementation(async (url: string) => {
      if (url.includes("/interactions")) return new Response(JSON.stringify({ interactions: pending }), { status: 200 });
      if (url.includes("/sessions")) await held;
      return new Response(JSON.stringify({}), { status: 200 });
    }));
    return () => release();
  }

  it("刷新途中回答的卡片，刷新回来后不再出现", async () => {
    let release = slowSessions([ix]);
    await (async () => {
      const p = refresh();
      release();
      await p;
    })();
    expect(getState().interactions.map((i) => i.id)).toEqual(["ix_q"]);

    release = slowSessions([ix]); // the snapshot still has it: asked for before the answer
    const p = refresh();
    await new Promise((r) => setTimeout(r, 0));
    endInteraction("ix_q", { ix, resolution: { answers: { "发吗？": "发" } } });
    expect(getState().interactions).toEqual([]);
    release();
    await p;
    expect(getState().interactions).toEqual([]);
    expect(getState().answered.ix_q?.resolution).toEqual({ answers: { "发吗？": "发" } });

    // a snapshot asked for after the answer is taken as it is
    release = slowSessions([]);
    const q = refresh();
    release();
    await q;
    expect(getState().interactions).toEqual([]);
  });

  it("已回答的卡片过一会儿收起，也可以手动收起", () => {
    vi.useFakeTimers();
    endInteraction("ix_q", { ix, resolution: { answers: { "发吗？": "发" } } });
    expect(Object.keys(getState().answered)).toEqual(["ix_q"]);
    vi.advanceTimersByTime(25_000);
    expect(getState().answered).toEqual({});

    endInteraction("ix_q", { ix, resolution: {} });
    dropAnswered("ix_q");
    expect(getState().answered).toEqual({});
  });

  it("没有答案（交回电脑、已过期）只收起卡片", () => {
    endInteraction("ix_q");
    expect(getState().answered).toEqual({});
  });
});

describe("applyMachine (PUT …/engine answers the machine)", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    reset();
  });

  it("takes the hub's answer over the listed machine and keeps what it does not say", async () => {
    reset();
    vi.stubGlobal("localStorage", { getItem: () => "mock-token", setItem: () => {}, removeItem: () => {} });
    const machines = [
      { id: "a", name: "A", online: true, engine: "herdr", hub_name: "家里" },
      { id: "b", name: "B", online: false },
    ];
    vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string) => Promise.resolve(new Response(JSON.stringify(url.includes("/machines") ? { machines } : {}), { status: 200 }))));
    await refresh();
    const b = getState().machines[1];
    applyMachine({ id: "a", name: "A", online: true, engine: "herdr-core" });
    expect(getState().machines[0]).toMatchObject({ engine: "herdr-core", hub_name: "家里" });
    expect(getState().machines[1]).toBe(b);
    applyMachine({ id: "gone", name: "?", online: false });
    expect(getState().machines.map((m) => m.id)).toEqual(["a", "b"]);
  });
});

describe("待办列表：先发的 /todos 晚回来不盖掉新的 (#3540)", () => {
  const todo = (id: string, title: string) => ({ id, title, created_at: "", updated_at: "", spaces: [] });
  /** Each /todos request waits until its turn is resolved; other paths answer at once. */
  function heldTodos() {
    const turns: ((list: unknown[]) => void)[] = [];
    vi.stubGlobal("fetch", vi.fn().mockImplementation(async (url: string) => {
      if (!url.includes("/todos")) return new Response(JSON.stringify({}), { status: 200 });
      const list = await new Promise<unknown[]>((r) => turns.push(r));
      return new Response(JSON.stringify({ todos: list }), { status: 200 });
    }));
    return turns;
  }
  const settle = () => new Promise((r) => setTimeout(r, 0));

  beforeEach(() => {
    reset();
    vi.stubGlobal("localStorage", { getItem: () => "mock-token", setItem: () => {}, removeItem: () => {} });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    reset();
  });

  it("refreshTodos: the later request's answer stays when the earlier one lands after it", async () => {
    const turns = heldTodos();
    const a = refreshTodos();
    const b = refreshTodos();
    await settle();
    expect(turns).toHaveLength(2);
    turns[1]([todo("t1", "done already")]);
    await b;
    turns[0]([todo("t1", "old title")]);
    await a;
    expect(getState().todos.map((t) => t.title)).toEqual(["done already"]);
  });

  it("a slow refresh() does not put back the list a later refreshTodos() already applied", async () => {
    const turns = heldTodos();
    const slow = refresh();
    const later = refreshTodos();
    await settle();
    turns[1]([todo("t1", "new"), todo("t2", "added")]);
    await later;
    turns[0]([todo("t1", "old")]);
    await slow;
    const st = getState();
    expect(st.loaded).toBe(true);
    expect(st.todos.map((t) => t.title)).toEqual(["new", "added"]);
  });

  it("confirmed completion fences older reads even when the follow-up read fails, but allows a later reopen", async () => {
    const reads: { resolve: (r: Response) => void; reject: (e: Error) => void }[] = [];
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((resolve, reject) => reads.push({ resolve, reject }))));
    const asked = { ...todo("t1", "Synthetic task"), ask: { outcome: "decide" as const, verdict: "Choose an option", asked_at: "2026-11-10T08:00:00Z" } };
    getState().todos = [asked];
    const old = refreshTodos();
    await settle();
    const done = { ...asked, done_at: "2026-11-10T09:00:00Z", ask: undefined };
    putTodo(done);
    const followup = refreshTodos();
    await settle();
    reads[0].resolve(new Response(JSON.stringify({ todos: [asked] }), { status: 200 }));
    await old;
    expect(getState().todos).toEqual([done]);
    reads[1].reject(new Error("Synthetic refresh failure"));
    await followup;
    expect(getState().todos).toEqual([done]);
    const reopened = { ...asked, ask: { ...asked.ask, asked_at: "2026-11-10T10:00:00Z" } };
    const later = refreshTodos();
    await settle();
    reads[2].resolve(new Response(JSON.stringify({ todos: [reopened] }), { status: 200 }));
    await later;
    expect(getState().todos).toEqual([reopened]);
  });

  it("an answer still on its way when the store is reset is dropped", async () => {
    const turns = heldTodos();
    const old = refreshTodos();
    await settle();
    reset();
    turns[0]([todo("t1", "the old login's")]);
    await old;
    expect(getState().todos).toEqual([]);
    expect(getState().todosLoaded).toBe(false);
  });

  it("answers that come back in order are all taken", async () => {
    const turns = heldTodos();
    const a = refreshTodos();
    await settle();
    turns[0]([todo("t1", "first")]);
    await a;
    expect(getState().todos.map((t) => t.title)).toEqual(["first"]);
    const b = refreshTodos();
    await settle();
    turns[1]([todo("t1", "second")]);
    await b;
    expect(getState().todos.map((t) => t.title)).toEqual(["second"]);
  });

  it("GET /api/v1/todos 返回携带 manager 的待办时，自动记录其 manager 归属", async () => {
    const managerTodos = [
      { id: "t-default", title: "默认待办", created_at: "", updated_at: "" },
      { id: "t-other", title: "其他管理者的待办", manager: "m-other", created_at: "", updated_at: "" },
    ];
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url.includes("/todos")) {
        return Promise.resolve(new Response(JSON.stringify({ todos: managerTodos }), { status: 200 }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetch);

    await refresh();
    expect(getState().todos).toHaveLength(2);
    expect(managerHeaderFor("/todos/t-other")).toBe("m-other");
    expect(managerHeaderFor("/todos/t-other/retry")).toBe("m-other");
    expect(managerHeaderFor("/todos/t-default")).toBeUndefined();
  });
});

describe("助手会话列表：先发的 /assistant 晚回来不盖掉新的 (#4136)", () => {
  const info = (main: string, ...ids: string[]) => ({ machine_id: "hub", main, conversations: ids.map((id) => ({ machine_id: "hub", session_id: id, title: id, status: "idle" })) });
  /** Each /assistant request waits until its turn is resolved; other paths answer at once. */
  function heldAssistant() {
    const turns: ((a: unknown) => void)[] = [];
    vi.stubGlobal("fetch", vi.fn().mockImplementation(async (url: string) => {
      if (!url.endsWith("/assistant")) return new Response(JSON.stringify({}), { status: 200 });
      const a = await new Promise<unknown>((r) => turns.push(r));
      return new Response(JSON.stringify(a), { status: 200 });
    }));
    return turns;
  }
  const settle = () => new Promise((r) => setTimeout(r, 0));
  const shown = () => ({ main: getState().assistant?.main, ids: getState().assistant?.conversations.map((c) => c.session_id) });

  beforeEach(() => {
    reset();
    vi.stubGlobal("localStorage", { getItem: () => "mock-token", setItem: () => {}, removeItem: () => {} });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    reset();
  });

  it("a slow refresh() does not put back the list a later refreshAssistant() already applied", async () => {
    const turns = heldAssistant();
    const slow = refresh();
    const later = refreshAssistant(); // a conversation was just started
    await settle();
    expect(turns).toHaveLength(2);
    turns[1](info("new-main", "new-main", "old-main"));
    await later;
    turns[0](info("old-main", "old-main"));
    await slow;
    expect(getState().loaded).toBe(true);
    expect(shown()).toEqual({ main: "new-main", ids: ["new-main", "old-main"] });
  });

  it("refreshAssistant: the later request's answer stays when the earlier one lands after it", async () => {
    const turns = heldAssistant();
    const a = refreshAssistant();
    const b = refreshAssistant();
    await settle();
    turns[1](info("m", "m", "archived-now"));
    await b;
    turns[0](info("m", "m"));
    await a;
    expect(shown()).toEqual({ main: "m", ids: ["m", "archived-now"] });
  });

  it("an answer still on its way when the store is reset is dropped", async () => {
    const turns = heldAssistant();
    const old = refreshAssistant();
    await settle();
    reset();
    turns[0](info("the old login's", "the old login's"));
    await old;
    expect(getState().assistant).toBeNull();
  });

  it("answers that come back in order are all taken", async () => {
    const turns = heldAssistant();
    const a = refreshAssistant();
    await settle();
    turns[0](info("first", "first"));
    await a;
    expect(shown()).toEqual({ main: "first", ids: ["first"] });
    const b = refresh();
    await settle();
    turns[1](info("second", "second", "first"));
    await b;
    expect(shown()).toEqual({ main: "second", ids: ["second", "first"] });
  });

  it("records assistantError on failure and clears it on success", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(async (url: string) => {
      if (url.endsWith("/assistant")) return new Response(JSON.stringify({ error: "Service Unavailable" }), { status: 503 });
      return new Response(JSON.stringify({}), { status: 200 });
    }));
    await expect(refreshAssistant()).rejects.toThrow();
    expect(getState().assistantError).toContain("Service Unavailable");
    expect(getState().assistant).toBeNull();

    vi.stubGlobal("fetch", vi.fn().mockImplementation(async (url: string) => {
      if (url.endsWith("/assistant")) return new Response(JSON.stringify(info("m1", "m1")), { status: 200 });
      return new Response(JSON.stringify({}), { status: 200 });
    }));
    await refreshAssistant();
    expect(getState().assistantError).toBeNull();
    expect(getState().assistant?.main).toBe("m1");
  });

  it("refresh() records assistantError when /assistant fails with 503", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(async (url: string) => {
      if (url.endsWith("/assistant")) return new Response(JSON.stringify({ error: "hub 503" }), { status: 503 });
      return new Response(JSON.stringify({}), { status: 200 });
    }));
    await refresh();
    expect(getState().loaded).toBe(true);
    expect(getState().assistantError).toContain("hub 503");
    expect(getState().assistant).toBeNull();
  });
});

describe("the hub withdrawing an answered card (#4877)", () => {
  afterEach(() => {
    vi.useRealTimers();
    reset();
  });

  it("marks it withdrawn and keeps it for its time", async () => {
    const { cardWithdrawn } = await import("./store");
    vi.useFakeTimers();
    const ix = { id: "ix_w", machine_id: "m", session_id: "s", kind: "permission" } as const;
    endInteraction("ix_w", { ix, resolution: { allow: true } });
    expect(getState().answered.ix_w?.withdrawn).toBeFalsy();
    cardWithdrawn("ix_w");
    expect(getState().answered.ix_w?.withdrawn).toBe(true);
    vi.advanceTimersByTime(25_000);
    expect(getState().answered).toEqual({});
  });

  it("knows a card the hub withdrew before the answer's reply came", async () => {
    const { cardWithdrawn } = await import("./store");
    const ix = { id: "ix_e", machine_id: "m", session_id: "s", kind: "permission" } as const;
    cardWithdrawn("ix_e");
    endInteraction("ix_e", { ix, resolution: { allow: true } });
    expect(getState().answered.ix_e?.withdrawn).toBe(true);
  });

  it("knows an already-answered card is withdrawn", () => {
    const ix = { id: "ix_a", machine_id: "m", session_id: "s", kind: "permission" } as const;
    endInteraction("ix_a", { ix, resolution: { allow: true }, already: true });
    expect(getState().answered.ix_a?.withdrawn).toBe(true);
  });
});
