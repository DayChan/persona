import Darwin
import XCTest

/// The image viewer (docs/apple/ios.md「图片查看器」): every button of its bars inside the screen (they ran off both sides on an
/// iPhone, 2026-11-05), a tap hides and shows the bars, a pull down closes it. Run it on the narrowest phone too (SE).
///   PORT=18831 MOCK_TURN_IMAGES=many python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18831 xcodebuild test … -only-testing:PersonaUITests/ImageViewerTests
final class ImageViewerTests: XCTestCase {
    private var hub = ""
    /// The blog conversation, whose last turn looked at five pictures (MOCK_TURN_IMAGES=many).
    private let blog = "studio/a1f3c2d4-0000-4000-8000-000000000003"

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TURN_IMAGES=many (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    @MainActor private func open(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", blog, "-openViewedImage", "2"] + extra
        app.launchPinned()
        XCTAssertTrue(app.buttons["viewer-close"].waitForExistence(timeout: 30), "the viewer did not open:\n\(app.debugDescription)")
        return app
    }

    @MainActor func testBarsFitTheScreen() throws {
        let app = open()
        let screen = app.windows.firstMatch.frame
        let controls: [XCUIElement] = [
            app.buttons["viewer-close"], app.descendants(matching: .any)["viewer-title"], app.buttons["viewer-more"],
            app.buttons["分享"], app.buttons["保存"], app.buttons["标注"], app.buttons["信息"], app.buttons["拷贝"],
        ]
        for c in controls {
            XCTAssertTrue(c.waitForExistence(timeout: 10), "missing \(c):\n\(app.debugDescription)")
            XCTAssertTrue(screen.contains(c.frame), "\(c.label) at \(c.frame) is not inside the screen \(screen)")
        }
        // Several pictures: the strip of thumbnails, and the title says which one.
        XCTAssertTrue(app.descendants(matching: .any)["viewer-thumbs"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["viewer-title"].label.contains("2 / 5"))
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "image-viewer"
        shot.lifetime = .keepAlways
        add(shot)
    }

    @MainActor func testTapHidesTheBarsAndPullDownCloses() throws {
        let app = open()
        let close = app.buttons["viewer-close"]
        let middle = app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        middle.tap()
        XCTAssertTrue(waitUntil { !close.isHittable }, "the bars stayed after a tap")
        middle.tap()
        XCTAssertTrue(waitUntil { close.isHittable }, "the bars did not come back")
        // A thumbnail moves to its picture.
        app.buttons["第 1 张"].tap()
        XCTAssertTrue(waitUntil { app.descendants(matching: .any)["viewer-title"].label.contains("1 / 5") })
        middle.press(forDuration: 0.05, thenDragTo: app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.9)))
        XCTAssertTrue(close.waitForNonExistence(timeout: 5), "a pull down did not close the viewer")
    }

