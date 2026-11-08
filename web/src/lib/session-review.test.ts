// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { parseCommentBlock } from "./comments";
import { splitQuotes, parseQuotes } from "./drafts";
import {
  orderedComments, pendingComment, pendingReview, pendingThreads, reviewDiff, reviewItems, reviewMessage, reviewURL, setPendingReview, type GitReview,
} from "./session-review";

const res: GitReview = {
  root: "/home/me/proj",
  scope: "branch",
  base: { hash: "0123456789abcdef0123456789abcdef01234567", short: "0123456", subject: "Start", ref: "origin/main" },
  head: { hash: "f".repeat(40), short: "fffffff", branch: "feat/x" },
  files: [
    {
      path: "src/app.ts",
      status: "modified",
      added: 2,
      deleted: 1,
      revision: "abc1234",
      hunks: [{ old_start: 10, new_start: 10, header: "function main()", lines: [" const a = 1;", "-const b = 2;", "+const b = 3;", "+const c = 4;", " return a;"] }],
    },
    { path: "big.json", status: "modified", added: 4000, deleted: 10, big: true },
    { path: "deploy/id_ed25519", status: "modified", added: 0, deleted: 0, refused: true },
    { path: "logo.png", status: "added", added: 0, deleted: 0, binary: true },
    { path: "gone.txt", status: "deleted", added: 0, deleted: 1, hunks: [{ old_start: 1, new_start: 1, lines: ["-bye"] }] },
    { path: "new/name.ts", old_path: "old/name.ts", status: "renamed", added: 0, deleted: 0 },
  ],
};
const at = { mid: "mac", sid: "s1", cwd: "/home/me/proj" };

beforeEach(() => {
  localStorage.clear();
  setPendingReview("mac/s1", { comments: [] });
});

describe("the review's address", () => {
  it("carries the scope, the base and each path", () => {
    expect(reviewURL("mac 1", "/p q", { scope: "commit", base: "abc" }, ["a b.ts", "c.ts"])).toBe("/machines/mac%201/git/review?cwd=%2Fp+q&scope=commit&base=abc&path=a+b.ts&path=c.ts");
    expect(reviewURL("m", "/p", { scope: "uncommitted" })).toBe("/machines/m/git/review?cwd=%2Fp&scope=uncommitted");
  });
});

describe("as the code review's items", () => {
  it("offers comments on what can be read, the lines between hunks of what is on disk, and big files' diffs", () => {
    const items = reviewItems(res, new Map(), [], "You");
    const by = Object.fromEntries(items.map((it) => [it.path, it]));
    expect(by["src/app.ts"]).toMatchObject({ type: "diff", status: "modified", additions: 2, deletions: 1, revision: "abc1234", draft: { id: "draft" }, comment: { id: "comment" }, context: { id: "context" }, suggestions: true });
    expect(by["src/app.ts"].load).toBeUndefined();
    expect(by["big.json"].load).toEqual({ id: "load" });
    expect(by["deploy/id_ed25519"].draft).toBeUndefined();
    expect(by["deploy/id_ed25519"].context).toBeUndefined();
    expect(by["logo.png"]).toMatchObject({ binary: true });
    expect(by["logo.png"].draft).toBeUndefined();
    // a removed file has no lines on disk between its hunks
    expect(by["gone.txt"].context).toBeUndefined();
    expect(by["new/name.ts"].old_path).toBe("old/name.ts");
    // once loaded on its own, its hunks; too big even then: not offered again
    const loaded = new Map([["big.json", { ...res.files[1], big: false, hunks: [{ old_start: 1, new_start: 1, lines: ["+x"] }] }]]);
    expect(reviewItems(res, loaded, [], "You")[1]).toMatchObject({ hunks: [{ lines: ["+x"] }] });
    expect(reviewItems(res, new Map([["big.json", { ...res.files[1], huge: true }]]), [], "You")[1].load).toBeUndefined();
  });
});

