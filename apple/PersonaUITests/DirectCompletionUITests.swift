import XCTest

/// MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASK_DECISION=1: a task-review decision on the pending tray.
final class DirectCompletionUITests: XCTestCase {
    @MainActor func testCompletingTheDecisionRemovesItWithoutAnotherAgentTurn() async throws {
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("the iPhone pending tray") }
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs the task decision mock hub")
        }
        continueAfterFailure = false
        _ = try await URLSession.shared.data(from: URL(string: hub + "/mock/reset")!)
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-dev.openChat", "",
                               "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001",
                               "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
        app.launchPinned()
        let pending = app.buttons["action-bar.pending"]
        XCTAssertTrue(pending.waitForExistence(timeout: 30), app.debugDescription)
        pending.tap()
        let tray = app.otherElements["action-tray.pending"].firstMatch
        let complete = tray.buttons["brief.complete.todo_task_tests"]
        let recommendation = tray.buttons["brief.accept.todo_task_tests"]
        XCTAssertTrue(complete.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertEqual(complete.label, "直接完成：补齐网络层的单元测试")
        XCTAssertTrue(recommendation.exists)
        let open = tray.buttons["打开：补齐网络层的单元测试"]
        XCTAssertTrue(open.exists)
        XCTAssertLessThan(open.frame.midX, complete.frame.midX)
        XCTAssertLessThan(complete.frame.midX, recommendation.frame.midX)
        let before = XCTAttachment(screenshot: app.screenshot())
        before.name = "Decision before direct completion"
        before.lifetime = .keepAlways
        add(before)
        complete.tap()
        XCTAssertTrue(complete.waitForNonExistence(timeout: 15), "the completed decision stayed in Pending")
        XCTAssertFalse(recommendation.exists, "the completed decision can still resume its Agent")
        XCTAssertFalse(tray.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "补齐网络层的单元测试")).firstMatch.exists,
                       "the finished task remains in Pending as a task or an idle Agent")
        let (data, _) = try await URLSession.shared.data(from: URL(string: hub + "/api/v1/todos")!)
        let response = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let todos = try XCTUnwrap(response["todos"] as? [[String: Any]])
        let todo = try XCTUnwrap(todos.first { $0["id"] as? String == "todo_task_tests" })
        XCTAssertNotNil(todo["done_at"])
        XCTAssertNil(todo["ask"])
    }
}
