/** @jsxImportSource ../../../../sdk/ts/src */
import { componentPage } from "../../../../sdk/ts/src/index.ts";
import { View, Section, Text, Button, callback } from "../../../../sdk/ts/src/components.ts";
import { useState, useInterval } from "../../../../sdk/ts/src/hooks.ts";

// Import-free guest Program: the installed trusted adapter, not this bundle, owns connect.
let mode = "", writes = 0, callbackActive = 0, callbackCanceled = 0, callbackCompleted = 0;
let callbackMaxActive = 0, uiTicks = 0, serviceTicks = 0, mounts = 0, disposes = 0;
let release: (() => void) | undefined;
setInterval(() => serviceTicks++, 10);
function Page() {
  const [count, update] = useState(0);
  useInterval(() => uiTicks++, 10);
  return <View key="root"><Section key="main">
    <Text key="count" text={String(count)} />
    <Button key="no-ui" title="Synthetic write" action={callback(async (_values, { signal }) => {
      writes++;
      const current = mode;
      if (!current) return;
      callbackActive++; callbackMaxActive = Math.max(callbackMaxActive, callbackActive);
      try {
        await new Promise<void>((resolve, reject) => {
          if (current === "late") release = resolve;
          signal.addEventListener("abort", () => { callbackCanceled++; if (current === "cooperative") reject(new Error("synthetic cancellation")); }, { once: true });
        });
        update(99); // Deliberately uncooperative: retirement must fence this state write.
      } finally { callbackActive--; callbackCompleted++; }
    })} />
  </Section></View>;
}
const page = componentPage(() => { mounts++; return <Page key="page" />; });
export default {
  id: "example-page", version: "1.0.0",
  ui: { home: { ...page, createRenderer(host: Parameters<typeof page.createRenderer>[0]) {
    const renderer = page.createRenderer(host), dispose = renderer.dispose.bind(renderer);
    renderer.dispose = () => { disposes++; dispose(); };
    return renderer;
  } } },
  namespaces: { test(method: string, params: any) {
    if (method === "callback") { mode = params.mode; return {}; }
    if (method === "releaseCallback") { release?.(); return {}; }
    if (method === "stats") return { writes, callbackActive, callbackCanceled, callbackCompleted, callbackMaxActive, uiTicks, serviceTicks, mounts, disposes };
    throw new Error("unknown fixture method");
  } },
};
