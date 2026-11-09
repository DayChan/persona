import XCTest

/// The Agents list's 「herdr 没在运行」 row (#1916): shown for a machine whose classic herdr does not run, Start asks the
/// node once and the row goes; a start that fails says why and keeps the button.
///   PORT=<port> python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/HerdrNoticeTests
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps a picture of each state (`…_SHOTS_APPEARANCE` names the simulator's appearance).
final class HerdrNoticeTests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
        _ = try Data(contentsOf: URL(string: hub + "/mock/reset")!)
    }

    /// The mock's herdr: which machines are down and how Start goes; answers how many starts each was asked for.
    @discardableResult private func mock(_ query: String = "") throws -> [String: Int] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/herdr" + query)!)
        let root = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        return root?["starts"] as? [String: Int] ?? [:]
    }

    @MainActor private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-openWelcome", "YES", "-demo.on", "NO", "-pairURL", pair,
                               "-app.tab", "chats", "-dev.openChat", "", "-tab", "dev", "-openAgentsFresh", "YES"]
        app.launchPinned()
        LiveActivityShots.allowNotifications()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("herdr-\(name)-\(look).png"))
    }

    private func text(_ app: XCUIApplication, containing s: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    @MainActor func testStartTakesTheRowAway() throws {
        try mock("?stopped=macbook&start=slow")
        let app = launch()
        XCTAssertTrue(text(app, containing: "MacBook 的 herdr 没在运行").waitForExistence(timeout: 20), "no row for the stopped machine")
        XCTAssertFalse(text(app, containing: "Mac Studio 的 herdr").exists, "a row for a machine whose herdr runs")
        shot(app, "stopped")

        let start = app.buttons["herdrNotice.start"]
        XCTAssertTrue(start.waitForExistence(timeout: 5))
        start.tap()
        XCTAssertTrue(text(app, containing: "正在启动 MacBook 的 herdr").waitForExistence(timeout: 3), "no 正在启动…")
        // Kept for the row's height while it starts, but not pressable.
        let again = app.buttons["herdrNotice.start"]
        XCTAssertFalse(again.exists && again.isEnabled, "启动 can be pressed again while it starts")
        shot(app, "starting")

        let row = app.descendants(matching: .any)["agents.herdrNotice"]
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: row)
        wait(for: [gone], timeout: 15)
        XCTAssertEqual(try mock(), ["macbook": 1], "the node was not asked exactly once")
    }

    @MainActor func testAFailedStartSaysWhy() throws {
        try mock("?stopped=macbook&start=fail")
        let app = launch()
        let start = app.buttons["herdrNotice.start"]
        XCTAssertTrue(start.waitForExistence(timeout: 20))
        start.tap()
        XCTAssertTrue(app.descendants(matching: .any)["herdrNotice.failure"].waitForExistence(timeout: 10), "no reason shown")
        XCTAssertTrue(text(app, containing: "马上退出了（退出码 1）").exists, "the node's reason is not in the row, in words")
        XCTAssertFalse(text(app, containing: "stopped right after starting").exists, "the raw error is in the row")
        XCTAssertTrue(app.buttons["herdrNotice.start"].exists, "no way to try again")
        shot(app, "failed")
        XCTAssertEqual(try mock(), ["macbook": 1])
    }

    /// A hub that only knew what node.info said kept answering "not running" after a start: the row must not come back.
    @MainActor func testTheRowStaysGoneWithAnOlderHub() throws {
        try mock("?stopped=macbook&start=stale")
        let app = launch()
        let start = app.buttons["herdrNotice.start"]
        XCTAssertTrue(start.waitForExistence(timeout: 20))
        start.tap()
        let row = app.descendants(matching: .any)["agents.herdrNotice"]
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: row)
        wait(for: [gone], timeout: 10)
        sleep(12) // past a read of the list (HerdrWatch.interval)
        XCTAssertFalse(row.exists, "the row came back after the start")
    }

    private func herdrReads() throws -> Int {
        let data = try Data(contentsOf: URL(string: hub + "/mock/hits")!)
        let root = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        let hits = root?["hits"] as? [String: Int] ?? [:]
        return hits.filter { $0.key.hasPrefix("GET /api/v1/machines/") && $0.key.hasSuffix("/herdr") }
            .values.reduce(0, +)
    }

    @MainActor func testBackgroundStopsPollingAndForegroundResumesIt() throws {
        try mock("?stopped=macbook")
        let app = launch()
        XCTAssertTrue(app.buttons["herdrNotice.start"].waitForExistence(timeout: 20))
        XCTAssertGreaterThan(try herdrReads(), 0)
        shot(app, "before-background")
        XCUIDevice.shared.press(.home)
        let backgrounded = expectation(for: NSPredicate(format: "state == %d OR state == %d",
                                                         XCUIApplication.State.runningBackground.rawValue,
                                                         XCUIApplication.State.runningBackgroundSuspended.rawValue),
                                       evaluatedWith: app)
        XCTAssertEqual(XCTWaiter.wait(for: [backgrounded], timeout: 5), .completed,
                       "Home did not background the app; state=\(app.state.rawValue)")
        let home = XCTAttachment(screenshot: XCUIApplication(bundleIdentifier: "com.apple.springboard").screenshot())
        home.name = "after-home"
        home.lifetime = .keepAlways
        add(home)
        sleep(1) // Let the scene transition and any request already on the wire finish.
        let background = try herdrReads()
        sleep(12) // Longer than the existing foreground polling interval.
        XCTAssertEqual(try herdrReads(), background, "a selected tab must not keep polling in the background")
        app.activate()
        for _ in 0..<50 {
            if try herdrReads() > background { break }
            usleep(100_000)
        }
        XCTAssertGreaterThan(try herdrReads(), background, "returning must resume the existing status feature")
        shot(app, "background-resume")
    }
}
