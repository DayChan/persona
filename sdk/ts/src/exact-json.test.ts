import { expect, test } from "bun:test";
import { connect, ExactJSON, JSONIntegerError, withExactParams, type Handler, type Program, type ExactCall } from "./index.ts";
import defaultProgram from "./testdata/exact-program.ts";
import cases from "./testdata/int64-cases.json";

// Compiled by the actual SDK typecheck, never invoked at runtime.
function typeContracts(call: ExactCall) {
  const legacy: Handler = (_method, params) => params;
  const opted: Handler = withExactParams((_method, _params, metadata) => metadata?.params?.int64());
  // @ts-expect-error A required third parameter is not a legacy Handler; wrap explicitly.
  const implicit: Handler = (_method: string, _params: any, _call: ExactCall) => {};
  // @ts-expect-error Exact integer values cannot be mistaken for Number.
  const number: number = call.params!.int64();
  // @ts-expect-error Exact call metadata is read-only.
  call.rawParams = "1";
  // @ts-expect-error Validated raw views cannot be constructed directly.
  const unchecked = new ExactJSON("{");
  void [legacy, opted, implicit, number, unchecked];
}
void typeContracts;

function harness(program: Program = defaultProgram) {
  const frames: any[] = [];
  const conn = connect(program, text => frames.push(JSON.parse(text)));
  const rawCall = async (raw: string) => {
    conn.line(raw);
    await conn.drain();
    return frames.at(-1);
  };
  return { conn, frames, rawCall };
}

for (const c of cases) {
  test(`exact int64 raw default Program dispatch: ${c.name}`, async () => {
    const { conn, rawCall } = harness();
    await rawCall('{"id":"init","method":"initialize","params":{"protocol":1}}');
    const frame = await rawCall(`{"id":"case","method":"connector.tools.call","params":{"name":"example_topic","args":${c.raw}}}`);
    if (c.error) expect(frame.error.code).toBe("invalid_params");
    else expect(frame.result).toEqual({ topic: c.topic, replies: c.replies, raw: c.raw.trim() });
    await conn.shutdown();
  });
}

test("the opt-in leaves the two-argument Handler, legacy Number and initialization unchanged", async () => {
  const arities: number[] = [];
  let exactCalls = 0;
  let eventCall: unknown = "not called";
  const legacy: Handler = function (_method, params) { arities.push(arguments.length); return params; };
  const { conn, rawCall } = harness({ id: "example", version: "1", namespaces: {
    legacy,
    exact: withExactParams((_method, params, call) => { exactCalls++; eventCall = call; return params; }),
  } });
  expect((await rawCall('{"id":"pre","method":"exact.get","params":{"n":1.0}}')).error.code).toBe("not_initialized");
  expect(exactCalls).toBe(0);
  await rawCall('{"id":"init","method":"initialize","params":{"protocol":1}}');
  for (const raw of ["1.0", "1e0", "1.0000000000000001", "9007199254740993", "9223372036854775807"]) {
    expect((await rawCall(`{"id":"legacy","method":"legacy.get","params":{"n":${raw}}}`)).result.n).toBe(JSON.parse(raw));
    expect((await rawCall(`{"id":"exact","method":"exact.get","params":{"n":${raw}}}`)).result.n).toBe(JSON.parse(raw));
  }
  expect(arities).toEqual([2, 2, 2, 2, 2]);
  conn.line('{"event":"exact.changed","params":{"n":1.0}}');
  await Promise.resolve(); await Promise.resolve();
  expect(eventCall).toMatchObject({ rawParams: '{"n":1.0}' });
  await conn.shutdown();
});

test("raw params and args extraction preserves escaped keys, duplicate replacement, null and absence", async () => {
  const seen: Array<string | undefined> = [];
  const { conn, rawCall } = harness({ id: "example", version: "1", namespaces: {
    exact: withExactParams((_method, _params, call) => { seen.push(call?.rawParams); return {}; }),
  } });
  await rawCall('{"id":"init","method":"initialize","params":{"protocol":1}}');
  await rawCall('{"id":"a","method":"exact.get","params":{"n":1.0},"pa\\u0072ams":{"args":9223372036854775807}}');
  await rawCall('{"id":"b","method":"exact.get","params":{},"params":null}');
  await rawCall('{"id":"c","method":"exact.get"}');
  await rawCall('{"id":"d","method":"exact.get","params":{"n":1.0},"PARAMS":null}');
  await rawCall('{"id":"e","method":"exact.get","Params":{"args":{"topic":6}}}');
  expect(seen).toEqual(['{"args":9223372036854775807}', "null", undefined, "null", '{"args":{"topic":6}}']);
  expect(ExactJSON.parse('{"args":{"topic":1.0},"a\\u0072gs":{"topic":6}}').member("args")?.int64Fields(["topic"]).topic).toBe(6n);
  expect(ExactJSON.parse('{"args":{"topic":6},"args":null}').member("args")?.raw).toBe("null");
  expect(ExactJSON.parse("null").member("args")).toBeUndefined();
  expect([...ExactJSON.parse('{"topic":1.0,"TOPIC":6,"to\\u0070ic":null}').fieldValues("topic")].map(value => value.raw)).toEqual(["1.0", "6", "null"]);
  expect(ExactJSON.parse('{"\\u212Aey":6,"\\u00df":7}').int64Fields(["key", "SS"])).toEqual({ key: 6n, SS: 0n });
  expect(() => ExactJSON.parse("[]").member("args")).toThrow(JSONIntegerError);
  await conn.shutdown();
});

test("invalid JSON numeric lexemes never dispatch to exact or legacy namespaces", async () => {
  let calls = 0;
  const { conn, frames, rawCall } = harness({ id: "example", version: "1", namespaces: {
    exact: withExactParams(() => { calls++; }), legacy: () => { calls++; },
  } });
  await rawCall('{"id":"init","method":"initialize","params":{"protocol":1}}');
  for (const ns of ["exact", "legacy"]) {
    for (const token of ["01", "+1", "1.", ".1", "1e", "NaN", "Infinity", "-01", "--1"]) {
      conn.line(`{"id":"bad","method":"${ns}.get","params":{"topic":${token}}}`);
    }
  }
  await conn.drain();
  expect(calls).toBe(0);
  expect(frames).toHaveLength(1);
  await conn.shutdown();
});

test("deep unknown fields are scanned iteratively and special keys cannot pollute prototypes", async () => {
  const deep = "[".repeat(9000) + '"escaped \\\" ] }"' + "]".repeat(9000);
  const { conn, rawCall } = harness();
  await rawCall('{"id":"init","method":"initialize","params":{"protocol":1}}');
  const frame = await rawCall(`{"id":"deep","method":"connector.tools.call","params":{"args":{"unknown":${deep},"topic":6,"replies":6}}}`);
  expect(frame.result.topic).toBe("6"); expect(frame.result.replies).toBe("6");
  const values = ExactJSON.parse('{"__proto__":6,"constructor":7}').int64Fields(["__proto__", "constructor"]);
  expect(Object.getPrototypeOf(values)).toBeNull();
  expect(values.__proto__).toBe(6n); expect(values.constructor).toBe(7n);
  expect(Object.prototype.hasOwnProperty.call(Object.prototype, "polluted")).toBe(false);
  expect(() => ExactJSON.parse(" ".repeat((8 << 20) + 1))).toThrow(RangeError);
  expect(() => ExactJSON.parse('{"n":1}').int64Fields(["n", "N"])).toThrow(TypeError);
  expect(Object.isFrozen(ExactJSON.parse("1"))).toBe(true);
  await conn.shutdown();
});
