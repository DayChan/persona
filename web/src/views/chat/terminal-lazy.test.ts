/// <reference types="node" />
// The session page keeps the terminal view out of its chunk (#2677): ChatView only reaches TerminalPanel through a
// dynamic import, so a visit that never switches to "Terminal" does not download or parse it.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./ChatView.tsx", import.meta.url), "utf8");

describe("ChatView's terminal view", () => {
  it("has no static import of TerminalPanel", () => {
    expect(source).not.toMatch(/^import[^;]*from\s+["']\.\.\/session\/TerminalPanel["']/m);
  });

  it("loads TerminalPanel with a dynamic import, under Suspense and PageBoundary", () => {
    expect(source).toMatch(/import\(\s*["']\.\.\/session\/TerminalPanel["']\s*\)/);
    // the fallback holds JSX of its own, so match up to the Suspense line's end rather than its first ">"
    expect(source).toMatch(/<PageBoundary [^\n]*>\s*<Suspense [^\n]*>\s*<TerminalPanel /);
  });

  // the lazy() wrapper picks the named export: it must still be there
  it("the dynamic import resolves to the TerminalPanel component", async () => {
    const m = await import("../session/TerminalPanel");
    expect(typeof m.TerminalPanel).toBe("function");
  }, 60_000);
});
