import XCTest

/// UI version 3 on an extension's page (docs/extensions/extensions.md「UI 版本 3」): the mock's example-gateway › 「笔记」 page — rows with
/// an icon, an avatar and a thumbnail, its search field and filter chips, `more` at the end of the list, a row that pushes
/// the note's page (Markdown, a table), that page pushing its editor, saving popping back to a page drawn again, and
/// `ui.invalidate` drawing the list again with what synced in. Against scripts/mock/mock_hub.py with MOCK_EXTENSIONS=1:
/// `TEST_RUNNER_PERSONA_EXTENSIONS_HUB=http://127.0.0.1:<port>`, restarted for each run (the notes are kept in its memory);
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps the screenshots, named by `TEST_RUNNER_PERSONA_EXTENSIONS_APPEARANCE` and the style.
final class ExtensionV3Tests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }

    @MainActor private func launch(_ args: [String] = []) throws -> XCUIApplication {
        guard let hub = env["PERSONA_EXTENSIONS_HUB"] else { throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 (PERSONA_EXTENSIONS_HUB)") }
        addUIInterruptionMonitor(withDescription: "notifications") { alert in
            for label in ["允许", "Allow"] where alert.buttons[label].exists { alert.buttons[label].tap(); return true }
            return false
        }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings"] + args
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["允许", "Allow"] where springboard.buttons[label].waitForExistence(timeout: 3) { springboard.buttons[label].tap() }
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let look = env["PERSONA_EXTENSIONS_APPEARANCE"] ?? "light"
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "v3-\(name)-\(UITestStyle.name)-\(look).png"))
    }

    private func mock(_ path: String) {
        guard let hub = env["PERSONA_EXTENSIONS_HUB"], let url = URL(string: hub + path) else { return }
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: url) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 10)
    }

    @MainActor func testTheNotesPage() throws {
        let app = try launch(["-openExtension", "example-gateway", "-openExtensionPage", "notes"])
        let first = app.staticTexts["周会纪要"]
        XCTAssertTrue(first.waitForExistence(timeout: 15), "the list is not drawn:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["plugin-ui-filter-work"].exists, "the filter's chips")
        shot(app, "list")

        // The filter: 「生活」 only.
        app.buttons["plugin-ui-filter-home"].tap()
        XCTAssertTrue(app.staticTexts["周末采购"].waitForExistence(timeout: 10), "生活's notes:\n\(app.debugDescription)")
        XCTAssertFalse(app.staticTexts["周会纪要"].exists, "工作's are filtered out")
        app.buttons["plugin-ui-filter-all"].tap()
        XCTAssertTrue(first.waitForExistence(timeout: 10))

        // More: scrolled to the end, the next five come.
        let more = app.buttons["plugin-ui-more"]
        for _ in 0..<6 where !(more.exists && more.isHittable) { app.swipeUp() }
        XCTAssertTrue(app.staticTexts["季度目标"].waitForExistence(timeout: 10), "more loaded the next notes:\n\(app.debugDescription)")
        // And again: the More row of the answer loads too, drawn after the first load finished (#1937).
        let eleventh = app.staticTexts["体检报告"]
        for _ in 0..<8 where !eleventh.exists { app.swipeUp() }
        XCTAssertTrue(eleventh.waitForExistence(timeout: 10), "the second more:\n\(app.debugDescription)")
        let loading = app.descendants(matching: .any)["plugin-ui-loading"]
        for _ in 0..<4 where !loading.exists { app.swipeUp() }
        XCTAssertTrue(loading.exists, "the loading component at the end:\n\(app.debugDescription)")
        for _ in 0..<6 where !first.isHittable { app.swipeDown() }

        // The search: nothing found draws the empty state; its button clears the search.
        let search = app.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 5), "no search field:\n\(app.debugDescription)")
        search.tap()
        search.typeText("没有这种笔记")
        XCTAssertTrue(app.descendants(matching: .any)["plugin-ui-empty"].waitForExistence(timeout: 10), "the empty state:\n\(app.debugDescription)")
        shot(app, "empty")
        app.buttons["清除搜索"].tap()
        XCTAssertTrue(first.waitForExistence(timeout: 10), "cleared")
        XCTAssertEqual((search.value as? String) ?? "", search.placeholderValue ?? "", "the search field follows the View: empty again")

        // A row pushes the note's page: Markdown and a table.
        XCTAssertTrue(first.waitForExistence(timeout: 10))
        first.tap()
        XCTAssertTrue(app.descendants(matching: .any)["plugin-ui-table"].waitForExistence(timeout: 10), "the note's page:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["结论"].exists || app.descendants(matching: .any)["plugin-ui-markdown"].exists, "its Markdown")
        shot(app, "detail")

        // Edit pushes the editor; Save pops back to the note, drawn again with the new title.
        app.buttons["plugin-ui-action-edit"].tap()
        // The editor's own field: the first text field can be the app's composer.
        let title = app.textFields["plugin-ui-field-title"]
        XCTAssertTrue(title.waitForExistence(timeout: 10), "the editor:\n\(app.debugDescription)")
        title.tap()
        title.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 8) + "改过的纪要")
        app.buttons["plugin-ui-action-save"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["plugin-ui-table"].waitForExistence(timeout: 10), "popped back to the note:\n\(app.debugDescription)")
        XCTAssertTrue(app.navigationBars.staticTexts["改过的纪要"].waitForExistence(timeout: 10) || app.staticTexts["改过的纪要"].exists,
                      "the note's page drawn again:\n\(app.debugDescription)")

        // Back to the list, which shows the new title too; then the extension says its data changed.
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.staticTexts["改过的纪要"].waitForExistence(timeout: 10), "the list drawn again:\n\(app.debugDescription)")
        mock("/mock/invalidate")
        for _ in 0..<6 where !app.searchFields.firstMatch.isHittable { app.swipeDown() }
        XCTAssertTrue(app.staticTexts["刚同步的笔记"].waitForExistence(timeout: 10), "ui.invalidate drew the list again:\n\(app.debugDescription)")
        for _ in 0..<4 { app.swipeUp() }
        XCTAssertFalse(app.descendants(matching: .any)["plugin-ui-loading"].exists, "the sync is done")
        shot(app, "invalidated")
    }
}
