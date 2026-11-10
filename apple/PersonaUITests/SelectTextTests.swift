import XCTest

/// Selecting a message's words (#3189, in place since #603): long-press a message and it is lifted; a second long press
/// on it selects the word there, Select Text in its bar selects all of it, where it is, with the system's handles and
/// edit menu. A reply reads as in the chat, without the Markdown marks; your own message stays as written (its bubble
/// shows it plain: `__init__.py`, `2*3*4`, `# …`, `- …` are kept).
///   PORT=18950 MOCK_CHAT_READ=1 MOCK_SELECT_TEXT=1 MOCK_ECHO_SENT=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18950 xcodebuild test … -only-testing:PersonaUITests/SelectTextTests
/// Both styles (TEST_RUNNER_PERSONA_UI_STYLE=beautiful), iPhone and iPad; `TEST_RUNNER_PERSONA_SHOTS=<dir>` saves the
/// screens, named by the simulator's appearance (`simctl ui <udid> appearance light|dark`).
final class SelectTextTests: XCTestCase {
    private var app: XCUIApplication!
    private let agentChat = ["-tab", "dev", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-0000000c1ead"]
    private let selectChat = ["-tab", "dev", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-0000005e1ec7"]
    private let richChat = ["-tab", "dev", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-0000005e1ec8"]
    private let mainChat = ["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_CHAT_READ=1 MOCK_SELECT_TEXT=1 (PERSONA_MOCK_HUB)")
        }
    }

    @MainActor private func launch(_ args: [String]) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // Nothing kept from an earlier run: a long draft makes the composer tall and pushes the reply up under the bar,
        // and the quotes testQuoteThePickedWords sent would pile up under the messages, pushing them off screen.
        app.launchArguments = ["-pairURL", pair, "-openDraftsFresh", "YES", "-openChatsFresh", "YES"] + args
        app.launchPinned()
    }

    private var composer: XCUIElement { app.descendants(matching: .any)["shell-composer-field"].firstMatch }

    /// Points kept clear of an edge when pressing inside a frame.
    private static let inset: CGFloat = 12

