import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtempSync, mkdirSync, readFile, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { availableParallelism, tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, test } from "node:test";
import { checkSourceBundle } from "./check-source-bundle.mjs";

const good = 'export default { id: "example", version: "1.0.0" };';
const script = fileURLToPath(new URL("./check-source-bundle.mjs", import.meta.url));
// One runtime comparison matrix, reused by native ESM, public SDK connect and the probe.
const realmInitialization = `
  const check = (v, C, label) => { if (!(v instanceof C) || !(v instanceof Object)) throw Error(label); };
  const values = [{nested:[]}, [1, {}], new Date(), new Map([[{}, new Set([{}])]]), new Set([[]]), /example/gi,
    new ArrayBuffer(8), new DataView(new ArrayBuffer(8)), new Error("example")];
  const types = [Object, Array, Date, Map, Set, RegExp, ArrayBuffer, DataView, Error];
  for (let i=0; i<values.length; i++) {
    check(values[i], types[i], "local-"+i);
    const clone = structuredClone(values[i]); check(clone, types[i], "clone-"+i);
    if (types[i] !== Array && clone instanceof Array) throw Error("false array");
    if (types[i] !== Map && clone instanceof Map) throw Error("false map");
  }
  for (const name of ["Int8Array","Uint8Array","Uint8ClampedArray","Int16Array","Uint16Array","Int32Array",
    "Uint32Array","Float16Array","Float32Array","Float64Array","BigInt64Array","BigUint64Array"]) {
    const C=globalThis[name]; if (!C) continue;
    const clone=structuredClone(new C(2)); check(clone,C,name);
    check(clone.buffer,ArrayBuffer,name+" buffer");
    if (!ArrayBuffer.isView(clone) || clone instanceof Array) throw Error("view family");
  }
  const shared = structuredClone(new SharedArrayBuffer(8)); check(shared,SharedArrayBuffer,"shared");
  const graph = {array:[{}],map:new Map([[{},new Set([{}])]])}; graph.self=graph;
  const cloned=structuredClone(graph); check(cloned,Object,"graph"); check(cloned.array,Array,"graph array");
  check(cloned.array[0],Object,"nested object"); check(cloned.map,Map,"nested map");
  for (const [key,value] of cloned.map) { check(key,Object,"map key"); check(value,Set,"map value"); }
  if (cloned.self!==cloned) throw Error("cycle identity");
  const input=new ArrayBuffer(8), transferred=structuredClone({input},{transfer:[input]});
  if (input.byteLength!==0) throw Error("transfer"); check(transferred.input,ArrayBuffer,"transferred");
  for (const payload of [{nested:[{}]}, [{nested:{}}]]) {
    const response=await Response.json(payload).json();
    check(response,Array.isArray(payload)?Array:Object,"Response.json");
    const nested=Array.isArray(response)?response[0].nested:response.nested;
    check(nested,Array.isArray(nested)?Array:Object,"json nested");
  }
  for (const value of [new URL("https://example.com/"),new URLSearchParams("a=1"),new Headers(),new FormData(),
    new AbortController().signal,new Blob(),new Response(),new TextEncoder(),new Event("example"),crypto]) {
    check(value,Object,"Web API object");
  }
  check(new URLSearchParams("a=1").entries().next(),Object,"iterator result");
  check(new URLSearchParams("a=1").entries().next().value,Array,"iterator pair");
  check(new Headers({example:"value"}).entries().next().value,Array,"headers pair");
  const reader=new ReadableStream({start(c){c.enqueue(new Uint8Array(2));c.close();}}).getReader();
  const read=await reader.read(); check(read,Object,"stream read"); check(read.value,Uint8Array,"stream value");
  reader.releaseLock();
  const pair=await crypto.subtle.generateKey({name:"ECDSA",namedCurve:"P-256"},true,["sign","verify"]);
  // Follow native Node: some releases (22.16) return a null-prototype key-pair record,
  // which must not become an Object instance; others (24.22.2) return an ordinary object.
  if (Object.getPrototypeOf(pair)===null ? pair instanceof Object : !(pair instanceof Object)) throw Error("key pair prototype");
  check(pair.publicKey,CryptoKey,"public key");
  check(await crypto.subtle.exportKey("jwk",pair.publicKey),Object,"jwk");
  class ChildMap extends Map {} class ChildObject {} class ChildArray extends Array {}
  if (structuredClone(new Map()) instanceof ChildMap || structuredClone({}) instanceof ChildObject
    || structuredClone([]) instanceof ChildArray) throw Error("false subclass");
  check(new ChildMap(),ChildMap,"subclass"); check(new ChildMap(),Map,"base");
  check({},Object,"literal"); check([],Array,"literal array"); check(/example/,RegExp,"literal regexp");
  check(performance,Performance,"performance");
  const mark=performance.mark("authoring-start"), measure=performance.measure("authoring-measure");
  check(mark,PerformanceMark,"mark"); check(mark,PerformanceEntry,"entry");
  check(measure,PerformanceMeasure,"measure");
  if (mark.constructor!==PerformanceMark || measure.constructor!==PerformanceMeasure
    || performance.constructor!==Performance) throw Error("performance constructor");
  if (typeof PerformanceResourceTiming!=="function") throw Error("resource timing");
  await new Promise(resolve=>{
    const observer=new PerformanceObserver(list=>{
      observer.disconnect(); check(list,PerformanceObserverEntryList,"observer list"); resolve();
    });
    check(observer,PerformanceObserver,"observer"); observer.observe({entryTypes:["mark"]});
    performance.mark("authoring-observed");
  });
  const event=new CustomEvent("ready",{detail:{ok:true}});
  check(event,CustomEvent,"custom event"); check(event,Event,"event base");
  if (!event.detail.ok || event.constructor!==CustomEvent) throw Error("event constructor");
`;
const settings = { timeout: 15000 };
const cleanEnv = { ...process.env };
// Every subprocess, including SDK tests that may launch Git, must own its locator metadata.
for (const key of ["GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"]) delete cleanEnv[key];
// Bounded parallelism: each slot is one owned checker worker or child process, so
// overlapping cases never oversubscribe a small runner. Waiting for a slot happens
// before a call starts its own deadline.
const slotLimit = Math.max(2, Math.min(4, availableParallelism() - 1));
let activeSlots = 0;
const slotWaiters = [];
async function inSlot(work) {
  if (activeSlots < slotLimit) activeSlots++;
  else await new Promise(resolve => slotWaiters.push(resolve));
  try { return await work(); }
  finally { const next = slotWaiters.shift(); if (next) next(); else activeSlots--; }
}
const check = options => inSlot(() => checkSourceBundle(options));
// Like spawnSync, but never blocks the parent event loop that supervises other
// cases' library workers; the child keeps its own hard deadline.
function run(command, args, timeoutMs) {
  return inSlot(async () => {
    const child = spawn(command, args, { stdio: ["ignore", "pipe", "pipe"], env: cleanEnv });
    let stdout = "", stderr = "";
    child.stdout.setEncoding("utf8"); child.stderr.setEncoding("utf8");
    child.stdout.on("data", data => stdout += data); child.stderr.on("data", data => stderr += data);
    const { code, signal } = await waitForExit(child, timeoutMs);
    return { status: code, signal, stdout, stderr };
  });
}
// Tests that neither block the parent event loop, stall it on purpose, nor count
// process-wide resources run after the serial tests, two at a time when slots allow.
const independentTests = [];
const independentTest = (name, options, fn) => independentTests.push([name, options, fn]);
function fixture(text = good) {
  const root = realpathSync(mkdtempSync(join(tmpdir(), "persona-source-authoring-")));
  // Canonical tmpdir supports macOS, where /tmp is a system symlink.
  const bundle = "main.mjs";
  writeFileSync(join(root, bundle), text);
  const options = { root, bundle, probe: true, timeoutMs: 3000 };
  return { root, options, close: () => rmSync(root, { recursive: true, force: true }) };
}
async function result(text, overrides = {}) {
  const f = fixture(text);
  try { return await check({ ...f.options, ...overrides }); }
  finally { f.close(); }
}
async function accepted(text, overrides) {
  const r = await result(text, overrides);
  assert.equal(r.status, "PASS", JSON.stringify(r));
  return r;
}
async function refused(text, code, overrides) {
  const r = await result(text, overrides);
  assert.equal(r.status, "FAIL", JSON.stringify(r));
  if (code) assert.equal(r.code, code, JSON.stringify(r));
  return r;
}

independentTest("static checks never execute initialization or claim runtime Program verification", settings, async () => {
  const r = await accepted('throw Error("must not run");' + good, { probe: false });
  assert.equal(r.code, "STATIC_OK"); assert.equal(r.program, "not-probed");
  await refused('throw Error("must run");' + good, "EVALUATION");
});

independentTest("default Program accepts optional manifest, function maps and official page shapes", settings, async () => {
  const r = await accepted(good); assert.equal(r.program, "verified");
  await accepted('export default { id: "example", version: "1.0.0", manifest: undefined, init() {},'
    + 'namespaces: { service() {} }, shortcuts: { run() {} }, ui: {'
    + 'legacy: { render() {}, action() {} }, native: { componentPage: true, root() {}, createRenderer() {}, options: {} } } };');
  await accepted('const p = { id: "example", version: "1.0.0" }; export { p as default };');
});

