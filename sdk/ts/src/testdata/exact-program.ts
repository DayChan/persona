import { withExactParams, type ExactHandler, type Program } from "../index.ts";

// Tests import this factory from a separately bundled Program as well as from source.
export function deliveryProgram(handler: ExactHandler): Program {
  return { id: "example", version: "1.0.0", namespaces: { error_reports: withExactParams(handler) } };
}

// A real source Program: the host and program may bundle different SDK copies.
export default {
  id: "example",
  version: "1.0.0",
  namespaces: {
    connector: withExactParams((_method, _legacy, call) => {
      if (!call) return {};
      const args = call.params?.field("args");
      const values = args?.int64Fields(["topic", "replies"]) ?? { topic: 0n, replies: 0n };
      // Exact input is deliberately rendered as text, not promised as exact JSON output.
      return { topic: values.topic.toString(), replies: values.replies.toString(), raw: args?.raw };
    }),
  },
} satisfies Program;
