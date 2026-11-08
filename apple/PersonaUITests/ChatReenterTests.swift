import XCTest

/// An Agent chat read up to its first message, left for the list and opened again shows what it showed, not an empty
/// page (scripts/mock/mock_hub.py, PERSONA_MOCK_HUB). The chat remembers its top line on screen when it is left and
/// lands on it when opened again (`ChatView.rememberReadPosition` / `landEntry`).
final class ChatReenterTests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private struct Chat {
        var id: String
        var first: String
        var title = ""
    }
    /// An idle chat of the mock with no cards waiting: it opens where it was left, not at its latest answer.
    private let review = Chat(id: "macbook/a1f3c2d4-0000-4000-8000-000000000006", first: "看一下 PR #42")
    /// `MOCK_TAB_PANES=1`: an idle chat in a herdr tab of a workspace with three tabs, so its header has the tabs' row
    /// under the title (a taller header), as the chat this was found in.
    private let tabbed = Chat(id: "server/7ab5e000-0000-4000-8000-000000000006", first: "hi", title: "缓存方案 B 的基准")

    override func tearDownWithError() throws {
        _ = try? Data(contentsOf: URL(string: hub + "/mock/delay?s=0")!)
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
        app = XCUIApplication()
    }

    /// About two screens, as the chat this was found in.
    @MainActor func testATwoScreenChatReadToItsTopOpensWithItsMessages() throws {
        try reenterAtTop(review, turns: 3)
    }

    /// Many screens.
    @MainActor func testALongChatReadToItsTopOpensWithItsMessages() throws {
        try reenterAtTop(review, turns: 12)
    }

    /// About two screens with a folded work line and Markdown answers, under the taller header of a chat whose
    /// workspace has other tabs (`MOCK_TAB_PANES=1`), as the chat this was found in.
    @MainActor func testATabbedChatReadToItsTopOpensWithItsMessages() throws {
        try reenterAtTop(tabbed, turns: 0)
    }

    /// The same, opened again over a hub as slow as one across the internet: the page and the pushes that follow the
    /// subscription come in while the list is landing.
    @MainActor func testATabbedChatComesBackOverASlowHub() throws {
        try reenterAtTop(tabbed, turns: 0, slowBack: true)
    }

    /// The same, back from its row in the list (pushed with the navigation's slide, as a finger does it).
    @MainActor func testATabbedChatComesBackFromItsRow() throws {
        try reenterAtTop(tabbed, turns: 0, fromRow: true)
    }

    /// The same, when its transcript was replaced while it was away (a new history token: the chat starts over from the
    /// hub's latest page while it lands, docs/protocol/protocol.md "Item continuity").
    @MainActor func testATabbedChatWhoseTranscriptWasReplacedComesBack() throws {
        try reenterAtTop(tabbed, turns: 0, newHistory: true)
    }

    /// The same in the large text size (Settings › Appearance): rows grow further from the list's estimates.
    @MainActor func testATabbedChatInLargeTextComesBack() throws {
        try reenterAtTop(tabbed, turns: 0, extra: ["-ui.textSize", "large"])
    }

    /// A landing that leaves nothing drawn (`-showBlankLanding YES`: thrown off its place and taken for blank, as the
    /// page of #1386 was) is built again and lands on the saved place once more (`ChatView.mendBlankLanding`).
    @MainActor func testABlankLandingIsMended() throws {
        try reenterAtTop(review, turns: 3, extra: ["-showBlankLanding", "YES"], blankLanding: true)
    }

    @MainActor private func reenterAtTop(_ chat: Chat, turns: Int, slowBack: Bool = false, fromRow: Bool = false,
                                         newHistory: Bool = false, extra: [String] = [], blankLanding: Bool = false) throws {
        // Each chat is in one mock mode only: the other's run skips it.
        let sid = String(chat.id.split(separator: "/")[1])
        let list = try String(decoding: Data(contentsOf: URL(string: hub + "/api/v1/sessions")!), as: UTF8.self)
        guard list.contains(sid) else { throw XCTSkip("not in this mock's sessions: \(chat.id)") }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // No chat kept on disk from an earlier run: its seqs would not match the mock's after a reset.
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-openChatsFresh", "YES", "-tab", "dev", "-openSession", chat.id, "-probes", "YES"] + extra
        app.launchPinned()
        let first = app.staticTexts.matching(NSPredicate(format: "label == %@", chat.first)).firstMatch
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label IN %@", ["返回", "Back"])).firstMatch.waitForExistence(timeout: 20), "the chat did not open")
        sleep(2)
        for n in stride(from: 1, through: turns, by: 1) {
            try say(user: "第 \(n) 个问题：这一段改动会不会影响缓存的失效时间，旧数据还会不会被读到？",
                    reply: "第 \(n) 段回复：看过了。缓存的失效时间没有变，旧数据在写入新值时就被替换，读路径先查版本号再取值，"
                        + "所以不会读到旧数据。另外顺手补了两条测试，覆盖并发写入和过期后的第一次读取。下一步看看重试的退避策略要不要一起改。")
            XCTAssertTrue(text("第 \(n) 段回复").waitForExistence(timeout: 20), "reply \(n) did not come")
        }
        sleep(2)
        // Up to the first message (the oldest end), and a little further: where the list stops.
        for _ in 0..<20 where !onScreen(first) { app.swipeDown(velocity: .fast) }
        app.swipeDown(velocity: .fast)
        sleep(2)
        XCTAssertTrue(onScreen(first), "the first message is not on screen at the top")
        XCTAssertTrue(rows().contains("\"arrow\":true"), "↓ is not up: the chat fits on one screen, nothing to read up to\n\(rows())")
        shot("top")
        let before = rows()
        back()
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label == %@", "Agents")).firstMatch.waitForExistence(timeout: 10), "the list")
        sleep(1)
        // Opened again as a fresh page, as a tap on its row does (an idle chat is folded in the list): by the link the
        // Live Activity opens it with.
        let parts = chat.id.split(separator: "/")
        if newHistory { // no chat open: only the token changes, on the pages and pushes from now on
            _ = try Data(contentsOf: URL(string: hub + "/mock/say?history=mock-1.r1")!)
        }
        if slowBack {
            _ = try Data(contentsOf: URL(string: hub + "/mock/delay?s=0.6")!)
            var r = URLRequest(url: URL(string: hub + "/mock/wire")!)
            r.httpMethod = "POST"
            r.httpBody = Data(#"{"rules":[{"type":"items","action":"delay","ms":900,"count":3}]}"#.utf8)
            _ = try? syncRequest(r)
        }
        if fromRow {
            let row = app.staticTexts.matching(NSPredicate(format: "label == %@", chat.title)).firstMatch
            // Folded under its tab's line (「cache · 2 个 Agent」) or a machine's "N more idle" line (「个空闲」) until opened.
            let fold = app.buttons.matching(NSPredicate(format: "label CONTAINS %@ OR label CONTAINS %@", "个空闲", "cache")).firstMatch
            let w = app.windows.firstMatch
            for _ in 0..<6 where !(row.exists && row.isHittable) {
                w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.75))
                    .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45)))
                sleep(1)
                if !(row.exists && row.isHittable), fold.exists, fold.isHittable { fold.tap(); sleep(1) }
            }
            shot("list")
            XCTAssertTrue(row.exists && row.isHittable, "the chat's row in the list")
            row.tap()
        } else {
            app.open(URL(string: "persona://open?machine=\(parts[0])&session=\(parts[1])")!)
        }
        sleep(3)
        shot("reentered")
        let after = rows()
        let probe = "left: \(before)\nback: \(after)"
        XCTAssertTrue(onScreen(first), "opened again, the chat does not show its first message\n\(probe)")
        XCTAssertGreaterThan(messagesOnScreen(), 0, "opened again, the chat shows nothing\n\(probe)")
        let shown = app.probe("chat-rows", as: Rows.self, page: "agents")
        XCTAssertGreaterThan(shown?.visible ?? 0, 0, "no line on screen\n\(probe)")
        XCTAssertGreaterThan(shown?.drawn ?? 0, 0, "no row drawn\n\(probe)")
        if blankLanding {
            XCTAssertGreaterThan(shown?.blank ?? 0, 0, "the landing was not taken for blank: nothing mended\n\(probe)")
        } else {
            XCTAssertEqual(shown?.blank ?? 0, 0, "a landing came out blank and was mended\n\(probe)")
        }
    }

    // MARK: Helpers

    private func syncRequest(_ r: URLRequest) throws -> Data {
        let done = DispatchSemaphore(value: 0)
        var out: Result<Data, Error> = .failure(URLError(.timedOut))
        URLSession.shared.dataTask(with: r) { d, _, e in
            out = e.map { .failure($0) } ?? .success(d ?? Data())
            done.signal()
        }.resume()
        _ = done.wait(timeout: .now() + 10)
        return try out.get()
    }

    /// `probe.chat-rows`: what the list has and shows.
    private struct Rows: Decodable {
        var lines: Int
        var visible: Int
        var drawn: Int?
        var blank: Int?
        var top: String?
        var offset: Int?
        var content: Int?
        var mode: String?
        var arrow: Bool?
        var gap: Int?
    }

    @MainActor private func rows() -> String {
        guard let v = app.probeEnvelope("chat-rows", page: "agents")?["v"],
              let data = try? JSONSerialization.data(withJSONObject: v, options: .sortedKeys) else { return "no probe" }
        return String(decoding: data, as: UTF8.self)
    }

    /// A message and its reply in the open chat.
    private func say(user: String, reply: String) throws {
        var c = URLComponents(string: hub + "/mock/say")!
        c.queryItems = [URLQueryItem(name: "user", value: user), URLQueryItem(name: "text", value: reply)]
        _ = try Data(contentsOf: c.url!)
    }

    @MainActor private func text(_ words: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
    }

    /// Drawn and inside the window, between the header and the composer.
    @MainActor private func onScreen(_ e: XCUIElement) -> Bool {
        guard e.exists else { return false }
        let f = e.frame, w = app.windows.firstMatch.frame
        return !f.isEmpty && f.maxY > w.minY + 120 && f.minY < w.maxY - 120
    }

    /// Texts drawn in the middle of the screen (the messages' area).
    @MainActor private func messagesOnScreen() -> Int {
        let w = app.windows.firstMatch.frame
        let band = CGRect(x: w.minX, y: w.minY + w.height * 0.25, width: w.width, height: w.height * 0.5)
        return app.staticTexts.allElementsBoundByIndex.prefix(80).filter { $0.frame.intersects(band) && !$0.label.isEmpty }.count
    }

    @MainActor private func back() {
        if let b = app.buttons.matching(NSPredicate(format: "label IN %@", ["返回", "Back"])).allElementsBoundByIndex
            .first(where: { $0.isHittable }) {
            b.tap()
        } else {
            let w = app.windows.firstMatch
            w.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5))
                .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.5)))
        }
        sleep(1)
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name)-\(self.name.contains("Long") ? "long" : "two")"
        a.lifetime = .keepAlways
        add(a)
    }
}
