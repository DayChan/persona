import XCTest

/// The new conversation layout of Persona's main conversation (#1666, docs/apple/ios.md "New conversation layout"):
/// Persona's message to a task reads "Persona → @task" and an agent's report "task → @Persona", both folded to three
/// lines with Expand, no evidence list under the report; legacy preferences cannot disable it.
/// A slide along the right edge goes to a message the user typed; settings no longer expose the retired page.
/// Against scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_REPORTS=1 MOCK_INJECTED=1 MOCK_EXCHANGES=1
/// MOCK_TASKS_NO_ASKS=1 MOCK_TASKS_AT=5.5 (the exchanges at the bottom) (PERSONA_MOCK_HUB). With PERSONA_SHOTS set it
/// writes its screenshots there (PERSONA_SHOTS_APPEARANCE names the appearance).
final class NewChatLayoutTests: XCTestCase {
    private var hub = ""
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_REPORTS=1 MOCK_INJECTED=1 MOCK_EXCHANGES=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASKS_AT=5.5 (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    @MainActor private func launch(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main,
                               "-showScrollState", "YES", "-openChatsFresh", "YES", "-dev.openChat", "",
                               "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"] + extra
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/chat-layout-\(appearance)-\(name).png"))
    }

    private func has(_ app: XCUIApplication, _ text: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    @MainActor func testNewLayoutSaysWhoTalksToWhomAndFolds() throws {
        try verifyExchanges()
    }

    @MainActor private func verifyExchanges(_ extra: [String] = [], name: String = "folded") throws {
        let app = launch(extra)
        let any = app.descendants(matching: .any)
        let routed = any["exchange.routed"].firstMatch
        XCTAssertTrue(routed.waitForExistence(timeout: 20), "no Persona → task message:\n\(app.debugDescription)")
        XCTAssertTrue(has(app, "Persona → @补齐网络层的单元测试").exists, "no 「Persona → @task」 header")
        XCTAssertTrue(has(app, "补齐网络层的单元测试 → @Persona").exists, "no 「task → @Persona」 header")
        XCTAssertTrue(any["supervision-record"].firstMatch.exists, "the quiet round has no compact record")
        // Both the injected digest and the assistant's answer stay inside the unopened audit trace.
        XCTAssertFalse(has(app, "督办：2 个派出去的任务要处理").exists)
        XCTAssertFalse(has(app, "这是 Persona 的督办消息").exists)
        // No evidence list under the report: its second link is only in the list.
        XCTAssertFalse(has(app, "actions/runs/1001").exists, "the evidence list is still drawn")
        // Folded to three lines: Expand under both long exchanges (a cut Text still reads in full to accessibility).
        XCTAssertGreaterThanOrEqual(any.matching(identifier: "exchange.expand").count, 2, "the long exchanges are not folded")
        sleep(1)
        try shot(app, name)

        let expand = any.matching(identifier: "exchange.expand").allElementsBoundByIndex.last { $0.isHittable }
        XCTAssertNotNil(expand, "no Expand under a folded exchange")
        expand?.tap()
        XCTAssertTrue(any["exchange.collapse"].firstMatch.waitForExistence(timeout: 5), "Expand did not open it")
        sleep(1)
        try shot(app, name + "-expanded")
        app.terminate()
    }

    /// The old switch, fold mode and line count cannot change the fixed policy, including launch-argument values.
    @MainActor func testLegacyDisabledAndExpandedPreferencesAreIgnored() throws {
        try verifyExchanges(["-ui.newChatLayout", "NO", "-ui.newChatLayout.fold", "expanded",
                             "-ui.newChatLayout.lines", "10"], name: "legacy-expanded")
    }

    @MainActor func testLegacyFoldedAndOneLinePreferencesAreIgnored() throws {
        try verifyExchanges(["-ui.newChatLayout", "NO", "-ui.newChatLayout.fold", "folded",
                             "-ui.newChatLayout.lines", "1"], name: "legacy-folded")
    }

    /// A slide up the right edge picks an older message the user typed; letting go brings it into view.
    @MainActor func testEdgeSlideGoesToAMessageTheUserTyped() throws {
        let app = launch()
        let first = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "博客迁移到 Astro")).firstMatch
        XCTAssertTrue(app.descendants(matching: .any)["exchange.routed"].firstMatch.waitForExistence(timeout: 20))
        sleep(2)
        XCTAssertFalse(first.exists && first.isHittable, "the oldest message is on screen already")
        // From low on the right edge up to the top of the ticks: the oldest message the user typed.
        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.985, dy: 0.7))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.985, dy: 0.15))
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .slow, thenHoldForDuration: 0.5)
        XCTAssertTrue(first.waitForExistence(timeout: 10), "the slide did not bring the oldest message into view:\n\(app.debugDescription)")
        let probe = app.staticTexts["chat.scrollState"].firstMatch
        XCTAssertTrue(probe.exists && probe.label.contains("bottom=0"), "the list did not leave the newest end: \(probe.label)")
        sleep(1)
        try shot(app, "edge-landed")
        app.terminate()
    }

    @MainActor private func verifySettings(_ extra: [String] = [], name: String) throws {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "settings", "-tab", "settings",
                               "-dev.openChat", "", "-openChatsFresh", "YES",
                               "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"] + extra
        app.launchPinned()
        defer { app.terminate() }
        XCTAssertTrue(app.buttons["settings-color"].firstMatch.waitForExistence(timeout: 20), app.debugDescription)
        XCTAssertFalse(app.descendants(matching: .any)["settings-chat-layout"].firstMatch.exists)
        XCTAssertFalse(app.switches["chat-layout-toggle"].firstMatch.exists)
        XCTAssertFalse(has(app, "新对话布局").exists)
        XCTAssertFalse(has(app, "项目沟通").exists)
        sleep(1)
        try shot(app, name)
    }

    @MainActor func testSettingsHaveNoConversationLayoutEntry() throws {
        try verifySettings(name: "settings")
    }

    @MainActor func testRetiredSettingsDeepLinkDoesNotOpenAPage() throws {
        try verifySettings(["-openAppearance", "chat"], name: "settings-retired-link")
    }

    /// `-showEdgeScrub 3`: the navigator held open on the third newest message, for its screenshot.
    @MainActor func testEdgeNavigatorShot() throws {
        let app = launch(["-showEdgeScrub", "3"])
        let preview = app.descendants(matching: .any)["chat.edgeScrub.preview"].firstMatch
        XCTAssertTrue(preview.waitForExistence(timeout: 20), "the navigator did not open:\n\(app.debugDescription)")
        sleep(1)
        try shot(app, "edge-open")
        app.terminate()
    }
}
