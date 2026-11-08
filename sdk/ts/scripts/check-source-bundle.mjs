// Offline authoring only. Neither a worker nor a VM is a security boundary.
import { createHash } from "node:crypto";
import { constants, closeSync, fstatSync, lstatSync, openSync, readSync, realpathSync } from "node:fs";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Worker } from "node:worker_threads";
import { types } from "node:util";

// EventTarget's listener methods are synchronous and return undefined. Observe a
// nonconforming return before refusing it, including cross-realm native Promises
// with a replaced/throwing `then` getter. No process-wide rejection handler is used.
// Intrinsic then still reads caller constructor/species. If that throws before
// handler installation, surrounding OPTIONS/cleanup paths handle our observation
// error, but the original rejection remains the caller's responsibility.
function observeNonconforming(value) {
  if (types.isPromise(value)) Promise.prototype.then.call(value, () => {}, () => {});
  else Promise.resolve(value).then(() => {}, () => {});
}

function synchronousListener(call) {
  const value = call();
  if (value === undefined) return;
  observeNonconforming(value);
  reject("OPTIONS");
}

const MAX_BUNDLE = 16 << 20;
const MAX_MANIFEST = 1 << 20;
const failure = (code, phase = "input") => ({ status: "FAIL", code, phase });
const reject = code => { throw Object.assign(new Error(code), { code }); };
const object = value => value !== null && typeof value === "object" && !Array.isArray(value);

function relativePath(path) {
  return typeof path === "string" && path.length > 0 && !isAbsolute(path)
    && !/[\\\x00\r\n]/.test(path) && path.split("/").every(part => part && part !== "." && part !== "..");
}

function readContained(root, relative, limit) {
  if (!relativePath(relative)) reject("PATH");
  // Inspect lexical ancestors before opening; do not silently resolve links.
  const path = join(root, relative);
  let current = path;
  for (;;) {
    const stat = lstatSync(current);
    if (stat.isSymbolicLink()) reject("SYMLINK");
    if (current === path ? !stat.isFile() : !stat.isDirectory()) reject("FILE_TYPE");
    const parent = dirname(current);
    if (parent === current) break;
    current = parent;
  }
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const stat = fstatSync(fd);
    if (!stat.isFile()) reject("FILE_TYPE");
    if (!stat.size || stat.size > limit) reject("SIZE");
    // Read at most the limit plus one byte, even if an author replaces/grows a file.
    const bytes = Buffer.alloc(stat.size + 1);
    let count = 0;
    while (count < bytes.length) {
      const n = readSync(fd, bytes, count, bytes.length - count, null);
      if (!n) break;
      count += n;
    }
    if (count !== stat.size) reject("INPUT_CHANGED");
    return bytes.subarray(0, count);
  } finally { closeSync(fd); }
}

function manifestIdentity(bytes, bundle, sha256) {
  let manifest;
  try { manifest = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)); }
  catch { reject("MANIFEST_JSON"); }
  if (!object(manifest) || manifest.manifest_version !== 1
      || typeof manifest.id !== "string" || !manifest.id
      || typeof manifest.version !== "string" || !manifest.version) reject("MANIFEST_IDENTITY");
  const runtime = manifest.runtime;
  // This intentionally checks the source profile, not the entire extension schema.
  if (!object(runtime) || runtime.mode !== "deno"
      || Object.keys(runtime).some(key => !["mode", "source", "rpc", "resources", "http"].includes(key))
      || (runtime.http !== undefined && runtime.http !== false)
      || (runtime.rpc !== undefined && runtime.rpc !== "stdio")) reject("SOURCE_RUNTIME");
  const source = runtime.source;
  if (!object(source) || Object.keys(source).some(key => !["entry", "bundle", "sha256"].includes(key))
      || !relativePath(source.entry) || !/\.tsx?$/.test(source.entry)
      || source.bundle !== bundle || !/\.mjs$/.test(source.bundle)
      || typeof source.sha256 !== "string" || !/^[a-f0-9]{64}$/.test(source.sha256)
      || source.sha256 !== sha256) reject("SOURCE_DESCRIPTOR");
  if (runtime.resources !== undefined) {
    const resources = runtime.resources;
    const bounds = { memory_mb: [64, 8192], cpu_percent: [10, 800] };
    if (!object(resources) || Object.entries(resources).some(([key, value]) => !Object.hasOwn(bounds, key)
        || !Number.isInteger(value) || value < bounds[key][0] || value > bounds[key][1])) reject("RESOURCES");
  }
  return { id: manifest.id, version: manifest.version };
}

