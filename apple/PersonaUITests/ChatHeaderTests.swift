import XCTest

/// An Agents chat's header, open and folded (`ChromeHeader`), opened from the list as a user would: it stays below the
/// status bar, and the one Back VoiceOver and XCUITest find is the one on screen, which goes back. In the BeautifulUI
/// style the folded bar once sat in the status bar's band (its place pill on the clock), and the hidden bar's Back stayed
/// in the accessibility tree, hittable but going nowhere (2026-11-02). Both layouts here; both styles with
/// `TEST_RUNNER_PERSONA_UI_STYLE=beautiful`.
final class ChatHeaderTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUp() {
        continueAfterFailure = false
    }

    /// Opens the first agent from the list; `folded`: the header folds itself after its idle seconds, else it is held open.
    /// `sideBars`: laid out as under iPhone Duo's side bars (`-sideBars YES`): the system's bar takes the actions, Back
    /// first, and the page draws no back of its own (2026-11-04: two backs, the system's and ours, one over the other).
    @MainActor private func openChat(oldLayout: Bool, folded: Bool, sideBars: Bool = false) {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-ui.oldLayout", oldLayout ? "YES" : "NO", "-app.tab", "chats", "-tab", "dev",
                               "-dev.openChat", "", "-openAgentsFresh", "YES", "-holdChromeOpen", folded ? "NO" : "YES",
                               "-sideBars", sideBars ? "YES" : "NO"]
        app.launchPinned()
        let row = app.buttons.matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), "no agent in the list")
        sleep(1)
        row.tap()
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label == %@", "更多")).firstMatch.waitForExistence(timeout: 10)
                      || app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "显示标题和标签")).firstMatch.waitForExistence(timeout: 5),
                      "the chat did not open")
        sleep(folded ? 6 : 2) // folded: past the 3 s it waits, and the fold's animation
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
    }

    /// The status bar's bottom: SpringBoard's status bar where XCUITest finds it, else the shortest one a notched or
    /// Dynamic Island iPhone has.
    @MainActor private var statusBarBottom: CGFloat {
        let bar = XCUIApplication(bundleIdentifier: "com.apple.springboard").statusBars.firstMatch
        return bar.exists && bar.frame.height > 0 ? bar.frame.maxY : 44
    }

    @MainActor private func check(_ what: String, folded: Bool, sideBars: Bool = false) {
        shot(what)
        if sideBars { // the system's own back (UIKit's "BackButton", labelled with the page before) besides ours
            let system = app.navigationBars.buttons.matching(identifier: "BackButton").allElementsBoundByIndex.filter(\.isHittable)
            XCTAssertTrue(system.isEmpty, "\(what): the system's back as well as 返回: \(system.map(\.label))")
            checkOneTitle(what)
        }
        let backs = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).allElementsBoundByIndex
        if backs.count != 1 {
            let t = XCTAttachment(string: app.debugDescription)
            t.name = "\(what) tree"
            t.lifetime = .keepAlways
            add(t)
        }
        XCTAssertEqual(backs.count, 1, "\(what): \(backs.count) 返回 in the accessibility tree (a hidden one exposed?): \(backs.map(\.frame))")
        guard let back = backs.first else { return }
        XCTAssertTrue(back.isHittable, "\(what): 返回 not hittable")
        let top = statusBarBottom
        XCTAssertGreaterThanOrEqual(back.frame.minY, top - 1, "\(what): 返回 at \(back.frame) is in the status bar (to \(top))")
        if folded {
            let place = app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "显示标题和标签")).firstMatch
            XCTAssertTrue(place.exists, "\(what): no folded bar")
            XCTAssertGreaterThanOrEqual(place.frame.minY, top - 1, "\(what): the folded bar at \(place.frame) is on the status bar (to \(top))")
        }
        back.tap()
        let row = app.buttons.matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5) && row.isHittable, "\(what): 返回 did not go back to the list")
    }

    @MainActor func testOpenHeaderInTheShell() {
        openChat(oldLayout: false, folded: false)
        check("shell open", folded: false)
    }

    @MainActor func testFoldedHeaderInTheShell() {
        openChat(oldLayout: false, folded: true)
        check("shell folded", folded: true)
    }

    @MainActor func testOpenHeaderInTheOldLayout() {
        openChat(oldLayout: true, folded: false)
        check("old open", folded: false)
    }

    @MainActor func testFoldedHeaderInTheOldLayout() {
        openChat(oldLayout: true, folded: true)
        check("old folded", folded: true)
    }

    @MainActor func testSideBarsInTheShell() {
        openChat(oldLayout: false, folded: false, sideBars: true)
        checkSideBars("shell side bars")
    }

    @MainActor func testSideBarsInTheOldLayout() {
        openChat(oldLayout: true, folded: false, sideBars: true)
        checkSideBars("old side bars")
    }

    /// The session's name once, in the header (`chat.title`): the system draws its bar's title over the content, not in
    /// the side column, so it showed a second time right above the header's (2026-11-08, TestFlight).
    @MainActor private func checkOneTitle(_ what: String) {
        let header = app.buttons["chat.title"].firstMatch
        XCTAssertTrue(header.exists, "\(what): no title in the header")
        let system = app.navigationBars.staticTexts.allElementsBoundByIndex.filter { !$0.label.isEmpty && header.label.hasPrefix($0.label) }
        XCTAssertTrue(system.isEmpty, "\(what): the system bar's title as well as the header's: \(system.map(\.label))")
    }

    /// One back, the chat's own: as `check` on an iPhone width; the split's first page (iPad, iPhone Duo open) has none.
    @MainActor private func checkSideBars(_ what: String) {
        guard app.windows.firstMatch.frame.width > 600 else { return check(what, folded: false, sideBars: true) }
        shot(what)
        checkOneTitle(what)
        let backs = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).allElementsBoundByIndex.filter(\.isHittable)
        let system = app.navigationBars.buttons.matching(identifier: "BackButton").allElementsBoundByIndex.filter(\.isHittable)
        XCTAssertTrue(backs.isEmpty && system.isEmpty, "\(what): a back on the split's first page: \(backs.map(\.frame)) \(system.map(\.label))")
    }
}

