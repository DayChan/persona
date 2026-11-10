import Foundation
@testable import Persona
import Testing

/// Comment blocks at the start of a message (`MessageComments`, docs/protocol/protocol.md「Quote and comment blocks at the
/// start of messages」): written and read back exactly as the web does, by the cases in spec/fixtures/comments.
struct MessageCommentsTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private struct Fixtures: Decodable {
        struct Comment: Decodable {
            struct Code: Decodable { var file: String; var from: Int; var to: Int; var old: Bool?; var diff: String?; var lang: String?; var omitted: Int? }
            struct Element: Decodable { var url: String; var selector: String; var component: String?; var source: String?; var text: String?; var omitted: Int? }
            var quoted: String
            var note: String
            var code: Code?
            var element: Element?

            /// Missing fields as their defaults (spec/fixtures/comments/README.md「Cases」).
            var comment: MessageComment {
                MessageComment(
                    quoted: quoted, note: note,
                    code: code.map { .init(file: $0.file, from: $0.from, to: $0.to, old: $0.old ?? false, diff: $0.diff.flatMap(MessageComments.diff),
                                           lang: $0.lang, omitted: $0.omitted ?? 0) },
                    element: element.map { .init(url: $0.url, selector: $0.selector, component: $0.component, source: $0.source, text: $0.text,
                                                 omitted: $0.omitted ?? 0) })
            }
        }

        struct Case: Decodable {
            struct Input: Decodable { var comments: [Comment]?; var text: String?; var message: String? }
            struct Expected: Decodable { var message: String?; var quote: String?; var comments: [Comment]?; var rest: String? }
            var name: String
            var input: Input
            var expected: Expected?
        }

        var version: Int
        var cases: [Case]
    }

    @Test func readsAndWritesTheSharedCases() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/comments/cases.json"))
        let fixtures = try JSONDecoder().decode(Fixtures.self, from: data)
        #expect(fixtures.version == 1)
        #expect(fixtures.cases.count >= 12)
        for c in fixtures.cases {
            if let comments = c.input.comments {
                #expect(MessageComments.message(comments.map(\.comment), text: c.input.text ?? "") == c.expected?.message, "\(c.name)")
                continue
            }
            let split = MessageComments.split(c.input.message ?? "")
            guard let expected = c.expected else {
                #expect(split == nil, "\(c.name)")
                continue
            }
            #expect(split?.quote == expected.quote, "\(c.name)")
            #expect(split?.comments == (expected.comments ?? []).map(\.comment), "\(c.name)")
            #expect(split?.rest == expected.rest, "\(c.name)")
        }
    }

    @Test func cutsTheNoteAtItsCapNeverInsideAPair() {
        let long = String(repeating: "n", count: MessageComments.maxNote + 10)
        #expect(MessageComments.clamp(long).utf16.count == MessageComments.maxNote)
        // An emoji is two UTF-16 units: one that would be cut in half is left out whole.
        let paired = String(repeating: "n", count: MessageComments.maxNote - 1) + "😀"
        #expect(MessageComments.clamp(paired) == String(repeating: "n", count: MessageComments.maxNote - 1))
        let block = MessageComments.block(MessageComment(quoted: "x", note: long), number: 1)
        #expect(block.components(separatedBy: "\n").last?.utf16.count == MessageComments.maxNote)
    }

    @Test func readsEveryDiffLabelItWrites() {
        let all: [MessageComment.Diff] = [.staged, .unstaged, .uncommitted, .commit("0123abcd"), .branchSince("4f2a9c1"), .since("4f2a9c1")]
        for d in all { #expect(MessageComments.diff(MessageComments.label(d)) == d) }
        #expect(MessageComments.label(.commit("0123456789abcdef")) == "commit 01234567")
        #expect(MessageComments.diff("tomorrow") == nil)
    }

    @Test func leavesAMessageWithoutBlocksAsItIs() {
        #expect(MessageComments.message([], text: "hello") == "hello")
        #expect(MessageComments.split("hello") == nil)
        #expect(MessageComments.split("  \n\n> Comment 1\n> Selected text:\n> > a")?.comments.count == 1)
    }
}