    /// Swiping between pictures always ends on a whole page (#5678): quick flicks, slow drags, drags let go halfway
    /// (forwards and back), with the next picture still loading (`-showViewerDecodeDelay`) and with the picture zoomed in.
    /// It used to stop now and then with the picture shifted and a strip of the neighbour showing.
    @MainActor func testSwipesAlwaysEndOnAWholePage() throws {
        let app = open(["-showViewerDecodeDelay", "0.35"])
        let win = app.windows.firstMatch
        let title = app.descendants(matching: .any)["viewer-title"]
        func at(_ x: CGFloat, _ y: CGFloat = 0.5) -> XCUICoordinate { win.coordinate(withNormalizedOffset: CGVector(dx: x, dy: y)) }
        var step = 0
        func check(_ what: String) {
            step += 1
            let aligned = waitUntil(4) { self.pageOffset(app) == 0 }
            if !aligned || step % 6 == 0 || what.hasPrefix("zoomed") {
                let shot = XCTAttachment(screenshot: app.screenshot())
                shot.name = String(format: "paging-%02d-%@", step, what)
                shot.lifetime = .keepAlways
                add(shot)
            }
            XCTAssertTrue(aligned, "\(what): the page stopped \(pageOffset(app).map { "\($0)" } ?? "?") pt off a whole page (\(title.label))")
            // The bars sit where they belong: the title in the middle of the screen.
            XCTAssertEqual(title.frame.midX, win.frame.midX, accuracy: 2, "\(what): the title capsule moved")
        }
        for round in 0..<3 {
            for _ in 0..<2 { win.swipeLeft(velocity: .fast); check("fast-left-\(round)") }
            for _ in 0..<2 { win.swipeRight(velocity: .fast); check("fast-right-\(round)") }
            at(0.85).press(forDuration: 0.05, thenDragTo: at(0.1), withVelocity: .slow, thenHoldForDuration: 0.1)
            check("slow-left-\(round)")
            at(0.15).press(forDuration: 0.05, thenDragTo: at(0.9), withVelocity: .slow, thenHoldForDuration: 0.1)
            check("slow-right-\(round)")
            // Let go halfway, after holding still: it settles on one page or the other, never between.
            for (from, to) in [(0.8, 0.45), (0.8, 0.3), (0.2, 0.6), (0.2, 0.7)] {
                at(from).press(forDuration: 0.05, thenDragTo: at(to), withVelocity: .slow, thenHoldForDuration: 0.6)
                check("halfway-\(from)-\(to)-\(round)")
            }
            // Quick swipes one after another, the next starting while the last still glides.
            for _ in 0..<3 { at(0.8).press(forDuration: 0.01, thenDragTo: at(0.3), withVelocity: .fast, thenHoldForDuration: 0) }
            check("rapid-\(round)")
            for _ in 0..<3 { at(0.2).press(forDuration: 0.01, thenDragTo: at(0.7), withVelocity: .fast, thenHoldForDuration: 0) }
            check("rapid-back-\(round)")
        }
        // Zoomed in: the picture pans first, then the swipe goes on to the next page and lands on it whole.
        for dir in ["left", "right", "left"] {
            at(0.5).doubleTap()
            RunLoop.current.run(until: Date().addingTimeInterval(0.6))
            for _ in 0..<4 { dir == "left" ? win.swipeLeft(velocity: .fast) : win.swipeRight(velocity: .fast) }
            check("zoomed-\(dir)")
        }
    }

    /// UIKit must actually cancel a recognizer that has begun and changed; a normal finger lift is not this test.
    @MainActor func testUIKitCancelledPansAlwaysAlign() throws {
        let app = openProbe()
        let win = app.windows.firstMatch
        for round in 0..<3 {
            for (left, fraction) in [(true, 0.18), (true, 0.65), (false, 0.65), (false, 0.18)] {
                let armed = try ctl("viewerPagingArmCancel", ["fraction": fraction])
                let gesture = try XCTUnwrap(armed["gesture"] as? Int)
                let from = win.coordinate(withNormalizedOffset: CGVector(dx: left ? 0.85 : 0.15, dy: 0.5))
                let to = win.coordinate(withNormalizedOffset: CGVector(dx: left ? 0.1 : 0.9, dy: 0.5))
                from.press(forDuration: 0.05, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.2)
                let sample = try alignedProbe(app)
                attachProbe(sample, name: "cancel-r\(round)-g\(gesture)")
                let events = try XCTUnwrap(sample["events"] as? [[String: Any]])
                let thisPan = events.filter { $0["gesture"] as? Int == gesture && $0["event"] as? String == "pan" }
                let states = thisPan.compactMap { $0["state"] as? String }
                let began = try XCTUnwrap(states.firstIndex(of: "began"), "never began: \(states)")
                let changed = try XCTUnwrap(states.firstIndex(of: "changed"), "never changed: \(states)")
                let cancelled = try XCTUnwrap(states.firstIndex(of: "cancelled"), "UIKit did not cancel: \(states)")
                XCTAssertLessThan(began, changed)
                XCTAssertLessThan(changed, cancelled)
                let cancelledRow = try XCTUnwrap(thisPan.first { $0["state"] as? String == "cancelled" })
                let terminal = try XCTUnwrap(events.first { $0["gesture"] as? Int == gesture && $0["event"] as? String == "settle-request" })
                XCTAssertEqual(terminal["state"] as? String, "cancelled")
                XCTAssertLessThan(try XCTUnwrap(cancelledRow["sequence"] as? Int), try XCTUnwrap(terminal["sequence"] as? Int))
                let settled = try XCTUnwrap(events.first { $0["gesture"] as? Int == gesture && $0["event"] as? String == "settled"
                    && ($0["sequence"] as? Int ?? -1) > (terminal["sequence"] as? Int ?? .max) })
                XCTAssertLessThan(try XCTUnwrap(terminal["sequence"] as? Int), try XCTUnwrap(settled["sequence"] as? Int))
                XCTAssertFalse(states.prefix(cancelled).contains("ended"), "normal release is not a cancelled pan")
                let request = try XCTUnwrap(events.first { $0["gesture"] as? Int == gesture && $0["event"] as? String == "cancel-request" })
                XCTAssertEqual(request["state"] as? String, "changed")
                XCTAssertEqual(request["dragging"] as? Bool, true)
                let offset = try XCTUnwrap(request["offset"] as? Double)
                let width = try XCTUnwrap(request["width"] as? Double)
                XCTAssertGreaterThan(abs(offset - (offset / width).rounded() * width), 1, "cancel must occur between pages")
            }
        }
        attachShot(app, name: "light-system-cancelled-pans-aligned")
    }

