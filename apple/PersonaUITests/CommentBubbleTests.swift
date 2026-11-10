import XCTest

/// Comment blocks (#151, `MessageComments`): a sent message that leads with them draws them as cards in its bubble, and
/// comments waiting over the composer go out as the same blocks, as the web writes them (docs/protocol/protocol.md「Quote
/// and comment blocks at the start of messages」). The mock's widget chat has a review sent from the web (MOCK_COMMENTS=1);
/// sends come back in the transcript (MOCK_ECHO_SENT=1). Cards are told apart by their notes: a send of an earlier run
/// that the app still holds as unconfirmed adds cards of its own.
///   PORT=18795 MOCK_COMMENTS=1 MOCK_ECHO_SENT=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18795 [TEST_RUNNER_PERSONA_SHOTS=<dir>] xcodebuild test … -only-testing:PersonaUITests/CommentBubbleTests
final class CommentBubbleTests: XCTestCase {
    private var hub = ""
    private let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_COMMENTS=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    /// The review's three blocks are three cards; the words after them are the bubble's text; a tap opens a card.
    @MainActor func testAReviewFromTheWebDrawsItsCommentsAsCards() throws {
        for scheme in ["light", "dark"] {
            let app = launch(scheme: scheme)
            let cards = reviewCards(app)
            XCTAssertTrue(waitUntil(20) { cards.count == 3 }, "not the review's three cards (\(cards.count)):\n\(app.debugDescription)")
            XCTAssertTrue(cards.element(boundBy: 0).label.contains("Forecast.swift:7"), cards.element(boundBy: 0).label)
            XCTAssertTrue(cards.element(boundBy: 2).label.contains("button.save"), cards.element(boundBy: 2).label)
            let summary = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "改完这三处再提交")).firstMatch
            XCTAssertTrue(summary.exists, "the summary is not the bubble's text")
            let raw = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Code comment 1")).firstMatch
            XCTAssertFalse(raw.exists, "a block is drawn as text")
            shot(app, "bubble-\(scheme)")
            cards.element(boundBy: 0).tap()
            XCTAssertTrue(waitUntil(5) { cards.element(boundBy: 0).label.contains("+let city: String?") }, "the card did not open to its code")
            shot(app, "bubble-open-\(scheme)")
            app.terminate()
        }
    }

    /// `-showSampleComments`: two code comments and an element over the box; × drops one; Send writes the other two as
    /// the web does, before what was typed, and the box is left with none.
    @MainActor func testWaitingCommentsGoOutAsTheirBlocks() throws {
        let app = launch(["-showSampleComments", "YES"])
        let waiting = app.descendants(matching: .any).matching(identifier: "composer.comment")
        XCTAssertTrue(waitUntil(10) { waiting.count == 3 }, "no sample comments over the box:\n\(app.debugDescription)")
        shot(app, "composer-light")
        app.buttons.matching(identifier: "composer.comment.remove").element(boundBy: 2).tap()
        XCTAssertTrue(waitUntil(5) { waiting.count == 2 }, "× did not drop the comment")
        let field = app.textViews.firstMatch.exists ? app.textViews.firstMatch : app.textFields.firstMatch
        field.tap()
        field.typeText("go on")
        app.buttons["发送"].firstMatch.tap()
        let expected = [
            "> Code comment 1", "> File: Shared/Forecast.swift:7", "> Diff: branch since 4f2a9c1", "> ```diff", "> +let city: String?", "> ```", "> ",
            "> Make the city optional: old caches have none.", "",
            "> Code comment 2", "> File: Shared/Forecast.swift:30-31", "> Diff: branch since 4f2a9c1", "> ```diff", ">  return cached", "> +?? last", "> ```", "> ",
            "> Fall back to the last cache so the widget is never empty.", "",
            "go on",
        ].joined(separator: "\n")
        XCTAssertTrue(waitUntil(8) { self.sent().contains(expected) }, "sent: \(sent().last ?? "nothing")")
        XCTAssertTrue(waitUntil(5) { waiting.count == 0 }, "the comments stayed over the box")
        for note in ["Make the city optional", "Fall back to the last cache"] {
            let card = app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'bubble.comment' AND label CONTAINS %@", note)).firstMatch
            XCTAssertTrue(card.waitForExistence(timeout: 8), "the sent message's comment \"\(note)\" is not a card")
        }
        shot(app, "sent-light")
        app.terminate()
    }

    // MARK: Helpers

    /// The cards of the review MOCK_COMMENTS put in the chat, by their notes.
    @MainActor private func reviewCards(_ app: XCUIApplication) -> XCUIElementQuery {
        app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'bubble.comment' AND (label CONTAINS %@ OR label CONTAINS %@ OR label CONTAINS %@)",
                                                             "城市名用可选的吧", "解析失败时回退", "改成整行宽的主按钮"))
    }

    @MainActor private func launch(_ extra: [String] = [], scheme: String = "light") -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-app.tab", "chats", "-dev.openChat", widget, "-openSession", widget,
                               "-openChatsFresh", "YES", "-ui.scheme", scheme] + extra
        app.launchPinned()
        dismissAlerts()
        return app
    }

    private func sent() -> [String] {
        struct Sent: Decodable { struct Body: Decodable { var text: String? }; var sent: [Body] }
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/sent")!),
              let s = try? JSONDecoder().decode(Sent.self, from: data) else { return [] }
        return s.sent.compactMap(\.text)
    }

    private func dismissAlerts() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for _ in 0..<2 {
            let ok = springboard.alerts.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许", "OK", "好"])).firstMatch
            if ok.waitForExistence(timeout: 2) { ok.tap() }
        }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/comments-\(name).png"))
        }
    }

    private func waitUntil(_ timeout: TimeInterval, _ ok: @escaping () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if ok() { return true }
            RunLoop.current.run(until: Date().addingTimeInterval(0.3))
        }
        return false
    }
}
