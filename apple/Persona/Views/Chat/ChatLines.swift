import Foundation
import PersonaKit

/// The chat as a flat list of lines (`ChatLine`), from the store's blocks: every message of a turn opens its own
/// exchange — the message, one work line for what the agent did after it, the agent's last words. Open work lines get
/// their steps as lines. A plain function of the blocks and what is open, so `ChatView` memoizes it by
/// `ChatStore.version` (`ChatScratch.LinesKey`) and `ChatLinesTests` checks it without a view.
struct ChatLines {
    enum Placeholder: Equatable { case loading, empty, none }

    /// Feed cards and messages still arriving are content too, even before transcript history has loaded.
    static func placeholder(lines: [ChatLine], hasTranscript: Bool, hasPending: Bool,
                            hasLiveText: Bool, loadedOnce: Bool) -> Placeholder {
        guard lines.isEmpty, !hasTranscript, !hasPending, !hasLiveText else { return .none }
        return loadedOnce ? .empty : .loading
    }

    /// Work lines the user flipped from how they start (folded, or open while running with `opensLive`); `openAll`
    /// flips them all (DEBUG `-openAllWork`).
    var openWork: Set<String>
    var openAll = false
    /// The running work line starts open, so its tool calls list one by one as they come; once the turn ends it is
    /// folded again (the chat forgets the user's flip of it then) and opens to all of them (#1614, the assistant).
    var opensLive = false
    /// Runs of tool calls the user opened; `openAllRuns` flips them all (DEBUG `-showRunsOpen`).
    var openRuns: Set<String> = []
    var openAllRuns = false
    /// BeautifulUI folds two or more tool calls in a row into a line of chips.
    var chipRuns = false
    /// Whether a call can join a run: a call that went to the background stays on its own line.
    var foldsIntoRun: (ToolCall) -> Bool = { _ in true }
    /// Whether a call stays in view while its work line is folded (a running subagent's card); `live`: the line runs.
    var showsFolded: (ToolCall, _ live: Bool) -> Bool = { _, _ in false }

    /// The lines of these blocks with the open work lines unfolded.
    func build(_ blocks: [ChatBlock], live: String?, streaming: Bool = false) -> [ChatLine] {
        unfold(lines(blocks, live: live, streaming: streaming))
    }

    /// The lines of these blocks, before open work lines are unfolded. `streaming`: the running turn's next words are
    /// streaming in (drawn after the lines): the words before them are in its work line already, where they go once
    /// those are written. Out of it until then, they stayed under the new words as these streamed in, and then folded
    /// away: every sentence of a running turn moved the chat up two lines and down three (2026-11-04).
    func lines(_ blocks: [ChatBlock], live liveID: String?, streaming: Bool = false) -> [ChatLine] {
        var out: [ChatLine] = []
        for block in blocks {
            switch block {
            case .row(let r):
                out.append(.row(r))
            case .turn(var t):
                // Work a background task woke the agent for opens with that notice, where a message would be.
                var wokeAt: Date?
                if let first = t.ordered.first, case .notice(let n) = first, n.wake == true {
                    out.append(.row(first))
                    t.ordered.removeFirst()
                    wokeAt = n.ts
                }
                let segments = Self.segments(t.ordered)
                for (k, seg) in segments.enumerated() {
                    // Once the answer is in and a background task woke the agent, the new work runs behind the chip.
                    let live = t.id == liveID && k == segments.count - 1 && t.tail.isEmpty
                    // Photos sent with the message (Claude inlines a pasted image path as an image block) sit above
                    // its text, as they did while it was being sent — not in the agent's work.
                    let photos = Self.photos(seg.rows)
                    if !photos.isEmpty { out.append(.userImages(id: "img-" + photos[0].id, items: photos)) }
                    if let u = seg.user { out.append(.row(u)) }
                    let last = live && streaming ? nil : seg.rows.lastIndex { if case .assistant = $0 { true } else { false } }
                    // A compaction after the answer (Claude Code compacted once the turn was over: idle, /compact on
                    // its own) is a divider after it, not a step hidden in the turn's work line.
                    let after = Set(seg.rows.indices.filter { n in
                        guard let l = last, n > l, case .notice(let i) = seg.rows[n] else { return false }
                        return i.compact != nil
                    })
                    let work = seg.rows.enumerated().filter { $0.offset != last && !after.contains($0.offset) }.map(\.element)
                    let items = Self.workItems(work)
                    if !live, !items.isEmpty, items.allSatisfy(Self.isCompaction) {
                        // Only compactions (/compact): dividers across the chat, no work line to open.
                        out += work.filter { if case .notice(let i) = $0 { i.compact != nil } else { false } }.map { .row($0) }
                    } else if !items.isEmpty || live {
                        let id = "w-" + (seg.user?.id ?? seg.rows.first?.id ?? t.id)
                        out.append(.work(id: id, items: items, live: live,
                                         start: seg.user?.firstTS ?? (k == 0 ? wokeAt : nil) ?? seg.rows.first?.firstTS ?? t.startedAt,
                                         end: work.last?.lastTS ?? seg.rows.last?.lastTS))
                    }
                    // Between the work and the answer: what the work looked at. Its id stays as pictures come in and
                    // the answer changes, so the strip grows sideways in place.
                    out += Self.viewed("vi-" + (seg.user?.id ?? seg.rows.first?.id ?? t.id), items)
                    if let last { out.append(.row(seg.rows[last])) }
                    out += after.sorted().map { .row(seg.rows[$0]) }
                }
                // After the answer: one small line for the background tasks; what they led to opens in a drawer.
                if !t.wakes.isEmpty { out.append(.wakes(t, live: t.id == liveID && !t.tail.isEmpty)) }
                out += t.trailing.map { .row($0) }
            }
        }
        return Self.foldRecaps(Self.foldAPIErrors(out))
    }

