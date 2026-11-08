import Foundation
@testable import Persona

/// A hub in memory for unit tests: canned JSON by "METHOD path", every request recorded. Hand its `transport` to an
/// `APIClient` (`FakeHub.client`); a view model built with that client runs its real requests and decoding.
final class FakeHub: @unchecked Sendable {
    /// Model-test handlers capture actor-owned fixtures. Preserve that contract
    /// across the asynchronous, Sendable API transport boundary.
    typealias Route = @MainActor (LocalRequest) throws -> Data

    private let lock = NSLock()
    private var routes: [String: Route] = [:]
    private(set) var requests: [LocalRequest] = []

    /// Answers `method path` with `json` (an object or array literal, as the hub would send it).
    func on(_ method: String, _ path: String, json: String) {
        lock.withLock { routes["\(method) \(path)"] = { _ in Data(json.utf8) } }
    }

    /// Answers with what `make` returns for the request (to look at the body, or to fail).
    func on(_ method: String, _ path: String, _ make: @escaping Route) {
        lock.withLock { routes["\(method) \(path)"] = make }
    }

    var client: APIClient {
        var c = APIClient(config: ServerConfig(url: URL(string: "https://fake.invalid")!, token: "t"))
        c.transport = { [self] req in try await self.answer(req) }
        return c
    }

    func sent(_ method: String, _ path: String) -> [LocalRequest] {
        lock.withLock { requests.filter { $0.method == method && $0.path == path } }
    }

    @MainActor private func answer(_ req: LocalRequest) throws -> Data {
        let route = lock.withLock { () -> Route? in
            requests.append(req)
            return routes["\(req.method) \(req.path)"]
        }
        guard let route else { throw APIError(status: 404, message: "FakeHub: no route for \(req.method) \(req.path)") }
        return try route(req)
    }
}
