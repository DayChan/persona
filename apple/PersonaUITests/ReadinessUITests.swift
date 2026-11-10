import XCTest

/// The main conversation's readiness on a new user's hub (#4193, docs/assistant/onboarding.md 4.4, 5.1, 5.2): against
/// scripts/mock/mock_hub.py with MOCK_READINESS=<scenario> (PERSONA_MOCK_HUB; PERSONA_READINESS names the scenario for the
/// screenshots). A new conversation shows the card for the missing step; with something typed, send says what to do
/// first, goes there, and the draft stays. PERSONA_SHOTS=<dir> writes the screenshots.
final class ReadinessUITests: XCTestCase {
    @MainActor private func launch(fresh: Bool = false, tab: String = "assistant") throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_READINESS (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", tab] + (fresh ? ["-openChatsFresh", "YES"] : [])
        app.launchPinned()
        return app
    }

    private var scenario: String { ProcessInfo.processInfo.environment["PERSONA_READINESS"] ?? "scenario" }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "\(name)-\(look).png"))
        }
    }

    /// On a new conversation: wait for the card (the mock's readiness), or note there is none (full).
    @MainActor private func openNew(_ app: XCUIApplication) {
        let new = app.buttons["assistant-new"].firstMatch
        if new.waitForExistence(timeout: 15), new.isHittable { new.tap() }
    }

    @MainActor func testTheCardSaysWhatIsMissingAndSendGoesThere() throws {
        let app = try launch()
        openNew(app)
        let card = app.descendants(matching: .any)["readiness-card"].firstMatch
        // Nothing blocks on these: an unverified model (unknown) and an assistant restarting (503: as before).
        if ["full", "unverified", "assistant_offline"].contains(scenario) {
            sleep(4)
            shot(app, "readiness-\(scenario)")
            XCTAssertFalse(card.exists, "a card on a ready hub:\n\(app.debugDescription)")
            return
        }
        XCTAssertTrue(card.waitForExistence(timeout: 20), "no readiness card:\n\(app.debugDescription)")
        sleep(1)
        shot(app, "readiness-\(scenario)")
        if scenario == "zero" {
            // Add a Computer on the card opens adding a computer in place (#4196).
            let add = card.buttons["添加电脑"].firstMatch
            XCTAssertTrue(add.waitForExistence(timeout: 5), "no 添加电脑 on the card:\n\(app.debugDescription)")
            add.tap()
            sleep(2)
            shot(app, "readiness-zero-add-computer")
            return
        }
        guard ["zero", "offline", "pending", "no_claude", "not_logged_in", "bad_key", "launcher_missing", "node_outdated"].contains(scenario) else { return }

        // Something typed: send says what to do first; tapping it opens that and the draft stays.
        let field = app.textViews["shell-composer-field"].exists ? app.textViews["shell-composer-field"] : app.textFields["shell-composer-field"]
        XCTAssertTrue(field.waitForExistence(timeout: 5), "no Shell composer:\n\(app.debugDescription)")
        field.tap()
        field.typeText("你好")
        let gate = app.buttons["composer-send-gate"].firstMatch
        XCTAssertTrue(gate.waitForExistence(timeout: 5), "send did not turn into what to do first:\n\(app.debugDescription)")
        shot(app, "readiness-\(scenario)-gate")
        gate.tap()
        sleep(2)
        shot(app, "readiness-\(scenario)-gate-opened")
    }

    /// A probe that outlasts the old 30 s client timeout still gets the hub's result; an HTTP failure stays beside the
    /// checklist button that started it. Run slow / error with MOCK_READINESS=not_logged_in, or model_error with bad_key.
    /// PERSONA_PROBE_EXPECT=slow|error|model_error.
    @MainActor func testTryMessageWaitsForAnAnswerOrShowsFailure() throws {
        let env = ProcessInfo.processInfo.environment
        guard let expected = env["PERSONA_PROBE_EXPECT"], ["slow", "error", "model_error"].contains(expected) else {
            throw XCTSkip("needs PERSONA_PROBE_EXPECT=slow|error|model_error and the matching mock probe delay / error")
        }
        let app = try launch(fresh: true, tab: "settings")
        let entry = app.descendants(matching: .any)["settings.checklist"].firstMatch
        XCTAssertTrue(entry.waitForExistence(timeout: 20), "no checklist in Settings:\n\(app.debugDescription)")
        entry.tap()
        let rowID = expected == "model_error" ? "model" : "first_message"
        let row = app.descendants(matching: .any)["checklist.\(rowID)"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), "no Try a message row:\n\(app.debugDescription)")
        let tryButton = row.buttons["试一句"].firstMatch
        XCTAssertTrue(tryButton.waitForExistence(timeout: 5), "no Try a message:\n\(app.debugDescription)")
        let began = Date.now
        tryButton.tap()

        let result = row.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "试了一句，没成功")).firstMatch
        XCTAssertTrue(result.waitForExistence(timeout: expected == "slow" ? 50 : 15),
                      "the probe ended without its \(expected) outcome beside the button that started it:\n\(app.debugDescription)")
        let reason = expected == "slow" ? "Not logged in" : "mock 探针没有回答"
        XCTAssertTrue(row.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", reason)).firstMatch.exists,
                      "the probe's failure reason is missing from \(rowID)")
        if expected == "model_error" {
            let first = app.descendants(matching: .any)["checklist.first_message"].firstMatch
            XCTAssertFalse(first.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", reason)).firstMatch.exists,
                           "another row must not claim the model row's failure")
        }
        if expected == "slow" {
            XCTAssertGreaterThan(Date.now.timeIntervalSince(began), 30, "the mock really outlasted the old client timeout")
        }
        XCTAssertTrue(tryButton.isEnabled, "a finished probe leaves Try a message available for another attempt")
        shot(app, "readiness-probe-\(expected)")
    }

    /// MOCK_START_ERROR=<code> on a ready hub: the refused start is a card with the hub's words, not a red line.
    @MainActor func testARefusedStartIsACard() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_START_ERROR"] != nil else { throw XCTSkip("needs MOCK_START_ERROR") }
        let app = try launch()
        openNew(app)
        let field = app.textViews.firstMatch.waitForExistence(timeout: 15) ? app.textViews.firstMatch : app.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("你好")
        let send = app.buttons["shell-composer-send"].exists ? app.buttons["shell-composer-send"].firstMatch : app.buttons["发送"].firstMatch
        send.tap()
        let card = app.descendants(matching: .any)["readiness-card"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 15), "no card for the refused start:\n\(app.debugDescription)")
        sleep(1)
        shot(app, "readiness-start-\(ProcessInfo.processInfo.environment["PERSONA_START_ERROR"] ?? "")")
    }

    /// The main conversation of MOCK_READINESS=not_logged_in / bad_key: its failed turn is a Model Not Connected card, not
    /// Claude Code's words as an answer, and the readiness card sits over its composer.
    @MainActor func testTheMainConversationShowsTheFix() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"],
              let main = ProcessInfo.processInfo.environment["PERSONA_MAIN"] else { throw XCTSkip("needs PERSONA_MOCK_HUB and PERSONA_MAIN") }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant"]
        app.launchPinned()
        // The first launch after pairing lands on the new conversation whatever is asked: pair, then open the page.
        sleep(4)
        app.terminate()
        app.launchArguments = ["-tab", "assistant", "-openAssistant", main]
        app.launchPinned()
        let fix = app.buttons["去修"].firstMatch
        XCTAssertTrue(fix.waitForExistence(timeout: 25), "no 模型没接好 card:\n\(app.debugDescription)")
        sleep(1)
        shot(app, "readiness-\(scenario)-main")
    }
}
