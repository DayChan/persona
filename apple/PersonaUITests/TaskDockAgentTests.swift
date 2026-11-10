import XCTest

/// The main conversation's agents opened by hand (#1113), on the phone in the action bar's trays (#2879): Progress lists
/// the tasks and its All the board, Agents the agents with the waiting one first, and a tap opens the agent's page in
/// the task's sheet. Against
/// scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 and MOCK_TASKS_DOCK unset (four tasks, two agents) or
/// =pair (one task, one agent) (PERSONA_MOCK_HUB; PERSONA_DOCK_MODE names which). With PERSONA_SHOTS set it writes the
/// dock, the board and the agent's sheet there (PERSONA_DOCK_APPEARANCE names the simulator's appearance in the file).
final class TaskDockAgentTests: XCTestCase {
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
        let mode = env["PERSONA_DOCK_MODE"] ?? "default"
        let appearance = env["PERSONA_DOCK_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/dock-\(mode)-\(appearance)-\(name).png"))
    }

    @MainActor func testAgentsFollowTheTasks() throws {
        let pair = ProcessInfo.processInfo.environment["PERSONA_DOCK_MODE"] == "pair"
        let app = XCUIApplication()
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", url]
        app.launchPinned()

        // Progress lists the conversation's tasks; Agents its agents, the ones of its own among them.
        let progress = app.openActionTray("progress")
        let tasks = app.trayTasks
        let agents = app.trayAgents
        let want = pair ? (tasks: 1, agents: 1) : (tasks: 4, agents: 2)
        app.waitForRows(tasks, atLeast: want.tasks)
        XCTAssertGreaterThanOrEqual(tasks.count, want.tasks, app.debugDescription)
        sleep(2) // the conversation above settles
        try shot(app, "dock")

        // Progress' All: the board lists them under Needs You / Working.
        let board = app.buttons["action-tray.board"]
        XCTAssertTrue(board.waitForExistence(timeout: 5), app.debugDescription)
        board.tap()
        let row = app.buttons.matching(identifier: "task-board.agent").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5), app.debugDescription)
        sleep(1)
        try shot(app, "board")
        app.buttons["关闭"].firstMatch.tap()
        XCTAssertTrue(progress.waitForExistence(timeout: 5))
        sleep(1)

        // Agents: the one waiting for the user (the photo sync) says so, first.
        app.openActionTray("agents")
        app.waitForRows(agents, atLeast: want.agents)
        XCTAssertGreaterThanOrEqual(agents.count, want.agents, app.debugDescription)
        XCTAssertTrue(agents.element(boundBy: 0).label.contains("需要你"), agents.element(boundBy: 0).label)
        try shot(app, "dock-scrolled")

        // A tap opens the agent's page in the task's sheet.
        agents.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons.matching(identifier: "chat.title").firstMatch.waitForExistence(timeout: 5), app.debugDescription)
        sleep(2)
        try shot(app, "sheet")

        // Full Screen: the sheet goes down and the agent's page is pushed onto the assistant page; back comes to the
        // conversation with the dock, the sheet does not rise again.
        let expand = app.buttons["task-detail.fullscreen"]
        XCTAssertTrue(expand.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertEqual(expand.label, "全屏查看")
        expand.tap()
        XCTAssertTrue(app.buttons["关闭"].waitForNonExistence(timeout: 5), "the sheet stayed up")
        XCTAssertFalse(expand.exists, "the full page still has the sheet's 全屏查看")
        sleep(2)
        try shot(app, "fullscreen")
        XCTAssertFalse(progress.isHittable, "the action bar shows through the full page:\n\(app.debugDescription)")
        let edge = app.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5)) // the system's back swipe
        edge.press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.85, dy: 0.5)))
        XCTAssertTrue(progress.waitForExistence(timeout: 5) && progress.isHittable, "back did not come to the conversation:\n\(app.debugDescription)")
        sleep(1)
        XCTAssertFalse(app.buttons["关闭"].exists, "the sheet rose again after back")
        try shot(app, "back")
    }
}
