import Foundation
import Testing
@testable import Persona

/// A reply a background task woke the assistant for (#3783: "it showed while it streamed, then was gone until the chat
/// was opened again"): whatever order its last events come in, it is on screen all along — its live copy while it
/// streams, then its line. The events are the hub's for the end of a turn: the last delta (the whole text), the
/// reply's item, the empty delta, the session going idle; between them, rebuilds and the delta pacer's timer may or may
/// not have run.
@MainActor
struct WakeReplyOrderTests {
    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, tool: String? = nil, id: String? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: id, toolName: tool, toolInput: nil, isError: nil, image: nil)
    }

    /// A chat as `AppModel` feeds it and `ChatView` draws it (the parts that decide what shows).
    @MainActor final class Chat {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        var pacer = DeltaPacer()
        var active = false

        /// AppModel's `.items`.
        func items(_ items: [Item]) {
            store.merge(items)
            if store.landed(items, pending: pacer.pendingText(store.key)) { pacer.drop(store.key) }
        }

        /// AppModel's `.delta`.
        func delta(_ text: String) {
            let key = store.key
            let newest = pacer.pendingText(key)
            switch pacer.offer(key, text, now: .now) {
            case .draw(let t): store.streamed(t, agents: false, newest: newest)
            case .later(let wait):
                Task { @MainActor [weak self] in
                    try? await Task.sleep(for: .seconds(wait))
                    guard let self, let t = self.pacer.due(key, now: .now) else { return }
                    self.store.streamed(t, agents: false)
                }
            case .kept: break
            }
        }

        /// ChatView's `liveShown` (`ChatLiveText.shows`).
        var liveShown: Bool {
            let live = store.liveText
            guard !live.isEmpty else { return false }
            if case .assistant(let i) = store.rows.last, let text = i.text { return !(text.hasPrefix(live) || live == text) }
            return true
        }

        /// How `id` shows: its line, its live copy, or not at all.
        func shows(_ id: String) -> String {
            var live: String?
            if active, case .turn(let t) = store.blocks.last { live = t.id }
            let lines = ChatLines(openWork: []).build(store.blocks, live: live, streaming: liveShown)
            if lines.contains(where: { $0.id == id }) { return "line" }
            return liveShown ? "live" : "none"
        }
    }

    private static func orders(_ a: [String]) -> [[String]] {
        guard a.count > 1 else { return [a] }
        return a.indices.flatMap { i -> [[String]] in
            var rest = a
            let x = rest.remove(at: i)
            return orders(rest).map { [x] + $0 }
        }
    }

    @Test func aWokenReplyStaysOnScreenWhateverOrderItsLastEventsComeIn() async {
        await stays(String(repeating: "新的回复。", count: 120))
    }

    /// Shorter than the answer before the wake (#3783): the longest words used to be the answer, so this reply went
    /// into the background tasks' drawer the moment its line came. The newest words are the answer now.
    @Test func aShortWokenReplyStaysOnScreenToo() async {
        await stays("构建好了。")
    }

    private func stays(_ reply: String) async {
        var wake = item(14, .notice, role: "system", text: "后台任务完成")
        wake.wake = true
        let before = [
            item(10, .text, role: "user", text: "批准了"), item(11, .toolUse, tool: "Bash", id: "a"),
            item(12, .toolResult, role: "user", text: "ok", id: "a"), item(13, .text, text: String(repeating: "旧的回复。", count: 50)),
        ]
        let woken = [wake, item(15, .toolUse, tool: "Bash", id: "b"), item(16, .toolResult, role: "user", text: "ok", id: "b")]
        let landed = item(17, .text, text: reply)

        // Every order × whether time passes after each of the first three (so 24 × 8 runs, side by side).
        let runs = Self.orders(["delta", "item", "clear", "idle"]).flatMap { order in (0..<8).map { (order, $0) } }
        let tasks = runs.map { order, waits in
            Task { @MainActor () -> String? in
                let chat = Chat()
                chat.items(before)
                try? await Task.sleep(for: .milliseconds(50))
                chat.items(woken)
                chat.active = true
                chat.delta(String(reply.prefix(80)))
                try? await Task.sleep(for: .milliseconds(50))
                chat.delta(String(reply.prefix(300))) // kept back by the pacer
                var seen: [String] = []
                for (k, event) in order.enumerated() {
                    switch event {
                    case "delta": chat.delta(reply)
                    case "item": chat.items([landed])
                    case "clear": chat.delta("")
                    default: chat.active = false
                    }
                    seen.append("\(event):\(chat.shows(landed.id))")
                    if k < 3, waits & (1 << k) != 0 {
                        try? await Task.sleep(for: .milliseconds(250)) // a rebuild and the pacer's timer
                        seen.append("…:\(chat.shows(landed.id))")
                    }
                }
                try? await Task.sleep(for: .milliseconds(300))
                seen.append("end:\(chat.shows(landed.id))")
                let ok = seen.last == "end:line" && !seen.contains { $0.hasSuffix(":none") }
                return ok ? nil : order.joined(separator: ",") + " waits=\(waits): " + seen.joined(separator: " ")
            }
        }
        var bad: [String] = []
        for t in tasks { if let r = await t.value { bad.append(r) } }
        #expect(bad.isEmpty, "\(bad.prefix(10).joined(separator: "\n"))")
    }
}
