import XCTest

/// A to-do's due date, reminder and priority (#1683) against the mock hub: the list shows them under the title, the
/// detail page edits them, and each change reaches the hub.
/// TEST_RUNNER_PERSONA_SHOTS=<dir> also saves the screens (TEST_RUNNER_PERSONA_SHOTS_APPEARANCE names them light / dark).
final class TodoPlanTests: XCTestCase {
    private var app: XCUIApplication!

    @MainActor func testPlanShowsAndSaves() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        continueAfterFailure = false
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openAssistantPage", "todos"]
        app.launchPinned()

        let nas = app.staticTexts["给 NAS 配自动备份"]
        XCTAssertTrue(nas.waitForExistence(timeout: 60))
        shot("todos-list")

        // The detail, as the screenshots open it (`-openTodoTitled`): a row's push needs the shell's destinations.
        app.terminate()
        app.launchArguments += ["-openTodoTitled", "给 NAS 配自动备份"]
        app.launchPinned()
        let due = app.switches["todo.plan.due"]
        XCTAssertTrue(due.waitForExistence(timeout: 60))
        shot("todo-plan")

        let priority = app.buttons["todo.plan.priority"]
        XCTAssertTrue(priority.waitForExistence(timeout: 10))
        priority.tap()
        let low = app.buttons["低"]
        XCTAssertTrue(low.waitForExistence(timeout: 10))
        low.tap()
        XCTAssertTrue(wait { try self.todo(hub, "todo_nas")["priority"] as? String == "low" })

        let remind = app.switches["todo.plan.remind"]
        remind.switches.firstMatch.tap()
        XCTAssertTrue(wait { try self.todo(hub, "todo_nas")["remind_at"] is NSNull || self.todo(hub, "todo_nas")["remind_at"] == nil })

        let allDay = app.switches["todo.plan.allDay"]
        XCTAssertTrue(allDay.waitForExistence(timeout: 10))
        allDay.switches.firstMatch.tap()
        XCTAssertTrue(wait { try self.todo(hub, "todo_nas")["due_at"] as? String != nil })
        shot("todo-plan-edited")
    }

    /// A to-do as the mock hub has it now.
    private func todo(_ hub: String, _ id: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + "/api/v1/todos")!)
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let todos = try XCTUnwrap(body["todos"] as? [[String: Any]])
        return try XCTUnwrap(todos.first { $0["id"] as? String == id })
    }

    /// Whether check holds within ten seconds (a change is saved shortly after it is made).
    private func wait(_ check: () throws -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(10)
        while Date.now < deadline {
            if (try? check()) == true { return true }
            usleep(200_000)
        }
        return false
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
