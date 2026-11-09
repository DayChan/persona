import XCTest

/// The inbox event page's Hand to AI bar (#2055): a chip hands the item over in one tap behind a toast whose Undo sends
/// nothing; left alone it goes to the main chat once. The target capsule is a menu, its choice is remembered across
/// launches, and the bar rides up with the keyboard, the content above it. Against scripts/mock/mock_hub.py with
/// MOCK_INBOX_EVENTS=1 (`TEST_RUNNER_PERSONA_MOCK_HUB`); `TEST_RUNNER_PERSONA_SHOTS` saves the screenshots,
/// `TEST_RUNNER_PERSONA_SHOTS_APPEARANCE` only names them.
final class InboxHandOffBarTests: XCTestCase {
    private static let item = "lark:oc_demo1:100"

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor func testAChipHandsOverInOneTapAndUndoSendsNothing() throws {
        let hub = try mockHub()
        let app = open(hub)
        let chip = app.buttons["handoff-chip-summarize"]
        XCTAssertTrue(chip.waitForExistence(timeout: 20), "no chips:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["handoff-target"].exists, "no target capsule")
        XCTAssertTrue(app.textFields["handoff-field"].exists, "no field")
        XCTAssertFalse(app.buttons["交给 AI"].exists, "the old Hand to AI button is still on the page")
        shot(app, "bar")
        let before = handedOff(hub)

        chip.tap()
        let undo = app.buttons["toast-secondary"]
        XCTAssertTrue(undo.waitForExistence(timeout: 3), "no toast with Undo:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["toast-action"].exists, "no View on the toast")
        shot(app, "chip-sent")
        undo.tap()
        XCTAssertTrue(chip.waitForExistence(timeout: 3), "the chips did not come back after Undo")
        sleep(6)
        XCTAssertEqual(handedOff(hub), before, "Undo still handed the item over")

        chip.tap()
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label BEGINSWITH %@", "已交给")).firstMatch
            .waitForExistence(timeout: 15), "the hand-off never went:\n\(app.debugDescription)")
        shot(app, "chip-handed")
        XCTAssertEqual(handedOff(hub), before + 1, "the chip did not hand the item to the main chat once")
    }

    @MainActor func testTheTargetIsAMenuAndIsRemembered() throws {
        let hub = try mockHub()
        var app = open(hub)
        let target = app.buttons["handoff-target"]
        XCTAssertTrue(target.waitForExistence(timeout: 20), "no target capsule:\n\(app.debugDescription)")
        if target.value as? String != "主对话" {
            target.tap()
            app.buttons["主对话"].firstMatch.tap()
        }
        target.tap()
        let newChat = app.buttons["新对话"].firstMatch
        XCTAssertTrue(newChat.waitForExistence(timeout: 3), "no menu:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["主对话"].firstMatch.exists, "the menu has no main chat")
        sleep(1)
        shot(app, "target-menu")
        newChat.tap()
        XCTAssertEqual(target.value as? String, "新对话")

        app.terminate()
        app = open(hub)
        let again = app.buttons["handoff-target"]
        XCTAssertTrue(again.waitForExistence(timeout: 20))
        XCTAssertEqual(again.value as? String, "新对话", "the target was not remembered")
        again.tap()
        app.buttons["主对话"].firstMatch.tap()
        XCTAssertEqual(again.value as? String, "主对话")
    }

    @MainActor func testTheBarRidesUpWithTheKeyboard() throws {
        let hub = try mockHub()
        let app = open(hub)
        let field = app.textFields["handoff-field"]
        XCTAssertTrue(field.waitForExistence(timeout: 20), "no field:\n\(app.debugDescription)")
        let down = field.frame.maxY
        field.tap()
        let keyboard = app.keyboards.firstMatch
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "no software keyboard")
        field.typeText("先看看这个人之前说过什么")
        sleep(1)
        XCTAssertLessThanOrEqual(field.frame.maxY, keyboard.frame.minY + 1, "the keyboard covers the field")
        XCTAssertLessThan(field.frame.maxY, down, "the bar did not move up")
        XCTAssertLessThanOrEqual(app.buttons["handoff-chip-summarize"].frame.maxY, field.frame.minY, "the chips are not above the field")
        shot(app, "keyboard")
    }

    // MARK: -

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=1 (PERSONA_MOCK_HUB)")
        }
        return hub
    }

    @MainActor private func open(_ hub: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", "",
                               "-openInboxItem", Self.item]
        app.launchPinned()
        acceptIconAlert()
        return app
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

    /// How many hand-offs reached the mock's main chat (`/mock/sent_to`, the ones with an inbox block).
    private func handedOff(_ hub: String) -> Int {
        guard let url = URL(string: hub + "/mock/sent_to") else { return 0 }
        var count = 0
        let done = expectation(description: "sent_to")
        URLSession.shared.dataTask(with: url) { data, _, _ in
            if let data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let sent = obj["sent"] as? [[String: Any]] {
                count = sent.filter { ($0["text"] as? String)?.contains("收件箱事件") == true }.count
            }
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return count
    }
}
