import { expect, test } from "bun:test";
import { connect, ExtError, withExactInit, withExactParams, type ExactCall, type Program } from "./index.ts";

function harness(program: Program) {
  const replies = new Map<string, (frame: any) => void>();
  const conn = connect(program, line => {
    const frame = JSON.parse(line);
    if (frame.id && !frame.method) replies.get(frame.id)?.(frame);
  });
  let sequence = 0;
  const call = (method: string, params: string) => new Promise<any>((resolve, reject) => {
    const id = `fixture-${++sequence}`;
    const deadline = setTimeout(() => reject(new Error("SDK initialization fixture timed out")), 1000);
    replies.set(id, frame => { clearTimeout(deadline); replies.delete(id); resolve(frame); });
    conn.line(`{"id":${JSON.stringify(id)},"method":${JSON.stringify(method)},"params":${params}}`);
  });
  return { conn, call };
}

const base = '"protocol":1,"hub":{"build":0},"plugin":{"id":"fixture","version":"1"},"granted":{}';

test("default initialization keeps parsed values, two arguments and receiver", async () => {
  const program: Program = {
    id: "fixture", version: "1",
    init: function(init, hub) {
      expect(this).toBe(program);
      expect(arguments.length).toBe(2);
      expect(init.info.id).toBe("fixture");
      expect(hub).toBe(h.conn.hub);
      expect(init.config).toEqual({ large: 9007199254740992, exponent: 1, n: 2, nil: null });
    },
  };
  const h = harness(program);
  try {
    const reply = await h.call("initialize", `{${base},"config":{"large":9007199254740993,"exponent":1e0,"n":"invalid","n":2,"nil":null}}`);
    expect(reply.result).toEqual({ protocol: 1, id: "fixture", version: "1" });
  } finally { h.conn.close(); }
});

test("nullish initializers keep optional-call behavior for own and inherited values", async () => {
  for (const inherited of [false, true]) for (const init of [undefined, null]) {
    const identity = { id: "fixture", version: "1" };
    const program = (inherited ? Object.assign(Object.create({ init }), identity) : { ...identity, init }) as Program;
    const h = harness(program);
    try {
      expect((await h.call("initialize", `{${base}}`)).error).toBeUndefined();
      expect((await h.call("ping", "{}")).error).toBeUndefined();
    } finally { h.conn.close(); }
  }
});

test("non-callable initializers fail without initializing and can retry after replacement", async () => {
  for (const inherited of [false, true]) for (const init of [false, 0, "", true, 1, "invalid", {}]) {
    const identity = { id: "fixture", version: "1" };
    const program = (inherited ? Object.assign(Object.create({ init }), identity) : { ...identity, init }) as unknown as Program;
    const h = harness(program);
    try {
      const failure = await h.call("initialize", `{${base}}`);
      expect(failure.result).toBeUndefined();
      expect(failure.error.message).toMatch(/init is not a function/);
      expect((await h.call("ping", "{}")).error.code).toBe("not_initialized");
      Object.defineProperty(program, "init", { value: function() { expect(this).toBe(program); expect(arguments.length).toBe(2); } });
      expect((await h.call("initialize", `{${base}}`)).error).toBeUndefined();
      expect((await h.call("ping", "{}")).error).toBeUndefined();
    } finally { h.conn.close(); }
  }
});

test("opted initialization retains lexical config and duplicate order without changing parsed Init", async () => {
  const config = '{"large":9223372036854775807,"exponent":1e0,"n":"invalid","n":2,"nil":null}';
  const params = `{${base},"config":${config}}`;
  const program: Program = {
    id: "fixture", version: "1",
    init: withExactInit(function(init, hub, call) {
      expect(this).toBe(program);
      expect(arguments.length).toBe(3);
      expect(hub).toBe(h.conn.hub);
      expect(init.config).toMatchObject({ exponent: 1, n: 2 });
      expect(call).toBeDefined();
      expect(Object.isFrozen(call)).toBe(true);
      expect(call!.rawParams).toBe(params);
      expect(call!.params?.field("config")?.raw).toBe(config);
      expect(call!.params?.field("config")?.field("large")?.raw).toBe("9223372036854775807");
      expect(call!.params?.field("config")?.field("exponent")?.raw).toBe("1e0");
      expect(call!.params?.field("config")?.field("nil")?.raw).toBe("null");
      return { fixture: true };
    }),
  };
  const h = harness(program);
  try { expect((await h.call("initialize", params)).result.contributes).toEqual({ fixture: true }); }
  finally { h.conn.close(); }
});

test("exact initialization distinguishes missing config and explicit null", async () => {
  for (const config of [undefined, "null"]) {
    const program: Program = {
      id: "fixture", version: "1",
      init: withExactInit((init, _hub, call) => {
        expect(call).toBeDefined();
        expect(Object.hasOwn(init, "config")).toBe(config !== undefined);
        expect(call!.params?.field("config")?.raw).toBe(config);
      }),
    };
    const h = harness(program);
    try {
      const params = `{${base}${config === undefined ? "" : `,"config":${config}`}}`;
      expect((await h.call("initialize", params)).error).toBeUndefined();
    } finally { h.conn.close(); }
  }
});

