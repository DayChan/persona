import Foundation
import Testing
@testable import Persona

/// The assistant's words written before a tool call (#1966): Claude Code streams them as text and records them as a
/// thinking block. Their live copy goes once that thinking line is in, also when the clear came while the chat was not
/// listening (a dropped subscription, a reconnect that resumed): a subscription's backlog clears what the hub does not
/// send again. Before, the words stayed at the end of the chat with their cursor, under the answer and into the next turn.
@MainActor
struct LiveTextNarrationTests {
    private let words = "我先找一下负责的 Agent，再把任务交给它。"

    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, id: String? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: id, toolName: id == nil ? nil : "Bash",
             toolInput: nil, isError: nil, image: nil)
    }

    /// The turn as the transcript has it: the message, the words as a thinking line, `tools` calls, the answer.
    private func turn(tools: Int) -> [Item] {
        var out = [item(1, .text, role: "user", text: "设计一个小组件"), item(2, .thinking, text: words)]
        for k in 0..<tools {
            out.append(item(3 + 2 * k, .toolUse, id: "t\(k)"))
            out.append(item(4 + 2 * k, .toolResult, role: "user", text: "ok", id: "t\(k)"))
        }
        out.append(item(3 + 2 * tools, .text, text: "已交给负责的 Agent。"))
        return out
    }

    private func settle() async { try? await Task.sleep(for: .milliseconds(300)) } // a rebuild

    @Test func aThinkingLineIsTheWordsLandedCopy() {
        #expect(ChatStore.isLiveCopy(item(2, .thinking, text: words), of: words))
        #expect(ChatStore.isLiveCopy(item(2, .thinking, text: words), of: String(words.prefix(6)))) // the stream ended early
        #expect(ChatStore.isLiveCopy(item(2, .text, text: words), of: words))
        #expect(!ChatStore.isLiveCopy(item(2, .thinking, role: "user", text: words), of: words))
        #expect(!ChatStore.isLiveCopy(item(2, .toolResult, text: words), of: words))
        #expect(!ChatStore.isLiveCopy(item(2, .text, text: "别的话"), of: words))
        #expect(!ChatStore.isLiveCopy(item(2, .thinking, text: words), of: ""))
        // Written before the live text began: an earlier line that starts the same way.
        #expect(!ChatStore.isLiveCopy(item(2, .thinking, text: "我先看看日志。"), of: "我", after: 2))
        #expect(ChatStore.isLiveCopy(item(3, .thinking, text: "我先看看日志。"), of: "我", after: 2))
    }

    /// A reply that begins like an earlier line of its turn ("我…") keeps streaming: that line is not its copy.
    @Test func aShortStartIsNotTakenForAnEarlierLine() async {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        store.merge([item(1, .text, role: "user", text: "看看"), item(2, .thinking, text: "我先看看日志。")])
        store.showLive("我")
        #expect(!store.landed([item(2, .thinking, text: "我先看看日志。")], pending: nil))
        store.clearLive(newest: nil) // the turn's other message began: its fallback, not at once
        await settle() // taken for its copy, it would go when the rows are published
        #expect(store.liveText == "我")
    }

    @Test func itsLineIsFoundBackToTheTurnsStart() {
        // Fifteen tool calls put the words thirty lines before the answer (the last twelve only had the answer).
        #expect(ChatStore.holdsLive(turn(tools: 15), words))
        // Not past the message that began the turn: the words of an earlier turn are not this one's.
        let earlier = turn(tools: 1) + [item(40, .text, role: "user", text: "下一个问题")]
        #expect(!ChatStore.holdsLive(earlier, words))
        // A message queued while it works does not begin a turn.
        var queued = item(40, .text, role: "user", text: "补充一句")
        queued.queued = true
        #expect(ChatStore.holdsLive(turn(tools: 1) + [queued], words))
    }

    /// The clear comes while the chat listens: the words go as soon as their line is in, not after the 5 s fallback.
    @Test func theWordsGoWhenTheClearComesAndTheirLineIsIn() async {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        store.merge(Array(turn(tools: 15).prefix(1)))
        store.showLive(words) // streams, then its line is written with the tool call
        store.merge(Array(turn(tools: 15).dropFirst(1)))
        store.clearLive(newest: nil)
        await settle()
        #expect(store.liveText.isEmpty)
    }

    /// The clear was missed (the chat was not listening): the subscription's backlog brings the turn, and nothing else
    /// comes. The words go.
    @Test func aMissedClearIsMadeUpByTheBacklog() async {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        store.merge(Array(turn(tools: 15).prefix(1)))
        store.showLive(words)
        // Its line came while the chat listened, but was not taken for the words (an older app); the clear was missed.
        store.merge(Array(turn(tools: 15).dropFirst(1)))
        store.backlogArrived()
        await settle()
        #expect(store.liveText.isEmpty)
    }

    /// A message still being generated: the hub sends its text right after the backlog, and it stays, also when it is
    /// the same text again (the fallback does nothing once something set it).
    @Test func aLiveMessageSentAgainAfterTheBacklogStays() async {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        store.merge([item(1, .text, role: "user", text: "写一段长的")])
        store.showLive("正在写的回答")
        store.backlogArrived()
        store.showLive("正在写的回答")
        try? await Task.sleep(for: .seconds(6))
        #expect(store.liveText == "正在写的回答")
    }

    /// Nothing comes after the backlog and the words' line is not in the chat: they go after the fallback.
    @Test func wordsWithoutTheirLineGoAfterTheFallback() async {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        store.merge([item(1, .text, role: "user", text: "设计一个小组件")])
        store.showLive(words)
        store.backlogArrived()
        await settle()
        #expect(store.liveText == words) // not at once: the hub may still send it
        try? await Task.sleep(for: .seconds(6))
        #expect(store.liveText.isEmpty)
    }

    /// The text comes again while its line is in already: sent again after a backlog that brought its line (the tool had
    /// finished, the next message not begun), or a last delta after its copy landed. It is not drawn: a copy drawn then
    /// began after its line, which never ended it.
    @Test func aTextWhoseLineIsInIsNotDrawnAgain() async {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        store.merge(Array(turn(tools: 2).dropLast())) // the words' line and the calls; the answer not yet
        store.streamed(words, agents: false)
        #expect(store.liveText.isEmpty)
        // The next message streams: it is drawn.
        store.streamed("已交给", agents: false)
        #expect(store.liveText == "已交给")
        // Its line lands, then its last delta comes after it.
        let answer = turn(tools: 2).last!
        store.merge([answer])
        #expect(store.landed([answer], pending: nil))
        await settle()
        #expect(store.liveText.isEmpty)
        store.streamed(answer.text!, agents: false)
        #expect(store.liveText.isEmpty)
        // A text of the next turn is drawn, though the last turn's answer starts the same way.
        store.merge([item(20, .text, role: "user", text: "下一个问题")])
        store.streamed("已交给", agents: false)
        #expect(store.liveText == "已交给")
    }

    /// The transcript file was replaced (its seqs started over): the live text's line is found in the new one.
    @Test func anotherHistoryFindsTheLineInTheNewFile() async {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        let old = [item(1, .text, role: "user", text: "旧的")] + (2...30).map { item($0, .text, text: "第 \($0) 行") }
        store.apply(old, span: ItemSpan(history: "h1", from: nil, to: 30))
        store.showLive(words) // began after line 30 of the old file
        store.apply([item(1, .text, role: "user", text: "设计一个小组件"), item(2, .thinking, text: words)],
                    span: ItemSpan(history: "h2", from: nil, to: 2))
        store.clearLive(newest: nil)
        await settle()
        #expect(store.liveText.isEmpty)
    }
}
