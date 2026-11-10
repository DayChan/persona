import XCTest

/// One-tap meeting recording and its minutes (docs/apple/recording.md「Meeting recordings」, #2694), against a mock hub
/// (PERSONA_MOCK_HUB, fresh per run: the hub keeps what was uploaded). A made-up meeting stands in for the microphone
/// (`-showMeetingSample`): no recording starts for real. PERSONA_SHOTS=<dir> saves the recording page, the meeting being
/// recorded, its Live Activity (island and Lock Screen) and the minutes (PERSONA_SHOTS_APPEARANCE names the appearance).
final class MeetingRecordingUITests: XCTestCase {
    private let live = "00000000-0000-0000-0000-000000000002"
    private let past = "00000000-0000-0000-0000-000000000003"

    /// The Recordings page leads with Meeting Recording; the drawer has it first among the places.
    @MainActor func testMeetingRecordingIsOneTapAway() throws {
        let hub = try mockHub()
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings"]
        app.launchPinned()
        let start = app.buttons["recording-meeting-start"]
        XCTAssertTrue(start.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertEqual(start.label, "会议录音")
        XCTAssertTrue(app.buttons["recording-start"].exists, "the plain recording stays")
        try shot(app, "recordings")

        app.terminate()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-newAssistant", "YES", "-openAssistantSidebar", "YES"]
        app.launchPinned()
        let row = app.buttons["sidebar-meeting-recording"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(row.label.contains("会议录音"), row.label)
    }

    /// A meeting being recorded: its name from the calendar, the time, Meeting, its bookmarks; the drawer's row opens it;
    /// its text reaches the hub with the meeting.
    @MainActor func testMeetingBeingRecorded() throws {
        let hub = try mockHub()
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "recording"]
        app.launchPinned()
        let title = app.descendants(matching: .any)["recording-now-title"].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(app.staticTexts["产品周会"].firstMatch.exists, app.debugDescription)
        XCTAssertTrue(app.staticTexts["12:34"].exists, app.debugDescription)
        XCTAssertTrue(app.buttons["recording-stop"].exists)
        XCTAssertTrue(app.buttons["recording-bookmark"].exists)
        try shot(app, "meeting-recording")
        // Synced as it goes: the meeting's title and its text so far.
        XCTAssertTrue(wait(15) { try self.stored(hub, self.live)?.contains("今天先看录音入口的进展") == true }, "没有同步到 hub")
        XCTAssertTrue(try stored(hub, live)?.contains("产品周会") == true)

        app.terminate()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-newAssistant", "YES", "-openAssistantSidebar", "YES",
                               "-showMeetingSample", "recording"]
        app.launchPinned()
        let row = app.buttons["sidebar-meeting-recording"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(row.label.contains("录音中"), row.label)
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["recording-now-title"].firstMatch.waitForExistence(timeout: 10), app.debugDescription)
    }

    /// Paused by a call: says so, and that it carries on by itself.
    @MainActor func testInterruptedByACall() throws {
        let hub = try mockHub()
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "interrupted"]
        app.launchPinned()
        let state = app.staticTexts["recording-now-state"].firstMatch
        XCTAssertTrue(state.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertEqual(state.label, "来电暂停")
        XCTAssertTrue(app.buttons["继续"].exists, app.debugDescription)
    }

