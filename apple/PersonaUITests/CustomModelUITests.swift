import UIKit
import XCTest

/// Typing a model ID (#1644): New Agent's model picker lists the IDs typed before (each can be forgotten) and "Other
/// Model…", whose field says why an ID can't be used as it is typed; the pick stays and goes with the start. A running
/// Claude Code session's model menu and the Persona chat's launcher menu offer the same field; the session's goes
/// through POST …/modes, and once it runs that model the menu lists it by its ID, ticked. Against
/// scripts/mock/mock_hub.py with `MOCK_CUSTOM_MODELS=1`; `=fail` for New Agent without a list, `=empty` for an empty
/// one, `=forgetfail` for a Forget the hub refuses (`TEST_RUNNER_PERSONA_CUSTOM_MODELS_HUB`; each test skips the modes
/// it isn't for). With PERSONA_SHOTS set the screenshots are written there too (PERSONA_SHOTS_APPEARANCE names the
/// simulator's appearance in the file names).
final class CustomModelUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    /// A resting Claude Code agent in a pane, reviewing the changes in PR #42.
    private let review = "macbook/a1f3c2d4-0000-4000-8000-000000000006"

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_CUSTOM_MODELS_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_CUSTOM_MODELS=1 (TEST_RUNNER_PERSONA_CUSTOM_MODELS_HUB)")
        }
        hub = h
        continueAfterFailure = false
        _ = try Data(contentsOf: URL(string: hub + "/mock/custom_models?reset=1")!)
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(screenshot: app.screenshot())
            a.lifetime = .keepAlways
            add(a)
            let t = XCTAttachment(string: app.debugDescription)
            t.lifetime = .keepAlways
            add(t)
        }
        super.tearDown()
    }

    // MARK: Helpers

    @MainActor private func launch(_ args: [String]) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-cwd.macbook", "/Users/me/code/blog"] + args
        app.launchPinned()
    }

    @MainActor private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        let image = app.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = env["PERSONA_SHOTS"] {
            let device = UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
            let file = "custom-model-\(device)-\(name)-\(env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown").png"
            try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: file))
        }
    }

    @discardableResult
    private func see(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 10) -> XCUIElement {
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "\(what) not found:\n\(app.debugDescription)")
        return e
    }

    private func any(_ id: String) -> XCUIElement { app.descendants(matching: .any).matching(identifier: id).firstMatch }

    private func labelled(_ text: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    private func mock(_ path: String) -> [String: Any] {
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/" + path)!) else { return [:] }
        return (try? JSONSerialization.jsonObject(with: data) as? [String: Any]) ?? [:]
    }

    private func wait(_ what: String, _ done: () -> Bool) {
        for _ in 0..<50 where !done() { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertTrue(done(), what)
    }

    /// The tests that need the lists with typed IDs (`MOCK_CUSTOM_MODELS=1`, or another `mode`).
    private func needsTypedIDs(_ mode: String = "1") throws {
        guard mock("custom_models")["mode"] as? String == mode else { throw XCTSkip("needs MOCK_CUSTOM_MODELS=\(mode)") }
    }

    /// Scrolls the list a little back down until `e` is below the navigation bar (not under it, blurred), for a picture.
    @MainActor private func bringBelowBar(_ e: XCUIElement, bar title: String) {
        let bar = app.navigationBars[title]
        for _ in 0..<4 {
            let gap = bar.frame.maxY + 8 - e.frame.minY
            guard gap > 0 else { return }
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45))
            from.press(forDuration: 0.2, thenDragTo: from.withOffset(CGVector(dx: 0, dy: gap + 24)), withVelocity: .slow, thenHoldForDuration: 0.3)
            sleep(1)
        }
        XCTAssertGreaterThanOrEqual(e.frame.minY, bar.frame.maxY, "still under the bar")
    }

    /// Scrolls the list until `e` is on screen (a List makes its rows only as they come into view).
    @MainActor @discardableResult private func reach(_ e: XCUIElement, _ what: String) -> XCUIElement {
        for _ in 0..<6 where !(e.exists && e.isHittable) { app.swipeUp() }
        return see(e, what)
    }

    private var composerField: XCUIElement {
        app.textViews["shell-composer-field"].firstMatch.exists ? app.textViews["shell-composer-field"].firstMatch
            : app.textFields["shell-composer-field"].firstMatch
    }

    /// The shell composer with a short draft and the keyboard put away (a menu over the keyboard has room for only part
    /// of its items), and its model button; `clearDraft()` takes the draft back out.
    @MainActor private func modesButton() -> XCUIElement {
        see(composerField, "the composer", timeout: 20)
        for _ in 0..<3 where !app.keyboards.firstMatch.exists {
            composerField.tap()
            _ = app.keyboards.firstMatch.waitForExistence(timeout: 4)
        }
        composerField.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 12) + "ok")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)).tap()
        _ = app.keyboards.firstMatch.waitForNonExistence(timeout: 5)
        return see(app.buttons["shell-composer-modes"], "the model button")
    }

    @MainActor private func clearDraft() {
        guard composerField.waitForExistence(timeout: 5) else { return }
        for _ in 0..<3 where !app.keyboards.firstMatch.exists {
            composerField.tap()
            _ = app.keyboards.firstMatch.waitForExistence(timeout: 4)
        }
        composerField.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 12))
        sleep(2) // drafts are saved a moment later
    }

    /// The typed field: what is typed replaces what is there.
    @MainActor private func type(_ text: String) {
        let field = see(app.textFields["typedModel.field"], "the model ID field")
        if !(field.value as? String ?? "").isEmpty, field.placeholderValue != field.value as? String {
            field.tap()
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 40))
        } else if !app.keyboards.firstMatch.exists {
            field.tap()
        }
        field.typeText(text)
    }

    // MARK: New Agent

    /// The IDs typed before under the list, Forget, "Other Model…" with the reason under a bad ID, and the typed pick
    /// staying on the row and going with the start.
    @MainActor func testNewAgentTakesATypedModel() throws {
        try needsTypedIDs()
        launch(["-tab", "dev", "-showNewChat", "YES", "-newChat.machine", "macbook"])
        see(app.navigationBars["新对话"], "New Agent", timeout: 20)
        see(app.buttons["newChat.model"], "the model row", timeout: 20).tap()
        see(app.navigationBars["模型"], "the model picker")
        reach(app.buttons["newChat.model.vendor/model-x"], "an ID typed before")
        let latest = reach(app.buttons["newChat.model.~vendor/model-latest[1m]"], "a gateway's latest alias typed before")
        see(labelled("输入过的"), "their section")
        let other = reach(app.buttons["newChat.model.other"], "Other Model…")
        shot("picker")

        // Forget: the row goes, and the hub forgets it.
        reach(latest, "the row to forget")
        latest.swipeLeft()
        see(app.buttons["newChat.model.forget.~vendor/model-latest[1m]"], "Forget").tap()
        XCTAssertTrue(latest.waitForNonExistence(timeout: 5), "the forgotten row is still there")
        wait("the hub was told to forget it") {
            ((mock("custom_models")["custom"] as? [String: [String]])?["claude"] ?? []) == ["vendor/model-x"]
        }

        reach(other, "Other Model…").tap()
        see(app.navigationBars["其他模型"], "the field's sheet")
        type("acme coder")
        let problem = see(any("typedModel.problem"), "why it can't be used")
        XCTAssertTrue(problem.label.contains("空格"), problem.label)
        XCTAssertFalse(app.buttons["typedModel.use"].isEnabled, "Use takes a bad ID")
        shot("typed-invalid")
        type("acme/coder-9")
        XCTAssertTrue(any("typedModel.problem").waitForNonExistence(timeout: 3), "the reason stays for a good ID")
        XCTAssertTrue(app.buttons["typedModel.use"].isEnabled)
        shot("typed-valid")
        app.buttons["typedModel.use"].tap()

        // Back on New Agent: the typed ID is the pick.
        let row = see(app.buttons["newChat.model"], "the model row")
        XCTAssertTrue(row.label.contains("acme/coder-9"), row.label)
        shot("typed-picked")
        let start = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "开始对话")).firstMatch
        see(start, "Start Chat").tap()
        wait("POST /api/v1/sessions sent") { !((mock("started")["started"] as? [[String: Any]]) ?? []).isEmpty }
        let body = ((mock("started")["started"] as? [[String: Any]]) ?? []).last ?? [:]
        XCTAssertEqual(body["model"] as? String, "acme/coder-9")
        wait("the hub remembers it once it ran") {
            ((mock("custom_models")["custom"] as? [String: [String]])?["claude"] ?? []).first == "acme/coder-9"
        }
    }

    /// The list couldn't be read (`MOCK_CUSTOM_MODELS=fail`): "Enter Model ID…" beside the error, and the typed ID goes
    /// with the start. Skipped against the other modes.
    @MainActor func testNewAgentTakesATypedModelWithoutAList() throws {
        guard mock("custom_models")["mode"] as? String == "fail" else { throw XCTSkip("needs MOCK_CUSTOM_MODELS=fail") }
        launch(["-tab", "dev", "-showNewChat", "YES", "-newChat.machine", "macbook"])
        see(app.navigationBars["新对话"], "New Agent", timeout: 20)
        let error = reach(any("newChat.model.error"), "why the list couldn't be read")
        let enter = reach(app.buttons["newChat.model.enter"], "Enter Model ID…")
        app.swipeUp() // out from under Start Chat, for the picture
        bringBelowBar(error, bar: "新对话") // ...and the reason not under the bar
        XCTAssertTrue(enter.isHittable)
        shot("list-failed")
        enter.tap()
        see(app.navigationBars["其他模型"], "the field's sheet")
        type("acme/coder-9")
        app.buttons["typedModel.use"].tap()
        let row = reach(app.buttons["newChat.model.enter"], "the typed pick")
        XCTAssertTrue(row.label.contains("acme/coder-9"), row.label)
        app.swipeUp()
        bringBelowBar(error, bar: "新对话")
        see(app.buttons["newChat.model.clearTyped"], "the way back to the default")
        shot("list-failed-typed")

        // The typed pick can be dropped again (the start then sends no model), and typed once more.
        app.buttons["newChat.model.clearTyped"].tap()
        XCTAssertTrue(app.buttons["newChat.model.clearTyped"].waitForNonExistence(timeout: 3), "the clear button stays")
        XCTAssertFalse(app.buttons["newChat.model.enter"].label.contains("acme/coder-9"), app.buttons["newChat.model.enter"].label)
        app.buttons["newChat.model.enter"].tap()
        see(app.navigationBars["其他模型"], "the field's sheet")
        type("acme/coder-9")
        app.buttons["typedModel.use"].tap()
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "开始对话")).firstMatch.tap()
        wait("POST /api/v1/sessions sent") { !((mock("started")["started"] as? [[String: Any]]) ?? []).isEmpty }
        XCTAssertEqual(((mock("started")["started"] as? [[String: Any]]) ?? []).last?["model"] as? String, "acme/coder-9")
    }

    /// The list is empty (`MOCK_CUSTOM_MODELS=empty`): the picker offers Default and "Enter Model ID…".
    @MainActor func testNewAgentWithAnEmptyListOffersEnterModelID() throws {
        try needsTypedIDs("empty")
        launch(["-tab", "dev", "-showNewChat", "YES", "-newChat.machine", "macbook"])
        see(app.navigationBars["新对话"], "New Agent", timeout: 20)
        see(app.buttons["newChat.model"], "the model row", timeout: 20).tap()
        see(app.navigationBars["模型"], "the model picker")
        let enter = see(app.buttons["newChat.model.other"], "the row for an ID")
        XCTAssertTrue(enter.label.contains("输入模型 ID"), enter.label)
        shot("empty-list")
    }

    /// A Forget the hub refuses (`MOCK_CUSTOM_MODELS=forgetfail`): the row comes back and the reason is under it.
    @MainActor func testAForgetTheHubRefusesPutsTheRowBack() throws {
        try needsTypedIDs("forgetfail")
        launch(["-tab", "dev", "-showNewChat", "YES", "-newChat.machine", "macbook"])
        see(app.navigationBars["新对话"], "New Agent", timeout: 20)
        see(app.buttons["newChat.model"], "the model row", timeout: 20).tap()
        see(app.navigationBars["模型"], "the model picker")
        let latest = reach(app.buttons["newChat.model.~vendor/model-latest[1m]"], "an ID typed before")
        latest.swipeLeft()
        see(app.buttons["newChat.model.forget.~vendor/model-latest[1m]"], "Forget").tap()
        let why = reach(any("newChat.model.forgetError"), "why it wasn't forgotten")
        XCTAssertTrue(why.label.contains("数据库暂时不可用"), why.label)
        XCTAssertTrue(latest.waitForExistence(timeout: 3), "the row didn't come back")
        reach(app.buttons["newChat.model.other"], "Other Model…")
        bringBelowBar(app.buttons["newChat.model.vendor/model-x"], bar: "模型")
        shot("forget-failed")
    }

    // MARK: A running session

    /// The composer's model menu: "Other Model…" opens the field with the IDs typed before; a pick goes through …/modes.
    @MainActor func testARunningSessionSwitchesToATypedModel() throws {
        try needsTypedIDs()
        launch(["-ui.oldLayout", "NO", "-openSession", review])
        modesButton().tap()
        // A menu's items keep their labels, not their identifiers.
        let other = see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "其他模型")).firstMatch, "Other Model… in the model menu")
        shot("session-menu")
        other.tap()
        see(app.navigationBars["其他模型"], "the field's sheet")
        see(any("typedModel.remembered.vendor/model-x"), "an ID typed before")
        type("a;b")
        let problem = see(any("typedModel.problem"), "why it can't be used")
        XCTAssertTrue(problem.label.contains(";"), problem.label)
        type("~vendor/model-latest")
        XCTAssertTrue(any("typedModel.problem").waitForNonExistence(timeout: 3))
        shot("session-typed")
        app.buttons["typedModel.use"].tap()
        wait("POST …/modes with the model") {
            ((mock("modes")["modes"] as? [[String: Any]]) ?? []).contains {
                $0["session_id"] as? String == "a1f3c2d4-0000-4000-8000-000000000006" && $0["model"] as? String == "~vendor/model-latest"
            }
        }
        clearDraft()

        // Running the typed model (read afresh from the hub): it is a row of its own, by its ID, the one ticked.
        app.terminate()
        launch(["-ui.oldLayout", "NO", "-openSession", review])
        modesButton().tap()
        see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "其他模型")).firstMatch, "Other Model…")
        let running = see(app.buttons.matching(NSPredicate(format: "label == %@", "~vendor/model-latest")).firstMatch, "the model it runs, by its ID")
        XCTAssertTrue(running.isSelected, "the model it runs isn't ticked")
        let byDefault = app.buttons.matching(NSPredicate(format: "label == %@", "默认")).firstMatch
        XCTAssertFalse(byDefault.exists && byDefault.isSelected, "Default is ticked")
        shot("session-running-typed")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)).tap() // the menu closes
        clearDraft()
    }

    // MARK: The Persona chat

    /// The launcher menu: the IDs typed before under the launcher's models, and "Other Model…" for the next message.
    @MainActor func testThePersonaChatTakesATypedModel() throws {
        try needsTypedIDs()
        launch(["-tab", "assistant", "-ui.oldLayout", "YES"])
        let avatar = see(app.buttons["persona-avatar"], "Persona's face", timeout: 20)
        // The launchers' models are read once the bar is up (a few requests); a menu shows what was there when it opened.
        sleep(6)
        for attempt in 0..<3 {
            avatar.tap()
            see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "启动器")).firstMatch, "the launcher menu").tap()
            see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "claude")).firstMatch, "the claude launcher").tap()
            if labelled("vendor/model-x").waitForExistence(timeout: 3) || attempt == 2 { break }
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.5)).tap() // outside the menu: it closes
            sleep(3)
        }
        see(labelled("vendor/model-x"), "an ID typed before in the launcher's models")
        let other = see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "其他模型")).firstMatch, "Other Model…")
        // The menu's end (the typed IDs and Other Model…) is below the launcher's models: scrolled there for the picture.
        if !other.isHittable, let list = app.collectionViews.allElementsBoundByIndex.last(where: { $0.frame.contains(other.frame.origin) || $0.cells.count > 6 }) {
            list.swipeUp(velocity: .slow)
        }
        shot("chat-menu")
        if !other.exists { // the scroll closed the menu: open it again
            avatar.tap()
            see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "启动器")).firstMatch, "the launcher menu").tap()
            see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "claude")).firstMatch, "the claude launcher").tap()
        }
        other.tap()
        see(app.navigationBars["其他模型"], "the field's sheet")
        type("vendor/model-y")
        shot("chat-typed")
        app.buttons["typedModel.use"].tap()
        see(labelled("vendor/model-y"), "the toast naming it")
    }
}
