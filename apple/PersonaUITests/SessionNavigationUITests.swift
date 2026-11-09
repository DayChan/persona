import XCTest

/// Real gestures against scripts/mock/mock_hub.py with MOCK_NAVIGATION=1 and PERSONA_MOCK_HUB. The fixture's main
/// session has eighty human questions, only 79/80 in the initial body, and an independent forty-question index.
/// Record the simulator during the edge drag to inspect the real held preview; the separate held screenshot below
/// is a composition fixture, not evidence that the real gesture ran.
final class SessionNavigationUITests: XCTestCase {
    private var hub = ""
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    private struct NavigationShown: Decodable {
        var mode: String
        var pickedSeq: Int?
        var marks: Int
        var fetched: Int
        var lastPickedSeq: Int?
        var lastText: String?
        var lastAnswer: String?
        var window: Bool
    }
    private struct Journal: Decodable {
        struct Request: Decodable {
            var kind: String
            var beforeSeq: Int?
            var aroundSeq: Int?
            var history: String?
            enum CodingKeys: String, CodingKey { case kind, history; case beforeSeq = "before_seq", aroundSeq = "around_seq" }
        }
        var requests: [Request]
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("Needs the MOCK_NAVIGATION=1 fixture and PERSONA_MOCK_HUB")
        }
        hub = value
        let request = URLRequest(url: URL(string: hub + "/mock/reset")!, timeoutInterval: 10)
        let done = expectation(description: "Reset the navigation fixture")
        URLSession.shared.dataTask(with: request) { @Sendable _, _, _ in done.fulfill() }.resume()
        XCTAssertEqual(XCTWaiter.wait(for: [done], timeout: 12), .completed)
    }

    @MainActor private func launch(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main,
                               "-openChatsFresh", "YES", "-probes", "YES", "-showScrollState", "YES", "-ui.newChatLayout", "YES"] + extra
        app.launchPinned()
        eventually("Forty independent navigation marks", within: 20) {
            let shown = app.probe("session-navigation", as: NavigationShown.self, page: "assistant")
            return shown?.fetched == 40 ? [] : ["Expected forty fetched user-input marks; got \(shown?.fetched ?? -1)"]
        }
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = "session-navigation-" + name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
            try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: directory)
                .appending(path: "session-navigation-\(appearance)-\(name).png"))
        }
    }

    private func journal() throws -> Journal {
        // The request has a timeout; waiting for its callback is bounded independently.
        let request = URLRequest(url: URL(string: hub + "/mock/navigation")!, timeoutInterval: 10)
        let done = expectation(description: "Read navigation requests")
        let result = JournalResult()
        URLSession.shared.dataTask(with: request) { @Sendable data, _, error in
            result.finish(data: data, error: error)
            done.fulfill()
        }.resume()
        XCTAssertEqual(XCTWaiter.wait(for: [done], timeout: 12), .completed)
        let (data, error) = result.snapshot
        if let error { throw error }
        return try JSONDecoder().decode(Journal.self, from: XCTUnwrap(data))
    }

    @MainActor func testCentralScrollShowsCurrentInputThenFadesWithoutLoadingTargetBody() throws {
        let app = launch()
        defer { app.terminate() }
        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.38))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.58))
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .slow, thenHoldForDuration: 0.2)
        let shown = try XCTUnwrap(app.probe("session-navigation", as: NavigationShown.self, page: "assistant"))
        XCTAssertEqual(shown.mode, "passive", "The central gesture must reveal the navigator, not activate scrubbing")
        XCTAssertNotNil(shown.pickedSeq, "The current visible content must have its preceding real user highlighted")
        XCTAssertFalse(shown.window)
        XCTAssertFalse(app.descendants(matching: .any)["chat.edgeScrub.preview"].firstMatch.exists)
        try shot(app, "central-passive")
        eventually("Passive navigator fades after scrolling stops", within: 5) {
            app.probe("session-navigation", as: NavigationShown.self, page: "assistant")?.mode == "hidden" ? [] : ["Navigator did not fade"]
        }
        XCTAssertTrue(try journal().requests.filter { $0.kind == "around" }.isEmpty)
        try shot(app, "central-faded")
    }

    @MainActor func testRealEdgeGesturePreviewsQuestionAnswerAndLoadsAbsentTargetOnRelease() throws {
        let app = launch()
        defer { app.terminate() }
        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.985, dy: 0.75))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.985, dy: 0.18))
        // The one-second hold is for real gesture video capture, before release starts the bounded body request.
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .slow, thenHoldForDuration: 1)
        eventually("Release loads a separate around window", within: 15) {
            app.probe("session-navigation", as: NavigationShown.self, page: "assistant")?.window == true ? [] : ["No isolated reading window"]
        }
        let shown = try XCTUnwrap(app.probe("session-navigation", as: NavigationShown.self, page: "assistant"))
        let seq = try XCTUnwrap(shown.lastPickedSeq)
        XCTAssertLessThan(seq, 790, "The gesture must select an input absent from the initial two-turn body")
        XCTAssertEqual(shown.lastText, "Navigation question \(seq / 10)")
        XCTAssertEqual(shown.lastAnswer, "Brief answer \(seq / 10)")
        let requests = try journal().requests
        XCTAssertTrue(requests.contains { $0.kind == "navigation" && $0.beforeSeq != nil }, "Earlier-half movement did not prefetch older indexes")
        XCTAssertEqual(requests.filter { $0.kind == "around" }.map(\.aroundSeq), [seq])
        let target = app.staticTexts.matching(NSPredicate(format: "label == %@", "Navigation question \(seq / 10)")).firstMatch
        XCTAssertTrue(target.waitForExistence(timeout: 10), "The released target body is missing")
        try shot(app, "edge-around-landed")
    }

    @MainActor func testQuestionAndAnswerPreviewComposition() throws {
        let app = launch(["-showEdgeScrub", "3"])
        defer { app.terminate() }
        let preview = app.descendants(matching: .any)["chat.edgeScrub.preview"].firstMatch
        XCTAssertTrue(preview.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Navigation question 78"].exists)
        XCTAssertTrue(app.staticTexts["Brief answer 78"].exists)
        try shot(app, "held-preview-composition")
    }
}

/// Locked across the URLSession callback and the test waiter, including timeout/late-response paths.
nonisolated private final class JournalResult: @unchecked Sendable {
    private let lock = NSLock()
    private var data: Data?
    private var error: Error?
    func finish(data: Data?, error: Error?) { lock.withLock { self.data = data; self.error = error } }
    var snapshot: (Data?, Error?) { lock.withLock { (data, error) } }
}
