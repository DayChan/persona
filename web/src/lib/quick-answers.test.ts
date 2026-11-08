import { describe, expect, it } from "vitest";
import { quickAnswers, withHeld, type HeldRow } from "./quick-answers";
import type { Interaction } from "./types";

const ix = (o: Partial<Interaction>): Interaction => ({ id: "i", machine_id: "m", session_id: "s", kind: "permission", ...o }) as Interaction;
const opts = (n: number) => Array.from({ length: n }, (_, i) => ({ label: `选项 ${i + 1}` }));

describe("quickAnswers (#4090, as iOS's 需要你 cards)", () => {
  it("allows or denies a permission with the chat card's bodies", () => {
    const q = quickAnswers(ix({ tool_name: "Bash", tool_input: { command: "swift test" } }))!;
    expect(q.map((c) => c.label)).toEqual(["允许", "拒绝"]);
    expect(q[0].body).toEqual({ allow: true });
    expect(q[1].body).toEqual({ allow: false, message: "用户拒绝了" });
  });

  it("leaves a permission with fields to fill in to its chat", () => {
    expect(quickAnswers(ix({ fields: [{ key: "password", label: "授权码", secret: true }] }))).toBeNull();
  });

  it("answers one single-choice question of up to four options, by its wording", () => {
    const q = quickAnswers(ix({ kind: "question", questions: [{ question: "用哪个？", options: opts(2) }] }))!;
    expect(q.map((c) => c.label)).toEqual(["选项 1", "选项 2"]);
    expect(q[1].body).toEqual({ answers: { "用哪个？": "选项 2" } });
    expect(quickAnswers(ix({ kind: "question", questions: [{ question: "多", options: opts(4) }] }))).toHaveLength(4);
  });

  it("leaves several questions, ticks, more than four options or none to the chat", () => {
    const one = { question: "a", options: opts(2) };
    expect(quickAnswers(ix({ kind: "question", questions: [one, { ...one, question: "b" }] }))).toBeNull();
    expect(quickAnswers(ix({ kind: "question", questions: [{ ...one, multi_select: true }] }))).toBeNull();
    expect(quickAnswers(ix({ kind: "question", questions: [{ question: "a", options: opts(5) }] }))).toBeNull();
    expect(quickAnswers(ix({ kind: "question", questions: [{ question: "a", options: [] }] }))).toBeNull();
    // a free-text question as the hub sends it (#1573: a Go nil slice)
    expect(quickAnswers(ix({ kind: "question", questions: [{ question: "a", options: null }] }))).toBeNull();
  });

  it("presses a terminal dialog's plain options only, and only a dialog the hub read", () => {
    const options = [{ key: "1", label: "Yes" }, { key: "2", label: "No" }, { key: "3", label: "Type here", kind: "type_something" }];
    const q = quickAnswers(ix({ kind: "screen", dialog: { question: "继续？", options } }))!;
    expect(q.map((c) => c.body)).toEqual([{ option: "1" }, { option: "2" }]);
    expect(quickAnswers(ix({ kind: "screen", screen: "raw" }))).toBeNull();
    expect(quickAnswers(ix({ kind: "screen", dialog: { multi: true, options } }))).toBeNull();
    // a question with several steps read off the terminal, or its review tab, is several questions: the chat
    expect(quickAnswers(ix({ kind: "screen", dialog: { options, steps: [{ label: "a", done: false }, { label: "b", done: false }] } }))).toBeNull();
    expect(quickAnswers(ix({ kind: "screen", dialog: { options, review: true } }))).toBeNull();
  });

  it("never answers a plan or an MCP form in one tap", () => {
    expect(quickAnswers(ix({ kind: "plan", plan: "# 计划" }))).toBeNull();
    expect(quickAnswers(ix({ kind: "elicitation" }))).toBeNull();
  });
});

describe("withHeld", () => {
  const a = ix({ id: "a" }), b = ix({ id: "b" }), c = ix({ id: "c" });
  const d = ix({ id: "d" });
  const held = (x: Interaction, index: number, before?: string): HeldRow => ({ ix: x, index, before, phase: "done", pick: "allow", body: { allow: true } });

  it("keeps an answered card in its slot after the hub took it off the list", () => {
    expect(withHeld([a, c], [held(b, 1)]).map((r) => r.ix.id)).toEqual(["a", "b", "c"]);
    expect(withHeld([b, c], [held(a, 0)]).map((r) => [r.ix.id, Boolean(r.held)])).toEqual([["a", true], ["b", false], ["c", false]]);
  });

  it("keeps two answered in a row in place when the first goes (review of #4090)", () => {
    // A, B, C, D: A answered (before B), then B (before C); A folds away first
    expect(withHeld([c, d], [held(a, 0, "b"), held(b, 1, "c")]).map((r) => r.ix.id)).toEqual(["a", "b", "c", "d"]);
    expect(withHeld([c, d], [held(b, 1, "c")]).map((r) => r.ix.id)).toEqual(["b", "c", "d"]);
    // the row it stood before is gone too: its old slot
    expect(withHeld([a, d], [held(b, 1, "c")]).map((r) => r.ix.id)).toEqual(["a", "b", "d"]);
  });

  it("does not show a card twice while it is still on the list, and puts a slot past the end last", () => {
    expect(withHeld([a, b, c], [held(b, 1)]).map((r) => r.ix.id)).toEqual(["a", "b", "c"]);
    expect(withHeld([a], [held(c, 5)]).map((r) => r.ix.id)).toEqual(["a", "c"]);
  });
});
