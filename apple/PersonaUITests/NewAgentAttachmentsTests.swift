import XCTest

/// New Agent's attachments (#2321) against scripts/mock/mock_hub.py started with MOCK_UPLOAD_KBPS=96, so that an
/// upload takes half a minute: TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port>. A simulator's photo and file
/// pickers can't be driven from here: `-openNewAgentAttachments` attaches the made-up photo and uploads made-up files.
/// TEST_RUNNER_PERSONA_SHOTS=<dir> also saves the screens (TEST_RUNNER_PERSONA_SHOTS_APPEARANCE names them light / dark).
final class NewAgentAttachmentsTests: XCTestCase {
    private var hub = ""
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
        _ = mock("reset") // no sessions started
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(string: app.debugDescription)
            a.name = "view tree"
            a.lifetime = .keepAlways
            add(a)
        }
        super.tearDown()
    }

    /// A photo and a file: Start Chat waits for the upload and says so; the first message carries the photo and the
    /// file's path on the computer after what was typed.
    @MainActor func testPhotoAndFileGoWithTheFirstMessage() throws {
        openNewAgent(attaching: "photo,build.log")
        reveal(app.buttons["移除图片 1"], "the photo's ×", timeout: 30)
        reveal(app.buttons["取消上传 build.log"], "the upload in progress")
        let start = see(button("开始对话"), "开始对话")
        XCTAssertFalse(start.isEnabled, "Start Chat waits for the upload")
        let why = see(app.descendants(matching: .any).matching(identifier: "newChat.notReady").firstMatch, "why Start Chat waits")
        XCTAssertTrue(why.label.contains("等文件上传完"), why.label)
        nudge() // the form's end above the button, for the screenshot
        shot("new-agent-uploading")

        reveal(app.buttons["移除 build.log"], "the file, on the computer", timeout: 90)
        XCTAssertTrue(start.isEnabled)
        XCTAssertFalse(app.buttons["取消上传 build.log"].exists)
        shot("new-agent-attachments")
        reveal(app.buttons["newChat.attach"], "the add menu").tap()
        see(app.buttons["照片"], "Photos in the menu")
        see(app.buttons["文件"], "Files in the menu")
        shot("new-agent-attach-menu")
        app.navigationBars["新对话"].coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap() // out of the menu
        XCTAssertTrue(app.buttons["照片"].waitForNonExistence(timeout: 5))

        let prompt = promptField()
        prompt.tap()
        prompt.typeText("看看这个日志")
        start.tap()
        wait("POST /api/v1/sessions sent") { !started().isEmpty }
        XCTAssertEqual(started().last?["async"] as? Bool, true)
        XCTAssertNil(started().last?["prompt"], "the first message is not part of the start")
        let path = ((mock("uploads")["uploads"] as? [[String: Any]]) ?? []).last { ($0["name"] as? String) == "build.log" }?["path"] as? String ?? "?"
        wait("the first message, with the photo and the path") { sent("看看这个日志\n\(path)") != nil }
        XCTAssertEqual((sent("看看这个日志\n\(path)")?["images"] as? [Any])?.count, 1)
        XCTAssertFalse(app.navigationBars["新对话"].waitForExistence(timeout: 3), "the sheet closes on start")
        see(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "看看这个日志")).firstMatch, "the first message on screen")
        shot("new-agent-started")
    }

    /// The files are on the computer they were uploaded to: picking another takes them out with a note; the photo stays.
    @MainActor func testAnotherComputerTakesTheFilesOut() throws {
        // Not pinned to the MacBook: a launch argument would win over the card picked here. It starts on the computer
        // picked last, which the upload names.
        openNewAgent(attaching: "photo,notes.txt", machine: nil)
        reveal(app.buttons["移除 notes.txt"], "the file, on the computer", timeout: 90)
        let from = ((mock("uploads")["uploads"] as? [[String: Any]]) ?? []).last { ($0["name"] as? String) == "notes.txt" }?["machine"] as? String
        reveal(button(from == "macbook" ? "服务器" : "MacBook"), "another computer's card", up: false).tap()
        let note = reveal(app.descendants(matching: .any).matching(identifier: "newChat.attachNote").firstMatch, "the note")
        XCTAssertTrue(note.label.contains(["macbook": "MacBook", "server": "服务器"][from ?? ""] ?? "“"), note.label)
        XCTAssertFalse(app.buttons["移除 notes.txt"].exists, "the file is out")
        XCTAssertTrue(app.buttons["移除图片 1"].exists, "the photo stays")
        nudge() // the note above the button
        shot("new-agent-computer-switched")
    }

    /// A Codex launcher takes no attachments: no add menu, the web's words under a photo picked before, and Start Chat
    /// says why until it is taken out.
    @MainActor func testCodexTakesNoAttachments() throws {
        openNewAgent(attaching: "photo")
        see(app.buttons["newChat.launcher"], "launcher row", timeout: 30).tap()
        see(button("codex"), "codex launcher").tap()
        see(app.staticTexts["codex"], "launcher picked")
        reveal(app.buttons["移除图片 1"], "the photo's ×", timeout: 30)
        XCTAssertFalse(app.buttons["newChat.attach"].exists, "no add menu for Codex")
        let note = reveal(app.descendants(matching: .any).matching(identifier: "newChat.codexNote").firstMatch, "the web's words")
        XCTAssertTrue(note.label.contains("换一个启动器"), note.label)
        nudge()
        shot("new-agent-codex")
        let prompt = promptField()
        prompt.tap()
        prompt.typeText("整理 README")
        let start = see(button("开始对话"), "开始对话")
        XCTAssertFalse(start.isEnabled)
        let why = see(app.descendants(matching: .any).matching(identifier: "newChat.notReady").firstMatch, "why Start Chat waits")
        XCTAssertTrue(why.label.contains("不能带附件"), why.label)
        usleep(1_500_000) // the keyboard settled
        shot("new-agent-codex-typing")
        nudge() // bring the photo's × out from behind the floating Start Chat bar while the keyboard is up
        shot("new-agent-codex-remove")
        let remove = app.buttons["移除图片 1"]
        XCTAssertFalse(remove.frame.intersects(start.frame), "the photo's × must not be under the floating Start Chat bar")
        remove.tap()
        wait("nothing attached, the Codex start can go") { start.isEnabled }
        XCTAssertFalse(app.buttons["移除图片 1"].exists)
        XCTAssertFalse(app.descendants(matching: .any).matching(identifier: "newChat.codexNote").firstMatch.exists)
    }

    /// A to-do handed to a new Agent: the same row, and the to-do's context block still first in the message.
    @MainActor func testToDoHandoffKeepsItsContextFirst() throws {
        launch(["-tab", "assistant", "-openShellPage", "todos", "-openNewAgentAttachments", "photo"])
        see(app.buttons["新建待办"], "the to-do list", timeout: 60)
        reveal(button("给 NAS 配自动备份"), "the to-do", timeout: 20).tap()
        reveal(button("交给 Agent"), "Hand to Agent").tap()
        see(button("选择电脑和启动器"), "the handoff's New Agent").tap()
        see(app.navigationBars["新对话"], "新对话 sheet")
        reveal(app.buttons["移除图片 1"], "the photo's ×", timeout: 30)
        reveal(app.buttons["newChat.attach"], "the add menu")
        let prompt = promptField()
        prompt.tap()
        prompt.typeText("先看日志")
        shot("new-agent-handoff")
        see(button("开始对话"), "开始对话").tap()
        wait("the first message") { sentContaining("先看日志") != nil }
        let body = sentContaining("先看日志") ?? [:]
        let text = body["text"] as? String ?? ""
        // The context block first (an older hub gets the tag under its older name), then what was typed.
        XCTAssertTrue(text.hasPrefix("<") && text.contains("-context kind=\"context\"") && text.hasSuffix("-context>\n\n先看日志"), text)
        XCTAssertEqual((body["images"] as? [Any])?.count, 1)
    }

    // MARK: -

    private func launch(_ args: [String], machine: String? = "macbook") {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // New Agent on the MacBook in a known folder, with Model Settings' launcher; nothing kept from an earlier run.
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-launcher.macbook", "", "-cwd.macbook", "/Users/me/code/blog"]
            + (machine.map { ["-newChat.machine", $0] } ?? []) + args
        app.launchPinned()
    }

    private func openNewAgent(attaching list: String, machine: String? = "macbook") {
        launch(["-tab", "dev", "-openNewAgentAttachments", list], machine: machine)
        see(app.buttons["新建会话"], "新建会话", timeout: 60).tap()
        see(app.navigationBars["新对话"], "新对话 sheet")
    }

    private func promptField() -> XCUIElement {
        let prompt = app.textViews["newChat.prompt"].exists ? app.textViews["newChat.prompt"] : app.textFields["newChat.prompt"]
        for _ in 0..<4 where !prompt.isHittable { app.swipeUp() }
        return see(prompt, "the first message field")
    }

    @discardableResult
    private func see(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 10) -> XCUIElement {
        if !e.waitForExistence(timeout: timeout) { XCTFail("\(what) not found") }
        return e
    }

    /// The form makes only the rows on screen: swipes it (up, or down for the rows above) until e is there to tap.
    @discardableResult
    private func reveal(_ e: XCUIElement, _ what: String, up: Bool = true, timeout: TimeInterval = 10) -> XCUIElement {
        let deadline = Date.now.addingTimeInterval(timeout)
        var dragged = false
        defer { if dragged { usleep(1_000_000) } } // a bounce at either end swallows the next tap
        while Date.now < deadline, !(e.exists && e.isHittable) {
            dragged = true
            // Slow, held at the end: no fling still moving the form when the row is tapped.
            let w = app.windows.firstMatch
            let from = w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.6 : 0.35))
            from.press(forDuration: 0.1, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.35 : 0.6)),
                       withVelocity: .slow, thenHoldForDuration: 0.2)
            usleep(300_000)
        }
        if !(e.exists && e.isHittable) { XCTFail("\(what) not found") }
        return e
    }

    /// One slow drag up, settled.
    private func nudge() {
        let w = app.windows.firstMatch
        w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.6))
            .press(forDuration: 0.1, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35)), withVelocity: .slow, thenHoldForDuration: 0.2)
        usleep(1_000_000)
    }

    private func button(_ s: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    private func mock(_ what: String) -> [String: Any] {
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/" + what)!),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return [:] }
        return obj
    }

    private func started() -> [[String: Any]] { (mock("started")["started"] as? [[String: Any]]) ?? [] }

    private func sent(_ text: String) -> [String: Any]? {
        ((mock("sent")["sent"] as? [[String: Any]]) ?? []).last { ($0["text"] as? String) == text }
    }

    private func sentContaining(_ text: String) -> [String: Any]? {
        ((mock("sent")["sent"] as? [[String: Any]]) ?? []).last { ($0["text"] as? String)?.contains(text) == true }
    }

    private func wait(_ what: String, timeout: TimeInterval = 20, _ test: () -> Bool) {
        let deadline = Date.now.addingTimeInterval(timeout)
        while Date.now < deadline, !test() { usleep(300_000) }
        XCTAssertTrue(test(), what)
    }

    private func shot(_ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + look + ".png"))
        }
    }
}
