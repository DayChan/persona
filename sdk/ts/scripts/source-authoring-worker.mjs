// Owned disposable authoring worker. Never load an untrusted artifact here.
import ts from "typescript";
import vm from "node:vm";
import { webcrypto } from "node:crypto";
import { parentPort, workerData } from "node:worker_threads";

const fail = code => { throw Object.assign(new Error(code), { authoringCode: code }); };

function staticChecks(text) {
  const ast = ts.createSourceFile("bundle.mjs", text, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS);
  if (ast.parseDiagnostics.length) fail("SYNTAX");
  // Bind exactly one file: no standard libraries, module resolution or filesystem reads.
  const options = { allowJs: true, noLib: true, noResolve: true };
  const host = {
    getSourceFile: name => name === "bundle.mjs" ? ast : undefined,
    getDefaultLibFileName: () => "", writeFile: () => {}, getCurrentDirectory: () => "",
    getDirectories: () => [], fileExists: name => name === "bundle.mjs",
    readFile: name => name === "bundle.mjs" ? text : undefined,
    getCanonicalFileName: name => name, useCaseSensitiveFileNames: () => true, getNewLine: () => "\n",
  };
  const checker = ts.createProgram(["bundle.mjs"], options, host).getTypeChecker();
  function globalRequire(node) {
    if (!ts.isIdentifier(node) || node.text !== "require") return false;
    const parent = node.parent;
    if ((ts.isPropertyAccessExpression(parent) && parent.name === node)
        || ((ts.isPropertyAssignment(parent) || ts.isMethodDeclaration(parent)
            || ts.isGetAccessorDeclaration(parent) || ts.isSetAccessorDeclaration(parent)
            || ts.isPropertyDeclaration(parent)) && parent.name === node)
        || (ts.isBindingElement(parent) && parent.propertyName === node)
        || ts.isLabeledStatement(parent) || ts.isBreakStatement(parent) || ts.isContinueStatement(parent)) return false;
    const symbol = ts.isShorthandPropertyAssignment(parent)
      ? checker.getShorthandAssignmentValueSymbol(parent) : checker.getSymbolAtLocation(node);
    // The JS binder synthesizes CommonJS require symbols: only real declarations shadow them.
    return !symbol?.declarations?.length;
  }
  const unparen = node => {
    while (node && ts.isParenthesizedExpression(node)) node = node.expression;
    return node;
  };
  const globalObject = node => node && ts.isIdentifier(unparen(node))
    && ["globalThis", "global", "window", "self"].includes(unparen(node).text)
    && !checker.getSymbolAtLocation(unparen(node))?.declarations?.length;
  // One literal-property rule for access, declarations and assignment patterns.
  // Bare identifiers name properties, but identifiers inside [] remain computed aliases.
  const requireName = (node, bareIdentifier = true) => {
    node = unparen(node);
    if (!node) return false;
    if (ts.isComputedPropertyName(node)) return requireName(node.expression, false);
    return (ts.isStringLiteralLike(node) || (bareIdentifier && ts.isIdentifier(node))) && node.text === "require";
  };
  function visit(node) {
    if (ts.isImportDeclaration(node) || ts.isImportEqualsDeclaration(node)
        || (ts.isExportDeclaration(node) && node.moduleSpecifier)
        || node.kind === ts.SyntaxKind.ImportKeyword
        || (ts.isMetaProperty(node) && node.keywordToken === ts.SyntaxKind.ImportKeyword)
        || globalRequire(node)) fail("IMPORT");
    // Global-object loader references are not ordinary object properties.
    if ((ts.isPropertyAccessExpression(node) || ts.isElementAccessExpression(node))
        && globalObject(node.expression)
        && requireName(ts.isPropertyAccessExpression(node) ? node.name : node.argumentExpression,
          ts.isPropertyAccessExpression(node))) fail("IMPORT");
    // Direct literal destructuring reads the same global property; do not chase aliases.
    if ((ts.isVariableDeclaration(node) || ts.isParameter(node)) && ts.isObjectBindingPattern(node.name)
        && globalObject(node.initializer)
        && node.name.elements.some(element => !element.dotDotDotToken && requireName(element.propertyName ?? element.name))) fail("IMPORT");
    if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.EqualsToken
        && ts.isObjectLiteralExpression(unparen(node.left)) && globalObject(node.right)
        && unparen(node.left).properties.some(property => requireName(property.name))) fail("IMPORT");
    ts.forEachChild(node, visit);
  }
  visit(ast);
}

