import XCTest

/// iPhone Duo with the system's bars down the side (the outer display, the inner one on its side): every page of the shell
/// keeps its bar as the side rail, the face low in the column and the switch stacked at its foot (docs/apple/ios.md
/// 「iPhone Duo」). With Messages on, its page drew the bar across the top again (#2318). Skips where the bars are across
/// the top. Needs an iPhone Duo simulator (iOS 27.1, built with Xcode 27) and scripts/mock/mock_hub.py:
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/ShellSideRailTests
final class ShellSideRailTests: XCTestCase {
    @MainActor func testPagesKeepTheRail() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-demo.on", "NO", "-integrations.lark.enabled", "YES"]
        app.launchPinned()

        let assistant = app.buttons.matching(identifier: "shell-assistant").firstMatch
        let agents = app.buttons.matching(identifier: "shell-agents").firstMatch
        let messages = app.buttons.matching(identifier: "shell-feishu").firstMatch
        XCTAssertTrue(assistant.waitForExistence(timeout: 20))
        guard stacked(assistant, over: agents) else {
            throw XCTSkip("the system's bars are across the top: needs iPhone Duo's outer display or the inner one on its side")
        }
        XCTAssertTrue(messages.waitForExistence(timeout: 20), "Messages is on and the mock hub has Lark chats")

        let window = app.windows.firstMatch.frame
        // Not Agents yet: in zh-Hans its header spins in a layout loop on the outer display (#2352).
        let pages = [(messages, "feishu-sidebar"), (assistant, "assistant-sidebar-button")]
        for (segment, faceID) in pages {
            segment.tapCenter()
            let face = app.buttons.matching(identifier: faceID).firstMatch
            XCTAssertTrue(face.waitForExistence(timeout: 10), "\(faceID) is on screen")
            // Only once the strip rests on the page is its face still in the column (while it slides, the page's bar
            // moves with it).
            eventually("\(faceID) in the rail", within: 10) {
                var off: [String] = []
                if abs(face.frame.midX - segment.frame.midX) >= segment.frame.width / 2 {
                    off.append("not in the rail's column: \(face.frame) beside \(segment.frame)")
                }
                if face.frame.minY <= window.height / 3 { off.append("not low in the column, within the thumb's reach: \(face.frame)") }
                if !(stacked(assistant, over: agents) && stacked(agents, over: messages)) { off.append("the switch is not stacked down the side") }
                return off
            }
        }
    }

    /// One segment above the other, in the same column. Only the tops: the chosen segment is a menu, and its frame spans
    /// the whole switch.
    @MainActor private func stacked(_ top: XCUIElement, over bottom: XCUIElement) -> Bool {
        abs(top.frame.midX - bottom.frame.midX) < 2 && bottom.frame.minY > top.frame.minY
    }
}
