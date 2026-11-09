import XCTest

/// Expand in the shell's composer (#5685): a draft grows to its viewport-dependent line limit, then offers an icon in
/// the text area's upper-right corner. The full-page editor holds the same text and quotes; edits and cursor position
/// survive collapse, and Send sends once. The assistant's and an agent's conversation, keyboard up and down.
/// Against scripts/mock/composer_hub.py (TEST_RUNNER_PERSONA_MOCK_HUB=…). Screenshots are kept as xcresult attachments.
final class ComposerExpandTests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private let agent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    private let assistant = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        // TEST_RUNNER_PERSONA_ORIENTATION=landscape: the same on its side (the folded iPhone Duo's side status bar).
        if ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape" { XCUIDevice.shared.orientation = .landscapeLeft }
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let tree = XCTAttachment(string: app.debugDescription)
            tree.name = "composer tree"
            tree.lifetime = .keepAlways
            add(tree)
        }
        if ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape" { XCUIDevice.shared.orientation = .portrait }
        super.tearDown()
    }

    @MainActor func testAnAgentsDraftOpensFullPage() { expands(isAssistant: false) }

    @MainActor func testTheAssistantsDraftOpensFullPage() { expands(isAssistant: true) }

    /// Discover the real threshold rather than assuming a device-specific line count. Capture the four acceptance states.
    @MainActor func testAdaptiveThresholdAndFourStates() {
        open(isAssistant: true)
        if ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape" {
            XCTAssertGreaterThan(app.frame.width, app.frame.height, "the app did not actually rotate to landscape")
        }
        startTyping()
        field.typeText("Draft line 1")
        sleep(1)
        XCTAssertTrue(expand.exists)
        let oneLineHeight = field.frame.height
        field.typeText("\nDraft line 2")
        sleep(1)
        let landscape = ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape"
        XCTAssertTrue(expand.exists, "expand button should be offered while focused or multiline")
        XCTAssertGreaterThan(field.frame.height, oneLineHeight, "typing another line did not grow the field")
        shot("01-below-threshold")

        var draft = "Draft line 1\nDraft line 2"
        var threshold = 2
        var prevHeight = field.frame.height
        for count in 3...8 {
            let next = "\nDraft line \(count)"
            field.typeText(next)
            draft += next
            sleep(1)
            let h = field.frame.height
            if h > prevHeight + 2 {
                threshold = count
                prevHeight = h
            } else {
                break
            }
        }
        XCTAssertTrue(expand.exists, "no expand icon at the maximum line count")
        print("RESULT: composer threshold=\(threshold), orientation=\(landscape ? "landscape" : "portrait"), language=\(language)")
        if let expected = ProcessInfo.processInfo.environment["PERSONA_EXPECTED_LINES"].flatMap(Int.init) {
            XCTAssertEqual(threshold, expected, "the viewport did not select the expected line limit")
        }
        assertCorner()
        XCTAssertEqual(field.value as? String, draft)
        shot("02-at-threshold")
        let cappedHeight = field.frame.height
        field.typeText("\nExtra scrolling line\nOne more scrolling line")
        sleep(1)
        XCTAssertEqual(field.frame.height, cappedHeight, accuracy: 2, "the field grew beyond its line limit")
        let scrollingDraft = draft + "\nExtra scrolling line\nOne more scrolling line"
        XCTAssertEqual(field.value as? String, scrollingDraft)

        expand.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 5))
        XCTAssertEqual(page.value as? String, scrollingDraft)
        let collapse = element("composer-page-collapse")
        let pageDismiss = element("composer-page-dismiss-keyboard")
        let send = element("composer-page-send")
        XCTAssertEqual(collapse.label, collapseLabel)
        XCTAssertTrue(pageDismiss.exists)
        XCTAssertGreaterThan(collapse.frame.midX, app.frame.midX, "collapse is not in the upper-right corner")
        XCTAssertGreaterThan(send.frame.minY, page.frame.midY, "send is not below the page editor")
        shot("03-expanded")
        collapse.tap()
        XCTAssertTrue(page.waitForNonExistence(timeout: 5))
        XCTAssertEqual(field.value as? String, scrollingDraft, "collapse changed the draft")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        assertCorner()
        shot("04-collapsed")
        field.typeText("!")
        XCTAssertEqual(field.value as? String, scrollingDraft + "!", "the cursor did not return to the end")

        // Delete back to a single line and dismiss the keyboard: the expand icon goes away when unfocused and single-line.
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: scrollingDraft.count + 1 - "Draft line 1".count))
        XCTAssertEqual(field.value as? String, "Draft line 1")
        XCTAssertEqual(field.frame.height, oneLineHeight, accuracy: 2)
        element("shell-composer-dismiss-keyboard").tap()
        XCTAssertTrue(expand.waitForNonExistence(timeout: 5), "the expand icon survived dismissal on a short single-line draft")
        shot("05-deleted-below-threshold")
        leaveEmpty()
    }

    @MainActor private func assertCorner() {
        XCTAssertTrue(expand.isHittable)
        XCTAssertEqual(expand.label, expandLabel)
        XCTAssertGreaterThan(expand.frame.minX, field.frame.midX)
        XCTAssertLessThan(expand.frame.midY, field.frame.midY, "the icon is not at the top of the text area")
        XCTAssertLessThan(expand.frame.maxY, element("shell-composer-add").frame.minY + 1, "expand moved into the bottom toolbar")
        XCTAssertLessThanOrEqual(expand.frame.maxY, app.keyboards.firstMatch.frame.minY + 1)
    }

    private var language: String { ProcessInfo.processInfo.environment["PERSONA_UI_LANGUAGE"] ?? "zh-Hans" }
    private var expandLabel: String {
        ["zh-Hans": "展开输入框", "en": "Expand the message box", "ja": "入力欄を広げる"][language]!
    }
    private var collapseLabel: String {
        ["zh-Hans": "收起输入框", "en": "Collapse the message box", "ja": "入力欄を縮める"][language]!
    }

    /// One paragraph, no newline, wrapped past the viewport's threshold: Expand all the same.
    @MainActor func testALongParagraphOffersIt() {
        open(isAssistant: true)
        startTyping()
        let line = String(repeating: "a long paragraph wraps ", count: 9) + "end"
        field.typeText(line)
        XCTAssertTrue(expand.waitForExistence(timeout: 5), "no 展开 for a wrapped paragraph")
        XCTAssertFalse((field.value as? String ?? "").contains("\n"))
        expand.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 5))
        XCTAssertEqual(page.value as? String, line)
        shot("assistant-paragraph-page")
        element("composer-page-collapse").tap()
        XCTAssertTrue(page.waitForNonExistence(timeout: 5))
        leaveEmpty()
    }

    /// The cursor goes over and back where it is, not at the end: an edit in the middle on the page, then typing on in
    /// the box goes on from there. A swipe down keeps what was typed on the page, as Collapse does.
    @MainActor func testTheCursorAndASwipeDownCarryOver() {
        // With the quotes: they put the page's text below its bar, where a tap lands in the text (without them the
        // text view starts under the bar).
        open(isAssistant: false, quotes: true)
        startTyping()
        field.typeText((1...8).map { "line \($0)" }.joined(separator: "\n"))
        XCTAssertTrue(expand.waitForExistence(timeout: 5))
        expand.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 5))
        sleep(1)
        // In the second line: the cursor goes to a word's start or end there, mid-text either way (hardware arrow keys
        // would be exact, but leave the iPad simulator in its hardware-keyboard mode, the on-screen one gone).
        page.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 30, dy: 50)).tap()
        sleep(1)
        page.typeText("X")
        let edited = page.value as? String ?? ""
        XCTAssertTrue(edited.contains("X") && !edited.hasSuffix("X"),
                      "the tap did not put the cursor mid-text: \(edited.debugDescription)")
        let mid = edited.components(separatedBy: "X")[0] // what is before the cursor
        element("composer-page-collapse").tap()
        XCTAssertTrue(page.waitForNonExistence(timeout: 5))
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        field.typeText("Y")
        let after = field.value as? String ?? ""
        XCTAssertTrue(after.hasPrefix(mid + "XY"), "the box's cursor is not where the page left it: \(after.debugDescription)")
        shot("agent-cursor-mid")

        // A swipe down on the page's bar: the page goes, what was typed there stays.
        guard drags else { return leaveEmptyWithoutQuotes() }
        expand.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 5))
        sleep(1)
        page.typeText(" swiped")
        let bar = element("composer-page-collapse").frame
        let from = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: app.frame.midX, dy: bar.midY))
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: app.frame.height * 0.7)),
                   withVelocity: .fast, thenHoldForDuration: 0)
        XCTAssertTrue(page.waitForNonExistence(timeout: 5), "the swipe did not take the page down")
        XCTAssertTrue((field.value as? String ?? "").hasPrefix(mid + "XY swiped"), "the swipe lost the page's edit: \(String(describing: field.value))")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "the keyboard did not come back to the box")
        shot("agent-swiped")
        leaveEmptyWithoutQuotes()
    }

    /// `leaveEmpty`, and the quotes taken off: unsent, they would stay with the conversation for the next test.
    @MainActor private func leaveEmptyWithoutQuotes() {
        leaveEmpty()
        removeQuotes()
    }

    @MainActor private func removeQuotes() {
        for _ in 1...4 {
            guard let x = app.buttons.matching(identifier: "取消引用").allElementsBoundByIndex.first(where: \.isHittable) else { break }
            x.tap()
            sleep(1)
        }
    }

    /// ⌘⏎ (a hardware keyboard) on the page sends from the page and takes it down, once: the box's own ⌘⏎ under the
    /// sheet is off meanwhile.
    @MainActor func testCommandReturnSendsFromThePage() throws {
        // On the iOS 26.5 simulators here (Xcode 27.2) a synthesized ⌘⏎ never reached the page's shortcut; run it where
        // the simulator has a hardware keyboard: TEST_RUNNER_PERSONA_HARDWARE_KEYS=1.
        guard ProcessInfo.processInfo.environment["PERSONA_HARDWARE_KEYS"] == "1" else {
            throw XCTSkip("needs a hardware keyboard on the simulator (PERSONA_HARDWARE_KEYS=1)")
        }
        open(isAssistant: false)
        startTyping()
        let marker = "expand-cmd-return-\(Int(Date.now.timeIntervalSince1970))"
        field.typeText((1...7).map { "line \($0)" }.joined(separator: "\n") + "\n" + marker)
        XCTAssertTrue(expand.waitForExistence(timeout: 5))
        expand.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 5))
        sleep(1)
        page.typeKey("z", modifierFlags: []) // does a synthesized hardware key reach the text view at all?
        let probe = page.value as? String ?? ""
        page.typeKey(XCUIKeyboardKey.return.rawValue, modifierFlags: .command)
        XCTAssertTrue(page.waitForNonExistence(timeout: 5),
                      "⌘⏎ left the page up (a plain key \(probe.hasSuffix("z") ? "did" : "did not") arrive): \((page.value as? String ?? "").debugDescription)")
        let cleared = NSPredicate(format: "value == '' OR value == %@", field.placeholderValue ?? "")
        expectation(for: cleared, evaluatedWith: field)
        waitForExpectations(timeout: 8)
        assertSentOnce(marker)
    }

    /// TEST_RUNNER_PERSONA_NO_DRAG=1: no synthesized drags (the unfolded iPhone Duo upright, where XCUITest takes the
    /// screen for portrait upside down and a drag lands elsewhere); the steps behind them are left out.
    private var drags: Bool { ProcessInfo.processInfo.environment["PERSONA_NO_DRAG"] != "1" }

    private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any).matching(identifier: id).firstMatch }
    private var field: XCUIElement { element("shell-composer-field") }
    private var expand: XCUIElement { element("shell-composer-expand") }
    private var page: XCUIElement { element("composer-page-field") }

    /// `quotes`: `-showSampleQuotes`, only where the test sends (unsent, they stay with the conversation for the next).
    @MainActor private func open(isAssistant: Bool, quotes: Bool = false) {
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app = XCUIApplication()
        // Pair first on its own: a first pairing resets the route arguments (as ComposerKeyboardTests).
        app.launchArguments = ["-pairURL", pair, "-AppleLanguages", "(\(language))"]
        app.launchPinned()
        XCTAssertTrue(app.buttons["Persona"].waitForExistence(timeout: 15) || field.waitForExistence(timeout: 15))
        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-AppleLanguages", "(\(language))", "-holdChromeOpen", "YES", "-showSampleQuotes", quotes ? "YES" : "NO",
                               "-dev.openChat", "", "-app.tab", isAssistant ? "assistant" : "chats",
                               "-tab", isAssistant ? "assistant" : "dev"]
        app.launchArguments += isAssistant ? ["-openAssistant", assistant] : ["-openSession", agent]
        app.launchPinned()
        XCTAssertTrue(field.waitForExistence(timeout: 20), "the conversation did not open:\n\(app.debugDescription)")
        if quotes {
            sleep(4) // `-showSampleQuotes` puts its quotes over the field after 3 s.
        } else {
            sleep(2)
            // Failed earlier runs can leave quotes in the persisted draft; the no-quotes cases start clean.
            removeQuotes()
        }
    }

    /// The keyboard on the box, the box empty (drafts are kept across runs).
    @MainActor private func startTyping() {
        // A second tap when the first only closed something over the box (the unfolded Duo's sidebar, left open).
        for _ in 1...2 where !app.keyboards.firstMatch.exists {
            field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.9)).tap()
            _ = app.keyboards.firstMatch.waitForExistence(timeout: 4)
        }
        XCTAssertTrue(app.keyboards.firstMatch.exists, "no keyboard")
        clear(field)
    }

    /// No draft left for the next test (it would open as the card); drafts are saved a moment after the last change.
    @MainActor private func leaveEmpty() {
        field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.9)).tap()
        clear(field)
        sleep(2)
    }

    @MainActor private func expands(isAssistant: Bool) {
        open(isAssistant: isAssistant, quotes: true)
        let name = isAssistant ? "assistant" : "agent"
        startTyping()

        // Two lines: the box is multiline and offers expansion.
        field.typeText("line one\nline two")
        sleep(1)
        XCTAssertTrue(expand.exists, "展开 offered for multiline draft")
        // Eight lines exceed either keyboard-visible threshold and still offer expansion with the keyboard down.
        let marker = "expand-\(name)-\(Int(Date.now.timeIntervalSince1970))"
        let rest = (3...7).map { "line \($0)" }.joined(separator: "\n") + "\n" + marker
        field.typeText("\n" + rest)
        XCTAssertTrue(expand.waitForExistence(timeout: 5), "no expand icon for eight lines")
        assertCorner()
        shot("\(name)-bar-long")
        let draft = "line one\nline two\n" + rest
        XCTAssertEqual(field.value as? String, draft)

        // Expand: the page, filling the window, with the same text and the quotes.
        expand.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 5), "the full-page editor did not open")
        sleep(1)
        XCTAssertEqual(page.value as? String, draft, "the page shows another text than the box")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "the page's editor has no keyboard")
        // The editor reaches its footer, and the footer reaches the keyboard; neither overlaps the other.
        let bottom = min(app.keyboards.firstMatch.frame.minY, app.frame.maxY)
        let pageSend = element("composer-page-send")
        XCTAssertGreaterThan(pageSend.frame.maxY, bottom - 80, "the send control stops short of the keyboard")
        XCTAssertGreaterThan(page.frame.maxY, pageSend.frame.minY - 32, "the editor stops short of its footer")
        XCTAssertLessThanOrEqual(page.frame.maxY, pageSend.frame.minY + 1, "the editor overlaps its footer")
        shot("\(name)-page")
        let quoteButtons = element("composer-page-quotes").buttons.matching(identifier: "取消引用")
        // Existence precedes hit testing during the sheet animation, especially on a busy simulator.
        for _ in 1...5 {
            if quoteButtons.allElementsBoundByIndex.contains(where: { $0.isHittable && $0.frame.minY < page.frame.minY }) { break }
            sleep(1)
        }
        XCTAssertTrue(quoteButtons.allElementsBoundByIndex.contains { $0.isHittable && $0.frame.minY < page.frame.minY },
                      "the quotes are not on the page")
        XCTAssertTrue(element("composer-page-collapse").isHittable && element("composer-page-send").isHittable)
        XCTAssertEqual(element("composer-page-collapse").label, collapseLabel)
        XCTAssertEqual(element("composer-page-send").label, ["zh-Hans": "发送", "en": "Send", "ja": "送信"][language]!)

        // An edit at the cursor (the end, where the box left it), then Collapse: the box has it, the keyboard on the box.
        page.typeText(" edited")
        element("composer-page-collapse").tap()
        XCTAssertTrue(page.waitForNonExistence(timeout: 5), "收起 left the page up")
        XCTAssertEqual(field.value as? String, draft + " edited", "the edit did not reach the box")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "the keyboard did not come back to the box")
        field.typeText("!") // the cursor is still at the end
        XCTAssertEqual(field.value as? String, draft + " edited!", "the cursor moved")
        shot("\(name)-collapsed")

        // Keyboard down: Expand is still there, and the page's Send sends what it shows.
        if drags { putKeyboardAway(name) }
        expand.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 5))
        sleep(1)
        XCTAssertEqual(page.value as? String, draft + " edited!")
        shot("\(name)-page-again")
        element("composer-page-send").tap()
        XCTAssertTrue(page.waitForNonExistence(timeout: 5), "发送 left the page up")
        let cleared = NSPredicate(format: "value == '' OR value == %@", field.placeholderValue ?? "")
        expectation(for: cleared, evaluatedWith: field)
        waitForExpectations(timeout: 8)
        assertSentOnce(marker, quotes: true)
        XCTAssertFalse(expand.exists, "展开 still offered for an empty box")
        shot("\(name)-sent")
    }

    /// Drags the chat down until the keyboard goes; Expand must still be there.
    @MainActor private func putKeyboardAway(_ name: String) {
        for y in [0.2, 0.135, 0.1] where app.keyboards.firstMatch.exists { // a drag the list took as a scroll's end leaves it up
            // High on the page: lower down, over the quotes and a long draft, the assistant's task chips (a sideways
            // scroll) took the drag. On the folded Duo's short screen only a strip of the chat is left under its bar.
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: y))
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: 160)), withVelocity: .default,
                       thenHoldForDuration: 0.1)
            _ = app.keyboards.firstMatch.waitForNonExistence(timeout: 3)
        }
        XCTAssertFalse(app.keyboards.firstMatch.exists, "the drag did not put the keyboard away")
        sleep(1)
        XCTAssertTrue(expand.isHittable, "展开 gone with the keyboard")
        shot("\(name)-bar-keyboard-down")
    }

    /// The mock's record of sends (`/mock/sent`) has exactly one message with `marker` in it, still one a few seconds
    /// later; `quotes`: with the quotes `-showSampleQuotes` put over the box.
    private func assertSentOnce(_ marker: String, quotes: Bool = false, file: StaticString = #filePath, line: UInt = #line) {
        func matching() -> [String] {
            guard let data = try? Data(contentsOf: URL(string: hub + "/mock/sent")!),
                  let all = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["sent"] as? [Any] else { return [] }
            return all.compactMap { entry in
                guard let d = try? JSONSerialization.data(withJSONObject: entry), let s = String(data: d, encoding: .utf8) else { return nil }
                return s.contains(marker) ? s : nil
            }
        }
        let deadline = Date.now.addingTimeInterval(20)
        while matching().isEmpty, Date.now < deadline { sleep(1) }
        sleep(6) // a second send would have come by now
        let got = matching()
        XCTAssertEqual(got.count, 1, "the hub got \(got.count) messages sent from the page: \(got)", file: file, line: line)
        if quotes {
            XCTAssertTrue(got.first?.contains("WidgetKit") == true, "the quotes did not go with it: \(got)", file: file, line: line)
        }
    }

    /// Empties the field (drafts are kept across runs). A long draft can leave the cursor short of its end (the box
    /// squeezed, scrolled to the middle): again from its end until nothing is left.
    @MainActor private func clear(_ field: XCUIElement) {
        for _ in 1...3 {
            let value = field.value as? String ?? ""
            let typed = value == field.placeholderValue ? 0 : value.count
            guard typed > 0 else { return }
            field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.9)).tap()
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: typed + 2))
        }
    }

    @MainActor private func shot(_ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        let device = (UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone")
            + (ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape" ? "-landscape" : "")
        try? screenshot.pngRepresentation
            .write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(device)-\(UITestStyle.name)-\(appearance)-\(name).png"))
    }
}