/** Read-only checks of a trusted local build. Cancellation owns only this call's worker. */
export async function checkSourceBundle(options = {}) {
  return inspectSourceBundle(options);
}

async function inspectSourceBundle(options = {}, ready) {
  if (![22, 24].includes(Number(process.versions.node.split(".")[0]))) return failure("NODE_VERSION");
  let root, bundle, manifest, probe, timeoutMs, signal;
  try {
    if (types.isPromise(options)) { observeNonconforming(options); reject("OPTIONS"); }
    const option = (name, fallback) => {
      const value = options[name];
      // A promise-valued getter is invalid too. Observe it before another getter
      // can throw, rather than leaving a rejected option value in the caller realm.
      if (types.isPromise(value)) { observeNonconforming(value); reject("OPTIONS"); }
      return value === undefined ? fallback : value;
    };
    root = option("root"); bundle = option("bundle"); manifest = option("manifest");
    probe = option("probe", false); timeoutMs = option("timeoutMs", 2000); signal = option("signal");
    if (signal !== undefined) {
      // Structural AbortSignal support includes other realms. Capture methods once;
      // malformed values and throwing option/signal getters fail before creating a worker.
      const original = signal;
      if (types.isPromise(original)) { observeNonconforming(original); reject("OPTIONS"); }
      const method = name => {
        const value = original[name];
        // Validate each getter result before reading another getter that could throw.
        if (typeof value !== "function") { observeNonconforming(value); reject("OPTIONS"); }
        if (types.isAsyncFunction(value)) reject("OPTIONS");
        return value;
      };
      const add = method("addEventListener"), remove = method("removeEventListener");
      const aborted = () => {
        const value = original.aborted;
        if (typeof value !== "boolean") { observeNonconforming(value); reject("OPTIONS"); }
        return value;
      };
      signal = { aborted, add: listener => synchronousListener(() => add.call(original, "abort", listener, { once: true })),
        remove: listener => synchronousListener(() => remove.call(original, "abort", listener)) };
      if (signal.aborted()) return failure("CANCELED");
    }
  } catch { return failure("OPTIONS"); }
  if (typeof root !== "string" || !isAbsolute(root) || resolve(root) !== root
      || /[\x00\r\n]/.test(root) || (process.platform !== "win32" && /\\/.test(root))
      || !relativePath(bundle) || !bundle.endsWith(".mjs")
      || typeof probe !== "boolean" || !Number.isInteger(timeoutMs) || timeoutMs < 100 || timeoutMs > 10000) return failure("OPTIONS");
  const deadline = performance.now() + timeoutMs;
  let bytes, text, identity, sha256;
  try {
    bytes = readContained(root, bundle, MAX_BUNDLE);
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    sha256 = createHash("sha256").update(bytes).digest("hex");
    if (manifest !== undefined) identity = manifestIdentity(readContained(root, manifest, MAX_MANIFEST), bundle, sha256);
  } catch (error) {
    if (performance.now() >= deadline) return failure("TIMEOUT", "authoring");
    return failure(["PATH", "SYMLINK", "FILE_TYPE", "SIZE", "INPUT_CHANGED", "MANIFEST_JSON", "MANIFEST_IDENTITY",
      "SOURCE_RUNTIME", "SOURCE_DESCRIPTOR", "RESOURCES"].includes(error.code) ? error.code : "INPUT");
  }
  if (performance.now() >= deadline) return failure("TIMEOUT", "authoring");
  const settled = await runWorker({ text, identity, probe }, deadline, signal, ready);
  const result = performance.now() >= deadline
    ? { ...failure("TIMEOUT", "authoring"), ...(settled.cleanup ? { cleanup: settled.cleanup } : {}) } : settled;
  return { ...result, node: process.versions.node, ...(result.status === "PASS" ? { sha256 } : {}) };
}

