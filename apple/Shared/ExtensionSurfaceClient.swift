import Foundation

nonisolated struct ExtensionSurfaceProjection: Sendable {
    var key: ExtensionSurfaceKey
    var family: String
    var cache: ExtensionSurfaceCache?
}

nonisolated extension ExtensionSurfaceSnapshot {
    /// A watch deep link is a widget projection, not permission to render a full extension page.
    func projection(_ link: ExtensionSystemLink) -> ExtensionSurfaceProjection? {
        guard let group = groups[link.group],
              let place = group.widgets.sorted(by: { $0.id < $1.id }).first(where: {
                  $0.extension == link.owner && (link.ui == nil || $0.opens == link.ui)
              }), let family = ["large", "medium", "small", "accessory_rectangular", "accessory_circular", "accessory_inline"]
                .first(where: { place.families.contains($0) }) else { return nil }
        let key = ExtensionSurfaceKey(group: link.group, owner: place.extension, contribution: place.id)
        return ExtensionSurfaceProjection(key: key, family: family, cache: cache(key, family: family))
    }
}

nonisolated struct ExtensionSurfaceCapabilitiesUnknown: LocalizedError {
    var errorDescription: String? { String(localized: "Open Persona on iPhone to sync extension capabilities.") }
}

nonisolated struct ExtensionSurfaceHTTPError: Error, Sendable {
    var status: Int
}

private nonisolated struct ExtensionSurfaceCapabilities: Decodable {
    var features: [String]?
}

private nonisolated struct ExtensionSurfaceHello: Decodable {
    var type: String
    var features: [String]
}

private nonisolated struct ExtensionSurfaceAcknowledgement: Decodable {}

/// System processes use only direct, system-trusted TLS (or the user's configured LAN URL), never app-only tunnels.
/// Redirects are refused so the shared device token cannot be forwarded to an unexpected host.
nonisolated final class ExtensionSurfaceSession: NSObject, URLSessionTaskDelegate, Sendable {
    static let delegate = ExtensionSurfaceSession()
    static let session: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 15
        config.timeoutIntervalForResource = 20
        return URLSession(configuration: config, delegate: delegate, delegateQueue: nil)
    }()
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

