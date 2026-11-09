import XCTest

/// Dictation in a to-do's notes (#2188) against the mock hub: the New To-Do sheet and the details' Notes each have the
/// mic, and on the notes as they are one tap opens the editor. A simulator hears nothing, so the system's questions
/// about the microphone are declined and the denied-access toast is what follows the tap.
/// TEST_RUNNER_PERSONA_SHOTS=<dir> also saves the screens (TEST_RUNNER_PERSONA_SHOTS_APPEARANCE names them light / dark).
final class TodoNotesDictationTests: XCTestCase {
    private var app: XCUIApplication!

    @MainActor func testNotesHaveTheMic() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        continueAfterFailure = false
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openShellPage", "todos"]
        app.launchPinned()

        let add = app.buttons["新建待办"]
        XCTAssertTrue(add.waitForExistence(timeout: 60))
        add.tap()
        let newMic = app.buttons["todo.new.notes.mic"]
        XCTAssertTrue(newMic.waitForExistence(timeout: 10))
        XCTAssertTrue(newMic.isHittable)
        XCTAssertEqual(newMic.label, "听写")
        shot("todo-new-notes-mic")
        app.buttons["取消"].tap()
        XCTAssertTrue(newMic.waitForNonExistence(timeout: 10))

        // The detail, as the screenshots open it (`-openTodoTitled`).
        app.terminate()
        app.launchArguments += ["-openTodoTitled", "给 NAS 配自动备份"]
        app.launchPinned()
        let mic = app.buttons["todo.notes.mic"]
        XCTAssertTrue(mic.waitForExistence(timeout: 60))
        XCTAssertTrue(mic.isHittable)
        let field = app.descendants(matching: .any)["todo.notes.field"]
        XCTAssertFalse(field.exists)
        shot("todo-notes-mic")

        mic.tap()
        XCTAssertTrue(field.waitForExistence(timeout: 10), "the mic did not open the notes editor")
        let denied = app.staticTexts.containing(NSPredicate(format: "label CONTAINS '麦克风' OR label CONTAINS '语音识别'")).firstMatch
        XCTAssertTrue(waitDecliningAlerts(for: denied, timeout: 15), "no toast says why dictation could not start")
        shot("todo-notes-editing")

        // The notes' Done, not the page's (which would mark the to-do done).
        let done = app.buttons["todo.notes.edit"]
        XCTAssertEqual(done.label, "完成")
        done.tap()
        XCTAssertTrue(field.waitForNonExistence(timeout: 10))
        XCTAssertEqual(done.label, "编辑")
        XCTAssertTrue(mic.exists)
    }

    /// Waits for element, answering the microphone and speech recognition questions with Don't Allow on the way (the
    /// system asks once per simulator; asked before, the toast comes at once and soon goes).
    @MainActor private func waitDecliningAlerts(for element: XCUIElement, timeout: TimeInterval) -> Bool {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let deadline = Date.now.addingTimeInterval(timeout)
        while Date.now < deadline {
            if element.exists { return true }
            let alert = springboard.alerts.firstMatch
            if alert.exists, let label = ["不允许", "Don’t Allow", "Don't Allow"].first(where: { alert.buttons[$0].exists }) {
                alert.buttons[label].firstMatch.tap()
            }
            usleep(200_000)
        }
        return element.exists
    }

    private func shot(_ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
            let device = UIDevice.current.userInterfaceIdiom == .pad ? "-ipad" : ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + device + look + ".png"))
        }
    }
}
