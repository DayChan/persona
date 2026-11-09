import { describe, expect, it } from "vitest";
import { injectedOf, parseInjected } from "./injected";
import { lineOfItem } from "./chat-search";
import { displayLines } from "./feed";
import { chatLines, mergeItems, segments, supervisionTool } from "./turns";
import type { Item } from "./types";

// i18n-ignore: the fixed supervision header and marker are protocol data.
const raw = '📋 督办：你派出去的任务有 2 个要你处理\n<persona-supervision level="triage" />\n这是 Persona 的督办消息\nCheck the fictional release task.';
const user = (seq: number, text = raw, extra: Partial<Item> = {}): Item => ({ id: `u${seq}#0`, seq, role: "user", kind: "text", text, ...extra });
const answer = (seq: number, text = "The fictional task was checked."): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text });
const call = (seq: number, name: string, tool_input: Record<string, unknown> = {}): Item => ({ id: `t${seq}#0`, seq, role: "assistant", kind: "tool_use", tool_use_id: `call${seq}`, tool_name: name, tool_input });
const result = (seq: number, use: Item, text = "ok", is_error = false): Item => ({ id: `r${seq}#0`, seq, role: "user", kind: "tool_result", tool_use_id: use.tool_use_id, text, is_error });
const fold = (promoted = false): Item => ({ id: "fold:t2#0", seq: 2, role: "assistant", kind: "fold", fold: { items: 4, tools: 2, thoughts: 0, through_seq: 5, supervision_user: promoted } });
const receipt = (visibility: "silent" | "user") => JSON.stringify({ visibility, summary: "Checked the fictional release.", level: visibility === "user" ? "user" : "triage" });