independentTest("an actual browser bundle built from the official SDK passes the source contract", settings, async () => {
  const f = fixture();
  try {
    const sdk = fileURLToPath(new URL("../src/index.ts", import.meta.url));
    writeFileSync(join(f.root, "entry.ts"), `import { componentPage, type Program } from ${JSON.stringify(sdk)};
      export default { id: "example", version: "1.0.0", ui: { home: componentPage(() => () => ({ sections: [] })) },
        namespaces: { example: (_method: string, params: unknown) => params } } satisfies Program;`);
    const build = await run("bun", ["build", "--target=browser", "--format=esm", "--no-macros", join(f.root, "entry.ts"),
      "--outfile", join(f.root, "main.mjs")], 10000);
    assert.equal(build.status, 0, build.stderr);
    const manifest = outer(f, { memory_mb: 128, cpu_percent: 25 });
    writeFileSync(join(f.root, "manifest.json"), JSON.stringify(manifest));
    const verdict = await check({ ...f.options, manifest: "manifest.json" });
    assert.equal(verdict.status, "PASS", JSON.stringify(verdict));
  } finally { f.close(); }
});

test("MessageChannel ports, already closed ports and intervals cannot keep the checker alive", settings, async () => {
  await accepted('const channel = new MessageChannel(); channel.port1.onmessage = () => {};' + good);
  await accepted('const channel = new MessageChannel(); channel.port1.close(); channel.port2.close();' + good);
  await accepted('setInterval(() => {}, 1000);' + good);
  const before = process.getActiveResourcesInfo().filter(type => type === "MessagePort").length;
  for (let i = 0; i < 3; i++) await accepted('new MessageChannel().port1.onmessage = () => {};' + good);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(process.getActiveResourcesInfo().filter(type => type === "MessagePort").length, before);
});

independentTest("standard Abort, URLSearchParams, streams and WebCrypto initializers", settings, async () => {
  await accepted('const a = new AbortController(); a.abort(); if (!a.signal.aborted) throw Error("abort");'
    + 'const p = new URLSearchParams("a=1"); if (p.get("a") !== "1") throw Error("params");'
    + 'await crypto.subtle.digest("SHA-256", new TextEncoder().encode("example"));'
    + 'crypto.randomUUID(); new URL("https://example.com/");'
    + 'new Request("https://example.com/", { signal: new AbortController().signal });'
    + 'new Response(new Blob(["example"])); new EventTarget().dispatchEvent(new Event("example"));'
    + 'await new ReadableStream().cancel(); new TransformStream(); new WritableStream();'
    + 'structuredClone({ a: 1 }); if (atob(btoa("example")) !== "example") throw Error("encoding");' + good);
});

independentTest("no guest filesystem, environment, process, module, worker or network authority is supplied", settings, async () => {
  await accepted('for (const name of ["process", "Buffer", "Bun", "Deno", "Worker", "require"]) '
    + 'if (typeof globalThis[name] !== "undefined") throw Error("authority");' + good);
  await refused('await fetch("https://example.com/");' + good, "EVALUATION");
});

independentTest("all runtime import forms and unbound loader references fail", settings, async () => {
  await independentCases(['import "example";', 'import { x } from "example";', 'export * from "example";',
    'export { x } from "example";', 'function later() { return import("example"); }',
    'const meta = import.meta;', 'function later() { return require("example"); }',
    'const load = require;', 'const obj = { require };', 'function later() { return requ\\u0069re("example"); }',
    'const load = globalThis.require;', 'const load = globalThis["require"];'],
    prefix => refused(prefix + good, "IMPORT"));
});

independentTest("comments, strings, ordinary require properties and lexical bindings are not imports", settings, async () => {
  await independentCases(['// import("example"); require("example");\nconst words = "require import";',
    'const p = { require: true }; p.require;', 'const p = { require() {} }; p.require();',
    'const p = { require: true }; const { require: v } = p;', 'const require = true; const p = { require };',
    'function local(require) { return require; } local(true);',
    'function local({ require }) { return require; } local({ require: true });',
    'function require(v) { return v; } require(true);', '{ const require = () => true; require(); }',
    'function local() { return require(); function require() { return true; } } local();',
    'const globalThis = { require: true }; globalThis.require;'], prefix => accepted(prefix + good));
});

independentTest("local require in one scope does not hide a global loader in another", settings, async () => {
  for (const prefix of ['function local(require) { return require; } function other() { return require("example"); }',
    '{ const require = true; } function outside() { return require("example"); }',
    'function local() { const load = require; return load("example"); }']) await refused(prefix + good, "IMPORT");
});

independentTest("ECMAScript syntax, truncated bundles and missing defaults fail without evaluation", settings, async () => {
  for (const text of ['export default {', 'const value: number = 1;' + good,
    'export default { id: "example", version: "1.0.0" }; export default null;']) await refused(text, "SYNTAX", { probe: false });
  await refused('export const p = {};', "DEFAULT_EXPORT", { probe: false });
});

independentTest("non-Program values, getters and malformed handlers fail in the trusted probe", settings, async () => {
  await independentCases(['null', '() => ({})', '{ render() {} }', '{ id: "example", version: "1.0.0", init: true }',
    '{ id: "example", version: "1.0.0", namespaces: { service: {} } }',
    '{ id: "example", version: "1.0.0", shortcuts: { run: true } }',
    '{ id: "example", version: "1.0.0", ui: { home: {} } }',
    '{ id: "example", version: "1.0.0", ui: { home: { componentPage: true, root() {} } } }',
    '{ get id() { throw Error("getter must not run"); }, version: "1.0.0" }',
    '{ id: "example", version: "1.0.0", namespaces: { get service() { return () => {}; } } }'],
    value => refused('export default ' + value + ';', "PROGRAM"));
});

independentTest("Program.fetch is unsupported only by this source authoring contract", settings, async () => {
  for (const value of ['() => new Response("ok")', 'false', 'true', 'null']) {
    await refused('export default { id: "example", version: "1.0.0", fetch: ' + value + ' };', "PROGRAM");
  }
  await accepted('export default { id: "example", version: "1.0.0", fetch: undefined };');
});

independentTest("top-level await is supported and asynchronous/CPU hangs have a deadline", settings, async () => {
  await accepted('await new Promise(resolve => setTimeout(resolve, 1));' + good);
  await refused('await new Promise(() => {});' + good, "TIMEOUT", { timeoutMs: 500 });
  await refused('while (true) {};' + good, "TIMEOUT", { timeoutMs: 500 });
  await refused('queueMicrotask(function again() { queueMicrotask(again); });' + good, "TIMEOUT", { timeoutMs: 500 });
});

independentTest("initialization throws, async rejections and late errors never print a false PASS", settings, async () => {
  await refused('throw Error("example");' + good, "EVALUATION");
  await refused('Promise.reject(Error("example"));' + good, "EVALUATION");
  await refused('setTimeout(() => { throw Error("example"); }, 10);' + good, "WORKER");
  await refused('setTimeout(() => Promise.reject(Error("example")), 10);' + good, "EVALUATION");
});

async function independentCases(values, inspect) {
  let next = 0;
  // Only disjoint temporary fixtures may overlap. Wait for both lanes even on
  // failure so owned workers/children settle before their test ends.
  const lane = async () => { while (next < values.length) await inspect(values[next++]); };
  await settled([lane(), lane()]);
}
// Like Promise.all, but owned workers/children settle before a failure ends the test.
async function settledValues(pending) {
  const results = await Promise.allSettled(pending);
  for (const result of results) if (result.status === "rejected") throw result.reason;
  return results.map(result => result.value);
}
const settled = async pending => { await settledValues(pending); };

function outer(f, resources) {
  const sha256 = createHash("sha256").update(readFileSync(join(f.root, "main.mjs"))).digest("hex");
  return { manifest_version: 1, id: "example", version: "1.0.0", runtime: {
    mode: "deno", source: { entry: "src/main.tsx", bundle: "main.mjs", sha256 }, ...(resources !== undefined ? { resources } : {}),
  } };
}

independentTest("outer manifest uses the exact final hash and preserves valid resource budgets and all inputs", settings, async () => {
  const f = fixture();
  try {
    const before = readFileSync(join(f.root, "main.mjs"));
    mkdirSync(join(f.root, ".previous-stage"));
    const previousPackage = Buffer.from("Synthetic previous package: do not overwrite");
    writeFileSync(join(f.root, ".previous-stage", "package.tgz"), previousPackage);
    writeFileSync(join(f.root, "asset.txt"), "Synthetic release input");
    for (const resources of [{}, { memory_mb: 128, cpu_percent: 25 }, { memory_mb: 64, cpu_percent: 10 },
      { memory_mb: 8192, cpu_percent: 800 }]) {
      const bytes = JSON.stringify(outer(f, resources)); writeFileSync(join(f.root, "manifest.json"), bytes);
      const r = await check({ ...f.options, manifest: "manifest.json" });
      assert.equal(r.status, "PASS", JSON.stringify(r));
      assert.equal(readFileSync(join(f.root, "manifest.json"), "utf8"), bytes);
      assert.deepEqual(readFileSync(join(f.root, "main.mjs")), before);
      assert.deepEqual(readFileSync(join(f.root, ".previous-stage", "package.tgz")), previousPackage);
      assert.equal(readFileSync(join(f.root, "asset.txt"), "utf8"), "Synthetic release input");
    }
  } finally { f.close(); }
});

