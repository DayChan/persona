import XCTest

/// iPhone Duo open (regular width): the Agents list is the split's sidebar and stays put while the agents it opens
/// replace each other in the detail column (docs/apple/ios.md「iPhone Duo」). Needs an unfolded iPhone Duo simulator and
/// scripts/mock/mock_hub.py:
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/DuoSplitTests
final class DuoSplitTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-tab", "dev", "-openAgentsFresh", "YES"]
        app.launchPinned()
        guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("not a split: needs iPhone Duo open") }
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(screenshot: app.screenshot())
            a.lifetime = .keepAlways
            add(a)
        }
        super.tearDown()
    }

    /// The sidebar's list: the collection view at the leading edge.
    private var sidebar: XCUIElement {
        app.collectionViews.allElementsBoundByIndex.first { $0.frame.minX < 40 && $0.frame.width < app.windows.firstMatch.frame.width * 0.7 }
            ?? app.collectionViews.firstMatch
    }

    /// Agent rows in the sidebar, top to bottom.
    private func rows() -> [XCUIElement] {
        let list = sidebar.frame
        return sidebar.cells.allElementsBoundByIndex
            .filter { $0.isHittable && $0.frame.height > 40 && $0.frame.maxY < list.maxY - 20 && $0.frame.minY > list.minY + 60 }
    }

    func testSwitchingAgentsKeepsTheSidebarStill() {
        XCTAssertTrue(app.collectionViews.firstMatch.waitForExistence(timeout: 15))
        sleep(2)
        let candidates = rows()
        guard candidates.count >= 2 else {
            print("SIDEBAR TREE", sidebar.debugDescription.prefix(6000))
            return XCTFail("fewer than two agent rows in the sidebar")
        }
        print("DUO ROWS", candidates.map { "\($0.label) @\(Int($0.frame.minX)),\(Int($0.frame.minY)) \(Int($0.frame.width))x\(Int($0.frame.height))" })
        print("DUO WINDOW", app.windows.firstMatch.frame, "SIDEBAR", sidebar.frame)
        candidates[0].tap()
        sleep(2)
        print("DUO AFTER FIRST", app.staticTexts.allElementsBoundByIndex.filter { $0.frame.minX > sidebar.frame.maxX }.prefix(8).map { $0.label })
        let still = sidebar.frame

        // The switch: shoot the screen through the transition and compare the sidebar's pixels with the still frame
        // (XCUITest's frames do not follow the columns' animation).
        let before = app.screenshot().image
        rows().first { $0.frame.minY > candidates[0].frame.maxY - 1 }?.tap() ?? candidates[1].tap()
        var frames: [UIImage] = []
        let end = Date.now.addingTimeInterval(1.5)
        while Date.now < end { frames.append(app.screenshot().image) }
        print("DUO AFTER SECOND", app.staticTexts.allElementsBoundByIndex.filter { $0.frame.minX > sidebar.frame.maxX }.prefix(8).map { $0.label })
        let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"]
        if let dir {
            try? before.pngData()?.write(to: URL(fileURLWithPath: dir).appendingPathComponent("duo-0.png"))
            for (i, f) in frames.enumerated() {
                try? f.pngData()?.write(to: URL(fileURLWithPath: dir).appendingPathComponent("duo-\(i + 1).png"))
            }
        }
        let side = CGRect(x: still.minX + 8, y: still.minY + 120, width: still.width - 16, height: min(still.height - 240, 500))
        let base = Self.luma(before, in: side)
        let worst = frames.map { Self.difference(base, Self.luma($0, in: side)) }.max() ?? 0
        XCTAssertLessThan(worst, 0.05, "the sidebar changed (\(worst) of its pixels) while switching agents, \(frames.count) frames")
    }

    /// An agent opened beside the list has no ‹ (nothing to go back to: the list is there). With the list closed by its
    /// toolbar button, the chat's ‹ brings it back: the chat draws its own header, so no system button would. Before,
    /// the row's glass drew the hidden ‹ anyway and it did nothing, with or without the list. With nothing open, the
    /// empty page offers Show List instead.
    func testBackBringsTheClosedListBack() {
        app.terminate()
        // The chat as the split's first page (`-openSession`), its header held open to be tapped.
        app.launchArguments += ["-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000002", "-holdChromeOpen", "YES"]
        app.launchPinned()
        let back = app.buttons["返回"]
        let closeList = app.buttons["隐藏边栏"]
        XCTAssertTrue(app.staticTexts["补齐网络层的单元测试"].waitForExistence(timeout: 15), "the chat did not open")
        XCTAssertTrue(closeList.waitForExistence(timeout: 10), "no button to close the list")
        sleep(1)
        XCTAssertFalse(back.exists, "a ‹ beside the list, with nothing to go back to")
        close(closeList)
        XCTAssertTrue(back.waitForExistence(timeout: 5), "the list is closed and the chat has no way back to it")
        back.tap()
        XCTAssertTrue(closeList.waitForExistence(timeout: 5), "‹ did not bring the list back")
        XCTAssertFalse(back.exists, "‹ still there with the list beside the chat")
    }

    /// One search field: the system's drawer above the list. The list's first row has the quick filters alone, not the
    /// phone's own field over them too (#1971: the sidebar column reads compact while the page is regular).
    func testOneSearchField() {
        XCTAssertTrue(sidebar.waitForExistence(timeout: 15))
        sleep(2)
        let fields = app.searchFields.allElementsBoundByIndex
        print("DUO SEARCH FIELDS", fields.map { "\($0.frame)" }, "SIDEBAR", sidebar.frame)
        XCTAssertEqual(sidebar.searchFields.count, 0, "a search field in the list's rows")
        XCTAssertEqual(fields.count, 1, "search fields: \(fields.map { "\($0.frame)" })")
    }

    /// Nothing open and the list closed: the empty page says how to get the list back (it filled the screen, no way back).
    func testEmptyPageBringsTheClosedListBack() {
        let closeList = app.buttons["隐藏边栏"]
        XCTAssertTrue(closeList.waitForExistence(timeout: 15), "no button to close the list")
        let show = app.buttons["显示列表"]
        XCTAssertFalse(show.exists, "Show List beside the list")
        close(closeList)
        XCTAssertTrue(show.waitForExistence(timeout: 5), "the list is closed and the empty page has no way back to it")
        show.tap()
        XCTAssertTrue(closeList.waitForExistence(timeout: 5), "Show List did not bring the list back")
    }

    /// Closes the list with its toolbar button (the system's). A tap on it now and then did nothing (2 runs in 6, the
    /// list still there): tapped again until the list goes.
    private func close(_ closeList: XCUIElement) {
        for _ in 0..<3 {
            closeList.tap()
            let deadline = Date.now.addingTimeInterval(3)
            while Date.now < deadline, closeList.isHittable { usleep(200_000) }
            if !closeList.isHittable { return }
        }
        XCTFail("the list's button did not close it")
    }

    /// Grey levels of a region (points), sampled every 4 points.
    private static func luma(_ image: UIImage, in rect: CGRect) -> [UInt8] {
        guard let cg = image.cgImage else { return [] }
        let scale = CGFloat(cg.width) / image.size.width
        let w = Int(image.size.width / 4), h = Int(image.size.height / 4)
        var px = [UInt8](repeating: 0, count: w * h)
        let ctx = CGContext(data: &px, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w, space: CGColorSpaceCreateDeviceGray(),
                            bitmapInfo: CGImageAlphaInfo.none.rawValue)!
        ctx.draw(cg, in: CGRect(x: 0, y: 0, width: w, height: h))
        _ = scale
        var out: [UInt8] = []
        for y in Int(rect.minY / 4)..<min(h, Int(rect.maxY / 4)) {
            for x in Int(rect.minX / 4)..<min(w, Int(rect.maxX / 4)) { out.append(px[y * w + x]) }
        }
        return out
    }

    /// The share of samples that differ by more than a little.
    private static func difference(_ a: [UInt8], _ b: [UInt8]) -> Double {
        guard a.count == b.count, !a.isEmpty else { return 1 }
        return Double(zip(a, b).filter { abs(Int($0) - Int($1)) > 24 }.count) / Double(a.count)
    }
}
