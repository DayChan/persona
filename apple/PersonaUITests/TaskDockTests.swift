import XCTest

/// The phone's Progress tray with more tasks than fit (#1094, then #2879): every task going on is listed in the tray
/// above the action bar, scrolling up and down inside it, while All stays at the tray's top, always in reach. Against
/// scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASKS_DOCK=many (PERSONA_MOCK_HUB).
final class TaskDockTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_DOCK=many (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor func testBoardStaysInReachWhileTasksScroll() throws {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair]
        app.launchPinned()

        let progress = app.openActionTray("progress")
        let tasks = app.trayTasks
        app.waitForRows(tasks, atLeast: 6, timeout: 10)
        XCTAssertGreaterThanOrEqual(tasks.count, 6, "want six or more tasks:\n\(app.debugDescription)")
        let board = app.buttons["action-tray.board"]
        XCTAssertTrue(board.exists, "no 全部 in the tray")
        sleep(2) // the conversation above settles
        XCTAssertLessThan(board.frame.maxY, tasks.element(boundBy: 0).frame.minY, "全部 is not above the tasks")
        XCTAssertLessThan(tasks.element(boundBy: 0).frame.maxY, progress.frame.minY, "the tray is not above the bar")

        // Scroll the tasks: they move, All does not.
        let before = board.frame
        let first = tasks.element(boundBy: 0).frame.minY
        tasks.element(boundBy: 2).swipeUp()
        sleep(1)
        XCTAssertEqual(board.frame.minY, before.minY, accuracy: 0.5, "全部 moved with the tasks")
        XCTAssertTrue(board.isHittable, "全部 is out of reach after a scroll")
        XCTAssertLessThan(tasks.element(boundBy: 0).frame.minY, first - 10, "the tasks did not scroll")
    }
}
