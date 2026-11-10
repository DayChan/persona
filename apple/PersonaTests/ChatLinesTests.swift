import Foundation
import Testing
@testable import Persona

/// The chat's lines (`ChatLines`) from items, without the chat: one exchange per message, open work lines unfolded,
/// several messages of a finished turn merged, BeautifulUI's runs of tool calls.
@MainActor
struct ChatLinesTests {
    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, tool: String? = nil, id: String? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: id, toolName: tool, toolInput: nil, isError: nil, image: nil)
    }

    private func blocks(_ items: [Item]) -> [ChatBlock] { ChatStore.rebuild(items, settled: nil).blocks }

    /// Each line's kind, short: user / answer / work / step / end / run / error.
    private func kinds(_ lines: [ChatLine]) -> [String] {
        lines.map { line in
            switch line.content {
            case .row(.user): "user"
            case .row(.assistant): "answer"
            case .row: "row"
            case .work: "work"
            case .workStep: "step"
            case .workEnd: "end"
            case .agentOut: "agent"
            case .agents(_, let calls, _, _): "agents\(calls.count)"
            case .toolRun: "run"
            case .apiError: "error"
            default: "other"
            }
        }
    }

    private let oneTurn = [
        { (s: ChatLinesTests) in s.item(1, .text, role: "user", text: "做一下") },
        { (s: ChatLinesTests) in s.item(2, .toolUse, tool: "Bash", id: "a") },
        { (s: ChatLinesTests) in s.item(3, .toolResult, role: "user", text: "ok", id: "a") },
        { (s: ChatLinesTests) in s.item(4, .toolUse, tool: "Read", id: "b") },
        { (s: ChatLinesTests) in s.item(5, .toolResult, role: "user", text: "ok", id: "b") },
        { (s: ChatLinesTests) in s.item(6, .text, text: "做完了") },
    ]

    @Test(arguments: [false, true]) func placeholdersOnlyAppearWithoutAnyContent(loaded: Bool) {
        let empty = ChatLines.placeholder(lines: [], hasTranscript: false, hasPending: false,
                                          hasLiveText: false, loadedOnce: loaded)
        #expect(empty == (loaded ? .empty : .loading))
        for (transcript, pending, live) in [(true, false, false), (false, true, false), (false, false, true)] {
            #expect(ChatLines.placeholder(lines: [], hasTranscript: transcript, hasPending: pending,
                                          hasLiveText: live, loadedOnce: loaded) == .none)
        }
        let feed = FeedItem(id: "card-only", conversation: "example", at: .distantPast, kind: .view, ref: "view_example", body: nil)
        let liveCard = ChatLines.daySections(ChatLines.mergeFeed([], [feed]))
        #expect(!liveCard.isEmpty)
        #expect(ChatLines.placeholder(lines: liveCard, hasTranscript: false, hasPending: false,
                                      hasLiveText: false, loadedOnce: loaded) == .none)
        let message = ChatLines(openWork: []).build(blocks([item(1, .text, role: "user", text: "示例消息")]), live: nil)
        #expect(ChatLines.placeholder(lines: message, hasTranscript: true, hasPending: false,
                                      hasLiveText: false, loadedOnce: loaded) == .none)
    }

    @Test func aMessageItsWorkAndItsAnswer() {
        let lines = ChatLines(openWork: []).build(blocks(oneTurn.map { $0(self) }), live: nil)
        #expect(kinds(lines) == ["user", "work", "answer"])
        #expect(lines[1].id.hasPrefix("w-"))
    }

    @Test func anOpenWorkLineListsItsStepsThenEnds() {
        let closed = ChatLines(openWork: []).build(blocks(oneTurn.map { $0(self) }), live: nil)
        let open = ChatLines(openWork: [closed[1].id]).build(blocks(oneTurn.map { $0(self) }), live: nil)
        #expect(kinds(open) == ["user", "work", "step", "step", "end", "answer"])
        // DEBUG -openAllWork flips every line: the opened one closes.
        #expect(kinds(ChatLines(openWork: [closed[1].id], openAll: true).build(blocks(oneTurn.map { $0(self) }), live: nil))
                == ["user", "work", "answer"])
    }

    /// Running subagents stay inside the summary too, and remain inspectable after completion.
    @Test func aRunningSubagentFoldsWithItsWorkLine() {
        let items = [item(1, .text, role: "user", text: "拆给子 Agent"), item(2, .toolUse, tool: "Agent", id: "a"),
                     item(3, .toolUse, tool: "Bash", id: "b"), item(4, .toolResult, role: "user", text: "ok", id: "b")]
        var lines = ChatLines(openWork: [])
        let closed = lines.build(blocks(items), live: "1#0")
        #expect(kinds(closed) == ["user", "work"])
        lines.openWork = [closed[1].id]
        #expect(kinds(lines.build(blocks(items), live: "1#0")) == ["user", "work", "step", "step", "end"])
        let done = blocks(items + [item(5, .toolResult, role: "user", text: "42 passed", id: "a")])
        #expect(kinds(lines.build(done, live: nil)) == ["user", "work", "step", "step", "end"])
        lines.openWork = []
        #expect(kinds(lines.build(done, live: nil)) == ["user", "work"])
    }

    /// Subagents launched one after another share one line, their cards in a grid (#5602); one alone stays a step.
    /// Folded, the running ones of a grid stay out together.
    @Test(arguments: [false, true]) func subagentsLaunchedTogetherShareAGrid(chips: Bool) {
        let items = [item(1, .text, role: "user", text: "并行拆给子 Agent"), item(2, .toolUse, tool: "Agent", id: "a"),
                     item(3, .toolUse, tool: "Agent", id: "b"), item(4, .toolUse, tool: "Task", id: "c"),
                     item(5, .toolUse, tool: "Bash", id: "d"), item(6, .toolUse, tool: "Agent", id: "e")]
        var lines = ChatLines(openWork: [], chipRuns: chips)
        let closed = lines.build(blocks(items), live: nil)
        #expect(kinds(closed) == ["user", "work"])
        lines.openWork = [closed[1].id]
        let open = lines.build(blocks(items), live: nil)
        #expect(kinds(open) == ["user", "work", "agents3", "step", "step", "end"])
        #expect(open[2].id == closed[1].id + "/agents-2#0")
    }

    @Test func beautifulUIFoldsToolCallsInARowIntoOneRun() {
        let b = blocks(oneTurn.map { $0(self) })
        let work = ChatLines(openWork: []).build(b, live: nil)[1].id
        let lines = ChatLines(openWork: [work], chipRuns: true).build(b, live: nil)
        #expect(kinds(lines) == ["user", "work", "run", "end", "answer"])
        // A call that went to the background stays on its own line.
        let apart = ChatLines(openWork: [work], chipRuns: true, foldsIntoRun: { $0.use.toolUseID != "b" }).build(b, live: nil)
        #expect(kinds(apart) == ["user", "work", "step", "step", "end", "answer"])
    }

    /// The first call of an opened run goes to the background (its result says so) and leaves the run for a line of
    /// its own: the rest keep the run's id, so it stays open (#5602: it went on under a new id, closed).
    @Test func aRunKeepsItsIdWhenItsFirstCallGoesToTheBackground() {
        let items = [item(1, .text, role: "user", text: "做一下"), item(2, .toolUse, tool: "Agent", id: "a"),
                     item(3, .toolResult, role: "user", text: "ok", id: "a"), item(4, .toolUse, tool: "Bash", id: "b"),
                     item(5, .toolResult, role: "user", text: "ok", id: "b"), item(6, .toolUse, tool: "Read", id: "c"),
                     item(7, .toolResult, role: "user", text: "ok", id: "c"), item(8, .text, text: "做完了")]
        let b = blocks(items)
        let work = ChatLines(openWork: []).build(b, live: nil)[1].id
        let before = ChatLines(openWork: [work], chipRuns: true).build(b, live: nil)
        guard let run = before.first(where: { if case .toolRun = $0.content { true } else { false } })?.id else {
            Issue.record("no run \(kinds(before))"); return
        }
        let after = ChatLines(openWork: [work], openRuns: [run], chipRuns: true, foldsIntoRun: { $0.use.toolUseID != "a" })
            .build(b, live: nil)
        #expect(kinds(after) == ["user", "work", "step", "run", "step", "step", "end", "answer"]) // a, then the open run
        #expect(after.contains { $0.id == run })
    }

    @Test func aFinishedTurnOfSeveralMessagesReadsMessageByMessage() {
        // The second message was sent while the agent worked (queued), so it joined the same turn: each message still
        // opens its own exchange, the work after it and the answer under it (#2512).
        var queued = item(4, .text, role: "user", text: "再那样")
        queued.queued = true
        let items = [item(1, .text, role: "user", text: "先这样"), item(2, .toolUse, tool: "Bash", id: "a"),
                     item(3, .toolResult, role: "user", text: "ok", id: "a"), queued,
                     item(5, .toolUse, tool: "Read", id: "b"), item(6, .toolResult, role: "user", text: "ok", id: "b"),
                     item(7, .text, text: "都好了")]
        let lines = ChatLines(openWork: []).build(blocks(items), live: nil)
        #expect(kinds(lines) == ["user", "work", "user", "work", "answer"])
        #expect(lines.map(\.id) == ["1#0", "w-1#0", "4#0", "w-4#0", "7#0"])
    }

    /// A running turn's words between its steps: the latest out of the work line; while the next ones stream in (drawn
    /// after the lines), in it already, so the chat does not grow under the new words and then shrink when they land.
    @Test func theRunningTurnsLatestWordsFoldWhileTheNextStream() {
        let items = [item(1, .text, role: "user", text: "查一下"), item(2, .toolUse, tool: "Bash", id: "a"),
                     item(3, .toolResult, role: "user", text: "ok", id: "a"), item(4, .text, text: "先看了第一处"),
                     item(5, .toolUse, tool: "Read", id: "b"), item(6, .toolResult, role: "user", text: "ok", id: "b")]
        let b = blocks(items)
        guard case .turn(let t) = b.last else { Issue.record("expected a turn"); return }
        #expect(kinds(ChatLines(openWork: []).build(b, live: t.id)) == ["user", "work", "answer"])
        let streaming = ChatLines(openWork: []).build(b, live: t.id, streaming: true)
        #expect(kinds(streaming) == ["user", "work"])
        guard case .work(_, let steps, _, _, _) = streaming[1].content else { Issue.record("expected a work line"); return }
        #expect(steps.count == 3, "the words are a step of the line now")
        // A finished turn has no streaming words of its own: as before.
        #expect(kinds(ChatLines(openWork: []).build(b, live: nil, streaming: true)) == ["user", "work", "answer"])
    }

    /// Every style defaults to folded execution; manual choices survive live updates and completion.
    @Test func aRunningTurnStaysFoldedAndManualExpansionSurvivesCompletion() {
        let running = [item(1, .text, role: "user", text: "查一下"), item(2, .toolUse, tool: "Bash", id: "a"),
                       item(3, .toolResult, role: "user", text: "ok", id: "a"), item(4, .toolUse, tool: "Read", id: "b")]
        let b = blocks(running)
        guard case .turn(let t) = b.last else { Issue.record("expected a turn"); return }
        var lines = ChatLines(openWork: [])
        let live = lines.build(b, live: t.id)
        #expect(kinds(live) == ["user", "work"])
        lines.openWork = [live[1].id]
        #expect(kinds(lines.build(b, live: t.id)) == ["user", "work", "step", "step", "end"])
        let more = blocks(running + [item(5, .toolResult, role: "user", text: "ok", id: "b"), item(6, .toolUse, tool: "Grep", id: "c")])
        #expect(kinds(lines.build(more, live: t.id)) == ["user", "work", "step", "step", "step", "end"])
        let done = blocks(running + [item(5, .toolResult, role: "user", text: "ok", id: "b"), item(6, .toolUse, tool: "Grep", id: "c"),
                                     item(7, .toolResult, role: "user", text: "ok", id: "c"), item(8, .text, text: "查好了")])
        #expect(kinds(lines.build(done, live: nil)) == ["user", "work", "step", "step", "step", "end", "answer"])
        lines.openWork = []
        #expect(kinds(lines.build(done, live: nil)) == ["user", "work", "answer"])
        #expect(kinds(ChatLines(openWork: []).build(done, live: nil)) == ["user", "work", "answer"], "reloaded history defaults to folded")
    }

    @Test func aWakeJoinsItsExchangesSummaryBeforeNewWordsArrive() {
        var wake = item(4, .notice, role: "system", text: "Forecast check")
        wake.wake = true
        let items = [item(1, .text, role: "user", text: "Will it rain?"),
                     item(2, .toolUse, tool: "Bash", id: "a"),
                     item(3, .text, text: "The forecast check is running."),
                     wake, item(5, .toolUse, tool: "Bash", id: "b")]
        let b = blocks(items)
        guard case .turn(let t) = b.last else { Issue.record("expected a turn"); return }
        #expect(t.hasPendingWake, "the wake marks unfinished continuation even before another text segment")
        let running = ChatLines(openWork: []).build(b, live: t.id)
        #expect(kinds(running) == ["user", "work"], "a pending wake folds its earlier narration")
        #expect(running.last?.holds("3#0") == false)
        let streaming = ChatLines(openWork: []).build(b, live: t.id, streaming: true)
        #expect(kinds(streaming) == ["user", "work"], "new live words replace the wake's pre-existing text slot")
        guard case .work(let id, let work, _, _, _) = running[1].content else {
            Issue.record("expected the wake's ordinary work summary")
            return
        }
        #expect(work.contains { $0.id == wake.id })
        #expect(work.contains { $0.id == "5#0" })
        let open = ChatLines(openWork: [id]).build(b, live: t.id)
        #expect(kinds(open) == ["user", "work", "step", "step", "step", "step", "end"])
        #expect(open.contains { if case .workStep(_, .step(.thought(let i)), _) = $0.content { i.id == wake.id } else { false } })
        #expect(open.contains { if case .workStep(_, .text(let i), _) = $0.content { i.id == "3#0" } else { false } })

        let complete = blocks(items + [item(6, .text, text: "No rain; keep the plan.")])
        let done = ChatLines(openWork: []).build(complete, live: nil)
        #expect(kinds(done) == ["user", "work", "answer"])
        #expect(done.last?.holds("6#0") == true)
        let inspected = ChatLines(openWork: [id]).build(complete, live: nil)
        #expect(inspected.contains { if case .workStep(_, .text(let i), _) = $0.content { i.id == "3#0" } else { false } })
    }

    @Test func liveNarrationReplacesThePreviousSegmentAndOnlyTheFinalAnswerRemains() {
        let running = [item(1, .text, role: "user", text: "Check the synthetic change"),
                       item(2, .text, text: "First narration"), item(3, .toolUse, tool: "Read", id: "a"),
                       item(4, .toolResult, role: "user", text: "ok", id: "a")]
        var renderer = ChatLines(openWork: [])
        let first = renderer.build(blocks(running), live: "1#0")
        let latest = renderer.build(blocks(running + [item(5, .text, text: "Latest narration")]), live: "1#0")
        #expect(kinds(first) == ["user", "work", "answer"])
        #expect(kinds(latest) == ["user", "work", "answer"])
        #expect(latest.last?.holds("5#0") == true)
        #expect(!latest.contains { $0.holds("2#0") })
        let done = blocks(running + [item(5, .text, text: "Latest narration"),
                                    item(6, .toolUse, tool: "Bash", id: "b"),
                                    item(7, .toolResult, role: "user", text: "ok", id: "b"),
                                    item(8, .text, text: "Final result")])
        let folded = renderer.build(done, live: nil)
        #expect(kinds(folded) == ["user", "work", "answer"])
        #expect(folded.last?.holds("8#0") == true)
        renderer.openWork = [folded[1].id]
        let open = renderer.build(done, live: nil)
        #expect(kinds(open) == ["user", "work", "step", "step", "step", "step", "end", "answer"])
        #expect(open.contains { if case .workStep(_, .text(let i), _) = $0.content { i.text == "First narration" } else { false } })
        #expect(open.contains { if case .workStep(_, .text(let i), _) = $0.content { i.text == "Latest narration" } else { false } })
    }

    @Test func theRunningTurnShowsEachMessage() {
        var queued = item(3, .text, role: "user", text: "b")
        queued.queued = true
        let items = [item(1, .text, role: "user", text: "a"), item(2, .toolUse, tool: "Bash", id: "x"),
                     queued, item(4, .toolUse, tool: "Bash", id: "y")]
        let b = blocks(items)
        guard case .turn(let t) = b.last else { Issue.record("expected a turn"); return }
        #expect(kinds(ChatLines(openWork: []).build(b, live: t.id)).filter { $0 == "user" }.count == 2)
    }

    /// Recaps in a row are one line (newest last in it, the first one's id); a recap with something after it stays.
    @Test func recapsInARowFold() {
        let items = [item(1, .text, role: "user", text: "看一下"), item(2, .text, text: "好了"),
                     item(3, .recap, text: "第一次"), item(4, .recap, text: "第二次"), item(5, .recap, text: "第三次")]
        let lines = ChatLines(openWork: []).build(blocks(items), live: nil)
        guard case .recaps(let id, let recaps)? = lines.last?.content else {
            Issue.record("no merged recaps: \(lines.map(\.id))")
            return
        }
        #expect(recaps.map(\.text) == ["第一次", "第二次", "第三次"])
        #expect(id == "rc-3#0")
        #expect(lines.last?.holds("5#0") == true)

        let apart = [item(1, .recap, text: "一"), item(2, .text, role: "user", text: "在吗"), item(3, .text, text: "在"), item(4, .recap, text: "二")]
        let kept = ChatLines(openWork: []).build(blocks(apart), live: nil)
        #expect(kept.filter { if case .row(.recap) = $0.content { true } else { false } }.count == 2)
        #expect(!kept.contains { if case .recaps = $0.content { true } else { false } })
    }

    /// The newest recap is drawn open as the chat's last line (#1360), in the flow: only while it is the last line and
    /// still pending; the earlier recaps of a folded line stay a chip above it.
    @Test func theOpenRecapIsTheLastLine() {
        let one = ChatLines(openWork: []).build(blocks([item(1, .text, role: "user", text: "看一下"), item(2, .text, text: "好了"),
                                                       item(3, .recap, text: "回顾")]), live: nil)
        let recap = one.last.flatMap { if case .row(.recap(let i)) = $0.content { i } else { nil } }
        let open = ChatLines.openRecap(one, pending: recap)
        #expect(open?.line == one.last?.id)
        #expect(open?.earlier.isEmpty == true)
        #expect(open?.newest.text == "回顾")
        #expect(ChatLines.openRecap(one, pending: nil) == nil, "closed: the one-line marker")

        let three = ChatLines(openWork: []).build(blocks([item(1, .text, role: "user", text: "看一下"), item(2, .text, text: "好了"),
                                                         item(3, .recap, text: "一"), item(4, .recap, text: "二"), item(5, .recap, text: "三")]), live: nil)
        guard case .recaps(_, let items)? = three.last?.content else {
            Issue.record("no merged recaps: \(three.map(\.id))")
            return
        }
        let folded = ChatLines.openRecap(three, pending: items.last)
        #expect(folded?.line == three.last?.id)
        #expect(folded?.earlier.map(\.text) == ["一", "二"])
        #expect(folded?.newest.text == "三")
        #expect(ChatLines.openRecap(three, pending: items.first) == nil, "only the newest one opens")

        // Something after it: no longer the newest thing, it is not open.
        let after = ChatLines(openWork: []).build(blocks([item(1, .recap, text: "回顾"), item(2, .text, role: "user", text: "在吗"),
                                                         item(3, .text, text: "在")]), live: nil)
        let earlier = after.first.flatMap { if case .row(.recap(let i)) = $0.content { i } else { nil } }
        #expect(earlier != nil)
        #expect(ChatLines.openRecap(after, pending: earlier) == nil)
    }

    /// A picture sent with a message to Persona: claude -p writes the message's line with the picture first and the
    /// text after it (an image block, then the text), where a pasted one in an Agents chat comes after the text. The
    /// picture belongs to that message, above its bubble, as it showed while it was being sent: it went to the
    /// exchange before (folded into its work, out of sight).
    @Test func aPictureSentBeforeItsTextStaysWithItsMessage() {
        var photo = item(3, .image, role: "user", id: nil)
        photo.image = ImagePayload(mime: "image/png", data: "iVBORw0KGgo=")
        var text = item(3, .text, role: "user", text: "看看这张")
        text.id = "3#1"
        let items = [item(1, .text, role: "user", text: "早"), item(2, .text, text: "早上好"), photo, text, item(4, .text, text: "是一张截图")]
        let lines = ChatLines(openWork: []).build(blocks(items), live: nil)
        #expect(kinds(lines) == ["user", "answer", "other", "user", "answer"], "\(lines.map(\.id))")
        guard case .userImages(_, let shown)? = lines.first(where: { if case .userImages = $0.content { true } else { false } })?.content else {
            Issue.record("no picture line: \(lines.map(\.id))")
            return
        }
        #expect(shown.map(\.id) == ["3#0"])
    }

    /// The same picture after its text (a pasted image in an Agents chat) shows as it did.
    @Test func aPictureAfterItsTextStaysWithItsMessage() {
        var photo = item(3, .image, role: "user", id: nil)
        photo.image = ImagePayload(mime: "image/png", data: "iVBORw0KGgo=")
        photo.id = "3#1"
        let items = [item(1, .text, role: "user", text: "早"), item(2, .text, text: "早上好"),
                     item(3, .text, role: "user", text: "看看这张"), photo, item(4, .text, text: "是一张截图")]
        #expect(kinds(ChatLines(openWork: []).build(blocks(items), live: nil)) == ["user", "answer", "other", "user", "answer"])
    }

    /// A picture sent to Persona with no text: its line holds the picture alone. It is a message of its own, shown as
    /// a sent picture, not the end of the exchange before it.
    @Test func aPictureSentAloneIsAMessageOfItsOwn() {
        var photo = item(3, .image, role: "user", id: nil)
        photo.image = ImagePayload(mime: "image/png", data: "iVBORw0KGgo=")
        let items = [item(1, .text, role: "user", text: "早"), item(2, .text, text: "早上好"), photo, item(4, .text, text: "是一张截图")]
        let lines = ChatLines(openWork: []).build(blocks(items), live: nil)
        #expect(kinds(lines) == ["user", "answer", "other", "answer"], "\(lines.map(\.id))")
    }

    // MARK: Compactions (a divider, `Item.compact`)

    private func notice(_ seq: Int, _ text: String) -> Item { item(seq, .notice, role: "system", text: text) }

    private func compactRows(_ lines: [ChatLine]) -> [Item] {
        lines.compactMap { if case .row(.notice(let i)) = $0.content, i.compact != nil { i } else { nil } }
    }

    /// Compacted while idle, after a finished turn: one divider, no work line of notices to open (the user's screenshot:
    /// 「上下文已压缩」「已从压缩的上下文继续」 and the idle note in a 「收起 3 步」 work line).
    @Test func anIdleCompactionIsOneDivider() {
        let items = [item(1, .text, role: "user", text: "看一下"), item(2, .text, text: "看完了"),
                     notice(3, ChatStore.compactedText), notice(4, ChatStore.continuedText),
                     notice(5, "Compacted while idle, before the prompt cache expired · turn off with \"idleCompaction\": false in settings.json")]
        let lines = ChatLines(openWork: []).build(blocks(items), live: nil)
        #expect(kinds(lines) == ["user", "answer", "row"], "\(lines.map(\.id))")
        let c = compactRows(lines)
        #expect(c.count == 1)
        #expect(c.first?.compact?.trigger == "idle")
        #expect(c.first?.compact?.source == "空闲时")
        #expect(c.first?.compact?.notes?.count == 3)
    }

    /// /compact: the hub's trigger kept, the summary folded in, a divider after the command.
    @Test func aManualCompactionIsADividerAfterTheCommand() {
        var boundary = notice(2, ChatStore.compactedText)
        boundary.compact = Compaction(trigger: "manual", preTokens: 151_467)
        let items = [item(1, .text, role: "user", text: "/compact"), boundary, notice(3, ChatStore.continuedText)]
        let lines = ChatLines(openWork: []).build(blocks(items), live: nil)
        #expect(kinds(lines) == ["user", "row"], "\(lines.map(\.id))")
        #expect(compactRows(lines).first?.compact?.source == "手动")
        #expect(compactRows(lines).first?.compact?.notes == [ChatStore.compactedText, ChatStore.continuedText])
    }

    /// Compacted in the middle of work: it stays in the work line, as one step (not two notices).
    @Test func aCompactionMidTurnIsOneStepOfItsWork() {
        let items = [item(1, .text, role: "user", text: "做"), item(2, .toolUse, tool: "Bash", id: "a"), item(3, .toolResult, role: "user", text: "ok", id: "a"),
                     notice(4, ChatStore.compactedText), notice(5, ChatStore.continuedText),
                     item(6, .toolUse, tool: "Read", id: "b"), item(7, .toolResult, role: "user", text: "ok", id: "b"), item(8, .text, text: "好了")]
        let lines = ChatLines(openWork: []).build(blocks(items), live: nil)
        #expect(kinds(lines) == ["user", "work", "answer"])
        guard case .work(_, let work, _, _, _)? = lines.first(where: { if case .work = $0.content { true } else { false } })?.content else {
            Issue.record("no work line")
            return
        }
        #expect(work.filter(ChatLines.isCompaction).count == 1)
    }

    /// The summary without its boundary (on an older page) is a divider by itself; Codex's lone notice too.
    @Test func aSummaryAloneOrACodexNoticeIsADivider() {
        let alone = ChatLines(openWork: []).build(blocks([notice(1, ChatStore.continuedText), item(2, .text, role: "user", text: "继续")]), live: nil)
        #expect(compactRows(alone).count == 1)
        let codex = ChatLines(openWork: []).build(blocks([item(1, .text, role: "user", text: "a"), item(2, .text, text: "b"), notice(3, ChatStore.compactedText)]), live: nil)
        #expect(compactRows(codex).count == 1)
    }

    /// The same after a turn that did work: the divider comes after the answer, not hidden in its folded work line
    /// (the Persona chat showed nothing of the compaction).
    @Test func anIdleCompactionAfterWorkIsAfterTheAnswer() {
        let items = [item(1, .text, role: "user", text: "看一下"), item(2, .toolUse, tool: "Bash", id: "a"), item(3, .toolResult, role: "user", text: "ok", id: "a"),
                     item(4, .text, text: "看完了"), notice(5, ChatStore.compactedText), notice(6, ChatStore.continuedText)]
        let lines = ChatLines(openWork: []).build(blocks(items), live: nil)
        #expect(kinds(lines) == ["user", "work", "answer", "row"], "\(lines.map(\.id))")
        guard case .work(_, let work, _, _, _)? = lines.first(where: { if case .work = $0.content { true } else { false } })?.content else { return }
        #expect(!work.contains(where: ChatLines.isCompaction))
    }

    @Test(arguments: [false, true])
    func compactionAfterLatestNarrationStillFoldsWhenToolsFollowIt(active: Bool) {
        var boundary = notice(5, ChatStore.compactedText)
        boundary.compact = Compaction(trigger: "auto", preTokens: 167_000)
        var failed = item(7, .toolResult, role: "user", text: "Synthetic failure", id: "b")
        failed.isError = true
        let items = [item(1, .text, role: "user", text: "Check the synthetic change"),
                     item(2, .toolUse, tool: "Read", id: "a"), item(3, .toolResult, role: "user", text: "ok", id: "a"),
                     item(4, .text, text: "Latest narration"), boundary,
                     item(6, .toolUse, tool: "Bash", id: "b"), failed]
        let b = blocks(items)
        var renderer = ChatLines(openWork: [])
        let live = active ? "1#0" : nil
        let closed = renderer.build(b, live: live)
        #expect(kinds(closed) == ["user", "work", "answer"])
        #expect(compactRows(closed).isEmpty)
        #expect(closed.last?.holds("4#0") == true)
        guard case .work(let id, let work, _, _, _) = closed[1].content else { Issue.record("Missing work summary"); return }
        #expect(work.filter(ChatLines.isCompaction).count == 1)
        renderer.openWork = [id]
        #expect(kinds(renderer.build(b, live: live)) == ["user", "work", "step", "step", "step", "end", "answer"])
    }

    /// A message that failed to send sits before the first message sent after it, not under everything (#6023); one with
    /// nothing after it, one without a time and one still on its way stay at the end, in the order they were written.
    @Test func aFailedMessageSitsWhereItWasWritten() {
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        func at(_ seq: Int, _ kind: ItemKind, role: String, text: String, _ s: TimeInterval, client: String? = nil) -> Item {
            var i = item(seq, kind, role: role, text: text)
            i.ts = t0.addingTimeInterval(s)
            i.clientMsgID = client
            return i
        }
        let lines = ChatLines(openWork: []).build(blocks([
            at(1, .text, role: "user", text: "first", 0), at(2, .text, role: "assistant", text: "done", 1),
            at(3, .text, role: "user", text: "second", 300), at(4, .text, role: "assistant", text: "done again", 301),
        ]), live: nil)
        let failed = PendingMessage(text: "lost", images: [], state: .failed, createdAt: t0.addingTimeInterval(50))
        let newest = PendingMessage(text: "last", images: [], state: .failed, createdAt: t0.addingTimeInterval(900))
        let untimed = PendingMessage(text: "old outbox", images: [], state: .failed, createdAt: nil)
        let waiting = PendingMessage(text: "offline", images: [], state: .waiting, createdAt: t0.addingTimeInterval(60))
        let placed = ChatLines.placeFailed(lines, [newest, failed, waiting, untimed])
        #expect(placed.before == ["3#0": [failed]])
        #expect(placed.rest == [untimed, waiting, newest]) // the order they were written, an untimed one first
        // Nothing failed: everything stays at the end.
        #expect(ChatLines.placeFailed(lines, [waiting]).rest == [waiting])
        // Two failed before the same message, oldest first.
        let earlier = PendingMessage(text: "lost too", images: [], state: .failed, createdAt: t0.addingTimeInterval(40))
        #expect(ChatLines.placeFailed(lines, [failed, earlier]).before == ["3#0": [earlier, failed]])
        // A message typed on the computer within two minutes after it (the clocks may differ): it stays below.
        let close = PendingMessage(text: "close", images: [], state: .failed, createdAt: t0.addingTimeInterval(250))
        #expect(ChatLines.placeFailed(lines, [close]).rest == [close])
    }

    /// While the list is frozen (the user reads history) the unsent messages stay where they were drawn when the freeze
    /// began (#1243): a failed one before its line, not at the newest end where every one used to go (it showed up under
    /// the newest messages while the list was pulled, and went back on release). One resent from there (a new id) stays
    /// there; one that failed during the freeze, or whose line is not drawn now, waits at the end.
    @Test func aFrozenListKeepsFailedMessagesInPlace() {
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        func at(_ seq: Int, role: String, text: String, _ s: TimeInterval) -> Item {
            var i = item(seq, .text, role: role, text: text)
            i.ts = t0.addingTimeInterval(s)
            return i
        }
        let lines = ChatLines(openWork: []).build(blocks([
            at(1, role: "user", text: "first", 0), at(2, role: "assistant", text: "done", 1),
            at(3, role: "user", text: "second", 300), at(4, role: "assistant", text: "done again", 301),
        ]), live: nil)
        let failed = PendingMessage(text: "lost", images: [], state: .failed, createdAt: t0.addingTimeInterval(50))
        let sending = PendingMessage(text: "on its way", images: [], state: .sending, createdAt: t0.addingTimeInterval(900))
        // As the freeze began: the failed one before the next message, the other at the end.
        let at = ChatLines.failedLines(ChatLines.placeFailed(lines, [failed, sending]).before)
        #expect(at == [ChatLines.frozenKey(failed): "3#0"])
        let frozen = ChatLines.placeFrozen(lines, [sending, failed], at: at)
        #expect(frozen.before == ["3#0": [failed]])
        #expect(frozen.rest == [sending])
        // Resent from where it sat: a new id, the same place.
        var resent = failed
        resent.id = UUID()
        resent.state = .sending
        #expect(ChatLines.placeFrozen(lines, [resent], at: at).before == ["3#0": [resent]])
        // The other one failed while the list was frozen: it waits where it was drawn, at the end.
        var nowFailed = sending
        nowFailed.state = .failed
        #expect(ChatLines.placeFrozen(lines, [failed, nowFailed], at: at).rest == [nowFailed])
        // Its line is not drawn now (a page that went away): at the end rather than nowhere.
        #expect(ChatLines.placeFrozen(Array(lines.prefix(2)), [failed], at: at).rest == [failed])
        // Nothing failed when it froze: all at the end, in the order they were written.
        #expect(ChatLines.placeFrozen(lines, [sending, failed], at: [:]).rest == [failed, sending])
    }

    /// A message this phone sent before the failed one but the agent took in later (queued): its copy is written then,
    /// yet it was written first, and the phone's own time says so.
    @Test func aQueuedMessageSentEarlierStaysAbove() {
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        let queued = UUID()
        var b = item(3, .text, role: "user", text: "sent first")
        b.ts = t0.addingTimeInterval(400) // the agent took it in minutes later
        b.clientMsgID = queued.uuidString.lowercased()
        var a = item(1, .text, role: "user", text: "before")
        a.ts = t0
        let lines = ChatLines(openWork: []).build(blocks([a, b]), live: nil)
        let failed = PendingMessage(text: "lost", images: [], state: .failed, createdAt: t0.addingTimeInterval(50))
        #expect(ChatLines.placeFailed(lines, [failed]).before == ["3#0": [failed]]) // without the phone's time
        let placed = ChatLines.placeFailed(lines, [failed], writtenAt: [queued.uuidString: t0.addingTimeInterval(45)])
        #expect(placed.before.isEmpty && placed.rest == [failed])
    }

    /// The next day's header and a later feed card stop it too; photos the user sent count as a message.
    @Test func aFailedMessageStopsAtTheNextDayAndPhotos() {
        let day1 = Date(timeIntervalSince1970: 1_800_000_000)
        var u1 = item(1, .text, role: "user", text: "evening")
        u1.ts = day1
        var photo = item(5, .image, role: "user")
        photo.ts = day1.addingTimeInterval(86400 * 2)
        let day2 = Calendar.current.startOfDay(for: day1.addingTimeInterval(86400))
        let lines = [ChatLine.row(.user(u1)), ChatLine.day(day2), ChatLine.userImages(id: "imgs", items: [photo])]
        let late = PendingMessage(text: "late", images: [], state: .failed, createdAt: day1.addingTimeInterval(60))
        #expect(ChatLines.placeFailed(lines, [late]).before == [lines[1].id: [late]])
        let next = PendingMessage(text: "next day", images: [], state: .failed, createdAt: day2.addingTimeInterval(3600))
        #expect(ChatLines.placeFailed(lines, [next]).before == ["imgs": [next]])
    }
}
