import XCTest

/// The phone's action bar above the assistant's composer (#2879): Pending, Progress and Agents in the dock's low capsules,
/// each opening its tray above the bar, one at a time. Against scripts/mock/mock_hub.py with MOCK_TASKS=1
/// MOCK_TASKS_NO_ASKS=1 and MOCK_TASKS_DOCK unset (four tasks, two agents of their own, one of them waiting)
/// (PERSONA_MOCK_HUB). With PERSONA_SHOTS set it writes the bar and each tray there (PERSONA_SHOTS_APPEARANCE names the
/// simulator's appearance in the file).
final class AssistantActionBarUITests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/actionbar-\(appearance)-\(name).png"))
    }

    @MainActor func testThreeLowEntriesOpenOneTrayAtATime() throws {
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("the phone's bar") }
        let app = XCUIApplication()
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", url, "-tab", "assistant"]
        app.launchPinned()

        let pending = app.buttons["action-bar.pending"]
        let progress = app.buttons["action-bar.progress"]
        let agents = app.buttons["action-bar.agents"]
        XCTAssertTrue(progress.waitForExistence(timeout: 20), "no action bar:\n\(app.debugDescription)")
        XCTAssertTrue(pending.exists && agents.exists)
        XCTAssertFalse(app.buttons["task-dock.board"].exists, "the dock still shows beside the bar")
        // The tasks, the sessions and their states come in separate pulls: wait until the counts are in.
        let deadline = Date.now.addingTimeInterval(20)
        while !(progress.label.contains("4") && agents.label.contains("2")) && Date.now < deadline { usleep(200_000) }
        XCTAssertTrue(progress.label.hasPrefix("进度"), progress.label)
        XCTAssertTrue(pending.label.hasPrefix("待处理"), pending.label)

        // One row of capsules as low as the dock's: the same height, well under a 44 pt button.
        let height = progress.frame.height
        XCTAssertLessThan(height, 36, "the entries are \(height) pt tall")
        XCTAssertEqual(pending.frame.height, height, accuracy: 0.5)
        XCTAssertEqual(agents.frame.height, height, accuracy: 0.5)
        XCTAssertEqual(pending.frame.midY, agents.frame.midY, accuracy: 0.5, "not one row")
        XCTAssertLessThan(pending.frame.maxX, progress.frame.minX)
        XCTAssertLessThan(progress.frame.maxX, agents.frame.minX)
        sleep(2) // the conversation above settles
        try shot(app, "bar")

        // Progress: the conversation's tasks.
        progress.tap()
        let tasks = app.buttons.matching(identifier: "action-tray.task")
        XCTAssertTrue(tasks.firstMatch.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertGreaterThanOrEqual(tasks.count, 4)
        XCTAssertTrue(progress.isSelected, "Progress is not marked open")
        XCTAssertEqual(progress.frame.height, height, accuracy: 0.5, "the open entry changed its height")
        sleep(1)
        try shot(app, "progress")

        // Agents takes its place: one tray at a time.
        agents.tap()
        let rows = app.buttons.matching(identifier: "action-tray.agent")
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(tasks.firstMatch.waitForNonExistence(timeout: 5), "the Progress tray stayed open under Agents")
        XCTAssertFalse(progress.isSelected)
        XCTAssertGreaterThanOrEqual(rows.count, 2)
        sleep(1)
        try shot(app, "agents")

        // Pending: the agent of its own that waits (the photo sync) is there.
        pending.tap()
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(rows.firstMatch.label.contains("需要你"), rows.firstMatch.label)
        sleep(1)
        try shot(app, "pending")

        // The entry again closes its tray.
        pending.tap()
        XCTAssertTrue(rows.firstMatch.waitForNonExistence(timeout: 5), "the tray did not close")

        // Progress' All: the board, as the dock's All opened it.
        progress.tap()
        let all = app.buttons["action-tray.board"]
        XCTAssertTrue(all.waitForExistence(timeout: 5), app.debugDescription)
        all.tap()
        XCTAssertTrue(app.buttons.matching(identifier: "task-board.agent").firstMatch.waitForExistence(timeout: 5), app.debugDescription)
        app.buttons["关闭"].firstMatch.tap()
        XCTAssertTrue(progress.waitForExistence(timeout: 5))
        sleep(1)

        // An agent's row opens its page in the task's sheet.
        agents.tap()
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 5), app.debugDescription)
        rows.firstMatch.tap()
        XCTAssertTrue(app.buttons.matching(identifier: "chat.title").firstMatch.waitForExistence(timeout: 5), app.debugDescription)
    }
}