independentTest("outer source profile rejects invalid descriptors, resources, runtime and identity mismatches", settings, async () => {
  const f = fixture();
  try {
    for (const resources of [null, [], true, { disk_mb: 128 }, { memory_mb: 63 }, { memory_mb: 8193 },
      { cpu_percent: 9 }, { cpu_percent: 801 }, { memory_mb: true }, { memory_mb: 128.5 }, { cpu_percent: "25" }]) {
      writeFileSync(join(f.root, "manifest.json"), JSON.stringify(outer(f, resources)));
      assert.equal((await check({ ...f.options, manifest: "manifest.json" })).code, "RESOURCES");
    }
    for (const patch of [m => m.runtime.source.sha256 = "0".repeat(64), m => m.runtime.source.entry = "../main.ts",
      m => m.runtime.source.bundle = "other.mjs", m => m.runtime.source.sha256 = "A".repeat(64)]) {
      const m = outer(f); patch(m); writeFileSync(join(f.root, "manifest.json"), JSON.stringify(m));
      assert.equal((await check({ ...f.options, manifest: "manifest.json" })).code, "SOURCE_DESCRIPTOR");
    }
    for (const patch of [m => m.runtime.command = "bin/example", m => m.runtime.http = true,
      m => m.runtime.env = {}, m => m.runtime.args = [], m => m.runtime.rpc = "none", m => m.runtime.mode = "process"]) {
      const m = outer(f); patch(m); writeFileSync(join(f.root, "manifest.json"), JSON.stringify(m));
      assert.equal((await check({ ...f.options, manifest: "manifest.json" })).code, "SOURCE_RUNTIME");
    }
    const m = outer(f); m.id = "other"; writeFileSync(join(f.root, "manifest.json"), JSON.stringify(m));
    assert.equal((await check({ ...f.options, manifest: "manifest.json" })).code, "PROGRAM");
  } finally { f.close(); }
});

test("traversal, symlinked roots/ancestors/bundles, directories, empty and oversized inputs fail", settings, async () => {
  const f = fixture();
  try {
    for (const bundle of ["../main.mjs", "sub/../main.mjs", "/main.mjs", "./main.mjs", "sub\\main.mjs"]) {
      assert.equal((await checkSourceBundle({ ...f.options, bundle })).status, "FAIL");
    }
    symlinkSync(f.root, join(f.root, "linked"));
    assert.equal((await checkSourceBundle({ ...f.options, root: join(f.root, "linked") })).code, "SYMLINK");
    symlinkSync(join(f.root, "main.mjs"), join(f.root, "link.mjs"));
    assert.equal((await checkSourceBundle({ ...f.options, bundle: "link.mjs" })).code, "SYMLINK");
    mkdirSync(join(f.root, "folder.mjs"));
    assert.equal((await checkSourceBundle({ ...f.options, bundle: "folder.mjs" })).code, "FILE_TYPE");
    writeFileSync(join(f.root, "empty.mjs"), "");
    assert.equal((await checkSourceBundle({ ...f.options, bundle: "empty.mjs" })).code, "SIZE");
    writeFileSync(join(f.root, "large.mjs"), Buffer.alloc((16 << 20) + 1));
    assert.equal((await checkSourceBundle({ ...f.options, bundle: "large.mjs" })).code, "SIZE");
    writeFileSync(join(f.root, "bad.mjs"), Buffer.from([0xff]));
    assert.equal((await checkSourceBundle({ ...f.options, bundle: "bad.mjs" })).code, "INPUT");
    if (process.platform !== "win32") {
      const fifo = spawnSync("mkfifo", [join(f.root, "fifo.mjs")], { timeout: 2000, env: cleanEnv });
      assert.equal(fifo.status, 0);
      assert.equal((await checkSourceBundle({ ...f.options, bundle: "fifo.mjs" })).code, "FILE_TYPE");
    }
  } finally { f.close(); }
});

test("cancel before and during a pending probe cleans only the owned worker", settings, async () => {
  const controller = new AbortController(); controller.abort();
  await refused(good, "CANCELED", { signal: controller.signal });
  const second = new AbortController();
  const timer = setTimeout(() => second.abort(), 250);
  try { await refused('await new Promise(() => {});' + good, "CANCELED", { signal: second.signal }); }
  finally { clearTimeout(timer); }
  await accepted(good);
});

async function cliResult(options, { flags = [], entry = script } = {}) {
  const args = [entry, "--root", options.root, "--bundle", options.bundle,
    ...(options.manifest !== undefined ? ["--manifest", options.manifest] : []),
    ...(options.probe ? ["--probe-trusted"] : []), "--timeout-ms", String(options.timeoutMs)];
  const r = await run(process.execPath, [...flags, ...args], 10000);
  assert.equal(r.signal, null, r.stderr);
  assert.equal(r.stdout.trim().split("\n").length, 1, r.stdout + r.stderr);
  const verdict = JSON.parse(r.stdout);
  return { ...r, verdict };
}
async function both(text, code, overrides = {}) {
  const f = fixture(text);
  try {
    const options = { ...f.options, ...overrides };
    // The CLI child does not block the parent event loop supervising the library worker.
    const [library, cli] = await settledValues([check(options), cliResult(options)]);
    for (const r of [library, cli.verdict]) {
      assert.equal(r.code, code, JSON.stringify(r));
      assert.equal(r.status, code.endsWith("_OK") ? "PASS" : "FAIL", JSON.stringify(r));
    }
    assert.equal(cli.status, code.endsWith("_OK") ? 0 : code === "TIMEOUT" ? 124 : 1);
  } finally { f.close(); }
}

independentTest("SDK-inherited Program members and getters are inspected, not silently dropped", settings, async () => {
  await independentCases(['{ init: true }', '{ fetch() {} }',
    '{ get init() { throw Error("getter"); } }', '{ get fetch() { throw Error("getter"); } }',
    '{ get id() { throw Error("getter"); } }', '{ namespaces: Object.create({ service: true }) }',
    '{ shortcuts: Object.create({ run: true }) }', '{ ui: { home: Object.create({ render: true }) } }'], async prototype => {
    const own = prototype.includes("get id") ? '{ version: "1.0.0" }' : '{ id: "example", version: "1.0.0" }';
    await both(`export default Object.assign(Object.create(${prototype}), ${own});`, "PROGRAM");
  });
});

independentTest("hidden inherited constructor members are contributions the SDK dispatches", settings, async () => {
  // A hidden inherited member named constructor is a contribution the SDK dispatches
  // (real connect: "h is not a function"). Only a prototype's own back-reference to
  // its class or function is structural; a class or function stored elsewhere is not.
  await independentCases([["namespaces", "true"], ["shortcuts", "true"], ["ui", "true"],
    ["namespaces", "class Hidden {}"], ["shortcuts", "class Hidden {}"], ["ui", "class Hidden {}"],
    ["ui", "function Page() {}"]], ([slot, value]) => both(`const map = Object.create(
    Object.defineProperty({}, "constructor", {value:${value}}));
    export default {id:"example",version:"1.0.0",${slot}:map};`, "PROGRAM"));
});

independentTest("structural class/function constructors and nearer own constructors remain valid maps", settings, async () => {
  await independentCases([
    // Structural class-map constructors are metadata, not contributions. Other
    // inherited callable constructors and nearer own data remain valid SDK lookups.
    `class Namespaces {service(){}} class Shortcuts {home(){}}
    class Pages {getPage={render(){}}}
    export default {id:"example",version:"1.0.0",namespaces:new Namespaces(),
      shortcuts:new Shortcuts(),ui:new Pages()};`,
    `class Base {service(){}} class Namespaces extends Base {other(){}}
    function Shortcuts() {} Shortcuts.prototype.home = function () {};
    class BasePages {} class Pages extends BasePages {home={render(){}}}
    export default {id:"example",version:"1.0.0",namespaces:new Namespaces(),
      shortcuts:new Shortcuts(),ui:new Pages()};`,
    `const namespaces=Object.create(Object.defineProperty({},"constructor",{value:()=>{}}));
    const shortcuts=Object.create(Object.defineProperty({},"constructor",{value:()=>{}}));
    const ui=Object.create(Object.defineProperty({},"constructor",{value:{render(){}}}));
    export default {id:"example",version:"1.0.0",namespaces,shortcuts,ui};`,
    `const proto=Object.defineProperty({},"constructor",{get(){while(true){}}});
    const namespaces=Object.defineProperty(Object.create(proto),"constructor",{value:()=>{}});
    const shortcuts=Object.defineProperty(Object.create(proto),"constructor",{value:()=>{}});
    const ui=Object.defineProperty(Object.create(proto),"constructor",{value:{render(){}}});
    export default {id:"example",version:"1.0.0",namespaces,shortcuts,ui};`,
    'class Program { id = "example"; version = "1.0.0"; init() {} } export default new Program();',
    'const p = { id: "example", version: "1.0.0", init() {}, fetch: undefined,'
    + 'namespaces: Object.create({ service() {} }), shortcuts: Object.create({ run() {} }),'
    + 'ui: Object.create({ home: Object.create({ render() {}, action() {} }) }) }; export default Object.create(p);',
    // A shadowed prototype getter is never read by connect and must not be called by the probe.
    'export default Object.setPrototypeOf({ id: "example", version: "1.0.0", init() {} },'
    + '{ get init() { throw Error("not read"); } });',
  ], text => both(text, "PROGRAM_OK"));
});