nonisolated struct ExtensionSurfaceClient: Sendable {
    typealias Transport = @Sendable (URLRequest) async throws -> (Data, HTTPURLResponse)
    typealias HelloTransport = @Sendable (URLRequest) async throws -> Set<String>
    var credential: ExtensionSurfaceCredential
    var transport: Transport? = nil
    var helloTransport: HelloTransport? = nil
    var clientMetadata: RequestClientMetadata = .current

    func request<T: Decodable>(_ path: String, body: (any Encodable & Sendable)? = nil, method: String? = nil) async throws -> T {
        guard ["https", "http"].contains(credential.url.scheme?.lowercased() ?? ""),
              credential.url.user == nil, credential.url.password == nil,
              credential.url.host != "demo.invalid", !credential.token.isEmpty,
              var components = URLComponents(url: credential.url, resolvingAgainstBaseURL: false) else { throw URLError(.badURL) }
        components.path = components.path.trimmingCharacters(in: CharacterSet(charactersIn: "/")) + "/" + path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if !components.path.hasPrefix("/") { components.path = "/" + components.path }
        components.queryItems = nil; components.fragment = nil
        guard let url = components.url else { throw URLError(.badURL) }
        var request = URLRequest(url: url)
        request.httpMethod = method ?? (body == nil ? "GET" : "POST")
        request.setValue("Bearer \(credential.token)", forHTTPHeaderField: "Authorization")
        clientMetadata.apply(to: &request)
        request.setValue(ExtensionUIContext.appLocale, forHTTPHeaderField: "Accept-Language")
        if let body {
            request.httpBody = try JSONEncoder().encode(body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let data: Data
        let response: URLResponse
        if let transport {
            let result = try await transport(request)
            data = result.0; response = result.1
        } else {
            let result = try await ExtensionSurfaceSession.session.data(for: request)
            data = result.0; response = result.1
        }
        guard let response = response as? HTTPURLResponse, (200..<300).contains(response.statusCode) else {
            throw ExtensionSurfaceHTTPError(status: (response as? HTTPURLResponse)?.statusCode ?? 0)
        }
        guard data.count <= 1_048_576 else { throw URLError(.dataLengthExceedsMaximum) }
        return try JSONDecoder().decode(T.self, from: data)
    }

    @discardableResult
    func refreshCatalog(group: String, expected: ExtensionSurfaceGroup, store: ExtensionSurfaceStore = .shared) async throws -> Set<String> {
        do {
            let capabilities: ExtensionSurfaceCapabilities = try await request("/api/v1/health")
            // Public health intentionally omits features, even with Authorization. That is unknown, not an old hub.
            let features: Set<String>
            if let advertised = capabilities.features { features = Set(advertised) }
            else if credential.isAnswerOnly {
                guard let forwarded = credential.capabilities else { throw ExtensionSurfaceCapabilitiesUnknown() }
                features = Set(forwarded)
            } else { features = try await helloFeatures() }
            async let widgets = widgetCatalog(enabled: features.contains("extension_widgets"))
            async let shortcuts = shortcutCatalog(enabled: features.contains("extension_shortcuts"))
            let (w, s) = try await (widgets, shortcuts)
            try store.catalog(group: group, expected: expected, widgets: w.widgets, shortcuts: s.shortcuts)
            return features
        } catch {
            if let error = error as? ExtensionSurfaceHTTPError, [401, 403].contains(error.status) {
                try? store.revoke(group, generation: expected.generation)
            }
            throw error
        }
    }

    func reportWidgets(group: String, expected: ExtensionSurfaceGroup, configurations: [(ExtensionSurfaceKey, String)],
                       store: ExtensionSurfaceStore = .shared) async throws {
        let snapshot = store.read()
        guard let current = snapshot.groups[group], current.generation == expected.generation,
              current.credential == credential, let device = current.credential.deviceID,
              device.range(of: "^[A-Za-z0-9_-]+$", options: .regularExpression) != nil else { return }
        let report = ExtensionDeviceWidgets(widgets: snapshot.reports(configurations)[group] ?? [])
        do {
            let _: ExtensionSurfaceAcknowledgement = try await request("/api/v1/devices/\(device)/widgets", body: report, method: "PUT")
        } catch {
            if let error = error as? ExtensionSurfaceHTTPError, [401, 403].contains(error.status) {
                try? store.revoke(group, generation: expected.generation)
            }
            throw error
        }
    }

    private func helloFeatures() async throws -> Set<String> {
        guard var url = URLComponents(url: credential.url, resolvingAgainstBaseURL: false) else { throw URLError(.badURL) }
        url.scheme = credential.url.scheme?.lowercased() == "https" ? "wss" : "ws"
        url.path = "/" + url.path.trimmingCharacters(in: CharacterSet(charactersIn: "/")) + "/api/v1/ws"
        if url.path.hasPrefix("//") { url.path.removeFirst() }
        url.queryItems = nil; url.fragment = nil
        guard let endpoint = url.url else { throw URLError(.badURL) }
        var request = URLRequest(url: endpoint, timeoutInterval: 15)
        request.setValue("Bearer \(credential.token)", forHTTPHeaderField: "Authorization")
        clientMetadata.apply(to: &request)
        if let helloTransport { return try await helloTransport(request) }
        let socket = ExtensionSurfaceSession.session.webSocketTask(with: request)
        socket.resume()
        let deadline = ContinuousClock.now.advanced(by: .seconds(10))
        let timeout = Task {
            try? await Task.sleep(for: .seconds(10))
            if !Task.isCancelled { socket.cancel(with: .goingAway, reason: nil) }
        }
        defer { timeout.cancel(); socket.cancel(with: .goingAway, reason: nil) }
        do {
            let message = try await withTaskCancellationHandler {
                try await socket.receive()
            } onCancel: {
                socket.cancel(with: .goingAway, reason: nil)
            }
            let data: Data
            switch message {
            case .data(let bytes): data = bytes
            case .string(let string): data = Data(string.utf8)
            @unknown default: throw URLError(.cannotParseResponse)
            }
            guard data.count <= 1_048_576 else { throw URLError(.dataLengthExceedsMaximum) }
            let hello = try JSONDecoder().decode(ExtensionSurfaceHello.self, from: data)
            guard hello.type == "hello" else { throw URLError(.cannotParseResponse) }
            return Set(hello.features)
        } catch {
            if let response = socket.response as? HTTPURLResponse, [401, 403].contains(response.statusCode) {
                throw ExtensionSurfaceHTTPError(status: response.statusCode)
            }
            if ContinuousClock.now >= deadline { throw URLError(.timedOut) }
            throw error
        }
    }

    private func widgetCatalog(enabled: Bool) async throws -> ExtensionWidgetPlaces {
        guard enabled else { return ExtensionWidgetPlaces(widgets: []) }
        return try await request("/api/v1/extensions/-/widgets")
    }

    private func shortcutCatalog(enabled: Bool) async throws -> ExtensionShortcutPlaces {
        guard enabled else { return ExtensionShortcutPlaces(shortcuts: []) }
        return try await request("/api/v1/extensions/-/shortcuts")
    }

    func widget(_ key: ExtensionSurfaceKey, family: String, store: ExtensionSurfaceStore = .shared) async -> ExtensionSurfaceCache? {
        let snapshot = store.read()
        guard let group = snapshot.groups[key.group], group.credential == credential,
              let place = group.widgets.first(where: { $0.extension == key.owner && $0.id == key.contribution }),
              place.families.contains(family), ExtensionWidgetFamily.all.contains(family),
              ExtensionSystemLink(owner: key.owner, ui: key.contribution, group: key.group).url != nil else { return nil }
        if credential.isAnswerOnly {
            // Unknown capabilities retain offline content; the wrist never probes a forbidden WebSocket.
            guard let capabilities = credential.capabilities else { return snapshot.cache(key, family: family) }
            guard capabilities.contains("extension_widgets") else { return nil }
        }
        if let cache = snapshot.cache(key, family: family), cache.fresh(at: .now) { return cache }
        do {
            let answer: ExtensionWidgetAnswer = try await request("/api/v1/extensions/\(key.owner)/-/widget/\(key.contribution)", body: ExtensionWidgetRequest(context: .here(size: family)))
            let cache = ExtensionSurfaceCache(key: key, family: family, locale: ExtensionUIContext.appLocale,
                                              timeZone: TimeZone.current.identifier, answer: answer)
            try store.save(cache, expected: group)
            // Read after commit: revocation/invalidation while the call was running cannot publish a stale result.
            return store.read().cache(key, family: family)
        } catch {
            if let error = error as? ExtensionSurfaceHTTPError {
                if [401, 403].contains(error.status) { try? store.revoke(key.group, generation: group.generation); return nil }
                if error.status == 404 {
                    try? store.catalog(group: key.group, expected: group,
                                       widgets: group.widgets.filter { $0.extension != key.owner || $0.id != key.contribution }, shortcuts: group.shortcuts)
                    return nil
                }
            }
            return store.read().cache(key, family: family)
        }
    }
}
