import Foundation
import Testing
@testable import Persona

@MainActor
struct RebuildTests {
    /// A small deterministic generator (the same sequences every run).
    struct LCG {
        var state: UInt64
        mutating func next(_ n: Int) -> Int {
            state = state &* 6364136223846793005 &+ 1442695040888963407
            return Int((state >> 33) % UInt64(n))
        }
    }

    static func item(_ seq: Int, _ role: String, _ kind: ItemKind, text: String? = nil, tool: String? = nil, queued: Bool? = nil,
                     wake: Bool? = nil) -> Item {
        Item(id: "i\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: tool, toolName: tool == nil ? nil : "Bash",
             toolInput: nil, isError: nil, fullSize: nil, image: nil, queued: queued, from: nil, wake: wake)
    }

    /// Items as a session writes them: prompts, queued input, thoughts, tool calls whose results sometimes come after the
    /// next prompt, notices (wakes, interruptions), recaps.
    static func session(_ seed: UInt64, count: Int) -> [Item] {
        var r = LCG(state: seed)
        var out: [Item] = []
        var open: [String] = []
        var seq = 0
        while out.count < count {
            seq += 1
            switch r.next(12) {
            case 0, 1: out.append(item(seq, "user", .text, text: "问 \(seq)"))
            case 2: out.append(item(seq, "user", .text, text: "插一句", queued: true))
            case 3: out.append(item(seq, "assistant", .thinking, text: "想"))
            case 4, 5:
                let t = "t\(seq)"
                out.append(item(seq, "assistant", .toolUse, tool: t))
                open.append(t)
            case 6, 7:
                if !open.isEmpty { out.append(item(seq, "user", .toolResult, text: "ok", tool: open.remove(at: r.next(open.count)))) }
            case 8: out.append(item(seq, "assistant", .text, text: "答 \(seq)"))
            case 9: out.append(item(seq, "system", .notice, text: r.next(2) == 0 ? "已中断" : "后台任务完成", wake: r.next(2) == 0))
            case 10: out.append(item(seq, "system", .recap, text: "回顾"))
            default: out.append(item(seq, "assistant", .text, text: "说明"))
            }
        }
        return out
    }

    @Test func incrementalRebuildEqualsAFullOne() {
        for seed in UInt64(1)...40 {
            let all = Self.session(seed, count: 160)
            var settled: ChatStore.SettledRows?
            var r = LCG(state: seed)
            var n = 0
            while n < all.count {
                n = min(all.count, n + 1 + r.next(4)) // events arrive one at a time or in small bursts
                let items = Array(all[..<n])
                let built = ChatStore.rebuild(items, settled: settled)
                let rows = ChatStore.buildRows(items)
                #expect(built.rows == rows, "seed \(seed), \(n) items: rows")
                #expect(built.blocks == ChatBlock.build(rows), "seed \(seed), \(n) items: blocks")
                if built.rows != rows { return }
                settled = built.settled
            }
            // An older page arrives in front: what was settled no longer holds.
            let older = Self.session(seed + 1000, count: 30).map { var it = $0; it.id = "old-" + it.id; it.toolUseID = it.toolUseID.map { "old-" + $0 }; return it }
            let items = older + all
            let built = ChatStore.rebuild(items, settled: settled)
            let rows = ChatStore.buildRows(items)
            #expect(built.rows == rows && built.blocks == ChatBlock.build(rows), "seed \(seed): after an older page")
        }
    }

    @Test func supervisionAndMultiBlockInputsRebuildIncrementallyLikeAFullTranscript() {
        var supervise = Self.item(3, "user", .text, text: "Fictional supervision", queued: true)
        supervise.id = "u3#1"
        supervise.injected = Injected(kind: "system", state: "supervise", line: "Check tasks", level: "triage")
        var photo = Self.item(3, "user", .image)
        photo.id = "u3#0"
        photo.image = ImagePayload(mime: "image/png", data: "")
        var extra = Self.item(3, "user", .text, text: "Fictional additional details")
        extra.id = "u3#2" // A second nonqueued text block must not be an incremental boundary inside this input.
        var nextPhoto = photo
        nextPhoto.seq = 6
        nextPhoto.id = "u6#1"
        var next = Self.item(6, "user", .text, text: "Next genuine queued input", queued: true)
        next.id = "u6#0"
        let items = [Self.item(1, "user", .text, text: "First request"), Self.item(2, "assistant", .text, text: "First answer"),
                     photo, supervise, extra, Self.item(4, "assistant", .text, text: "Supervision answer"),
                     Self.item(5, "system", .recap, text: "Fictional recap"), next, nextPhoto,
                     Self.item(7, "assistant", .text, text: "Next answer"), Self.item(8, "user", .text, text: "Last request")]
        var settled: ChatStore.SettledRows?
        for count in 1...items.count {
            let prefix = Array(items.prefix(count))
            let built = ChatStore.rebuild(prefix, settled: settled)
            let fullRows = ChatStore.buildRows(ChatStore.picturesAfterTheirText(prefix))
            #expect(built.rows == fullRows, "\(count) items: rows")
            #expect(built.blocks == ChatBlock.build(fullRows), "\(count) items: blocks")
            settled = built.settled
        }
    }

