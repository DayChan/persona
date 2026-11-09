/** @jsxImportSource ../../../../sdk/ts/src */
import { componentPage, ExtensionStorage, storageBytes, storageValue, type Hub, type Program } from "../../../../sdk/ts/src/index.ts";
import { View, Section, Text, Button, callback } from "../../../../sdk/ts/src/components.ts";
import { useLoadEffect, useState } from "../../../../sdk/ts/src/hooks.ts";

// A complete synthetic guest. No network, credentials, files or external effects.
let hub: Hub, store: ExtensionStorage;
const encoder = new TextEncoder(), decoder = new TextDecoder();
function Page() {
  const [status, setStatus] = useState("Loading stored progress"), [count, setCount] = useState(0);
  async function load() {
    try {
      const entry = await store.get("state/progress");
      const value = JSON.parse(decoder.decode(storageValue(entry.data)));
      if (value.version !== 1 || !Number.isSafeInteger(value.count)) throw new Error("Invalid fixture state");
      setCount(value.count);
      setStatus("Stored progress recovered");
    } catch (error) {
      if ((error as { code?: string }).code === "not_found") setStatus("No stored progress");
      else throw error;
    }
  }
  useLoadEffect(load, []);
  return <View key="storage-root" title="Synthetic storage fixture"><Section key="progress">
    <Text key="status" text={status} />
    <Text key="count" text={String(count)} />
    <Button key="reserve" title="Reserve synthetic work" action={callback(async () => {
      const { request_id } = await store.request();
      await store.transaction({ request_id, operations: [
        { kind: "put", key: "state/progress", data: storageBytes(encoder.encode(JSON.stringify({ version: 1, count: count + 1 }))) },
        { kind: "enqueue", key: "queue/jobs", dedup: request_id, data: storageBytes(encoder.encode("synthetic work")) },
      ] });
      await load();
    })} />
  </Section></View>;
}
export default {
  id: "fixture-storage", version: "1.0.0",
  init(_init, host) { hub = host; store = new ExtensionStorage(host); },
  ui: { home: componentPage(() => <Page key="page" />) },
  namespaces: {
    test(method, params) {
      if (method !== "storage") throw new Error("Unknown fixture method");
      // Tests exercise the same public client used by pages, including error replies.
      const value = params as { method: string; params: Record<string, unknown> };
      switch (value.method) {
        case "storage.request": return store.request();
        case "storage.get": return store.get(String(value.params.key));
        case "storage.list": return store.list(value.params);
        // Raw fixture requests intentionally include malformed wire objects;
        // the manager, not a TypeScript cast, is their validation boundary.
        default: return hub.call(value.method, value.params);
      }
    },
  },
} satisfies Program;