independentTest("real public SDK connect reproduces invalid inherited init/constructor and accepts a class Program", settings, async () => {
  const f = fixture();
  try {
    mkdirSync(join(f.root, "node_modules", "@persona"), { recursive: true });
    symlinkSync(fileURLToPath(new URL("..", import.meta.url)), join(f.root, "node_modules", "@persona", "extension"), "dir");
    writeFileSync(join(f.root, "transport.ts"), `import { connect, PROTOCOL } from "@persona/extension";
      const replies = [];
      for (const program of [Object.assign(Object.create({init:true}), {id:"example",version:"1.0.0"}),
        new (class { id="example"; version="1.0.0"; init() {} })()]) {
        const conn = connect(program, line => replies.push(JSON.parse(line)));
        conn.line(JSON.stringify({id:"init",method:"initialize",params:{protocol:PROTOCOL}}));
        await conn.shutdown();
      }
      const hidden = value => Object.create(Object.defineProperty({},"constructor",{value}));
      for (const [slots, method, params] of [[{namespaces:hidden(true)}, "constructor.status", {}],
        [{shortcuts:hidden(true)}, "shortcut.run", {contribution:"constructor"}],
        [{ui:hidden(true)}, "ui.render", {contribution:"constructor"}],
        [{namespaces:hidden(class Hidden {})}, "constructor.status", {}]]) {
        const conn = connect({id:"example",version:"1.0.0",...slots}, line => replies.push(JSON.parse(line)));
        conn.line(JSON.stringify({id:"init",method:"initialize",params:{protocol:PROTOCOL}}));
        conn.line(JSON.stringify({id:"call",method,params}));
        await conn.shutdown();
      }
      console.log(JSON.stringify(replies));`);
    const build = await run("bun", ["build", "--target=browser", "--format=esm", "--no-macros",
      join(f.root, "transport.ts"), "--outfile", join(f.root, "transport.mjs")], 10000);
    assert.equal(build.status, 0, build.stderr);
    const transport = await run(process.execPath, [join(f.root, "transport.mjs")], 5000);
    assert.equal(transport.status, 0, transport.stderr);
    const replies = JSON.parse(transport.stdout);
    assert.match(replies[0].error.message, /init is not a function/);
    assert.equal(replies[1].result.id, "example");
    // The checker refuses these hidden constructor contributions for the same reason.
    const calls = replies.filter(reply => reply.id === "call");
    assert.equal(calls.length, 4);
    for (const call of calls.slice(0, 3)) assert.match(call.error.message, /is not a function/);
    assert.match(calls[3].error.message, /Class constructor/);
  } finally { f.close(); }
});

independentTest("observation revalidates mutated members, inherited values and the live default binding", settings, async () => {
  await independentCases(['p.init = true', 'p.fetch = () => {}', 'p.namespaces = { service: true }', 'p = null',
    'Object.setPrototypeOf(p, { init: true })', 'Object.defineProperty(p, "init", { get() { throw Error("getter"); } })'],
    change => both('let p = { id: "example", version: "1.0.0" }; export { p as default };'
      + `setTimeout(() => { ${change}; }, 10);`, "PROGRAM"));
  await both('let p = null; p = { id: "example", version: "1.0.0" }; export { p as default };'
    + 'setTimeout(() => { p = Object.create(p); }, 10);', "PROGRAM_OK");
});

independentTest("Web API binary results and all available WebCrypto constructors keep Node semantics", settings, async () => {
  const cryptoNames = ["Crypto", "CryptoKey", "SubtleCrypto"];
  const binaries = ["ArrayBuffer", "SharedArrayBuffer", "DataView", "Int8Array", "Uint8Array", "Uint8ClampedArray",
    "Int16Array", "Uint16Array", "Int32Array", "Uint32Array", "Float16Array", "Float32Array", "Float64Array", "BigInt64Array", "BigUint64Array"];
  const expected = [...cryptoNames, ...binaries].filter(name => globalThis[name] !== undefined);
  const text = `for (const name of ${JSON.stringify(expected)}) if (typeof globalThis[name] !== "function") throw Error(name);
    const encoded = new TextEncoder().encode("example");
    if (!(encoded instanceof Uint8Array) || encoded.constructor !== Uint8Array) throw Error("typed array realm");
    const buffer = await crypto.subtle.digest("SHA-256", encoded);
    if (!(buffer instanceof ArrayBuffer) || buffer.constructor !== ArrayBuffer) throw Error("buffer realm");
    const cloned = structuredClone(new Uint16Array(2));
    if (!(cloned instanceof Uint16Array) || cloned.constructor !== Uint16Array) throw Error("clone realm");
    const blob = await new Blob([encoded]).arrayBuffer();
    if (!(blob instanceof ArrayBuffer) || blob.constructor !== ArrayBuffer) throw Error("blob realm");
    const response = await new Response(encoded).arrayBuffer();
    if (!(response instanceof ArrayBuffer)) throw Error("response realm");
    const key = await crypto.subtle.generateKey({name:"HMAC",hash:"SHA-256"}, true, ["sign","verify"]);
    if (!(crypto instanceof Crypto) || !(crypto.subtle instanceof SubtleCrypto) || !(key instanceof CryptoKey)) throw Error("crypto realm");
    const signature = await crypto.subtle.sign("HMAC", key, new Uint8Array(2));
    if (!(signature instanceof ArrayBuffer)) throw Error("signature realm");` + good;
  const f = fixture(text);
  try {
    // Ordinary Node is the reference, not a mock implementation of a Web API.
    const normal = await run(process.execPath, [join(f.root, "main.mjs")], 5000);
    assert.equal(normal.status, 0, normal.stderr);
  } finally { f.close(); }
  await both(text, "PROGRAM_OK");
});

for (const name of ["globalThis", "global", "window", "self"]) {
  independentTest(`no-substitution templates follow the same literal ${name} loader rule`, settings, async () => {
    await independentCases([[`const load = ${name}[\`require\`];`, "IMPORT"], [`const load = ${name}[\`requ\\u0069re\`];`, "IMPORT"],
      [`const ${name} = { require: true }; const load = ${name}[\`require\`];`, "PROGRAM_OK"],
      ...(name === "globalThis" ? [['const p = { require: true }; p[`require`];', "PROGRAM_OK"]] : [])],
      ([prefix, code]) => both(prefix + good, code, code === "IMPORT" ? { probe: false } : {}));
  });
}

test("real Node permission denial stays denied and returns TOOL from CLI and library", settings, async () => {
  const f = fixture();
  try {
    const flags = [Number(process.versions.node.split(".")[0]) === 22 ? "--experimental-permission" : "--permission", "--allow-fs-read=*"];
    const cli = await cliResult(f.options, { flags });
    assert.equal(cli.status, 2, cli.stderr);
    assert.equal(cli.verdict.code, "TOOL"); assert.equal(cli.verdict.status, "FAIL");
    writeFileSync(join(f.root, "library.mjs"), `import { checkSourceBundle } from ${JSON.stringify(new URL("./check-source-bundle.mjs", import.meta.url).href)};
      if (process.permission.has("worker")) throw Error("worker authority must remain denied");
      const result = await checkSourceBundle(${JSON.stringify(f.options)});
      console.log(JSON.stringify(result));`);
    const library = spawnSync(process.execPath, [...flags, join(f.root, "library.mjs")], { timeout: 5000, encoding: "utf8", env: cleanEnv });
    assert.equal(library.status, 0, library.stderr);
    assert.equal(library.stdout.trim().split("\n").length, 1);
    assert.equal(JSON.parse(library.stdout).code, "TOOL");
    assert.equal(JSON.parse(library.stdout).status, "FAIL");
  } finally { f.close(); }
});

