import XCTest

/// Only the app's composer controls are inspected, not the system keyboard's buttons. Synthetic chats on a private
/// mock hub (`scripts/mock/composer_hub.py`) cover the shared assistant/Agents composer, multiline drafts
/// and drag dismissal (#2200).
final class ComposerKeyboardTests: XCTestCase {
    private var app: XCUIApplication!
    private let agent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    private let assistant = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs the isolated composer mock hub (PERSONA_MOCK_HUB)")
        }
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let tree = XCTAttachment(string: app.debugDescription)
            tree.name = "composer tree"
            tree.lifetime = .keepAlways
            add(tree)
        }
        super.tearDown()
    }

    @MainActor private func openChat(isAssistant: Bool, sideBars: Bool = false) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app = XCUIApplication()
        // Pair separately: a first pairing resets the route arguments, and must never point at a real hub.
        app.launchArguments = ["-pairURL", pair]
        app.launchPinned()
        XCTAssertTrue(app.buttons["Persona"].waitForExistence(timeout: 15) || field.waitForExistence(timeout: 15))
        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-holdChromeOpen", "YES", "-sideBars", sideBars ? "YES" : "NO",
                               "-dev.openChat", "", "-app.tab", isAssistant ? "assistant" : "chats",
                               "-tab", isAssistant ? "assistant" : "dev"]
        app.launchArguments += isAssistant ? ["-openAssistant", assistant] : ["-openSession", agent]
        app.launchPinned()
        XCTAssertTrue(field.waitForExistence(timeout: 15), "the isolated conversation did not open")
        sleep(2)
    }

    @MainActor private var field: XCUIElement {
        let shell = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        if shell.exists { return shell }
        return app.textViews.firstMatch.exists ? app.textViews.firstMatch : app.textFields.firstMatch
    }

    @MainActor private func shot(_ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = "\(name) \(UITestStyle.name)"
        attachment.lifetime = .keepAlways
        add(attachment)
        if let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            let url = URL(fileURLWithPath: directory).appendingPathComponent("\(UITestStyle.name)-\(appearance)-\(name).png")
            try? screenshot.pngRepresentation.write(to: url)
        }
    }

    @MainActor private var dismissButton: XCUIElement {
        app.buttons["shell-composer-dismiss-keyboard"].firstMatch
    }

    @MainActor private func headerButtons() -> Set<String> {
        let limit = field.frame.minY - 120
        return Set(app.buttons.allElementsBoundByIndex.filter { $0.isHittable && $0.frame.maxY < limit && $0.frame.minY > 40 }.map(\.label))
    }

    @MainActor private func typeAndDismiss(_ name: String, shell: Bool, sideBars: Bool = false) {
        if shell {
            XCTAssertFalse(dismissButton.exists, "dismiss-keyboard button should not appear before typing")
        }
        let before = headerButtons()
        field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.9)).tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "no software keyboard")
        let existing = field.value as? String ?? ""
        if !existing.isEmpty && existing != field.placeholderValue {
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: existing.count + 2))
        }
        field.typeText("first line\nsecond line")
        sleep(1)
        if shell {
            XCTAssertTrue(dismissButton.exists && dismissButton.isHittable, "missing dismiss-keyboard button while typing")
        }
        XCTAssertEqual(field.value as? String, "first line\nsecond line", "return should insert a newline, not send")
        let send = app.buttons["发送"].firstMatch
        XCTAssertTrue(send.exists && send.isEnabled && send.isHittable)
        var frames: [String: CGRect] = [:]
        if shell {
            let controls = ["shell-composer-add", "shell-composer-modes", "shell-composer-mic", "shell-composer-send"]
            for id in controls {
                let control = app.buttons[id].firstMatch
                XCTAssertTrue(control.exists && control.isHittable, "missing/hit-blocked \(id)")
                XCTAssertLessThanOrEqual(control.frame.maxY, app.keyboards.firstMatch.frame.minY + 1)
                frames[id] = control.frame
            }
            let expected = name.hasPrefix("assistant") ? "6.1 Sol" : "6.1 Sol Codex Mini Preview 2026 11 05"
            XCTAssertTrue((app.buttons["shell-composer-modes"].value as? String ?? "").hasPrefix(expected),
                          "the model label lost the version or variant")
            let mic = frames["shell-composer-mic"]!, send = frames["shell-composer-send"]!
            XCTAssertEqual(mic.height, send.height, accuracy: 1)
            XCTAssertEqual(mic.minY, send.minY, accuracy: 1, "controls must stay on one row")
            XCTAssertLessThanOrEqual(frames["shell-composer-modes"]!.maxX, mic.minX)
            XCTAssertLessThanOrEqual(field.frame.maxY, mic.minY + 1)
        }
        shot("\(name)-keyboard-up")
        if sideBars {
            XCTAssertFalse(before.subtracting(headerButtons()).isEmpty, "side-bars header did not fold")
            // Only the fog, the header has no title meanwhile: the system bar's names the session.
            XCTAssertTrue(app.navigationBars.staticTexts.allElementsBoundByIndex.contains { !$0.label.isEmpty },
                          "side-bars: no title while typing")
        }

        if shell {
            dismissButton.tapCenter()
            XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "dismiss button did not dismiss the keyboard")
            XCTAssertFalse(dismissButton.exists, "dismiss button stayed after keyboard dismissal")
            XCTAssertEqual(field.value as? String, "first line\nsecond line", "button dismissal must preserve the draft")
            field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.9)).tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "keyboard did not reopen")
        }

        let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3))
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: 160)), withVelocity: .default,
                   thenHoldForDuration: 0.1)
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "drag no longer dismisses the keyboard")
        sleep(1)
        if shell {
            XCTAssertFalse(dismissButton.exists, "dismiss-keyboard button remained after drag dismissal")
        }
        XCTAssertEqual(field.value as? String, "first line\nsecond line", "dismissal must preserve the draft")
        for (id, frame) in frames {
            let now = app.buttons[id].firstMatch.frame
            XCTAssertEqual(now.width, frame.width, accuracy: 1, "\(id) tap width changed")
            XCTAssertEqual(now.height, frame.height, accuracy: 1, "\(id) tap height changed")
            if id != "shell-composer-modes" {
                XCTAssertEqual(now.minX, frame.minX, accuracy: 1, "\(id) moved horizontally after dismissal")
            }
        }
        shot("\(name)-keyboard-down")
        if sideBars { XCTAssertEqual(headerButtons(), before, "side-bars header did not return") }

        // Sending still works after dismissal; only the local mock receives this synthetic draft.
        send.tapCenter()
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        let cleared = NSPredicate(format: "value == '' OR value == %@", field.placeholderValue ?? "")
        expectation(for: cleared, evaluatedWith: field)
        waitForExpectations(timeout: 8)
    }

    @MainActor func testAgentInTheShell() {
        openChat(isAssistant: false)
        typeAndDismiss("agent-shell", shell: true)
    }

    @MainActor func testAssistantInTheShell() {
        openChat(isAssistant: true)
        typeAndDismiss("assistant-shell", shell: true)
    }

    @MainActor func testModelLabelMatchesTheMenu() {
        openChat(isAssistant: true)
        field.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        let button = app.buttons["shell-composer-modes"]
        XCTAssertTrue((button.value as? String ?? "").hasPrefix("6.1 Sol"))
        button.tapCenter()
        XCTAssertTrue(app.staticTexts["6.1 Sol"].waitForExistence(timeout: 5), "menu and current label disagree")
        shot("assistant-model-menu")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)).tap()
    }

    @MainActor func testSideBarsHeaderFoldsWhileTyping() {
        openChat(isAssistant: false, sideBars: true)
        typeAndDismiss("side-bars", shell: false, sideBars: true)
    }
}
