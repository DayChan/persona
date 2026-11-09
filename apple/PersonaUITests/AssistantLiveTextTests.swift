import XCTest

/// The Persona assistant's words streamed before a tool call do not stay on the page (#1966). Claude Code streams them as
/// text and records them as a thinking block, so their transcript line is no text reply the live copy could land as.
/// When the app misses the clearing delta (its socket was down while the turn went on: deltas are not replayed when a
/// connection resumes), the old words stayed at the bottom with the streaming cursor, under the turn's answer and on into
/// the next turn. Mock: scripts/mock/mock_hub.py with MOCK_RESTART=1 (PERSONA_MOCK_HUB); `/mock/narrate` plays the turn.
final class AssistantLiveTextTests: XCTestCase {
    private var hub: String!
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    // Told apart from the same turn of an earlier run, which the app may still have cached.
    private static let run = String(UUID().uuidString.prefix(4))
    private let words = "我先找一下专门负责的 Agent，看看现有上下文，再把任务交给它（\(AssistantLiveTextTests.run)）。"
    private let answer = "已交给负责的 Agent，请它先出设计（\(AssistantLiveTextTests.run)）。"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_RESTART=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
        guard (try? json("restart"))?["stream"] != nil else { throw XCTSkip("needs MOCK_RESTART=1") }
    }

    /// The turn goes on while the app is away: the words' line, the answer and the clear come while it is not listening.
    /// The resumed subscription's backlog brings the line, and the words go at once, not after a fallback; the next turn
    /// starts without them.
    @MainActor func testNarrationGoesWithTheResumedBacklog() throws {
        continueAfterFailure = true // on to the next turn either way: its shot shows where the words stay
        let app = open()
        try mock("narrate", ["step": "start", "words": words])
        let live = text(app, words)
        XCTAssertTrue(live.waitForExistence(timeout: 10), "the narration did not stream in")
        sleep(2) // the last delta
        shot(app, "streaming")
        // The hub restarts (sockets closed, reconnections wait 2 s) and the turn ends meanwhile: nobody gets the clear.
        let before = connects().count
        try mock("restart", ["gap_ms": "2000"])
        let finish = try mock("narrate", ["step": "finish", "answer": answer])
        XCTAssertEqual(finish["cleared"] as? Int, 0, "the app still listened: the clear was not missed")
        XCTAssertEqual(reconnection(after: before)?["resumed"] as? Bool, true, "the app did not resume the hub's stream: \(connects())")
        XCTAssertTrue(text(app, answer).waitForExistence(timeout: 20), "the answer never came with the resumed subscription")
        let gone = live.waitForNonExistence(timeout: 3)
        shot(app, "answered")
        XCTAssertTrue(gone, "the narration stayed on the page after the answer came")
        // The next turn starts without the last turn's words.
        let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5), "no composer")
        field.tap()
        field.typeText("下一个问题")
        app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "shell-composer-send", "发送")).firstMatch.tap()
        XCTAssertTrue(text(app, "下一个问题").waitForExistence(timeout: 5), "the next message did not go")
        sleep(2)
        shot(app, "next-turn")
        XCTAssertFalse(live.exists, "the last turn's narration is still there in the next turn")
    }

    /// The turn was cut off: no line of the words ever comes, and the clear was missed. After the resumed backlog the
    /// words go anyway, once the hub did not send them again (the fallback).
    @MainActor func testNarrationWithoutItsLineGoesAfterTheResume() throws {
        let app = open()
        try mock("narrate", ["step": "start", "words": words])
        let live = text(app, words)
        XCTAssertTrue(live.waitForExistence(timeout: 10), "the narration did not stream in")
        sleep(2)
        let before = connects().count
        try mock("restart", ["gap_ms": "2000"])
        let finish = try mock("narrate", ["step": "finish", "answer": answer, "line": "0"])
        XCTAssertEqual(finish["cleared"] as? Int, 0, "the app still listened: the clear was not missed")
        // Resumed: a connection that started over drops the live text at once, and the fallback would go untested.
        XCTAssertEqual(reconnection(after: before)?["resumed"] as? Bool, true, "the app did not resume the hub's stream: \(connects())")
        XCTAssertTrue(text(app, answer).waitForExistence(timeout: 20), "the answer never came")
        XCTAssertTrue(live.waitForNonExistence(timeout: 8), "the narration stayed after the resume")
    }

    /// A reply still being generated across a resume: the hub sends its text again after the backlog, and it stays.
    @MainActor func testAReplyStreamingAcrossAResumeStays() throws {
        let app = open()
        try mock("narrate", ["step": "start", "words": words, "steps": "40", "ms": "300"]) // 12 s of streaming
        let first = text(app, String(words.prefix(6)))
        XCTAssertTrue(first.waitForExistence(timeout: 10), "the reply did not stream in")
        let before = connects().count
        try mock("restart", ["gap_ms": "1500"])
        XCTAssertEqual(reconnection(after: before)?["resumed"] as? Bool, true, "the app did not resume the hub's stream: \(connects())")
        sleep(7) // its backlog, the text sent again, and the 5 s fallback had it not been
        XCTAssertTrue(first.exists, "the reply being written went after the resume")
        try mock("narrate", ["step": "finish", "answer": answer])
    }

    /// With the clear received, the narration goes as soon as its thinking line is in, not only after the 5 s fallback. The
    /// turn's steps, opened, keep the words where they were said: before the tool call, over the answer.
    @MainActor func testNarrationGoesWhenItsThinkingLineIsIn() throws {
        let app = open()
        try mock("narrate", ["step": "start", "words": words])
        let live = text(app, words)
        XCTAssertTrue(live.waitForExistence(timeout: 10), "the narration did not stream in")
        sleep(2)
        try mock("narrate", ["step": "finish", "answer": answer])
        XCTAssertTrue(text(app, answer).waitForExistence(timeout: 10), "the answer did not come")
        let gone = live.waitForNonExistence(timeout: 3)
        shot(app, "cleared")
        XCTAssertTrue(gone, "the narration stayed after the clear")
        // The last turn's steps, opened.
        let heads = app.buttons.matching(identifier: "work-head").allElementsBoundByIndex.filter(\.isHittable)
        if let last = heads.max(by: { $0.frame.maxY < $1.frame.maxY }) {
            last.tap()
            sleep(1)
            shot(app, "steps")
        }
    }

    /// The chat is left while the turn ends (Agents in front: the hidden chat stops listening) and opened again: the clear
    /// went to nobody, and the words go with the backlog of the subscription made again.
    @MainActor func testNarrationGoesWhenTheChatIsOpenedAgain() throws {
        let app = open(["-probes", "YES"])
        try mock("narrate", ["step": "start", "words": words])
        let live = text(app, words)
        XCTAssertTrue(live.waitForExistence(timeout: 10), "the narration did not stream in")
        sleep(2)
        let finger = ShellFinger(app)
        finger.switchTo(agents: true, "Agents, the words still on the Assistant page")
        sleep(2) // the hidden chat unsubscribed
        let finish = try mock("narrate", ["step": "finish", "answer": answer])
        XCTAssertEqual(finish["cleared"] as? Int, 0, "the hidden chat still listened: the clear was not missed")
        finger.switchTo(agents: false, "back to the Assistant page")
        XCTAssertTrue(text(app, answer).waitForExistence(timeout: 10), "the answer did not come when the chat was opened again")
        let gone = live.waitForNonExistence(timeout: 3)
        shot(app, "reopened")
        XCTAssertTrue(gone, "the narration stayed after the chat was opened again")
    }

    @MainActor private func open(_ more: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // The shell (Assistant | Agents), where it was seen.
        app.launchArguments = ["-pairURL", pair, "-ui.oldLayout", "NO", "-dev.openChat", "", "-openChatsFresh", "YES",
                               "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main] + more
        app.launchPinned()
        LiveActivityShots.allowNotifications()
        XCTAssertTrue(app.buttons["assistant-sidebar-button"].firstMatch.waitForExistence(timeout: 30), "the main conversation did not open")
        sleep(3) // the backlog is in and the subscription is quiet
        return app
    }

    @discardableResult
    private func mock(_ path: String, _ query: [String: String]) throws -> [String: Any] {
        var c = URLComponents(string: hub + "/mock/" + path)!
        c.queryItems = query.map { URLQueryItem(name: $0.key, value: $0.value) }
        return (try JSONSerialization.jsonObject(with: Data(contentsOf: c.url!)) as? [String: Any]) ?? [:]
    }

    /// The app's first connection after the mock had `count` (the restart's gap holds it): whether it resumed the hub's
    /// stream. The answer can come over HTTP before it, so it is waited for.
    private func reconnection(after count: Int) -> [String: Any]? {
        let end = Date().addingTimeInterval(20)
        repeat {
            let all = connects()
            if all.count > count { return all[count] }
            usleep(250_000)
        } while Date() < end
        return nil
    }

    /// Every connection since the mock started: when, whether it asked to resume, whether it did, and from where.
    private func connects() -> [[String: Any]] {
        ((try? json("restart"))?["connects"] as? [[String: Any]]) ?? []
    }

    private func json(_ path: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/" + path)!)
        return (try JSONSerialization.jsonObject(with: data) as? [String: Any]) ?? [:]
    }

    @MainActor private func text(_ app: XCUIApplication, _ words: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("live-text-" + name + style + ".png"))
        }
    }
}
