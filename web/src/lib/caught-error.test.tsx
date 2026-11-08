// @vitest-environment jsdom
// A crash a boundary catches is logged with the component stack that names the component that threw (#1574), and the
// boundary still draws its page.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
vi.mock("./error-reports", () => ({ reportBoundary: () => {} }));
import { logCaughtError } from "./caught-error";
import { PageBoundary } from "../views/PageBoundary";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => vi.restoreAllMocks());

function TaskRow(): never {
  throw new TypeError("Cannot read properties of null (reading 'map')");
}

it("logs a caught render error with the component stack naming who threw", () => {
  const logged = vi.spyOn(console, "error").mockImplementation(() => {});
  const host = document.createElement("div");
  const root = createRoot(host, { onCaughtError: logCaughtError });
  act(() => root.render(<PageBoundary page="/"><TaskRow /></PageBoundary>));
  expect(host.querySelector("[role=alert]")).not.toBeNull();
  expect(logged.mock.calls.some(([e]) => e instanceof TypeError)).toBe(true);
  const stack = logged.mock.calls.map(([m]) => String(m)).find((m) => m.includes("Component stack:"));
  expect(stack).toMatch(/\bTaskRow\b/);
  expect(stack).toMatch(/\bPageBoundary\b/);
  act(() => root.unmount());
});
