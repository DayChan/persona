import XCTest

/// Coming back to the app lands on the conversation that was open, the space's other agent picked in its header
/// included (2026-11-04, TestFlight on iPhone Duo open: after going to another app and back, or a relaunch, the Agents
/// page showed another page than the one chosen). Both after a trip to the home screen and after a relaunch.
/// Demo data on iPhone and iPad; iPad stands in for Duo open (regular width).
final class ReturnToChatTests: XCTestCase {
    private struct Chat: Decodable {
        var key: String
        var appeared: Int
    }

    private var app: XCUIApplication!
    private let base = ["-demo.on", "YES", "-app.tab", "chats", "-tab", "dev", "-probes", "YES", "-holdChromeOpen", "YES"]

    override func setUp() {
        continueAfterFailure = false
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(screenshot: app.screenshot())
            a.lifetime = .keepAlways
            add(a)
        }
        super.tearDown()
    }

    @MainActor private func shownChat(timeout: TimeInterval = 10) -> String? {
        let end = Date.now.addingTimeInterval(timeout)
        repeat {
            if let c = app.probe("chat", as: Chat.self, page: "agents") { return c.key }
            usleep(200_000)
        } while Date.now < end
        return nil
    }

    /// Opens the known multi-agent weather fixture, then another agent of its space from the header's tabs.
    /// `fresh`: starts on the list (`-dev.openChat ""`, which also hides from the app what it remembers while it runs:
    /// not for a test of what it remembers within a launch; the split shows the list anyway).
    @MainActor private func openSibling(fresh: Bool = true, extra: [String] = [], regularOnly: Bool = false) throws -> String {
        app = XCUIApplication()
        app.launchArguments = base + (fresh ? ["-dev.openChat", ""] : []) + ["-openAgentsFresh", "YES"] + extra
        app.launchPinned()
        if regularOnly, app.windows.firstMatch.frame.width <= 600 { throw XCTSkip("needs a regular width (iPad, iPhone Duo open)") }
        let row = app.buttons.matching(NSPredicate(format: "identifier == 'agents.row' AND label CONTAINS %@",
                                                  "给天气 App 加主屏幕小组件")).firstMatch // i18n-ignore: bundled synthetic fixture title
        if !row.exists || !row.isHittable {
            app.revealAgentsSearch()
            let search = app.searchFields.firstMatch
            XCTAssertTrue(search.waitForExistence(timeout: 5), "the Agents search did not open")
            search.tap()
            search.typeText("天气") // i18n-ignore: bundled synthetic fixture title
        }
        XCTAssertTrue(row.waitForExistence(timeout: 15), "the multi-agent fixture is not in the list")
        row.tap()
        guard let first = shownChat() else { XCTFail("the chat did not open"); return "" }
        XCTAssertTrue(first.hasSuffix("macbook/a1f3c2d4-0000-4000-8000-000000000001"), "the intended fixture did not open")
        let compact = app.buttons["chat.compactTitle"].firstMatch
        if compact.exists && compact.isHittable { compact.tap() }
        sleep(2)
        // The tabs read 「<tab>，<state>」 to VoiceOver; the one open is selected.
        let tab = ["review", "widget", "release", "decide"].lazy
            .map { self.app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@ AND selected == false", $0)).firstMatch }
            .first { $0.exists && $0.isHittable }
        guard let tab else {
            XCTFail("no other agent's tab in the header: \(app.buttons.allElementsBoundByIndex.prefix(40).map(\.label))")
            return first
        }
        tab.tap()
        let end = Date.now.addingTimeInterval(5)
        var now = first
        while now == first, Date.now < end { usleep(200_000); now = shownChat(timeout: 1) ?? first }
        XCTAssertNotEqual(now, first, "the header's tab did not switch the chat")
        sleep(2)
        return now
    }

    @MainActor func testTheChatStaysAfterTheHomeScreen() throws {
        let picked = try openSibling()
        XCUIDevice.shared.press(.home)
        sleep(4)
        app.activate()
        sleep(3)
        XCTAssertEqual(shownChat(), picked, "after the home screen")
    }

    /// The tree swaps (`-swapTreeAt`: the shell for a moment and back, as a fold and an unfold make it on iPhone Duo; the
    /// snapshots on the way to the background no longer do, #2836): the new Agents tree opens the chat again. On a regular
    /// width only (iPad), where the swap is from the split to the shell and back.
    @MainActor func testTheChatStaysAfterTheTreeSwaps() throws {
        let picked = try openSibling(fresh: false, extra: ["-swapTreeAt", "20"], regularOnly: true)
        let launched = app.probe("chat", as: Chat.self, page: "agents")?.appeared ?? 0
        sleep(25) // past the swap and back
        XCTAssertGreaterThan(app.probe("chat", as: Chat.self, page: "agents")?.appeared ?? .max, launched, "the tree did not swap")
        XCTAssertEqual(shownChat(), picked, "after the tree swapped")
    }

    @MainActor func testTheChatStaysAfterARelaunch() throws {
        let picked = try openSibling()
        app.terminate()
        app = XCUIApplication()
        app.launchArguments = base + ["-openWelcome", "NO"]
        app.launchPinned()
        XCTAssertEqual(shownChat(timeout: 15), picked, "after a relaunch")
    }
}
