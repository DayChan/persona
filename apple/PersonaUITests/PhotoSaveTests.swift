import XCTest

/// Saving to the photo library for real (scripts/mock/mock_hub.py, PERSONA_MOCK_HUB): the image viewer's Save
/// and the share card's Save, each after allowing the system's permission alert, in the old layout and in the shell.
/// Photos runs the change block on its own queue; a block the compiler took for main-actor code crashed the app there
/// (2026-10-31), so the test only passes if the app is still alive and says Saved to Photos, drawn over the viewer or
/// the card: the old layout drew its toasts under both (#1492).
/// The conversation with the screenshot is put in by the test (GET /mock/photo?on=1) rather than by starting the mock
/// with MOCK_PHOTO=1: a mock started the usual way had no such conversation, the chat opened empty and both failed (#2334).
final class PhotoSaveTests: XCTestCase {
    private var hub: String!
    private var photoWas = true
    private let session = "macbook/a1f3c2d4-0000-4000-8000-00000000000c"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        let state = try mockPhoto(true)
        XCTAssertEqual(state["photo"] as? Bool, true, "the mock did not put in the conversation with the screenshot: \(state)")
        photoWas = state["was"] as? Bool ?? true
    }

    override func tearDownWithError() throws {
        if !photoWas { _ = try? mockPhoto(false) } // the tests after this one see the mock as it was
    }

    /// GET /mock/photo?on=1|0 puts the conversation with the screenshot in or takes it out; answers {"photo", "was"}.
    private func mockPhoto(_ on: Bool) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/photo?on=" + (on ? "1" : "0"))!)
        return try XCTUnwrap(try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                             "no /mock/photo in this mock (an older scripts/mock/mock_hub.py?)")
    }

    @MainActor private func launch(_ extra: [String], oldLayout: Bool) -> XCUIApplication {
        let app = XCUIApplication()
        app.resetAuthorizationStatus(for: .photos) // the alert comes up again, like on a fresh install
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", session, "-ui.oldLayout", oldLayout ? "YES" : "NO"] + extra
        app.launchPinned()
        return app
    }

    /// Taps Allow on the photo library alert (worded in the simulator's language) and checks the app saved and says so
    /// where it can be seen.
    @MainActor private func allowAndExpectSaved(_ app: XCUIApplication, _ name: String) {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.alerts.buttons.matching(NSPredicate(format: "label BEGINSWITH %@ OR label BEGINSWITH %@", "允许", "Allow")).firstMatch
        XCTAssertTrue(allow.waitForExistence(timeout: 10), "no photo library alert:\n\(springboard.debugDescription)")
        allow.tap()
        let saved = app.staticTexts["已保存到相册"].firstMatch // the toast; its words inside it match too
        XCTAssertTrue(saved.waitForExistence(timeout: 10), "no 已保存到相册 (state \(app.state.rawValue)):\n\(app.debugDescription)")
        XCTAssertEqual(app.state, .runningForeground, "the app is gone after saving")
        expectDrawn(saved, app, name)
    }

    /// The toast is drawn in front of the viewer or the card, not under it (#1492): where it is, the screen changes when
    /// it goes. A toast under a cover is still in the accessibility tree, so finding its words proves nothing.
    @MainActor private func expectDrawn(_ toast: XCUIElement, _ app: XCUIApplication, _ name: String) {
        Thread.sleep(forTimeInterval: 0.5) // in place (Theme.Motion.standard); a success toast stays 2.5 s
        let frame = toast.frame
        let shown = app.screenshot()
        let shot = XCTAttachment(screenshot: shown)
        shot.name = name
        shot.lifetime = .keepAlways
        add(shot)
        XCTAssertTrue(toast.waitForNonExistence(timeout: 10), "已保存到相册 never went away")
        Thread.sleep(forTimeInterval: 0.5) // on its way out
        let changed = Self.changed(shown.image, app.screenshot().image, in: frame, width: app.frame.width)
        XCTAssertGreaterThan(changed, 0.03, "已保存到相册 is not drawn where it is (\(name): \(Int(changed * 100))% of its pixels changed when it went): something covers it")
    }

    /// The share of the pixels in `frame` (points; the screen is `width` points wide) that clearly differ between two screenshots.
    private static func changed(_ a: UIImage, _ b: UIImage, in frame: CGRect, width: CGFloat) -> Double {
        guard let pa = pixels(a, frame, width), let pb = pixels(b, frame, width), pa.count == pb.count, !pa.isEmpty else { return 0 }
        var differ = 0
        for i in stride(from: 0, to: pa.count, by: 4) {
            let d = max(abs(Int(pa[i]) - Int(pb[i])), abs(Int(pa[i + 1]) - Int(pb[i + 1])), abs(Int(pa[i + 2]) - Int(pb[i + 2])))
            if d > 48 { differ += 1 }
        }
        return Double(differ) / Double(pa.count / 4)
    }

    /// The RGBA bytes of `frame` (points) in a screenshot.
    private static func pixels(_ image: UIImage, _ frame: CGRect, _ width: CGFloat) -> [UInt8]? {
        guard let cg = image.cgImage, width > 0 else { return nil }
        let scale = CGFloat(cg.width) / width
        let r = frame.intersection(CGRect(x: 0, y: 0, width: width, height: CGFloat(cg.height) / scale))
        let w = Int(r.width * scale), h = Int(r.height * scale)
        guard !r.isNull, w > 0, h > 0 else { return nil }
        var px = [UInt8](repeating: 0, count: w * h * 4)
        guard let ctx = CGContext(data: &px, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                                  space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
        else { return nil }
        ctx.draw(cg, in: CGRect(x: -r.minX * scale, y: -(CGFloat(cg.height) - r.maxY * scale), width: CGFloat(cg.width), height: CGFloat(cg.height)))
        return px
    }

    @MainActor func testSaveFromTheImageViewer() throws { saveFromTheImageViewer(oldLayout: true) }
    @MainActor func testSaveFromTheImageViewerInTheShell() throws { saveFromTheImageViewer(oldLayout: false) }
    @MainActor func testSaveTheShareCard() throws { saveTheShareCard(oldLayout: true) }
    @MainActor func testSaveTheShareCardInTheShell() throws { saveTheShareCard(oldLayout: false) }

    @MainActor private func saveFromTheImageViewer(oldLayout: Bool) {
        let app = launch(["-openFirstImage", "YES"], oldLayout: oldLayout)
        let save = app.buttons["保存"]
        XCTAssertTrue(save.waitForExistence(timeout: 20), "the image viewer did not open:\n\(app.debugDescription)")
        XCTAssertTrue(NSPredicate(format: "isEnabled == true").evaluate(with: save) || {
            _ = XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "isEnabled == true"), object: save)], timeout: 10)
            return save.isEnabled
        }(), "保存 stays disabled: the image never decoded")
        save.tap()
        allowAndExpectSaved(app, oldLayout ? "photo-save-viewer-old" : "photo-save-viewer-shell")
    }

    @MainActor private func saveTheShareCard(oldLayout: Bool) {
        let app = launch(["-showShareCard", "YES"], oldLayout: oldLayout)
        let save = app.buttons["保存"]
        XCTAssertTrue(save.waitForExistence(timeout: 20), "the share card did not open:\n\(app.debugDescription)")
        _ = XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "isEnabled == true"), object: save)], timeout: 10)
        save.tap()
        allowAndExpectSaved(app, oldLayout ? "photo-save-card-old" : "photo-save-card-shell")
    }
}
