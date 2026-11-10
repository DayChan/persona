import XCTest

/// A task opened from the main conversation (#3255): its sheet has one header, the agent's chat's — ✕, the task's
/// title over the agent's state and place, ⋯ — with no row of the space's other agents under it; ⋯ has
/// 「在 Agents 里打开」, and there the Agents page's chat keeps its tabs. Against scripts/mock/mock_hub.py with
/// MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 (PERSONA_MOCK_HUB); with MOCK_TASKS_DOCK=multi (PERSONA_TASK_MULTI=1) the task has
/// two agents and its title's ⌄ switches between them. Phone only (an iPad pushes the page, `IPadTaskDetailTests`).
/// With PERSONA_SHOTS set it writes the pages there, PERSONA_SHOTS_APPEARANCE naming the simulator's appearance.
final class TaskSheetHeaderTests: XCTestCase {
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
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/task-sheet-\(appearance)-\(name).png"))
    }

    @MainActor func testOneHeaderAndNoTabs() throws {
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("a phone's sheet") }
        let multi = ProcessInfo.processInfo.environment["PERSONA_TASK_MULTI"] == "1"
        let app = XCUIApplication()
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", url, "-holdChromeOpen", "YES", "-tab", "assistant"] // a run before may have ended on the Agents page
        app.launchPinned()

        app.openActionTray("progress") // the phone lists the tasks in the action bar's Progress tray (#2879)
        let pill = app.buttons.matching(NSPredicate(format: "identifier == 'action-tray.task' AND label CONTAINS '修小组件横屏崩溃'")).firstMatch
        XCTAssertTrue(pill.waitForExistence(timeout: 20), "no task in Progress:\n\(app.debugDescription)")
        sleep(2) // the conversation settles
        pill.tap()

        // One row: ✕ and the title (the task's) side by side, and nothing else that closes or goes back.
        let title = app.buttons.matching(identifier: "chat.title").firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 10), "no chat header in the sheet:\n\(app.debugDescription)")
        XCTAssertTrue(title.label.hasPrefix("修小组件横屏崩溃"), "the header's title is not the task's: \(title.label)")
        let close = app.buttons.matching(identifier: "关闭")
        XCTAssertEqual(close.count, 1, "not one ✕:\n\(app.debugDescription)")
        XCTAssertEqual(close.firstMatch.frame.midY, title.frame.midY, accuracy: 12, "✕ is not on the title's row")
        XCTAssertFalse(app.buttons["返回"].exists, "a ‹ besides the sheet's ✕")
        XCTAssertLessThan(title.frame.maxY, app.windows.firstMatch.frame.height * 0.2, "the header is not at the top")
        // No strip of the space's tabs.
        XCTAssertEqual(app.buttons.matching(identifier: "chat.sibling").count, 0, "the space's tabs are in the sheet")
        // The mock was just started: let the link settle so the pictures show the state line, not Connecting….
        let linking = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH '连接中'")).firstMatch
        let settled = Date.now.addingTimeInterval(15)
        while linking.exists && Date.now < settled { usleep(300_000) }
        sleep(1)
        try shot(app, multi ? "multi" : "task")

        if multi {
            // The title's ⌄ lists the task's two agents (once both sessions are in); picking the other switches the page.
            let both = Date.now.addingTimeInterval(20)
            while !title.label.contains("点按切换") && Date.now < both { usleep(300_000) }
            XCTAssertTrue(title.label.contains("点按切换"), "no ⌄ on the title: \(title.label)")
            title.tap()
            let other = app.buttons.matching(NSPredicate(format: "label BEGINSWITH '补齐网络层的单元测试'")).firstMatch
            XCTAssertTrue(other.waitForExistence(timeout: 5), "no agent to switch to:\n\(app.debugDescription)")
            try shot(app, "multi-menu")
            other.tap()
            sleep(2)
            XCTAssertTrue(app.buttons.matching(identifier: "chat.title").firstMatch.label.hasPrefix("修小组件横屏崩溃"))
            XCTAssertEqual(app.buttons.matching(identifier: "chat.sibling").count, 0)
            return
        }

        // ⋯ has the task's way to the Agents page; there the chat has its space's tabs again.
        app.buttons["更多"].firstMatch.tap()
        let open = app.buttons["在 Agents 里打开"]
        XCTAssertTrue(open.waitForExistence(timeout: 5), "no 在 Agents 里打开 in ⋯:\n\(app.debugDescription)")
        try shot(app, "menu")
        open.tap()
        let tabs = app.buttons.matching(identifier: "chat.sibling")
        let deadline = Date.now.addingTimeInterval(10)
        while tabs.count == 0 && Date.now < deadline { usleep(200_000) }
        XCTAssertGreaterThan(tabs.count, 0, "the Agents page's chat lost its tabs:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["返回"].firstMatch.exists, "no ‹ on the Agents page's chat")
        XCTAssertFalse(app.buttons["关闭"].exists, "the sheet is still up")
        sleep(1)
        try shot(app, "agents")
    }
}
