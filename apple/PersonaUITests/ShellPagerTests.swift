import XCTest

/// The shell's Assistant | Agents strip (docs/apple/ios.md「新外壳」, `ShellPager`): a drag to the left on Assistant slides to Agents, a
/// drag to the right on Agents slides back, a drag to the right on Assistant is still the drawer; pushed pages keep the
/// system's back swipe, the list scrolls up and down without paging, and Agents' rows have no swipe of their own: a drag
/// along one pages, and the row's actions are in its ⋯ (#1244).
/// Which page shows is read from the app's probe (`ShellPageProbe.swift`): the hidden page can stay in the accessibility
/// tree.
final class ShellPagerTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor private func launch(_ extra: [String] = []) {
        app = XCUIApplication()
        // Not a chat an earlier test left open on Agents (it comes back when Agents is first built).
        app.launchArguments = ["-demo.on", "YES", "-ui.oldLayout", "NO", "-probes", "YES", "-newAssistant", "YES", "-dev.openChat", ""] + extra
        app.launchPinned()
        XCTAssertTrue(app.buttons["assistant-sidebar-button"].firstMatch.waitForExistence(timeout: 15), "no shell")
        sleep(1)
    }

    /// Attached to the run; with TEST_RUNNER_PERSONA_SHOTS=<dir> also saved there (TEST_RUNNER_PERSONA_STYLE names it).
    @MainActor private func attachScreen(_ name: String) {
        let screen = app.screenshot()
        let shot = XCTAttachment(screenshot: screen)
        shot.name = name
        shot.lifetime = .keepAlways
        add(shot)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"].map { "-" + $0 } ?? ""
            try? screen.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + style + ".png"))
        }
    }

    /// A finger's drag between two points of the screen (fractions of its size).
    @MainActor private func drag(from a: CGVector, to b: CGVector, fast: Bool = false) {
        let w = app.windows.firstMatch
        w.coordinate(withNormalizedOffset: a)
            .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: b),
                   withVelocity: fast ? .fast : .default, thenHoldForDuration: 0)
    }

    /// Back from a pushed page with the edge swipe, as a finger does it (held, slow).
    @MainActor private func edgeSwipeBack() {
        let w = app.windows.firstMatch
        w.coordinate(withNormalizedOffset: CGVector(dx: 0.005, dy: 0.5))
            .press(forDuration: 0.1, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)),
                   withVelocity: .slow, thenHoldForDuration: 0.1)
    }

    @MainActor private func expectAgents(_ shown: Bool, _ what: String) {
        let ok = app.waitAgentsDrawn(shown)
        if !ok { attachScreen(what) }
        XCTAssertTrue(ok, (shown ? "Agents is not drawn: \(what)" : "Agents still drawn: \(what)") + " \(app.shellTrace)")
    }

    @MainActor private var drawerOpen: Bool { app.descendants(matching: .any)["assistant-drawer-close"].firstMatch.exists }

    @MainActor private func toAgents() {
        drag(from: CGVector(dx: 0.85, dy: 0.45), to: CGVector(dx: 0.1, dy: 0.45))
        expectAgents(true, "a drag to the left on 助手")
        XCTAssertTrue(app.buttons["agents-new"].firstMatch.waitForExistence(timeout: 3), "no 新建会话 on Agents")
    }

    @MainActor func testDragLeftOnTheAssistantAndRightOnAgents() {
        launch()
        expectAgents(false, "launch on 助手")
        toAgents()
        XCTAssertFalse(drawerOpen, "the drawer came out on a drag to the left")
        // Back from the edge band (over a row there too: the edge is the strip's).
        drag(from: CGVector(dx: 0.02, dy: 0.6), to: CGVector(dx: 0.85, dy: 0.6))
        expectAgents(false, "a drag to the right from Agents' left edge")
        XCTAssertFalse(drawerOpen, "the drawer came out instead of 助手")
        // A slanted one (30°), and back from the filter bar (not a row: anywhere along it).
        drag(from: CGVector(dx: 0.85, dy: 0.5), to: CGVector(dx: 0.25, dy: 0.5 + 0.6 * 0.46 * 0.577))
        expectAgents(true, "a 30° drag to the left on 助手")
        let chips = app.buttons["agents.filter.all"].firstMatch
        let y = chips.frame.midY / app.windows.firstMatch.frame.height
        drag(from: CGVector(dx: 0.5, dy: y), to: CGVector(dx: 0.95, dy: y))
        expectAgents(false, "a drag to the right along Agents' filter bar")
        XCTAssertTrue(app.descendants(matching: .any)["shell-composer-field"].firstMatch.isHittable, "助手's composer not in reach")
    }

    @MainActor func testDragRightOnTheAssistantIsTheDrawer() {
        launch()
        drag(from: CGVector(dx: 0.3, dy: 0.45), to: CGVector(dx: 0.9, dy: 0.45))
        let close = app.descendants(matching: .any)["assistant-drawer-close"].firstMatch
        XCTAssertTrue(close.waitForExistence(timeout: 3), "a drag to the right on 助手 did not open the drawer")
        expectAgents(false, "the drawer on 助手")
        close.tap()
        XCTAssertTrue(close.waitForNonExistence(timeout: 3), "the drawer did not close")
        // And a drag to the left with the drawer out closes it, it does not page.
        drag(from: CGVector(dx: 0.3, dy: 0.45), to: CGVector(dx: 0.9, dy: 0.45))
        XCTAssertTrue(close.waitForExistence(timeout: 3), "the drawer again")
        drag(from: CGVector(dx: 0.9, dy: 0.45), to: CGVector(dx: 0.2, dy: 0.45))
        XCTAssertTrue(close.waitForNonExistence(timeout: 3), "a drag to the left did not close the drawer")
        expectAgents(false, "closing the drawer paged to Agents")
    }

    /// Agents' rows do not swipe (#1244: on the way to a chat the drag opened a row's buttons): a drag to the right along
    /// one pages back to Assistant.
    @MainActor func testADragAlongARowPagesBack() {
        launch()
        toAgents()
        let row = app.descendants(matching: .any).matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5), "no row")
        let f = row.frame, h = app.windows.firstMatch.frame
        drag(from: CGVector(dx: 0.4, dy: f.midY / h.height), to: CGVector(dx: 0.8, dy: f.midY / h.height))
        expectAgents(false, "a drag to the right along a row")
        let pin = app.buttons.matching(NSPredicate(format: "label IN {'置顶', '取消置顶'}")).firstMatch
        XCTAssertFalse(pin.exists && pin.isHittable, "a row's 置顶 came out")
        attachScreen("row drag")
    }

    /// A row ends in its ⋯ (#1244): a tap opens the row's menu, with what its swipes held, and not its chat.
    @MainActor func testARowsMoreOpensItsMenu() {
        launch()
        toAgents()
        rowMenuOpens("")
    }

    /// The compact list's rows (`CompactSlotRow`) end in their ⋯ too.
    @MainActor func testACompactRowsMoreOpensItsMenu() {
        launch(["-dev.compact", "YES"])
        toAgents()
        rowMenuOpens(" compact")
    }

    @MainActor private func rowMenuOpens(_ look: String) {
        let more = app.buttons.matching(identifier: "agents.row.more").firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 5), "no ⋯ on a row")
        attachScreen("agents rows" + look)
        more.tap()
        let pin = app.buttons.matching(NSPredicate(format: "label IN {'置顶', '取消置顶'}")).firstMatch
        XCTAssertTrue(pin.waitForExistence(timeout: 3), "the row's menu did not open")
        attachScreen("row menu" + look)
        XCTAssertTrue(app.buttons["关闭 Agent…"].exists || app.buttons["重命名…"].exists, "not the row's menu: \(app.debugDescription)")
        // Closed without choosing: still the list, no chat pushed.
        app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.12)).tap()
        XCTAssertTrue(pin.waitForNonExistence(timeout: 3), "the menu did not close")
        XCTAssertTrue(more.isHittable, "the row's chat opened")
        expectAgents(true, "the row's menu")
    }

    /// With Messages on there is a page to the left of Agents' rows too (#2822): a drag to the left along a row pages on to
    /// it, and no Close comes out of the row (#1244). Needs the mock hub: Demo Mode has no Feishu chats, so no Messages page.
    @MainActor func testADragLeftAlongARowPagesToMessages() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs mock Hub") }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-demo.on", "NO", "-ui.oldLayout", "NO", "-probes", "YES", "-newAssistant", "YES",
                               "-dev.openChat", "", "-tab", "assistant", "-integrations.lark.enabled", "YES"]
        app.launchPinned()
        XCTAssertTrue(app.buttons["assistant-sidebar-button"].firstMatch.waitForExistence(timeout: 20), "no shell")
        sleep(2)
        toAgents()
        // Terminal lists the sessions with a terminal: the rows that swiped to Close. The mock's cards that need you fill
        // the first screen.
        let terminal = app.buttons["agents.filter.terminal"].firstMatch
        XCTAssertTrue(terminal.waitForExistence(timeout: 10), "no 终端 filter")
        terminal.tap()
        let rows = app.descendants(matching: .any).matching(identifier: "agents.row")
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 10), "no row")
        let h = app.windows.firstMatch.frame
        let shown = (0..<min(rows.count, 6)).map { rows.element(boundBy: $0).frame }
            .first { $0.height > 0 && $0.minY > h.height * 0.2 && $0.maxY < h.height * 0.9 }
        guard let f = shown else { return XCTFail("no row on the screen: \(app.shellTrace)") }
        drag(from: CGVector(dx: 0.7, dy: f.midY / h.height), to: CGVector(dx: 0.3, dy: f.midY / h.height))
        wait(for: [expectation(for: NSPredicate { _, _ in !self.app.agentsDrawn() }, evaluatedWith: nil)], timeout: 4)
        XCTAssertFalse(app.buttons["关闭"].firstMatch.exists, "a row's 关闭 came out")
        attachScreen("row drag left pages to 消息")
    }

    /// A herdr tab's group row (#2668) has no swipe of its own: a drag to the right along it pages back to Assistant rather
    /// than doing nothing. Needs the mock hub with `MOCK_TAB_PANES=1` (PERSONA_MOCK_HUB_TABS).
    @MainActor func testTabGroupRowPagesBack() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB_TABS"] else { throw XCTSkip("needs the tab panes mock") }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-demo.on", "NO", "-ui.oldLayout", "NO", "-probes", "YES", "-newAssistant", "YES",
                               "-dev.openChat", "", "-tab", "assistant"]
        app.launchPinned()
        XCTAssertTrue(app.buttons["assistant-sidebar-button"].firstMatch.waitForExistence(timeout: 20), "no shell")
        sleep(2)
        toAgents()
        let tab = app.descendants(matching: .any).matching(identifier: "agents.tab").firstMatch
        XCTAssertTrue(tab.waitForExistence(timeout: 10), "no tab group row")
        if !tab.isHittable { app.swipeUp(velocity: .slow) }
        let h = app.windows.firstMatch.frame, y = tab.frame.midY / h.height
        drag(from: CGVector(dx: 0.4, dy: y), to: CGVector(dx: 0.9, dy: y))
        expectAgents(false, "a drag to the right along a tab's group row")
    }

    @MainActor func testPushedPagesSwipeBack() {
        launch()
        // Assistant: Knowledge from the drawer, the edge swipe goes back to Assistant (not Agents, not the drawer).
        app.buttons["assistant-sidebar-button"].firstMatch.tapCenter()
        XCTAssertTrue(app.buttons["sidebar-知识库"].firstMatch.waitForExistence(timeout: 5), "no 知识库 in the drawer")
        app.buttons["sidebar-知识库"].firstMatch.tap()
        let kb = app.navigationBars["知识库"]
        XCTAssertTrue(kb.waitForExistence(timeout: 5), "知识库 not pushed")
        sleep(1)
        edgeSwipeBack()
        XCTAssertTrue(kb.waitForNonExistence(timeout: 5), "the swipe did not go back from 知识库")
        expectAgents(false, "back from 知识库")
        XCTAssertFalse(drawerOpen, "the drawer came out instead of going back")
        // Agents: a chat pushed, the edge swipe goes back to the list (not to Assistant).
        toAgents()
        let row = app.descendants(matching: .any).matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5), "no row")
        row.tap()
        XCTAssertTrue(app.buttons["agents-new"].firstMatch.waitForNonExistence(timeout: 5), "the chat did not open")
        sleep(1)
        edgeSwipeBack()
        XCTAssertTrue(app.buttons["agents-new"].firstMatch.waitForExistence(timeout: 5),
                      "the swipe did not go back to the list: [\(app.shellPage)] \(app.shellTrace)")
        expectAgents(true, "back from a chat on Agents")
    }

    @MainActor func testScrollingDoesNotPage() {
        launch()
        toAgents()
        app.swipeUp()
        app.swipeDown()
        expectAgents(true, "the list scrolled up and down")
        // Steeper than 45° is the list's, either way.
        drag(from: CGVector(dx: 0.3, dy: 0.7), to: CGVector(dx: 0.5, dy: 0.35))
        drag(from: CGVector(dx: 0.6, dy: 0.35), to: CGVector(dx: 0.4, dy: 0.7))
        expectAgents(true, "a steep drag on the list")
    }

    /// Stills of the strip part way (`-showShellPagerAt`), for the PR: both pages beside each other, the switch's pill
    /// between them, the top bar in place.
    @MainActor func testShotsPartWay() {
        for at in ["0.3", "0.7"] {
            launch(["-showShellPagerAt", at])
            sleep(2)
            attachScreen("strip at \(at)")
            app.terminate()
        }
    }
}
