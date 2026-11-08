import XCTest

/// The ChatGPT-style shell on an iPhone (docs/apple/ios.md「新外壳」), in Demo Mode: no tab bar; Assistant | Agents; the drawer with
/// Inbox / Settings; the empty chat lists what needs the user; the composer grows while typing; Old Layout brings the tabs back.
final class ShellTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUp() {
        continueAfterFailure = false
    }

    /// `-newAssistant YES` (the empty chat) also picks the Persona tab: tests that open another page leave it out.
    @MainActor private func launch(_ extra: [String] = [], emptyChat: Bool = true) {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-ui.oldLayout", "NO", "-probes", "YES", "-dev.openChat", ""] + (emptyChat ? ["-newAssistant", "YES"] : []) + extra
        app.launchPinned()
    }

    @MainActor @discardableResult private func see(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 10) -> XCUIElement {
        let found = e.waitForExistence(timeout: timeout)
        if !found {
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.lifetime = .keepAlways
            add(shot)
            print("SHELLTREE-BEGIN\n\(app.debugDescription)\nSHELLTREE-END")
        }
        XCTAssertTrue(found, "no \(what)")
        return e
    }

    @MainActor private func button(_ id: String) -> XCUIElement { app.buttons[id].firstMatch }

    @MainActor private var oldLayoutSwitch: XCUIElement {
        app.switches.matching(NSPredicate(format: "identifier == 'old-layout' OR label == '旧布局'")).firstMatch
    }

    @MainActor func testSwitchBetweenChatAndWork() {
        launch()
        see(button("shell-assistant"), "the 助手 | Agents switch", timeout: 15)
        XCTAssertFalse(app.tabBars.firstMatch.exists, "the tab bar in the shell")
        // The empty chat may show only agents; the overflow still reaches the Inbox actions.
        see(button("shell-needs-agent"), "an agent waiting, over the composer")
        if button("shell-needs-inbox").exists {
            see(button("shell-needs-inbox"), "an Inbox action over the composer")
        } else {
            see(button("shell-needs-more"), "the remaining needs-you actions").tap()
            let inbox = see(app.navigationBars["收件箱"], "Inbox reached from the needs-you overflow")
            sleep(1)
            inbox.buttons["BackButton"].tap()
            XCTAssertTrue(inbox.waitForNonExistence(timeout: 5), "back from the overflow's Inbox")
            see(button("shell-assistant"), "the switch after returning from Inbox")
        }
        let finger = ShellFinger(app)
        finger.switchTo(agents: true, "Agents tapped on the empty chat")
        see(button("agents-new"), "新建会话 on Agents")
        // The search starts folded away (2026-11-01 user request, 9de38a9) and a pull brings it back.
        sleep(1)
        let field = app.searchFields.firstMatch
        XCTAssertFalse(field.exists && field.isHittable, "the agents' search is out at first, not folded away")
        app.revealAgentsSearch()
        finger.switchTo(agents: false, "助手 tapped with Agents' search out")
        see(button("assistant-new"), "新对话 on 助手")
    }

    @MainActor func testDrawerOpensTheInboxAndSettings() {
        launch()
        see(button("assistant-sidebar-button"), "☰", timeout: 15).tapCenter()
        see(button("sidebar-inbox"), "收件箱 in the drawer").tap()
        see(app.navigationBars["收件箱"], "the inbox pushed on 助手")
        // Back once the push has settled (a tap during the push's animation can be lost).
        let inbox = app.navigationBars["收件箱"]
        for _ in 0..<2 where inbox.exists {
            sleep(1)
            inbox.buttons["BackButton"].tap()
            _ = inbox.waitForNonExistence(timeout: 3)
        }
        XCTAssertFalse(inbox.exists, "still on 收件箱 after back")
        see(button("assistant-sidebar-button"), "☰ again").tapCenter()
        // The drawer's hosting view names every element in it "assistant-sidebar": find Settings by its label.
        see(app.buttons.matching(NSPredicate(format: "label == '设置'")).firstMatch, "设置 at the drawer's foot").tap()
        see(button("settings-close"), "设置's ×").tap()
        XCTAssertTrue(button("shell-assistant").waitForExistence(timeout: 5), "back on 助手 after closing 设置")
    }

    /// Machines in the drawer: the one machines page (agents and monitoring together, docs/server/machines.md「App」), and a card
    /// opens the machine with its agents and charts on one page.
    @MainActor func testDrawerOpensMachines() {
        launch()
        see(button("assistant-sidebar-button"), "☰", timeout: 15).tapCenter()
        see(button("sidebar-machines"), "机器 in the drawer").tap()
        see(app.navigationBars["机器"], "机器 pushed on 助手")
        // The token summary repeats the name; only the machine card is a navigation button.
        let server = see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "服务器、")).firstMatch,
                         "the server's machine card")
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "个 Agent 工作中")).firstMatch.exists,
                      "the agents counted on the card")
        server.tap()
        see(app.descendants(matching: .any)["metric-range"].firstMatch, "the machine's charts on its page")
        XCTAssertTrue(app.navigationBars["服务器"].exists)
    }

    /// The drawer's rows keep their size however often it comes out. The shell's "a size up" (`shellScaled`) is one
    /// Dynamic Type step on the app's text size; the drawer lives in a hosting controller whose environment took that
    /// step back in on every refresh (Inbox's row 59 → 78 → 80 pt after a few trips to Inbox and back).
    @MainActor private func inboxRowHeight(_ entry: Int, _ via: String) -> CGFloat {
        let row = see(button("sidebar-inbox"), "收件箱 in the drawer, entry \(entry) \(via)")
        sleep(1)
        print("SIDEBAR-ROW-HEIGHT \(via) entry \(entry): \(row.frame.height)")
        return row.frame.height
    }

    @MainActor func testDrawerRowsKeepTheirSizeWhenReentered() {
        launch()
        let toggle = see(button("assistant-sidebar-button"), "☰", timeout: 15)
        var heights: [CGFloat] = []
        // From Assistant: out, push Inbox, back, out again.
        for entry in 1...5 {
            toggle.tapCenter()
            heights.append(inboxRowHeight(entry, "from 助手"))
            button("sidebar-inbox").tap()
            let inbox = see(app.navigationBars["收件箱"], "the inbox pushed")
            sleep(2)
            inbox.buttons["BackButton"].tap()
            see(toggle, "☰ again after the inbox")
        }
        // From Agents' avatar: out and closed with a tap beside it.
        see(button("shell-agents"), "Agents").tap()
        let work = see(button("work-sidebar-button"), "Agents' avatar")
        for entry in 1...3 {
            work.tapCenter()
            heights.append(inboxRowHeight(entry, "from Agents"))
            let row = button("sidebar-inbox")
            app.descendants(matching: .any)["assistant-drawer-close"].firstMatch.tap()
            let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: row)
            XCTAssertEqual(XCTWaiter.wait(for: [gone], timeout: 5), .completed, "the drawer did not close, entry \(entry)")
        }
        for (i, h) in heights.enumerated() {
            XCTAssertEqual(h, heights[0], accuracy: 0.5, "收件箱's height on entry \(i + 1) differs from the first: \(heights)")
        }
    }

    @MainActor func testComposerGrowsWhileTyping() {
        launch()
        let field = see(app.textViews["shell-composer-field"].firstMatch.exists ? app.textViews["shell-composer-field"].firstMatch
                        : app.textFields["shell-composer-field"].firstMatch, "the composer", timeout: 15)
        XCTAssertFalse(button("shell-composer-modes").exists, "the gauge before typing (one line)")
        field.tap()
        see(button("shell-composer-modes"), "the gauge once the composer opens")
        field.typeText("你好")
        see(button("shell-composer-send"), "send once there is something to send")
    }

    /// The keyboard goes away with a tap on the page above the composer, and the composer, with what was typed, stays.
    @MainActor func testTapOnThePageClosesTheKeyboard() {
        launch()
        let field = see(app.textViews["shell-composer-field"].firstMatch.exists ? app.textViews["shell-composer-field"].firstMatch
                        : app.textFields["shell-composer-field"].firstMatch, "the composer", timeout: 15)
        field.tap()
        field.typeText("你好")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "the keyboard after tapping the composer")
        field.tap()
        XCTAssertTrue(app.keyboards.firstMatch.exists, "a tap on the field itself keeps the keyboard")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)).tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "the keyboard still up after a tap on the page")
        see(button("shell-composer-send"), "the draft and its send button after the keyboard closed")
    }

    @MainActor func testOldLayoutBringsTheTabsBack() {
        launch(["-tab", "settings", "-openAppearance", "layout"], emptyChat: false)
        let toggle = see(oldLayoutSwitch, "旧布局 in 设置 › 外观 › 布局", timeout: 15)
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
        see(app.tabBars.firstMatch, "the tab bar with 旧布局 on")
        let back = see(oldLayoutSwitch, "旧布局, still on 布局")
        back.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
        // Back in the shell, Settings is a sheet again, still on Layout (its own pages have a back button; × is on its root).
        see(app.navigationBars["布局"], "the shell's 设置 sheet on 布局 with 旧布局 off")
        XCTAssertFalse(app.tabBars.firstMatch.exists, "a tab bar after turning 旧布局 off")
        app.navigationBars["布局"].buttons["BackButton"].tap()
        see(button("settings-close"), "设置's ×").tap()
        see(button("shell-assistant"), "助手 after closing 设置")
    }
}
