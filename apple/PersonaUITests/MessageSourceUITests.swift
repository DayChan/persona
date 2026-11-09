import XCTest

/// Runs only against the dedicated loopback fixture in scripts/mock/message_sources_hub.py.
final class MessageSourceUITests: XCTestCase {
    @MainActor func testSourcesAreNotUserBubblesAndLongUpdatesExpand() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"],
              let url = URL(string: hub), url.host == "127.0.0.1" else {
            throw XCTSkip("needs the loopback message_sources_hub.py fixture")
        }
        continueAfterFailure = false
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant",
                               "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001",
                               "-dev.openChat", "", "-openChatsFresh", "YES", "-ui.newChatLayout", "YES", "-AppleLanguages", "(zh-Hans)"]
        app.launchPinned()
        let any = app.descendants(matching: .any)
        let patrol = any["message-source-patrol"].firstMatch
        XCTAssertTrue(patrol.waitForExistence(timeout: 30), app.debugDescription)
        let cross = any["message-source-cross"].firstMatch
        XCTAssertTrue(cross.exists, app.debugDescription)
        let labels = any.matching(identifier: "message-source-label")
        XCTAssertEqual(labels.count, 2, "consecutive patrol rows must share one source label")
        XCTAssertTrue(labels.matching(NSPredicate(format: "label == %@", "@督办巡检")).firstMatch.exists)
        XCTAssertTrue(labels.matching(NSPredicate(format: "label == %@", "@开源巡检")).firstMatch.exists)
        XCTAssertFalse(any.matching(NSPredicate(format: "label CONTAINS %@", "这条消息不是用户本人发的")).firstMatch.exists)
        let toggle = any["message-source-toggle-patrol"].firstMatch
        XCTAssertTrue(toggle.exists)
        XCTAssertEqual(toggle.label, "展开")
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = "message-sources-folded"
        attachment.lifetime = .keepAlways
        add(attachment)
        toggle.tap()
        XCTAssertTrue(patrol.descendants(matching: .any)["message-source-body"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertEqual(toggle.label, "收起")
        toggle.tap()
        XCTAssertTrue(patrol.descendants(matching: .any)["message-source-preview"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertEqual(toggle.label, "展开")
    }
}
