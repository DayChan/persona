import XCTest

/// Messages Persona typed in for the model in the main conversation (#2358, docs/protocol/protocol.md「Persona 注入的消息」): a
/// task's notice and a scheduled run are each one centered line, never a bubble with the model's instructions; the
/// notice opens its task, the run's line opens to what the task asks. Against scripts/mock/mock_hub.py with
/// MOCK_TASKS=1 MOCK_INJECTED=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASKS_AT=14 (the lines at the bottom) (PERSONA_MOCK_HUB). With
/// PERSONA_SHOTS set it writes the lines and the opened run there (PERSONA_SHOTS_APPEARANCE names the appearance).
final class InjectedLineTests: XCTestCase {
    private var hub = ""
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_INJECTED=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASKS_AT=14 (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/injected-\(appearance)-\(name).png"))
    }

    @MainActor func testNoticesAreLinesThatOpenTheirTaskOrAsk() throws {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main]
        app.launchPinned()

        let any = app.descendants(matching: .any)
        let notice = any["injected-task_notice"].firstMatch
        XCTAssertTrue(notice.waitForExistence(timeout: 20), "no task notice line:\n\(app.debugDescription)")
        XCTAssertTrue(notice.label.contains("任务「整理发布说明」做完了"), notice.label)
        let run = any.matching(identifier: "message-source-label").matching(NSPredicate(format: "label == %@", "@午间整理")).firstMatch
        XCTAssertTrue(run.exists, "no scheduled source mention")
        // The model's instructions are nowhere: neither the notice's nor the run's first line as typed.
        for text in ["这是 Persona 的任务通知", "你来决定要不要告诉用户", "Agent 最后说的", "⏰ 午间整理"] {
            XCTAssertFalse(any.matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch.exists, "shows 「\(text)」")
        }
        sleep(1) // the list settles at the newest
        try shot(app, "lines")

        // A short scheduled request is already visible in its source row; only long requests need folding.
        XCTAssertTrue(any.matching(NSPredicate(format: "label CONTAINS %@", "看看上午的邮件和飞书")).firstMatch.exists)
        try shot(app, "scheduled-source")

        // The notice opens its task's detail.
        notice.tap()
        let title = app.staticTexts.matching(NSPredicate(format: "label == %@", "整理发布说明")).firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 5), "the task did not open:\n\(app.debugDescription)")
        app.terminate()
    }
}
