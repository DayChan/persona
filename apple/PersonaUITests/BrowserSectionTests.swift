import XCTest

/// Browser (ego lite) on a Mac's page (docs/plans/plan-ego-browser.md), against scripts/mock/mock_hub.py with
/// MOCK_BROWSER=<state> (`TEST_RUNNER_PERSONA_BROWSER_HUB`, `TEST_RUNNER_PERSONA_BROWSER_STATE`): not installed it installs,
/// installed its first-run setup is steps with Open ego lite, set up it is Ready in green. PERSONA_SHOTS keeps the
/// screenshots, named by `TEST_RUNNER_PERSONA_LOOK`.
final class BrowserSectionTests: XCTestCase {
    private var state: String { ProcessInfo.processInfo.environment["PERSONA_BROWSER_STATE"] ?? "ready" }

    @MainActor private func open(_ args: [String] = ["-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "macbook"])
        throws -> (XCUIApplication, XCUIElement) {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_BROWSER_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_BROWSER (PERSONA_BROWSER_HUB)")
        }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=" + UITestToken.device
        app.launchArguments = ["-pairURL", pair, "-openScreenFresh", "YES"] + args // no Mac sign-in kept from another test
        app.launchPinned()
        let toggle = app.switches["browser-switch"].firstMatch
        if args.contains("-openMachineDetail") {
            XCTAssertTrue(toggle.waitForExistence(timeout: 30), "no 浏览器 switch:\n\(app.debugDescription)")
            for _ in 0..<8 where !toggle.isHittable || toggle.frame.maxY > app.frame.height * 0.45 { app.swipeUp() }
        }
        return (app, app.descendants(matching: .any)["browser-status"].firstMatch)
    }

    @MainActor func testInstallThenSetUp() throws {
        guard state == "missing" else { throw XCTSkip("MOCK_BROWSER=missing") }
        let (app, status) = try open()
        let install = app.buttons["browser-install"].firstMatch
        XCTAssertTrue(install.waitForExistence(timeout: 10) && status.label.contains("还没有装 ego lite"), status.label)
        shot(app, "browser-missing")
        install.tap()
        XCTAssertTrue(waitFor(status, "下载 ego lite") || waitFor(status, "校验"), status.label)
        XCTAssertTrue(app.buttons["browser-cancel"].exists)
        shot(app, "browser-installing")
        XCTAssertTrue(waitFor(status, "还要做完首次设置", timeout: 30), "installed, then its setup: \(status.label)")
    }

    @MainActor func testTheSetUpIsSteps() throws {
        guard state == "setup" else { throw XCTSkip("MOCK_BROWSER=setup") }
        let (app, status) = try open()
        XCTAssertTrue(status.waitForExistence(timeout: 10) && status.label.contains("还要做完首次设置"), status.label)
        let importStep = app.descendants(matching: .any)["browser-step-import"].firstMatch
        XCTAssertTrue(importStep.label.contains("从 Chrome") && importStep.label.contains("钥匙串密码"), importStep.label)
        XCTAssertTrue(app.buttons["browser-open"].exists && app.buttons["browser-recheck"].exists && app.buttons["browser-screen"].exists)
        shot(app, "browser-setup")
        if ProcessInfo.processInfo.environment["PERSONA_BROWSER_SCREEN"] == "1" {
            // VNC opens that Mac's live screen with only the plugin ego on for it (#1507), up to its sign-in
            // (MOCK_WEB, MOCK_SCREEN=fake).
            app.buttons["browser-screen"].tap()
            let signIn = app.staticTexts["登录这台 Mac 的屏幕共享"].firstMatch
            XCTAssertTrue(signIn.waitForExistence(timeout: 30), "the live screen with ego only:\n\(app.debugDescription)")
            shot(app, "browser-screen")
            app.descendants(matching: .any)["screen-close"].firstMatch.tap()
            XCTAssertTrue(app.buttons["browser-open"].waitForExistence(timeout: 10), "back on the machine page")
        }
        app.buttons["browser-open"].tap()
        XCTAssertTrue(waitFor(status, "打开了 ego lite"), status.label)
        XCTAssertTrue(waitFor(status, "就绪", timeout: 30), "set up on the Mac: \(status.label)")
    }

    @MainActor func testReadyIsGreen() throws {
        guard state == "ready" else { throw XCTSkip("MOCK_BROWSER=ready") }
        let (app, status) = try open()
        XCTAssertTrue(waitFor(status, "就绪 · 助手能用这台的浏览器"), status.label)
        let analytics = app.descendants(matching: .any)["browser-step-analytics"].firstMatch
        XCTAssertEqual(analytics.value as? String, "待做")
        XCTAssertTrue(analytics.label.contains("设置 › 同步和服务"), analytics.label)
        XCTAssertEqual(app.descendants(matching: .any)["browser-step-import"].firstMatch.value as? String, "已完成")
        shot(app, "browser-ready")
    }

    /// Settings › Extensions › Browser: a switch per Mac, how ego lite is on each. The mock serves extensions only with MOCK_EXTENSIONS=1
    /// as well (else the page says not found).
    @MainActor func testTheExtensionListsTheMacs() throws {
        guard state == "ready" else { throw XCTSkip("MOCK_BROWSER=ready") }
        let (app, _) = try open(["-tab", "settings", "-openExtension", "ego"])
        let macbook = app.switches.matching(NSPredicate(format: "label BEGINSWITH %@", "MacBook、")).firstMatch
        XCTAssertTrue(macbook.waitForExistence(timeout: 30), "no switch per Mac:\n\(app.debugDescription)")
        XCTAssertTrue(macbook.label.contains("在线 · ego 就绪"), macbook.label)
        let studio = app.switches.matching(NSPredicate(format: "label BEGINSWITH %@", "Mac Studio、")).firstMatch
        XCTAssertTrue(studio.label.contains("ego 没装"), studio.label)
        let setup = app.buttons["extension-machine-setup-macbook"].firstMatch
        XCTAssertTrue(setup.exists, "no way to open ego lite's installation and first-run setup")
        setup.tap()
        XCTAssertTrue(app.buttons["browser-recheck"].waitForExistence(timeout: 15), "the existing browser setup did not open:\n\(app.debugDescription)")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(macbook.waitForExistence(timeout: 10), "back to the extension settings")
        shot(app, "browser-plugin")
    }

    private func waitFor(_ e: XCUIElement, _ label: String, timeout: TimeInterval = 10) -> Bool {
        let deadline = Date.now.addingTimeInterval(timeout)
        while Date.now < deadline { if e.exists, e.label.contains(label) { return true }; usleep(200_000) }
        return false
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let look = ProcessInfo.processInfo.environment["PERSONA_LOOK"].map { "-" + $0 } ?? ""
        let full = "\(name)-\(UITestStyle.name)\(look)"
        let png = app.screenshot()
        let attachment = XCTAttachment(screenshot: png)
        attachment.name = full
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? png.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(full + ".png"))
        }
    }
}