test("absolute deadline rejects late results after a parent I/O event-loop stall", settings, async () => {
  const f = fixture('await new Promise(resolve => setTimeout(resolve, 800));' + good);
  try {
    const preload = join(f.root, "stall.mjs");
    // Schedule the I/O stall only once the real CLI has begun its budgeted input read.
    // It must not run before the public CLI starts measuring its deadline.
    writeFileSync(preload, `import fs from "node:fs"; import { syncBuiltinESMExports } from "node:module";
      const original = fs.readSync; let scheduled = false;
      fs.readSync = (...args) => {
        const count = original(...args);
        if (!scheduled) {
          scheduled = true;
          fs.readFile(${JSON.stringify(join(f.root, "main.mjs"))}, () => {
            const end = performance.now() + 2000; while (performance.now() < end) {}
          });
        }
        return count;
      };
      syncBuiltinESMExports();`);
    const late = await cliResult({ ...f.options, timeoutMs: 600 }, { flags: ["--import", preload] });
    assert.equal(late.status, 124, late.stdout + late.stderr); assert.equal(late.verdict.code, "TIMEOUT");
    const pending = checkSourceBundle({ ...f.options, timeoutMs: 600 });
    await new Promise(resolve => {
      // Only the caller stalls; the owned worker continues the 800 ms initializer.
      readFile(join(f.root, "main.mjs"), () => {
        const end = performance.now() + 2000; while (performance.now() < end) {}
        resolve();
      });
    });
    const r = await pending;
    assert.equal(r.code, "TIMEOUT", JSON.stringify(r)); assert.equal(r.status, "FAIL");
  } finally { f.close(); }
});

independentTest("host Web API plain-object Programs pass CLI, library, native ESM and real public SDK connect", settings, async () => {
  const f = fixture();
  try {
    mkdirSync(join(f.root, "node_modules", "@persona"), { recursive: true });
    symlinkSync(fileURLToPath(new URL("..", import.meta.url)), join(f.root, "node_modules", "@persona", "extension"), "dir");
    writeFileSync(join(f.root, "adapter.ts"), 'export { connect, PROTOCOL } from "@persona/extension";');
    const build = await run("bun", ["build", "--target=browser", "--format=esm", "--no-macros",
      join(f.root, "adapter.ts"), "--outfile", join(f.root, "adapter.mjs")], 10000);
    assert.equal(build.status, 0, build.stderr);
    const programs = [realmInitialization + good,
      'export default structuredClone({id:"example",version:"1.0.0"});',
      'export default await new Response(JSON.stringify({id:"example",version:"1.0.0"})).json();',
      'export default await Response.json({id:"example",version:"1.0.0"}).json();',
      'const p = structuredClone({id:"example",version:"1.0.0",namespaces:{},shortcuts:{},ui:{}});'
        + 'p.init = () => {}; p.namespaces.service = () => {}; p.shortcuts.run = () => {};'
        + 'p.ui.home = Object.assign(structuredClone({}), {render() {}}); export default p;'];
    for (let i = 0; i < programs.length; i++) {
      const text = programs[i];
      await both(text, "PROGRAM_OK");
      writeFileSync(join(f.root, `program-${i}.mjs`), text);
    }
    writeFileSync(join(f.root, "native.mjs"), `import { connect, PROTOCOL } from "./adapter.mjs";
      const replies = [];
      for (let i = 0; i < ${programs.length}; i++) {
        const { default: program } = await import("./program-" + i + ".mjs");
        const conn = connect(program, line => replies.push(JSON.parse(line)));
        conn.line(JSON.stringify({id:"init",method:"initialize",params:{protocol:PROTOCOL}}));
        await conn.shutdown();
      }
      console.log(JSON.stringify(replies));`);
    const native = await run(process.execPath, [join(f.root, "native.mjs")], 5000);
    assert.equal(native.status, 0, native.stderr);
    const replies = JSON.parse(native.stdout);
    assert.equal(replies.length, programs.length);
    for (const reply of replies) {
      assert.equal(reply.result.id, "example"); assert.equal(reply.result.version, "1.0.0");
    }
  } finally { f.close(); }
});

independentTest("host Web API plain objects still follow inherited SDK lookup and class prototypes", settings, async () => {
  await independentCases(['Object.setPrototypeOf(p, {init:true})', 'Object.setPrototypeOf(p, {fetch() {}})',
    'Object.defineProperty(p, "init", {get() {throw Error("getter");}})',
    'p.namespaces = structuredClone({service:true})', 'p.ui = structuredClone({home:{render:true}})'],
    change => both('const p = structuredClone({id:"example",version:"1.0.0"});' + change + '; export default p;', "PROGRAM"));
  await both('class Program { init() {} } const p = structuredClone({id:"example",version:"1.0.0"});'
    + 'Object.setPrototypeOf(p, Program.prototype); export default p;', "PROGRAM_OK");
});

for (const name of ["globalThis", "global", "window", "self"]) {
  independentTest(`direct literal ${name} loader destructuring follows CLI and library IMPORT policy`, settings, async () => {
    const bindings = name === "globalThis"
      ? ['require: load', '"require": load', '["require"]: load', '[`require`]: load', 'require'] : ['require: load'];
    for (const binding of bindings) {
      const text = `const {${binding}} = ${name};` + good;
      if (binding === 'require: load') await both(text, "IMPORT", { probe: false });
      else await refused(text, "IMPORT", { probe: false });
      await accepted(`const ${name} = {require:true}; const {${binding}} = ${name};` + good);
    }
    await refused(`let load; ({require:load} = (${name}));` + good, "IMPORT", { probe: false });
    await refused(`function later({require:load} = ${name}) { return load; }` + good, "IMPORT", { probe: false });
  });
}

independentTest("literal destructuring respects local bindings without chasing arbitrary aliases", settings, async () => {
  for (const text of ['const require = true; const {require:load} = globalThis;',
    'const {requ\\u0069re:load} = globalThis;']) await both(text + good, "IMPORT", { probe: false });
  for (const text of ['const p = {require() {return true;}}; const {require:load} = p; load();',
    'function local(globalThis) { const {require:load} = globalThis; return load; } local({require:true});',
    'const key = "require"; const {[key]:load} = globalThis;',
    'const {...require} = globalThis;', 'function local({...require} = globalThis) { return require; } local();',
    'const alias = globalThis; const {require:load} = alias;']) await accepted(text + good);
});

test("tool-entry symlinks and npm script bin links run the real CLI, unlike forbidden input links", settings, async () => {
  const f = fixture('new MessageChannel().port1.onmessage = () => {};' + good);
  try {
    const entry = join(f.root, "checker-link.mjs"); symlinkSync(script, entry);
    mkdirSync(join(f.root, "node_modules", ".bin"), { recursive: true });
    symlinkSync(script, join(f.root, "node_modules", ".bin", "persona-source-check.mjs"));
    writeFileSync(join(f.root, "package.json"), JSON.stringify({private:true,
      scripts:{check:'node node_modules/.bin/persona-source-check.mjs'}}));
    for (const linked of [entry, join(f.root, "node_modules", ".bin", "persona-source-check.mjs")]) {
      for (let i = 0; i < 2; i++) assert.equal((await cliResult(f.options, {entry:linked})).verdict.code, "PROGRAM_OK");
      const unknown = spawnSync(process.execPath, [linked, "--unknown"], {timeout:5000, encoding:"utf8", env:cleanEnv});
      assert.equal(unknown.status, 2); assert.equal(unknown.stdout.trim().split("\n").length, 1);
      assert.equal(JSON.parse(unknown.stdout).code, "USAGE");
    }
    for (const args of [["--unknown"], ["--root", f.root, "--bundle", f.options.bundle, "--probe-trusted"]]) {
      const npm = spawnSync("npm", ["--silent", "--prefix", f.root, "run", "check", "--", ...args],
        {timeout:10000, encoding:"utf8", env:cleanEnv});
      assert.equal(npm.status, args[0] === "--unknown" ? 2 : 0, npm.stdout + npm.stderr);
      assert.equal(npm.stdout.trim().split("\n").length, 1);
      assert.equal(JSON.parse(npm.stdout).code, args[0] === "--unknown" ? "USAGE" : "PROGRAM_OK");
    }
    symlinkSync(join(f.root, "main.mjs"), join(f.root, "input-link.mjs"));
    const input = await cliResult({...f.options, bundle:"input-link.mjs"}, {entry});
    assert.equal(input.status, 1); assert.equal(input.verdict.code, "SYMLINK");
  } finally { f.close(); }
});

independentTest("canonical source runtime http:false preserves final digest and never enables HTTP", settings, async () => {
  const f = fixture();
  try {
    const manifest = outer(f); manifest.runtime.http = false; manifest.runtime.rpc = "stdio";
    writeFileSync(join(f.root, "manifest.json"), JSON.stringify(manifest));
    const options = {...f.options, manifest:"manifest.json"};
    for (const r of [await check(options), (await cliResult(options)).verdict]) {
      assert.equal(r.code, "PROGRAM_OK"); assert.equal(r.sha256, manifest.runtime.source.sha256);
    }
    for (const http of [true, null, 0, "false", {}]) {
      manifest.runtime.http = http; writeFileSync(join(f.root, "manifest.json"), JSON.stringify(manifest));
      for (const r of [await check(options), (await cliResult(options)).verdict]) assert.equal(r.code, "SOURCE_RUNTIME");
    }
    manifest.runtime.http = false; manifest.runtime.source.sha256 = "0".repeat(64);
    writeFileSync(join(f.root, "manifest.json"), JSON.stringify(manifest));
    assert.equal((await check(options)).code, "SOURCE_DESCRIPTOR");
    await both('export default {id:"example",version:"1.0.0",fetch() {}};', "PROGRAM");
  } finally { f.close(); }
});