// Executed in the guest realm. Follow SDK property lookup without invoking getters/handlers.
function validateProgram(program, identity, objectBaselines) {
  const data = (value, contributions = true) => {
    if (value === null || typeof value !== "object" || Array.isArray(value)) throw Error("PROGRAM");
    const result = Object.create(null), seen = new Set();
    for (let current = value; current; current = Object.getPrototypeOf(current)) {
      const baseline = objectBaselines.find(item => item.prototype === current)?.descriptors;
      // Program/page/options are shapes, not contribution maps. Only their known
      // SDK fields read intrinsic members; read() below follows the entire chain.
      if (baseline && !contributions) continue;
      for (const [key, descriptor] of Object.entries(Object.getOwnPropertyDescriptors(current))) {
        if (seen.has(key)) continue;
        seen.add(key);
        // Unchanged native Object.prototype members are not contributions. Added or
        // changed members are, in either realm; nearer data members still shadow them.
        const original = baseline && Object.getOwnPropertyDescriptor(baseline, key)?.value;
        if (original && ["value", "get", "set", "writable", "enumerable", "configurable"]
          .every(field => Object.hasOwn(descriptor, field) === Object.hasOwn(original, field)
            && (!Object.hasOwn(descriptor, field) || descriptor[field] === original[field]))) continue;
        if (!Object.hasOwn(descriptor, "value")) throw Error("PROGRAM");
        // Class/function prototypes carry structural constructor metadata. Identify
        // the back-reference without reading a user getter; an arbitrary hidden
        // member named constructor is still an SDK contribution.
        if (!baseline && current !== value && key === "constructor" && !descriptor.enumerable
            && typeof descriptor.value === "function"
            && Object.getOwnPropertyDescriptor(descriptor.value, "prototype")?.value === current) continue;
        result[key] = descriptor.value;
      }
    }
    return result;
  };
  const read = (value, key) => {
    for (let current = value; current; current = Object.getPrototypeOf(current)) {
      const descriptor = Object.getOwnPropertyDescriptor(current, key);
      if (descriptor) {
        if (!Object.hasOwn(descriptor, "value")) throw Error("PROGRAM");
        return descriptor.value;
      }
    }
  };
  const shape = (value, keys) => {
    data(value, false);
    return Object.fromEntries(keys.map(key => [key, read(value, key)]));
  };
  // Do not invoke handlers to predict behavior. Direct class syntax is recognizable;
  // opaque bound/native functions and proxies do not offer the same shape evidence.
  const callable = value => {
    if (typeof value !== "function") return false;
    const source = Function.prototype.toString.call(value);
    // A method named class may have trivia before its parameter list. It is still
    // callable; a class declaration/expression cannot start with that parameter list.
    return !/^class(?:\s|\/[/*]|\{)/.test(source)
      || /^class(?:\s|\/\*[\s\S]*?\*\/|\/\/[^\r\n\p{Zl}\p{Zp}]*(?:[\r\n\p{Zl}\p{Zp}]|$))*\(/u.test(source);
  };
  const optionalFunction = value => value === undefined || callable(value);
  const functionMap = value => value === undefined || Object.values(data(value)).every(callable);
  const page = value => {
    const p = shape(value, ["componentPage", "options", "root", "createRenderer", "render", "action"]);
    if (p.componentPage === true) {
      data(p.options, false);
      return callable(p.root) && callable(p.createRenderer);
    }
    return callable(p.render) && optionalFunction(p.action);
  };
  const p = shape(program, ["id", "version", "init", "fetch", "namespaces", "shortcuts", "ui"]);
  if (typeof p.id !== "string" || !p.id || typeof p.version !== "string" || !p.version
      || (identity && (p.id !== identity.id || p.version !== identity.version))
      || !optionalFunction(p.init) || p.fetch !== undefined || !functionMap(p.namespaces)
      || !functionMap(p.shortcuts) || (p.ui !== undefined && !Object.values(data(p.ui)).every(page))) throw Error("PROGRAM");
  // manifest is optional. An outer completed-bundle hash is not a Program self-hash.
  return true;
}

// The VM owns language intrinsics/literals; Node owns Web API results. Treat their
// corresponding intrinsic prototypes as one instanceof family, without traversing
// or rewriting result graphs (cycles, transferables, brands and identity survive).
// This is a comparison bridge, not full cross-realm prototype identity or isolation.
function installInstanceChecks(hostIntrinsics) {
  const ordinary = Function.prototype[Symbol.hasInstance];
  for (const [name, descriptor] of Object.entries(Object.getOwnPropertyDescriptors(globalThis))) {
    const intrinsic = descriptor.value, host = hostIntrinsics[name];
    if (typeof intrinsic !== "function" || typeof host !== "function" || !intrinsic.prototype
        || !host.prototype || intrinsic === host || Object.hasOwn(intrinsic, Symbol.hasInstance)) continue;
    Object.defineProperty(intrinsic, Symbol.hasInstance, {
      configurable: true,
      value: function (value) {
        // Inherited @@hasInstance must not make a host base instance a guest subclass.
        return ordinary.call(this, value) || (this === intrinsic && ordinary.call(host, value));
      },
    });
  }
}

async function check() {
  const { text, identity, probe } = workerData;
  staticChecks(text);
  const globals = {};
  const webNames = `AbortController AbortSignal atob btoa Blob ByteLengthQueuingStrategy
    CompressionStream CountQueuingStrategy Crypto CryptoKey SubtleCrypto DecompressionStream DOMException Event EventTarget File
    FormData Headers MessageChannel MessageEvent MessagePort CustomEvent performance Performance PerformanceEntry
    PerformanceMark PerformanceMeasure PerformanceObserver PerformanceObserverEntryList PerformanceResourceTiming ReadableStream
    ReadableStreamBYOBReader ReadableStreamDefaultReader ReadableByteStreamController
    ReadableStreamDefaultController Request Response TextDecoder TextDecoderStream TextEncoder
    TextEncoderStream TransformStream TransformStreamDefaultController URL URLPattern URLSearchParams
    WritableStream WritableStreamDefaultController WritableStreamDefaultWriter structuredClone queueMicrotask
    ArrayBuffer SharedArrayBuffer DataView Int8Array Uint8Array Uint8ClampedArray Int16Array Uint16Array
    Int32Array Uint32Array Float16Array Float32Array Float64Array BigInt64Array BigUint64Array`;
  for (const name of webNames.trim().split(/\s+/)) {
    // Share the binary constructors used by host Web APIs so their results have the
    // same instanceof/constructor semantics as ordinary initialization in this Node.
    // Optional APIs follow the selected Node toolchain; do not reject every bundle for one absent API.
    if (globalThis[name] !== undefined) globals[name] = globalThis[name];
  }
  Object.assign(globals, { crypto: webcrypto, setTimeout, clearTimeout, setInterval, clearInterval,
    fetch: async () => { throw Error("Authoring probe has no network IO; use the confined Deno host"); } });
  const context = vm.createContext(globals, { codeGeneration: { strings: false, wasm: false } });
  const hostIntrinsics = Object.create(null);
  for (const [name, descriptor] of Object.entries(Object.getOwnPropertyDescriptors(globalThis))) {
    if (typeof descriptor.value === "function" && descriptor.value.prototype) hostIntrinsics[name] = descriptor.value;
  }
  context.__authoringIntrinsics = hostIntrinsics;
  vm.runInContext(`(${installInstanceChecks.toString()})(__authoringIntrinsics)`, context);
  delete context.__authoringIntrinsics;
  // Snapshot both realms before guest evaluation can add/change intrinsic contributions.
  context.__authoringObjectBaselines = [
    { prototype: Object.prototype, descriptors: Object.getOwnPropertyDescriptors(Object.prototype) },
    vm.runInContext("({ prototype: Object.prototype, descriptors: Object.getOwnPropertyDescriptors(Object.prototype) })", context),
  ];
  let module;
  try { module = new vm.SourceTextModule(text, { context }); }
  catch { fail("SYNTAX"); }
  // Constructing a module checks actual ECMAScript grammar, without running initialization.
  await module.link(() => fail("IMPORT"));
  if (!Object.getOwnPropertyNames(module.namespace).includes("default")) fail("DEFAULT_EXPORT");
  if (!probe) return { status: "PASS", code: "STATIC_OK", phase: "static", program: "not-probed" };
  // The owned lifecycle and CLI handlers are ready BEFORE guest initialization.
  // module.evaluate() itself can synchronously block forever; it is not readiness.
  parentPort.postMessage({ type: "authoring-ready" });
  await module.evaluate();
  context.__authoringIdentity = identity;
  const validate = () => {
    // Read the live default binding again, not a snapshot from the first inspection.
    context.__authoringProgram = module.namespace.default;
    try { vm.runInContext(`(${validateProgram.toString()})(__authoringProgram, __authoringIdentity, __authoringObjectBaselines)`, context); }
    catch { fail("PROGRAM"); }
  };
  validate();
  // Observe queued microtasks and short asynchronous initialization errors. Not arbitrary future behavior.
  await new Promise(resolve => setTimeout(resolve, 50));
  validate();
  return { status: "PASS", code: "PROGRAM_OK", phase: "trusted-probe", program: "verified" };
}

// Keep the owned control port referenced while an unresolved top-level await is pending.
// Otherwise Node may abandon evaluation before the supervisor's deterministic deadline.
parentPort.on("message", () => {});
let sent = false;
function send(result) {
  if (!sent) { sent = true; parentPort.postMessage(result); }
  // The supervisor terminates/reaps this worker, including every port and timer.
}
process.on("unhandledRejection", () => send({ status: "FAIL", code: "EVALUATION", phase: "authoring" }));
try { send(await check()); }
catch (error) {
  send({ status: "FAIL", code: ["SYNTAX", "IMPORT", "DEFAULT_EXPORT", "PROGRAM"].includes(error.authoringCode)
    ? error.authoringCode : "EVALUATION", phase: "authoring" });
}
