import XCTest
import Vision

/// A real system action saved by build 1 and executed after build 2 overwrites the same app.
final class NativeStateUpgradeUITests: XCTestCase {
    private func requireUpgradeFixture() throws {
        try XCTSkipIf(ProcessInfo.processInfo.environment["PERSONA_NATIVE_UPGRADE"] != "1",
                      "Opt-in upgrade fixture: set TEST_RUNNER_PERSONA_NATIVE_UPGRADE=1 after installing the previous build.")
    }

    @MainActor private func capture(_ name: String) {
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    @MainActor private func visibleScreenText() throws -> String {
        let image = try XCTUnwrap(XCUIScreen.main.screenshot().image.cgImage)
        let request = VNRecognizeTextRequest()
        request.recognitionLevel = .accurate
        request.recognitionLanguages = ["en-US"]
        try VNImageRequestHandler(cgImage: image).perform([request])
        return (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }.joined(separator: " ")
    }

    @MainActor private func allowNotifications() {
        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        if board.alerts.firstMatch.waitForExistence(timeout: 3) {
            for label in ["允许", "Allow", "好", "OK"] where board.alerts.buttons[label].exists {
                board.alerts.buttons[label].tap()
            }
        }
    }

    @MainActor func testHandleUpgradeSystemAlerts() async throws {
        try requireUpgradeFixture()
        let app = XCUIApplication()
        app.launchArguments = ["-runtimeCLI", "19252", "-tab", "settings"]
        app.launchPinned()
        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let ready = URL(string: "http://127.0.0.1:19253/mock/ui-driver?ready=1")!
        let status = URL(string: "http://127.0.0.1:19253/mock/ui-driver")!
        _ = try await URLSession.shared.data(from: ready)
        let deadline = Date().addingTimeInterval(600)
        while Date() < deadline {
            if board.alerts.firstMatch.exists {
                for label in ["允许", "Allow", "好", "OK"] where board.alerts.buttons[label].exists {
                    board.alerts.buttons[label].tap()
                }
            }
            let (data, _) = try await URLSession.shared.data(from: status)
            let value = try JSONSerialization.jsonObject(with: data) as? [String: Bool]
            if value?["stop"] == true {
                capture("upgrade-system-alerts-handled")
                return
            }
            try await Task.sleep(for: .milliseconds(500))
        }
        XCTFail("The bounded upgrade driver did not receive its stop signal.")
    }

    @MainActor func testAcceptUpgradeNotificationPermission() throws {
        try requireUpgradeFixture()
        let app = XCUIApplication()
        app.launchArguments = ["-runtimeCLI", "19252", "-tab", "settings"]
        app.launchPinned()
        allowNotifications()
        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        XCTAssertFalse(board.alerts.firstMatch.exists, board.debugDescription)
        capture("upgraded-app-after-system-permission")
    }

    @MainActor func testCreateOldShortcut() throws {
        try requireUpgradeFixture()
        let app = XCUIApplication()
        if let link = ProcessInfo.processInfo.environment["PERSONA_UPGRADE_PAIR_URL"] {
            app.launchArguments = ["-pairURL", link]
        }
        app.launchPinned()
        allowNotifications()
        Thread.sleep(forTimeInterval: 5)
        let shortcuts = XCUIApplication(bundleIdentifier: "com.apple.shortcuts")
        shortcuts.launchPinned()
        allowNotifications()
        for label in ["继续", "Continue", "好", "OK"] where shortcuts.buttons[label].exists { shortcuts.buttons[label].tap() }
        if shortcuts.buttons["取消"].exists { shortcuts.buttons["取消"].tap() }
        let back = shortcuts.navigationBars.buttons["BackButton"].firstMatch
        if back.waitForExistence(timeout: 2) { back.tap() }
        let all = shortcuts.buttons.matching(NSPredicate(format: "label == '所有快捷指令' OR label == 'All Shortcuts'")).firstMatch
        if all.waitForExistence(timeout: 2), all.isHittable { all.tap() }
        let create = shortcuts.buttons.matching(NSPredicate(format: "identifier == 'main.button.newshortcut' OR identifier == 'CreateShortcut' OR label == '新建快捷指令' OR label == '创建快捷指令' OR label == 'Create Shortcut' OR label == 'New Shortcut'")).firstMatch
        XCTAssertTrue(create.waitForExistence(timeout: 15), shortcuts.debugDescription)
        create.tap()
        let add = shortcuts.buttons.matching(NSPredicate(format: "label == '添加操作' OR label == 'Add Action'")).firstMatch
        if add.waitForExistence(timeout: 5) { add.tap() }
        let search = shortcuts.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 10), shortcuts.debugDescription)
        search.tap()
        for label in ["继续", "Continue"] where shortcuts.buttons[label].exists { shortcuts.buttons[label].tap() }
        search.typeText("Persona")
        let ask = shortcuts.staticTexts.matching(NSPredicate(format: "label == '问 Persona' OR label == 'Ask Persona'")).firstMatch
        XCTAssertTrue(ask.waitForExistence(timeout: 20), shortcuts.debugDescription)
        capture("old-action-catalog")
        ask.tap()
        let questionButton = shortcuts.buttons["textInput"].firstMatch
        XCTAssertTrue(questionButton.waitForExistence(timeout: 5), shortcuts.debugDescription)
        questionButton.tap()
        let question = shortcuts.textViews.firstMatch
        XCTAssertTrue(question.waitForExistence(timeout: 5), shortcuts.debugDescription)
        question.typeText("Native migration compatibility fixture")
        let keyboardDone = shortcuts.keyboards.buttons["Done"].firstMatch
        if keyboardDone.waitForExistence(timeout: 2) { keyboardDone.tap() }
        capture("old-action-parameter")
        let done = shortcuts.buttons.matching(NSPredicate(format: "label == '完成' OR label == 'Done'")).firstMatch
        if done.waitForExistence(timeout: 2) { done.tap() }
        let exit = shortcuts.navigationBars.buttons["BackButton"].firstMatch
        XCTAssertTrue(exit.waitForExistence(timeout: 5), shortcuts.debugDescription)
        exit.tap()
        capture("old-action-saved")
    }

    @MainActor func testCaptureRetainedPersonalPreferences() throws {
        try requireUpgradeFixture()
        let app = XCUIApplication()
        app.launchArguments = ["-runtimeCLI", "19252", "-tab", "dev"]
        if let pair = ProcessInfo.processInfo.environment["PERSONA_UPGRADE_PAIR_URL"] {
            app.launchArguments += ["-pairURL", pair]
        }
        app.launchPinned()
        allowNotifications()
        let personal = app.buttons["agents.personal"]
        XCTAssertTrue(personal.waitForExistence(timeout: 30), "The upgrade fixture must provide configured accounts and a personal session. " + app.debugDescription)
        personal.tap()
        XCTAssertTrue(app.staticTexts["Upgrade preference fixture"].waitForExistence(timeout: 15), app.debugDescription)
        capture("retained-personal-agent-pin")
        let filter = app.buttons.matching(NSPredicate(format: "label == 'Filter and sort' OR label == '筛选与排序'")).firstMatch
        XCTAssertTrue(filter.waitForExistence(timeout: 5), app.debugDescription)
        filter.tap()
        capture("retained-personal-agent-view")
    }

    @MainActor func testRunSavedShortcut() throws {
        try requireUpgradeFixture()
        let shortcuts = XCUIApplication(bundleIdentifier: "com.apple.shortcuts")
        shortcuts.launchPinned()
        let back = shortcuts.navigationBars.buttons["BackButton"].firstMatch
        if back.waitForExistence(timeout: 3) { back.tap() }
        let all = shortcuts.staticTexts["所有快捷指令"].firstMatch
        if all.waitForExistence(timeout: 3), all.isHittable { all.tap() }
        let tile = shortcuts.buttons.matching(NSPredicate(format: "label CONTAINS 'Native migration fixture'")).firstMatch
        XCTAssertTrue(tile.waitForExistence(timeout: 10), shortcuts.debugDescription)
        tile.tap()
        // A restored library can open the editor instead of executing the tile; run the unchanged saved action there.
        let play = shortcuts.buttons.matching(NSPredicate(format: "label == '播放' OR label == '运行' OR label == 'Play' OR label == 'Run'")).firstMatch
        if play.waitForExistence(timeout: 3) {
            capture("old-action-recognized-after-upgrade")
            play.tap()
        }
        // Shortcuts renders Intent snippets in a separate system UI service, outside its application AX tree.
        let deadline = Date().addingTimeInterval(90)
        var visible = ""
        while Date() < deadline {
            visible = try visibleScreenText()
            if visible.contains("Native migration answer") { break }
            Thread.sleep(forTimeInterval: 0.5)
        }
        XCTAssertTrue(visible.contains("Native migration answer"), visible)
        XCTAssertTrue(visible.contains("Native migration compatibility fixture"), visible)
        XCTAssertFalse(shortcuts.staticTexts["未知操作"].exists)
        capture("old-shortcut-runs-after-upgrade")
    }
}
