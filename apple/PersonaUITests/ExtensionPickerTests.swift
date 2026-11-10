import XCTest

/// An extension's form picks one machine (format machine) and launchers (format launcher) from the hub's lists by name, and
/// steps whole numbers (#2385): no machine id or launcher name typed in, a launcher the hub no longer has still shown, the
/// choices saved with the rest of the form. Against scripts/mock/mock_hub.py with MOCK_EXTENSIONS=1 MOCK_EXTENSION_PICKERS=1
/// (`TEST_RUNNER_PERSONA_EXTENSIONS_HUB`, `TEST_RUNNER_PERSONA_EXTENSIONS_PICKERS=1`); `TEST_RUNNER_PERSONA_SHOTS` keeps the
/// screenshots, named by `TEST_RUNNER_PERSONA_EXTENSIONS_APPEARANCE` (switch the simulator with `simctl ui … appearance`).
final class ExtensionPickerTests: XCTestCase {
    @MainActor func testMachinesAndLaunchersArePicked() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_EXTENSIONS_HUB"], env["PERSONA_EXTENSIONS_PICKERS"] == "1" else {
            throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 MOCK_EXTENSION_PICKERS=1 (PERSONA_EXTENSIONS_HUB, PERSONA_EXTENSIONS_PICKERS=1)")
        }
        continueAfterFailure = false
        let look = env["PERSONA_EXTENSIONS_APPEARANCE"] ?? "light"
        func shot(_ app: XCUIApplication, _ name: String) {
            guard let dir = env["PERSONA_SHOTS"] else { return }
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "plugins-pickers-\(name)-\(look).png"))
        }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openExtension", "example-agent-chat"]
        app.launchPinned()

        func field(_ name: String) -> XCUIElement { app.descendants(matching: .any).matching(identifier: "plugin-field-\(name)").firstMatch }
        func scrollTo(_ e: XCUIElement) {
            for _ in 0..<10 where !(e.exists && e.isHittable) { app.swipeUp() }
            usleep(800_000) // the page coasts on after a swipe, and a tap then only stops it
            // It can come to rest on the screen's bottom edge, hittable at its top with its controls below (#3079): nudge
            // the page up with slow drags, which do not coast, until all of it is on screen. Dragged at the leading edge,
            // clear of the rows' switches and steppers.
            let bottom = app.windows.firstMatch.frame.maxY - 80
            for _ in 0..<6 where e.exists && e.frame.maxY > bottom {
                app.coordinate(withNormalizedOffset: CGVector(dx: 0.08, dy: 0.6))
                    .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.08, dy: 0.4)),
                           withVelocity: .slow, thenHoldForDuration: 0.3)
            }
        }
        func button(_ label: String) -> XCUIElement { app.buttons.matching(NSPredicate(format: "label == %@", label)).firstMatch }

        // The /agent switch, then the machine: a picker showing the machine by name, never an id field.
        let agent = app.switches.matching(identifier: "plugin-field-agent").firstMatch
        XCTAssertTrue(agent.waitForExistence(timeout: 30), "no /agent switch:\n\(app.debugDescription)")
        scrollTo(agent)
        XCTAssertEqual(agent.value as? String, "0")
        agent.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
        XCTAssertTrue(waitUntil { agent.value as? String == "1" }, "turned on")

        let machine = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "/agent 在哪台电脑上跑")).firstMatch
        scrollTo(machine)
        XCTAssertTrue(machine.exists, "the machine is not a picker:\n\(app.debugDescription)")
        XCTAssertTrue(machine.label.contains("服务器"), "shown by name: \(machine.label)")
        // The launchers come in apart from the page: the picker replaces the field once they do.
        let launcher = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "/agent 用的启动器")).firstMatch
        XCTAssertTrue(launcher.waitForExistence(timeout: 30), "the launcher is not a picker:\n\(app.debugDescription)")
        shot(app, "form")

        machine.tap()
        XCTAssertTrue(button("MacBook").waitForExistence(timeout: 5), "no machines to pick:\n\(app.debugDescription)")
        XCTAssertTrue(button("Mac Studio").exists && button("未设置").exists)
        shot(app, "machines")
        button("MacBook").tap()
        XCTAssertTrue(waitUntil { machine.label.contains("MacBook") }, machine.label)

        scrollTo(launcher)
        launcher.tap()
        XCTAssertTrue(button("cc_main").waitForExistence(timeout: 5), "no launchers to pick:\n\(app.debugDescription)")
        XCTAssertTrue(button("claude").exists && button("codex").exists)
        XCTAssertFalse(button("cc_old").exists, "a hidden launcher is offered")
        shot(app, "launchers")
        button("cc_main").tap()
        XCTAssertTrue(waitUntil { launcher.label.contains("cc_main") }, launcher.label)

        // A whole number steps within its bounds.
        // The row's identifier is on its controls, as for every field.
        let hours = app.steppers.matching(identifier: "plugin-field-agent_continue_hours").firstMatch
        scrollTo(hours)
        XCTAssertTrue(hours.exists, "no stepper:\n\(app.debugDescription)")
        hours.buttons.element(boundBy: 1).tap()
        let hoursText = app.textFields.matching(NSPredicate(format: "value == %@", "5")).firstMatch
        XCTAssertTrue(hoursText.waitForExistence(timeout: 5), "4 (the default) + 1:\n\(app.debugDescription)")
        shot(app, "steps")

        // A launcher the hub no longer has is still shown, marked, so it can be changed.
        let autoReply = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "代答用的启动器")).firstMatch
        scrollTo(autoReply)
        XCTAssertTrue(autoReply.label.contains("cc_retired") && autoReply.label.contains("找不到这个启动器"), autoReply.label)
        shot(app, "missing")

        app.buttons["plugin-save"].tap()
        XCTAssertTrue(waitUntil { (try? self.config(hub))?["agent_launcher"] as? String == "cc_main" }, "not saved: \(String(describing: try? config(hub)))")
        let saved = try config(hub)
        XCTAssertEqual(saved["agent"] as? Bool, true)
        XCTAssertEqual(saved["agent_machine"] as? String, "macbook")
        XCTAssertEqual(saved["agent_continue_hours"] as? Double, 5)
        XCTAssertEqual(saved["auto_reply_launcher"] as? String, "cc_retired", "a value nobody changed is kept")
    }

    /// The extension's settings as the hub has them.
    private func config(_ hub: String) throws -> [String: Any] {
        var req = URLRequest(url: URL(string: hub + "/api/v1/extensions/example-agent-chat/-/")!)
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        var out: Data?
        let done = DispatchSemaphore(value: 0)
        URLSession.shared.dataTask(with: req) { data, _, _ in out = data; done.signal() }.resume()
        done.wait()
        let json = try XCTUnwrap(try JSONSerialization.jsonObject(with: out ?? Data()) as? [String: Any])
        return json["config"] as? [String: Any] ?? [:]
    }

    private func waitUntil(_ ok: () -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(10)
        while Date.now < deadline { if ok() { return true }; usleep(200_000) }
        return false
    }
}
