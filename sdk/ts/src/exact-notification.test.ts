import { expect, test } from "bun:test";
import { connect, type ExactCall, type Handler, type Program } from "./index.ts";
import { deliveryProgram } from "./testdata/exact-program.ts";
import cases from "./testdata/exact-notification-cases.json";

const settleEvent = async () => { await Promise.resolve(); await Promise.resolve(); };

function harness(program: Program) {
  const frames: any[] = [];
  const conn = connect(program, text => frames.push(JSON.parse(text)));
  const initialize = async () => {
    conn.line('{"id":"init","method":"initialize","params":{"protocol":1}}');
    await conn.drain();
  };
  return { conn, frames, initialize };
}

for (const c of cases) {
  test(`exact raw notification Program dispatch: ${c.name}`, async () => {
    const seen: Array<{ method: string; call: ExactCall | undefined; values?: Record<string, bigint>; error?: any }> = [];
    const { conn, frames, initialize } = harness(deliveryProgram((method, _legacy, call) => {
      const entry: typeof seen[number] = { method, call };
      seen.push(entry);
      try { entry.values = call?.params?.int64Fields(["count", "frame"]); }
      catch (error) { entry.error = error; throw error; }
      // Notifications ignore even non-serializable return values, with no reply.
      return entry.values;
    }));
    await initialize();
    conn.line(`{"id":"not-a-request","event":"error_reports.deliver","params":${c.raw}}`);
    await settleEvent();
    expect(seen).toHaveLength(1);
    expect(seen[0].method).toBe("deliver");
    expect(seen[0].call?.rawParams).toBe(c.raw);
    expect(Object.isFrozen(seen[0].call)).toBe(true);
    if (c.error) expect(seen[0].error?.code).toBe("invalid_params");
    else expect(seen[0].values).toEqual({ count: BigInt(c.count!), frame: BigInt(c.frame!) });
    conn.line('{"id":"ping","method":"ping"}');
    await conn.drain();
    expect(frames.map(frame => frame.id)).toEqual(["init", "ping"]);
    await conn.shutdown();
  });
}

test("exact notifications keep missing/null and last folded RawMessage params distinct from legacy lookup", async () => {
  const seen: Array<{ raw: string | undefined; legacy: any }> = [];
  const { conn, frames, initialize } = harness(deliveryProgram((_method, legacy, call) => {
    expect(call).toBeDefined();
    seen.push({ raw: call?.rawParams, legacy });
  }));
  await initialize();
  for (const fields of [
    '', ',"params":null', ',"params":{"count":1.0},"pa\\u0072ams":{"count":9007199254740993}',
    ',"params":{"count":2},"PARAMS":null', ',"Params":{"frame":-9223372036854775808}',
  ]) conn.line(`{"event":"error_reports.deliver"${fields}}`);
  await settleEvent();
  expect(seen).toEqual([
    { raw: undefined, legacy: {} }, { raw: "null", legacy: {} },
    { raw: '{"count":9007199254740993}', legacy: { count: JSON.parse("9007199254740993") } },
    { raw: "null", legacy: { count: 2 } },
    { raw: '{"frame":-9223372036854775808}', legacy: {} },
  ]);
  expect(frames).toHaveLength(1);
  await conn.shutdown();
});

test("exact notification gating does not queue pre-init events or change request/event routing and legacy arity", async () => {
  let release!: () => void;
  const ready = new Promise<void>(resolve => { release = resolve; });
  const exactMethods: string[] = [];
  const legacyCalls: any[] = [];
  const program = deliveryProgram((method, legacy, call) => {
    exactMethods.push(method);
    expect(call?.rawParams).toBe('{"count":9007199254740993}');
    expect(legacy.count).toBe(JSON.parse("9007199254740993"));
    return {};
  });
  const legacy: Handler = function (method, params) { legacyCalls.push({ method, params, arity: arguments.length }); };
  program.namespaces!.legacy = legacy;
  program.init = () => ready;
  const { conn, frames, initialize } = harness(program);
  conn.line('{"event":"error_reports.before"}');
  const initialization = initialize();
  await settleEvent();
  conn.line('{"event":"error_reports.during"}');
  conn.line('{"id":"req","method":"error_reports.request","event":"error_reports.not-an-event","params":{"count":9007199254740993}}');
  await settleEvent();
  expect(exactMethods).toEqual([]);
  release();
  await initialization;
  await conn.drain();
  conn.line('{"event":"error_reports.deliver","params":{"count":9007199254740993}}');
  conn.line('{"event":"legacy.changed","params":{"count":9007199254740993}}');
  conn.line('{"event":"unknown.deliver"}');
  conn.line('{"event":"error_reports"}');
  conn.line('{"event":".deliver"}');
  conn.line('{"event":123}');
  conn.line('{"event":"error_reports.invalid","params":{"count":01}}');
  await settleEvent();
  expect(exactMethods).toEqual(["request", "deliver"]);
  expect(legacyCalls).toEqual([{ method: "changed", params: { count: JSON.parse("9007199254740993") }, arity: 2 }]);
  expect(frames.map(frame => frame.id)).toEqual(["init", "req"]);
  await conn.shutdown();
  conn.line('{"event":"error_reports.after-shutdown"}');
  await settleEvent();
  expect(exactMethods).toEqual(["request", "deliver"]);
});

test("exact notification raw-view bounds do not add a global legacy frame limit", async () => {
  let exactCalls = 0, legacyCalls = 0;
  const program = deliveryProgram(() => { exactCalls++; });
  program.namespaces!.legacy = () => { legacyCalls++; };
  const { conn, frames, initialize } = harness(program);
  await initialize();
  const params = `{"other":"${"x".repeat(8 << 20)}"}`;
  conn.line(`{"event":"error_reports.deliver","params":${params}}`);
  conn.line(`{"event":"legacy.deliver","params":${params}}`);
  await settleEvent();
  expect(exactCalls).toBe(0);
  expect(legacyCalls).toBe(1);
  expect(frames).toHaveLength(1);
  await conn.shutdown();
});
