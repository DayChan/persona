import XCTest

/// What Persona sent to its tasks' agents in the main conversation (#1542): never the user's bubble. The hand-off sent with
/// a task's card is only on that card (「已发 1 条指令」, a tap lists it, a tap on it opens the text with Copy); a later
/// message uses a three-line exchange with Expand; the user's own word to a task stays the user's bubble. Against
/// scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_SENT=1 MOCK_TASKS_NO_ASKS=1 (PERSONA_MOCK_HUB). With PERSONA_SHOTS
/// set it writes the folded and open states there (PERSONA_SHOTS_APPEARANCE names the simulator's appearance in the file).
final class SentToAgentTests: XCTestCase {
    private var hub = ""
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_SENT=1 MOCK_TASKS_NO_ASKS=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/sent-\(appearance)-\(name).png"))
    }

    @MainActor func testPersonasMessagesFoldAndTheHandOffIsOnTheCard() throws {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main,
                               "-openChatsFresh", "YES", "-dev.openChat", "", "-AppleLanguages", "(zh-Hans)"]
        app.launchPinned()

        let any = app.descendants(matching: .any)
        func has(_ text: String) -> XCUIElement { any.matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch }
        // The card's own button takes up the row's identifier too (SwiftUI): the row is the one not named by the card's title.
        let list = app.buttons.matching(NSPredicate(format: "identifier == %@ AND NOT (label CONTAINS %@)", "task-sent", "降低后台同步的内存占用")).firstMatch
        XCTAssertTrue(list.waitForExistence(timeout: 20), "no list of what was sent on the task's card:\n\(app.debugDescription)")
        sleep(1) // the list settles at the newest
        // The hand-off is not drawn on its own: its words are nowhere until the card's list is opened.
        XCTAssertFalse(has("常驻内存降下来").exists, "the hand-off is drawn outside its card")
        // The rework to the widget fix stays an exchange, not the removed iOS baseline's one-line disclosure.
        XCTAssertTrue(has("Persona → @修小组件横屏崩溃").exists, app.debugDescription)
        let line = any.matching(identifier: "exchange.routed").allElementsBoundByIndex.last { $0.isHittable }
        XCTAssertNotNil(line, "the rework exchange is missing")
        let expand = line?.descendants(matching: .any)["exchange.expand"].firstMatch
        XCTAssertTrue(expand?.exists == true, "the long rework is not folded to three lines")
        XCTAssertTrue(has("先补重试和超时").exists, "the user's own word to a task is gone")
        try shot(app, "folded")

        expand?.tap()
        let collapse = line?.descendants(matching: .any)["exchange.collapse"].firstMatch
        XCTAssertTrue(collapse?.waitForExistence(timeout: 5) == true, "Expand did not open the rework")
        XCTAssertTrue(has("补一张横屏的截图贴进 PR").exists, "the open exchange is not whole")
        sleep(1)
        try shot(app, "line-open")
        collapse?.tap()
        XCTAssertTrue(expand?.waitForExistence(timeout: 5) == true, "the exchange did not fold")

        list.tap()
        let entry = any["task-sent-entry"].firstMatch
        XCTAssertTrue(entry.waitForExistence(timeout: 5), "the card's list did not open")
        XCTAssertTrue(entry.label.contains("常驻内存降下来"), "the list does not show the hand-off's first line: \(entry.label)")
        entry.tap()
        XCTAssertTrue(any["sent-copy"].firstMatch.waitForExistence(timeout: 5), "the hand-off did not open with Copy")
        XCTAssertTrue(has("PR 里有改前改后的 Allocations 截图").exists, "the open hand-off is not whole")
        sleep(1)
        try shot(app, "card-open")
        app.terminate()
    }
}
