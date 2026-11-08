/** @jsxImportSource ../../../../sdk/ts/src */
import { createInterface } from "node:readline";
import { createServer } from "node:net";
import { connect, componentPage } from "../../../../sdk/ts/src/index.ts";
import { createElement, View, Section, Text, Button, Row, Code, callback } from "../../../../sdk/ts/src/components.ts";
import { useState, useInterval, useEffect, useLoadEffect } from "../../../../sdk/ts/src/hooks.ts";

// A real Node source program over an injected test host, not a sandbox E2E.
let writes = 0, uiTicks = 0, serviceTicks = 0, mounts = 0;
let update: (n: number) => void = () => {};
let prompt = "Synthetic confirmation", effectUpdates = false, renders = 0, disposes = 0;
let rootGate: Promise<void> | undefined, releaseRoot: (() => void) | undefined;
let nativeResult: unknown, reviewEnabled = false, lastValues: unknown;
let navigationEnabled = false, mountUIVersion = 0;
let loadingEnabled = false, loadingDelay = 200, loadingFailure = false, cached = false;
let loadReads = 0, loadCanceled = 0, loadActive = 0, snapshotTransport = 0;
let callbackMode = "", callbackActive = 0, callbackCanceled = 0, callbackCompleted = 0, callbackMaxActive = 0;
let releaseCallback: (() => void) | undefined;
const pageOptions = { snapshotTimeoutMs: 1000 };
function LoadingPage() {
  const [value, setValue] = useState("loading");
  const [revision, setRevision] = useState(0);
  useLoadEffect(async signal => {
    if (cached) { setValue("loaded"); return; }
    loadReads++; loadActive++;
    try {
      await new Promise<void>((resolve, reject) => {
        const abort = () => { clearTimeout(timer); loadCanceled++; reject(new Error("synthetic cancellation")); };
        const timer = setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, loadingDelay);
        signal.addEventListener("abort", abort, { once: true });
      });
      if (!signal.aborted) { cached = !loadingFailure; setValue(loadingFailure ? "error" : "loaded"); }
    } catch { /* Canceled UI work is not a successful read. */ }
    finally { loadActive--; }
  }, [revision]);
  useInterval(() => { uiTicks++; }, 10);
  return <View key="root"><Section key="main">
    <Text key="load" text={value} />
    <Button key="refresh" title="Synthetic refresh" action={callback(() => {
      writes++; cached = false; setValue("loading"); setRevision(n => n + 1); return {toast: "Saved"};
    })} />
  </Section></View>;
}
function Navigation({ uiVersion }: { uiVersion: number }) {
  const [page, setPage] = useState("home");
  const push = { state: "detail", title: "Synthetic detail" };
  return <View key="root"><Section key="main">
    <Text key="location" text={page} />
    <Button key="navigate" title="Synthetic navigation" action={callback(() => {
      if (uiVersion < 3) { setPage(page === "home" ? "detail" : "home"); return; }
      return { push };
    }, uiVersion < 3 ? {} : { push })} />
  </Section></View>;
}
const save = async (values: unknown, { signal }: { signal: AbortSignal }) => {
  writes++; lastValues = values;
  if (callbackMode) {
    callbackActive++; callbackMaxActive = Math.max(callbackMaxActive, callbackActive);
    const mode = callbackMode;
    const lateUpdate = update;
    try {
      await new Promise<void>((resolve, reject) => {
        if (mode === "late") releaseCallback = resolve;
        signal.addEventListener("abort", () => {
          callbackCanceled++;
          if (mode === "cooperative") reject(new Error("synthetic cancellation"));
        }, { once: true });
      });
      lateUpdate(99); // Deliberately ignores cancellation; retired state must not publish.
    } finally { callbackActive--; callbackCompleted++; }
  }
  return nativeResult;
};
function Page() {
  renders++;
  const [count, setCount] = useState(0); update = setCount;
  useEffect(() => { if (effectUpdates) setCount(n => n + 1); }, []);
  useInterval(() => { uiTicks++; }, 10);
  return <View key="root" review={reviewEnabled ? {count: 0, name: "summary", buttons: [{title: "Review", action: callback(save, {submit: true, values: {saved: "native"}})}]} : undefined}><Section key="main">
    <Text key="count" text={String(count)} />
    <Text key="padding" text={"synthetic fixture ".repeat(50)} />
    <Code key="code" text="" truncated={false} />
    <Button key="increment" title="Synthetic increment" action={callback(async () => { writes++; setCount(n => n + 1); await Promise.resolve(); setCount(n => n + 1); })} />
    <Button key="no-ui" title="Synthetic no UI write" action={callback(save)} />
    <Button key="confirm" title="Synthetic confirm" action={callback(() => { writes++; }, { confirm: prompt })} />
    <Button key="ambiguous" title="Synthetic failed write" action={callback(() => { writes++; throw new Error("synthetic callback failure"); })} />
    <Row key="toggle" title="Synthetic toggle" control={{kind:"switch",name:"toggle",on:false,action:callback(() => { writes++; })}} />
  </Section></View>;
}
const service = setInterval(() => serviceTicks++, 10);
function start(inputStream: NodeJS.ReadableStream, outputStream: NodeJS.WritableStream) {
let heldMethod = "";
const methods = new Map<string, string>(), held: string[] = [];
const protocol = connect({ id:"example-page",version:"1.0.0",ui:{home:componentPage(async request => { if (rootGate) await rootGate; mountUIVersion = request.ui_version; snapshotTransport = request.transport === "snapshot" ? 1 : 0; return loadingEnabled ? <LoadingPage key="page" /> : navigationEnabled ? <Navigation key="page" uiVersion={request.ui_version} /> : <Page key="page" />; }, pageOptions)},
 namespaces:{ test: async (method, params) => {
  if(method==="delay") { heldMethod=params.method; return {}; }
  if(method==="release") { heldMethod=""; for (const line of held.splice(0)) outputStream.write(line+"\n"); return {}; }
  if(method==="result") { nativeResult=params; return {}; }
  if(method==="review") { reviewEnabled=params.enabled; return {}; }
  if(method==="values") return lastValues;
  if(method==="effect") { effectUpdates=params.enabled; return {}; }
  if(method==="blockRoot") { rootGate=new Promise(resolve => { releaseRoot=resolve; }); return {}; }
  if(method==="releaseRoot") { releaseRoot?.(); rootGate=undefined; return {}; }
  if(method==="update") { update(params.count); await new Promise(resolve => setTimeout(resolve, 5)); return {}; }
  if(method==="prompt") { prompt=params.prompt; update(params.count); await new Promise(resolve => setTimeout(resolve, 5)); return {}; }
  if(method==="restart") process.exit(0);
  if(method==="navigation") { navigationEnabled=params.enabled; return {}; }
  if(method==="loading") { loadingEnabled = params.enabled; cached = params.cached ?? false; loadingFailure = params.fail ?? false; loadingDelay = params.delay ?? 200; pageOptions.snapshotTimeoutMs = params.timeout ?? 1000; return {}; }
  if(method==="callback") { callbackMode=params.mode; return {}; }
  if(method==="releaseCallback") { releaseCallback?.(); return {}; }
  if(method==="stats") return {writes,uiTicks,serviceTicks,renders,disposes,mounts,mountUIVersion,loadReads,loadCanceled,loadActive,snapshotTransport,callbackActive,callbackCanceled,callbackCompleted,callbackMaxActive,held:held.length};
  throw new Error("unknown fixture method");
 }},
}, line => {
 const frame=JSON.parse(line);
 if(frame.id && methods.get(frame.id)===heldMethod) { held.push(line); return; }
 outputStream.write(line+"\n");
});
const input = createInterface({ input:inputStream });
input.on("line", line => {
 const frame=JSON.parse(line);
 if(frame.id) methods.set(frame.id, frame.method);
 if(frame.method==="ui.page.mount") mounts++;
 if(frame.method==="ui.page.dispose") disposes++;
 protocol.line(line);
});
input.on("close", () => { protocol.close(); if (!process.argv[2]) clearInterval(service); });
}
if (process.argv[2]) createServer(socket => start(socket, socket)).listen(process.argv[2]);
else start(process.stdin, process.stdout);