    /// Claude Code writes a recap each time an idle session is looked at again: several in a row (nothing else
    /// between) are one line, like the background tasks' reports (2026-10-31: three While You Were Away lines stacked up).
    /// The line keeps the first recap's id, so it stays put as more arrive.
    static func foldRecaps(_ lines: [ChatLine]) -> [ChatLine] {
        var out: [ChatLine] = []
        out.reserveCapacity(lines.count)
        var run: [Item] = []
        func flush() {
            if run.count == 1 { out.append(.row(.recap(run[0]))) }
            if run.count > 1 { out.append(.recaps(id: "rc-" + run[0].id, items: run)) }
            run = []
        }
        for line in lines {
            if case .row(.recap(let i)) = line.content { run.append(i); continue }
            flush()
            out.append(line)
        }
        flush()
        return out
    }

    /// The newest recap shown open, in full, as the chat's last line (#1360): in the flow with the agent's words, so it
    /// scrolls with them and never covers them. Only while it is the last line and not closed (`pending`); the earlier
    /// recaps folded into the same line stay a one-line chip above it.
    struct OpenRecap: Equatable {
        /// The line drawn open (the folded line's id, or the single recap's).
        let line: String
        /// The recaps before it in the same line, oldest first; empty for a single recap.
        let earlier: [Item]
        let newest: Item
    }

    static func openRecap(_ lines: [ChatLine], pending: Item?) -> OpenRecap? {
        guard let pending, let last = lines.last else { return nil }
        switch last.content {
        case .row(.recap(let i)) where i.id == pending.id:
            return OpenRecap(line: last.id, earlier: [], newest: i)
        case .recaps(_, let items) where items.last?.id == pending.id:
            return OpenRecap(line: last.id, earlier: Array(items.dropLast()), newest: pending)
        default:
            return nil
        }
    }

