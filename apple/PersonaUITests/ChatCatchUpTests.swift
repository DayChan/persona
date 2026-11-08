import XCTest

/// An open Agent chat still gets the new turn when its live items stop (scripts/mock/mock_hub.py, PERSONA_MOCK_HUB).
/// `/mock/say?drop=1` writes a message and its reply into the transcript and moves the session's `updated_at`, but never
/// pushes the items, like a node whose transcript watch ended. The chat notices that the session is newer than its
/// newest line and nothing came over its subscription, asks for what came after it (`AgentSyncPacing.catchUpAt`) and
/// shows the message with the reply under it. The same for the last line of a burst, which only the session's `rev` tells.
final class ChatCatchUpTests: XCTestCase {
    private var hub: String!
    private let working = "macbook/a1f3c2d4-0000-4000-8000-000000000001" // the weather widget session, at work

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
    }

    @MainActor func testAChatWhoseItemsStopCatchesUp() throws {
        let app = open()
        let asked = "补拉测试的新消息", answer = "补拉测试的新回复"
        try say(answer, user: asked, drop: true)

        // Asked for after `AgentSyncPacing.staleQuiet` without items, at most every `stalePullEvery`: well within 20 s.
        let message = text(app, asked)
        let reply = text(app, answer)
        XCTAssertTrue(message.waitForExistence(timeout: 20), "the new message never came in without its push")
        XCTAssertTrue(reply.waitForExistence(timeout: 5), "the new reply never came in without its push")
        // Its own exchange: the reply under the new message, not with the turn before it.
        XCTAssertGreaterThan(reply.frame.minY, message.frame.maxY, "the reply is not under the new message")
        shot(app, "caught-up", file: "catch-up")
    }

    /// The last line of a burst, written 0.3 s after the one before, whose push is lost (#1179): its `updated_at` is
    /// within a second of the line the chat holds, so only the rev that moved since tells. Shows the bug with the mock's
    /// lines written now (`MOCK_SYNC_REVS=1`); without it their times are old and the `updated_at` alone tells.
    @MainActor func testALostLastLineOfABurstCatchesUp() throws {
        let app = open()
        let first = "连写测试的第一行", last = "连写测试的最后一行"
        try say(first)
        Thread.sleep(forTimeInterval: 0.3) // not waiting for it on screen: that takes longer than the slack
        try say(last, drop: true)
        XCTAssertTrue(text(app, first).waitForExistence(timeout: 10), "the first line was pushed")
        XCTAssertTrue(text(app, last).waitForExistence(timeout: 20), "the last line of the burst never came in without its push")
        shot(app, "burst-caught-up", file: "catch-up-burst")
    }

    @MainActor private func open() -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // No chat kept on disk from an earlier run: the mock starts its conversations over at each reset, and a cached
        // chat's seqs would hide the lines this test adds.
        app.launchArguments = ["-pairURL", pair, "-showScrollState", "YES", "-dev.openChat", "", "-openChatsFresh", "YES",
                               "-tab", "dev", "-openSession", working]
        app.launchPinned()
        // The notification question is SpringBoard's and can cover the chat.
        LiveActivityShots.allowNotifications()
        let probe = app.staticTexts.matching(identifier: "chat.scrollState").firstMatch
        XCTAssertTrue(probe.waitForExistence(timeout: 20), "the chat did not open")
        sleep(3) // the backlog is in and the subscription is quiet
        return app
    }

    /// A reply in the open chat (after `user`'s message); `drop`: written and listed, its push lost.
    private func say(_ answer: String, user: String? = nil, drop: Bool = false) throws {
        var c = URLComponents(string: hub + "/mock/say")!
        c.queryItems = [URLQueryItem(name: "text", value: answer)]
        if let user { c.queryItems?.append(URLQueryItem(name: "user", value: user)) }
        if drop { c.queryItems?.append(URLQueryItem(name: "drop", value: "1")) }
        _ = try Data(contentsOf: c.url!)
    }

    @MainActor private func text(_ app: XCUIApplication, _ words: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String, file: String) {
        let screen = app.screenshot()
        let shot = XCTAttachment(screenshot: screen)
        shot.name = name
        shot.lifetime = .keepAlways
        add(shot)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"] ?? "light"
            try? screen.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/\(file)-\(style).png"))
        }
    }
}
