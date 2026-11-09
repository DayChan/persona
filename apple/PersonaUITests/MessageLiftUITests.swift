import XCTest

/// A long press lifts the message over the dimmed chat with its bar next to it (#603), against scripts/mock/mock_hub.py
/// with MOCK_MESSAGE_LIFT=1: a short message, the newest one just above the composer, and the middle of an answer taller
/// than the screen, whose bar must be on screen without scrolling; and an answer in Persona's conversation, in its box.
/// PERSONA_LIFT_SCHEME: light or dark.
final class MessageLiftUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private let short = "macbook/a1f3c2d4-0000-4000-8000-0000000c1e08"
    private let long = "macbook/a1f3c2d4-0000-4000-8000-0000000c1e09"
    private var scheme: String { ProcessInfo.processInfo.environment["PERSONA_LIFT_SCHEME"] ?? "light" }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        app = XCUIApplication()
    }

    private func launch(_ chat: String) {
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", chat, "-ui.scheme", scheme]
        app.launchPinned()
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "lift-\(name)-\(scheme)"
        a.lifetime = .keepAlways
        add(a)
    }

    private func text(_ s: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    @MainActor private func press(_ words: String) {
        let e = text(words)
        XCTAssertTrue(e.waitForExistence(timeout: 20), "missing: \(words)")
        sleep(1) // the chat settles at its newest
        e.press(forDuration: 0.8)
        sleep(1)
    }

    /// A short message of yours in the middle of the chat.
    @MainActor func testAShortMessage() throws {
        launch(short)
        press("可以，就这么改")
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label == %@", "复制")).firstMatch.exists || app.staticTexts["复制"].exists)
        shot("short")
    }

    /// The newest answer, just above the composer: no room under it for the bar.
    @MainActor func testTheNewestAnswerAboveTheComposer() throws {
        launch(short)
        press("收到，都提交了")
        shot("bottom")
    }

    /// An answer in Persona's own conversation, which draws it in a box (the new conversation layout, on by default):
    /// lifted in that box, with no card around it.
    @MainActor func testAnAnswerInPersonasConversation() throws {
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant",
                               "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001", "-openChatsFresh", "YES",
                               "-ui.scheme", scheme]
        app.launchPinned()
        press("博客迁移已经做完")
        XCTAssertTrue(app.staticTexts["复制"].firstMatch.exists, "no bar")
        shot("persona")
    }

    /// The middle of an answer taller than the screen: the bar is on screen.
    @MainActor func testTheMiddleOfALongAnswer() throws {
        launch(long)
        XCTAssertTrue(text("有问题的话告诉我").waitForExistence(timeout: 20))
        sleep(1)
        // Back a little, so the answer runs past both ends of the screen.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35))
            .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.6)))
        sleep(2)
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).press(forDuration: 0.8)
        sleep(1)
        let copy = app.staticTexts["复制"].firstMatch
        XCTAssertTrue(copy.exists && app.frame.contains(copy.frame), "the bar's Copy is on screen")
        shot("long")
    }
}
