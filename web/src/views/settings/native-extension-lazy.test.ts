/// <reference types="node" />
// The native extension page is its own chunk: ExtensionView reaches NativeExtensionUI (and through it the UI stream and
// the extension JSON schemas it validates against) only through a dynamic import, so the app's first chunk does not
// download or parse them; most extension pages are drawn over HTTP and never need them.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./ExtensionView.tsx", import.meta.url), "utf8");
const staticImports = [...source.matchAll(/^import[^;]*from\s+["']([^"']+)["']/gm)].map((m) => m[1]);

describe("ExtensionView's native page", () => {
  it("has no static import of the native page or the UI stream", () => {
    expect(staticImports).not.toContain("./NativeExtensionUI");
    expect(staticImports.filter((path) => /extension-ui-(app-)?stream$/.test(path))).toEqual([]);
  });

  it("loads NativeExtensionUI with a dynamic import, under PageBoundary and Suspense", () => {
    expect(source).toMatch(/import\(\s*["']\.\/NativeExtensionUI["']\s*\)/);
    expect(source).toMatch(/<PageBoundary [^\n]*>\s*<Suspense [^\n]*>\s*<NativeExtensionUI /);
  });

  it("the dynamic import resolves to the NativeExtensionUI component", async () => {
    const m = await import("./NativeExtensionUI");
    expect(typeof m.NativeExtensionUI).toBe("function");
  }, 60_000);
});