independentTest("invalid options and signal getters fail before Worker creation; registration/cleanup always reap", settings, async t => {
  const f = fixture('new MessageChannel().port1.onmessage = () => {};' + good);
  try {
    writeFileSync(join(f.root, "signals.mjs"), `import assert from "node:assert/strict";
      import vm from "node:vm"; import threads from "node:worker_threads"; import {syncBuiltinESMExports} from "node:module";
      let created = 0, terminated = 0;
      const OriginalWorker = threads.Worker;
      threads.Worker = class extends OriginalWorker {
        constructor(...args) { super(...args); created++; }
        async terminate() { try { return await super.terminate(); } finally { terminated++; } }
      };
      syncBuiltinESMExports();
      const {checkSourceBundle} = await import(${JSON.stringify(new URL("./check-source-bundle.mjs", import.meta.url).href)});
      const options = ${JSON.stringify(f.options)};
      const ports = () => process.getActiveResourcesInfo().filter(type => type === "MessagePort").length;
      const baseline = ports();
      const invalid = [{}, null, true, {aborted:false}, {aborted:0,addEventListener(){},removeEventListener(){}},
        {get aborted(){throw Error("getter");},addEventListener(){},removeEventListener(){}},
        {aborted:false,get addEventListener(){throw Error("getter");},removeEventListener(){}},
        {aborted:false,addEventListener(){},get removeEventListener(){throw Error("getter");}}];
      for (let i = 0; i < 2; i++) for (const signal of invalid) {
        const r = await checkSourceBundle({...options,signal}); assert.equal(r.code,"OPTIONS"); assert.equal(r.status,"FAIL");
      }
      assert.equal((await checkSourceBundle({get root(){throw Error("option getter");}})).code,"OPTIONS");
      assert.equal((await checkSourceBundle({...options,get signal(){throw Error("signal option getter");}})).code,"OPTIONS");
      assert.equal((await checkSourceBundle(null)).code,"OPTIONS");
      assert.equal(created,0); assert.equal(terminated,0); assert.equal(ports(),baseline);
      for (const kind of ["register", "cleanup", "cleanup-before-remove", "second-getter", "synchronous-abort-throw"]) {
        for (let i = 0; i < 2; i++) {
          const controller = new AbortController(); let reads = 0, removed = 0;
          const signal = {
            get aborted() { if (++reads > 1 && kind === "second-getter") throw Error("getter"); return false; },
            addEventListener(...args) {
              controller.signal.addEventListener(...args);
              if (kind === "synchronous-abort-throw") { args[1](); throw Error("register after abort"); }
              if (kind === "register") throw Error("register after add");
            },
            removeEventListener(...args) {
              removed++;
              if (kind === "cleanup-before-remove") throw Error("cleanup before remove");
              controller.signal.removeEventListener(...args);
              if (kind === "cleanup") throw Error("cleanup after remove");
            }
          };
          const r = await checkSourceBundle({...options,signal}); assert.equal(r.status,"FAIL"); assert.equal(r.code,"OPTIONS");
          assert.equal(removed,1); assert.equal(created,terminated);
          await new Promise(resolve => setImmediate(resolve)); assert.equal(ports(),baseline);
        }
      }
      const cross = vm.runInNewContext("new AbortController()", {AbortController});
      assert.equal((await checkSourceBundle({...options,signal:cross.signal})).code,"PROGRAM_OK");
      cross.abort(); assert.equal((await checkSourceBundle({...options,signal:cross.signal})).code,"CANCELED");
      const active = vm.runInNewContext("new AbortController()", {AbortController});
      const timer = setTimeout(() => active.abort(),1);
      try { assert.equal((await checkSourceBundle({...options,signal:active.signal})).code,"CANCELED"); }
      finally { clearTimeout(timer); }
      for (let i = 0; i < 3; i++) assert.equal((await checkSourceBundle(options)).code,"PROGRAM_OK");
      await new Promise(resolve => setImmediate(resolve)); assert.equal(ports(),baseline); assert.equal(created,terminated);
      console.log(JSON.stringify({status:"PASS",created,terminated,messagePorts:ports(),baseline}));`);
    const child = await run(process.execPath, ["--unhandled-rejections=strict", join(f.root, "signals.mjs")], 10000);
    assert.equal(child.status, 0, child.stdout + child.stderr);
    assert.equal(child.stdout.trim().split("\n").length, 1);
    const r = JSON.parse(child.stdout); assert.equal(r.status, "PASS"); assert.equal(r.created, r.terminated);
    assert.equal(r.messagePorts, r.baseline);
    t.diagnostic(JSON.stringify(r));
  } finally { f.close(); }
});

for (const name of ["globalThis", "global", "window", "self"]) {
  independentTest(`parenthesized static loader matrix for ${name}`, settings, async () => {
    for (const prefix of [
      `const load = (((${name})))[((("require")))];`,
      `const {[((\`require\`))]: alias} = (((${name})));`,
      `let alias; (({[(("require"))]:alias} = ((${name}))));`,
      `function later({[((\`require\`))]:alias} = ((${name}))) { return alias; }`,
    ]) await both(prefix + good, "IMPORT", {probe:false});
    await both(`const ${name}={require(){return true;}};
      const {[((\`require\`))]:alias}=(${name}); alias();` + good, "PROGRAM_OK");
  });
}

independentTest("synchronous signal protocol refuses promises and thenables without strict rejection crashes", settings, async t => {
  const f = fixture('new MessageChannel().port1.onmessage = () => {};' + good);
  try {
    const entry=join(f.root,"async-signals.mjs");
    writeFileSync(entry, `import assert from "node:assert/strict";
      import vm from "node:vm"; import threads from "node:worker_threads"; import {syncBuiltinESMExports} from "node:module";
      let created=0,terminated=0; const W=threads.Worker;
      threads.Worker=class extends W {constructor(...a){super(...a);created++;}
        async terminate(){try{return await super.terminate();}finally{terminated++;}}}; syncBuiltinESMExports();
      const {checkSourceBundle}=await import(${JSON.stringify(new URL("./check-source-bundle.mjs",import.meta.url).href)});
      const options=${JSON.stringify(f.options)};
      const ports=()=>process.getActiveResourcesInfo().filter(x=>x==="MessagePort").length;
      const baseline=ports(); let removed=0;
      for (const name of ["root","bundle","manifest","probe","timeoutMs","signal"]) {
        const invalid={...options};Object.defineProperty(invalid,name,{get(){return Promise.reject(Error("option value"));}});
        assert.equal((await checkSourceBundle(invalid)).code,"OPTIONS");
        await new Promise(setImmediate);assert.equal(ports(),baseline);assert.equal(created,0);
      }
      assert.equal((await checkSourceBundle(Promise.reject(Error("options value")))).code,"OPTIONS");
      for (const method of ["addEventListener","removeEventListener"]) {
        const signal={aborted:false,addEventListener(){},removeEventListener(){}};
        signal[method]=async()=>{throw Error("declared async must not be invoked");};
        assert.equal((await checkSourceBundle({...options,signal})).code,"OPTIONS");
        await new Promise(setImmediate);assert.equal(ports(),baseline);assert.equal(created,0);
      }
      for (const make of [
        ()=>({aborted:Promise.reject(Error("aborted")),addEventListener(){},removeEventListener(){}}),
        ()=>({aborted:false,get addEventListener(){return Promise.reject(Error("add getter"));},removeEventListener(){}}),
        ()=>({aborted:false,addEventListener(){},get removeEventListener(){return Promise.reject(Error("remove getter"));}}),
        ()=>({aborted:false,get addEventListener(){return Promise.reject(Error("add getter"));},
          get removeEventListener(){throw Error("must not read");}}),
        ()=>Promise.reject(Error("signal value")),
      ]) {
        assert.equal((await checkSourceBundle({...options,signal:make()})).code,"OPTIONS");
        await new Promise(setImmediate);assert.equal(ports(),baseline);assert.equal(created,0);
      }
      const returns=[()=>Promise.reject(Error("native")),async()=>{throw Error("async");},async()=>{},
        ()=>({then(resolve,reject){reject(Error("thenable"));}}),()=>({then(resolve){resolve();}}),
        ()=>({get then(){throw Error("then getter");}}),()=>vm.runInNewContext('Promise.reject(Error("foreign"))'),
        ()=>{const p=Promise.reject(Error("own then"));Object.defineProperty(p,"then",{get(){throw Error("then getter");}});return p;},
        ()=>1];
      for (const method of ["addEventListener","removeEventListener"]) for (const make of returns) {
        const signal={aborted:false,addEventListener(){},removeEventListener(){removed++;}};
        signal[method]=()=>{if(method==="removeEventListener")removed++;return make();};
        const before=removed;
        const r=await checkSourceBundle({...options,signal});
        assert.equal(r.status,"FAIL");assert.equal(r.code,"OPTIONS");assert.equal(removed,before+1);
        await new Promise(setImmediate);assert.equal(created,terminated);assert.equal(ports(),baseline);
      }
      // A secondary listener error cannot overwrite the primary evaluation/timeout/cancellation failure.
      for (const primary of ["EVALUATION","TIMEOUT","CANCELED"]) {
        const controller=new AbortController(); const signal={
          get aborted(){return controller.signal.aborted;},
          addEventListener(...a){controller.signal.addEventListener(...a);if(primary==="CANCELED")controller.abort();},
          removeEventListener(...a){controller.signal.removeEventListener(...a);return Promise.reject(Error("cleanup"));}
        };
        const text=primary==="EVALUATION"?'throw Error("primary");': 'await new Promise(()=>{});';
        const fs=await import("node:fs");fs.writeFileSync(options.root+"/main.mjs",text+${JSON.stringify(good)});
        const r=await checkSourceBundle({...options,timeoutMs:primary==="TIMEOUT"?500:3000,signal});
        assert.equal(r.code,primary);assert.equal(r.status,"FAIL");assert.equal(r.cleanup.signal,"OPTIONS");
        await new Promise(setImmediate);assert.equal(created,terminated);assert.equal(ports(),baseline);
      }
      console.log(JSON.stringify({status:"PASS",cases:35,created,terminated,baseline,ports:ports()}));`);
    const child=await run(process.execPath,["--unhandled-rejections=strict",entry],12000);
    assert.equal(child.status,0,child.stdout+child.stderr);
    assert.equal(child.stdout.trim().split("\n").length,1);
    t.diagnostic(child.stdout.trim());
  } finally { f.close(); }
});

