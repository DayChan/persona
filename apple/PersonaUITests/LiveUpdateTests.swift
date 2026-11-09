import XCTest

/// What the user is in the middle of survives the hub's live updates (2026-10-31, the user: the Agents ⋯ menu jumped
/// while choosing a machine, a screenshot being looked at in a chat's work closed itself). The mock hub pushes a busy
/// hub's updates on demand (GET /mock/churn: sessions with new lines and a moving order, the machine list, a new step
/// in each open conversation); the app counts view bodies and main-thread time per push (`-perfCounts YES`).
///   PORT=18797 MOCK_AGENTS=1 MOCK_CHURN=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_CHURN_HUB=http://127.0.0.1:18797 xcodebuild test … -only-testing:PersonaUITests/LiveUpdateTests
final class LiveUpdateTests: XCTestCase {
    private var hub = ""
    private var app: XCUIApplication!
    private static let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    private static let rounds = 12

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_CHURN_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AGENTS=1 MOCK_CHURN=1 (PERSONA_CHURN_HUB)")
        }
        hub = h
        _ = mock("reset")
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(screenshot: app.screenshot())
            a.name = "failure"
            a.lifetime = .keepAlways
            add(a)
        }
        super.tearDown()
    }

    /// `chat`: opened as the chat left open last time. A cold start restores it only when the tab saved with it is
    /// Agents (`app.tab`), so that is given too: otherwise the tab the last run ended on (another test's) decided it.
    private func launch(chat: String = "", _ extra: [String] = []) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-dev.openChat", chat, "-tab", "dev", "-openAgentsFresh", "YES", "-openChatsFresh", "YES", "-perfCounts", "YES"]
            + extra
        app.launchPinned()
    }

    @discardableResult
    private func mock(_ what: String) -> [String: Any] {
        let done = expectation(description: what)
        var out: [String: Any] = [:]
        URLSession.shared.dataTask(with: URL(string: hub + "/mock/" + what)!) { data, _, _ in
            out = (data.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any]) ?? [:]
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return out
    }

    /// Pushes `rounds` rounds of updates, 400 ms apart, and waits for the last. `steps`: new steps per round in each
    /// open conversation.
    private func churn(steps: Int = 1, final: Bool = false, waiting: Bool = false) {
        mock("churn?n=\(Self.rounds)&ms=400&steps=\(steps)&final=\(final ? 1 : 0)&inbox=1&waiting=\(waiting ? 1 : 0)")
        let deadline = Date.now.addingTimeInterval(30)
        while (mock("churned")["rounds"] as? Int ?? 0) < Self.rounds + (final ? 1 : 0), Date.now < deadline { usleep(300_000) }
        sleep(1) // the last round applied
    }

    private struct Counts {
        var bodies: [String: Int]
        var events: Int
        var passes: Int
        var ms: Double
        var max: Double
    }

    private func counts() -> Counts {
        let probe = app.staticTexts["perf.counts"]
        XCTAssertTrue(probe.waitForExistence(timeout: 5), "perf.counts (launched with -perfCounts YES)")
        let d = (try? JSONSerialization.jsonObject(with: Data(probe.label.utf8))) as? [String: Any] ?? [:]
        return Counts(bodies: d["bodies"] as? [String: Int] ?? [:], events: d["events"] as? Int ?? 0,
                      passes: d["passes"] as? Int ?? 0, ms: d["ms"] as? Double ?? 0, max: d["max"] as? Double ?? 0)
    }

    /// Bodies and main-thread time the churn cost, per round of pushes; attached to the test and printed.
    private func report(_ name: String, from a: Counts, to b: Counts) {
        let n = Double(Self.rounds)
        var lines = [String(format: "%@: per round (%d pushes in %d main-thread passes overall) %.1f ms on the main thread, slowest pass %.1f ms",
                            name, b.events - a.events, b.passes - a.passes, (b.ms - a.ms) / n, b.max)]
        for (k, v) in b.bodies.sorted(by: { $0.key < $1.key }) where v - (a.bodies[k] ?? 0) > 0 {
            lines.append(String(format: "  %@ %.1f", k, Double(v - (a.bodies[k] ?? 0)) / n))
        }
        let text = lines.joined(separator: "\n")
        print("PERF " + text.replacingOccurrences(of: "\n", with: "\nPERF "))
        let att = XCTAttachment(string: text)
        att.name = "perf " + name
        att.lifetime = .keepAlways
        add(att)
    }

    // The ⋯ menu over the Agents list stays open where it was while updates come in: it was rebuilt on every push
    // (the machine list, the sections it folds) and jumped, or scrolled itself, under the finger choosing a machine.
    func testListMenuHoldsStillThroughUpdates() {
        launch()
        // Both retained pages carry the switch: use the one that can be tapped.
        XCTAssertTrue(app.buttons["shell-agents"].firstMatch.waitForExistence(timeout: 15), "the list's options")
        let shells = app.buttons.matching(identifier: "shell-agents").allElementsBoundByIndex
        let menuButton = shells.first(where: { $0.isHittable }) ?? app.buttons["shell-agents"].firstMatch
        sleep(2)
        menuButton.tap()
        let machines = app.buttons["全部机器"]
        XCTAssertTrue(machines.waitForExistence(timeout: 5), "the menu opened")
        if !machines.isHittable {
            // The long native menu exposes offscreen rows. Drag inside its visible bounds, not an empty row frame.
            let first = app.buttons["按仓库"]
            let display = app.buttons["细列表"]
            XCTAssertTrue(first.isHittable && display.isHittable, "visible menu rows for the scroll gesture")
            let origin = app.windows.firstMatch.coordinate(withNormalizedOffset: .zero)
            let start = origin.withOffset(CGVector(dx: display.frame.midX, dy: display.frame.midY))
            let end = origin.withOffset(CGVector(dx: first.frame.midX, dy: first.frame.midY))
            start.press(forDuration: 0.05, thenDragTo: end, withVelocity: 600, thenHoldForDuration: 0)
        }
        XCTAssertTrue(machines.isHittable, "machine choices must really be on screen")
        sleep(1)
        let last = app.buttons["训练机"]
        XCTAssertTrue(last.isHittable, "the last machine must really be on screen")
        let lastY = last.frame.minY
        let machinesY = machines.frame.minY
        let before = counts()
        churn(waiting: true)
        // Where the menu is, before the counts: reading the probe while the menu is open moves the menu itself.
        let open = last.exists
        let (lastAfter, machinesAfter) = (last.frame.minY, machines.frame.minY)
        let after = counts()
        report("Agents list, ⋯ menu open", from: before, to: after)
        XCTAssertTrue(open, "the menu is still open")
        XCTAssertEqual(lastAfter, lastY, accuracy: 1, "the menu did not scroll or jump")
        XCTAssertEqual(machinesAfter, machinesY, accuracy: 1, "the menu did not scroll or jump")
        XCTAssertLessThanOrEqual((after.bodies["ListMenu.content"] ?? 0) - (before.bodies["ListMenu.content"] ?? 0), 1,
                                 "the open menu's content is not rebuilt for each push")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.95)).tap() // close it
    }

    // A row's long-press menu stays open through updates of that very agent (it is working: a new line every push).
    @MainActor func testRowMenuHoldsStillThroughUpdates() {
        launch(["-dev.machineFilter", "macbook"])
        let row = app.buttons.matching(NSPredicate(format: "identifier == 'agents.row' AND label CONTAINS %@", "给天气 App 加主屏幕小组件")).firstMatch
        XCTAssertTrue(app.buttons["agents.filter.all"].waitForExistence(timeout: 15))
        sleep(2)
        // Below the cards that need you, out of sight and not built yet (#836).
        app.bringAboveTabBar(row)
        row.press(forDuration: 1.2)
        let rename = app.buttons["重命名…"]
        XCTAssertTrue(rename.waitForExistence(timeout: 5), "the long-press menu opened")
        let y = rename.frame.minY
        let before = counts()
        churn()
        XCTAssertTrue(rename.exists, "the long-press menu is still open")
        XCTAssertEqual(rename.frame.minY, y, accuracy: 1, "the long-press menu did not move")
        let after = counts()
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.08)).tap()
        report("Agents list, long-press menu open", from: before, to: after)
        XCTAssertEqual(after.bodies["RowMenu.content"] ?? 0, before.bodies["RowMenu.content"] ?? 0,
                       "the open long-press menu's content was not rebuilt by the pushes")
    }

    // Scrolled into the middle of the list, what is on screen stays where it is while agents above it change order.
    func testScrolledListHoldsStillThroughUpdates() {
        launch()
        XCTAssertTrue(app.buttons["agents.filter.all"].waitForExistence(timeout: 15))
        sleep(2)
        let list = app.collectionViews.firstMatch
        list.swipeUp(velocity: .slow)
        sleep(2)
        // A row in the middle of the screen, and where it is.
        let rows = app.buttons.matching(identifier: "agents.row")
        let mid = app.frame.midY
        let row = (0..<rows.count).map { rows.element(boundBy: $0) }
            .filter { $0.isHittable }.min { abs($0.frame.midY - mid) < abs($1.frame.midY - mid) }
        guard let row else { return XCTFail("no row on screen") }
        // Found again by its title: the label also has its time (「3 分钟前」), which may turn over meanwhile.
        let parts = row.label.components(separatedBy: "、")
        let title = parts.count > 1 ? "、" + parts[1] + "、" : row.label
        let y = row.frame.minY
        let before = counts()
        churn()
        let same = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", title)).firstMatch
        XCTAssertTrue(same.exists, "the row is still there")
        print("TRUTH reader row moved \(String(format: "%.1f", abs(same.frame.minY - y))) pt through the pushes")
        XCTAssertEqual(same.frame.minY, y, accuracy: 2, "the row under the reader stayed put")
        report("Agents list, scrolled to the middle", from: before, to: counts())
    }

    // A screenshot in a working agent's steps, open full screen, stays open while the agent goes on (new steps every
    // push, a word between them) and finishes: it was presented by its row, which the list rebuilds as the turn goes.
    func testImageViewerStaysOpenThroughUpdates() {
        launch(chat: Self.widget)
        let head = app.buttons["work-head"].firstMatch
        XCTAssertTrue(head.waitForExistence(timeout: 15))
        if head.label == "展开过程" { head.tap() }
        // The screenshot is the result of a tool call: open that step.
        let step = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "截一张小组件的图")).firstMatch
        if UITestStyle.isBeautiful, !step.waitForExistence(timeout: 3) {
            // The BeautifulUI style folds calls into runs of chips: open them until the screenshot's shows.
            let runs = app.buttons.matching(identifier: "tool-run")
            for i in 0..<runs.count where !step.exists {
                runs.element(boundBy: i).tap()
                _ = step.waitForExistence(timeout: 2)
            }
        }
        XCTAssertTrue(step.waitForExistence(timeout: 5), "the screenshot's step")
        step.tap()
        let image = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "图片，点按查看大图")).firstMatch
        XCTAssertTrue(image.waitForExistence(timeout: 5), "the screenshot in the work")
        image.tap()
        let save = app.buttons["保存"]
        XCTAssertTrue(save.waitForExistence(timeout: 5), "the viewer opened")
        let before = counts()
        // Many steps a round (a busy turn moves the list far past the screenshot's row), then the turn ends: its
        // last reply comes and the work line is laid out again.
        churn(steps: 6, final: true)
        XCTAssertTrue(save.exists, "the viewer is still open")
        app.buttons["关闭"].tap()
        sleep(1)
        report("Chat, image viewer open", from: before, to: counts())
    }

    // The open work card of a working agent stays open while steps and words come in.
    func testOpenWorkStaysOpenThroughUpdates() {
        launch(chat: Self.widget)
        let head = app.buttons["work-head"].firstMatch
        XCTAssertTrue(head.waitForExistence(timeout: 15))
        if head.label == "展开过程" { head.tap() }
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label == %@", "收起过程")).firstMatch.waitForExistence(timeout: 5))
        let before = counts()
        churn()
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label == %@", "收起过程")).firstMatch.exists, "the work card is still open")
        report("Chat, work card open", from: before, to: counts())
    }

    // Typing into the search field: the text and the keyboard stay while the list behind it changes. The field
    // starts folded away; a pull shows it.
    @MainActor func testSearchKeepsWhatIsTypedThroughUpdates() {
        launch()
        let field = app.revealAgentsSearch()
        field.tap()
        field.typeText("platform")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        let before = counts()
        churn()
        XCTAssertEqual(field.value as? String, "platform", "what was typed is still there")
        XCTAssertTrue(app.keyboards.firstMatch.exists, "the keyboard is still up")
        report("Agents list, searching", from: before, to: counts())
    }

    // A row's ⋯ menu (#1244: the rows do not swipe) stays open while its agent keeps working: the menu is built again
    // only when what it offers changes (`AgentRowMenu`'s ==), not on every push. In the list itself: one machine's
    // agents (the widget's), so the row is near the top.
    @MainActor func testARowMenuStaysOpenThroughUpdates() {
        launch(["-dev.machineFilter", "macbook"])
        XCTAssertTrue(app.buttons["agents.filter.all"].waitForExistence(timeout: 15))
        sleep(2)
        menuStaysOpen()
    }

    // The same for a search result's ⋯ (a search result is a button, not a link).
    @MainActor func testASearchResultsMenuStaysOpenThroughUpdates() {
        launch()
        XCTAssertTrue(app.buttons["agents.filter.all"].waitForExistence(timeout: 15))
        sleep(2)
        let field = app.revealAgentsSearch()
        field.tap()
        field.typeText("天气")
        menuStaysOpen()
    }

    @MainActor private func menuStaysOpen() {
        let more = app.buttons.matching(NSPredicate(format: "identifier == 'agents.row.more' AND label CONTAINS %@", "给天气 App 加主屏幕小组件")).firstMatch
        // The row starts below the cards that need you (#836), or under the tab bar on a smaller iPhone (#488).
        app.bringAboveTabBar(more)
        more.tap()
        let close = app.buttons["关闭 Agent…"]
        XCTAssertTrue(close.waitForExistence(timeout: 5), "the row's menu")
        churn()
        XCTAssertTrue(close.exists, "the menu is still open")
    }

    // The Agents badge in the shell is the hub's: a question coming adds
    // one within 2 s, its answer takes it off again (docs/apple/ios-state.md「第 3 步」: counted in the model, published
    // when the number changes).
    @MainActor func testTheAgentsBadgeIsTheHubs() {
        launch()
        let hubAsks = { (self.mock("truth")["asks"] as? [Any])?.count ?? -1 }
        eventually("the badge shows the hub's questions", within: 5) { self.badgeDiffers(hubAsks()) }
        let before = agentsBadge()
        mock("bump?what=ask")
        if let took = eventually("a question adds one", within: 2, { self.badgeDiffers(before + 1) }) {
            print(String(format: "TRUTH badge: a question counted %.2f s after the push", took))
        }
        mock("bump?what=resolve")
        if let took = eventually("its answer takes it off", within: 2, { self.badgeDiffers(before) }) {
            print(String(format: "TRUTH badge: an answer counted %.2f s after the push", took))
        }
    }

    // A card read off the terminal of the agent whose conversation is open is up within a second of the hub's push,
    // while the hub is busy; answered on the computer, it is gone within a second too (#751: cards are never held).
    @MainActor func testAScreenCardInTheOpenConversation() {
        launch(chat: Self.widget)
        XCTAssertTrue(app.buttons["work-head"].firstMatch.waitForExistence(timeout: 15))
        sleep(1)
        mock("churn?n=\(Self.rounds)&ms=400&steps=1&inbox=1") // a busy hub meanwhile
        let session = String(Self.widget.split(separator: "/")[1])
        for round in 1...3 {
            guard let question = mock("bump?what=screen&session=" + session)["question"] as? String else {
                return XCTFail("the mock made no card")
            }
            let card = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", question)).firstMatch
            if let took = eventually("round \(round): the card is up", within: 1, poll: 0.05, { card.exists ? [] : ["no 「\(question)」"] }) {
                print(String(format: "TRUTH card: up %.2f s after the push", took))
            }
            mock("bump?what=resolve")
            if let took = eventually("round \(round): the card is gone", within: 1, poll: 0.05, { card.exists ? ["「\(question)」 still there"] : [] }) {
                print(String(format: "TRUTH card: gone %.2f s after the answer on the computer", took))
            }
        }
    }

    /// The Agents badge's number: the shell's (「N 个需要你」, the whole label: a space's header ends the same way) or the Agents tab's value; 0 when there is none.
    private func agentsBadge() -> Int {
        let shell = app.descendants(matching: .any).matching(NSPredicate(format: "label MATCHES %@", "[0-9]+ 个需要你")).firstMatch
        let text = shell.exists ? shell.label : ""
        return Int(text.prefix(while: \.isNumber)) ?? 0
    }

    private func badgeDiffers(_ want: Int) -> [String] {
        let got = agentsBadge()
        return got == want ? [] : ["badge \(got), hub \(want)"]
    }

}
