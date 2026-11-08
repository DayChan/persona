import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { test } from "node:test";
import { ownedCleanup } from "./extension-mirrors-cleanup.mjs";

const source = await readFile(new URL("./extension-mirrors.mjs", import.meta.url), "utf8");
const body = source.match(/\} finally \{([\s\S]*)\n\}\s*$/)[1];
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
async function runFinally(resources, timedOut = false) {
  let cleared = false;
  const lines = [];
  const process = {};
  await new AsyncFunction("cleanup", "process", "deadline", "clearTimeout", "timedOut", "verdict", "console", body)(
    ownedCleanup(() => resources), process, {}, () => { cleared = true; }, timedOut, "WEB_EXTENSION_MIRRORS PASS", {
      error: (line) => lines.push(line), log: (line) => lines.push(line),
    },
  );
  return { cleared, lines, process };
}
async function ownServer() {
  const mock = createServer((_req, res) => res.end("synthetic"));
  await new Promise((done) => mock.listen(0, "127.0.0.1", done));
  return mock;
}
async function release(mock) {
  mock.closeAllConnections();
  if (mock.listening) await new Promise((done) => mock.close(done));
}

// Execute the driver's actual finalizer, not a second copy of its deadline policy.
test("browser close rejection still releases Vite/mock and retains the deadline", async () => {
  let viteClosed = false;
  const mock = await ownServer();
  try {
    const result = await runFinally({
      browser: { close: async () => { throw new Error("Synthetic browser close failure"); } },
      vite: { close: async () => { viteClosed = true; } }, mock,
    });
    assert.equal(viteClosed, true);
    assert.equal(mock.listening, false);
    assert.equal(result.cleared, false);
    assert.equal(result.process.exitCode, 1);
    assert.match(result.lines[0], /^WEB_EXTENSION_MIRRORS FAIL cleanup/);
  } finally { await release(mock); }
});

test("Vite close rejection does not skip mock release", async () => {
  const mock = await ownServer();
  try {
    const result = await runFinally({ vite: { close: async () => { throw new Error("Synthetic Vite close failure"); } }, mock });
    assert.equal(mock.listening, false);
    assert.equal(result.cleared, false);
  } finally { await release(mock); }
});

test("connection cleanup rejection still closes the owned mock listener", async () => {
  const mock = await ownServer();
  const closeConnections = mock.closeAllConnections.bind(mock);
  mock.closeAllConnections = () => { throw new Error("Synthetic connection cleanup failure"); };
  try {
    const result = await runFinally({ mock });
    assert.equal(mock.listening, false);
    assert.equal(result.cleared, false);
  } finally {
    mock.closeAllConnections = closeConnections;
    await release(mock);
  }
});

test("a stalled browser close cannot stop other resources from closing", { timeout: 2_000 }, async () => {
  const mock = await ownServer();
  let finish;
  let browserCloses = 0;
  let viteClosed = false;
  const cleanup = ownedCleanup(() => ({
    browser: { close: () => { browserCloses++; return new Promise((done) => { finish = done; }); } },
    vite: { close: async () => { viteClosed = true; } }, mock,
  }));
  const closed = new Promise((done) => mock.once("close", done));
  const pending = cleanup();
  try {
    await closed;
    assert.equal(viteClosed, true);
    assert.equal(mock.listening, false);
    assert.equal(cleanup(), pending, "Deadline shares the in-flight cleanup, not a new close attempt");
    finish();
    assert.deepEqual(await pending, []);
    assert.equal(browserCloses, 1);
  } finally {
    finish?.();
    await release(mock);
  }
});

test("only full release clears the deadline and reports PASS", async () => {
  const mock = await ownServer();
  try {
    const result = await runFinally({ browser: { close: async () => {} }, vite: { close: async () => {} }, mock });
    assert.equal(mock.listening, false);
    assert.equal(result.cleared, true);
    assert.deepEqual(result.lines, ["WEB_EXTENSION_MIRRORS PASS"]);
  } finally { await release(mock); }
});

test("TIMEOUT stays the final verdict even when cleanup rejects", async () => {
  const result = await runFinally({ browser: { close: async () => { throw new Error("Synthetic late close failure"); } } }, true);
  assert.equal(result.cleared, false);
  assert.deepEqual(result.lines, [], "Finalizer must not overwrite the deadline's TIMEOUT with FAIL");
  const timeoutBody = source.match(/const deadline = setTimeout\(\(\) => \{([\s\S]*?)\n\}, 180_000\);/)[1];
  let fallback;
  const lines = [];
  const exits = [];
  await new AsyncFunction("cleanup", "process", "setTimeout", "clearTimeout", "timedOut", "console", timeoutBody)(
    () => new Promise(() => {}), { exit: (code) => exits.push(code) }, (f) => { fallback = f; }, () => {}, false, { error: (line) => lines.push(line) },
  );
  fallback();
  assert.deepEqual(lines, ["WEB_EXTENSION_MIRRORS TIMEOUT"]);
  assert.deepEqual(exits, [1], "An unresolved close remains bounded by the force-exit timer");
});
