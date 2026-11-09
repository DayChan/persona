// A model ID typed by hand (#1644): the same cases as the hub's TestValidModelID (server/internal/proto/modelid_test.go)
// and the apps' ModelIDTests, so the three say the same thing about every ID.
import { describe, expect, it } from "vitest";
import { cleanModelId, MAX_MODEL_ID_LEN, modelIdProblem, modelIdProblemText, modelIdRule, typedModelProblem, validModelId } from "./model-id";

const VALID = [
  "opus", "glm-5", "vendor/model-x", "opus[1m]", "claude-opus-4-6[1m]", "qwen3:32b", "claude-x@20250101",
  "gpt-5.1-codex", "meta-llama/Llama-3.1-8B-Instruct", "a_b", "7b",
  "~anthropic/claude-opus-latest", "~anthropic/claude-sonnet-latest[1m]", "~openai/gpt-mini-latest", "~x",
  "arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-x-v1:0",
  "a".repeat(MAX_MODEL_ID_LEN),
];

const INVALID = [
  "", " ", "opus ", " opus", "a b", "a\tb", "a\nb", "a\rb", "a\x00b", "a\x7fb",
  "-x", "--dangerously-skip-permissions", ".hidden", "/abs", "[1m]", "_x",
  "$(id)", "a$b", "a`id`", "a'b", 'a"b', "a\\b", "a;b", "a|b", "a&b", "a>b", "a<b", "a*", "a?",
  "~", "~~x", "~-x", "~/x", "~.x", "~ x", "a~b", "x~",
  "a,b", "a=b", "a+b", "a#b", "a(b)", "a{b}", "a!b", "a%b", "模型", "é",
  "a".repeat(MAX_MODEL_ID_LEN + 1),
];

describe("模型 ID 的规则（和 hub 的 proto.ValidModelID 一样）", () => {
  it.each(VALID)("takes %j", (id) => {
    expect(validModelId(id)).toBe(true);
    expect(modelIdProblem(id)).toBeNull();
  });

  it.each(INVALID)("refuses %j", (id) => {
    expect(validModelId(id)).toBe(false);
  });

  it("says why: the first thing wrong", () => {
    expect(modelIdProblem("")).toEqual({ kind: "empty" });
    expect(modelIdProblem("-x")).toEqual({ kind: "start" });
    expect(modelIdProblem("~/x")).toEqual({ kind: "start" });
    expect(modelIdProblem("模型")).toEqual({ kind: "start" });
    expect(modelIdProblem("a b")).toEqual({ kind: "space" });
    expect(modelIdProblem("a\nb")).toEqual({ kind: "control" });
    expect(modelIdProblem("a$b")).toEqual({ kind: "character", char: "$" });
    expect(modelIdProblem("vendor/模型")).toEqual({ kind: "character", char: "模" });
    expect(modelIdProblem("a~b")).toEqual({ kind: "character", char: "~" });
    expect(modelIdProblem("a".repeat(257))).toEqual({ kind: "too_long" });
    // counted in bytes, as the hub does
    expect(validModelId("a".repeat(256))).toBe(true);
  });

  it("in the page's language (tests run in Chinese)", () => {
    expect(modelIdProblemText({ kind: "character", char: "$" })).toContain("「$」");
    expect(modelIdProblemText({ kind: "too_long" })).toContain("256");
    expect(modelIdRule()).toContain("~");
  });

  it("a field: trimmed around, nothing to say while empty", () => {
    expect(cleanModelId("  vendor/model-x\n")).toBe("vendor/model-x");
    expect(typedModelProblem("  ")).toBe("");
    expect(typedModelProblem(" vendor/model-x ")).toBe("");
    expect(typedModelProblem("a b")).toBe("模型 ID 里不能有空格");
  });
});
