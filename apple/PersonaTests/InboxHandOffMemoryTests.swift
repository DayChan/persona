import Foundation
import Testing
@testable import Persona

/// Hand to AI remembers, per source, where an item was last handed (#2121): the shared cases in
/// spec/fixtures/inbox/handoff_memory.json, which web and Android read too.
@MainActor
struct InboxHandOffMemoryTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private struct Fixture: Decodable {
        struct Mute: Decodable { var tag: String; var value: String; var name: String }
        struct KeyCase: Decodable { var name: String; var source: String; var mutes: [Mute]?; var key: String? }
        struct ResolveCase: Decodable {
            var name: String
            var remembered: String?
            var listed: [String]
            var target: String?
            var last_time: Bool
            var gone: Bool
        }
        struct Recency: Decodable {
            var remember: [[String]]
            var order: [String]
            var recall: [String: String?]
        }
        var schema_version: Int
        var limit: Int
        var keys: [KeyCase]
        var resolve: [ResolveCase]
        var recency: Recency
    }

    private static func fixture() throws -> Fixture {
        let data = try Data(contentsOf: repo.appending(path: "spec/fixtures/inbox/handoff_memory.json"))
        return try JSONDecoder().decode(Fixture.self, from: data)
    }

    /// A memory of its own: a scratch defaults suite, empty.
    private func scratch() -> InboxHandOffMemory {
        let suite = "handoff-memory-tests-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        return InboxHandOffMemory(group: "", defaults: defaults)
    }

    @Test func sourceKeysFollowTheSharedCases() throws {
        let f = try Self.fixture()
        #expect(f.schema_version == 1)
        #expect(f.limit == InboxHandOffMemory.limit)
        for c in f.keys {
            let mutes = c.mutes?.map { InboxItem.MuteOption(tag: $0.tag, value: $0.value, name: $0.name) }
            #expect(InboxHandOffMemory.key(source: c.source, mutes: mutes) == c.key, "\(c.name)")
        }
    }

    @Test func resolvingFollowsTheSharedCases() throws {
        for c in try Self.fixture().resolve {
            let r = InboxHandOffMemory.resolve(c.remembered) { c.listed.contains($0.path) }
            #expect(r?.target.stored == c.target, "\(c.name)")
            #expect((r?.lastTime ?? false) == c.last_time, "\(c.name)")
            #expect((r?.gone ?? false) == c.gone, "\(c.name)")
        }
    }

    @Test func oneEntryPerSourceLatestFirst() throws {
        let f = try Self.fixture().recency
        let memory = scratch()
        for pair in f.remember { memory.remember(pair[0], target: InboxHandOffModel.Target(stored: pair[1])) }
        #expect(memory.entries.map(\.source) == f.order)
        for (key, target) in f.recall { #expect(memory.recall(key) == target, "\(key)") }
    }

    @Test func keepsTheMostRecentSources() {
        let memory = scratch()
        for i in 0..<(InboxHandOffMemory.limit + 5) { memory.remember("github|repo:a/\(i)", target: .main) }
        #expect(memory.entries.count == InboxHandOffMemory.limit)
        #expect(memory.recall("github|repo:a/\(InboxHandOffMemory.limit + 4)") == "main")
        #expect(memory.recall("github|repo:a/0") == nil)
    }

    @Test func aGoneGroupForgets() {
        let suite = "handoff-memory-tests-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        let work = InboxHandOffMemory(group: "work", defaults: defaults), home = InboxHandOffMemory(group: "", defaults: defaults)
        work.remember("github|repo:a/one", target: .newChat)
        home.remember("github|repo:a/one", target: .main)
        InboxHandOffMemory.forget(group: "work", defaults: defaults)
        #expect(work.recall("github|repo:a/one") == nil)
        #expect(home.recall("github|repo:a/one") == "main") // another group keeps its own
        defaults.removePersistentDomain(forName: suite)
    }
}
