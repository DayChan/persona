import XCTest

/// The shell's way to the personal agents page on a phone (#140): the Personal Agents pill beside the grouping, under
/// the quick filters. Against scripts/mock/mock_hub.py with MOCK_PERSONAL_AGENTS=1 (TEST_RUNNER_PERSONA_PERSONAL_HUB);
/// screenshots in PERSONA_SHOTS, with PERSONA_SHOTS_SUFFIX appended to each name.
final class PersonalAgentsEntryTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(screenshot: app.screenshot())
            a.lifetime = .keepAlways
            add(a)
        }
        super.tearDown()
    }

    private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        try? XCUIScreen.main.screenshot().pngRepresentation
            .write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)\(env["PERSONA_SHOTS_SUFFIX"] ?? "").png"))
    }

    @MainActor private func open(_ state: String, delay: Int = 0) throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_PERSONAL_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_PERSONAL_AGENTS=1 (PERSONA_PERSONAL_HUB)")
        }
        _ = try Data(contentsOf: URL(string: hub + "/mock/personal-agents?reset=1&state=\(state)&delay=\(delay)")!)
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-dev.openChat", "", "-dev.grouping", "state"]
        app.launchPinned()
        XCTAssertTrue(app.buttons["agents.grouping"].waitForExistence(timeout: 30), app.debugDescription)
        return hub
    }

    @MainActor func testThePillOpensPersonalAgents() throws {
        _ = try open("configured")
        let pill = app.buttons["agents.personal"]
        XCTAssertTrue(pill.waitForExistence(timeout: 30), app.debugDescription)
        XCTAssertTrue(app.buttons["agents.grouping"].exists, "beside the grouping")
        shot("entry")
        pill.tap()
        let title = app.navigationBars.staticTexts.matching(NSPredicate(format: "label == 'Personal Agents' OR label == '个人 Agent'")).firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 10), app.debugDescription)
        shot("page")
    }

    @MainActor func testNoSessionsHidesTheEntry() throws {
        _ = try open("empty")
        XCTAssertFalse(app.buttons["agents.personal"].waitForExistence(timeout: 5), app.debugDescription)
        shot("entry-empty")
    }

    @MainActor func testMissingSetupHidesTheEntry() throws { try assertHidden("missing") }
    @MainActor func testDisabledSetupHidesTheEntry() throws { try assertHidden("disabled") }
    @MainActor func testNoAccountsHidesTheEntry() throws { try assertHidden("no_accounts") }

    @MainActor private func assertHidden(_ state: String) throws {
        _ = try open(state)
        XCTAssertFalse(app.buttons["agents.personal"].waitForExistence(timeout: 5), app.debugDescription)
    }

    @MainActor func testLoadingDoesNotFlashAndSessionsAutomaticallyRevealTheEntry() throws {
        let hub = try open("empty", delay: 3)
        XCTAssertFalse(app.buttons["agents.personal"].waitForExistence(timeout: 7), "hidden throughout loading and the empty result")
        _ = try Data(contentsOf: URL(string: hub + "/mock/personal-agents?state=configured&delay=0")!)
        XCTAssertTrue(app.buttons["agents.personal"].waitForExistence(timeout: 15), "the hub event reveals the entry without relaunching")
        _ = try Data(contentsOf: URL(string: hub + "/mock/personal-agents?state=empty")!)
        XCTAssertTrue(app.buttons["agents.personal"].waitForNonExistence(timeout: 15), "the last session leaving hides the entry")
    }
}