independentTest("cancellation remains observable while successful verification is awaiting cleanup", settings, async t => {
  const f=fixture('new MessageChannel().port1.onmessage=()=>{};'+good);
  try {
    const entry=join(f.root,"cancel-during-cleanup.mjs");
    writeFileSync(entry,`import assert from "node:assert/strict";import threads from "node:worker_threads";
      import {syncBuiltinESMExports} from "node:module";const controller=new AbortController();let created=0,stopped=0;
      const W=threads.Worker;threads.Worker=class extends W {constructor(...a){super(...a);created++;}
        async terminate(){const result=await super.terminate();stopped++;controller.abort();
          await new Promise(resolve=>setTimeout(resolve,100));return result;}};syncBuiltinESMExports();
      const {checkSourceBundle}=await import(${JSON.stringify(new URL("./check-source-bundle.mjs",import.meta.url).href)});
      const ports=()=>process.getActiveResourcesInfo().filter(x=>x==="MessagePort").length;const before=ports();
      const result=await checkSourceBundle({...${JSON.stringify(f.options)},signal:controller.signal});
      await new Promise(setImmediate);assert.equal(result.code,"CANCELED");assert.equal(result.status,"FAIL");
      assert.equal(result.sha256,undefined);assert.equal(created,stopped);assert.equal(ports(),before);
      console.log(JSON.stringify({status:"PASS",created,stopped,before,after:ports()}));`);
    const child=await run(process.execPath,["--unhandled-rejections=strict",entry],5000);
    assert.equal(child.status,0,child.stdout+child.stderr);assert.equal(child.stdout.trim().split("\n").length,1);
    t.diagnostic(child.stdout.trim());
  } finally {f.close();}
});

for (const host of [false,true]) {
  independentTest(`intrinsic contributions inspect the ${host?"host":"VM"} realm without rejecting native baselines or own shadows`, settings, async () => {
    const map=host?'structuredClone({})':'{}';
    const proto=host?'Object.getPrototypeOf(structuredClone({}))':'Object.prototype';
    const cases=[];
    for (const [slot,key] of [["namespaces","service"],["shortcuts","home"],["ui","home"]]) {
      for (const descriptor of ['value:1,enumerable:true','value:1',
        'get(){throw Error("must not invoke getter");}']) {
        cases.push(refused(`const map=${map}; Object.defineProperty(${proto},${JSON.stringify(key)},
          {configurable:true,${descriptor}}); export default {id:"example",version:"1.0.0",${slot}:map};`, "PROGRAM"));
      }
    }
    // Descriptor metadata has missing fields too. Never read those through a
    // polluted prototype while comparing the captured native baseline.
    for (const key of ["get","value"]) cases.push(refused(`const map=${map};
      Object.defineProperty(${proto},${JSON.stringify(key)},{get(){while(true){}}});
      export default {id:"example",version:"1.0.0",namespaces:map};`, "PROGRAM"));
    // A replaced native constructor is a changed intrinsic, not structural metadata.
    for (const slot of ["namespaces","shortcuts","ui"]) cases.push(refused(`const map=${map};
      Object.defineProperty(${proto},"constructor",{configurable:true,writable:true,value:class Polluted{}});
      export default {id:"example",version:"1.0.0",${slot}:map};`, "PROGRAM"));
    // Cases are independent owned workers; slots bound how many run at once.
    await settled(cases);
    await refused(`const map=${map}; ${proto}.toString=1;
      export default {id:"example",version:"1.0.0",namespaces:map};`, "PROGRAM");
    await accepted(`const namespaces=${map},shortcuts=${map},ui=${map};
      Object.defineProperty(${proto},"service",{get(){throw Error("shadowed");}});
      Object.defineProperty(namespaces,"service",{value:()=>{}});
      Object.defineProperty(shortcuts,"service",{value:()=>{}});
      Object.defineProperty(ui,"service",{value:{render(){}}});
      export default {id:"example",version:"1.0.0",namespaces,shortcuts,ui};`);
    await accepted(`export default {id:"example",version:"1.0.0",namespaces:${map},shortcuts:${map},ui:${map}};`);
  });
}

independentTest("direct classes cannot fill call slots; methods and opaque future behavior remain shape-only", settings, async () => {
  await settled([
    'init:class Init{}', 'namespaces:{service:class Service{}}', 'shortcuts:{home:class Shortcut{}}',
    'ui:{home:{render:class Render{}}}', 'ui:{home:{render(){},action:class Action{}}}',
    'ui:{home:{componentPage:true,options:{},root:class Root{},createRenderer(){}}}',
    'ui:{home:{componentPage:true,options:{},root(){},createRenderer:class Factory{}}}',
    'init:class/*comment*/{}',
  ].map(slot=>refused(`export default {id:"example",version:"1.0.0",${slot}};`,"PROGRAM")));
  await accepted(`class Program {id="example";version="1.0.0";init(){}
    namespaces={service(){throw Error("future throw is not invoked");},class(){},
      commented:({class/*comment*/(){}}).class, spaced:({class (){}}).class,
      lineComment:({class// comment
        (){}}).class};
    shortcuts={home:(function run(){}).bind(null)};ui={home:{render(){},action(){}}};}
    export default new Program();`);
  await accepted(`export default {id:"example",version:"1.0.0",namespaces:{
    service:(class Bound{}).bind(null),proxy:new Proxy(()=>{},{apply(){throw Error("future proxy");}})}};`);
});

independentTest("caller species observation errors are structured, not proof of handling the original rejection", settings, async t => {
  const f=fixture('new MessageChannel().port1.onmessage=()=>{};'+good);
  try {
    const entry=join(f.root,"species.mjs");
    writeFileSync(entry,`import assert from "node:assert/strict";import threads from "node:worker_threads";
      import {syncBuiltinESMExports} from "node:module";
      let created=0,started=0,stopped=0;const W=threads.Worker;
      threads.Worker=class extends W {constructor(...a){super(...a);created++;}
        async terminate(){started++;const r=await super.terminate();stopped++;return r;}};
      syncBuiltinESMExports();
      const {checkSourceBundle}=await import(${JSON.stringify(new URL("./check-source-bundle.mjs",import.meta.url).href)});
      const options=${JSON.stringify(f.options)};
      for(const kind of ["constructor","species"])for(const slot of ["option","add","remove"]){
        const p=Promise.reject(Error("caller rejection"));p.catch(()=>{});
        const bad={get [Symbol.species](){throw Error("observation species");}};
        Object.defineProperty(p,"constructor",kind==="constructor"?
          {get(){throw Error("observation constructor");}}:{value:bad});
        const descriptor=Object.getOwnPropertyDescriptor(p,"constructor"), before=stopped;
        const signal={aborted:false,addEventListener(){},removeEventListener(){}};
        if(slot!=="option")signal[slot==="add"?"addEventListener":"removeEventListener"]=()=>p;
        const result=await checkSourceBundle(slot==="option"?{...options,probe:p}:{...options,signal});
        assert.equal(result.code,"OPTIONS");assert.equal(result.status,"FAIL");
        if(slot==="remove")assert.equal(result.cleanup.signal,"OPTIONS");
        assert.equal(stopped,before+(slot==="option"?0:1));assert.equal(started,stopped);assert.equal(created,stopped);
        assert.deepEqual(Object.getOwnPropertyDescriptor(p,"constructor"),descriptor);
      }
      console.log(JSON.stringify({status:"PASS",cases:6,created,started,stopped}));`);
    const child=await run(process.execPath,["--unhandled-rejections=strict",entry],10000);
    assert.equal(child.status,0,child.stdout+child.stderr);
    assert.equal(child.stdout.trim().split("\n").length,1);t.diagnostic(child.stdout.trim());
    // With no caller handler, the same bad Promise fails under Node's strict policy
    // even without the checker. OPTIONS is observable, but is not a rejection shield.
    for(const checker of [false,true]){
      writeFileSync(entry,`${checker?`const {checkSourceBundle}=await import(${JSON.stringify(new URL("./check-source-bundle.mjs",import.meta.url).href)});`:''}
        const p=Promise.reject(Error("caller rejection"));
        Object.defineProperty(p,"constructor",{get(){throw Error("constructor");}});
        ${checker?`console.log(JSON.stringify(await checkSourceBundle({...${JSON.stringify(f.options)},probe:p})));`:''}
        await new Promise(resolve=>setTimeout(resolve,20));`);
      const invalid=await run(process.execPath,["--unhandled-rejections=strict",entry],5000);
      assert.equal(invalid.status,1,invalid.stdout+invalid.stderr);assert.match(invalid.stderr,/caller rejection/);
      if(checker)assert.equal(JSON.parse(invalid.stdout).code,"OPTIONS");
      else assert.equal(invalid.stdout,"");
    }
  } finally {f.close();}
});

