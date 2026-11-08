import Foundation
import Testing
@testable import Persona

@MainActor
struct FakeHubTests {
    @Test func actorOwnedRoutesAreSafeFromDetachedRequests() async throws {
        let hub = FakeHub()
        var calls = 0
        hub.on("GET", "/actor-owned") { _ in
            MainActor.assertIsolated()
            calls += 1
            // Route execution must be outside the table lock: handlers can
            // register the next response and inspect the recorded request.
            #expect(hub.sent("GET", "/actor-owned").count == 1)
            hub.on("GET", "/next", json: #"{"ok":true}"#)
            return Data(#"{"first":true}"#.utf8)
        }
        let client = hub.client
        let first = try await Task.detached {
            try await client.request("GET", "/actor-owned")
        }.value
        #expect(first == Data(#"{"first":true}"#.utf8))
        #expect(calls == 1)
        let next = try await Task.detached {
            try await client.request("GET", "/next")
        }.value
        #expect(next == Data(#"{"ok":true}"#.utf8))
        #expect(hub.sent("GET", "/next").count == 1)
    }

    @Test func backgroundTransportPreservesRouteErrors() async throws {
        let hub = FakeHub()
        hub.on("DELETE", "/fixture") { _ in
            MainActor.assertIsolated()
            throw APIError(status: 409, message: "fixture refusal")
        }
        let transport = try #require(hub.client.transport)
        let status = await Task.detached {
            do {
                _ = try await transport(LocalRequest(method: "DELETE", path: "/fixture", query: [:], body: nil))
                return 0
            } catch let error as APIError {
                return error.status
            } catch {
                return -1
            }
        }.value
        #expect(status == 409)
        #expect(hub.sent("DELETE", "/fixture").count == 1)
    }
}
