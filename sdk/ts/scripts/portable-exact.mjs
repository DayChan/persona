import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { test } from "node:test";

// The caller emits the official host SDK and default Program separately. There is
// no copied SDK source, custom parser, Bun global, or authorization bypass here.
const { connect, ExactJSON } = await import(pathToFileURL(process.argv[2]));
const { default: defaultProgram, deliveryProgram } = await import(pathToFileURL(process.argv[3]));
const cases = JSON.parse(readFileSync(new URL("../src/testdata/int64-cases.json", import.meta.url), "utf8"));
const notificationCases = JSON.parse(readFileSync(new URL("../src/testdata/exact-notification-cases.json", import.meta.url), "utf8"));
const settleEvent = async () => { await Promise.resolve(); await Promise.resolve(); };
assert.equal(typeof globalThis.Bun, "undefined");

for (const c of notificationCases) {
  test(`separately bundled raw notification int64: ${c.name}`, { timeout: 1000 }, async () => {
    const frames = [], seen = [];
    const conn = connect(deliveryProgram((method, legacy, call) => {
      const entry = { method, legacy, call };
      seen.push(entry);
      try { entry.values = call?.params?.int64Fields(["count", "frame"]); }
      catch (error) { entry.error = error; throw error; }
      return entry.values;
    }), text => frames.push(JSON.parse(text)));
    try {
      conn.line('{"id":"init","method":"initialize","params":{"protocol":1}}');
      await conn.drain();
      conn.line(`{"id":"not-a-request","event":"error_reports.deliver","params":${c.raw}}`);
      await settleEvent();
      assert.equal(seen.length, 1);
      assert.equal(seen[0].method, "deliver");
      assert.equal(seen[0].call?.rawParams, c.raw);
      assert.equal(Object.isFrozen(seen[0].call), true);
      assert.equal(Object.isFrozen(seen[0].call?.params), true);
      if (c.error) assert.equal(seen[0].error?.code, "invalid_params");
      else assert.deepEqual({ ...seen[0].values }, { count: BigInt(c.count), frame: BigInt(c.frame) });
      conn.line('{"id":"ping","method":"ping"}');
      await conn.drain();
      assert.deepEqual(frames.map(frame => frame.id), ["init", "ping"]);
    } finally { await conn.shutdown(); }
  });
}

test("source notifications retain initialization, routing, legacy arity and raw params selection", { timeout: 1000 }, async () => {
  let release;
  const ready = new Promise(resolve => { release = resolve; });
  const frames = [], exact = [], legacy = [];
  const program = deliveryProgram((method, params, call) => { exact.push({ method, params, call }); return {}; });
  program.init = () => ready;
  program.namespaces.legacy = function (method, params) { legacy.push({ method, params, arity: arguments.length }); };
  const conn = connect(program, text => frames.push(JSON.parse(text)));
  try {
    conn.line('{"event":"error_reports.before"}');
    conn.line('{"id":"init","method":"initialize","params":{"protocol":1}}');
    await settleEvent();
    conn.line('{"event":"error_reports.during"}');
    conn.line('{"id":"req","method":"error_reports.request","event":"error_reports.not-an-event","params":{"count":9007199254740993}}');
    await settleEvent();
    assert.equal(exact.length, 0);
    release();
    await conn.drain();
    for (const fields of [
      '', ',"params":null', ',"params":{"count":1.0},"pa\\u0072ams":{"count":9007199254740993}',
      ',"params":{"count":2},"PARAMS":null', ',"Params":{"frame":-9223372036854775808}',
    ]) conn.line(`{"event":"error_reports.deliver"${fields}}`);
    conn.line('{"event":"legacy.changed","params":{"count":9007199254740993}}');
    for (const raw of ['{"event":"unknown.deliver"}', '{"event":"error_reports"}', '{"event":".deliver"}',
      '{"event":123}', '{"event":"error_reports.invalid","params":{"count":01}}']) conn.line(raw);
    await settleEvent();
    assert.deepEqual(exact.map(entry => entry.method), ["request", ...Array(5).fill("deliver")]);
    assert.deepEqual(exact.map(entry => entry.call?.rawParams), [
      '{"count":9007199254740993}', undefined, "null", '{"count":9007199254740993}', "null", '{"frame":-9223372036854775808}',
    ]);
    assert.deepEqual(exact.slice(1).map(entry => entry.params), [{}, {}, { count: JSON.parse("9007199254740993") }, { count: 2 }, {}]);
    assert.deepEqual(legacy, [{ method: "changed", params: { count: JSON.parse("9007199254740993") }, arity: 2 }]);
    assert.deepEqual(frames.map(frame => frame.id), ["init", "req"]);
  } finally { await conn.shutdown(); }
  conn.line('{"event":"error_reports.after-shutdown"}');
  await settleEvent();
  assert.equal(exact.length, 6);
});

