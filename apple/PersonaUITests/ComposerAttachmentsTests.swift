import XCTest

/// Photos picked for the next message stay in the chat's composer (#2626) when the whole tree is built again, as a fold
/// or an unfold makes it while the app is active (iPhone Duo; iOS's snapshots on the way to the background did it too,
/// until #2836), and the composer's own state went with it. `-swapTreeAt` does that on a regular-width simulator; the
/// trip to the home screen is a plain one there. Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB), with pictures in
/// the simulator's library (`xcrun simctl addmedia`).
final class ComposerAttachmentsTests: XCTestCase {
    private var hub = ""
    /// The app's `probe.tree` (`-probes YES`): how many times `-swapTreeAt` flipped the width, and the swaps between the
    /// shell and the tabs that followed.
    private struct Swaps: Decodable { var count: Int; var rebuilt: Int }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        hub = h
    }

    /// The tree built again a moment (`-swapTreeAt`): the photos are still in the box.
    @MainActor func testPickedPhotosStayWhenTheTreeIsBuiltAgain() throws {
        let app = launch(["-swapTreeAt", "45", "-probes", "YES"])
        let launched = Date.now
        guard app.windows.firstMatch.frame.width > 700 else { throw XCTSkip("the tree swaps in regular width only (iPad, iPhone Duo open)") }
        let picked = pickPhotos(app)
        shot(app, "composer-photos-before-swap")
        XCTAssertLessThan(Date.now.timeIntervalSince(launched), 40, "the photos were picked too close to the swap: pick sooner")
        sleep(UInt32(max(0, launched.addingTimeInterval(55).timeIntervalSinceNow)))
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.count, 1, "the width did not flip")
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.rebuilt, 2, "the tree was not built again")
        XCTAssertTrue(app.buttons["移除图片 \(picked)"].waitForExistence(timeout: 5), "the photos went with the tree:\n\(app.debugDescription)")
        XCTAssertEqual(removeButtons(app).count, picked, "as many photos as were picked")
        shot(app, "composer-photos-after-swap")
    }

    /// The new conversation's page too: its photos and what was typed stay when the tree is built again.
    @MainActor func testANewConversationKeepsItsPhotosAndWordsWhenTheTreeIsBuiltAgain() throws {
        let app = launch(["-newAssistant", "YES", "-swapTreeAt", "50", "-probes", "YES"])
        let launched = Date.now
        guard app.windows.firstMatch.frame.width > 700 else { throw XCTSkip("the tree swaps in regular width only (iPad, iPhone Duo open)") }
        let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 30), "no composer on the new conversation's page:\n\(app.debugDescription)")
        field.tap()
        field.typeText("周五的发布")
        let picked = pickPhotos(app)
        shot(app, "starter-photos-before-swap")
        XCTAssertLessThan(Date.now.timeIntervalSince(launched), 45, "the photos were picked too close to the swap: pick sooner")
        sleep(UInt32(max(0, launched.addingTimeInterval(60).timeIntervalSinceNow)))
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.count, 1, "the width did not flip")
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.rebuilt, 2, "the tree was not built again")
        XCTAssertTrue(app.buttons["移除图片 \(picked)"].waitForExistence(timeout: 5), "the photos went with the tree:\n\(app.debugDescription)")
        let again = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(again.waitForExistence(timeout: 5))
        XCTAssertEqual(again.value as? String, "周五的发布", "what was typed went with the tree")
        shot(app, "starter-photos-after-swap")
    }

    /// To the home screen and back: the photos are still in the box. (A simulator keeps its width there: this is the
    /// plain trip; the swap a device makes on the way is the test above.)
    @MainActor func testPickedPhotosStayThroughTheHomeScreen() throws {
        let app = launch([])
        let picked = pickPhotos(app)
        XCUIDevice.shared.press(.home)
        sleep(4)
        app.activate()
        XCTAssertTrue(app.buttons["移除图片 \(picked)"].waitForExistence(timeout: 10), "the photos are gone after the home screen:\n\(app.debugDescription)")
        XCTAssertEqual(removeButtons(app).count, picked, "as many photos as were picked")
        shot(app, "composer-photos-after-home")
    }

    /// The ⓧ of each photo in the box.
    @MainActor private func removeButtons(_ app: XCUIApplication) -> XCUIElementQuery {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH '移除图片'"))
    }

    /// + › Photos, up to two pictures (the iPad's picker takes one of the two taps), Add: how many are in the box.
    @MainActor private func pickPhotos(_ app: XCUIApplication) -> Int {
        let plus = app.buttons.matching(NSPredicate(format: "(identifier == %@ OR label IN %@) AND enabled == true", "shell-composer-add", ["添加", "Add"])).firstMatch
        XCTAssertTrue(plus.waitForExistence(timeout: 30), "no + button:\n\(app.debugDescription)")
        plus.tap()
        let photos = app.buttons["照片"].firstMatch
        XCTAssertTrue(photos.waitForExistence(timeout: 5), "no 照片 item:\n\(app.debugDescription)")
        photos.tap()
        let pictures = app.images.matching(NSPredicate(format: "label CONTAINS[c] %@ OR label CONTAINS %@", "photo", "照片"))
        XCTAssertTrue(pictures.firstMatch.waitForExistence(timeout: 20), "no pictures in the picker:\n\(app.debugDescription)")
        XCTAssertGreaterThanOrEqual(pictures.count, 2, "fewer than two pictures in the library")
        pictures.element(boundBy: 0).tap()
        pictures.element(boundBy: 1).tap()
        // The picker's ✓ (out of process; not the app's own + behind it, which reads 「添加」 too).
        let add = app.buttons.matching(identifier: "Add").firstMatch
        XCTAssertTrue(add.waitForExistence(timeout: 5), "no ✓ in the picker:\n\(app.debugDescription)")
        add.tap()
        XCTAssertTrue(app.buttons["移除图片 1"].waitForExistence(timeout: 15), "no photo in the box:\n\(app.debugDescription)")
        sleep(1)
        return removeButtons(app).count
    }

    @MainActor private func launch(_ extra: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-app.tab", "assistant"] + extra
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + style + ".png"))
        }
    }
}
