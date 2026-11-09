import { expect, test } from "bun:test";
import { ExtensionStorage, Hub, storageBytes, storageValue } from "./index.ts";

const harness = () => {
  const frames: { id: string; method: string; params: unknown }[] = [];
  const hub = new Hub(line => frames.push(JSON.parse(line)));
  return { frames, hub, store: new ExtensionStorage(hub) };
};

test("storage bytes preserve Unicode and binary without filesystem or extra authority", () => {
  const raw = new TextEncoder().encode("fixture 😀 日本語 中文");
  expect(storageValue(storageBytes(raw))).toEqual(raw);
  const binary = Uint8Array.from({ length: 65536 }, (_, i) => i % 256);
  expect(storageValue(storageBytes(binary))).toEqual(binary);
  expect(() => storageBytes(new Uint8Array(65537))).toThrow();
  expect(() => storageValue("YQ")).toThrow();
  expect(() => storageValue("a".repeat(87385))).toThrow();
});

test("storage client forwards the original mutation identity and never retries unknown outcomes", async () => {
  const { hub, frames, store } = harness();
  try {
    const input = { request_id: "fixture-original", operations: [{ kind: "put", key: "state/a", data: "eA==" }] };
    const pending = store.transaction(input);
    expect(frames.length).toBe(1);
    expect(frames[0].method).toBe("storage.transaction");
    expect(frames[0].params).toEqual(input);
    hub.answer({ id: frames[0].id, error: { code: "storage_unknown", message: "fixture unknown commit" } });
    await expect(pending).rejects.toMatchObject({ code: "storage_unknown" });
    expect(frames.length).toBe(1);
  } finally { hub.close(); }
});

test("key, blob and queue calls expose only the typed logical contract", async () => {
  const { hub, frames, store } = harness();
  try {
    const calls = [
      () => store.request(), () => store.get("state/a"), () => store.list(),
      () => store.queueList({ key: "queue/jobs" }),
      () => store.enqueue({ key: "queue/jobs", request_id: "fixture", dedup: "event", data: "eA==" }),
      () => store.claim({ key: "queue/jobs", request_id: "fixture", lease_seconds: 30 }),
      () => store.ack({ key: "queue/jobs", request_id: "fixture", id: "job", lease: "lease" }),
      () => store.retry({ key: "queue/jobs", request_id: "fixture", id: "job", lease: "lease", not_before: 1 }),
      () => store.forget({ key: "queue/jobs", request_id: "fixture", id: "job" }),
      () => store.createBlob({ key: "media/image", request_id: "fixture" }),
      () => store.writeBlob({ key: "media/image", request_id: "fixture", handle: "blob", offset: 0, data: "eA==" }),
      () => store.finishBlob({ key: "media/image", request_id: "fixture", handle: "blob" }),
      () => store.readBlob({ key: "media/image", handle: "blob", offset: 0, limit: 1 }),
      () => store.deleteBlob({ key: "media/image", request_id: "fixture", handle: "blob" }),
    ];
    for (const invoke of calls) {
      const pending = invoke(), frame = frames.at(-1)!;
      expect(frame.method.startsWith("storage.")).toBe(true);
      expect(Object.keys(frame.params as object).some(key => ["installation", "sql", "path", "owner", "hub"].includes(key))).toBe(false);
      hub.answer({ id: frame.id, result: {} });
      await pending;
    }
    expect(frames.length).toBe(calls.length);
  } finally { hub.close(); }
});
