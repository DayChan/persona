import { describe, expect, it } from "vitest";
import { CAPTION_LIMIT, historyOf, mergeCaption, type VoiceCaption } from "./captions";

const tr = (id: string, role: string, text: string, final = true, before?: string) => ({ id, role, text, final, before });

function play(frames: ReturnType<typeof tr>[], generation = 1, start: readonly VoiceCaption[] = []) {
  return frames.reduce((c, f) => mergeCaption(c, f, generation), start);
}

describe("voice captions", () => {
  it("updates a line by id as it grows, then ends it", () => {
    const c = play([tr("u1", "user", "帮我把", false), tr("u1", "user", "帮我把登录页修一下", true)]);
    expect(c).toEqual([{ id: "1:u1", generation: 1, role: "user", text: "帮我把登录页修一下", final: true }]);
  });

  it("returns the same list for a frame that changes nothing", () => {
    const c = play([tr("a1", "assistant", "ok")]);
    expect(mergeCaption(c, tr("a1", "assistant", "ok"), 1)).toBe(c);
  });

  it("does not open a line without words", () => {
    expect(play([tr("a1", "assistant", "", false)])).toEqual([]);
  });

  it("puts a late caption before the answer it came in, and keeps it there as both grow", () => {
    const c = play([
      tr("a2", "assistant", "Claude asks: which database?"),
      tr("a3", "assistant", "OK, ", false),
      tr("u3", "user", "the first", true, "a3"),
      tr("a3", "assistant", "OK, told it."),
    ]);
    expect(c.map((x) => `${x.role}:${x.text}`)).toEqual(["assistant:Claude asks: which database?", "user:the first", "assistant:OK, told it."]);
  });

  it("appends when before names a row of an older connection or none", () => {
    const older = play([tr("a3", "assistant", "old answer")], 1);
    const c = play([tr("u3", "user", "late", true, "a3")], 2, older);
    expect(c.map((x) => x.id)).toEqual(["1:a3", "2:u3"]);
    expect(play([tr("u1", "user", "x", true, "missing")]).map((x) => x.id)).toEqual(["1:u1"]);
  });

  it("keeps a new connection's u1 apart from the last one's", () => {
    const c = play([tr("u1", "user", "again")], 2, play([tr("u1", "user", "first")], 1));
    expect(c.map((x) => x.id)).toEqual(["1:u1", "2:u1"]);
  });

  it("keeps the last 40 and hands those with words back as history", () => {
    const frames = Array.from({ length: CAPTION_LIMIT + 5 }, (_, i) => tr(`a${i}`, "assistant", `line ${i}`));
    const c = play(frames);
    expect(c).toHaveLength(CAPTION_LIMIT);
    expect(c[0].text).toBe("line 5");
    expect(historyOf(c)).toHaveLength(40);
    expect(historyOf(c)[0]).toEqual({ role: "assistant", text: "line 5" });
  });
});
