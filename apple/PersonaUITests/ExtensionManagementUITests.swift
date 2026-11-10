import XCTest

/// Management and declared contributions in the real app; uses only the dedicated mock, every write is recorded in memory.
final class ExtensionManagementUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var hub: String { get throws {
        guard let value = env["PERSONA_EXTENSIONS_HUB"] else { throw XCTSkip("需要 MOCK_EXTENSION_MANAGEMENT=1 的专用 mock") }
        return value
    } }

    private func recordedWrites() async throws -> [[String: Any]] {
        let result = try await state()
        return try XCTUnwrap(result["writes"] as? [[String: Any]])
    }

    private func state(reset: [String: Bool]? = nil) async throws -> [String: Any] {
        var request = URLRequest(url: URL(string: try hub + "/mock/extension-management")!)
        if let reset {
            request.httpMethod = "POST"
            request.httpBody = try JSONSerialization.data(withJSONObject: reset)
        }
        let (data, response) = try await URLSession.shared.data(for: request)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
        return try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    @MainActor private func launch(_ arguments: [String]) throws -> XCUIApplication {
        continueAfterFailure = false
        let pair = "persona://pair?url=" + (try hub).addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-ui.color", "style"] + arguments
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["允许", "Allow"] where springboard.buttons[label].waitForExistence(timeout: 2) { springboard.buttons[label].tap() }
        return app
    }

    @MainActor private func show(_ element: XCUIElement, app: XCUIApplication, up: Bool = true) {
        for _ in 0..<7 where !(element.exists && element.isHittable) {
            if up { app.swipeUp() } else { app.swipeDown() }
        }
        XCTAssertTrue(element.exists && element.isHittable, app.debugDescription)
    }

    @MainActor private func toggle(_ element: XCUIElement) {
        element.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.5)).tap()
    }

    @MainActor private func value(_ expected: String, of element: XCUIElement) {
        let wait = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value == %@", expected), object: element)
        XCTAssertEqual(XCTWaiter.wait(for: [wait], timeout: 5), .completed,
                       "预期：\(expected)；当前：\(String(describing: element.value))")
    }

    @MainActor private func cancel(_ app: XCUIApplication) {
        let button = app.buttons["取消"].firstMatch
        if button.exists && button.isHittable { button.tap() }
        else { app.coordinate(withNormalizedOffset: CGVector(dx: 0.05, dy: 0.25)).tap() }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        guard let dir = env["PERSONA_SHOTS"] else { return }
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir)
            .appending(path: "management-\(name)-\(UITestStyle.name)-\(env["PERSONA_EXTENSIONS_APPEARANCE"] ?? "light").png"))
    }

    @MainActor func testContributionKeepsDraftAndCascadeNeedsConfirmation() async throws {
        _ = try await state(reset: [:])
        let app = try launch(["-openExtension", "sample-kit"])
        // By its own identifier: Settings is the shell's sheet, and the Assistant's composer under it comes first (#3072).
        let title = app.textFields["plugin-field-title"]
        XCTAssertTrue(title.waitForExistence(timeout: 15), app.debugDescription)
        title.tap()
        title.typeText("未保存\n")
        let draft = try XCTUnwrap(title.value as? String)
        let contribution = app.switches["extension-contribution-ui/home"]
        show(contribution, app: app)
        toggle(contribution)
        value("0", of: contribution)
        let connector = app.switches["extension-contribution-connectors/source"]
        show(connector, app: app)
        XCTAssertFalse(connector.isEnabled)
        try shot(app, "contributions")
        let unchanged = try await state()
        XCTAssertEqual((unchanged["kit"] as? [String: Any])?["config"].flatMap { $0 as? [String: Any] }?["title"] as? String, "初始标题")
        let enabled = app.switches["plugin-enabled"]
        show(enabled, app: app, up: false)
        toggle(enabled)
        let cascade = app.buttons["extension-cascade-confirm"].firstMatch
        XCTAssertTrue(cascade.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "示例依赖者")).firstMatch.exists)
        try shot(app, "cascade")
        cancel(app)
        XCTAssertEqual(enabled.value as? String, "1")
        toggle(enabled)
        XCTAssertTrue(cascade.waitForExistence(timeout: 5))
        cascade.tap()
        value("0", of: enabled)
        show(title, app: app)
        XCTAssertEqual(title.value as? String, draft)
        let writes = try await recordedWrites()
        let enables = writes.filter { ($0["path"] as? String)?.hasSuffix("/enabled") == true }
        XCTAssertEqual(enables.count, 3)
        XCTAssertNil((enables.first?["body"] as? [String: Any])?["cascade"])
        XCTAssertEqual((enables.last?["body"] as? [String: Any])?["cascade"] as? Bool, true)
        XCTAssertFalse(writes.contains { ($0["path"] as? String)?.hasSuffix("/config") == true })
        try shot(app, "draft-kept")
    }

    @MainActor func testTemplateOnlyReplacesDraftAfterConfirmation() async throws {
        _ = try await state(reset: [:])
        let app = try launch(["-openExtension", "sample-kit"])
        let menu = app.buttons["从模板填"]
        XCTAssertTrue(menu.waitForExistence(timeout: 15), app.debugDescription)
        show(menu, app: app)
        menu.tap()
        app.buttons["重要事项优先"].tap()
        let replace = app.buttons["extension-triage-template-confirm"].firstMatch
        XCTAssertTrue(replace.waitForExistence(timeout: 5))
        try shot(app, "template-confirm")
        cancel(app)
        XCTAssertFalse(app.textFields.matching(NSPredicate(format: "value == %@", "只保留需要行动的重要事项")).firstMatch.exists)
        menu.tap()
        app.buttons["重要事项优先"].tap()
        XCTAssertTrue(replace.waitForExistence(timeout: 5))
        replace.tap()
        XCTAssertTrue(app.textFields.matching(NSPredicate(format: "value == %@", "只保留需要行动的重要事项")).firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["plugin-save"].isEnabled)
        let writes = try await recordedWrites()
        XCTAssertTrue(writes.isEmpty)
        try shot(app, "template-draft")
    }

    @MainActor func testBundleDisplaysEveryPermissionAndSendsEveryDigest() async throws {
        _ = try await state(reset: ["uninstalled": true])
        let app = try launch(["-openMarketExtension", "3f2a9c1d0b/sample-pack"])
        let install = app.buttons["market-install"]
        XCTAssertTrue(install.waitForExistence(timeout: 15), app.debugDescription)
        install.tap()
        let confirm = app.buttons["market-confirm"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 10), app.debugDescription)
        show(app.staticTexts["工具包访问示例网络"], app: app)
        try shot(app, "bundle-first")
        show(app.staticTexts["补充工具读取示例文件"], app: app)
        try shot(app, "bundle-second")
        let before = try await recordedWrites()
        XCTAssertFalse(before.contains { ($0["path"] as? String)?.hasSuffix("/-/install") == true })
        confirm.tap()
        XCTAssertTrue(app.buttons["market-uninstall"].waitForExistence(timeout: 10), app.debugDescription)
        let writes = try await recordedWrites()
        let body = try XCTUnwrap(writes.last?["body"] as? [String: Any])
        let bundle = try XCTUnwrap(body["bundle"] as? [[String: String]])
        XCTAssertEqual(bundle.map { $0["preview_id"] }, ["preview-sample-kit", "preview-sample-extra"])
        XCTAssertEqual(bundle.map { $0["permissions_digest"] }, ["digest-sample-kit", "digest-sample-extra"])
        try shot(app, "bundle-installed")
    }

    @MainActor func testPreviewExpiresWhileSheetIsOpen() async throws {
        _ = try await state(reset: ["uninstalled": true, "expiresSoon": true])
        let app = try launch(["-openMarketExtension", "3f2a9c1d0b/sample-pack"])
        XCTAssertTrue(app.buttons["market-install"].waitForExistence(timeout: 15))
        app.buttons["market-install"].tap()
        let confirm = app.buttons["market-confirm"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 10))
        XCTAssertTrue(confirm.isEnabled)
        let expires = XCTNSPredicateExpectation(predicate: NSPredicate(format: "enabled == false"), object: confirm)
        XCTAssertEqual(XCTWaiter.wait(for: [expires], timeout: 35), .completed)
        let problem = app.staticTexts["market-preview-problem"]
        show(problem, app: app)
        XCTAssertTrue(problem.exists)
        let writes = try await recordedWrites()
        XCTAssertFalse(writes.contains { ($0["path"] as? String)?.hasSuffix("/-/install") == true })
        try shot(app, "expired-preview")
    }

    @MainActor func testUnresolvedPreviewCannotInstall() async throws {
        _ = try await state(reset: [:])
        let app = try launch(["-openMarketExtension", "3f2a9c1d0b/sample-unresolved"])
        XCTAssertTrue(app.buttons["market-install"].waitForExistence(timeout: 15), app.debugDescription)
        app.buttons["market-install"].tap()
        let confirm = app.buttons["market-confirm"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertFalse(confirm.isEnabled)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "missing-source")).firstMatch.exists)
        try shot(app, "unresolved")
        let writes = try await recordedWrites()
        XCTAssertFalse(writes.contains { ($0["path"] as? String)?.hasSuffix("/-/install") == true })
    }

    @MainActor func testPackRemovalConfirmsCascadeAndDoesNotOverstateMembers() async throws {
        _ = try await state(reset: [:])
        let app = try launch(["-openMarketExtension", "3f2a9c1d0b/sample-pack"])
        let remove = app.buttons["market-uninstall"]
        XCTAssertTrue(remove.waitForExistence(timeout: 15), app.debugDescription)
        remove.tap()
        let members = app.buttons["market-uninstall-members"].firstMatch
        XCTAssertTrue(members.waitForExistence(timeout: 5), app.debugDescription)
        try shot(app, "pack-remove-choice")
        members.tap()
        let cascade = app.buttons["market-uninstall-cascade"].firstMatch
        XCTAssertTrue(cascade.waitForExistence(timeout: 5), app.debugDescription)
        try shot(app, "pack-remove-cascade")
        cascade.tap()
        XCTAssertTrue(app.buttons["market-install"].waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.staticTexts["合集已卸载；成功卸载 1 个成员"].waitForExistence(timeout: 5), app.debugDescription)
        let result = try await state()
        XCTAssertNotNil(result["extra"] as? [String: Any])
        XCTAssertEqual((result["child"] as? [String: Any])?["enabled"] as? Bool, false)
        let writes = try XCTUnwrap(result["writes"] as? [[String: Any]])
        XCTAssertEqual((writes.last?["query"] as? [String: [String]])?["cascade"], ["1"])
        XCTAssertEqual((writes.last?["query"] as? [String: [String]])?["members"], ["1"])
        try shot(app, "pack-partial-result")
    }

    @MainActor func testPromptOnlyFillsTheComposer() async throws {
        _ = try await state(reset: [:])
        let app = try launch(["-tab", "assistant", "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"])
        let attach = app.buttons["shell-composer-add"]
        XCTAssertTrue(attach.waitForExistence(timeout: 15), app.debugDescription)
        attach.tap()
        // The iPhone's + is a panel (AttachSheet): extension prompts are in its More tab.
        let more = app.buttons["attach-tab-more"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 5), app.debugDescription)
        more.tap()
        let prompt = app.buttons["整理示例周报"].firstMatch
        XCTAssertTrue(prompt.waitForExistence(timeout: 5), app.debugDescription)
        try shot(app, "prompt-menu")
        prompt.tap()
        let filled = app.descendants(matching: .any).matching(NSPredicate(format: "value == %@", "整理本周的进展，先列需要我决定的事项。")).firstMatch
        XCTAssertTrue(filled.waitForExistence(timeout: 5), app.debugDescription)
        let writes = try await recordedWrites()
        XCTAssertFalse(writes.contains { ($0["path"] as? String)?.contains("/send") == true })
        try shot(app, "prompt-draft")
    }

    @MainActor func testRemoteThemeFallbackRebuildKeepsUnsavedForm() async throws {
        _ = try await state(reset: [:])
        let app = try launch(["-openAppearance", "color"])
        let theme = app.buttons["extension-theme-sample-kit/blue"]
        XCTAssertTrue(theme.waitForExistence(timeout: 15), app.debugDescription)
        show(theme, app: app)
        theme.tap()
        value("已选择", of: theme)
        app.navigationBars.buttons.firstMatch.tap()
        let extensions = app.buttons["settings-plugins"]
        show(extensions, app: app)
        extensions.tap()
        let kit = app.buttons["plugin-sample-kit"]
        XCTAssertTrue(kit.waitForExistence(timeout: 10), app.debugDescription)
        show(kit, app: app)
        kit.tap()
        let title = app.textFields["plugin-field-title"]
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        show(title, app: app)
        let original = try XCTUnwrap(title.value as? String)
        for _ in 0..<3 where !app.keyboards.firstMatch.exists {
            title.tap()
            _ = app.keyboards.firstMatch.waitForExistence(timeout: 3)
        }
        XCTAssertTrue(app.keyboards.firstMatch.exists, "输入草稿前必须等键盘出现")
        app.typeText("尚未保存\n")
        let draft = original + "尚未保存"
        value(draft, of: title)
        let before = try await state()
        let reads = try XCTUnwrap(before["detail_reads"] as? Int)
        var request = URLRequest(url: URL(string: try hub + "/api/v1/extensions/sample-kit/-/contributions/themes/blue")!)
        request.httpMethod = "PUT"
        request.httpBody = Data(#"{"enabled":false}"#.utf8)
        _ = try await URLSession.shared.data(for: request)
        let end = Date.now.addingTimeInterval(80)
        var rebuilt = false
        while Date.now < end {
            let current = try await state()
            if (current["detail_reads"] as? Int ?? 0) > reads { rebuilt = true; break }
            try await Task.sleep(for: .seconds(1))
        }
        XCTAssertTrue(rebuilt, "远程停用后必须观察到外观回退引起的真实页面重建")
        XCTAssertTrue(title.waitForExistence(timeout: 5))
        value(draft, of: title)
        XCTAssertTrue(app.buttons["plugin-save"].isEnabled)
        try shot(app, "fallback-keeps-form")
    }

    @MainActor func testThemeIsOptInAndDisappearingContributionFallsBack() async throws {
        _ = try await state(reset: [:])
        let app = try launch(["-openAppearance", "color"])
        let theme = app.buttons["extension-theme-sample-kit/blue"]
        XCTAssertTrue(theme.waitForExistence(timeout: 15), app.debugDescription)
        show(theme, app: app)
        XCTAssertEqual(theme.value as? String, "未选择")
        XCTAssertFalse(app.buttons["extension-theme-sample-kit/bad"].exists)
        try shot(app, "theme-unselected")
        theme.tap()
        XCTAssertTrue(theme.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertEqual(theme.value as? String, "已选择")
        try shot(app, "theme-selected")
        var request = URLRequest(url: URL(string: try hub + "/api/v1/extensions/sample-kit/-/contributions/themes/blue")!)
        request.httpMethod = "PUT"
        request.httpBody = Data(#"{"enabled":false}"#.utf8)
        _ = try await URLSession.shared.data(for: request)
        XCTAssertTrue(theme.waitForNonExistence(timeout: 75), app.debugDescription)
        let fallback = app.buttons["color-style"]
        show(fallback, app: app, up: false)
        XCTAssertTrue(fallback.isSelected, app.debugDescription)
        try shot(app, "theme-fallback")
    }
}
