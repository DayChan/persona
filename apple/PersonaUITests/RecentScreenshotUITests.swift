import XCTest

/// The composer's + menu offers the screenshot just taken (#1994), in Demo Mode's main conversation. What the simulator's
/// photo library holds is set up before the run, so `TEST_RUNNER_PERSONA_RECENT_SHOT` says what to expect:
/// - `ask`: photo access never asked (`simctl privacy <udid> reset photos <bundle id>`): the menu has the row that asks;
///   tapping it shows the system's prompt; refused there, the row is gone and the menu is as it was.
/// - `settings`: access refused (`simctl privacy … revoke photos …`): the row sends to Settings.
/// - `offer`: never asked, and a screenshot added within the last two minutes (`simctl addmedia`): the row asks, full
///   access is given in the prompt, then the screenshot is first in the menu; one tap attaches it, and it is not offered again.
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps the screenshots, named by `TEST_RUNNER_PERSONA_RECENT_SHOT_APPEARANCE` (light / dark).
final class RecentScreenshotUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }

    override func setUp() {
        continueAfterFailure = false
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        let image = app.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = name; a.lifetime = .keepAlways; add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let look = env["PERSONA_RECENT_SHOT_APPEARANCE"] ?? "light"
        try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "recent-screenshot-\(name)-\(look).png"))
    }

    @MainActor private func launch() throws -> (XCUIApplication, String) {
        guard let expect = env["PERSONA_RECENT_SHOT"] else {
            throw XCTSkip("set TEST_RUNNER_PERSONA_RECENT_SHOT=ask|settings|offer after setting up the simulator's photos")
        }
        let app = XCUIApplication()
        // The access row is shown until used once; each run starts with it unused.
        app.launchArguments = ["-demo.on", "YES", "-newAssistant", "YES", "-dev.openChat", "", "-tab", "assistant",
                               "-composer.recentScreenshotGuideUsed", "NO"]
        app.launchPinned()
        return (app, expect)
    }

    @MainActor private func openPlus(_ app: XCUIApplication) {
        let plus = app.buttons["shell-composer-add"].firstMatch
        XCTAssertTrue(plus.waitForExistence(timeout: 20), "no + button:\n\(app.debugDescription)")
        plus.tap()
        XCTAssertTrue(app.buttons["照片"].waitForExistence(timeout: 5), "the + menu did not open:\n\(app.debugDescription)")
    }

    @MainActor private func row(_ app: XCUIApplication, _ id: String, _ title: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == %@ OR label BEGINSWITH %@", id, title)).firstMatch
    }

    @MainActor func testThePlusMenu() throws {
        let (app, expect) = try launch()
        let offer = row(app, "composer-recent-screenshot", "刚截的图")
        let guide = row(app, "composer-recent-screenshot-guide", "推荐刚截的图")
        openPlus(app)
        switch expect {
        case "offer":
            // Never asked: the row asks, Allow Full Access, and the screenshot added before the run takes the row's place
            // (`simctl privacy grant` is not seen by Photos on the iOS 26 simulator, so access is given as a user gives it).
            if guide.waitForExistence(timeout: 5) {
                shot(app, "offer-before-access")
                guide.tap()
                let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
                let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["允许完全访问", "Allow Full Access"])).firstMatch
                XCTAssertTrue(allow.waitForExistence(timeout: 10), "no system prompt:\n\(springboard.debugDescription)")
                allow.tap()
                openPlus(app)
            }
            let found = offer.waitForExistence(timeout: 5)
            shot(app, "offer-menu")
            XCTAssertTrue(found, "no recent screenshot in the menu:\n\(app.debugDescription)")
            XCTAssertFalse(guide.exists)
            offer.tap()
            let attached = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "图片 1")).firstMatch
            XCTAssertTrue(attached.waitForExistence(timeout: 10), "the screenshot is not in the composer")
            shot(app, "offer-attached")
            openPlus(app)
            XCTAssertFalse(offer.waitForExistence(timeout: 2), "the attached screenshot is offered again")
        case "ask":
            XCTAssertTrue(guide.waitForExistence(timeout: 5), "no access row:\n\(app.debugDescription)")
            XCTAssertFalse(offer.exists)
            shot(app, "ask-menu")
            guide.tap()
            let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
            let refuse = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["不允许", "Don’t Allow", "Don't Allow"])).firstMatch
            XCTAssertTrue(refuse.waitForExistence(timeout: 10), "no system prompt:\n\(springboard.debugDescription)")
            let prompt = XCTAttachment(screenshot: springboard.screenshot())
            prompt.name = "ask-prompt"; prompt.lifetime = .keepAlways; add(prompt)
            if let dir = env["PERSONA_SHOTS"] {
                let look = env["PERSONA_RECENT_SHOT_APPEARANCE"] ?? "light"
                try? springboard.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "recent-screenshot-ask-prompt-\(look).png"))
            }
            refuse.tap()
            openPlus(app)
            XCTAssertFalse(guide.waitForExistence(timeout: 2), "the row is still there after it was used")
            XCTAssertFalse(offer.exists)
            shot(app, "refused-menu")
        case "settings":
            XCTAssertTrue(guide.waitForExistence(timeout: 5), "no access row:\n\(app.debugDescription)")
            XCTAssertFalse(offer.exists)
            // The subtitle (allow full access in Settings) is not in the row's accessibility label: the screenshot shows it.
            shot(app, "settings-menu")
        default:
            XCTFail("unknown PERSONA_RECENT_SHOT \(expect)")
        }
    }
}