test("source exact notification limits leave ordinary legacy input unchanged", { timeout: 1000 }, async () => {
  let exactCalls = 0, legacyCalls = 0;
  const frames = [];
  const program = deliveryProgram(() => { exactCalls++; });
  program.namespaces.legacy = () => { legacyCalls++; };
  const conn = connect(program, text => frames.push(JSON.parse(text)));
  try {
    conn.line('{"id":"init","method":"initialize","params":{"protocol":1}}');
    await conn.drain();
    const params = `{"other":"${"x".repeat(8 << 20)}"}`;
    conn.line(`{"event":"error_reports.deliver","params":${params}}`);
    conn.line(`{"event":"legacy.deliver","params":${params}}`);
    await settleEvent();
    assert.equal(exactCalls, 0);
    assert.equal(legacyCalls, 1);
    assert.equal(frames.length, 1);
  } finally { await conn.shutdown(); }
});

async function dispatch(raw, program = defaultProgram) {
  const frames = [];
  const conn = connect(program, text => frames.push(JSON.parse(text)));
  try {
    conn.line('{"id":"init","method":"initialize","params":{"protocol":1}}');
    await conn.drain();
    conn.line(raw);
    await conn.drain();
    return frames.at(-1);
  } finally { await conn.shutdown(); }
}

for (const c of cases) {
  test(`separately bundled source raw int64: ${c.name}`, { timeout: 1000 }, async () => {
    const frame = await dispatch(`{"id":"case","method":"connector.tools.call","params":{"name":"example_topic","args":${c.raw}}}`);
    assert.equal(frame.id, "case");
    if (c.error) assert.equal(frame.error.code, "invalid_params");
    else assert.deepEqual(frame.result, { topic: c.topic, replies: c.replies, raw: c.raw.trim() });
  });
}

test("source RawMessage field folding, escaped keys, last duplicate and null", { timeout: 1000 }, async () => {
  for (const [params, expected] of [
    ['{"a\\u0072gs":{"topic":1.0},"ARGS":{"TOPIC":6,"replie\\u017f":6}}', { topic: "6", replies: "6", raw: '{"TOPIC":6,"replie\\u017f":6}' }],
    ['{"args":{"topic":6},"ARGS":null}', { topic: "0", replies: "0", raw: "null" }],
    ['{"args":null,"ARGS":{"topic":6}}', { topic: "6", replies: "0", raw: '{"topic":6}' }],
    ['{}', { topic: "0", replies: "0" }],
  ]) {
    assert.deepEqual((await dispatch(`{"id":"case","method":"connector.tools.call","params":${params}}`)).result, expected);
  }
  assert.deepEqual((await dispatch('{"id":"case","method":"connector.tools.call","params":{"args":{"topic":1.0}},"pa\\u0072ams":{"args":{"topic":6}}}')).result,
    { topic: "6", replies: "0", raw: '{"topic":6}' });
  assert.deepEqual((await dispatch('{"id":"case","method":"connector.tools.call","params":{"args":{"topic":1.0}},"PARAMS":{"args":{"topic":6}}}')).result,
    { topic: "6", replies: "0", raw: '{"topic":6}' });
  assert.deepEqual((await dispatch('{"id":"case","method":"connector.tools.call","params":{"args":{"topic":6}},"Params":null}')).result,
    { topic: "0", replies: "0" });
});

test("source opt-in retains original tokens while legacy namespaces keep two arguments and Number", { timeout: 1000 }, async () => {
  const counts = [];
  const program = { ...defaultProgram, namespaces: { ...defaultProgram.namespaces,
    legacy: function (_method, params) { counts.push(arguments.length); return params; },
  } };
  for (const token of ["1.0", "1e0", "1.0000000000000001", "9007199254740993", "9223372036854775807"]) {
    assert.equal((await dispatch(`{"id":"case","method":"legacy.get","params":{"topic":${token}}}`, program)).result.topic, JSON.parse(token));
  }
  assert.deepEqual(counts, [2, 2, 2, 2, 2]);
});

test("deep unknown raw values do not recurse or pollute the prototype", { timeout: 1000 }, async () => {
  const raw = `{"unknown":${"[".repeat(9000)}0${"]".repeat(9000)},"__proto__":{"polluted":true},"topic":6}`;
  const frame = await dispatch(`{"id":"case","method":"connector.tools.call","params":{"args":${raw}}}`);
  assert.equal(frame.result.topic, "6");
  assert.equal(Object.hasOwn(Object.prototype, "polluted"), false);
  const fields = ExactJSON.parse('{"__proto__":6}').int64Fields(["__proto__"]);
  assert.equal(Object.getPrototypeOf(fields), null);
  assert.equal(fields.__proto__, 6n);
});

test("JSON syntax errors never invoke the source or legacy handler", { timeout: 1000 }, async () => {
  let calls = 0;
  const frames = [];
  const conn = connect({ ...defaultProgram, namespaces: { legacy: () => { calls++; }, connector: defaultProgram.namespaces.connector } },
    text => frames.push(JSON.parse(text)));
  conn.line('{"id":"init","method":"initialize","params":{"protocol":1}}');
  await conn.drain();
  for (const ns of ["connector", "legacy"]) {
    for (const token of ["01", "+1", "1.", ".1", "1e", "NaN", "Infinity", "-01", "--1"]) {
      conn.line(`{"id":"bad","method":"${ns}.get","params":{"args":{"topic":${token}}}}`);
    }
  }
  await conn.shutdown();
  assert.equal(calls, 0);
  assert.equal(frames.length, 1);
});
