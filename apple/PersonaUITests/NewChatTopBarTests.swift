import XCTest

/// The top bar's New Chat (`assistant-new`) leaves the conversation shown for the welcome, in the shell (the iPhone's only
/// layout since #1696): from the main conversation and from another one, with the keyboard up, from the history drawer,
/// and right after a first message started a conversation. On build 209 a tap in the shell on the main conversation did nothing about one
/// launch in five: the model went to a new conversation and the page was not redrawn (#1749; gone since #1393).
///   PORT=18793 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18793 [TEST_RUNNER_NEWCHAT_RUNS=10] xcodebuild test …
///     -only-testing:PersonaUITests/NewChatTopBarTests
final class NewChatTopBarTests: XCTestCase {
    private var hub = ""
    /// The mock's main conversation and an ordinary one, with a line each shows.
    private let conversations = [("main", "b2e4d6f8-0000-4000-8000-000000000001"), ("other", "b2e4d6f8-0000-4000-8000-000000000003")]

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    @MainActor func testNewChatLeavesAConversation() throws {
        for (name, id) in conversations {
            let app = launch(["-openAssistant", "server/\(id)"])
            try newChat(app, from: name)
            app.terminate()
        }
    }

    /// The shell on the main conversation, over several launches (NEWCHAT_RUNS, default 5): New Chat works every time.
    @MainActor func testNewChatFromTheMainConversationInTheShell() throws {
        var failed = 0, notes: [String] = []
        let runs = Int(ProcessInfo.processInfo.environment["NEWCHAT_RUNS"] ?? "") ?? 5
        for run in 1...runs {
            let app = launch(["-openAssistant", "server/\(conversations[0].1)", "-probes", "YES"])
            let new = app.buttons["assistant-new"].firstMatch
            XCTAssertTrue(new.waitForExistence(timeout: 20), "run \(run): no New Chat")
            _ = waitUntil(15) { new.isEnabled && !self.welcome(app).exists }
            RunLoop.current.run(until: Date().addingTimeInterval(1))
            let before = app.shellShown
            new.tap()
            if !welcome(app).waitForExistence(timeout: 5) {
                failed += 1
                shot(app, "shell main run \(run) nothing")
                let after = app.shellShown
                notes.append("run \(run): before tap moving=\(before?.moving ?? false) lingering=\(before?.lingering ?? false) strip=\(before?.strip ?? "?"); "
                             + "after moving=\(after?.moving ?? false) lingering=\(after?.lingering ?? false) strip=\(after?.strip ?? "?") "
                             + "trace=\((after?.trace ?? []).suffix(14).joined(separator: " | "))")
            }
            app.terminate()
        }
        XCTAssertEqual(failed, 0, "New Chat did nothing in \(failed) of \(runs) launches\n" + notes.joined(separator: "\n"))
    }

    /// The user's case: two conversations started from New Chat, then New Chat again.
    @MainActor func testNewChatAfterStartingConversations() throws {
        let app = launch(["-newAssistant", "YES"])
        XCTAssertTrue(welcome(app).waitForExistence(timeout: 20), "no welcome to start from")
        for round in 1...3 {
            send(app, "新对话测试 \(round)")
            XCTAssertTrue(waitUntil(20) { !self.welcome(app).exists }, "round \(round): the conversation did not start")
            shot(app, "started \(round)")
            try newChat(app, from: "round \(round)")
        }
        app.terminate()
    }

    /// Typing in a conversation: the first tap on New Chat is not spent on putting the keyboard away.
    @MainActor func testNewChatWithTheKeyboardUp() throws {
        let app = launch(["-openAssistant", "server/\(conversations[1].1)"])
        XCTAssertTrue(app.buttons["assistant-new"].firstMatch.waitForExistence(timeout: 20), "no New Chat")
        let field = composer(app)
        XCTAssertTrue(field.waitForExistence(timeout: 20), "no composer\n\(app.debugDescription)")
        field.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10), "no keyboard")
        try newChat(app, from: "keyboard up")
        app.terminate()
    }

    /// A conversation opened from the history drawer: nothing the drawer leaves behind covers New Chat.
    @MainActor func testNewChatAfterOpeningFromTheSidebar() throws {
        let app = launch(["-openAssistant", "server/\(conversations[0].1)"])
        let menu = app.buttons["assistant-sidebar-button"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 20), "no sidebar button")
        RunLoop.current.run(until: Date().addingTimeInterval(1))
        menu.tap()
        let row = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "帮我约周五")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the sidebar did not list the conversation\n\(app.debugDescription)")
        row.tap()
        try newChat(app, from: "from the sidebar")
        app.terminate()
    }

    @MainActor private func launch(_ extra: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-dev.openChat", "", "-openChatsFresh", "YES"] + extra
        app.launchPinned()
        dismissAlerts()
        return app
    }

    @MainActor private func welcome(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any)["assistant-welcome"].firstMatch
    }

    /// In a conversation: New Chat is enabled, and a tap brings the welcome within a few seconds.
    @MainActor private func newChat(_ app: XCUIApplication, from label: String) throws {
        let new = app.buttons["assistant-new"].firstMatch
        XCTAssertTrue(new.waitForExistence(timeout: 20), "\(label): no New Chat")
        XCTAssertTrue(waitUntil(15) { new.isEnabled && !self.welcome(app).exists }, "\(label): not in a conversation, or New Chat disabled")
        RunLoop.current.run(until: Date().addingTimeInterval(1))
        shot(app, "\(label) before New Chat")
        new.tap()
        let came = welcome(app).waitForExistence(timeout: 5)
        shot(app, "\(label) after New Chat")
        XCTAssertTrue(came, "\(label): New Chat did nothing (enabled \(new.isEnabled), hittable \(new.isHittable))\n\(app.debugDescription)")
    }

    @MainActor private func send(_ app: XCUIApplication, _ text: String) {
        let field = composer(app)
        XCTAssertTrue(field.waitForExistence(timeout: 10), "no composer")
        field.tap()
        field.typeText(text)
        let button = app.buttons["shell-composer-send"].firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 5), "no send button")
        button.tap()
    }

    @MainActor private func composer(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any)["shell-composer-field"].firstMatch
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
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/newchat-\(name).png"))
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
