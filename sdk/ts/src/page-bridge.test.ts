import { expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { connect, componentPage, type UIRequest } from "./index.ts";
import { createElement, View, Section, Button, Text, callback } from "./components.ts";
import { useState } from "./hooks.ts";
import type { Snapshot } from "./renderer.ts";

function harness() {
 const frames: any[] = []; let writes = 0; let update!: (n: number) => void;
 function Root() {
  const [value,setValue]=useState(0); update=setValue;
  return createElement(View,{key:"root",children:createElement(Section,{key:"main",children:[
   createElement(Text,{key:"value",text:String(value)}),
   createElement(Button,{key:"write",title:"fixture",action:callback(()=>{writes++})}),
  ]})});
 }
 const p=connect({id:"fixture",version:"1",ui:{home:componentPage((_request: UIRequest)=>createElement(Root,{key:"page"}))},namespaces:{test:()=>"service"}},line=>frames.push(JSON.parse(line)));
 let id=0;
 const call=async(method:string,params:unknown={})=>{const request=String(++id);p.line(JSON.stringify({id:request,method,params}));await p.drain();return frames.find(frame=>frame.id===request)};
 return {p,frames,call,writes:()=>writes,update:(n:number)=>update(n)};
}

test("Program componentPage bridge keeps factories/callbacks local and service namespaces independent",async()=>{
 const h=harness();await h.call("initialize",{protocol:1});
 expect((await h.call("ui.page.supported")).result).toEqual({contributions:["home"]});
 const mount=(await h.call("ui.page.mount",{session:"manager-owned",params:{contribution:"home",ui_version:6}})).result as Snapshot;
 expect(JSON.stringify(mount)).not.toContain("callbackTag");
 const button=mount.view.sections[0].items[1] as {action:{id:string}};
 const interaction={generation:mount.generation,version:mount.version,eventId:"one",key:"write",actionId:button.action.id,values:{}};
 expect((await h.call("ui.page.event",{session:"manager-owned",interaction})).error.message).toContain("ticket");
 const result=(await h.call("ui.page.event",{session:"manager-owned",ticket:"host-issued",interaction})).result;
 expect(result.status).toBe("accepted");expect(result.snapshot.version).toBe(mount.version+1);expect(h.writes()).toBe(1);
 await h.call("ui.page.event",{session:"manager-owned",ticket:"host-issued",interaction});expect(h.writes()).toBe(1);
 h.update(3);await Promise.resolve();await Promise.resolve();
 expect(h.frames.some(frame=>frame.event==="ui.page.snapshot"&&frame.params.snapshot.view.sections[0].items[0].text==="3")).toBe(true);
 await h.call("ui.page.suspend",{session:"manager-owned"});
 expect((await h.call("test.any")).result).toBe("service");
 expect((await h.call("ui.page.resume",{session:"manager-owned"})).result.generation).toBe(mount.generation);
 await h.call("ui.page.dispose",{session:"manager-owned"});await h.call("ui.page.dispose",{session:"manager-owned"});
 expect((await h.call("ui.page.snapshot",{session:"manager-owned"})).error).toBeDefined();h.p.close();
});

test("disposing an awaiting root factory cannot resurrect a mounted page", async () => {
 let release!: () => void;
 const gate = new Promise<void>(resolve => { release = resolve; });
 const frames: any[] = [];
 const p = connect({ id:"fixture", version:"1", ui:{home:componentPage(async () => {
  await gate;
  return createElement(View,{key:"root",children:createElement(Section,{key:"main",children:[]})});
 })}}, line => frames.push(JSON.parse(line)));
 p.line(JSON.stringify({id:"mount",method:"ui.page.mount",params:{session:"pending",params:{contribution:"home"}}}));
 await Promise.resolve();
 p.line(JSON.stringify({id:"dispose",method:"ui.page.dispose",params:{session:"pending"}}));
 await Promise.resolve();
 release();
 await p.drain();
 expect(frames.find(frame => frame.id==="mount").error).toBeDefined();
 p.line(JSON.stringify({id:"snapshot",method:"ui.page.snapshot",params:{session:"pending"}}));
 await p.drain();
 expect(frames.find(frame => frame.id==="snapshot").error).toBeDefined();
 p.close();
});

test("component pages fail closed on stateless HTTP and dispose all sessions on process close",async()=>{
 const h=harness();await h.call("initialize",{protocol:1});
 expect((await h.call("ui.render",{contribution:"home",ui_version:6})).error.code).toBe("unsupported");
 expect((await h.call("ui.render",{contribution:"home",ui_version:6,placement:"widget"})).result.sections).toHaveLength(1);
 const first=(await h.call("ui.page.mount",{session:"one",params:{contribution:"home"}})).result;
 const second=(await h.call("ui.page.mount",{session:"two",params:{contribution:"home"}})).result;
 expect(first.generation).not.toBe(second.generation);h.p.close();
 expect((await h.call("ui.page.snapshot",{session:"one"})).error).toBeDefined();
});

test("a separately bundled page uses its own Renderer, symbols and hook context", async () => {
 const dir = await mkdtemp(join(tmpdir(), "page-bundle-"));
 try {
  const bundle = join(dir, "page.mjs");
  const built = await Bun.build({entrypoints: [join(import.meta.dir, "testdata/separate-page.ts")], target: "browser", minify: true});
  expect(built.success).toBe(true);
  await Bun.write(bundle, built.outputs[0]);
  const program = (await import(bundle)).default;
  const frames: any[] = [];
  const connection = connect(program, line => frames.push(JSON.parse(line)));
  const call = async (id: string, method: string, params: unknown) => {
   connection.line(JSON.stringify({id, method, params})); await connection.drain();
   return frames.find(frame => frame.id === id);
  };
  try {
   await call("init", "initialize", {protocol: 1});
   for (const transport of ["stream", "snapshot"]) {
    const mounted = await call(`mount-${transport}`, "ui.page.mount", {session: transport, transport, params: {contribution: "home"}});
    expect(mounted.error).toBeUndefined();
    const snapshot = mounted.result;
    const action = snapshot.view.sections[0].items[1].action.id;
    const params = {session: transport, ticket: "host-issued", interaction: {
     generation: snapshot.generation, version: snapshot.version, eventId: "one", key: "change", actionId: action, values: {},
    }};
    const reply = await call(`event-${transport}`, "ui.page.event", params);
    expect(reply.result.status).toBe("accepted");
    expect(reply.result.snapshot.view.sections[0].items[0].text).toBe("1");
    expect(reply.result.result).toEqual({toast: "Saved"});
    expect((await call(`replay-${transport}`, "ui.page.event", params)).result).toEqual(reply.result);
   }
  } finally { connection.close(); }
 } finally { await rm(dir, {recursive: true, force: true}); }
});

test("PageBridge preserves bounded native callback results through await and replay", async () => {
 const frames: any[] = []; let writes = 0;
 const connection = connect({id: "results", version: "1", ui: {home: componentPage(() =>
  createElement(View, {key: "root", children: createElement(Section, {key: "main", children:
   createElement(Button, {key: "write", title: "Write", action: callback(() => {writes++; return {toast: "Saved", open_url: "https://example.com", push: {ui: "detail", state: "fixture"}, pop: false};})})})}))}},
  line => frames.push(JSON.parse(line)));
 const call = async (id: string, method: string, params: unknown) => {
  connection.line(JSON.stringify({id, method, params})); await connection.drain(); return frames.find(frame => frame.id === id);
 };
 try {
  await call("init", "initialize", {protocol: 1});
  const snapshot = (await call("mount", "ui.page.mount", {session: "results", params: {contribution: "home"}})).result;
  const params = {session: "results", ticket: "host-issued", interaction: {generation: snapshot.generation, version: snapshot.version,
   eventId: "one", key: "write", actionId: snapshot.view.sections[0].items[0].action.id, values: {}}};
  const reply = await call("event", "ui.page.event", params);
  expect(reply.result.result).toEqual({toast: "Saved", open_url: "https://example.com", push: {ui: "detail", state: "fixture"}, pop: false});
  expect((await call("retry", "ui.page.event", params)).result.result).toEqual(reply.result.result);
  expect(writes).toBe(1);
 } finally { connection.close(); }
});

test("PageBridge detaches retained results and bounds them before caching", async () => {
 const shared: any = {toast: "Original"}; let value: unknown = shared;
 const frames: any[] = []; let writes = 0;
 const connection = connect({id: "bounds", version: "1", ui: {home: componentPage(() =>
  createElement(View, {key: "root", children: createElement(Section, {key: "main", children:
   createElement(Button, {key: "write", title: "Write", action: callback(() => {writes++; return value;})})})}))}},
  line => frames.push(JSON.parse(line)));
 let next = 0;
 const call = async (method: string, params: unknown) => {
  const id = String(++next); connection.line(JSON.stringify({id, method, params})); await connection.drain();
  return frames.find(frame => frame.id === id).result;
 };
 try {
  await call("initialize", {protocol: 1});
  let snapshot = await call("ui.page.mount", {session: "bounds", params: {contribution: "home"}});
  const event = () => ({session: "bounds", ticket: "host-issued", interaction: {generation: snapshot.generation,
   version: snapshot.version, eventId: String(next), key: "write", actionId: snapshot.view.sections[0].items[0].action.id, values: {}}});
  const first = event();
  const original = await call("ui.page.event", first);
  shared.toast = "Mutated";
  expect((await call("ui.page.event", first)).result).toEqual({toast: "Original"});
  snapshot = original.snapshot;
  value = {lines: {path: "fixture.go", start: 1, lines: new Array(20000).fill("")}};
  const maximum = await call("ui.page.event", event());
  expect(maximum.status).toBe("accepted"); expect(maximum.result.lines.lines).toHaveLength(20000);
  snapshot = maximum.snapshot;
  for (const oversized of [{toast: "x".repeat(65536)}, {toast: String.fromCodePoint(0x754c).repeat(22000)}, {lines: new Array(32769).fill("")}, {run: () => {}}, (() => {const cyclic: any = {}; cyclic.self=cyclic; return cyclic;})()]) {
   value = oversized;
   const reply = await call("ui.page.event", event());
   expect(reply.status).toBe("ambiguous"); expect(reply.result).toBeUndefined();
   expect(reply.snapshot.version).toBe(snapshot.version + 1); snapshot = reply.snapshot;
  }
  expect(writes).toBe(7);
 } finally { connection.close(); }
});

test("component pages retain no more native receipts than the manager can replay", async () => {
 for (const limits of [undefined, {events: 1024}]) {
  let writes = 0;
  const page = componentPage(() => createElement(View, {key: "root", children: createElement(Section, {key: "main", children:
   createElement(Button, {key: "write", title: "Write", action: callback(() => {writes++; return {lines: {path: "fixture.go", start: 1, lines: new Array(20000).fill("")}};})})})}), {limits});
  const renderer = page.createRenderer({authorize: () => true});
  try {
   renderer.mount(await page.root({} as UIRequest));
   const event = (id: string) => {
    const snapshot = renderer.current();
    return {generation: snapshot.generation, version: snapshot.version, eventId: id, key: "write",
     actionId: (snapshot.view.sections[0].items[0] as {action: {id: string}}).action.id, values: {}};
   };
   const first = event("first");
   await renderer.event(first);
   for (let i = 0; i < 64; i++) await renderer.event(event(String(i)));
   await expect(renderer.event(first)).rejects.toThrow("stale UI event version");
   expect(writes).toBe(65);
  } finally {renderer.dispose();}
 }
});
