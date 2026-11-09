import XCTest

/// Synthetic main conversation only: MOCK_TASKS=1 MOCK_SUPERVISION=1, never the user's hub or chat.
final class SupervisionRoundUITests: XCTestCase {
    override func setUpWithError() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs mock_hub.py with MOCK_TASKS=1 MOCK_SUPERVISION=1")
        }
        continueAfterFailure = false
    }

    @MainActor private func launch(_ extra: [String] = []) -> XCUIApplication {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openChatsFresh", "YES",
                               "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"] + extra
        app.launchPinned()
        return app
    }

    private func has(_ app: XCUIApplication, _ text: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    @MainActor func testQuietRoundCollapsesAndUserPromotionRemainsVisible() {
        let app = launch()
        defer { app.terminate() }
        let record = app.buttons["supervision-record"].firstMatch
        XCTAssertTrue(record.waitForExistence(timeout: 25), app.debugDescription)
        XCTAssertEqual(app.buttons.matching(identifier: "supervision-record").count, 1)
        XCTAssertFalse(has(app, "检查示例任务的进展，不需要用户处理。").exists)
        XCTAssertFalse(has(app, "后台核对也完成了，继续静默跟进。").exists)
        XCTAssertTrue(has(app, "下一条真实输入仍在督办记录之外。").exists)
        XCTAssertTrue(has(app, "需要你决定：示例任务今天发布，还是明天发布？").exists)
        record.tap()
        XCTAssertTrue(app.descendants(matching: .any)["supervision-input"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(has(app, "检查示例任务的进展，不需要用户处理。").exists)
        record.tap()
        XCTAssertFalse(app.descendants(matching: .any)["supervision-input"].firstMatch.exists)
    }

    @MainActor func testLegacyDisabledPreferenceStillFoldsTheWholeRound() {
        let app = launch(["-ui.newChatLayout", "NO"])
        defer { app.terminate() }
        XCTAssertTrue(app.buttons["supervision-record"].firstMatch.waitForExistence(timeout: 25))
        XCTAssertFalse(has(app, "示例任务的测试通过，无需你处理。").exists)
        XCTAssertTrue(has(app, "需要你决定：示例任务今天发布，还是明天发布？").exists)
    }
}