/// What the open header presents stays while it is up: folded, the header leaves the view tree, so it must not fold
/// itself under its own sheet or menu.
final class ChatHeaderPresentationTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor private func openChat() {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-ui.oldLayout", "NO", "-app.tab", "chats", "-tab", "dev",
                               "-dev.openChat", "", "-openAgentsFresh", "YES", "-holdChromeOpen", "NO"]
        app.launchPinned()
        let row = app.buttons.matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), "no agent in the list")
        sleep(1)
        row.tap()
    }

    @MainActor func testTheTabsSheetStays() {
        openChat()
        let tabs = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "同一空间的")).firstMatch
        XCTAssertTrue(tabs.waitForExistence(timeout: 5), "no ⋯ for the space's tabs")
        tabs.tap()
        let sheet = app.staticTexts.matching(NSPredicate(format: "label ENDSWITH %@", "个标签页")).firstMatch
        XCTAssertTrue(sheet.waitForExistence(timeout: 5), "the tabs sheet did not open")
        sleep(6) // past the 3 s the header waits before folding
        XCTAssertTrue(sheet.exists, "the tabs sheet closed by itself")
    }

    @MainActor func testTheMoreMenuStays() {
        openChat()
        let more = app.buttons.matching(NSPredicate(format: "label == %@", "更多")).firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 5), "no ⋯")
        more.tap()
        let rename = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "重命名")).firstMatch
        XCTAssertTrue(rename.waitForExistence(timeout: 5), "the menu did not open")
        sleep(6)
        XCTAssertTrue(rename.exists, "the menu closed by itself")
    }
}
