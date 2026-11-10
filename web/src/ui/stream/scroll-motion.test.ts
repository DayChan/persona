// @vitest-environment node
// Scripted scrolls follow reduced motion only through scrollBehavior() (motion.ts): the stylesheet's reduced-motion rule
// cannot reach scrollTo, scrollIntoView or scrollBy, so a smooth scroll written anywhere else animates for someone who
// asked for less motion (docs/lessons/web.md "Scripted scrolling ignores reduced motion"). A scroller styled to scroll
// smoothly would animate even scrollBehavior()'s "auto", so none is.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

const SRC = join(__dirname, "..", "..");
const OWNER = join(__dirname, "motion.ts");

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    if (statSync(p).isDirectory()) return sources(p);
    return /\.(tsx?|css)$/.test(f) && !/\.test\.tsx?$/.test(f) ? [p] : [];
  });
}

// Read once, while the file is collected (lib/busy-motion.test.ts says why).
const SOURCES = sources(SRC).map((file) => ({ file, lines: readFileSync(file, "utf8").split("\n") }));

// `behavior: "smooth"`, `behavior: reduce ? "auto" : "smooth"`, `style.scrollBehavior = "smooth"`; the CSS property and
// Tailwind's scroll-smooth. Comments may say the word.
const SMOOTH = /behavior\s*[:=][^,;}]*["'`]smooth["'`]|scroll-behavior:\s*smooth|\bscroll-smooth\b/i;
const comment = (line: string) => /^\s*(\/\/|\*|\/\*)/.test(line);
const hits = (lines: string[]) => lines.flatMap((line, i) => (SMOOTH.test(line) && !comment(line) ? [i + 1] : []));

describe("scripted scrolls under reduced motion", () => {
  it("scrollBehavior() is where the smooth scroll is written", () => {
    const owner = SOURCES.find((s) => s.file === OWNER);
    expect(owner).toBeDefined();
    expect(hits(owner!.lines).length).toBeGreaterThan(0);
  });

  it("no other source writes a smooth scroll of its own", () => {
    const found = SOURCES.filter((s) => s.file !== OWNER).flatMap(({ file, lines }) => hits(lines).map((n) => `${relative(SRC, file)}:${n}`));
    expect(found).toEqual([]);
  });
});
