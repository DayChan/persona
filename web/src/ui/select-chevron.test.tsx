/// <reference types="node" />
import { readFileSync } from "node:fs";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Icon } from "./icons";

// A dropdown's arrow (#2314): every single-line <select> draws the icon set's chevron from styles.css instead of the
// browser's own arrow, which ignores the field's padding (Chrome pins it to the right edge, Safari drops the padding).
// A browser check measures it: scripts/test/web_select_chevron_e2e.py.
const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
const RULE = "select:not([multiple], [size], .appearance-none)";

function block(scheme: "light" | "dark"): string {
  return (scheme === "light" ? /:root\s*\{([^}]+)\}/ : /\.dark\s*\{([^}]+)\}/).exec(css)?.[1] ?? "";
}

/** The chevron token's SVG, decoded, and the block's --ink-3. */
function chevron(scheme: "light" | "dark"): { svg: string; ink3: string } {
  const b = block(scheme);
  const url = /--select-chevron:\s*url\("data:image\/svg\+xml,([^"]+)"\);/.exec(b)?.[1] ?? "";
  return { svg: decodeURIComponent(url), ink3: /--ink-3:\s*(#[0-9a-f]{6});/i.exec(b)?.[1] ?? "" };
}

/** The attributes of the first `<tag …>` in an SVG string, quotes either way. */
function attrs(svg: string, tag: string): Record<string, string> {
  const open = new RegExp(`<${tag}\\b([^>]*)>`).exec(svg)?.[1] ?? "";
  return Object.fromEntries([...open.matchAll(/([\w-]+)=["']([^"']*)["']/g)].map((m) => [m[1], m[2]]));
}

describe("select chevron", () => {
  const icon = renderToStaticMarkup(<Icon name="chevron" />);

  for (const scheme of ["light", "dark"] as const) {
    it(`${scheme}: is the icon set's chevron, stroked in that scheme's --ink-3`, () => {
      const { svg, ink3 } = chevron(scheme);
      expect(svg, "--select-chevron").toMatch(/^<svg /);
      expect(ink3).toMatch(/^#[0-9a-f]{6}$/i);
      expect(attrs(svg, "svg").stroke?.toLowerCase()).toBe(ink3.toLowerCase());
      expect(attrs(svg, "path").d).toBe(attrs(icon, "path").d);
      const own = attrs(icon, "svg");
      expect(attrs(svg, "svg")).toMatchObject({
        viewBox: own.viewBox,
        fill: "none",
        "stroke-width": own["stroke-width"],
        "stroke-linecap": own["stroke-linecap"],
        "stroke-linejoin": own["stroke-linejoin"],
      });
    });
  }

  it("turns the native arrow off and keeps the text clear of the chevron", () => {
    const start = css.indexOf(`${RULE} {`);
    expect(start).toBeGreaterThan(-1);
    const body = css.slice(start, css.indexOf("}", start));
    expect(body).toMatch(/appearance:\s*none;/);
    expect(body).toMatch(/background-image:\s*var\(--select-chevron\);/);
    expect(body).toMatch(/background-repeat:\s*no-repeat;/);
    const pad = Number(/padding-right:\s*(\d+)px;/.exec(body)?.[1]);
    const inset = Number(/background-position:\s*right (\d+)px center;/.exec(body)?.[1]);
    const size = Number(/background-size:\s*(\d+)px \1px;/.exec(body)?.[1]);
    expect([pad, inset, size].every((n) => n > 0)).toBe(true);
    expect(pad).toBeGreaterThanOrEqual(inset + size + 4);
  });

  it("is unlayered, so its padding wins over the px-* utilities", () => {
    // Comments may hold braces ("`:root {}`"); the data URIs hold none.
    const code = css.replace(/\/\*[\s\S]*?\*\//g, (c) => " ".repeat(c.length));
    const at = code.indexOf(`${RULE} {`);
    let depth = 0;
    for (const ch of code.slice(0, at)) depth += ch === "{" ? 1 : ch === "}" ? -1 : 0;
    expect(depth).toBe(0);
  });
});
