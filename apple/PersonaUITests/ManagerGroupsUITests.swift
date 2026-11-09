import XCTest

/// Settings › Extensions and Connectors with several managers (#1827, docs/assistant/managers.md "Grouping"): each manager's items
/// under its name, the offline one's heading saying when it last synced, another manager's row opening its own copy, and with
/// one manager no heading at all. Against scripts/mock/mock_hub.py with MOCK_EXTENSIONS=1 (three managers by default):
/// `TEST_RUNNER_PERSONA_MANAGERS_HUB=http://127.0.0.1:<port>`; with MOCK_MANAGERS=1 add `TEST_RUNNER_PERSONA_MANAGERS_ONE=1`.
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps the screenshots, named by `TEST_RUNNER_PERSONA_SHOTS_APPEARANCE` (light / dark: set with
/// `simctl ui … appearance`).
final class ManagerGroupsUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var one: Bool { env["PERSONA_MANAGERS_ONE"] == "1" }

    private func launch(_ hub: String, _ args: [String] = []) -> XCUIApplication {
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings"] + args
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["允许", "Allow"] where springboard.buttons[label].waitForExistence(timeout: 2) { springboard.buttons[label].tap() }
        return app
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    private func headings(_ app: XCUIApplication) -> XCUIElementQuery {
        app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH %@", "manager-heading-"))
    }

    private func reveal(_ app: XCUIApplication, _ e: XCUIElement, down: Bool = false) {
        for _ in 0..<10 where !(e.exists && e.isHittable) { if down { app.swipeDown() } else { app.swipeUp() } }
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        let png = app.screenshot()
        let a = XCTAttachment(screenshot: png)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let look = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        try? png.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "\(name)-\(look).png"))
    }

    @MainActor func testExtensionsGroupByManager() throws {
        guard let hub = env["PERSONA_MANAGERS_HUB"] else { throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 (PERSONA_MANAGERS_HUB)") }
        let app = launch(hub, ["-openExtensions", "YES"])
        let own = element(app, "plugin-telegram")
        XCTAssertTrue(own.waitForExistence(timeout: 15), "no extensions:\n\(app.debugDescription)")
        if one {
            XCTAssertEqual(headings(app).count, 0, "one manager: no headings")
            shot(app, "extensions-one-manager")
            return
        }
        XCTAssertTrue(element(app, "manager-heading-assistant").exists && element(app, "manager-heading-m-3fa21c").exists)
        shot(app, "extensions-grouped")
        // 家里 is offline: its heading says when it last synced.
        let home = element(app, "manager-heading-m-8b04d2")
        reveal(app, home)
        XCTAssertTrue(home.label.contains("离线"), home.label)
        shot(app, "extensions-grouped-offline")

        // 工作's Telegram is its own row, opening its own copy (every request of the page names 工作).
        let work = element(app, "plugin-m-3fa21c-telegram")
        reveal(app, work, down: true)
        XCTAssertTrue(work.exists && element(app, "plugin-m-8b04d2-telegram").exists, "each manager's copy is a row of its own")
        work.tap()
        XCTAssertTrue(app.switches["plugin-enabled"].waitForExistence(timeout: 10), "no extension page:\n\(app.debugDescription)")
        shot(app, "extension-detail-other-manager")
    }

    @MainActor func testConnectorsGroupByManager() throws {
        guard let hub = env["PERSONA_MANAGERS_HUB"] else { throw XCTSkip("needs mock_hub.py (PERSONA_MANAGERS_HUB)") }
        let app = launch(hub)
        XCTAssertTrue(element(app, "settings-list").waitForExistence(timeout: 15), "no settings:\n\(app.debugDescription)")
        let bot = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "飞书机器人")).firstMatch
        reveal(app, bot)
        if one {
            XCTAssertTrue(bot.exists, "no Lark Bot row:\n\(app.debugDescription)")
            XCTAssertEqual(headings(app).count, 0, "one manager: no headings")
            // The section's heading dragged slowly (no momentum) to a fixed place: the shot compares with another build's.
            let heading = app.staticTexts["连接器"].firstMatch
            if heading.exists {
                let from = heading.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
                let to = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: from.screenPoint.x, dy: 200))
                from.press(forDuration: 0.05, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.5)
            }
            shot(app, "connectors-one-manager")
            return
        }
        // The mock's 家里 (offline) has GitHub: its own section under its name.
        let home = element(app, "manager-heading-m-8b04d2")
        reveal(app, home)
        XCTAssertTrue(home.exists && element(app, "manager-heading-assistant").exists, "no manager headings:\n\(app.debugDescription)")
        shot(app, "connectors-grouped")
    }
}