function waitForExit(child, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { child.kill("SIGKILL"); reject(Error("child deadline")); }, timeoutMs);
    child.once("error", error => { clearTimeout(timer); reject(error); });
    child.once("close", (code, signal) => { clearTimeout(timer); resolve({ code, signal }); });
  });
}

test("CLI is one JSON verdict and exits after MessageChannel cleanup, with untouched sentinel", settings, async () => {
  const f = fixture('new MessageChannel().port1.onmessage = () => {};' + good);
  const sentinel = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore", env: cleanEnv });
  try {
    for (const probe of [false, true]) {
      const r = spawnSync(process.execPath, [script, "--root", f.root, "--bundle", "main.mjs", ...(probe ? ["--probe-trusted"] : [])],
        { timeout: 5000, encoding: "utf8", env: cleanEnv });
      assert.equal(r.status, 0, r.stderr + r.stdout);
      assert.equal(r.stdout.trim().split("\n").length, 1);
      assert.equal(JSON.parse(r.stdout).status, "PASS");
      assert.equal(sentinel.exitCode, null); process.kill(sentinel.pid, 0);
    }
    const r = spawnSync(process.execPath, [script, "--unknown"], { timeout: 5000, encoding: "utf8", env: cleanEnv });
    assert.equal(r.status, 2); assert.equal(JSON.parse(r.stdout).code, "USAGE");
  } finally { sentinel.kill("SIGTERM"); await waitForExit(sentinel); f.close(); }
});

function waitForReady(child, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    const cleanup = () => { clearTimeout(timer); child.off("message", message); child.off("close", closed); child.off("error", errored); };
    const message = value => { if (value?.type === "source-authoring-ready") { cleanup(); resolve(); } };
    const closed = () => { cleanup(); reject(Error("CLI exited before ready")); };
    const errored = error => { cleanup(); reject(error); };
    const timer = setTimeout(() => { cleanup(); reject(Error("CLI ready deadline")); }, timeoutMs);
    child.on("message", message); child.once("close", closed); child.once("error", errored);
  });
}

test("CLI timeout and ready-handshaken SIGINT/SIGTERM survive slow startup and close all pipes", settings, async () => {
  const f = fixture('new MessageChannel().port1.onmessage = () => {}; await new Promise(() => {});' + good);
  const sentinel = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore", env: cleanEnv });
  try {
    const timed = spawnSync(process.execPath, [script, "--root", f.root, "--bundle", "main.mjs", "--probe-trusted", "--timeout-ms", "500"],
      { timeout: 5000, encoding: "utf8", env: cleanEnv });
    assert.equal(timed.status, 124); assert.equal(JSON.parse(timed.stdout).code, "TIMEOUT");
    process.kill(sentinel.pid, 0);
    const slow = join(f.root, "slow-start.mjs");
    // Inject a known startup CPU stall, then execute the actual CLI. Signals still wait
    // for its real handler-ready IPC event, not for this duration or a guessed delay.
    writeFileSync(slow, `const start = performance.now(); while (performance.now() - start < 800) {}
      process.argv[1] = ${JSON.stringify(script)};
      await import(${JSON.stringify(new URL("./check-source-bundle.mjs", import.meta.url).href)});`);
    const pendingText=readFileSync(join(f.root,"main.mjs"),"utf8");
    for (const [entry,text] of [[script,pendingText],[slow,pendingText],
      [script,'while(true){};'+good],
      [script,'queueMicrotask(function again(){queueMicrotask(again);}); await new Promise(()=>{});'+good]])
      for (const signal of ["SIGINT", "SIGTERM"]) {
      writeFileSync(join(f.root,"main.mjs"),text);
      const child = spawn(process.execPath, [entry, "--root", f.root, "--bundle", "main.mjs", "--probe-trusted", "--timeout-ms", "10000"],
        { stdio: ["ignore", "pipe", "pipe", "ipc"], env: cleanEnv });
      let stdout = "", stderr = "";
      child.stdout.on("data", data => stdout += data); child.stderr.on("data", data => stderr += data);
      // Attach both before awaiting readiness so even an early exit is reaped.
      const ready = waitForReady(child);
      const exit = waitForExit(child);
      try {
        const r = (await Promise.all([ready.then(() => { assert.equal(child.kill(signal), true); }), exit]))[1];
        assert.equal(r.code, signal === "SIGINT" ? 130 : 143, stderr);
        assert.equal(stdout.trim().split("\n").length, 1);
        assert.equal(JSON.parse(stdout).code, "CANCELED");
        assert.equal(JSON.parse(stdout).signal, signal);
        assert.equal(sentinel.exitCode, null); process.kill(sentinel.pid, 0);
      } finally {
        if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
        await exit;
      }
    }
  } finally { sentinel.kill("SIGTERM"); await waitForExit(sentinel); f.close(); }
});

test("recorded signals outrank cleanup deadlines and cleanup failures never claim reaping", settings, async t => {
  const f=fixture('while(true){};'+good);
  const sentinel=spawn(process.execPath,["-e","setInterval(()=>{},1000)"],{stdio:"ignore",env:cleanEnv});
  try {
    for (const mode of ["late","failure"]) {
      const preload=join(f.root,mode+"-cleanup.mjs");
      writeFileSync(preload,`import threads from "node:worker_threads";import {syncBuiltinESMExports} from "node:module";
        const W=threads.Worker;threads.Worker=class extends W {async terminate(){
          const result=await super.terminate();
          if(process.connected)process.send({type:"owned-worker-stopped"});
          ${mode==="late"?'await new Promise(resolve=>setTimeout(resolve,1500));':'throw Error("synthetic cleanup failure");'}
          return result;
        }};syncBuiltinESMExports();`);
      for (const signal of ["SIGINT","SIGTERM"]) {
        const child=spawn(process.execPath,["--import",preload,script,"--root",f.root,"--bundle","main.mjs",
          "--probe-trusted","--timeout-ms","1200"],{stdio:["ignore","pipe","pipe","ipc"],env:cleanEnv});
        let stdout="",stderr="",stopped=false;
        child.stdout.on("data",data=>stdout+=data);child.stderr.on("data",data=>stderr+=data);
        child.on("message",message=>{if(message?.type==="owned-worker-stopped")stopped=true;});
        const ready=waitForReady(child),exit=waitForExit(child);
        try {
          const r=(await Promise.all([ready.then(()=>assert.equal(child.kill(signal),true)),exit]))[1];
          assert.equal(r.code,signal==="SIGINT"?130:143,stdout+stderr);assert.equal(r.signal,null);
          assert.equal(stdout.trim().split("\n").length,1);
          const verdict=JSON.parse(stdout);assert.equal(verdict.status,"FAIL");assert.equal(verdict.signal,signal);
          assert.equal(verdict.code,mode==="late"?"TIMEOUT":"CANCELED");
          if(mode==="failure")assert.equal(verdict.cleanup.worker,"WORKER");
          assert.equal(stopped,true);assert.equal(sentinel.exitCode,null);process.kill(sentinel.pid,0);
          t.diagnostic(JSON.stringify({mode,signal,exit:r.code,code:verdict.code,stopped}));
        } finally {
          if(child.exitCode===null && child.signalCode===null)child.kill("SIGKILL");
          await exit;
        }
      }
    }
  } finally { sentinel.kill("SIGTERM");await waitForExit(sentinel);f.close(); }
});

// Registered last, so the serial tests above never overlap these cases.
describe("independent source authoring cases", { concurrency: slotLimit > 2 ? 2 : 1 }, () => {
  for (const [name, options, fn] of independentTests) test(name, options, fn);
});
