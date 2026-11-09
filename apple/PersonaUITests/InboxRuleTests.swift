import XCTest

/// Rules from an inbox row's long-press menu and an item's More menu (docs/assistant/inbox.md "Policies"): the menus
/// offer a rule for each thing the item can be muted by, after the mute options; the sheet saves one at the hub and
/// says so; More › Rules lists them. Against scripts/mock/mock_hub.py with MOCK_GITHUB_INBOX=1 MOCK_POLICIES=1
/// (PERSONA_MOCK_HUB), which keeps the rules at GET /api/v1/inbox/policies.
final class InboxRuleTests: XCTestCase {
    @MainActor func testRuleFromAnItemsMenus() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", ""]
        app.launchPinned()
        /// Attached to the run; with TEST_RUNNER_PERSONA_SHOTS=<dir> also saved there.
        func shot(_ name: String) {
            let screen = app.screenshot()
            let a = XCTAttachment(screenshot: screen)
            a.name = "\(name) \(UITestStyle.name)"
            a.lifetime = .keepAlways
            add(a)
            if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
                let file = "\(name) \(UITestStyle.name)".replacingOccurrences(of: " ", with: "-") + ".png"
                try? screen.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
            }
        }
        func text(_ s: String) -> XCUIElement {
            app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
        }
        func menuButton(_ s: String) -> XCUIElement {
            app.buttons.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
        }
        func reveal(_ el: XCUIElement) {
            for _ in 0..<8 where !(el.exists && el.isHittable) {
                app.swipeUp()
            }
        }
        func closeMenu() {
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.06)).tap()
            sleep(1)
        }

        XCTAssertTrue(text("韩梅梅").waitForExistence(timeout: 20), "the inbox did not load")
        let more = app.buttons["inbox.more"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 5), "no 更多")
        more.tap()
        XCTAssertTrue(menuButton("规则").waitForExistence(timeout: 5), "no 规则 in 更多:\n\(app.debugDescription)")
        XCTAssertTrue(menuButton("已静音").exists, "no 已静音 in 更多")
        sleep(1)
        shot("inbox rule more")
        closeMenu()

        let group = text("周末徒步群")
        reveal(group)
        group.press(forDuration: 1.2)
        XCTAssertTrue(menuButton("为这个会话设规则").waitForExistence(timeout: 5), "no 为这个会话设规则:\n\(app.debugDescription)")
        XCTAssertTrue(menuButton("为这个发件人设规则").exists, "no 为这个发件人设规则")
        XCTAssertTrue(menuButton("静音这个会话").exists, "no 静音这个会话")
        sleep(1)
        shot("inbox rule menu")
        closeMenu()

        // The item's page, opened directly: a tap just after a context menu closes can be swallowed.
        app.terminate()
        app.launchArguments += ["-openInboxItem", "lark:f3:300"]
        app.launchPinned()
        let detailMore = app.buttons["inbox.detail.more"].firstMatch
        XCTAssertTrue(detailMore.waitForExistence(timeout: 20), "no 更多 on the item:\n\(app.debugDescription)")
        sleep(1)
        shot("inbox rule detail")
        detailMore.tap()
        let rule = menuButton("为这个会话设规则")
        XCTAssertTrue(rule.waitForExistence(timeout: 5), "no 为这个会话设规则 on the item:\n\(app.debugDescription)")
        sleep(1)
        shot("inbox rule detail menu")
        rule.tap()

        XCTAssertTrue(text("周末徒步群 的规则").waitForExistence(timeout: 5), "no rule sheet:\n\(app.debugDescription)")
        let save = app.buttons["inbox.policy.save"].firstMatch
        XCTAssertTrue(save.waitForExistence(timeout: 5), "no 保存")
        XCTAssertFalse(save.isEnabled, "保存 is on for a new rule that sets nothing")
        menuButton("始终需要你").tap()
        XCTAssertTrue(save.isEnabled, "保存 is still off")
        sleep(1)
        shot("inbox rule sheet")
        save.tap()
        XCTAssertTrue(text("已为“周末徒步群”保存规则").waitForExistence(timeout: 5), "no toast:\n\(app.debugDescription)")
        let policies = try String(contentsOf: URL(string: hub + "/api/v1/inbox/policies")!, encoding: .utf8)
        XCTAssertTrue(policies.contains(#""value": "f3""#) && policies.contains(#""tier": "action""#), "the rule did not reach the hub: \(policies)")

        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", "", "-showInboxRules", "YES"]
        app.launchPinned()
        XCTAssertTrue(text("王经理").waitForExistence(timeout: 20), "no 规则 list:\n\(app.debugDescription)")
        XCTAssertTrue(text("飞书 · 会话 · 始终需要你").exists, "no new rule in the list")
        XCTAssertTrue(text("邮件 · 发件人 · 始终需要你 · 分身优先").exists, "no source, kind and parts")
        sleep(1)
        shot("inbox rules")
    }
}
