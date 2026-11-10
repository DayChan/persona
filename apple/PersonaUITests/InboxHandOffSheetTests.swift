import XCTest

/// The Hand to AI sheet of an item page (#2121): after an item from one sender goes to a New Chat, the next item from that
/// sender opens the sheet with New Chat chosen and marked Last time. Against scripts/mock/mock_hub.py with
/// MOCK_INBOX_EVENTS=legacy MOCK_EVENT_MUTES=1 (items without event fields open the page with the action bar and this sheet;
/// the two system notifications share a sender), `TEST_RUNNER_PERSONA_MOCK_HUB`; `TEST_RUNNER_PERSONA_SHOTS` saves the
/// screenshot, `TEST_RUNNER_PERSONA_SHOTS_APPEARANCE` only names it.
final class InboxHandOffSheetTests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor func testTheSheetStartsWhereTheSenderWentLastTime() throws {
        let hub = try mockHub()
        var app = open(hub, item: "mail:a1:ci:77")
        let newChat = app.buttons.containing(NSPredicate(format: "label CONTAINS %@", "新对话")).firstMatch
        XCTAssertTrue(newChat.waitForExistence(timeout: 20), "no sheet:\n\(app.debugDescription)")
        XCTAssertFalse(lastTime(app).exists, "Last time before anything went from this sender")
        newChat.tap()
        app.buttons["发送"].tap()
        XCTAssertTrue(app.buttons["发送"].waitForNonExistence(timeout: 15), "the sheet did not send:\n\(app.debugDescription)")
        app.terminate()

        app = open(hub, item: "mail:a1:notice:110")
        XCTAssertTrue(lastTime(app).waitForExistence(timeout: 20), "no Last time mark:\n\(app.debugDescription)")
        shot(app, "sheet-last-time")
        XCTAssertTrue(app.buttons.containing(NSPredicate(format: "label CONTAINS %@", "新对话")).firstMatch.isSelected,
                      "New Chat is not the chosen target:\n\(app.debugDescription)")
    }

    // MARK: - Helpers

    /// The Last time chip, on its own or folded into its row's label.
    @MainActor private func lastTime(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "上次")).firstMatch
    }

    @MainActor private func open(_ hub: String, item: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", "",
                               "-openInboxItem", item, "-showInboxAction", "handoff"]
        app.launchPinned()
        acceptIconAlert()
        return app
    }

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=legacy MOCK_EVENT_MUTES=1 (PERSONA_MOCK_HUB)")
        }
        return hub
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let png = app.screenshot().pngRepresentation
        let a = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        let env = ProcessInfo.processInfo.environment
        if let dir = env["PERSONA_SHOTS"] {
            let look = env["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
            try? png.write(to: URL(fileURLWithPath: dir).appending(path: "handoff-\(name)-\(UITestStyle.name)\(look).png"))
        }
    }
}