    /// The meeting's Live Activity: Dynamic Island compact and expanded, Lock Screen. PERSONA_MEETING_PHASE=recording
    /// (default) | paused | interrupted. Shots, not a pass / fail check of the drawing; the Lock Screen's Stop is checked.
    @MainActor func testLiveActivityShots() throws {
        let hub = try mockHub()
        let phase = ProcessInfo.processInfo.environment["PERSONA_MEETING_PHASE"] ?? "recording"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", phase]
        app.launchPinned()
        XCTAssertTrue(app.descendants(matching: .any)["recording-now-title"].firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        sleep(2)
        XCUIDevice.shared.press(.home)
        sleep(3)
        try screen("island-\(phase)")
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        springboard.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 201, dy: 37)).press(forDuration: 1.5)
        sleep(2)
        try screen("island-expanded-\(phase)")
        XCUIDevice.shared.press(.home)
        sleep(1)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(2)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(3)
        // The system's first question about Live Activities (Allow / Always Allow): the activity's own buttons are
        // Bookmark and Stop, so its Allow is the system's.
        for label in ["Allow", "允许", "Always Allow", "始终允许"] {
            let b = springboard.buttons[label].firstMatch
            if b.exists { b.tap(); sleep(2) }
        }
        try screen("lock-\(phase)")
        let stop = springboard.buttons.matching(NSPredicate(format: "label CONTAINS %@ OR label CONTAINS %@", "停止", "Stop")).firstMatch
        XCTAssertTrue(stop.waitForExistence(timeout: 5), "no Stop on the Lock Screen:\n\(springboard.debugDescription)")
        // Unlock (swipe up; no passcode on the simulator): a locked simulator hangs the next test run.
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.99))
        bottom.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        sleep(1)
    }

    /// The hub's minutes on the meeting's page: the sections, a time that shows its line of the transcript, Copy and Share.
    @MainActor func testMinutesJumpToTheTranscript() throws {
        let hub = try mockHub()
        try minutesMode(hub, "ready")
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "minutes"]
        app.launchPinned()
        // Uploaded, written by the hub, read again (every 15 s while it waits).
        let copy = app.buttons["minutes-copy"]
        XCTAssertTrue(copy.waitForExistence(timeout: 45), app.debugDescription)
        XCTAssertTrue(app.staticTexts["纪要"].firstMatch.exists, app.debugDescription)
        XCTAssertTrue(app.buttons["minutes-share"].exists)
        // Brought to the top of the screen for the picture: the minutes, then the transcript under them.
        app.swipeUp()
        try shot(app, "minutes")
        // The sections below, scrolled into view one by one.
        for text in ["要点", "决定", "待办", "张三 · 周五前", "未决问题"] {
            let element = app.staticTexts[text].firstMatch
            var swipes = 0
            while !element.exists || !element.isHittable, swipes < 4 { app.swipeUp(); swipes += 1 }
            XCTAssertTrue(element.exists, "no \(text):\n\(app.debugDescription)")
        }
        // The meeting's fields went up with it.
        let body = try stored(hub, past) ?? ""
        XCTAssertTrue(body.contains("\"kind\":\"meeting\""), body)
        XCTAssertTrue(body.contains("\"attendees\":[\"张三\",\"李四\",\"王五\"]"), body)
        try shot(app, "minutes-sections")
        // The open question was said at 1:26: its time shows that line, lit.
        let time = app.buttons.matching(identifier: "minutes-time").allElementsBoundByIndex.last
        XCTAssertNotNil(time)
        if let time {
            if !time.isHittable { app.swipeUp() }
            time.tap()
        }
        let lit = app.descendants(matching: .any)["recording-sentence-lit"].firstMatch
        XCTAssertTrue(lit.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(lit.isHittable, "the line is not in view")
        try shot(app, "minutes-jump")
    }

    /// Not written yet: says the hub is writing them.
    @MainActor func testMinutesBeingWritten() throws {
        let hub = try mockHub()
        try minutesMode(hub, "pending")
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "pending"]
        app.launchPinned()
        let writing = app.descendants(matching: .any)["minutes-waiting"].firstMatch
        XCTAssertTrue(writing.waitForExistence(timeout: 30), app.debugDescription)
        XCTAssertTrue(wait(30) { writing.label.contains("正在整理纪要") }, writing.label)
        try shot(app, "minutes-pending")
    }

    /// Writing them failed: Try Again asks the hub to write them again.
    @MainActor func testMinutesTryAgain() throws {
        let hub = try mockHub()
        try minutesMode(hub, "failed")
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "pending"]
        app.launchPinned()
        let again = app.buttons["再试一次"].firstMatch
        XCTAssertTrue(again.waitForExistence(timeout: 45), app.debugDescription)
        again.tap()
        XCTAssertTrue(wait(10) { try self.minutesAsked(hub).contains(self.past) }, "Try Again didn't reach the hub")
        XCTAssertTrue(app.descendants(matching: .any)["minutes-waiting"].firstMatch.waitForExistence(timeout: 5), app.debugDescription)
    }

    // MARK: Helpers

    private func minutesMode(_ hub: String, _ mode: String) throws {
        var request = URLRequest(url: URL(string: hub + "/mock/recording_minutes")!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: ["mode": mode])
        let done = expectation(description: "mode")
        URLSession.shared.dataTask(with: request) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    private func minutesAsked(_ hub: String) throws -> [String] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/recordings")!)
        return (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["minutes_asked"] as? [String] ?? []
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        try save(app.screenshot().pngRepresentation, name)
    }

    @MainActor private func screen(_ name: String) throws {
        let image = XCUIScreen.main.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        try save(image.pngRepresentation, name)
    }

    private func save(_ png: Data, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try png.write(to: URL(fileURLWithPath: "\(dir)/meeting-\(appearance)-\(name).png"))
    }

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") }
        return hub
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    /// A recording's stored body on the hub as JSON text (nil: not there).
    private func stored(_ hub: String, _ id: String) throws -> String? {
        let data = try Data(contentsOf: URL(string: hub + "/mock/recordings")!)
        let all = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        guard let body = (all["recordings"] as? [String: Any])?[id] else { return nil }
        return String(decoding: try JSONSerialization.data(withJSONObject: body, options: [.sortedKeys]), as: UTF8.self)
    }

    private func wait(_ seconds: TimeInterval, _ ok: () throws -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(seconds)
        while Date.now < deadline {
            if (try? ok()) == true { return true }
            usleep(200_000)
        }
        return false
    }
}