test("concurrent exact initialization keeps the first frame and executes once", async () => {
  let release!: () => void;
  let start!: () => void;
  const ready = new Promise<void>(resolve => { start = resolve; });
  const held = new Promise<void>(resolve => { release = resolve; });
  let calls = 0;
  let seen: ExactCall | undefined;
  const program: Program = {
    id: "fixture", version: "1",
    init: withExactInit(async (_init, _hub, call) => {
      calls++;
      seen = call;
      start();
      await held;
      return { fixture: true };
    }),
  };
  const h = harness(program);
  try {
    const first = h.call("initialize", `{${base},"config":{"n":1e0}}`);
    await ready;
    const second = h.call("initialize", `{${base},"config":{"n":2.0}}`);
    const ping = h.call("ping", "{}");
    release();
    const replies = await Promise.all([first, second, ping]);
    expect(calls).toBe(1);
    expect(seen?.params?.field("config")?.field("n")?.raw).toBe("1e0");
    expect(replies.map(reply => reply.error)).toEqual([undefined, undefined, undefined]);
    expect(replies[0].result.contributes).toEqual({ fixture: true });
    expect(replies[1].result.contributes).toEqual({ fixture: true });
  } finally { release(); h.conn.close(); }
});

test("exact initialization failure can retry with fresh metadata and unchanged protocol errors", async () => {
  const raws: string[] = [];
  const program: Program = {
    id: "fixture", version: "1",
    init: withExactInit((_init, _hub, call) => {
      expect(call).toBeDefined();
      raws.push(call!.params!.field("config")!.raw);
      if (raws.length === 1) throw new ExtError("fixture retry", "unavailable");
    }),
  };
  const h = harness(program);
  try {
    expect((await h.call("initialize", '{"protocol":9}')).error.code).toBe("wrong_protocol");
    expect(raws).toEqual([]);
    expect((await h.call("initialize", `{${base},"config":{"n":1e0}}`)).error).toEqual({ message: "fixture retry", code: "unavailable" });
    expect((await h.call("ping", "{}")).error.code).toBe("not_initialized");
    expect((await h.call("initialize", `{${base},"config":{"n":2.0}}`)).error).toBeUndefined();
    expect((await h.call("initialize", `{${base},"config":{"n":3}}`)).error).toBeUndefined();
    expect(raws).toEqual(['{"n":1e0}', '{"n":2.0}']);
  } finally { h.conn.close(); }
});

test("namespace exact opt-in does not opt an initializer in", async () => {
  const initializer = function(_init: any, _hub: any) { expect(arguments.length).toBe(2); };
  Object.defineProperty(initializer, Symbol.for("@persona/extension.exactParams"), { value: true });
  const program: Program = {
    id: "fixture", version: "1", init: initializer,
    namespaces: { fixture: withExactParams((_method, legacy, call) => ({ parsed: legacy.n, raw: call?.params?.field("n")?.raw })) },
  };
  const h = harness(program);
  try {
    expect((await h.call("initialize", `{${base}}`)).error).toBeUndefined();
    expect((await h.call("fixture.read", '{"n":1e0}')).result).toEqual({ parsed: 1, raw: "1e0" });
  } finally { h.conn.close(); }
});

test("inherited and accessor initializer markers leave default invocation unchanged", async () => {
  const marker = Symbol.for("@persona/extension.exactInitParams");
  for (const accessor of [false, true]) {
    const initializer = function(_init: any, _hub: any) { expect(arguments.length).toBe(2); };
    if (accessor) Object.defineProperty(initializer, marker, { get() { throw new Error("must not read marker getter"); } });
    else Object.setPrototypeOf(initializer, Object.create(Function.prototype, { [marker]: { value: true } }));
    const h = harness({ id: "fixture", version: "1", init: initializer });
    try { expect((await h.call("initialize", `{${base}}`)).error).toBeUndefined(); }
    finally { h.conn.close(); }
  }
});

test("initializer receiver and single getter lookup do not depend on an overridden call method", async () => {
  let lookups = 0;
  const initializer = function(this: Program, _init: any, _hub: any) { expect(this).toBe(program); expect(arguments.length).toBe(2); };
  Object.defineProperty(initializer, "call", { value: () => { throw new Error("must invoke the initializer itself"); } });
  const program: Program = { id: "fixture", version: "1" };
  Object.defineProperty(program, "init", { get() { lookups++; return initializer; } });
  const h = harness(program);
  try {
    expect((await h.call("initialize", `{${base}}`)).error).toBeUndefined();
    expect(lookups).toBe(1);
  } finally { h.conn.close(); }
});

test("direct exact-initializer calls keep their receiver and do not invent a raw frame", async () => {
  let seen: ExactCall | undefined;
  const program: Program = {
    id: "fixture", version: "1",
    init: withExactInit(function(_init, _hub, call) { expect(this).toBe(program); seen = call; }),
  };
  const h = harness(program);
  try {
    await program.init!.call(program, { protocol: 1, hub: { build: 0, extension_points: {} }, granted: {}, info: { id: "fixture", version: "1" } }, h.conn.hub);
    expect(seen).toBeUndefined();
  } finally { h.conn.close(); }
});
