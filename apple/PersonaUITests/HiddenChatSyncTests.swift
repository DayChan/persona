import Darwin
import XCTest

/// A retained hidden chat waits for its visible subscription, while uncached recent chats may still warm on disk.
/// `/mock/incoming` writes a real synthetic fixture reply even when nobody subscribes to that conversation.
final class HiddenChatSyncTests: XCTestCase {
    private var hub: String!
    private var app: XCUIApplication?
    private let cliPort: UInt16 = 17392
    private let cliToken = "synthetic-hidden-sync-test"
    private let assistant = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private let agent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = value
        _ = try get("/mock/reset")
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let screen = XCTAttachment(screenshot: app.screenshot())
            screen.name = "hidden-sync-failure"
            screen.lifetime = .keepAlways
            add(screen)
            let tree = XCTAttachment(string: app.debugDescription)
            tree.name = "hidden-sync-failure-tree"
            tree.lifetime = .keepAlways
            add(tree)
        }
        app?.terminate()
        super.tearDown()
    }

    private func get(_ path: String, query: [String: String] = [:]) throws -> [String: Any] {
        var url = URLComponents(string: hub + path)!
        if !query.isEmpty { url.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) } }
        return try JSONSerialization.jsonObject(with: Data(contentsOf: url.url!)) as! [String: Any]
    }

    private func itemRequests(_ key: String) throws -> Int {
        let hits = try get("/mock/hits")["hits"] as? [String: Int] ?? [:]
        return hits.filter { $0.key.contains("/api/v1/sessions/\(key)/items") }.values.reduce(0, +)
    }

    private func itemFrames() throws -> Int {
        let clients = try get("/mock/ws_clients")["clients"] as? [[String: Any]] ?? []
        XCTAssertTrue(clients.contains { $0["connected"] as? Bool == true }, "the fixture transport disconnected")
        return clients.reduce(0) { $0 + ($1["item_frames"] as? Int ?? 0) }
    }

    @MainActor private func probe(_ app: XCUIApplication, key: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "identifier == 'chat.scrollState' AND label ENDSWITH %@", "key=" + key)).firstMatch
    }

    @MainActor private func count(_ element: XCUIElement) -> Int {
        Int(element.label.split(separator: " ").first { $0.hasPrefix("items=") }?.dropFirst(6) ?? "") ?? -1
    }

    @MainActor private func verifyHiddenChat(assistant isAssistant: Bool) throws {
        let key = isAssistant ? assistant : agent
        let app = XCUIApplication()
        self.app = app
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // Pair before choosing the initial page; clear any Agent stack restored by the preceding test.
        app.launchArguments = ["-pairURL", pair, "-openWelcome", "YES", "-demo.on", "NO", "-ui.oldLayout", "NO",
                               "-dev.openChat", "", "-tab", "assistant", "-newAssistant", "YES"]
        app.launchPinned()
        LiveActivityShots.allowNotifications()
        XCTAssertTrue(app.buttons["assistant-sidebar-button"].firstMatch.waitForExistence(timeout: 20))
        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-openWelcome", "NO", "-demo.on", "NO", "-ui.oldLayout", "NO",
                               "-openChatsFresh", "YES", "-showScrollState", "YES", "-dev.openChat", "",
                               "-runtimeCLI", String(cliPort), "-runtimeCLIToken", cliToken,
                               "-app.tab", isAssistant ? "assistant" : "chats", "-tab", isAssistant ? "assistant" : "dev"]
            + (isAssistant ? ["-openAssistant", key] : ["-openSession", key])
        app.launchPinned()
        LiveActivityShots.allowNotifications()
        let transcript = probe(app, key: key)
        XCTAssertTrue(transcript.waitForExistence(timeout: 20), "the intended retained chat did not open")
        let loaded = expectation(for: NSPredicate(format: "label MATCHES %@", ".*items=[1-9][0-9]* .*"), evaluatedWith: transcript)
        XCTAssertEqual(XCTWaiter.wait(for: [loaded], timeout: 20), .completed)
        sleep(2) // Setup, not a performance timing sample.
        let before = count(transcript)
        if isAssistant {
            app.buttons["shell-agents"].firstMatch.tap()
        } else {
            // A pushed Agent has no top switch. Use the existing in-place app route, not a rebuild or a fake button.
            // This retains the model's ChatStore, not the pushed navigation view.
            _ = try route(["scene": "home", "keep": true])
        }
        sleep(2)
        let requests = try itemRequests(key)
        let frames = try itemFrames()
        let parts = key.split(separator: "/").map(String.init)
        let text = isAssistant ? "retained-assistant-replay-1159" : "retained-agent-replay-1159"
        let written = try get("/mock/incoming", query: ["mid": parts[0], "sid": parts[1], "text": text])
        XCTAssertNotNil(written["item"], "the fixture did not write the hidden reply")
        sleep(6) // Includes the revision debounce and the visible-stall quiet window.
        XCTAssertEqual(try itemRequests(key), requests, "a retained hidden chat fetched transcript items")
        XCTAssertEqual(try itemFrames(), frames, "the hidden chat still received a transcript batch")
        if transcript.exists {
            XCTAssertEqual(count(transcript), before, "a retained hidden store applied the reply before visibility returned")
        }
        if isAssistant {
            app.buttons["shell-assistant"].firstMatch.tap()
        } else {
            _ = try route(["scene": "chat", "session": key, "keep": true])
        }
        let replayed = expectation(for: NSPredicate(format: "label CONTAINS %@", "items=\(before + 1) "), evaluatedWith: transcript)
        XCTAssertEqual(XCTWaiter.wait(for: [replayed], timeout: 20), .completed, "the visible subscription did not replay the hidden reply")
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = isAssistant ? "assistant-hidden-replay" : "agent-hidden-replay"
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    /// The existing runtime CLI transport, bounded like ImageViewerTests; no new app-side test hook.
    private func route(_ args: [String: Any]) throws -> [String: Any] {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { throw NSError(domain: "HiddenSyncRoute", code: 1) }
        defer { Darwin.close(fd) }
        var timeout = timeval(tv_sec: 5, tv_usec: 0)
        _ = setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
        _ = setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
        var noPipe: Int32 = 1
        _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noPipe, socklen_t(MemoryLayout<Int32>.size))
        var address = sockaddr_in()
        address.sin_family = sa_family_t(AF_INET)
        address.sin_port = in_port_t(cliPort.bigEndian)
        address.sin_addr.s_addr = inet_addr("127.0.0.1")
        let connected = withUnsafePointer(to: &address) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        guard connected == 0 else { throw NSError(domain: "HiddenSyncRoute", code: 2) }
        var request = try JSONSerialization.data(withJSONObject: ["token": cliToken, "cmd": "navigate", "args": args])
        request.append(0x0A)
        let sent = request.withUnsafeBytes { send(fd, $0.baseAddress, request.count, 0) }
        guard sent == request.count else { throw NSError(domain: "HiddenSyncRoute", code: 3) }
        var reply = Data(), buffer = [UInt8](repeating: 0, count: 65536)
        while reply.last != 0x0A {
            let n = recv(fd, &buffer, buffer.count, 0)
            guard n > 0, reply.count < 4 << 20 else { throw NSError(domain: "HiddenSyncRoute", code: 4) }
            reply.append(contentsOf: buffer.prefix(n))
        }
        let result = try XCTUnwrap(try JSONSerialization.jsonObject(with: reply) as? [String: Any])
        XCTAssertEqual(result["ok"] as? Bool, true, "\(result)")
        return result
    }

    @MainActor func testAssistantTranscriptWaitsUntilItsTabReturns() throws {
        try verifyHiddenChat(assistant: true)
    }

    @MainActor func testAgentTranscriptWaitsUntilItsTabReturns() throws {
        try verifyHiddenChat(assistant: false)
    }
}
