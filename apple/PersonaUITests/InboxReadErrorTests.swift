import XCTest

/// Sources the hub could not read this round (#1849): their earlier items stay, and a quiet notice names each one so
/// the list does not look complete. Against scripts/mock/mock_hub.py with
/// MOCK_INBOX_UNREAD=<name>:timeout:<reason>,ext-tracker:rate_limited (TEST_RUNNER_PERSONA_MOCK_HUB).
final class InboxReadErrorTests: XCTestCase {
    @MainActor func testSourcesNotReadThisRoundAreNamed() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_UNREAD (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", ""]
        app.launchPinned()

        func text(_ string: String) -> XCUIElement {
            app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", string)).firstMatch
        }
        XCTAssertTrue(text("示例聊天：这一轮没读到：超时了").waitForExistence(timeout: 20),
                      "the hub's read reason is not shown:\n\(app.debugDescription)")
        XCTAssertTrue(text("ext-tracker：这一轮没读到，列表可能不全").exists, "a missing reason needs the fallback")
        XCTAssertTrue(text("ext-third：第三轮没读到").exists)
        XCTAssertTrue(text("ext-fourth：第四条来源仍可见").exists)
        // Four lines exceed NoticeBanner's default three. The saved screenshot must show the fourth line: a clipped
        // Text still exposes its full accessibility label, so the queries above alone cannot prove it is visible.
        XCTAssertFalse(app.descendants(matching: .any)["inbox.triage"].exists,
                       "triage is ok: the waiting-for-triage banner stays hidden")
        if let dir = env["PERSONA_SHOTS"] {
            let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir)
                .appendingPathComponent("inbox-unread-\(appearance).png"))
        }
    }
}
