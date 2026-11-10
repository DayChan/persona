import Foundation
import Testing
@testable import Persona

@MainActor
struct ChatStoreTests {
    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, tool: String? = nil, id: String? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: id, toolName: tool, toolInput: nil, isError: nil, image: nil)
    }

    @Test func groupsToolRunsAndAttachesResults() {
        let items = [
            item(1, .text, role: "user", text: "hi"),
            item(2, .thinking, text: "plan"),
            item(3, .toolUse, tool: "Bash", id: "a"),
            item(4, .toolResult, role: "user", text: "ok", id: "a"),
            item(5, .thinking, text: "next"),
            item(6, .toolUse, tool: "Read", id: "b"),
            item(7, .text, text: "done"),
        ]
        let rows = ChatStore.buildRows(items)
        #expect(rows.count == 4)
        guard case .tools(let steps) = rows[2] else { Issue.record("expected tools"); return }
        #expect(steps.count == 3)
        if case .call(let c) = steps[0] { #expect(c.result?.text == "ok") } else { Issue.record("expected call") }
    }

    /// `backlogBuilt` turns true only once the first backlog is in `blocks` (#597): a cold start with the chat cached looked
    /// for the saved reading place in the cache's lines, the page already merged, and gave up on it.
    @Test func backlogBuiltWaitsForTheBacklogsLines() async throws {
        let s = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString)) // nothing on disk
        s.merge([item(9, .text, role: "user", text: "最后一问"), item(10, .text, text: "最后一答")]) // the cache's tail
        try await Task.sleep(for: .milliseconds(200))
        #expect(!s.loadedOnce && !s.backlogBuilt)

        let page = (1...10).map { item($0, .text, role: $0 % 2 == 1 ? "user" : "assistant", text: "第 \($0) 条") }
        s.merge(page, backlog: true, hasMore: false)
        #expect(s.loadedOnce)
        #expect(!s.backlogBuilt && s.isRebuilding) // merged, its lines not built yet
        for _ in 0..<50 where !s.backlogBuilt { try await Task.sleep(for: .milliseconds(20)) }
        #expect(s.backlogBuilt && !s.isRebuilding)
        #expect(s.rows.count == 10) // built from the whole page
    }

    @Test func aBacklogWithNothingNewIsBuiltAtOnce() async throws {
        let s = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        let items = [item(1, .text, role: "user", text: "问"), item(2, .text, text: "答")]
        s.merge(items)
        for _ in 0..<50 where s.rows.count < 2 { try await Task.sleep(for: .milliseconds(20)) }
        s.merge(items, backlog: true, hasMore: false) // the hub's page holds what the cache had: no rebuild comes
        #expect(s.backlogBuilt)
    }

    @Test func parsesSpinnerAndOptions() {
        let screen = """
        ⏺ Working on it
        ✻ Cogitating… (12s · ↓ 3.2k tokens · esc to interrupt)
        ──────────────────────────────────────────
        ❯ 1. Yes
          2. No, exit
        """
        #expect(ScreenParser.spinner(screen) == "Cogitating… (12s · ↓ 3.2k tokens)")
        let opts = ScreenParser.options(screen)
        #expect(opts.map(\.key) == ["1", "2"])
        #expect(opts.first?.selected == true)
    }

    @Test func pairURL() {
        let c = ServerConfig(pairURL: "persona://pair?url=http%3A%2F%2Fhost%3A18660&token=abc")
        #expect(c?.url.absoluteString == "http://host:18660")
        #expect(c?.token == "abc")
    }

    @Test func goDates() {
        #expect(JSON.parseDate("2026-10-24T11:15:05.251344371Z") != nil)
        #expect(JSON.parseDate("2026-10-24T11:15:05Z") != nil)
        #expect(JSON.parseDate("0001-01-01T00:00:00Z")?.isMeaningful == false)
    }
}

struct FilePathTests {
    @Test func keepsTildeAndFindsPaths() {
        let text = "cat > ~/.config/systemd/user/personad.service 并读 /tmp/persona-tasks/05-image-viewer.md 和 ios/Persona/Foo.swift，网址 https://example.com/a/b 不算"
        let paths = FilePaths.find(in: text)
        #expect(paths == ["~/.config/systemd/user/personad.service", "/tmp/persona-tasks/05-image-viewer.md", "ios/Persona/Foo.swift"])
        #expect(FilePaths.find(in: "模型 deepseek/v4.1-flash-chain 和 model_hub/es1_orange_o50").isEmpty)
    }
}

