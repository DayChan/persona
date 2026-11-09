import XCTest

/// A plugin's list of machines (schema format machine, docs/extensions/extensions.md): a switch per Mac, by name and how it is, no
/// machine ids typed in; a switch takes effect at once and is the machine page's Let the Assistant Control This Computer too. Against
/// scripts/mock/mock_hub.py with MOCK_EXTENSIONS=1 MOCK_COMPUTER=ready (`TEST_RUNNER_PERSONA_EXTENSIONS_HUB`), which has the
/// plugin cua on for MacBook; `TEST_RUNNER_PERSONA_SHOTS` keeps the screenshots, named by
/// `TEST_RUNNER_PERSONA_EXTENSIONS_APPEARANCE`.
final class ExtensionMachineSwitchesTests: XCTestCase {
    @MainActor func testMacsAreSwitches() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_EXTENSIONS_HUB"], env["PERSONA_EXTENSIONS_COMPUTER"] == "1" else {
            throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 MOCK_COMPUTER=ready (PERSONA_EXTENSIONS_HUB, PERSONA_EXTENSIONS_COMPUTER=1)")
        }
        continueAfterFailure = false
        let look = env["PERSONA_EXTENSIONS_APPEARANCE"] ?? "light"
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openExtension", "cua"]
        app.launchPinned()

        // By name and state (the field's identifier is on the whole row, as for every field).
        func row(_ name: String) -> XCUIElement { app.switches.matching(NSPredicate(format: "label BEGINSWITH %@", name + "、")).firstMatch }
        let macbook = row("MacBook"), studio = row("Mac Studio")
        XCTAssertTrue(macbook.waitForExistence(timeout: 30), "no switch per Mac:\n\(app.debugDescription)")
        XCTAssertTrue(studio.exists)
        XCTAssertFalse(row("服务器").exists, "a Linux machine is not listed")
        XCTAssertFalse(app.textFields["添加一项"].exists, "no machine ids typed in")
        XCTAssertEqual(macbook.value as? String, "1")
        XCTAssertEqual(studio.value as? String, "0")
        XCTAssertTrue(macbook.label.contains("MacBook") && macbook.label.contains("在线 · Cua 可以用"), macbook.label)
        XCTAssertTrue(studio.label.contains("在线 · Cua 没装"), "said for a Mac it is not on for too: \(studio.label)")
        if let dir = env["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "plugins-machines-\(look).png"))
        }

        let setup = app.buttons["extension-machine-setup-studio"].firstMatch
        XCTAssertTrue(setup.exists, "no way to open Cua Driver's installation and permissions")
        setup.tap()
        XCTAssertTrue(app.buttons["computer-install"].waitForExistence(timeout: 15), "the existing computer setup did not open:\n\(app.debugDescription)")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(studio.waitForExistence(timeout: 10), "back to the extension settings")

        // In effect at once, no Save: the hub turns it on for that Mac (the machine page's switch).
        studio.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
        XCTAssertTrue(waitFor(studio, value: "1"), "turned on")
        XCTAssertTrue(waitUntil { (try? self.enabledMacs(hub)) == ["macbook", "studio"] }, "the hub has it: \(String(describing: try? enabledMacs(hub)))")
        XCTAssertFalse(app.buttons["plugin-save"].isEnabled, "nothing left to 保存")
        studio.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
        XCTAssertTrue(waitUntil { (try? self.enabledMacs(hub)) == ["macbook"] }, "and off again")
    }

    /// A list with x-confirm (the plugin cua's Don't Auto-Lock While On, #1507 b2): turning a Mac on asks first, Cancel leaves
    /// it off, Open turns it on (the hub has it); turning it off does not ask.
    @MainActor func testAListWithAConfirmAsksFirst() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_EXTENSIONS_HUB"], env["PERSONA_EXTENSIONS_COMPUTER"] == "1" else {
            throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 MOCK_COMPUTER=ready (PERSONA_EXTENSIONS_HUB, PERSONA_EXTENSIONS_COMPUTER=1)")
        }
        continueAfterFailure = false
        let look = env["PERSONA_EXTENSIONS_APPEARANCE"] ?? "light"
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openExtension", "cua"]
        app.launchPinned()

        let noLock = app.switches.matching(NSPredicate(format: "identifier == %@ AND label BEGINSWITH %@", "plugin-field-no_auto_lock", "MacBook、")).firstMatch
        XCTAssertTrue(noLock.waitForExistence(timeout: 30), "no 插件开着时不自动锁屏 switch for MacBook:\n\(app.debugDescription)")
        for _ in 0..<6 where !noLock.isHittable { app.swipeUp() }
        usleep(800_000) // the page coasts on after a swipe, and a tap then only stops it
        XCTAssertEqual(noLock.value as? String, "0", "off by default")
        let flip = { noLock.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap() }
        let turnOn = app.buttons["打开"].firstMatch
        flip()
        XCTAssertTrue(turnOn.waitForExistence(timeout: 5), "asked first:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "坐在它前面的人能直接用它")).firstMatch.exists,
                      "the plugin's x-confirm:\n\(app.debugDescription)")
        if let dir = env["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "plugins-no-lock-ask-\(look).png"))
        }
        cancelDialog(app)
        XCTAssertTrue(waitUntil { !turnOn.exists })
        XCTAssertEqual(noLock.value as? String, "0", "取消: still off")
        XCTAssertEqual(try noLockMacs(hub), [])
        flip()
        XCTAssertTrue(turnOn.waitForExistence(timeout: 5))
        turnOn.tap()
        XCTAssertTrue(waitFor(noLock, value: "1"), "turned on")
        XCTAssertTrue(waitUntil { (try? self.noLockMacs(hub)) == ["macbook"] }, "the hub has it: \(String(describing: try? noLockMacs(hub)))")
        flip()
        XCTAssertTrue(waitUntil { (try? self.noLockMacs(hub)) == [] }, "and off again")
        XCTAssertFalse(turnOn.exists, "turning it off does not ask")
    }

    /// Cancel the dialog: its button where it has one, else a tap outside it (iOS 26 shows it as a popover by the switch,
    /// with no Cancel).
    @MainActor private func cancelDialog(_ app: XCUIApplication) {
        let cancel = app.buttons["取消"].firstMatch
        let outside = app.otherElements["PopoverDismissRegion"].firstMatch
        if cancel.exists { cancel.tap() } else if outside.exists { outside.tap() } else {
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.04, dy: 0.5)).tap()
        }
    }

    /// The Macs the hub says Don't Auto-Lock While On is on for (machine.computer.no_auto_lock), by id.
    private func noLockMacs(_ hub: String) throws -> [String] {
        try machines(hub).filter { $0.computer?.no_auto_lock == true }.map(\.id).sorted()
    }

    private struct Machines: Decodable {
        struct M: Decodable {
            struct C: Decodable { var enabled: Bool; var no_auto_lock: Bool? }
            var id: String
            var computer: C?
        }
        var machines: [M]
    }

    private func machines(_ hub: String) throws -> [Machines.M] {
        var req = URLRequest(url: URL(string: hub + "/api/v1/machines")!)
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        var out: Data?
        let done = DispatchSemaphore(value: 0)
        URLSession.shared.dataTask(with: req) { data, _, _ in out = data; done.signal() }.resume()
        done.wait()
        return try JSONDecoder().decode(Machines.self, from: out ?? Data()).machines
    }

    /// The Macs the hub says the plugin is on for (machine.computer.enabled), by id.
    private func enabledMacs(_ hub: String) throws -> [String] {
        try machines(hub).filter { $0.computer?.enabled == true }.map(\.id).sorted()
    }

    private func waitFor(_ e: XCUIElement, value: String) -> Bool {
        waitUntil { e.value as? String == value }
    }

    private func waitUntil(_ ok: () -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(10)
        while Date.now < deadline { if ok() { return true }; usleep(200_000) }
        return false
    }

    /// A plugin whose program runs on computers (#1830): the field picking them says, for each machine switched on, how
    /// the program is there; switching one on asks first (a program will run there). `MOCK_EXTENSIONS=1` has it on for
    /// MacBook (running) and Mac Studio (failed to start).
    @MainActor func testWhereAPluginsProgramRuns() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_EXTENSIONS_HUB"] else {
            throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 (PERSONA_EXTENSIONS_HUB)")
        }
        continueAfterFailure = false
        let look = env["PERSONA_EXTENSIONS_APPEARANCE"] ?? "light"
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openPlugin", "example-helper"]
        app.launchPinned()
        func row(_ name: String) -> XCUIElement { app.switches.matching(NSPredicate(format: "label BEGINSWITH %@", name + "、")).firstMatch }
        let macbook = row("MacBook"), studio = row("Mac Studio"), server = row("服务器")
        XCTAssertTrue(macbook.waitForExistence(timeout: 30), "no switch per computer:\n\(app.debugDescription)")
        XCTAssertTrue(macbook.label.contains("程序在运行 · 1.0.0"), macbook.label)
        XCTAssertTrue(studio.label.contains("程序出错") && studio.label.contains("退出码 3"), studio.label)
        XCTAssertFalse(studio.label.contains("exit status"), studio.label)
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label CONTAINS '不用保存'")).firstMatch.exists,
                       "a footer repeating that the switches take effect at once")
        XCTAssertTrue(server.exists, "a Linux machine is offered: the program has a Linux build")
        XCTAssertFalse(server.label.contains("程序"), "said for a machine it is not on for: \(server.label)")
        if let dir = env["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "plugins-node-\(look).png"))
        }
        server.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
        let open = app.buttons["打开"], cancel = app.buttons["取消"]
        XCTAssertTrue(open.waitForExistence(timeout: 5), "switching a machine on did not ask")
        XCTAssertTrue(cancel.exists, "the question has no 取消")
        XCTAssertTrue(app.staticTexts["打开「服务器」？"].exists, "the question does not name the machine:\n\(app.debugDescription)")
        cancel.tap()
        XCTAssertTrue(waitFor(server, value: "0"), "取消 still turned it on")
        server.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
        XCTAssertTrue(open.waitForExistence(timeout: 5), "switching a machine on did not ask the second time")
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS '将在这台电脑上运行'")).firstMatch.exists)
        if let dir = env["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "plugins-node-ask-\(look).png"))
        }
        open.tap()
        XCTAssertTrue(waitFor(server, value: "1"), "turned on")
        XCTAssertTrue(waitUntil { server.label.contains("等 hub 把程序发过去") }, server.label)
    }

}
