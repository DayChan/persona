import XCTest

/// The "Type to Persona" control (`StartPersonaTypingIntent`, #1996): a press opens the main conversation with its field focused
/// and the keyboard up. `-openTypeControl YES` does at launch what the press does (`AppModel.openTypingConversation`; a fresh
/// install knows no main conversation yet, so it is the new conversation page), and `-openTypeControl 3` three seconds after the
/// shell is up and Persona's settings named the main conversation (the path of an app that is already running).
/// Also the control gallery (Control Center › Add a Control): Persona lists Ask Persona, Persona Agents and Type to Persona once each.
/// Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB); run it on a fresh stock simulator before anything types (a typed text turns
/// the simulator's software keyboard off, docs/lessons/testing.md). PERSONA_SHOTS=<dir>: while the keyboard is up the test writes
/// `<name>.ready` there and waits for `<name>.png` (iOS 27's `app.screenshot()` leaves the keyboard blank, so the runner takes it
/// with `simctl io screenshot`), giving up after a while.
final class TypeControlUITests: XCTestCase {
    private let env = ProcessInfo.processInfo.environment
    private var pair = ""
    private var language: String { env["PERSONA_UI_LANGUAGE"] ?? "zh-Hans" }
    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = env["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    /// Follows the system's Light or Dark (the runner sets it with `simctl ui appearance`): an app scheme of its own (`-ui.scheme`)
    /// switches the home screen icon, and SpringBoard's "icon changed" alert then keeps the app inactive, so the ask would wait.
    @MainActor private func launch(_ args: [String]) -> XCUIApplication {
        clearSpringBoardAlerts()
        let app = XCUIApplication()
        var all = ["-pairURL", pair, "-ui.oldLayout", "NO", "-dev.openChat", ""] + args
        if let lang = env["PERSONA_UI_LANGUAGE"] { all += ["-AppleLanguages", "(\(lang))"] }
        app.launchArguments = all
        app.launchPinned()
        return app
    }

    /// Answers SpringBoard alerts left by an earlier run (an icon change, a permission), which would keep the app inactive.
    private func clearSpringBoardAlerts() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for _ in 0..<3 {
            let alert = springboard.alerts.firstMatch
            guard alert.waitForExistence(timeout: 1) else { return }
            alert.buttons.firstMatch.tap()
            _ = alert.waitForNonExistence(timeout: 3)
        }
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement { app.descendants(matching: .any).matching(identifier: id).firstMatch }

    /// Asks the runner for a `simctl` screenshot while the screen holds: `<name>.ready`, then up to 20 s for `<name>.png`.
    private func shot(_ name: String) {
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let base = URL(fileURLWithPath: dir)
        let ready = base.appendingPathComponent("\(name).ready"), png = base.appendingPathComponent("\(name).png")
        try? Data().write(to: ready)
        let end = Date.now.addingTimeInterval(20)
        while Date.now < end, !FileManager.default.fileExists(atPath: png.path) { usleep(250_000) }
        try? FileManager.default.removeItem(at: ready)
    }

    private func keyboardUp(_ app: XCUIApplication, within timeout: TimeInterval) -> Bool {
        app.keyboards.firstMatch.waitForExistence(timeout: timeout)
    }

    /// The running app on the Agents page: the press takes it to the main conversation, its field focused, the keyboard up.
    @MainActor func testAPressWhileTheAppRunsOpensTheMainConversationTyping() {
        let app = launch(["-tab", "dev", "-openTypeControl", "3"])
        let field = element(app, "shell-composer-field")
        XCTAssertTrue(field.waitForExistence(timeout: 60), "no composer after the press:\n\(app.debugDescription)")
        XCTAssertTrue(keyboardUp(app, within: 45), "the keyboard did not come up:\n\(app.debugDescription)")
        XCTAssertFalse(element(app, "assistant-welcome").exists, "the new conversation page, not the main conversation")
        sleep(1) // the keyboard settles
        shot("type-main-\(language)-\(env["PERSONA_APPEARANCE"] ?? "system")")
    }

    /// Pressed at launch (a cold start): the composer that comes up takes it. On a fresh install no main conversation is known yet
    /// and that is the new conversation page; with a saved snapshot it is the main conversation.
    @MainActor func testAPressAtLaunchFocusesTheComposer() {
        let app = launch(["-tab", "dev", "-openTypeControl", "YES"])
        XCTAssertTrue(element(app, "shell-composer-field").waitForExistence(timeout: 60), "no composer:\n\(app.debugDescription)")
        XCTAssertTrue(keyboardUp(app, within: 30), "the keyboard did not come up:\n\(app.debugDescription)")
        sleep(1)
        shot("type-launch-\(language)")
    }

    /// Without the control the main conversation opens with the keyboard down, and coming back to the app is not another ask.
    @MainActor func testWithoutThePressTheKeyboardStaysDown() {
        let app = launch(["-tab", "assistant"])
        XCTAssertTrue(element(app, "shell-composer-field").waitForExistence(timeout: 60), "no composer:\n\(app.debugDescription)")
        XCTAssertFalse(keyboardUp(app, within: 8), "the keyboard came up without the press")
    }

    /// The press is spent once taken: put the keyboard away, leave the app and come back, and it stays down.
    @MainActor func testTheAskFocusesOnce() {
        let app = launch(["-tab", "dev", "-openTypeControl", "3"])
        XCTAssertTrue(keyboardUp(app, within: 60), "the keyboard did not come up:\n\(app.debugDescription)")
        // The chat dismisses the keyboard as it is dragged (there is no button for it).
        app.swipeDown()
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 10), "could not put the keyboard away")
        XCUIDevice.shared.press(.home)
        sleep(2)
        app.activate()
        XCTAssertTrue(element(app, "shell-composer-field").waitForExistence(timeout: 15))
        XCTAssertFalse(keyboardUp(app, within: 6), "coming back raised the keyboard again")
    }

    /// Control Center › Add a Control › Persona: each control once, Type to Persona among them.
    @MainActor func testTheControlGalleryListsEachControlOnce() throws {
        // Launched once so the system knows the widget extension.
        let app = launch(["-tab", "assistant"])
        XCTAssertTrue(element(app, "shell-composer-field").waitForExistence(timeout: 60))
        XCUIDevice.shared.press(.home)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        sleep(2)
        clearSpringBoardAlerts()
        // Control Center: a pull down from the top right corner.
        let top = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.005))
        top.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.7)))
        sleep(2)
        // Edit: a long press on an empty part of the page, then Add a Control.
        let add = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["添加控制", "添加控件", "Add a Control", "コントロールを追加"])).firstMatch
        if !add.waitForExistence(timeout: 3) {
            // The top left "+" (Add Controls) puts the page in edit mode; else a long press on an empty part of it.
            let edit = springboard.buttons["Add Controls"]
            if edit.exists { edit.tap() } else { springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.93)).press(forDuration: 1.5) }
        }
        attachTree(springboard, "control-center")
        XCTAssertTrue(add.waitForExistence(timeout: 10), "no Add a Control in Control Center's edit mode")
        add.tap()
        let search = springboard.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 10), "no search field in the control gallery")
        search.tap()
        // Key by key: `typeText` would turn the simulator's software keyboard off for every test after this one.
        XCTAssertTrue(springboard.keyboards.firstMatch.waitForExistence(timeout: 10), "no keyboard for the gallery's search")
        for key in "persona".map(String.init) {
            let k = springboard.keys[key].exists ? springboard.keys[key] : springboard.keys[key.uppercased()]
            XCTAssertTrue(k.waitForExistence(timeout: 3), "no \(key) key:\n\(springboard.keyboards.firstMatch.debugDescription)")
            k.tap()
        }
        sleep(3)
        attachTree(springboard, "gallery")
        // By kind (the button's identifier): SpringBoard's labels are in the system's language, not the app's. One button for each
        // control, and for Ask Persona and Persona Agents the stable kind (LegacyNames), not the interim `persona.control.*` one.
        let buttons = springboard.buttons
        for control in ["ask", "agents"] {
            let all = buttons.matching(NSPredicate(format: "identifier ENDSWITH %@", ".control.\(control)")).count
            XCTAssertEqual(all, 1, "\(control) listed \(all) times in the gallery")
            XCTAssertEqual(buttons.matching(identifier: "persona.control.\(control)").count, 0, "the interim \(control) kind is in the gallery")
        }
        XCTAssertEqual(buttons.matching(identifier: "persona.control.type").count, 1, "Type to Persona is not in the gallery once")
        shot("gallery-\(language)")
    }

    @MainActor private func attachTree(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(string: app.debugDescription)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = env["PERSONA_SHOTS"] {
            try? app.debugDescription.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).txt"), atomically: true, encoding: .utf8)
        }
    }
}