    /// Nothing typed in the field (its value is then its placeholder, or only spaces and line breaks).
    @MainActor private func blank(_ field: XCUIElement) -> Bool {
        let text = field.value as? String ?? ""
        return text == field.placeholderValue || text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// Empties the composer after a short copy was read from it (a long one stays until the next launch's
    /// `-openDraftsFresh`): the cursor put at its end, then deleted back. The field is looked up each time: it is
    /// built again when it grows or shrinks, or after a system prompt.
    @MainActor private func clear() {
        let count = (composer.value as? String ?? "").count
        guard !blank(composer), count < 40 else { return }
        composer.coordinate(withNormalizedOffset: CGVector(dx: 0.97, dy: 0.92)).tap()
        composer.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: count + 4))
    }

    /// The lifted message's words while they are selected.
    private var words: XCUIElement { app.textViews["lift-text"] }

    /// The edit menu's item (a menu item or a button, by the system's version).
    private func menuItem(_ label: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "(elementType == %d OR elementType == %d) AND label == %@",
            XCUIElement.ElementType.menuItem.rawValue, XCUIElement.ElementType.button.rawValue, label)).firstMatch
    }

    /// Long-presses the message starting with `message`, where it is: its bar shows.
    @MainActor private func lift(_ message: String, shot name: String) {
        let text = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", message)).firstMatch
        XCTAssertTrue(text.waitForExistence(timeout: 20), "\(message) is not in the chat")
        acceptIconAlert(timeout: 3) // the scheme's icon
        sleep(3) // the list settles at the newest message; the header folds
        // Rows of the flipped chat list may report flipped frames: press by the centre in the window.
        let window = app.windows.firstMatch.frame
        var f = text.frame
        if !window.contains(CGPoint(x: f.midX, y: f.midY)) { f.origin.y = window.height - f.maxY }
        // Where it shows: below the top bar (a tall message can start under it; a press there goes to the bar).
        let bar = app.buttons["chat.title"].firstMatch
        let top = bar.exists ? bar.frame.maxY + Self.inset : 0
        let y = min(max(f.midY, top), f.maxY - Self.inset)
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: f.midX, dy: y)).press(forDuration: 0.6)
        XCTAssertTrue(app.buttons["选择文本"].waitForExistence(timeout: 3), "the bar has no 选择文本:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["复制"].exists, "复制 (the whole message) is gone")
        sleep(1)
        shot(name + "-bar")
    }

    /// Lifts the message and taps Select Text: all of its words are selected where they are; returns them.
    @MainActor private func openSelection(_ message: String, shot name: String) -> XCUIElement {
        lift(message, shot: name)
        // On its label, the lower part: the message above may be drawn over the bar's top and take touches there.
        app.buttons["选择文本"].coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85)).tap()
        XCTAssertTrue(words.waitForExistence(timeout: 5), "选择文本 did not select the words in place:\n\(app.debugDescription)")
        XCTAssertTrue(menuItem("拷贝").waitForExistence(timeout: 3), "no edit menu over the selected words:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons["选择文本"].exists, "the bar stayed over the edit menu")
        sleep(1)
        shot(name)
        return words
    }

    /// Double-taps the first word (`word`, at the start of the first line), copies it from the edit menu and checks the
    /// pasteboard holds exactly that word; Copy puts the message back.
    @MainActor private func copyFirstWord(of page: XCUIElement, _ word: String) {
        UIPasteboard.general.string = "-"
        page.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 12, dy: 10)).doubleTap()
        let copy = menuItem("拷贝")
        XCTAssertTrue(copy.waitForExistence(timeout: 3), "no edit menu after a double tap:\n\(app.debugDescription)")
        shot("selected")
        copy.tap()
        sleep(1)
        XCTAssertEqual(pasted(), word)
        XCTAssertTrue(page.waitForNonExistence(timeout: 3), "拷贝 did not put the message back")
    }

    /// What the app put on the pasteboard, pasted into the composer from its edit menu: a paste the user taps asks
    /// nothing (the runner reading the app's copy itself is not allowed here, as in PendingBubbleMenuTests). The
    /// composer is emptied again, so no draft is left for the next test.
    @MainActor private func pasted() -> String {
        let field = composer
        XCTAssertTrue(field.waitForExistence(timeout: 5), "no composer:\n\(app.debugDescription)")
        field.tap()
        field.press(forDuration: 1.0)
        let paste = menuItem("粘贴")
        XCTAssertTrue(paste.waitForExistence(timeout: 3), "no 粘贴 in the composer's edit menu:\n\(app.debugDescription)")
        paste.tap()
        sleep(1)
        let got = composer.value as? String ?? ""
        clear()
        return got
    }

    /// Beside the lifted message, on the scrim: below the status bar, whose taps never reach the app.
    @MainActor private func tapBeside() {
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.12)).tap()
    }

    /// A reply with a fence inside a longer fence, a formula, indented code, a rule and a tab-indented item: the
    /// marks go, those stay as written; one word copies as itself.
    @MainActor func testCopyAWordOfAReply() throws {
        launch(selectChat)
        let page = openSelection("Yes", shot: "reply")
        XCTAssertEqual(page.value as? String, "Yes，2*3*4 没错，是 24。例子：\n\n```swift\nlet a = x*y*z\n```\n\n"
                       + "公式 $x*y*z$ 照原样。\n\nindented *code*\n\n* * *\n\n•\t一\n◦\ttab item")
        copyFirstWord(of: page, "Yes")
    }

    /// A second long press on the lifted message selects the word under it, where it is: handles, the edit menu. A
    /// tap beside it brings the bar back; one more closes it. (Any tap in the words clears the selection, which ends
    /// the selecting too: a range is made with the handles, `testDragTheEndForAPart`.)
    @MainActor func testASecondLongPressSelectsTheWordThere() throws {
        launch(richChat)
        lift("迁移计划", shot: "rich")
        let lead = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "先把三个页面")).firstMatch
        XCTAssertTrue(lead.exists, "the lifted reply's first paragraph is not on screen:\n\(app.debugDescription)")
        let f = lead.frame
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: f.minX + 40, dy: f.midY)).press(forDuration: 0.8)
        XCTAssertTrue(words.waitForExistence(timeout: 5), "a second long press did not select in place:\n\(app.debugDescription)")
        XCTAssertTrue(menuItem("拷贝").waitForExistence(timeout: 3), "no edit menu:\n\(app.debugDescription)")
        XCTAssertTrue(menuItem("引用").exists, "引用 is not in the edit menu")
        XCTAssertFalse(app.buttons["选择文本"].exists, "the bar stayed over the edit menu")
        let all = words.value as? String ?? ""
        XCTAssertTrue(all.hasPrefix("迁移计划\n\n先把三个页面的数据请求合到一起"), all)
        XCTAssertTrue(all.contains("export async function load(page: string) {"), "the code is not there: \(all)")
        XCTAssertFalse(all.contains("```") || all.contains("**"), "Markdown marks are shown: \(all)")
        sleep(1)
        shot("pressed")
        // Beside the words: the bar again, the message still lifted.
        tapBeside()
        XCTAssertTrue(words.waitForNonExistence(timeout: 3), "a tap beside the words did not end the selecting")
        XCTAssertTrue(app.buttons["选择文本"].waitForExistence(timeout: 3), "the bar did not come back")
        tapBeside()
        XCTAssertTrue(app.buttons["选择文本"].waitForNonExistence(timeout: 3), "a second tap did not put the message back")
    }

    /// Select Text on a long reply with a table and code: all of it is selected where it is; Copy copies the table as
    /// its Markdown, the rest as it reads.
    @MainActor func testSelectTextSelectsAllOfALongReply() throws {
        launch(richChat)
        let page = openSelection("迁移计划", shot: "rich-all")
        UIPasteboard.general.string = "-"
        menuItem("拷贝").tap()
        XCTAssertTrue(page.waitForNonExistence(timeout: 3), "拷贝 did not put the message back")
        let copied = pasted()
        XCTAssertTrue(copied.hasPrefix("迁移计划\n\n先把三个页面"), copied)
        XCTAssertTrue(copied.contains("| 价格页 | 3 次请求 | 1 次 | 中：缓存键要改 |"), "the table is not copied as Markdown: \(copied)")
        XCTAssertTrue(copied.hasSuffix("有变化再告诉你。"), copied)
    }

    /// Select Text on the long reply, then its end handle dragged up to the end of 「步骤：」: that part is selected, the
    /// edit menu comes back over it, and Copy copies just it (the table as its Markdown).
    @MainActor func testDragTheEndForAPart() throws {
        launch(richChat)
        let page = openSelection("迁移计划", shot: "rich-part")
        try dragEnd(of: page, to: "步骤：")
        XCTAssertTrue(menuItem("拷贝").waitForExistence(timeout: 3), "no edit menu over the part:\n\(app.debugDescription)")
        sleep(1)
        shot("rich-part-range")
        UIPasteboard.general.string = "-"
        menuItem("拷贝").tap()
        XCTAssertTrue(page.waitForNonExistence(timeout: 3), "拷贝 did not put the message back")
        let copied = pasted()
        XCTAssertTrue(copied.hasPrefix("迁移计划\n\n先把三个页面"), copied)
        XCTAssertTrue(copied.contains("| 价格页 | 3 次请求 | 1 次 | 中：缓存键要改 |"), copied)
        XCTAssertTrue(copied.trimmingCharacters(in: .whitespacesAndNewlines).hasSuffix("步骤："), copied)
    }

    /// Drags the selection's end handle (under the end of the last paragraph, a short line) to the end of the
    /// one-line paragraph `line`. The paragraphs are the text view's elements, at their words' frames.
    @MainActor private func dragEnd(of page: XCUIElement, to line: String) throws {
        let paragraphs = page.textViews.allElementsBoundByIndex.filter { !$0.label.isEmpty }
        let last = try XCTUnwrap(paragraphs.last, "no paragraphs:\n\(page.debugDescription)").frame
        let target = try XCTUnwrap(paragraphs.first { $0.label == line }, "no \(line):\n\(page.debugDescription)").frame
        let origin = app.coordinate(withNormalizedOffset: .zero)
        origin.withOffset(CGVector(dx: last.maxX - 1, dy: last.maxY + 4))
            .press(forDuration: 0.5, thenDragTo: origin.withOffset(CGVector(dx: target.maxX - 1, dy: target.maxY - 4)))
        sleep(1)
    }

    /// Select Text › Quote (#4549): Quote leads the selection's menu and puts only the picked words over the box, as
    /// part of the reply; sent, the bubble opens with them.
    @MainActor func testQuoteThePickedWords() throws {
        launch(selectChat)
        let page = openSelection("Yes", shot: "quote")
        page.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 12, dy: 10)).doubleTap()
        let pick = menuItem("引用")
        XCTAssertTrue(pick.waitForExistence(timeout: 3), "no 引用 in the selection's menu:\n\(app.debugDescription)")
        sleep(1) // the menu fades in
        shot("quote-menu")
        pick.tap()
        XCTAssertTrue(page.waitForNonExistence(timeout: 3), "引用 did not put the message back")

        let quotes = app.descendants(matching: .any).matching(identifier: "composer.quotes").firstMatch
        XCTAssertTrue(quotes.waitForExistence(timeout: 3), "no quote over the box")
        let heading = quotes.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "引用选中的文字")).firstMatch
        XCTAssertTrue(heading.exists, "the quote does not say it is a part:\n\(quotes.debugDescription)")
        XCTAssertTrue(quotes.staticTexts["Yes"].exists, "the quote is not just the picked word:\n\(quotes.debugDescription)")
        sleep(1)
        shot("quote-bar")

        let send = app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "shell-composer-send", "发送")).firstMatch
        XCTAssertTrue(send.waitForExistence(timeout: 3))
        send.tap()
        XCTAssertTrue(quotes.waitForNonExistence(timeout: 5), "the quote stayed over the box after sending")
        let block = app.buttons.matching(identifier: "bubble.quote").firstMatch
        XCTAssertTrue(block.waitForExistence(timeout: 5), "the sent bubble has no quote")
        XCTAssertTrue(block.label.hasPrefix("引用选中的文字") && block.label.hasSuffix("Yes"), block.label)
    }

    /// Your own message: exactly as written, no Markdown applied.
    @MainActor func testYourOwnMessageStaysAsWritten() throws {
        launch(selectChat)
        let page = openSelection("Python", shot: "user")
        XCTAssertEqual(page.value as? String, "Python 里 __init__.py 的 2*3*4 是不是写错了？\n# 不是标题\n- 也不是列表")
        copyFirstWord(of: page, "Python")
    }

    /// An Agents reply with a fenced code block: the prose and the code without the fence.
    @MainActor func testAnAgentReplyReadsAsInTheChat() throws {
        launch(agentChat)
        let page = openSelection("改好了，测试全过", shot: "agent")
        let all = page.value as? String ?? ""
        XCTAssertTrue(all.hasPrefix("改好了，测试全过。新的写法：\n\n"), all)
        XCTAssertTrue(all.contains("let hours = forecast.hours"), "the code is not there: \(all)")
        XCTAssertFalse(all.contains("```"), "the fence is shown: \(all)")
        XCTAssertTrue(all.contains("$S = \\pi r^2$"), "the formula is not as written: \(all)")
    }

    /// Persona's main conversation: bold and a numbered list read as they do in the chat, without `**`.
    @MainActor func testAPersonaReplyReadsAsInTheChat() throws {
        launch(mainChat)
        let page = openSelection("今天有 3", shot: "persona")
        let all = page.value as? String ?? ""
        XCTAssertTrue(all.hasPrefix("今天有 3 件事需要你："), "the Markdown marks are shown: \(all)")
        XCTAssertTrue(all.contains("1.\t补齐网络层的单元测试"), "the list is not there: \(all)")
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "select-text \(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let mode = XCUIDevice.shared.appearance == .dark ? "dark" : "light"
        let device = UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
        try? app.screenshot().pngRepresentation
            .write(to: URL(fileURLWithPath: dir).appending(path: "select-text-\(name)-\(device)-\(UITestStyle.name)-\(mode).png"))
    }
}