@MainActor
struct TurnTests {
    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, tool: String? = nil, id: String? = nil, queued: Bool? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: id, toolName: tool, toolInput: nil, isError: nil, image: nil, queued: queued)
    }

    @Test func finishedTurnGathersQueuedMessagesAndKeepsTheLastAnswer() {
        let items = [
            item(1, .text, role: "user", text: "do it"),
            item(2, .text, text: "你好，我先看看"),
            item(3, .toolUse, tool: "Bash", id: "a"),
            item(4, .toolResult, role: "user", text: "ok", id: "a"),
            item(5, .text, role: "user", text: "also this", queued: true),
            item(6, .thinking, text: "hmm"),
            item(7, .toolUse, tool: "Read", id: "b"),
            item(8, .text, text: "done: here is the result"),
            item(9, .recap, role: "system", text: "recap"),
            item(10, .text, role: "user", text: "next"),
            item(11, .text, text: "sure"),
        ]
        let blocks = ChatBlock.build(ChatStore.buildRows(items))
        #expect(blocks.count == 3)
        guard case .turn(let t) = blocks[0] else { Issue.record("turn expected"); return }
        #expect(t.users.count == 2)
        #expect(t.toolCount == 2)
        #expect(t.interimCount == 1)
        if case .assistant(let a) = t.answer { #expect(a.text == "done: here is the result") } else { Issue.record("answer") }
        if case .row(.recap) = blocks[1] {} else { Issue.record("recap stays standalone") }
    }

    @Test func compactionMidTurnDoesNotSplitIt() {
        let items = [
            item(1, .text, role: "user", text: "do it"),
            item(2, .toolUse, tool: "Bash", id: "a"),
            item(3, .notice, role: "system", text: "上下文已压缩"),
            item(4, .toolUse, tool: "Read", id: "b"),
            item(5, .text, text: "done"),
        ]
        let blocks = ChatBlock.build(ChatStore.buildRows(items))
        #expect(blocks.count == 1)
        guard case .turn(let t) = blocks[0] else { Issue.record("turn expected"); return }
        #expect(t.users.count == 1)
        #expect(t.toolCount == 2)
    }

    @Test func bubbleHidesTheImageNote() {
        #expect(UserBubble.display("看这个\n\n相关图片：/a/1.jpg、/a/2.jpg") == "看这个")
        #expect(UserBubble.display("看这个\n\n（我从手机发来了图片，请用 Read 查看：/a/1.jpg）") == "看这个")
        #expect(UserBubble.display("相关图片：/a/1.jpg").isEmpty) // only photos: thumbnails, no bubble
        #expect(UserBubble.imagePaths("看这个\n\n相关图片：/a/1.jpg、/a/2.jpg") == ["/a/1.jpg", "/a/2.jpg"])
        #expect(UserBubble.imagePaths("看\n\n（我从手机发来了图片，请用 Read 查看：/a/1.jpg）") == ["/a/1.jpg"])
        #expect(UserBubble.imagePaths("没有图片").isEmpty)
        let tagged = "看这个\n\n<attached_images>\n/a/1.jpg\n/a/2.jpg\n</attached_images>"
        #expect(UserBubble.display(tagged) == "看这个")
        #expect(UserBubble.imagePaths(tagged) == ["/a/1.jpg", "/a/2.jpg"])
        // typed mid-sentence, not the note
        #expect(UserBubble.display("改成，相关图片：xx 就行") == "改成，相关图片：xx 就行")
    }

    @Test func cacheKeepsTheLastTwoExchanges() {
        let items = [
            item(1, .text, role: "user", text: "one"), item(2, .text, text: "a1"),
            item(3, .text, role: "user", text: "two"), item(4, .text, role: "user", text: "two more", queued: true),
            item(5, .text, text: "a2"),
            item(6, .text, role: "user", text: "three"), item(7, .text, text: "a3"),
        ]
        #expect(ChatCache.tail(items, turns: 2).map(\.seq) == [3, 4, 5, 6, 7])
    }

    /// Whether a subagent runs goes by its Agent call's result in the chat (#5602), not only by the hub's list: done once
    /// the result is in, whatever the list says; one sent to the background, once it reported back.
    @Test func aSubagentRunsUntilItsCallsResultIsIn() {
        let agent = Subagent(id: "x", sessionID: "s@x", toolUseID: "a", running: true)
        let asked = [item(1, .text, role: "user", text: "go"), item(2, .toolUse, tool: "Agent", id: "a")]
        #expect(agent.running(in: TaskIndex.build(asked)))
        let answered = asked + [item(3, .toolResult, role: "user", text: "found it", id: "a")]
        #expect(!agent.running(in: TaskIndex.build(answered))) // the list still says running: the result wins
        var stale = agent
        stale.running = false
        #expect(!stale.running(in: TaskIndex.build(asked))) // no result yet, its session not at work: the list's word
        #expect(stale.running(in: TaskIndex.build(asked), sessionWorks: true)) // …at work: it runs
        #expect(Subagent(id: "y", sessionID: "s@y", running: true).running(in: TaskIndex.build(answered))) // no call to go by

        // In the background: its receipt is a result, but it runs until its notice.
        let launched = asked + [item(3, .toolResult, role: "user", text: "Async agent launched successfully. agentId: x1", id: "a")]
        #expect(agent.running(in: TaskIndex.build(launched)))
        var done = item(4, .notice, role: "system", text: "Agent \"Explore\" completed")
        done.task = TaskNote(kind: "agent", status: "completed", title: "Explore", toolUseID: "a")
        #expect(!agent.running(in: TaskIndex.build(launched + [done])))
    }

    @Test func agentOverridesItsMutedSpace() {
        var p = Pins()
        p.spaces = ["server/app"]
        #expect(p.mutes(session: "server/a", space: "server/app"))         // muted by its space
        p = p.togglingMute(session: "server/a", space: "server/app")       // Unmute
        #expect(!p.mutes(session: "server/a", space: "server/app") && p.allowed == ["server/a"])
        #expect(p.mutes(session: "server/b", space: "server/app"))         // the others stay muted
        p = p.togglingMute(session: "server/a", space: "server/app")       // Mute again
        #expect(p.mutes(session: "server/a", space: "server/app") && p.sessions == ["server/a"] && p.allowed.isEmpty)
        p.spaces = []                                                       // space unmuted: own mute still holds
        #expect(p.mutes(session: "server/a", space: "server/app"))
        p = p.togglingMute(session: "server/a", space: "server/app")
        #expect(!p.mutes(session: "server/a", space: "server/app") && p.sessions.isEmpty && p.allowed.isEmpty)
    }

    @Test func noticeAfterARecapJoinsTheWork() {
        let items = [
            item(1, .text, role: "user", text: "go"), item(2, .text, text: "done"),
            item(3, .recap, role: "system", text: "recap"),
            item(4, .notice, role: "system", text: "Background command \"x\" completed (exit code 0)"),
            item(5, .text, text: "the command finished"),
        ]
        let blocks = ChatBlock.build(ChatStore.buildRows(items))
        #expect(blocks.count == 3) // turn, recap, a turn holding the notice in its work
        guard case .turn(let t) = blocks[2] else { Issue.record("turn expected"); return }
        #expect(t.users.isEmpty && t.process.count == 1)
        if case .assistant(let a) = t.answer { #expect(a.text == "the command finished") } else { Issue.record("answer") }
    }

    @Test func backgroundWakeEndsOnTheNewestWords() {
        var wake = item(4, .notice, role: "system", text: "Background command \"Release\" completed (exit code 0)")
        wake.wake = true
        let summary = "All four are done and pushed; here is what changed and how to try each of them on the phone."

        // A full summary, then a short follow-up once the build is up: the follow-up is the answer (#3783: the
        // longest used to be, and the follow-up went into the drawer); the summary is in the work before it.
        var items = [
            item(1, .text, role: "user", text: "go"), item(2, .toolUse, tool: "Bash", id: "a"),
            item(3, .text, text: summary),
            wake, item(5, .toolUse, tool: "Bash", id: "b"), item(6, .text, text: "build 42 is up"),
        ]
        var blocks = ChatBlock.build(ChatStore.buildRows(items))
        #expect(blocks.count == 1) // one exchange, not split at the wake
        guard case .turn(let a) = blocks[0] else { Issue.record("turn"); return }
        if case .assistant(let i) = a.answer { #expect(i.text == "build 42 is up") } else { Issue.record("answer") }
        #expect(a.wakes.map(\.id) == [wake.id])
        #expect(a.tail.isEmpty) // nothing after the newest words
        #expect(a.ordered.last?.id == "6#0")
        #expect(a.process.contains { $0.id == "3#0" }) // the summary: in the work line
        // Woken, at work, no new words yet: the pending continuation stays in the ordinary execution summary.
        blocks = ChatBlock.build(ChatStore.buildRows(Array(items.prefix(5))))
        guard case .turn(let w) = blocks[0] else { Issue.record("turn"); return }
        if case .assistant(let i) = w.answer { #expect(i.text == summary) } else { Issue.record("answer") }
        #expect(w.hasPendingWake)

        // "Waiting for it", then the summary after the wake: the summary is the answer, and every tool before it
        // (the wake included) is one stretch of work.
        items = [
            item(1, .text, role: "user", text: "go"), item(2, .toolUse, tool: "Bash", id: "a"),
            item(3, .text, text: "waiting for the build"),
            wake, item(5, .toolUse, tool: "Bash", id: "b"), item(6, .text, text: summary),
        ]
        blocks = ChatBlock.build(ChatStore.buildRows(items))
        guard case .turn(let b) = blocks[0], blocks.count == 1 else { Issue.record("turn"); return }
        if case .assistant(let i) = b.answer { #expect(i.text == summary) } else { Issue.record("answer") }
        #expect(b.tail.isEmpty && b.process.count == 4) // tool, interim words, wake, tool

        // Absorbed mid-turn (no wake): an ordinary turn, the last words are the answer.
        wake.wake = nil
        blocks = ChatBlock.build(ChatStore.buildRows(items.map { $0.seq == 4 ? wake : $0 }))
        guard case .turn(let c) = blocks[0] else { Issue.record("turn"); return }
        #expect(c.wakes.isEmpty)
    }

    @Test func wakeAfterARecapStaysInItsExchange() {
        var wake = item(4, .notice, role: "system", text: "Background command \"Release\" completed (exit code 0)")
        wake.wake = true
        let summary = "Agent tags and XML handling are done and pushed; the build is still packaging."
        let items = [
            item(1, .text, role: "user", text: "go"), item(2, .text, text: summary),
            item(3, .recap, role: "system", text: "recap"),
            wake, item(5, .toolUse, tool: "Bash", id: "b"), item(6, .text, text: "build 42 is up"),
        ]
        let blocks = ChatBlock.build(ChatStore.buildRows(items))
        #expect(blocks.count == 2) // the exchange (wake folded in), then the recap card where it was
        guard case .turn(let t) = blocks[0], case .row(.recap) = blocks[1] else { Issue.record("shape \(blocks)"); return }
        if case .assistant(let a) = t.answer { #expect(a.text == "build 42 is up") } else { Issue.record("answer") }
        #expect(t.wakes.count == 1 && t.tail.isEmpty)
    }

    @Test func catchUpNeverEndsTheHistory() {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        store.merge((100...110).map { item($0, .text, text: "m\($0)") }, backlog: true, hasMore: true)
        #expect(store.hasMore)
        // A catch-up after since_seq (only newer items) saying "nothing older": not about our start, ignored.
        store.merge((111...112).map { item($0, .text, text: "m\($0)") }, backlog: true, hasMore: false)
        #expect(store.hasMore)
        // No answer at all (newer hubs omit it on catch-ups): unchanged.
        store.merge([item(113, .text, text: "m")], backlog: true, hasMore: Bool?.none)
        #expect(store.hasMore)
    }

    private func store() -> ChatStore { ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString)) }
    private func texts(_ seqs: ClosedRange<Int>) -> [Item] { seqs.map { item($0, .text, text: "m\($0)") } }

    @Test func aBatchThatDoesNotContinueRevealsAHole() {
        let s = store()
        #expect(s.apply(texts(1...10), span: ItemSpan(history: "h", from: nil, to: 12), backlog: true, hasMore: false) == .none)
        #expect(s.covered == 12) // lines 11-12 had no items, still covered
        // Continues from 12: fine, and covered moves to its end.
        #expect(s.apply(texts(13...14), span: ItemSpan(history: "h", from: 12, to: 15)) == .none)
        #expect(s.covered == 15)
        // Starts after 20: lines 16-20 never came. Kept, but covered stays and the hole is to be fetched.
        #expect(s.apply(texts(21...22), span: ItemSpan(history: "h", from: 20, to: 22)) == .fill)
        #expect(s.covered == 15 && s.lastSeq == 22)
        // The fill: everything after 15, through 22.
        #expect(s.apply(texts(16...22), span: ItemSpan(history: "h", from: 15, to: 22)) == .none)
        #expect(s.covered == 22 && s.items.map(\.seq) == Array(1...10) + [13, 14] + Array(16...22)) // 11, 12, 15: no items
    }

    @Test func emptyBatchesMoveCoveredForward() {
        let s = store()
        s.apply(texts(1...3), span: ItemSpan(history: "h", from: nil, to: 3), backlog: true, hasMore: false)
        #expect(s.apply([], span: ItemSpan(history: "h", from: 3, to: 5)) == .none) // a title line, a mode line
        #expect(s.apply(texts(6...6), span: ItemSpan(history: "h", from: 5, to: 6)) == .none)
        #expect(s.covered == 6)
    }

    @Test func aCappedCatchUpAsksForTheRest() {
        let s = store()
        s.apply(texts(1...3), span: ItemSpan(history: "h", from: nil, to: 3), backlog: true, hasMore: false)
        #expect(s.apply(texts(4...9), span: ItemSpan(history: "h", from: 3, to: 9), backlog: true, moreAfter: true) == .fill)
        #expect(s.covered == 9)
    }

    @Test func anotherHistoryStartsOver() {
        let s = store()
        s.apply(texts(1...30), span: ItemSpan(history: "old", from: nil, to: 30), backlog: true, hasMore: false)
        // A page from the end of the replaced file: it is all there is.
        #expect(s.apply(texts(1...4), span: ItemSpan(history: "new", from: nil, to: 4), backlog: true, hasMore: false) == .none)
        #expect(s.items.map(\.seq) == Array(1...4) && s.covered == 4 && s.history == "new")
        // A continuing batch from yet another file: nothing here fits it, start from the latest page.
        #expect(s.apply(texts(9...9), span: ItemSpan(history: "newer", from: 8, to: 9)) == .reload)
        #expect(s.covered == 0)
    }

    @Test func aLatestPageThatDoesNotReachTheCacheReplacesIt() {
        let s = store()
        s.apply(texts(1...10), span: ItemSpan(history: "h", from: nil, to: 10), backlog: true, hasMore: true) // the old cache
        // Opened cold after several more exchanges: the latest page starts at 40, nothing in between is here.
        s.apply(texts(40...45), span: ItemSpan(history: "h", from: nil, to: 46), backlog: true, hasMore: true)
        #expect(s.items.map(\.seq) == Array(40...45))
        #expect(s.covered == 46 && s.hasMore)
        // One that overlaps is merged.
        s.apply(texts(44...48), span: ItemSpan(history: "h", from: nil, to: 48), backlog: true, hasMore: true)
        #expect(s.items.map(\.seq) == Array(40...48))
    }

    @Test func noFillBeforeABaseline() {
        let s = store()
        // A live push overtakes the subscribe backlog of an empty chat: no hole to fill from 0.
        #expect(s.apply(texts(50...51), span: ItemSpan(history: "h", from: 49, to: 51)) == .none)
        #expect(s.covered == 0)
        s.apply(texts(45...51), span: ItemSpan(history: "h", from: nil, to: 51), backlog: true, hasMore: true)
        #expect(s.covered == 51 && s.items.map(\.seq) == Array(45...51))
    }

    @Test func olderHubsWithoutSpansWorkAsBefore() {
        let s = store()
        #expect(s.apply(texts(1...3), span: ItemSpan(), backlog: true, hasMore: false) == .none)
        #expect(s.apply(texts(7...8), span: ItemSpan()) == .none)
        #expect(s.covered == 8)
        #expect(s.apply(texts(9...9), span: ItemSpan(), moreAfter: true) == .reload)
    }

    @Test func blocksOfALineSortNumerically() {
        let a = Item(id: "u#10", seq: 5, ts: nil, role: "user", kind: .image, text: nil, toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
        let b = Item(id: "u#2", seq: 5, ts: nil, role: "user", kind: .text, text: "x", toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
        #expect(ChatStore.ordered([a, b]).map(\.id) == ["u#2", "u#10"])
    }

    @Test func cacheTailKeepsWholeLines() {
        func it(_ id: String, _ seq: Int, _ role: String, _ kind: ItemKind) -> Item {
            Item(id: id, seq: seq, ts: nil, role: role, kind: kind, text: "t", toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
        }
        // Line 3 is an image and then the user's words: the tail starts at the image, not halfway through the line.
        let items = [it("a#0", 1, "user", .text), it("b#0", 2, "assistant", .text), it("c#0", 3, "user", .image), it("c#1", 3, "user", .text),
                     it("d#0", 4, "assistant", .text)]
        #expect(ChatCache.tail(items, turns: 1).map(\.id) == ["c#0", "c#1", "d#0"])
    }

    @Test func shortPendingMessagesNeedTheirOwnCopy() {
        #expect(PendingMessage.same("好", "好"))
        #expect(!PendingMessage.same("好", "好的，我先看看日志再说"))
        #expect(PendingMessage.same("帮我看一下这个构建为什么失败", "帮我看一下这个构建为什么失败 [Image #1]"))
    }

    @Test func wakeSummaryReadsInChinese() {
        #expect(NoticeRow.wakeSummary("Background command \"Release master\" completed (exit code 0)") == ("后台命令完成 · Release master", false))
        #expect(NoticeRow.wakeSummary("Background command \"Build\" failed with exit code 65").failed)
        #expect(NoticeRow.wakeSummary("Agent \"Explore\" completed").text == "子 Agent 完成 · Explore")
        #expect(NoticeRow.wakeSummary("后台任务有更新").text == "后台任务有更新")
    }

    @Test func pendingMatchesTheTranscriptCopy() {
        #expect(PendingMessage.same("[Image #21]1 多了一个空格色块\n2 有的 agent", "1 多了一个空格色块 2 有的 agent"))
        #expect(PendingMessage.same("看这个", "看这个\n\n<attached_images>\n/a/1.jpg\n</attached_images>"))
        #expect(!PendingMessage.same("别的话", "看这个"))
        #expect(UserBubble.display("[Image #21]1 多了一个色块") == "1 多了一个色块")
    }

    @Test func cutOutputsKnowTheirWholeSize() throws {
        let json = #"{"id":"u1#0","seq":3,"role":"user","kind":"tool_result","text":"a\nb\n…（已截断，点开看全文，共 4874 字节）","full_size":4874}"#
        let it = try JSON.decoder.decode(Item.self, from: Data(json.utf8))
        #expect(it.fullSize == 4874 && it.cutText == "a\nb")
        let whole = try JSON.decoder.decode(Item.self, from: Data(#"{"id":"u1#0","seq":3,"role":"user","kind":"tool_result","text":"a\nb"}"#.utf8))
        #expect(whole.fullSize == nil && whole.cutText == "a\nb")
    }

    @Test func watchShowsMessagesWithoutMarkup() {
        let ref = ContextRef(kind: "context", title: "设计稿 \"v2\"", url: "https://example.com/d", text: "选中的文字")
        #expect(WatchText.shown(ref.xml + "\n\n看看这个") == "〔设计稿 \"v2\"〕\n看看这个")
        #expect(WatchText.shown("[Image #1] [Image #2] 这个按钮歪了\n\n<attached_images>\n/tmp/a.jpg\n</attached_images>") == "〔图片〕 这个按钮歪了\n〔图片〕")
        #expect(WatchText.shown("<pasted_content id=\"x\">长文本</pasted_content>") == "长文本")
        #expect(WatchText.shown("普通的话") == "普通的话")
        // Markdown's marks go; the words stay.
        #expect(WatchText.shown("## 支持 HEIC\n\n1. 用 `sips` 把 HEIC 转成 JPEG") == "支持 HEIC\n\n1. 用 sips 把 HEIC 转成 JPEG")
        #expect(WatchText.shown("**粗体**和*斜体*，看[文档](https://example.com/d)") == "粗体和斜体，看文档")
        #expect(WatchText.shown("- 一\n  - 二\n> 引用\n---\n```swift\nlet a = 1\n```") == "• 一\n  • 二\n引用\n\nlet a = 1")
        #expect(WatchText.shown("2 * 3 = 6，snake_case_name") == "2 * 3 = 6，snake_case_name")
    }

    @Test func timeoutsAreNotShown() {
        #expect(URLError(.timedOut).shown == nil)
        #expect(URLError(.cancelled).shown == nil)
        #expect(APIError(status: 504, message: "gateway timeout").shown == nil)
        #expect(APIError(status: 502, message: "node call: context deadline exceeded").shown == nil)
        #expect(APIError(status: 502, message: "machine offline").shown == "machine offline")
        #expect(URLError(.notConnectedToInternet).shown == nil)
        #expect(URLError(.cannotConnectToHost).isOffline)
        #expect(!URLError(.timedOut).isOffline)
        #expect(URLError(.badServerResponse).shown != nil)
    }

    @Test func subagentItemsComeFromTheSubagentsTranscript() throws {
        let main = SessionKey(machineID: "m", sessionID: "s1")
        let agent = try JSON.decoder.decode(Subagent.self, from: Data(#"{"id":"a1","session_id":"s1/subagents/agent-a1","running":false}"#.utf8))
        #expect(ChatView.shownKey(main, viewing: nil) == main)
        #expect(ChatView.shownKey(main, viewing: agent) == SessionKey(machineID: "m", sessionID: agent.sessionID))
    }

    @Test func aBatchFromTheFirstLineIsABaseline() {
        let s = ChatStore(key: SessionKey(machineID: "t", sessionID: "baseline-\(UUID())"))
        // A new session watched from its first line: pushes from 0, then one is lost.
        #expect(s.apply([item(1, .text, role: "user", text: "hi")], span: ItemSpan(history: "h", from: 0, to: 1)) == .none)
        #expect(s.covered == 1)
        #expect(s.apply([item(4, .text, text: "x")], span: ItemSpan(history: "h", from: 3, to: 4)) == .fill)
    }

    @Test func aLostFirstPushOfANewSessionIsNoticed() {
        let s = ChatStore(key: SessionKey(machineID: "t", sessionID: "first-\(UUID())"))
        // Opened before the transcript existed: an empty backlog without a span. Then the first push is lost.
        #expect(s.apply([], span: ItemSpan(), backlog: true, hasMore: false) == .none)
        #expect(s.apply([item(5, .text, text: "x")], span: ItemSpan(history: "h", from: 4, to: 5)) == .reload)
        // Before any backlog came, the one on its way sets the baseline: nothing to do yet.
        let t = ChatStore(key: SessionKey(machineID: "t", sessionID: "first2-\(UUID())"))
        #expect(t.apply([item(5, .text, text: "x")], span: ItemSpan(history: "h", from: 4, to: 5)) == .none)
    }

    @Test func theLatestPageKeepsPushesThatOvertookIt() {
        let s = ChatStore(key: SessionKey(machineID: "t", sessionID: "replace-\(UUID())"))
        _ = s.apply([item(1, .text, role: "user", text: "a")], span: ItemSpan(history: "h", from: 0, to: 1))
        _ = s.apply([item(9, .text, text: "pushed meanwhile")], span: ItemSpan(history: "h", from: 8, to: 9))
        let resets = s.resets
        s.replace(with: [item(7, .text, role: "user", text: "b"), item(8, .text, text: "c")], hasMore: true, span: ItemSpan(history: "h", to: 8))
        #expect(s.items.map(\.seq) == [7, 8, 9] && s.covered == 8 && s.resets == resets + 1)
    }

    @Test func frozenChatCountsItemsUpToTheSnapshot() {
        let items = [(1, 0), (2, 0), (2, 1), (3, 0), (5, 0)].map { seq, part in
            Item(id: "i\(seq)#\(part)", seq: seq, ts: nil, role: "assistant", kind: .text, text: "x")
        }
        #expect(ChatView.prefixCount(items) { ($0.seq, $0.part) <= (2, 0) } == 2)
        #expect(ChatView.prefixCount(items) { ($0.seq, $0.part) <= (4, 0) } == 4)
        #expect(ChatView.prefixCount(items) { ($0.seq, $0.part) <= (0, 0) } == 0)
        #expect(ChatView.prefixCount(items) { ($0.seq, $0.part) <= (9, 0) } == 5)
    }

    @Test func picturesSentAfterAWakeStayWithTheirMessage() {
        // A turn a background task woke up again; then a message with a picture typed while the agent worked.
        var img = item(5, .image, role: "user")
        img.id = "m#1"
        img.image = ImagePayload(mime: "image/png", data: "iVBORw0KGgo=")
        img.queued = true
        var wake = item(3, .notice, role: "system", text: "后台任务完成")
        wake.wake = true
        var msg = item(5, .text, role: "user", text: "看这个")
        msg.id = "m#0"
        msg.queued = true
        let items = [item(1, .text, role: "user", text: "开始"), item(2, .text, text: "好的，在跑"), wake,
                     item(4, .text, text: "跑完了，结果如下：……"), msg, img, item(6, .text, text: "收到")]
        let blocks = ChatBlock.build(ChatStore.buildRows(items))
        guard case .turn(let t) = blocks.last else { Issue.record("no turn"); return }
        #expect(t.ordered.contains { if case .image(let i) = $0 { i.id == "m#1" } else { false } })
        #expect(!t.tail.contains { if case .image = $0 { true } else { false } })
    }

    @Test func contextTagRoundTrips() {
        let ref = ContextRef(kind: "edit", title: "A \"quoted\" <title>", url: "https://x.y/a?b=1&c=2", text: "选中的 <b>文字</b>")
        let msg = ref.xml + "\n\n按要求修改上面选中的内容：更短"
        let parsed = ContextRef.parse(msg)
        #expect(parsed.refs.count == 1)
        #expect(parsed.refs[0].kind == "edit" && parsed.refs[0].title == ref.title && parsed.refs[0].url == ref.url && parsed.refs[0].text == ref.text)
        #expect(parsed.rest == "按要求修改上面选中的内容：更短")
        #expect(ContextRef.parse("没有标签").refs.isEmpty)
    }

    /// A Feishu /agent request (prompts/lark): instructions and context in blocks without a url, the request outside.
    static let larkAgentMessage = """
        <persona-context kind="instructions" title="给 Agent 的说明">
        示例用户在飞书「发布群」里用 /agent 叫你。

        ## 怎么做

        - 做完回复一次。
        </persona-context>
        <persona-context kind="context" title="请求的位置">
        位置：群聊「发布群」
        </persona-context>
        <persona-context kind="doc" title="文档 · 手册" url="https://example.com/d">
        第一步
        </persona-context>
        <persona-context kind="context" title="项目 · 发布">
        </persona-context>
        看下构建为什么挂了
        """

    @Test func larkAgentRequestShowsOnlyWhatWasWritten() {
        let parsed = ContextRef.parse(Self.larkAgentMessage)
        #expect(parsed.rest == "看下构建为什么挂了")
        #expect(parsed.refs.map(\.kind) == ["instructions", "context", "doc", "context"])
        #expect(parsed.refs.map(\.title) == ["给 Agent 的说明", "请求的位置", "文档 · 手册", "项目 · 发布"])
        #expect(parsed.refs[2].url == "https://example.com/d" && parsed.refs[1].url.isEmpty && parsed.refs[3].text.isEmpty)
        #expect(ContextRef.summary(parsed.refs) == "上下文 · 4 项")
        #expect(ContextRef.summary([parsed.refs[1]]) == "上下文 · 请求的位置")
        #expect(UserRowText(Self.larkAgentMessage).shown == "看下构建为什么挂了")
        // Attributes in another order still read.
        let swapped = ContextRef.parse("<persona-context url=\"https://x\" title=\"T\" kind=\"edit\">x</persona-context>改短")
        #expect(swapped.refs.first?.kind == "edit" && swapped.refs.first?.title == "T" && swapped.rest == "改短")
        #expect(WatchText.shown(Self.larkAgentMessage) == "〔上下文 4 项〕\n看下构建为什么挂了")
        // Only Persona context blocks fold; an unrelated tag remains message text.
        let foreign = Self.larkAgentMessage.replacingOccurrences(of: "persona-context", with: "other-context")
        #expect(ContextRef.parse(foreign).refs.isEmpty)
        #expect(ContextRef.parse(foreign).rest == foreign)
        let adjacent = ContextRef.parse("<persona-context kind=\"doc\" title=\"A\">a</persona-context>\n<persona-context kind=\"context\" title=\"B\">b</persona-context>\n问题")
        #expect(adjacent.refs.map(\.title) == ["A", "B"] && adjacent.rest == "问题")
    }
}
