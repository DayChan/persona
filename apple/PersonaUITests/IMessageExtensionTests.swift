import XCTest

/// Settings › Extensions › Messages (iMessage) (#1830 step 6) against scripts/mock/mock_hub.py with MOCK_EXTENSIONS=1
/// (TEST_RUNNER_PERSONA_EXTENSIONS_HUB): a switch per Mac, MacBook on; turning on another replaces it in one save, and
/// its connector settings are reachable from the extension. PERSONA_SHOTS keeps screenshots, named by PERSONA_EXTENSIONS_APPEARANCE.
final class IMessageExtensionTests: XCTestCase {
    @MainActor func testOneMacOnly() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_EXTENSIONS_HUB"] else {
            throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 (PERSONA_EXTENSIONS_HUB)")
        }
        continueAfterFailure = false
        let look = env["PERSONA_EXTENSIONS_APPEARANCE"] ?? "light"
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openExtension", "imessage"]
        app.launchPinned()
        func row(_ name: String) -> XCUIElement { app.switches.matching(NSPredicate(format: "label BEGINSWITH %@", name + "、")).firstMatch }
        let macbook = row("MacBook"), studio = row("Mac Studio")
        XCTAssertTrue(macbook.waitForExistence(timeout: 30), "no switch per Mac:\n\(app.debugDescription)")
        XCTAssertEqual(macbook.value as? String, "1")
        XCTAssertEqual(studio.value as? String, "0")
        XCTAssertFalse(row("服务器").exists, "a Linux machine offered for Messages")
        shot(app, "imessage-ext-\(look)")
        XCTAssertTrue(app.buttons["extension-connector-open-imessage"].waitForExistence(timeout: 10), "no connector status and permission guide")
        studio.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
        XCTAssertTrue(waitFor(studio, value: "1"), "the replacement Mac shows on")
        XCTAssertTrue(waitFor(macbook, value: "0"), "the old Mac goes off in the same save")
        shot(app, "imessage-ext-switched-\(look)")
        app.buttons["extension-connector-open-imessage"].tap()
        XCTAssertTrue(app.staticTexts["信息"].waitForExistence(timeout: 10), "connector settings did not open")
        XCTAssertTrue(app.switches["messages-ai"].waitForExistence(timeout: 10), "Messages' own Hand to Persona setting")
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: name + ".png"))
    }

    private func waitFor(_ e: XCUIElement, value: String, timeout: TimeInterval = 10) -> Bool {
        let deadline = Date.now.addingTimeInterval(timeout)
        while Date.now < deadline { if e.value as? String == value { return true }; usleep(200_000) }
        return false
    }
}