    /// Cost of one event in a 1000-exchange session (printed, for the record): the full rebuild against the incremental one.
    @Test func incrementalRebuildIsCheap() {
        var items: [Item] = []
        var seq = 0
        for t in 0..<1000 {
            seq += 1; items.append(Self.item(seq, "user", .text, text: "第 \(t) 轮"))
            for _ in 0..<6 {
                seq += 1; items.append(Self.item(seq, "assistant", .toolUse, tool: "t\(seq)"))
                seq += 1; items.append(Self.item(seq, "user", .toolResult, text: "ok", tool: "t\(seq - 1)"))
            }
            seq += 1; items.append(Self.item(seq, "assistant", .text, text: "答"))
        }
        let settled = ChatStore.rebuild(items, settled: nil).settled
        seq += 1; items.append(Self.item(seq, "assistant", .thinking, text: "又想"))
        let clock = ContinuousClock()
        let full = clock.measure { _ = ChatBlock.build(ChatStore.buildRows(items)) }
        var built: (rows: [ChatRow], blocks: [ChatBlock], settled: ChatStore.SettledRows?)?
        let incremental = clock.measure { built = ChatStore.rebuild(items, settled: settled) }
        print("REBUILD-COST \(items.count) items: full \(full), incremental \(incremental)")
        #expect(built?.rows == ChatStore.buildRows(items))
    }

    /// Real sessions (exported by the server's TestCorpusDumpItems; `TEST_RUNNER_PERSONA_ITEMS_DIR=<dir> xcodebuild test`):
    /// the last 200 items arrive one by one; the incremental rebuild must equal the full one at every step. Prints
    /// the cost of both per event. Skipped without the directory.
    @Test func realSessionsRebuildIncrementally() throws {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_ITEMS_DIR"] else { return }
        let files = try FileManager.default.contentsOfDirectory(atPath: dir).filter { $0.hasSuffix(".json") }.sorted()
        var lines: [String] = []
        for name in files {
            let items = try JSON.decoder.decode([Item].self, from: Data(contentsOf: URL(fileURLWithPath: dir).appending(path: name)))
            let start = max(0, items.count - 200)
            var settled = ChatStore.rebuild(Array(items[..<start]), settled: nil).settled
            var full: [Duration] = [], inc: [Duration] = []
            var mismatches = 0
            let clock = ContinuousClock()
            for n in (start + 1)...items.count {
                let now = Array(items[..<n])
                var rows: [ChatRow] = [], blocks: [ChatBlock] = []
                full.append(clock.measure { rows = ChatStore.buildRows(now); blocks = ChatBlock.build(rows) })
                var built: (rows: [ChatRow], blocks: [ChatBlock], settled: ChatStore.SettledRows?)?
                inc.append(clock.measure { built = ChatStore.rebuild(now, settled: settled) })
                if built?.rows != rows || built?.blocks != blocks { mismatches += 1 }
                settled = built?.settled
            }
            func p(_ d: [Duration], _ q: Double) -> String {
                let s = d.sorted()
                let v = s[min(s.count - 1, Int(Double(s.count) * q))]
                return String(format: "%.2f", Double(v.components.attoseconds) / 1e15 + Double(v.components.seconds) * 1000)
            }
            lines.append("\(name)\t\(items.count)\t\(p(full, 0.5))\t\(p(full, 0.99))\t\(p(inc, 0.5))\t\(p(inc, 0.99))\t\(mismatches)")
            #expect(mismatches == 0, "\(name): incremental rebuild differs from the full one \(mismatches) times")
        }
        let out = "file\titems\tfull p50 ms\tfull p99 ms\tincremental p50 ms\tincremental p99 ms\tmismatches\n" + lines.joined(separator: "\n")
        try out.write(to: URL(fileURLWithPath: dir).appending(path: "rebuild-cost.tsv"), atomically: true, encoding: .utf8)
        print("REBUILD-REAL\n" + out)
    }
}

extension RebuildTests {
    @Test func backgroundRebuildConvergesAfterBurstsAndReplacement() async throws {
        let store = ChatStore(key: SessionKey(machineID: "rebuild-tests", sessionID: UUID().uuidString))
        let first = Self.session(8, count: 1800)
        for end in stride(from: 30, through: first.count, by: 30) {
            store.merge(Array(first[(end - 30)..<end]))
            await Task.yield()
        }
        var replacement = Self.session(91, count: 400)
        for index in replacement.indices { replacement[index].id = "replacement-" + replacement[index].id }
        store.replace(with: replacement, hasMore: false, span: ItemSpan(history: "new", from: nil, to: 400))
        let expected = ChatStore.buildRows(store.items)
        let deadline = ContinuousClock.now + .seconds(5)
        while store.rows != expected && ContinuousClock.now < deadline {
            try await Task.sleep(for: .milliseconds(5))
        }
        #expect(store.rows == expected)
        #expect(store.blocks == ChatBlock.build(expected))
        #expect(store.items.allSatisfy { $0.id.hasPrefix("replacement-") })
    }
}
