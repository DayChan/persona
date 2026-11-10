import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/comments/cases.json";
import { diffLabel, isCommentHead, parseCommentBlock, parseDiffLabel, type QuoteComment } from "./comments";
import { type DraftQuote, splitQuotes, withQuoteBlock } from "./drafts";

// The comment blocks a message starts with, as spec/fixtures/comments has them (the iOS app checks the same cases).

interface Code { file: string; from: number; to: number; old?: boolean; diff?: string; lang?: string; omitted?: number }
interface Element { url: string; selector: string; component?: string; source?: string; text?: string; omitted?: number }
interface Comment { quoted: string; note: string; code?: Code; element?: Element }
interface Case {
  name: string;
  input: { comments?: Comment[]; text?: string; message?: string };
  expected: { message?: string; quote?: string | null; comments?: Comment[]; rest?: string } | null;
}

function quote(c: Comment): DraftQuote {
  const comment: QuoteComment = { body: c.note };
  if (c.code) {
    const diff = c.code.diff ? parseDiffLabel(c.code.diff) : null;
    comment.spot = {
      file: c.code.file, path: c.code.file, from: c.code.from, to: c.code.to, omitted: c.code.omitted ?? 0,
      ...(c.code.old ? { old: true } : {}), ...(diff ? { diff } : {}), ...(c.code.lang ? { lang: c.code.lang } : {}),
    };
  }
  if (c.element) comment.element = { ...c.element, omitted: c.element.omitted ?? 0 };
  return { text: c.quoted, comment };
}

/** Every field with its default filled in, so a missing `old` equals `false` (README "Cases"). */
function normal(c: Comment) {
  return {
    quoted: c.quoted,
    note: c.note,
    code: c.code ? { file: c.code.file, from: c.code.from, to: c.code.to, old: c.code.old ?? false, diff: c.code.diff ?? null, lang: c.code.lang ?? null, omitted: c.code.omitted ?? 0 } : null,
    element: c.element ? { url: c.element.url, selector: c.element.selector, component: c.element.component ?? null, source: c.element.source ?? null, text: c.element.text ?? null, omitted: c.element.omitted ?? 0 } : null,
  };
}

function back(q: DraftQuote) {
  const s = q.comment?.spot;
  const e = q.comment?.element;
  return normal({
    quoted: q.text,
    note: q.comment?.body ?? "",
    ...(s ? { code: { file: s.file, from: s.from, to: s.to, old: s.old, diff: s.diff ? diffLabel(s.diff) : undefined, lang: s.lang, omitted: s.omitted } } : {}),
    ...(e ? { element: { url: e.url, selector: e.selector, component: e.component || undefined, source: e.source, text: e.text, omitted: e.omitted } } : {}),
  });
}

describe("spec/fixtures/comments", () => {
  const cases = (fixture as { version: number; cases: Case[] }).cases;

  it("has its cases", () => {
    expect((fixture as { version: number }).version).toBe(1);
    expect(cases.length).toBeGreaterThanOrEqual(12);
  });

  for (const c of cases) {
    it(c.name, () => {
      if (c.input.comments) {
        expect(withQuoteBlock(c.input.comments.map(quote), c.input.text ?? "")).toBe(c.expected?.message);
        return;
      }
      const split = splitQuotes(c.input.message ?? "");
      if (c.expected === null) {
        expect(split).toBeNull();
        return;
      }
      expect(split).not.toBeNull();
      const [first, ...more] = split!.quotes;
      const plain = isCommentHead(first.split("\n")[0]) ? null : first;
      const blocks = plain === null ? split!.quotes : more;
      expect(plain).toBe(c.expected.quote);
      expect(blocks.map((b) => back(parseCommentBlock(b)!))).toEqual((c.expected.comments ?? []).map(normal));
      expect(split!.rest).toBe(c.expected.rest);
    });
  }
});
