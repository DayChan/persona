import XCTest

/// Inbox › Hand to AI opens the Persona conversation it went to on the message as sent, not on No messages yet until
/// claude has written its transcript; once it has, the message is there once (#511, #527). The default target is
/// Persona's main conversation, through the inbox hand-off (#1194): the message the hub put in its transcript carries the
/// hand-off's id and takes the bubble's place. Against scripts/mock/mock_hub.py with MOCK_ASSISTANT_TRANSCRIPT_SECONDS=8
/// (PERSONA_MOCK_HUB): the mock answers the start or hand-off request at once and writes the line that much later, as the
/// node does. A hand-off whose answer got lost keeps the sheet and its client id (`testAHandOffThatMayHaveGoneSaysToLookFirst`).
final class NewConversationEntryTests: XCTestCase {
    private static let mainConversation = "b2e4d6f8-0000-4000-8000-000000000001"

    @MainActor func testInboxHandOffOpensTheMainChatOnTheSentMessage() throws {
        try handOff(name: "main-shell", newChat: false)
    }

    @MainActor func testInboxHandOffOpensANewChatOnTheSentMessage() throws {
        try handOff(name: "shell", newChat: true)
    }

    /// The answer to a hand-off gets lost after the hub ran it (the mock's lost_receipt fault: 502, delivered unknown): the
    /// sheet stays and says to look in the chat first. Sending again goes under the same client id, so the mock answers it
    /// from the first and the chat gets the hand-off once; its answer lost too, the sheet adds how to send it anyway.
    @MainActor func testAHandOffThatMayHaveGoneSaysToLookFirst() throws {
        for scheme in ["light", "dark"] { try inDoubt(scheme: scheme) }
    }

