// @vitest-environment jsdom
// What PageBoundary sends to the hub's error reports when a page crashes: the error, and where in the component tree it
// was caught, so a crash nobody saw happen can be traced from its report alone (docs/web/web.md "Error reports").
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ErrorReporter, setReporterForTest } from "../lib/error-reports";
import type { ErrorReport } from "../lib/extension-wire";
import { PageBoundary } from "./PageBoundary";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  setReporterForTest(null);
  vi.restoreAllMocks();
});

function TaskRow(): never {
  throw new TypeError("Cannot read properties of null (reading 'map')");
}

it("reports a page crash with the component stack it was caught in, the error as the innermost cause", async () => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  const sent: ErrorReport[] = [];
  const reporter = new ErrorReporter({
    storage: null,
    send: async (reports) => {
      sent.push(...reports);
      return { ok: true, enabled: true };
    },
    now: Date.now,
    uuid: () => "00000000-0000-4000-8000-000000000001",
    origin: "http://localhost:3000",
    platform: {},
    setTimer: () => 0,
    clearTimer: () => {},
  });
  reporter.setFeatures(["error_reports"]);
  setReporterForTest(reporter);

  const host = document.createElement("div");
  const root = createRoot(host);
  act(() => root.render(<PageBoundary page="/"><TaskRow /></PageBoundary>));
  await reporter.flush();
  act(() => root.unmount());

  expect(sent).toHaveLength(1);
  const [report] = sent;
  expect(report).toMatchObject({ level: "fatal", tags: { mechanism: "page_boundary" } });
  const [outer, inner] = report.exceptions ?? [];
  // the error stays the innermost cause (the last exception): the hub fingerprints a report by it
  expect(inner).toMatchObject({ type: "TypeError", value: "Cannot read properties of null (reading 'map')" });
  expect(outer.type).toBe("ComponentStack");
  // innermost last, as every report's frames: the component that threw, under the boundary that caught it
  const names = (outer.frames ?? []).map((f) => f.function);
  expect(names.at(-1)).toBe("TaskRow");
  expect(names).toContain("PageBoundary");
});
