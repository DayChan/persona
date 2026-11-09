# TypeScript extension SDK: source authoring

The SDK runtime and public types live in `src/`. Source programs default-export a
`Program`; the manager supplies the stdio adapter. Executable and remote programs
retain their existing APIs. See `docs/extensions/extensions.md` in the Persona
repository for the source runtime and its production admission rules.

## Exact integer request params (explicit opt-in)

`withExactParams((method, legacyParams, call) => ...)` keeps the legacy parsed
`Number` argument and exposes original namespace RPC request params as
`call.rawParams` / `call.params`. Events have no call metadata; replies and
serialization are unchanged. Use `call.params?.field("args")` for a Go
`json.RawMessage` struct field (case-folded names, last duplicate including null),
then `int64()` or `int64Fields(["topic", "replies"])` for lexical Go signed-64
integers as bigint. `member(name)` instead uses exact decoded map keys.

Decimal/exponent tokens such as `1.0` / `1e0` are rejected even when Number rounds
them to an integer. `9007199254740993` and both signed-64 bounds remain valid.
Struct decoding keeps ordered duplicate/null/error behavior, ignores unknown
fields, uses iterative scanning and null-prototype results, and bounds the opt-in
raw text to 8 MiB of UTF-16 text. This is not JSON Schema integer validation or an
end-to-end serialization guarantee. Preserve an existing string-ID branch and
business validation when adopting the API. Full semantics and a default-export
Program example are in `docs/extensions/extensions.md` ("Opt-in exact JSON integer
request params"). The portable suite tests raw input against independently
bundled official host and default Program modules under Node without Bun globals.

## Exact initialization params (explicit opt-in)

`withExactInit((init, hub, call) => ...)` preserves the ordinary parsed `Init`,
`Hub` and initializer receiver. An initialization RPC additionally supplies
frozen `call.rawParams` / `call.params`; inspect `call.params?.field("config")`
with the existing exact reader to retain configuration tokens and duplicate
order. Do not log raw initialization/configuration text: it can contain secrets.

An unwrapped initializer still receives exactly two arguments. Namespace
`withExactParams` does not opt initialization in. Direct/manual initializer
calls and trusted authoring probes have no original RPC frame, so `call` is
undefined there; never serialize parsed values and present them as original
raw metadata. Protocol checks, initialization single-flight/retry, events,
reply serialization, page deadlines and sandbox permissions remain unchanged.

## Installation-owned business storage

`ExtensionStorage` forwards typed `storage.*` calls through the initialized `Hub`.
An installed source manifest must explicitly declare `permissions.storage` keys
or literal-prefix patterns; queues and blobs also require their own flags. The
manager checks actual grants and the installation/process identity, not a
caller-supplied extension ID. `storageBytes` / `storageValue` handle canonical
base64 values or chunks of at most 64 KiB. Neither grants filesystem, SQL,
credential, hub or new network access.

Request a mutation ID with `store.request()` and retain that exact ID and payload
for an uncertain outcome. The client never retries automatically. Transactions
combine revision-checked entries and queue reservation/completion writes in one
commit. Request receipts expire after five minutes, and a replay cannot bypass a
withdrawn grant or resurrect an expired/replaced lease. Unknown external outcomes
are not safe new sends; protocol-specific confirmations and recovery still apply.
The generic store does not import legacy files or provide media URLs, lease
renewal or external-effect authority. Full bounds, lifecycle and owner-only
retained-data restore are in `docs/extensions/extensions.md` ("Installation-owned
business storage"). Production/source publication gates remain unchanged.

## Finite page loading

Use `useLoadEffect` from `@persona/extension/ui` for finite data loads. Keep
subscriptions and long-lived effects on `useEffect`. The official component-page
factory advertises snapshot readiness; owner-bound HTTP snapshot sessions wait
for finite loads within the existing deadline, while native streams retain
immediate loading and asynchronous updates. The manager selects the transport,
not the page's UI version.

Page cancellation is negotiated through canonical initialization and stays scoped
to its mount/event/resume request. Service handlers, exact namespace metadata,
permissions and resource budgets are unchanged. Cancellation/visibility/generation
fences suppress late success; arbitrary uncooperative JavaScript is not preempted.

## Offline CLI

Install this official SDK copy's development dependencies with `npm ci`. The
checker uses the SDK's pinned TypeScript parser, not a caller's compiler. It is
an **authoring tool for trusted local builds**, not an installer or a production
validator. Supported toolchains are Node 22 and Node 24; the JSON result records
the actual Node version. Bun drives the SDK's existing tests and portable suite;
the checker itself runs under Node, including when invoked by npm, Python or Bash.

From `sdk/ts`:

```sh
npm run check:source -- --root /absolute/package --bundle bundle/main.mjs
node scripts/check-source-bundle.mjs --root /absolute/package \
  --bundle bundle/main.mjs --manifest persona-extension.json --probe-trusted \
  --timeout-ms 2000
npm run test:source
npm run test:portable
```

External callers should invoke `node <official-sdk>/scripts/check-source-bundle.mjs`
with an argument array, collect stdout and require a zero exit code. The direct
CLI emits exactly one JSON line (npm may add its own script banner). Tool-entry
symlinks, including npm bin links, select the same canonical CLI entry; this does
not permit symlinks in bundle inputs. It never
prints bundle text, guest logs or exception messages. For example, static success
has `status: "PASS"`, `code: "STATIC_OK"`, `phase: "static"`,
`program: "not-probed"`, `node` and `sha256`. Successful probing changes the code
to `PROGRAM_OK`, phase to `trusted-probe`, and program to `verified`. A failure
has `status: "FAIL"`, a stable code and phase; it never has a success hash.

| Exit | Meaning |
| --- | --- |
| 0 | Requested authoring checks passed; not production acceptance |
| 1 | Input, syntax, import policy, Program shape, evaluation or worker failure |
| 2 | CLI options, unsupported Node toolchain or tool setup failure |
| 124 | Authoring deadline expired |
| 130 / 143 | Recorded SIGINT / SIGTERM, even if cleanup crosses the deadline or fails |

The library export `checkSourceBundle({ root, bundle, manifest?, probe?,
timeoutMs?, signal? })` returns the same verdict, without printing or installing
process signal handlers. Optional `signal` supports structural AbortSignals,
including signals passed across realms: boolean `aborted` and callable,
synchronous `addEventListener`/`removeEventListener` methods returning `undefined`,
as required by standard EventTarget. Async methods, promises, thenables and other
non-undefined returns are nonconforming and fail with `OPTIONS`. Declared async
methods are refused before invocation; other nonconforming ordinary promise or
thenable returns have their rejections observed, not ignored to produce success.
Caller options, signal implementations and promise machinery are trusted caller
code, not a hostile-caller isolation boundary. If caller-controlled constructor or
species machinery prevents rejection-handler installation, the original rejection
remains the caller's responsibility. The observation error produces `OPTIONS` and
triggers owned-worker cleanup when a worker exists; it does not imply that the
original rejection was handled. Invalid options, promise-valued options or throwing
getters return `FAIL/OPTIONS` before worker creation; listener registration or
cleanup errors still attempt owned-worker termination. A secondary cleanup error
never replaces an existing primary failure: optional `cleanup.signal: "OPTIONS"`
and `cleanup.worker: "WORKER"` fields record these errors. A worker termination
failure does not claim that the worker was reaped.
`root` is an absolute normalized directory using native platform separators; bundle and
manifest use normalized forward-slash relative paths beneath it. Linux is tested;
macOS temporary-directory aliases are accounted for. Native Windows authoring and
signal behavior remain unverified; accepting native root separators is not a
Windows support/acceptance claim. All lexical ancestors and
file components must be free of symlinks: use an explicitly canonical root when
a platform's temporary-directory alias is itself a symlink. Inputs must be
regular files, are read only, and are never rebuilt, rewritten or staged. A
bundle must be a nonempty UTF-8 `.mjs` file at most 16 MiB; an optional manifest is
at most 1 MiB. Length inconsistencies during the bounded read fail. This is not a
transactional snapshot of a concurrently modified package; production admission
still uses its own sealed artifact and fingerprint checks.

## What each check means

**Static mode is the default and executes no bundle initialization.** A disposable
worker parses the one supplied file, binds its lexical scopes without filesystem
module resolution, and compiles it with Node's actual ECMAScript module grammar.
Imports, re-exports from another module, dynamic import, import metadata and
unbound CommonJS `require` references are rejected, even inside uncalled
functions. Literal global-object `require` references, including direct
literal destructuring, are also rejected; ordinary
object properties and locally bound values named `require` are allowed. Quoted
strings and no-substitution template literals follow the same literal rule,
including any number of parentheses around the literal or global object. This is
syntax/authoring policy, not an exhaustive detection of computed loader aliases or
malicious JavaScript behavior. The actual manager validator remains independent.
A default export must exist, including `export { program as default }`. Static
success does **not** verify its value or prove that initialization will finish.
TypeScript-only syntax, truncated files and invalid ECMAScript fail closed.

**`--probe-trusted` explicitly opts into initialization of trusted built code.**
The worker evaluates the import-free module in a VM, then inspects data
properties of the live default Program, following inherited SDK member lookup:
nonempty string id/version, optional init, function-valued namespaces and
shortcuts, and legacy or component page shapes. Ordinary prototype Programs and
class methods are supported, as are plain objects returned by host Web APIs
such as `structuredClone` and `Response.json()`. Accessors in those inspected objects are unsupported
and are rejected without calling the getter; an own data member may shadow an
inherited accessor that the SDK would not read. Inherited contribution members
added to or changed on either realm's `Object.prototype` are inspected against a
baseline captured before evaluation. Unchanged native baseline members are not
contributions, and nearer data members take precedence. A class or function
prototype's own non-enumerable `constructor` back-reference to that class or
function is structural metadata, as created by class syntax and function
prototypes; an enumerable back-reference, such as one assigned in an ES5-style
prototype object literal, is inspected like any other member; any other inherited `constructor` member, enumerable or
not, is a contribution like any other name. Directly recognizable
class constructors are rejected in handler and renderer slots. This is a bounded
shape check, not a complete TypeScript check or a proof of future behavior for
opaque bound functions or proxies. Proxies and other dynamic behavior are not a
supported security contract. Handlers/renderers are not invoked. Top-level await is
supported; unresolved await, CPU loops and runaway microtasks reach the external
deadline. `Program.manifest` may be omitted. Defined `Program.fetch` values are
rejected because the current source adapter requires a stdio Program; this does
not remove fetch from generic SDK `Program` or change remote mode.

The probe provides a curated, version-gated Web API surface from the selected
Node toolchain, not full browser or Node-global parity:

- AbortController/AbortSignal; atob/btoa; Blob/File; DOMException;
  Event/CustomEvent/EventTarget; MessageChannel/MessageEvent/MessagePort.
- URL/URLPattern/URLSearchParams; FormData/Headers/Request/Response; TextDecoder,
  TextEncoder and their Stream variants; CompressionStream/DecompressionStream.
- ByteLengthQueuingStrategy/CountQueuingStrategy; ReadableStream and its
  BYOBReader/DefaultReader/ByteStreamController/DefaultController; TransformStream
  and its DefaultController; WritableStream and its DefaultController/DefaultWriter.
- performance, Performance, PerformanceEntry, PerformanceMark, PerformanceMeasure,
  PerformanceObserver, PerformanceObserverEntryList and PerformanceResourceTiming.
- crypto, Crypto/CryptoKey/SubtleCrypto; structuredClone; queueMicrotask;
  setTimeout/clearTimeout/setInterval/clearInterval; the rejecting fetch stub.

Performance and event constructors are shared with their native API instances.
Crypto/CryptoKey/SubtleCrypto and the available binary-data constructors are also shared with those APIs, preserving
ordinary Node `instanceof` and constructor checks on returned typed arrays,
ArrayBuffers and keys. Language literals and intrinsics remain VM-owned. A single
intrinsic comparison bridge admits either the VM or Node intrinsic prototype
family for `instanceof` against the corresponding VM global constructor. This
covers Object, Array, Date, Map, Set, RegExp, Error, Promise and the other language
constructors present in that toolchain, including nested/cyclic `structuredClone`
graphs, iterator results, stream read records and `Response.json()` payloads.
It does not rewrite object graphs, spoof subclass instances or turn null-prototype
records (such as the WebCrypto key pairs some Node releases return) into Object
instances.

The supported comparison is ordinary `instanceof` against those global intrinsic
constructors and the existing shared Web/binary constructors. Exact host-vs-VM
`Object.getPrototypeOf` identity, host plain-object `.constructor` equality,
explicit `Function.prototype[Symbol.hasInstance].call` bypasses, arbitrary custom
realm constructors, proxies and modified intrinsic comparison hooks are not a
full native-ESM realm emulation contract. These limits do not reject ordinary
SDK Program initialization or remove any provided global. Optional APIs such as
URLPattern follow that Node version; their absence does not reject unrelated
programs. No Node process/module/worker, filesystem or environment API is
supplied to the VM. Global fetch rejects without performing network IO. String
code generation is disabled. This avoids giving routine authoring code new IO
capabilities; **VM objects and shared Web constructors do not securely isolate
malicious code**, and worker heap limits are not hard RSS or OS confinement.
Never run an unauthorized artifact or secrets-bearing code in this probe.

After successful initialization/shape inspection, the worker observes queued
microtasks and asynchronous errors for 50 ms, then rereads the live default binding
and reinspects its current members. Immediate throws, unhandled
rejections and observed timer errors fail. It cannot verify errors scheduled for
arbitrary later times or future handler behavior. Then the supervisor terminates
and waits for **only its owned worker**, disposing unclosed MessageChannel ports,
intervals and other worker-local handles before emitting success. Failure,
external deadline and cancellation use the same cleanup path. Cancellation remains
observable until termination settles; a success waiting for cleanup cannot hide a
later abort. No forced
`process.exit` hides unfinished work or a failing verdict. The deadline (default
2000 ms; configurable 100–10000 ms) includes input reads, parsing, initialization
and observation. An absolute monotonic deadline is rechecked at every worker
result admission and after cleanup, so a stalled caller cannot accept a late
success ahead of a delayed timeout callback. Worker creation errors, including
Node permission denial, return `FAIL/TOOL` from both library and CLI without
granting worker authority. Filesystem latency and Node termination are not hard-real-time
or adversarial resource guarantees. An external automation deadline should also
cover CLI startup and teardown. No SIGKILL cleanup guarantee is made.

**Optional outer-manifest checks** compare id/version to the probed Program and
verify source mode, supported runtime fields, contained authoring entry, matching
bundle path and the SHA-256 of the exact completed bundle bytes. The authoring
entry need not be shipped in the package. There is no false self-referential
Program hash: build first, then hash the final artifact in the outer manifest.
The profile allows optional `rpc: "stdio"`, `http: false` (HTTP explicitly
disabled, not enabled), and legal `resources` values:
`memory_mb` 64–8192 and `cpu_percent` 10–800, both integers; absent keys use the
host's defaults. Unknown/wrong-type resource fields fail. This is a bounded source
profile check, **not full manifest/schema, permission, identity-version format or
contribution admission**.

## Distribution and acceptance

These scripts, tests and package commands are included by the official
`scripts/gen/sdk_bundle.py` generator and `scripts/gen/sync-sdk.sh` distribution.
After a change to this SDK, regenerate the embedded SDK archive and check it with
`python3 scripts/gen/sdk_bundle.py --check`. Do not hand-patch copied SDK scripts
or synchronize an unmerged SDK revision into another repository.

The source authoring tests run first in `test:portable`, which the selected
runtime CI workflow always runs before the SDK type and Bun tests. Synthetic
fixtures cover standard globals, lexical loaders, Program shapes, resource
budgets, timeouts, errors, signal cleanup, pipe EOF and an untouched neighboring
process. Signal tests use the CLI's optional parent-only IPC ready notification
(after its handlers are installed and the owned lifecycle is cancelable, **before**
calling guest module evaluation), including synchronous CPU loops, runaway
microtasks and deliberate slow startup. Ready does not mean evaluation has
completed or Program validation has passed; no
readiness lines are added to stdout and no IPC capability is supplied to the guest.
Green local authoring/type/portable/distribution tests are not a green
hosted `CI required` check. Merge still requires that aggregate check; real
pinned-Deno, confined-manager, native-client and production resource acceptance
remain separate gates. Authoring success never authorizes migration or deployment.
