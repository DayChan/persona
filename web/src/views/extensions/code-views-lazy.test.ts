/// <reference types="node" />
// The first screen does not fetch what it does not draw (#1794). The assistant's cards bring ViewBody.tsx into the app's
// first chunk, and App.tsx brings ExtensionSlots.tsx; neither may pull in UI version 4's code hosting components
// (CodeViews.tsx, DiffReview.tsx) or the extension page (settings/ExtensionView.tsx) with a static import: those load
// with a dynamic import, under a Suspense of their own.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const staticImports = (source: string) => [...source.matchAll(/^import[^;]*from\s+["']([^"']+)["']/gm)].map((m) => m[1]);

describe("the first screen's extension files", () => {
  it("ViewBody loads the code views and the diff on demand", () => {
    const source = read("./ViewBody.tsx");
    expect(staticImports(source)).not.toContain("./CodeViews");
    expect(staticImports(source)).not.toContain("./DiffReview");
    expect(source).toMatch(/import\(\s*["']\.\/CodeViews["']\s*\)/);
    expect(source).toMatch(/import\(\s*["']\.\/DiffReview["']\s*\)/);
  });

  it("ExtensionSlots loads the extension page on demand", () => {
    const source = read("./ExtensionSlots.tsx");
    expect(staticImports(source)).not.toContain("../settings/ExtensionView");
    expect(source).toMatch(/import\(\s*["']\.\.\/settings\/ExtensionView["']\s*\)/);
    // the page, a card and a section each wait under their own Suspense, not App's
    expect(source.match(/<Suspense fallback=\{<Loading \/>\}>\s*<ExtensionUI /g)).toHaveLength(3);
  });

  it("the dynamic imports resolve to the components drawn", async () => {
    const [code, diff, page] = await Promise.all([import("./CodeViews"), import("./DiffReview"), import("../settings/ExtensionView")]);
    for (const c of [code.Header, code.Breadcrumb, code.FileTree, code.CodeView, code.CommentThread, code.Composer, diff.DiffView, page.ExtensionUI]) {
      expect(typeof c).toBe("function");
    }
  }, 60_000);
});