    /// Claude Code's "API Error: …" answers and the hub's API error notices become error cards; the same error
    /// again right after, with nothing but a retry ("Continue") between, folds into the first with a count.
    static func foldAPIErrors(_ lines: [ChatLine]) -> [ChatLine] {
        var out: [ChatLine] = []
        out.reserveCapacity(lines.count)
        for line in lines {
            guard case .row(let r) = line.content, let item = r.errorCandidate, let info = APIErrorInfo.parse(item.text) else {
                out.append(line)
                continue
            }
            if let last = out.lastIndex(where: { if case .apiError = $0.content { true } else { false } }),
               case .apiError(let first, let prev, let n) = out[last].content, prev.signature == info.signature,
               out[(last + 1)...].allSatisfy(isRetry) {
                out.removeSubrange((last + 1)...)
                out[last] = .apiError(item: first, info: info, count: n + 1)
                continue
            }
            out.append(.apiError(item: item, info: info, count: 1))
        }
        return out
    }

    static func isRetry(_ line: ChatLine) -> Bool {
        guard case .row(.user(let i)) = line.content else { return false }
        let t = (i.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return ["继续", "重试", "再试一次", "continue", "retry", "go on", "続けて", "続行", "再試行", "もう一度"].contains(t) // i18n-ignore
    }

    /// Open work lines get their steps as lines of their own and a Collapse line after the last.
    func unfold(_ lines: [ChatLine]) -> [ChatLine] {
        var out: [ChatLine] = []
        out.reserveCapacity(lines.count)
        for line in lines {
            out.append(line)
            if case .work(let id, let items, let live, _, _) = line.content, !isOpen(id, live: live) {
                // Subagents launched one after another stay out together; any other call that stays out (a listed
                // workflow run's) on its own.
                var run: [ToolCall] = []
                func flush() {
                    let shown = run.filter { showsFolded($0, live) }
                    if shown.count > 1 {
                        out.append(.agents(group: id, calls: shown, first: true, alone: true))
                    } else if let c = shown.first {
                        out.append(.agentOut(group: id, call: c))
                    }
                    run = []
                }
                for item in items {
                    if case .step(.call(let c)) = item, SubagentCard.isAgent(c) { run.append(c); continue }
                    flush()
                    if case .step(.call(let c)) = item, showsFolded(c, live) { out.append(.agentOut(group: id, call: c)) }
                }
                flush()
            }
            if case .work(let id, let items, let live, _, _) = line.content, isOpen(id, live: live), !items.isEmpty {
                // The BeautifulUI style folds runs of tool calls into a line of chips; the others list every step.
                out += Self.gridAgents(chipRuns ? runLines(group: id, items: items)
                    : items.enumerated().map { .workStep(group: id, item: $0.element, first: $0.offset == 0) })
                out.append(.workEnd(group: id, count: items.reduce(0) { n, item in
                    if case .step(.folded(let f)) = item { n + (f.fold.map { $0.tools + $0.thoughts } ?? 1) } else { n + 1 }
                }, files: chipRuns ? Self.changedFiles(items) : [])) // the BeautifulUI style shows them
            }
        }
        return out
    }

    /// Whether a work line is open; `live`: it is the running one. The wake drawer's work line ("dw-…") keeps folding
    /// while it runs, as it always has: only the chat's own running line opens by itself.
    func isOpen(_ work: String, live: Bool) -> Bool {
        openWork.contains(work) != (openAll || (opensLive && live && !work.hasPrefix(Self.drawerWork)))
    }
    /// The id prefix of the wake drawer's work line (`ChatView.wakeLines`).
    static let drawerWork = "dw-"

    /// The subagents a work line launched one after another (in parallel), each run in the order they were launched.
    static func agentRuns(_ items: [WorkItem]) -> [[ToolCall]] {
        var out: [[ToolCall]] = []
        var run: [ToolCall] = []
        for item in items {
            if case .step(.call(let c)) = item, SubagentCard.isAgent(c) { run.append(c); continue }
            if !run.isEmpty { out.append(run); run = [] }
        }
        if !run.isEmpty { out.append(run) }
        return out
    }

    /// An open work line's subagents launched one after another share one line, their cards in a grid under "Running
    /// 3 agents" (#5602, as the web); one alone stays a step of its own.
    static func gridAgents(_ steps: [ChatLine]) -> [ChatLine] {
        var out: [ChatLine] = []
        var run: [(group: String, call: ToolCall, first: Bool, line: ChatLine)] = []
        func flush() {
            if run.count > 1 {
                out.append(.agents(group: run[0].group, calls: run.map(\.call), first: run[0].first, alone: false))
            } else {
                out += run.map(\.line)
            }
            run = []
        }
        for line in steps {
            if case .workStep(let group, .step(.call(let c)), let first) = line.content, SubagentCard.isAgent(c) {
                run.append((group, c, first, line))
                continue
            }
            flush()
            out.append(line)
        }
        flush()
        return out
    }

    /// The files a work line's edits and writes changed, in the order first touched, with the lines added and removed
    /// (the counts its diffs show, `ToolDiffMemo`). Failed edits changed nothing and are left out.
    static func changedFiles(_ items: [WorkItem]) -> [ChangedFile] {
        var out: [ChangedFile] = []
        for item in items {
            guard case .step(.call(let c)) = item, !c.isError,
                  ["Edit", "MultiEdit", "Write", "NotebookEdit"].contains(ToolPresentation.baseName(c.name)),
                  let path = c.input["file_path"]?.string ?? c.input["notebook_path"]?.string, !path.isEmpty else { continue }
            let d = ToolRunSummary.diff(c)
            if let n = out.firstIndex(where: { $0.path == path }) {
                out[n].adds += d.adds
                out[n].dels += d.dels
            } else {
                out.append(ChangedFile(path: path, adds: d.adds, dels: d.dels))
            }
        }
        return out
    }

    /// An open work line's steps: two or more tool calls in a row fold into one line of chips (`ToolRunRow`); opened,
    /// their rows follow it, one lazy line per call as before. Thoughts, notices and words stay lines of their own, and
    /// so does a call that went to the background: its row says how the task went (Background · Done).
    func runLines(group: String, items: [WorkItem]) -> [ChatLine] {
        var out: [ChatLine] = []
        for piece in runs(items) {
            switch piece {
            case .item(let n):
                out.append(.workStep(group: group, item: items[n], first: n == 0))
            case .run(let calls, let start, let key):
                guard calls.count > 1 else {
                    out.append(.workStep(group: group, item: .step(.call(calls[0])), first: start == 0))
                    continue
                }
                let id = group + "/run-" + key
                let open = isRunOpen(id)
                out.append(.toolRun(id: id, calls: calls, open: open, first: start == 0))
                if open { out += calls.map { .workStep(group: group, item: .step(.call($0)), first: false) } }
            }
        }
        return out
    }

    /// A work line's steps cut into runs of calls that fold together and the steps between them.
    private enum RunPiece { case item(Int), run([ToolCall], start: Int, key: String) }

    /// Each run's key is its first call's id, except the first run of a stretch of calls, which takes the stretch's
    /// first call: a call that goes to the background leaves its run (its row says how the task went), and when it was
    /// the first one the run went on under a new id and an opened run closed (#5602). Now it keeps the id it had.
    private func runs(_ items: [WorkItem]) -> [RunPiece] {
        var out: [RunPiece] = []
        var run: [ToolCall] = []
        var runStart = 0
        var stretch: String? // the first call of the calls in a row so far, until a run takes it
        var inStretch = false
        func flush() {
            guard let first = run.first else { return }
            if run.count > 1 {
                out.append(.run(run, start: runStart, key: stretch ?? first.id))
                stretch = nil
            } else {
                out.append(.run(run, start: runStart, key: first.id))
            }
            run = []
        }
        for (n, item) in items.enumerated() {
            guard case .step(.call(let c)) = item else {
                flush()
                stretch = nil
                inStretch = false
                out.append(.item(n))
                continue
            }
            if !inStretch { stretch = c.id }
            inStretch = true
            // A subagent's card is a line of its own (or in a grid), a plan's line has its bar under it.
            if foldsIntoRun(c), !SubagentCard.isAgent(c), ToolPresentation.baseName(c.name) != "TodoWrite" {
                if run.isEmpty { runStart = n }
                run.append(c)
                continue
            }
            flush()
            out.append(.item(n))
        }
        flush()
        return out
    }

    func isRunOpen(_ id: String) -> Bool { openRuns.contains(id) != openAllRuns }

    /// The run line (`runLines`) a call is folded into, if it is.
    func runID(holding call: String, group: String, items: [WorkItem]) -> String? {
        for case .run(let calls, _, let key) in runs(items) where calls.count > 1 && calls.contains(where: { $0.id == call }) {
            return group + "/run-" + key
        }
        return nil
    }

    static func viewed(_ id: String, _ work: [WorkItem]) -> [ChatLine] {
        let images = ViewedImages.of(work)
        return images.isEmpty ? [] : [.viewed(id: id, images: images)]
    }

    static func photos(_ rows: [ChatRow]) -> [Item] {
        rows.compactMap { r -> Item? in
            if case .image(let i) = r, i.role == "user", i.toolUseID == nil, i.image != nil { return i }
            return nil
        }
    }

    /// A compaction (`Item.compact`), as a work line holds it.
    static func isCompaction(_ w: WorkItem) -> Bool {
        if case .step(.thought(let i)) = w, i.compact != nil { return true }
        return false
    }

    /// What a work line folds from these rows: tools, thoughts, notices and the agent's words.
    static func workItems(_ rows: [ChatRow]) -> [WorkItem] {
        var items: [WorkItem] = []
        for r in rows {
            switch r {
            case .thinking(let i):
                if !(i.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { items.append(.step(.thought(i))) }
            case .tools(let steps): items += steps.map { .step($0) }
            case .notice(let i): items.append(.step(.thought(i)))
            case .assistant(let i): items.append(.text(i))
            default: break
            }
        }
        return items
    }

    /// A turn cut at each message: (message, what came after it until the next one).
    static func segments(_ ordered: [ChatRow]) -> [(user: ChatRow?, rows: [ChatRow])] {
        var out: [(user: ChatRow?, rows: [ChatRow])] = []
        var user: ChatRow?
        var rows: [ChatRow] = []
        var started = false
        for r in ordered {
            if case .user = r {
                if started { out.append((user, rows)) }
                user = r
                rows = []
            } else {
                rows.append(r)
            }
            started = true
        }
        if started { out.append((user, rows)) }
        return out
    }
}

/// A file a work line changed: its path and the lines added and removed.
nonisolated struct ChangedFile: Hashable, Sendable {
    var path: String
    var adds: Int
    var dels: Int
}

extension ChatLines {
    // MARK: The assistant's feed

    /// The lines with what the hub added to an assistant conversation (docs/assistant/unified-entry.md) among them: an item
    /// goes at the end of the exchange it happened in — before the first message sent after it — so a plan card or a
    /// receipt follows the answer of its turn instead of splitting it; what came after the last message goes last.
    static func mergeFeed(_ lines: [ChatLine], _ feed: [FeedItem]) -> [ChatLine] {
        guard !feed.isEmpty else { return lines }
        var out: [ChatLine] = []
        out.reserveCapacity(lines.count + feed.count)
        var next = 0
        for line in lines {
            if let at = inputTime(line) {
                while next < feed.count, feed[next].at < at {
                    out.append(.feed(feed[next]))
                    next += 1
                }
            }
            out.append(line)
        }
        while next < feed.count {
            out.append(.feed(feed[next]))
            next += 1
        }
        return out
    }

    /// Where messages that failed to send go (#6023): one written before a message sent after it goes right before the
    /// first such message (or the day, or the feed card, that came after it), at the end of the exchange it was written
    /// in, as a feed item does (`mergeFeed`). The chat then reads in the order things were written instead of keeping it
    /// under everything sent since. One with nothing after it, one without a time (an outbox from before `createdAt`),
    /// and every message still on its way stay at the end, in the order they were written. `before`: by the id of the
    /// line each goes before, in the order they were written. `writtenAt`: when this phone wrote the messages it sent
    /// (`ChatStore.writtenAt`); a line typed elsewhere goes by the computer's time, two minutes' slack for its clock, so
    /// a near tie leaves the failed message below.
    static func placeFailed(_ lines: [ChatLine], _ pending: [PendingMessage], writtenAt: [String: Date] = [:])
        -> (before: [String: [PendingMessage]], rest: [PendingMessage]) {
        let ordered = pending.enumerated().sorted { a, b in
            let x = a.element.createdAt ?? .distantPast, y = b.element.createdAt ?? .distantPast
            return x != y ? x < y : a.offset < b.offset
        }.map(\.element)
        guard ordered.contains(where: { $0.state == .failed }) else { return ([:], ordered) }
        let after = lines.compactMap { line in placeTime(line, writtenAt).map { (id: line.id, at: $0) } }
        var before: [String: [PendingMessage]] = [:]
        var rest: [PendingMessage] = []
        for msg in ordered {
            guard msg.state == .failed, let at = msg.createdAt, let next = after.first(where: { $0.at > at }) else {
                rest.append(msg)
                continue
            }
            before[next.id, default: []].append(msg)
        }
        return (before, rest)
    }

    /// The unsent messages while the chat is frozen (`ChatView.Frozen`: the user reads history), each where it was drawn
    /// when the freeze began, so nothing moves under the reader: a failed one before the line it sat before then (`at`,
    /// by `frozenKey`: `failedLines` of `placeFailed`), the others at the newest end, in the order they were written. One
    /// whose line is not drawn now, or that failed during the freeze, waits at the end until the freeze lifts. Every one
    /// went to the end before (#1243): an old failed message showed up under the newest ones while the list was pulled,
    /// and went back on release.
    static func placeFrozen(_ lines: [ChatLine], _ pending: [PendingMessage], at: [String: String])
        -> (before: [String: [PendingMessage]], rest: [PendingMessage]) {
        let ordered = pending.enumerated().sorted { a, b in
            let x = a.element.createdAt ?? .distantPast, y = b.element.createdAt ?? .distantPast
            return x != y ? x < y : a.offset < b.offset
        }.map(\.element)
        guard !at.isEmpty else { return ([:], ordered) }
        let drawn = Set(at.values).intersection(lines.map(\.id))
        var before: [String: [PendingMessage]] = [:]
        var rest: [PendingMessage] = []
        for msg in ordered {
            if let line = at[frozenKey(msg)], drawn.contains(line) {
                before[line, default: []].append(msg)
            } else {
                rest.append(msg)
            }
        }
        return (before, rest)
    }

    /// `placeFailed`'s `before` as `placeFrozen` takes it: the line each failed message goes before, by `frozenKey`.
    static func failedLines(_ before: [String: [PendingMessage]]) -> [String: String] {
        before.reduce(into: [:]) { out, entry in
            for msg in entry.value { out[frozenKey(msg)] = entry.key }
        }
    }

    /// What keeps an unsent message the same one through a freeze: when it was written and its words. Its id does not:
    /// a resend gives it a new one (`AppModel.retry`), and a message resent from where it sat must stay there.
    static func frozenKey(_ msg: PendingMessage) -> String {
        "\(msg.createdAt?.timeIntervalSince1970 ?? 0)|\(msg.text)"
    }

    /// When a line was written, for `placeFailed`: a message the user sent (this phone's time for one sent from it), a
    /// day's header, a feed card; nil for every other line.
    private static func placeTime(_ line: ChatLine, _ writtenAt: [String: Date]) -> Date? {
        func sent(_ i: Item) -> Date? {
            if let id = i.clientMsgID?.uppercased(), let at = writtenAt[id] { return at }
            return i.ts?.addingTimeInterval(-120)
        }
        switch line.content {
        case .row(.user(let i)): return sent(i)
        case .row(.image(let i)) where i.role == "user": return sent(i)
        case .userImages(_, let items): return items.first.flatMap(sent)
        case .day(let d): return d
        case .feed(let f): return f.at
        default: return nil
        }
    }

    /// When a line the user sent was sent; nil for every other line.
    private static func inputTime(_ line: ChatLine) -> Date? {
        switch line.content {
        case .row(.user(let i)): i.ts
        case .row(.image(let i)) where i.role == "user": i.ts
        case .userImages(_, let items): items.first?.ts
        default: nil
        }
    }
}
