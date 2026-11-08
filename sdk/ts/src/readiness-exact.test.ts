import { expect, test } from "bun:test";
import { connect, componentPage, withExactParams } from "./index.ts";
import { createElement, View, Section, Button, callback } from "./components.ts";
import { useLoadEffect, useState } from "./hooks.ts";
import type { Snapshot } from "./renderer.ts";

const gate = () => {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
};

test("one connection retains exact namespace params alongside finite page loading and scoped cancellation", async () => {
  const service = gate(), serviceStarted = gate(), callbackStarted = gate();
  let callbackSignal!: AbortSignal, legacyTopic: number | undefined;
  const frames: any[] = [];
  function Root() {
    const [title, setTitle] = useState("loading");
    useLoadEffect(async () => { await Promise.resolve(); setTitle("loaded"); }, []);
    return createElement(View, { key: "root", title, children: createElement(Section, { key: "main", children:
      createElement(Button, { key: "write", title: "Write", action: callback(async (_values, { signal }) => {
        callbackSignal = signal; callbackStarted.resolve();
        await new Promise<void>((_resolve, reject) => signal.addEventListener("abort", () => reject(new Error("canceled")), { once: true }));
      }) }),
    }) });
  }
  const conn = connect({ id: "combined", version: "1", ui: { home: componentPage(() => createElement(Root, { key: "page" })) },
    namespaces: { connector: withExactParams(async (_method, legacy, call) => {
      if (!call) return {};
      const topic = call.params!.field("topic")!.int64();
      legacyTopic = legacy.topic; serviceStarted.resolve();
      await service.promise;
      return { topic: topic.toString() };
    }) },
  }, line => frames.push(JSON.parse(line)));
  const send = (id: string, method: string, params: unknown = {}) => conn.line(JSON.stringify({ id, method, params }));
  const cancel = (id: string) => conn.line(JSON.stringify({ event: "rpc.cancel", params: { id } }));
  try {
    send("init", "initialize", { protocol: 1 }); await conn.drain();
    expect(frames[0].result.ui_page_cancellation).toBe(1);
    send("mount", "ui.page.mount", { session: "owned", transport: "snapshot", params: { contribution: "home" } });
    await conn.drain();
    const snapshot = frames.find(frame => frame.id === "mount").result as Snapshot;
    expect(snapshot.view.title).toBe("loaded");
    conn.line('{"id":"fractional","method":"connector.read","params":{"topic":1.0}}');
    await conn.drain();
    expect(frames.find(frame => frame.id === "fractional").error.code).toBe("invalid_params");
    conn.line('{"id":"service","method":"connector.read","params":{"topic":9007199254740993}}');
    send("event", "ui.page.event", { session: "owned", ticket: "host", interaction: {
      generation: snapshot.generation, version: snapshot.version, eventId: "one", key: "write", values: {},
      actionId: (snapshot.view.sections[0].items[0] as { action: { id: string } }).action.id,
    } });
    await Promise.all([serviceStarted.promise, callbackStarted.promise]);
    cancel("service"); expect(callbackSignal.aborted).toBe(false);
    cancel("event"); service.resolve(); await conn.drain();
    expect(callbackSignal.aborted).toBe(true);
    expect(frames.find(frame => frame.id === "event").error).toBeDefined();
    expect(frames.find(frame => frame.id === "service").result).toEqual({ topic: "9007199254740993" });
    expect(legacyTopic).toBe(9007199254740992);
  } finally { conn.close(); service.resolve(); }
});