function runWorker(data, deadline, signal, ready) {
  const admit = result => performance.now() >= deadline ? failure("TIMEOUT", "authoring") : result;
  return new Promise(resolveResult => {
    let worker;
    try {
      worker = new Worker(new URL("./source-authoring-worker.mjs", import.meta.url), {
        workerData: data, execArgv: ["--experimental-vm-modules"], env: {},
        stdout: true, stderr: true,
        // Cooperative V8 heap guard only: NOT a hard RSS or security limit.
        resourceLimits: { maxOldGenerationSizeMb: 128 },
      });
    } catch {
      // Permission denial and other setup errors use the library's verdict contract too.
      resolveResult(admit(failure("TOOL", "authoring")));
      return;
    }
    worker.stdout.resume(); worker.stderr.resume();
    let finishing = false, workerError = false, registrationError = false, listening = false, aborted = false;
    const finish = result => {
      if (finishing) return;
      finishing = true;
      clearTimeout(timer);
      // Defer cleanup until synchronous registration has returned: an abort callback
      // may run inside addEventListener, which can still throw or return a Promise.
      void Promise.resolve().then(async () => {
        result = admit(registrationError ? failure("OPTIONS") : result);
        const cleanup = {};
        // A secondary failure must not replace an existing primary failure or
        // claim reaping. Keep cancellation observable until termination settles.
        try { await worker.terminate(); }
        catch { cleanup.worker = "WORKER"; }
        try { if (listening) signal.remove(abort); }
        catch { cleanup.signal = "OPTIONS"; }
        if (result.status === "PASS" && aborted) result = failure("CANCELED", "authoring");
        if (result.status === "PASS") {
          if (cleanup.signal) result = failure("OPTIONS");
          else if (cleanup.worker || workerError) result = failure("WORKER", "authoring");
        }
        resolveResult({ ...admit(result), ...(Object.keys(cleanup).length ? { cleanup } : {}) });
      });
    };
    const abort = () => { aborted = true; finish(failure("CANCELED", "authoring")); };
    const timer = setTimeout(() => { finish(failure("TIMEOUT", "authoring")); }, Math.max(0, deadline - performance.now()));
    worker.on("error", () => { workerError = true; void finish(failure("WORKER", "authoring")); });
    worker.on("exit", () => { if (!finishing) void finish(failure("WORKER_EXIT", "authoring")); });
    worker.on("message", result => {
      if (result.type === "authoring-ready") {
        if (performance.now() >= deadline) void finish(failure("TIMEOUT", "authoring"));
        else if (!finishing) ready?.();
      } else void finish(result);
    });
    try {
      if (signal) {
        listening = true; // Even a registration that throws after adding needs cleanup.
        signal.add(abort);
        if (signal.aborted()) abort();
      }
    } catch { registrationError = true; finish(failure("OPTIONS")); }
  });
}

async function cli(args) {
  const options = {};
  for (let i = 0; i < args.length; i++) {
    const key = args[i];
    if (key === "--probe-trusted") {
      if (options.probe) return failure("USAGE");
      options.probe = true;
    } else if (["--root", "--bundle", "--manifest", "--timeout-ms"].includes(key)) {
      const name = { "--root": "root", "--bundle": "bundle", "--manifest": "manifest", "--timeout-ms": "timeoutMs" }[key];
      if (options[name] !== undefined || args[i + 1] === undefined) return failure("USAGE");
      options[name] = key === "--timeout-ms" ? Number(args[++i]) : args[++i];
    } else return failure("USAGE");
  }
  const controller = new AbortController();
  let interrupted;
  const interrupt = name => { interrupted = name; controller.abort(); };
  const sigint = () => interrupt("SIGINT"), sigterm = () => interrupt("SIGTERM");
  process.on("SIGINT", sigint); process.on("SIGTERM", sigterm);
  try {
    // Parent-only IPC after handlers are installed and before guest initialization.
    // The lifecycle is cancelable even when synchronous evaluation never yields.
    const ready = () => {
      if (process.connected) process.send({ type: "source-authoring-ready" }, () => {});
    };
    const result = await inspectSourceBundle({ ...options, signal: controller.signal }, ready);
    if (interrupted) result.signal = interrupted;
    return result;
  } finally {
    process.off("SIGINT", sigint); process.off("SIGTERM", sigterm);
    if (process.connected) process.disconnect();
  }
}

function isCLI() {
  // npm bin/tool-entry links are allowed; input bundle links remain forbidden.
  try { return !!process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url)); }
  catch { return false; } // Library callers may have no filesystem entry (e.g. node -e).
}

if (isCLI()) {
  let result;
  try { result = await cli(process.argv.slice(2)); }
  catch { result = failure("TOOL", "authoring"); }
  process.stdout.write(JSON.stringify(result) + "\n");
  process.exitCode = result.signal === "SIGINT" ? 130 : result.signal === "SIGTERM" ? 143
    : result.status === "PASS" ? 0 : result.code === "CANCELED" ? 130 : result.code === "TIMEOUT" ? 124
      : ["USAGE", "OPTIONS", "NODE_VERSION", "TOOL"].includes(result.code) ? 2 : 1;
}
