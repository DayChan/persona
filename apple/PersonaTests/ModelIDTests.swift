import Foundation
import Testing
@testable import Persona

/// A model ID typed by hand (#1644): the same cases as the hub's `TestValidModelID` (server/internal/proto/modelid_test.go)
/// and the web's model-id.test.ts, so the three say the same thing about every ID.
struct ModelIDTests {
    static let valid = [
        "opus", "glm-5", "vendor/model-x", "opus[1m]", "claude-opus-4-6[1m]", "qwen3:32b", "claude-x@20250101",
        "gpt-5.1-codex", "meta-llama/Llama-3.1-8B-Instruct", "a_b", "7b",
        "~anthropic/claude-opus-latest", "~anthropic/claude-sonnet-latest[1m]", "~openai/gpt-mini-latest", "~x",
        "arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-x-v1:0",
        String(repeating: "a", count: ModelID.maxLength),
    ]

    static let invalid = [
        "", " ", "opus ", " opus", "a b", "a\tb", "a\nb", "a\rb", "a\u{0}b", "a\u{7f}b",
        "-x", "--dangerously-skip-permissions", ".hidden", "/abs", "[1m]", "_x",
        "$(id)", "a$b", "a`id`", "a'b", "a\"b", "a\\b", "a;b", "a|b", "a&b", "a>b", "a<b", "a*", "a?",
        "~", "~~x", "~-x", "~/x", "~.x", "~ x", "a~b", "x~",
        "a,b", "a=b", "a+b", "a#b", "a(b)", "a{b}", "a!b", "a%b", "模型", "é",
        String(repeating: "a", count: ModelID.maxLength + 1),
    ]

    @Test(arguments: valid) func takesWhatTheHubTakes(_ id: String) {
        #expect(ModelID.isValid(id), "\(id)")
        #expect(ModelID.problem(id) == nil)
    }

    @Test(arguments: invalid) func refusesWhatTheHubRefuses(_ id: String) {
        #expect(!ModelID.isValid(id), "\(id)")
    }

    /// The reason is the first thing wrong, in words that say what to do.
    @Test func saysWhy() {
        #expect(ModelID.problem("") == .empty)
        #expect(ModelID.problem("-x") == .start)
        #expect(ModelID.problem("~") == .start && ModelID.problem("~/x") == .start)
        #expect(ModelID.problem("模型") == .start)
        #expect(ModelID.problem("a b") == .space && ModelID.problem("a\tb") == .space)
        #expect(ModelID.problem("a\nb") == .control && ModelID.problem("a\u{7f}b") == .control)
        #expect(ModelID.problem("a$b") == .character("$"))
        #expect(ModelID.problem("vendor/模型") == .character("模"))
        #expect(ModelID.problem("a~b") == .character("~"))
        #expect(ModelID.problem(String(repeating: "a", count: 257)) == .tooLong)
        // In the app's language (the tests run in Chinese).
        #expect(ModelID.Problem.character("$").text.contains("「$」"))
        #expect(ModelID.Problem.tooLong.text.contains("256"))
        #expect(!ModelID.Problem.space.text.isEmpty && !ModelID.rule.isEmpty)
    }

    /// A pasted ID's spaces and line break around it go; inside it they still don't.
    @Test func aFieldIsTrimmed() {
        #expect(ModelID.cleaned("  vendor/model-x\n") == "vendor/model-x")
        #expect(ModelID.problem(ModelID.cleaned(" a b ")) == .space)
    }
}
