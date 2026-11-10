import XCTest

/// What the app holds stays when it comes back (#2836). iOS snapshots an app on its way to the background in another
/// width (iPhone Duo open), which built the whole tree again: an open New Agent sheet closed with what was typed in it,
/// uploads under way lost their place, what was picked went. Away, the pages now keep the width class they had.
/// `-swapTreeAt` with `-swapTreeAway YES` flips the whole app's width class a moment while it counts as away, as iOS
/// does for its snapshots on the Duo. Regular width only: an iPad, or the iPhone Duo simulator opened in Xcode's Device
/// Hub. The simulators take no snapshots in another width (the trip to the home screen keeps the sheet there without
/// the fix too): that the fix holds on the way to the background is for a real iPhone Duo open. Against
/// scripts/mock/mock_hub.py (PERSONA_MOCK_HUB).
final class TreeSwapAwayTests: XCTestCase {
    private var hub = ""
    /// `probe.tree`: the width's flips, and the swaps between the shell and the tabs.
    private struct Swaps: Decodable { var count: Int; var rebuilt: Int }
    /// `probe.tree-inner`: how many times the pages below RootView saw their width class change.
    private struct Inner: Decodable { var count: Int }

    override func setUpWithError() throws {
        continueAfterFailure = false
        // TEST_RUNNER_PERSONA_ORIENTATION=landscape: on its side (the iPhone Duo open, whose synthetic drags miss in portrait).
        XCUIDevice.shared.orientation = ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape" ? .landscapeLeft : .portrait
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        hub = h
    }

    /// New Agent open with a first message typed: the width flips while the app is away, and the sheet is still there
    /// with the message.
    @MainActor func testNewAgentStaysOpenWithWhatWasTyped() throws {
        let (app, launched) = try openNewAgent(["-swapTreeAt", "45", "-swapTreeAway", "YES"])
        XCTAssertLessThan(Date.now.timeIntervalSince(launched), 40, "typed too close to the swap")
        let inner = app.probe("tree-inner", as: Inner.self)?.count
        XCTAssertNotNil(inner, "no probe.tree-inner")
        sleep(UInt32(max(0, launched.addingTimeInterval(53).timeIntervalSinceNow)))
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.count, 1, "the width did not flip")
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.rebuilt, 0, "the tree was built again")
        XCTAssertEqual(app.probe("tree-inner", as: Inner.self)?.count, inner, "the pages saw the width class change")
        stillThere(app, "new-agent-after-swap")
    }

    /// To the home screen and back: the sheet is still there with the message. A plain trip on a simulator; on an iPhone
    /// Duo open the snapshots on the way are taken in the outer screen's width.
    @MainActor func testNewAgentStaysOpenThroughTheHomeScreen() throws {
        let (app, _) = try openNewAgent([])
        XCUIDevice.shared.press(.home)
        sleep(4)
        app.activate()
        stillThere(app, "new-agent-after-home")
    }

    /// TEST_RUNNER_PERSONA_NO_DRAG=1 (the iPhone Duo open, where synthetic drags miss): nothing is typed, First Message
    /// being below the form's fold; the sheet staying open is what tells.
    private var noDrag: Bool { ProcessInfo.processInfo.environment["PERSONA_NO_DRAG"] == "1" }

    /// New Agent open (regular width only), "整理一下本周的发布说明" typed as its first message.
    @MainActor private func openNewAgent(_ extra: [String]) throws -> (XCUIApplication, Date) {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-app.tab", "chats", "-showNewChat", "YES", "-probes", "YES"] + extra
        app.launchPinned()
        let launched = Date.now
        guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("the tree swaps in regular width only (iPad, iPhone Duo open)") }
        let prompt = app.descendants(matching: .any)["newChat.prompt"].firstMatch
        let sheet = app.navigationBars["新对话"].firstMatch
        XCTAssertTrue(sheet.waitForExistence(timeout: 20), "no New Agent sheet:\n\(app.debugDescription)")
        if noDrag { return (app, launched) }
        // Down the form to First Message (a form builds the rows near the screen).
        let middle = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: sheet.frame.midX, dy: sheet.frame.maxY + 300))
        for _ in 0..<8 where !(prompt.exists && prompt.isHittable) {
            middle.press(forDuration: 0.05, thenDragTo: middle.withOffset(CGVector(dx: 0, dy: -200)), withVelocity: .slow, thenHoldForDuration: 0.2)
        }
        XCTAssertTrue(prompt.waitForExistence(timeout: 5), "no First Message field:\n\(app.debugDescription)")
        prompt.tap()
        prompt.typeText("整理一下本周的发布说明")
        return (app, launched)
    }

    /// The New Agent sheet is still open with the message typed; a screenshot.
    @MainActor private func stillThere(_ app: XCUIApplication, _ name: String) {
        let open = app.navigationBars["新对话"].firstMatch.waitForExistence(timeout: 5)
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s); a.name = name; a.lifetime = .keepAlways; add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)-\(ProcessInfo.processInfo.environment["PERSONA_STYLE"] ?? "x").png"))
        }
        XCTAssertTrue(open, "the New Agent sheet went with the tree:\n\(app.debugDescription)")
        guard !noDrag else { return }
        let again = app.descendants(matching: .any)["newChat.prompt"].firstMatch
        XCTAssertTrue(again.waitForExistence(timeout: 5), "no First Message field:\n\(app.debugDescription)")
        XCTAssertEqual(again.value as? String, "整理一下本周的发布说明", "what was typed went with the tree")
    }
}