    @MainActor private func inDoubt(scheme: String) throws {
        let hub = try mockHub()
        let app = open(hub, ["-ui.scheme", scheme])
        defer {
            app.terminate()
            fault(hub, on: false)
        }
        let ask = "Hand-off in doubt \(scheme) \(UUID().uuidString.prefix(8))"
        func said(_ words: String) -> XCUIElement {
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
        }
        // The error is the form's last row, below the targets; the drag stays clear of the keyboard.
        func shown(_ words: String) -> Bool {
            let form = app.collectionViews.firstMatch
            for _ in 0..<6 {
                if said(words).waitForExistence(timeout: 1), said(words).isHittable { return true }
                form.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35))
                    .press(forDuration: 0.05, thenDragTo: form.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.05)))
            }
            return said(words).exists
        }
        let send = app.navigationBars["交给 AI"].buttons["发送"].firstMatch // not the event page's bar under the sheet
        XCTAssertTrue(send.waitForExistence(timeout: 20), "no 交给 AI sheet:\n\(app.debugDescription)")
        let field = instructions(app)
        XCTAssertTrue(field.waitForExistence(timeout: 5), "no instructions field:\n\(app.debugDescription)")
        field.tap()
        field.typeText(ask)

        loseTheNextAnswer(hub)
        send.tap()
        XCTAssertTrue(shown("可能已经送到电脑上了"), "no look-first error:\n\(app.debugDescription)")
        XCTAssertFalse(said("仍要发送的话").exists, "the first lost answer already says how to send it anyway")
        shot(app, "handoff-in-doubt-\(scheme)")

        loseTheNextAnswer(hub)
        send.tap()
        XCTAssertTrue(shown("仍要发送的话"), "the second lost answer does not say how to send it anyway:\n\(app.debugDescription)")
        XCTAssertTrue(said("可能已经送到电脑上了").exists, "the second lost answer does not say to look first")
        shot(app, "handoff-in-doubt-again-\(scheme)")
        XCTAssertTrue(send.exists, "the sheet closed")

        let handed = sentTo(hub).filter { ($0["text"] as? String)?.contains(ask) == true }
        XCTAssertEqual(handed.count, 1, "the second send reached the chat again: \(handed)")
        XCTAssertNotNil(handed.first?["client_msg_id"] as? String, "the hand-off went without a client id")
    }

    @MainActor private func handOff(name: String, newChat: Bool) throws {
        let hub = try mockHub()
        let app = open(hub, [])
        func shot(_ name: String) { self.shot(app, name) }
        // Every case hands off its own words: the main conversation keeps the earlier cases' hand-offs (in the mock and
        // in the app's chat cache), which would count as a second copy.
        let ask = "Hand-off check \(name) \(UUID().uuidString.prefix(8))"
        func asked() -> XCUIElementQuery {
            app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", ask))
        }
        func row(_ label: String) -> XCUIElement {
            app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", label)).firstMatch
        }
        let handedBefore = sentTo(hub).count

        let send = app.navigationBars["交给 AI"].buttons["发送"].firstMatch // not the event page's bar under the sheet
        XCTAssertTrue(send.waitForExistence(timeout: 20), "no 交给 AI sheet:\n\(app.debugDescription)")
        XCTAssertTrue(row("Persona（主对话）").exists, "no main chat target:\n\(app.debugDescription)")
        sleep(1)
        shot("handoff-sheet-\(name)")
        let field = instructions(app)
        XCTAssertTrue(field.waitForExistence(timeout: 5), "no instructions field:\n\(app.debugDescription)")
        field.tap()
        field.typeText(ask)
        if newChat {
            row("Persona（新对话）").tap()
            sleep(1)
        }
        send.tap()
        // The conversation opens on the message right away (the transcript comes 8 s after the request).
        let bubble = asked().firstMatch
        let shown = bubble.waitForExistence(timeout: 4)
        shot("handoff-sent-\(name)")
        XCTAssertTrue(shown, "the conversation did not open on the sent message:\n\(app.debugDescription)")
        XCTAssertFalse(app.staticTexts["还没有消息"].exists, "the conversation showed 还没有消息")
        let handed = sentTo(hub)
        if newChat {
            XCTAssertEqual(handed.count, handedBefore, "a new chat went through the hand-off")
        } else {
            XCTAssertEqual(handed.count, handedBefore + 1, "the main chat target did not go through the hand-off")
            XCTAssertEqual(handed.last?["session_id"] as? String, Self.mainConversation, "the hand-off went elsewhere")
        }
        // Once claude has written it, the transcript's copy takes the bubble's place: once, not twice.
        sleep(10)
        shot("handoff-transcript-\(name)")
        XCTAssertTrue(bubble.exists, "the message went away")
        let hittable = asked().allElementsBoundByIndex.filter { $0.isHittable }
        XCTAssertEqual(hittable.count, 1, "the message shows \(hittable.count) times:\n\(app.debugDescription)")
    }

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        return hub
    }

    /// The app on the sample mail's Hand to AI sheet, past the system's icon alert (`-ui.scheme` can change the icon).
    @MainActor private func open(_ hub: String, _ more: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", "",
                               "-openInboxItem", "mail:acc1:t1:501", "-showInboxAction", "handoff", "-openChatsFresh", "YES"] + more
        app.launchPinned()
        acceptIconAlert()
        return app
    }

    @MainActor private func instructions(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(
            format: "elementType IN %@ AND placeholderValue == %@",
            [XCUIElement.ElementType.textField.rawValue, XCUIElement.ElementType.textView.rawValue], "总结一下，告诉我要做什么，给出建议的回复")).firstMatch
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: name + "-\(UITestStyle.name).png"))
        }
    }

    /// The mock runs the next successful API write and then answers 502, delivered unknown (`POST /mock/fault` lost_receipt).
    private func loseTheNextAnswer(_ hub: String) {
        fault(hub, on: true)
    }

    private func fault(_ hub: String, on: Bool) {
        var req = URLRequest(url: URL(string: hub + "/mock/fault")!)
        req.httpMethod = "POST"
        req.httpBody = Data(#"{"fault":"lost_receipt","enabled":\#(on)}"#.utf8)
        var status = 0
        let done = expectation(description: "fault")
        URLSession.shared.dataTask(with: req) { _, response, _ in
            status = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        XCTAssertEqual(status, 200, "the mock did not take the lost_receipt fault (enabled: \(on))")
    }

    /// Where the mock's hand-offs went (`/mock/sent_to`; the main chat's are the ones there with an inbox block).
    private func sentTo(_ hub: String) -> [[String: Any]] {
        guard let url = URL(string: hub + "/mock/sent_to") else { return [] }
        var answer: [[String: Any]] = []
        let done = expectation(description: "sent_to")
        URLSession.shared.dataTask(with: url) { data, _, _ in
            if let data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let sent = obj["sent"] as? [[String: Any]] {
                answer = sent.filter { ($0["text"] as? String)?.contains("收件箱事件") == true }
            }
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return answer
    }
}