    /// The decoder starts only after the real pager begins changing. Its completion AND the UIKit page update must
    /// occur while that same gesture is still changed/dragging, strictly before the settled event.
    @MainActor func testDecodeActuallyCompletesDuringPan() throws {
        for round in 0..<3 {
            let app = openProbe(["-showViewerDecodeDuringPan", "YES", "-ui.scheme", round == 2 ? "dark" : "light"])
            XCTAssertTrue(waitUntil(10) { (try? self.ctl("viewerPaging")["decodeWaiting"] as? Bool) == true }, "decode gate never armed")
            let win = app.windows.firstMatch
            win.coordinate(withNormalizedOffset: CGVector(dx: 0.85, dy: 0.5)).press(
                forDuration: 0.05,
                thenDragTo: win.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.5)),
                withVelocity: .slow, thenHoldForDuration: 0.2)
            let sample = try alignedProbe(app)
            attachProbe(sample, name: "decode-during-pan-r\(round)")
            let events = try XCTUnwrap(sample["events"] as? [[String: Any]])
            let completed = try XCTUnwrap(events.first { $0["event"] as? String == "decode-complete" && $0["page"] as? Int == 2 })
            let gesture = try XCTUnwrap(completed["gesture"] as? Int)
            XCTAssertEqual(completed["state"] as? String, "changed")
            XCTAssertEqual(completed["dragging"] as? Bool, true)
            let updated = try XCTUnwrap(events.first { $0["event"] as? String == "image-update" && $0["page"] as? Int == 2 })
            XCTAssertEqual(updated["state"] as? String, "changed")
            XCTAssertEqual(updated["dragging"] as? Bool, true)
            XCTAssertEqual(updated["gesture"] as? Int, gesture)
            let started = try XCTUnwrap(events.first { $0["event"] as? String == "decode-start" && $0["page"] as? Int == 2 })
            let settled = try XCTUnwrap(events.first { $0["event"] as? String == "settled" && $0["gesture"] as? Int == gesture })
            XCTAssertLessThan(try XCTUnwrap(started["sequence"] as? Int), try XCTUnwrap(completed["sequence"] as? Int))
            XCTAssertLessThan(try XCTUnwrap(completed["sequence"] as? Int), try XCTUnwrap(updated["sequence"] as? Int))
            XCTAssertLessThan(try XCTUnwrap(updated["sequence"] as? Int), try XCTUnwrap(settled["sequence"] as? Int))
            XCTAssertEqual(sample["page"] as? Int, 2, "the swipe must actually reach the newly decoded neighbour")
            if round == 2 { XCTAssertEqual(sample["appearance"] as? String, "dark") }
            attachShot(app, name: round == 2 ? "dark-decode-during-pan-aligned" : "light-decode-during-pan-r\(round)")
            app.terminate()
        }
    }

    @MainActor func testDarkViewerPagingAndChrome() throws {
        let app = openProbe(["-ui.scheme", "dark"])
        let win = app.windows.firstMatch
        for _ in 0..<2 {
            win.swipeLeft(velocity: .fast)
            _ = try alignedProbe(app)
            win.swipeRight(velocity: .fast)
            _ = try alignedProbe(app)
            win.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.5)).press(
                forDuration: 0.05, thenDragTo: win.coordinate(withNormalizedOffset: CGVector(dx: 0.45, dy: 0.5)),
                withVelocity: .slow, thenHoldForDuration: 0.6)
            _ = try alignedProbe(app)
        }
        let sample = try alignedProbe(app)
        XCTAssertEqual(sample["appearance"] as? String, "dark")
        for id in ["viewer-close", "viewer-title", "viewer-more"] {
            let control = app.descendants(matching: .any)[id]
            XCTAssertTrue(win.frame.contains(control.frame), "\(id) ran offscreen")
        }
        attachProbe(sample, name: "dark-paging")
        attachShot(app, name: "dark-viewer-paging-and-chrome")
    }

    private static let probePort = Int(ProcessInfo.processInfo.environment["PERSONA_VIEWER_CLI_PORT"] ?? "18932") ?? 18932
    private static let probeToken = UUID().uuidString

    /// A picture that did not load (#182), light and dark: the page says so with Retry instead of spinning; a tap on it
    /// leaves the bars (Close) and the page's own backdrop (it used to go black, the words black on black and Close
    /// gone); Retry brings the picture; Close closes. The mock fails lazy pictures until `/mock/images?ok=1`.
    @MainActor func testAPictureThatDidNotLoadSaysSoWithRetry() throws {
        // The other tests' pictures load, however this one ends (a failed assertion skips `defer`).
        let letThrough = URL(string: hub + "/mock/images?ok=1")!
        addTeardownBlock { _ = try? Data(contentsOf: letThrough) }
        for scheme in ["light", "dark"] {
            mock("images")
            let app = open(Self.fresh + ["-ui.scheme", scheme])
            dismissAlerts()
            // `-openViewedImage 2` opens on the second picture: page 1.
            let failed = app.descendants(matching: .any)["viewer-not-loaded-1"].firstMatch
            XCTAssertTrue(failed.waitForExistence(timeout: 20), "no word that the picture did not load:\n\(app.debugDescription)")
            XCTAssertTrue(app.staticTexts["图片没载入"].exists)
            shot(app, "\(scheme)-not-loaded")
            let close = app.buttons["viewer-close"]
            app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)).tap()
            RunLoop.current.run(until: Date().addingTimeInterval(0.6))
            XCTAssertTrue(close.isHittable, "a tap on the page that did not load hid Close")
            XCTAssertTrue(failed.exists)
            shot(app, "\(scheme)-not-loaded-tapped")
            mock("images?ok=1")
            app.buttons["重试"].firstMatch.tap()
            XCTAssertTrue(app.descendants(matching: .any)["viewer-page-1"].waitForExistence(timeout: 20), "Retry did not bring the picture")
            XCTAssertTrue(waitUntil { !failed.exists })
            shot(app, "\(scheme)-retried")
            close.tap()
            XCTAssertTrue(close.waitForNonExistence(timeout: 5), "Close did not close the viewer")
            app.terminate()
        }
    }

    /// Long-pressing a viewed image shows its preview and its menu, light and dark, and the app lives on (#1490: the
    /// preview read the app's model, which the context menu's own host did not have, and the app crashed).
    @MainActor func testLongPressingAViewedImageShowsItsPreview() throws {
        mock("images?ok=1")
        for scheme in ["light", "dark"] {
            let app = openChat(Self.fresh + ["-ui.scheme", scheme])
            dismissAlerts()
            let tile = app.buttons["viewed-image-0"]
            XCTAssertTrue(tile.waitForExistence(timeout: 30), "no viewed images:\n\(app.debugDescription)")
            tile.press(forDuration: 1.2)
            XCTAssertTrue(app.buttons["拷贝路径"].waitForExistence(timeout: 5), "the tile's menu did not open")
            XCTAssertEqual(app.state, .runningForeground, "the app is gone")
            shot(app, "\(scheme)-viewed-image-preview")
            // Outside the preview and the menu (both sit in the upper half): the dimmed chat closes them.
            app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85)).tap()
            XCTAssertTrue(app.buttons["拷贝路径"].waitForNonExistence(timeout: 5), "the menu did not close")
            XCTAssertTrue(tile.exists, "the chat is gone")
            app.terminate()
        }
    }

    /// A viewed image that did not load, on a wide and a narrow tile (#559), light and dark: the tile says so, and a tap
    /// asks the hub again (the mock counts what it is asked) and brings the picture without opening the viewer; once
    /// it is there, a tap opens the viewer as before.
    @MainActor func testATileThatDidNotLoadRetriesWithoutOpeningTheViewer() throws {
        let letThrough = URL(string: hub + "/mock/images?ok=1")!
        addTeardownBlock { _ = try? Data(contentsOf: letThrough) }
        for scheme in ["light", "dark"] {
            mock("images")
            let app = openChat(Self.fresh + ["-ui.scheme", scheme])
            dismissAlerts()
            let tiles = (0..<5).map { app.buttons["viewed-image-\($0)"] }
            XCTAssertTrue(tiles[0].waitForExistence(timeout: 30), "no viewed images:\n\(app.debugDescription)")
            var failed: [XCUIElement] = []
            XCTAssertTrue(waitUntil(20) {
                failed = tiles.filter { $0.exists && $0.label == Self.notLoaded }
                return failed.count >= 2
            }, "the tiles did not say their pictures did not load:\n\(app.debugDescription)")
            let narrow = try XCTUnwrap(failed.min { $0.frame.width < $1.frame.width })
            let wide = try XCTUnwrap(failed.max { $0.frame.width < $1.frame.width })
            XCTAssertLessThan(narrow.frame.width, wide.frame.width, "no narrow tile among \(failed.map(\.frame))")
            shot(app, "\(scheme)-tiles-not-loaded")
            mock("images?ok=1")
            RunLoop.current.run(until: Date().addingTimeInterval(1))
            XCTAssertEqual(narrow.label, Self.notLoaded, "a tile loaded again without a tap")
            for tile in [narrow, wide] { try retries(tile, in: app) }
            shot(app, "\(scheme)-tiles-retried")
            wide.tap()
            XCTAssertTrue(app.buttons["viewer-close"].waitForExistence(timeout: 10), "a tile with its picture did not open the viewer")
            app.terminate()
        }
    }

    /// A sent photo that did not load (#559), light and dark: the same as a viewed image's tile. Needs the photos lazy:
    /// `MOCK_CHAT_IMAGES=many MOCK_IMAGES_FAIL=1` (with `MOCK_TURN_IMAGES=many`); skipped otherwise. Run it alone there:
    /// that mock has a second strip of viewed images, which the tile test can't tell apart.
    @MainActor func testASentPhotoThatDidNotLoadRetriesWithoutOpeningTheViewer() throws {
        guard mockImages()["sent_photos_lazy"] as? Bool == true else {
            throw XCTSkip("needs MOCK_CHAT_IMAGES=many MOCK_IMAGES_FAIL=1 (sent photos lazy)")
        }
        let letThrough = URL(string: hub + "/mock/images?ok=1")!
        addTeardownBlock { _ = try? Data(contentsOf: letThrough) }
        for scheme in ["light", "dark"] {
            mock("images")
            let app = openChat(Self.fresh + ["-ui.scheme", scheme])
            dismissAlerts()
            // The photo is near the top of the conversation. (A row off screen can be in the tree with no place to tap:
            // `isHittable` fails on it rather than saying no, so ask for its middle on screen.)
            let photo = app.buttons["sent-photo-0"].firstMatch
            let screen = app.windows.firstMatch.frame.insetBy(dx: 0, dy: 120)
            let shown = { photo.exists && screen.contains(CGPoint(x: photo.frame.midX, y: photo.frame.midY)) }
            for _ in 0..<20 where !shown() { app.swipeDown(velocity: .fast) }
            XCTAssertTrue(shown(), "no sent photo on screen:\n\(app.debugDescription)")
            XCTAssertTrue(waitUntil(20) { photo.label == Self.notLoaded }, "the photo did not say it did not load: \(photo.label)")
            shot(app, "\(scheme)-sent-not-loaded")
            mock("images?ok=1")
            try retries(photo, in: app)
            shot(app, "\(scheme)-sent-retried")
            app.terminate()
        }
    }

    /// No picture kept on disk from an earlier run, so a failing mock fails it, and no chat kept from a mock whose items
    /// have other ids (`MOCK_CHAT_IMAGES` shifts them): a kept chat opened the viewer on refs that are not pictures.
    private static let fresh = ["-openImagesFresh", "YES", "-openChatsFresh", "YES"]

    /// What a tile that did not load says to VoiceOver (zh-Hans).
    private static let notLoaded = "图片没载入，点按重试"

    /// A tap on a tile that did not load asks the hub again and shows its picture, without opening the viewer.
    @MainActor private func retries(_ tile: XCUIElement, in app: XCUIApplication) throws {
        let before = try XCTUnwrap(mockImages()["asked"] as? Int)
        tile.tap()
        XCTAssertTrue(waitUntil(10) { tile.label != Self.notLoaded }, "the tap did not bring the picture back")
        XCTAssertGreaterThan(try XCTUnwrap(mockImages()["asked"] as? Int), before, "the tap did not ask the hub again")
        XCTAssertFalse(app.buttons["viewer-close"].exists, "a tap on a tile that did not load opened the viewer")
    }

    /// The blog conversation, without opening the viewer.
    @MainActor private func openChat(_ extra: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", blog] + extra
        app.launchPinned()
        return app
    }

    /// SpringBoard's notification question, and 「你已更改“Persona”的图标」 (the icon follows the appearance).
    private func dismissAlerts() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for _ in 0..<2 {
            let ok = springboard.alerts.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许", "OK", "好"])).firstMatch
            if ok.waitForExistence(timeout: 2) { ok.tap() }
        }
    }

    /// What the mock says of its lazy images (`/mock/images?asked=1`, which changes nothing).
    private func mockImages() -> [String: Any] {
        guard let d = try? Data(contentsOf: URL(string: hub + "/mock/images?asked=1")!) else { return [:] }
        return (try? JSONSerialization.jsonObject(with: d)) as? [String: Any] ?? [:]
    }

    /// Tells the mock hub something (`/mock/<what>`).
    private func mock(_ what: String) {
        _ = try? Data(contentsOf: URL(string: hub + "/mock/" + what)!)
    }

    /// An attachment, and a file in PERSONA_SHOTS when set.
    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        attachShot(app, name: name)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/image-\(name).png"))
        }
    }

    @MainActor private func openProbe(_ extra: [String] = []) -> XCUIApplication {
        let scheme = extra.contains("-ui.scheme") ? [] : ["-ui.scheme", "light"]
        return open(["-showViewerPagingProbe", "YES", "-runtimeCLI", "\(Self.probePort)", "-runtimeCLIToken", Self.probeToken] + scheme + extra)
    }

    @MainActor private func alignedProbe(_ app: XCUIApplication) throws -> [String: Any] {
        var sample: [String: Any] = [:]
        let aligned = waitUntil(5) {
            sample = (try? self.ctl("viewerPaging")) ?? [:]
            // Keep the non-Sendable probe dictionary on this actor; Boolean
            // autoclosures capture only the copied scalar values.
            let dragging = sample["dragging"] as? Bool
            let decelerating = sample["decelerating"] as? Bool
            let alignmentError = sample["alignmentError"] as? Double ?? .infinity
            return dragging == false && decelerating == false && alignmentError < 0.01
        }
        if !aligned { attachProbe(sample, name: "alignment-timeout") }
        XCTAssertTrue(aligned, "pager did not settle: \(sample)")
        XCTAssertEqual(try XCTUnwrap(sample["alignmentError"] as? Double), 0, accuracy: 0.01)
        XCTAssertEqual(app.descendants(matching: .any)["viewer-title"].frame.midX, app.windows.firstMatch.frame.midX, accuracy: 2)
        return sample
    }

    private func attachProbe(_ sample: [String: Any], name: String) {
        let data = try! JSONSerialization.data(withJSONObject: sample, options: [.prettyPrinted, .sortedKeys])
        let attachment = XCTAttachment(data: data, uniformTypeIdentifier: "public.json")
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    @MainActor private func attachShot(_ app: XCUIApplication, name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    /// The existing DEBUG runtime channel, with socket timeouts: waiting for a missing probe must fail, not hang.
    private func ctl(_ command: String, _ args: [String: Any] = [:]) throws -> [String: Any] {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { throw NSError(domain: "ImageViewerProbe", code: 1) }
        defer { Darwin.close(fd) }
        var timeout = timeval(tv_sec: 3, tv_usec: 0)
        _ = setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
        _ = setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
        var noPipe: Int32 = 1
        _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noPipe, socklen_t(MemoryLayout<Int32>.size))
        var address = sockaddr_in()
        address.sin_family = sa_family_t(AF_INET)
        address.sin_port = in_port_t(UInt16(Self.probePort).bigEndian)
        address.sin_addr.s_addr = inet_addr("127.0.0.1")
        let connected = withUnsafePointer(to: &address) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        guard connected == 0 else { throw NSError(domain: "ImageViewerProbe", code: 2) }
        var request = try JSONSerialization.data(withJSONObject: ["token": Self.probeToken, "cmd": command, "args": args])
        request.append(0x0A)
        let sent = request.withUnsafeBytes { send(fd, $0.baseAddress, request.count, 0) }
        guard sent == request.count else { throw NSError(domain: "ImageViewerProbe", code: 3) }
        var reply = Data(), buffer = [UInt8](repeating: 0, count: 65536)
        while reply.last != 0x0A {
            let n = recv(fd, &buffer, buffer.count, 0)
            guard n > 0, reply.count < 4 << 20 else { throw NSError(domain: "ImageViewerProbe", code: 4) }
            reply.append(contentsOf: buffer.prefix(n))
        }
        let result = try XCTUnwrap(try JSONSerialization.jsonObject(with: reply) as? [String: Any])
        XCTAssertEqual(result["ok"] as? Bool, true, "\(result)")
        return result
    }

    /// How far the page under the middle of the screen is from lying exactly on it, in points (0: a whole page).
    @MainActor private func pageOffset(_ app: XCUIApplication) -> CGFloat? {
        let screen = app.windows.firstMatch.frame
        let pages = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'viewer-page-'"))
        for p in pages.allElementsBoundByIndex {
            let f = p.frame
            guard f.width > 0, f.minX < screen.midX, f.maxX > screen.midX else { continue }
            let off = abs(f.minX - screen.minX)
            return off < 1 ? 0 : off
        }
        return nil
    }

    private func waitUntil(_ timeout: TimeInterval = 5, _ ok: @escaping () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if ok() { return true }
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        return ok()
    }
}
