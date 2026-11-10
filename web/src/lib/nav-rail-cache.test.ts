// The rail's ticks are read once per line object and a long answer's excerpt from its head (nav-rail.ts): both must
// give what reading every line and the whole answer gave before.
import { afterEach, describe, expect, it } from "vitest";
import { NAV_RAIL_EXCERPT_MAX, navRailExcerpt, navRailTicks } from "./nav-rail";
import { stabilizeLines } from "./stable-lines";
import { chatLines } from "./turns";
import type { Item } from "./types";

/** The excerpt as it was before the head cut: every step over the whole text. */
function wholeTextExcerpt(raw: string): string {
  return raw
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/^#{1,6}\s+/gm, "")
    .replace(/^>\s?/gm, "")
    .replace(/^[-+*]\s+/gm, "")
    .replace(/[*`]/g, "")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, NAV_RAIL_EXCERPT_MAX);
}

/** The longest text any regex replace was run on, and how many ran, while `fn` runs. */
const realReplace = RegExp.prototype[Symbol.replace];
function regexWork(fn: () => void): { calls: number; longest: number } {
  const seen = { calls: 0, longest: 0 };
  RegExp.prototype[Symbol.replace] = function (this: RegExp, text: string, by: never) {
    seen.calls++;
    seen.longest = Math.max(seen.longest, text.length);
    return realReplace.call(this, text, by);
  } as typeof realReplace;
  try {
    fn();
  } finally {
    RegExp.prototype[Symbol.replace] = realReplace;
  }
  return seen;
}
afterEach(() => {
  RegExp.prototype[Symbol.replace] = realReplace;
});

/** A small seeded generator (mulberry32), so a failure names a case that can be run again. */
function random(seed: number) {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// Markdown pieces, with the ones the cut has to respect (comment and link openers and closers, marks whose spaces
// run on over line breaks, other line breaks) far more often than in real answers.
const PIECES = [
  "word ", "another word ", "改好了，测试也过了。", "x", " ", "  ", "\t", "\n", "\n", "\n\n", "\r\n", "\r", " ",
  "# ", "#", "###### ", "####### ", "> ", ">", "- ", "-", "* ", "+ ", "**bold**", "`code`", "```\n", "*", "`",
  "[", "]", "(", ")", "](", "[link](https://example.com/a)", "[multi\nline](u\n)", "[]()", "[a [b](c)", "[x]", "(y)",
  "<!--", "-->", "<!-- note -->", "<!-- a\nlong\ncomment -->", "<!", "--", ">",
];

function markdown(next: () => number): string {
  // a run of prose up to around the head's size, then a stretch of pieces across it
  const prose = "Some prose of the answer, sentence after sentence. ".repeat(Math.floor(next() * 26));
  const parts = [prose];
  const n = Math.floor(next() * 120);
  for (let i = 0; i < n; i++) {
    const piece = PIECES[Math.floor(next() * PIECES.length)];
    parts.push(next() < 0.1 ? piece.repeat(1 + Math.floor(next() * 40)) : piece);
  }
  if (next() < 0.5) parts.push("\n", "tail ".repeat(Math.floor(next() * 400)));
  return parts.join("");
}

describe("navRailExcerpt reading a long answer's head", () => {
  it("gives the whole-text excerpt on fixtures and random Markdown", () => {
    const pad = (n: number) => "a".repeat(n);
    const fixtures = [
      "",
      "  \n\n",
      "答".repeat(5000),
      `${pad(1100)}\n<!-- open at the cut\n${pad(300)} -->\nafter`,
      `${pad(100)}\n${pad(1000)}<!--\n${pad(2000)}\n-->${pad(500)}\n`,
      `${pad(1100)}[a link whose text\nruns over the cut\n${pad(400)}](https://example.com)\n${pad(400)}`,
      `${pad(1100)}[text](a target that runs\non past the cut\n${pad(400)})\n${pad(400)}`,
      `${"# heading\n".repeat(100)}#\n\n\n   \n${pad(600)}\n`,
      `${"- item\n".repeat(150)}-\n   \n\n${pad(600)}\n${pad(10)}`,
      `<!-- ${pad(3000)} -->\n${"word ".repeat(300)}\n${pad(10)}`,
      `${"```\ncode\n```\n".repeat(100)}\n${pad(300)}\n${pad(300)}`,
      `${" \n".repeat(2000)}${pad(50)}\n${pad(50)}`,
      `${"[x](y)".repeat(400)}\n${pad(300)}\n${pad(300)}`,
      `${"> quote\n".repeat(200)}>\n${pad(900)}\n${pad(9)}`,
      `${"word ".repeat(250)}\r\n${"word ".repeat(250)} ${pad(9)}\n${pad(9)}`,
      // a comment and a link left open at the cut, with a full excerpt in front of or around them
      `${pad(198)}\n- b <!--${pad(1000)}\n${pad(100)}`,
      `${"a ".repeat(50)}[${"b ".repeat(600)}\n${"c ".repeat(100)}`,
    ];
    for (const raw of fixtures) expect(navRailExcerpt(raw), raw.slice(0, 80)).toBe(wholeTextExcerpt(raw));
    for (let seed = 1; seed <= 4000; seed++) {
      const raw = markdown(random(seed));
      if (navRailExcerpt(raw) !== wholeTextExcerpt(raw)) expect.fail(`seed ${seed}: ${JSON.stringify(raw.slice(0, 200))}…`);
    }
  });

  it("reads only the head of a long answer in prose", () => {
    const raw = "A long answer in prose, one line after another.\n".repeat(4000);
    const whole = wholeTextExcerpt(raw);
    let excerpt = "";
    const work = regexWork(() => (excerpt = navRailExcerpt(raw)));
    expect(excerpt).toBe(whole);
    expect(work.longest).toBeLessThan(2048);
  });
});

describe("navRailTicks reading each line once", () => {
  const u = (seq: number, text: string): Item => ({ id: `u${seq}#0`, seq, role: "user", kind: "text", text });
  const a = (seq: number, text: string): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text });
  const items = Array.from({ length: 40 }, (_, i) => [u(i * 2 + 1, `问题 ${i}`), a(i * 2 + 2, `**回答** ${i}：[改好了](x)。`)]).flat();

  it("re-reads only the lines a push changed, with the same ticks as reading them all", () => {
    const before = chatLines(items);
    const first = navRailTicks(before);
    const pushed = [...items, a(82, "第 40 个回答的后半段")];
    const after = stabilizeLines(before, chatLines(pushed));
    let ticks = first;
    const work = regexWork(() => (ticks = navRailTicks(after)));
    expect(ticks).toEqual(navRailTicks(chatLines(pushed)));
    expect(ticks.slice(0, -1).every((tick, i) => tick === first[i])).toBe(true);
    // only the one changed exchange: its question's quote step, its answer's comment and link steps and the five after them
    expect(work.calls).toBeLessThanOrEqual(8);
  });
});