describe("supervision rounds", () => {
  it("retains feed placement and day separators around the quiet round's original input", () => {
    const lines = chatLines([user(1, raw, { ts: "2026-11-01T10:00:00Z" }), answer(2), user(3, "Next real request", { ts: "2026-11-01T11:00:00Z" })]);
    const feed = { id: "fictional-feed", conversation: "fictional-chat", kind: "notice" as const, at: "2026-11-01T09:00:00Z", body: { text: "Fictional prior notice" } };
    const shown = displayLines(lines, [feed]);
    expect(shown.map((l) => l.kind)).toEqual(["day", "feed", "chat", "chat"]);
    expect(shown[2].kind === "chat" && shown[2].line.kind).toBe("supervision");
    const withoutFeed = displayLines(lines, []);
    expect(withoutFeed.map((l) => l.kind)).toEqual(["day", "chat", "chat"]);
  });
  it("recognizes the fixed markers, new levels and legacy triage without hiding real users", () => {
    expect(parseInjected(raw)).toMatchObject({ state: "supervise", level: "triage" });
    expect(parseInjected(raw.replace('level="triage"', 'level="user"'))).toMatchObject({ level: "user" });
    expect(parseInjected(raw.replace('<persona-supervision level="triage" />\n', ""))).toMatchObject({ level: "triage" });
    expect(parseInjected(raw.split("\n")[0])).toBeNull();
    expect(parseInjected('A real request\n<persona-supervision level="user" />')).toBeNull();
    expect(injectedOf(user(1, raw, { client_msg_id: "fictional-message" }))).toBeNull();
    expect(injectedOf(user(1, raw, { from: "fictional-agent" }))).toBeNull();
    expect(chatLines([user(1), answer(2)])[0].kind).toBe("supervision");
    expect(chatLines([user(1, raw, { injected: { kind: "system", state: "supervise", line: "Check tasks" } }), answer(2)])[0].kind).toBe("supervision");
    expect(chatLines([user(1, raw, { injected: { kind: "system", state: "supervise", level: "user", line: "Check tasks" } }), answer(2)])[0].kind).toBe("segment");
    expect(chatLines([user(1, raw.replace('level="triage"', 'level="user"')), answer(2)])[0].kind).toBe("segment");
  });

  it("preserves every original input block and its images before display parsing", () => {
    const original = `<persona-context kind="context">Fictional snapshot</persona-context>\n${raw}`;
    const image: Item = { id: "u1#1", seq: 1, role: "user", kind: "image", image: { mime: "image/png", lazy: true } };
    const more = user(1, "Extra fictional details", { id: "u1#2" });
    const [seg] = segments([user(1, original), image, more, answer(2)]);
    expect(seg.user?.rawInput?.map((i) => i.id)).toEqual(["u1#0", "u1#1", "u1#2"]);
    expect(seg.user?.rawInput?.[0].text).toBe(original);
    expect(seg.user?.images).toEqual([image]);
    const lines = chatLines([user(1, original), image, more, answer(2)]);
    expect(lineOfItem(lines, [], more.id)).toBe(lines[0].id);
    expect(lineOfItem(lines, [], image.id)).toBe(lines[0].id);
  });

  it("separates queued supervision from prior work and later real messages, including their images", () => {
    const use = call(2, "Read");
    const later = user(6, "A new real request", { queued: true, client_msg_id: "fictional-next" });
    const image: Item = { id: "u6#1", seq: 6, role: "user", kind: "image", image: { mime: "image/png", lazy: true } };
    const lines = chatLines([user(1, "First real request"), use, user(3, raw, { queued: true }), answer(4), later, image, answer(7)]);
    expect(lines.map((l) => l.kind)).toEqual(["segment", "supervision", "segment"]);
    const last = lines[2];
    expect(last.kind === "segment" && last.seg.user?.text).toBe(later.text);
    expect(last.kind === "segment" && last.seg.user?.images).toEqual([image]);
    const quiet = lines[1];
    expect(quiet.kind === "supervision" && lineOfItem(quiet.lines, [], later.id)).toBeUndefined();
  });

  it("keeps recap-split continuation segments in one round and promotes all of them together", () => {
    const recap: Item = { id: "recap#0", seq: 3, role: "system", kind: "recap", text: "Fictional continuation recap" };
    const use = call(4, "supervision_finish");
    const items = [user(1), answer(2, "Fictional interim reply"), recap, use, result(5, use, receipt("silent")), answer(6), user(7, "A new real request", { queued: true })];
    const lines = chatLines(items);
    expect(lines.map((l) => l.kind)).toEqual(["supervision", "segment"]);
    expect(lines[0].kind === "supervision" && lines[0].lines.map((l) => l.kind)).toEqual(["segment", "recaps", "segment"]);
    expect(chatLines(items.map((i) => i.seq === 5 ? result(5, use, receipt("user")) : i)).map((l) => l.kind)).toEqual(["segment", "recaps", "segment", "segment"]);
    const [live] = chatLines(items.slice(0, 3), { live: true });
    expect(live.kind).toBe("supervision");
    expect(live.kind === "supervision" && live.live).toBe(true);
    expect(live.kind === "supervision" && live.lines.at(-1)?.id).toBe("live");
  });

  it("keeps interruption and the last reply inside the complete quiet trace", () => {
    const stop: Item = { id: "n3#0", seq: 3, role: "system", kind: "notice", text: "已中断" }; // i18n-ignore: transcript interrupt marker
    const [line] = chatLines([user(1), answer(2), stop]);
    expect(line.kind).toBe("supervision");
    expect(line.kind === "supervision" && line.lines.map((l) => l.kind)).toEqual(["segment", "notice"]);
  });

  it.each(["notify_user", "ask_user", "mcp__persona__notify_user", "mcp__persona__ask_user"])("promotes successful %s but never an error", (name) => {
    const use = call(2, name);
    expect(chatLines([user(1), use, result(3, use), answer(4)])[0].kind).toBe("segment");
    expect(chatLines([user(1), use, result(3, use, receipt("user"), true), answer(4)])[0].kind).toBe("supervision");
  });

  it("shows pending ask_user work but not an unfinished notify_user or supervision_finish", () => {
    for (const name of ["ask_user", "mcp__persona__ask_user"]) {
      expect(chatLines([user(1), call(2, name)], { live: true })[0].kind).toBe("segment");
    }
    for (const name of ["notify_user", "supervision_finish"]) {
      const [line] = chatLines([user(1), call(2, name, { visibility: "user" })], { live: true });
      expect(line.kind).toBe("supervision");
      expect((line.kind === "supervision" || line.kind === "segment") && line.live).toBe(true);
    }
  });

  it("uses the successful finish receipt, not its requested visibility or an error", () => {
    const use = call(2, "mcp__persona__supervision_finish", { visibility: "user", summary: "Requested user visibility" });
    for (const text of [receipt("silent"), "not JSON", "null", '{"level":"user"}', '[{"visibility":"user"}]']) {
      expect(chatLines([user(1), use, result(3, use, text), answer(4)])[0].kind).toBe("supervision");
    }
    expect(chatLines([user(1), use, result(3, use, receipt("user")), answer(4)])[0].kind).toBe("segment");
    expect(chatLines([user(1), use, result(3, use, receipt("user"), true), answer(4)])[0].kind).toBe("supervision");
    expect(supervisionTool("mcp__fictional__notify_user")).toBeUndefined();
  });

  it.each(["accepted", "needs_user", "rework", "cancelled"])("keeps successful task_review %s consistent with fold promotion", (verdict) => {
    const use = call(2, "mcp__persona__task_review", { verdict });
    expect(chatLines([user(1), use, result(3, use), answer(4)])[0].kind).toBe(verdict === "accepted" || verdict === "needs_user" ? "segment" : "supervision");
    expect(chatLines([user(1), use, result(3, use, "error", true), answer(4)])[0].kind).toBe("supervision");
  });

  it("associates a late result with its call rather than the next real user's turn", () => {
    const use = call(2, "notify_user");
    const lines = chatLines([user(1), use, user(3, "Next real request", { queued: true }), result(4, use), answer(5)]);
    expect(lines.map((l) => l.kind)).toEqual(["segment", "segment"]);
    expect(lines[0].kind === "segment" && lines[0].seg.steps[0].kind === "tool" && lines[0].seg.steps[0].result?.id).toBe("r4#0");
  });

  it("rebuilds visibility after reverse-order paged merges and keeps stable line ids", () => {
    const use = call(2, "supervision_finish");
    let page = mergeItems([], [result(3, use, receipt("user")), answer(4), user(5, "Next real request", { queued: true })]);
    expect(chatLines(page).map((l) => l.kind)).toEqual(["segment", "segment"]); // no anchor means no invented triage
    page = mergeItems(page, [user(1), use, result(3, use, receipt("user"))]);
    expect(chatLines(page).map((l) => l.kind)).toEqual(["segment", "segment"]);
    expect(chatLines(page)[0].id).toBe("seg:u1#0");
  });

  it("retains authoritative folded promotion through partial overlap, replacement and later silent finishes", () => {
    let page = mergeItems([], [user(1), fold(true), answer(6)]);
    expect(chatLines(page)[0].kind).toBe("segment");
    const read = call(2, "Read");
    page = mergeItems(page, [read]);
    expect(page.some((i) => i.kind === "fold")).toBe(false);
    expect(chatLines(page)[0].kind).toBe("segment");
    const silent = call(4, "supervision_finish", { visibility: "silent" });
    page = mergeItems(page, [read, result(3, read), silent, result(5, silent, receipt("silent"))]);
    expect(chatLines(page)[0].kind).toBe("segment");
    expect(chatLines(mergeItems([], [user(1), fold(), answer(6)]))[0].kind).toBe("supervision");
    expect(chatLines(page).map((l) => l.id)).toEqual(["seg:u1#0"]);
  });
});