describe("a comment of the review", () => {
  const diff = reviewDiff(res);

  it("names the diff it is on", () => {
    expect(diff).toEqual({ kind: "review", scope: "branch", base: "0123456" });
    expect(reviewDiff({ ...res, scope: "uncommitted" })).toEqual({ kind: "review", scope: "uncommitted" });
    expect(reviewDiff({ scope: "commit", base: { hash: "89abcdef00", short: "89abcde" } })).toEqual({ kind: "review", scope: "commit", base: "89abcde" });
  });

  it("carries the diff's rows of its lines, the removed ones between them too", () => {
    const c = pendingComment(res.files[0], { side: "new", line: 12, start_line: 10 }, "Why 3?", diff);
    expect(c).toMatchObject({ path: "src/app.ts", side: "new", line: 12, start_line: 10, body: "Why 3?" });
    expect(c.code).toEqual([" const a = 1;", "-const b = 2;", "+const b = 3;", "+const c = 4;"]);
    expect(pendingComment(res.files[0], { side: "old", line: 11 }, "x", diff).code).toEqual(["-const b = 2;"]);
    // a line shown from between hunks
    expect(pendingComment(res.files[0], { side: "new", line: 3 }, "x", diff, (n) => (n === 3 ? "import y" : undefined)).code).toEqual([" import y"]);
  });

  it("is drawn as a draft at its lines; an old side's from another diff is outdated", () => {
    const c = pendingComment(res.files[0], { side: "old", line: 11 }, "Gone?", diff);
    const [th] = pendingThreads([c], "src/app.ts", diff, "You");
    expect(th).toMatchObject({ id: c.id, side: "old", line: 11, pending: true, outdated: false, remove: { id: "remove" }, comments: [{ author: "You", body: "Gone?" }] });
    expect(pendingThreads([c], "src/app.ts", { kind: "review", scope: "uncommitted" }, "You")[0].outdated).toBe(true);
    expect(pendingThreads([c], "other.ts", diff, "You")).toEqual([]);
  });

  it("is kept for the session in this browser", () => {
    const c = pendingComment(res.files[0], { side: "new", line: 12 }, "x", diff);
    setPendingReview("mac/s1", { comments: [c], note: "Summary" });
    expect(JSON.parse(localStorage.getItem("persona.review:mac/s1")!).comments[0].id).toBe(c.id);
    expect(pendingReview("mac/s1").note).toBe("Summary");
    setPendingReview("mac/s1", { comments: [] });
    expect(localStorage.getItem("persona.review:mac/s1")).toBeNull();
  });
});

describe("the review as a message", () => {
  it("is every comment's block in file and line order, then the summary, and reads back", () => {
    const diff = reviewDiff(res);
    const late = pendingComment(res.files[0], { side: "new", line: 13 }, "And c?", diff);
    const early = pendingComment(res.files[0], { side: "new", line: 12, start_line: 10 }, "Why 3?\n```suggestion\nconst b = 2;\n```", diff);
    const gone = pendingComment(res.files[4], { side: "old", line: 1 }, "Keep it", diff);
    const list = orderedComments([gone, late, early], res.files);
    expect(list.map((c) => c.id)).toEqual([early.id, late.id, gone.id]);
    const text = reviewMessage(list, "Looks good otherwise.", at);
    expect(text.split("\n").slice(0, 12)).toEqual([
      "> Code comment 1",
      "> File: src/app.ts:10-12",
      "> Diff: branch since 0123456",
      "> ```diff",
      ">  const a = 1;",
      "> -const b = 2;",
      "> +const b = 3;",
      "> +const c = 4;",
      "> ```",
      "> ",
      "> Why 3?",
      "> ```suggestion",
    ]);
    expect(text).toContain("> Code comment 3\n> File: gone.txt:1\n> Side: old\n> Diff: branch since 0123456");
    expect(text.endsWith("\n\nLooks good otherwise.")).toBe(true);
    // an edited or sent message reads its comments back, with where they are
    const split = splitQuotes(text)!;
    const back = parseQuotes(split.quotes);
    expect(back).toHaveLength(3);
    expect(back[0].comment?.spot).toMatchObject({ file: "src/app.ts", from: 10, to: 12, diff: { kind: "review", scope: "branch", base: "0123456" } });
    expect(back[2].comment?.spot).toMatchObject({ old: true, diff: { kind: "review", scope: "branch" } });
    expect(split.rest).toBe("Looks good otherwise.");
    // no summary: just the blocks
    expect(reviewMessage([late], "  ", at).startsWith("> Code comment 1")).toBe(true);
  });

  it("reads each kind of Diff line back", () => {
    for (const [line, diff] of [
      ["uncommitted", { kind: "review", scope: "uncommitted" }],
      ["since 89abcde", { kind: "review", scope: "commit", base: "89abcde" }],
      ["branch since 0123456", { kind: "review", scope: "branch", base: "0123456" }],
      ["commit 1234abcd", { kind: "commit", sha: "1234abcd" }],
      ["staged", { kind: "staged" }],
    ] as const)
      expect(parseCommentBlock(`Code comment 1\nFile: a.ts:1\nDiff: ${line}\n\`\`\`diff\n+x\n\`\`\``)?.comment?.spot?.diff).toEqual(diff);
  });
});
