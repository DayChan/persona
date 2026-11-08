import Foundation
import Testing
@testable import Persona

@MainActor struct ExtensionSystemAPITests {
    @Test func widgetRequestsUseTheFamilyAndNewPath() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions/-/widgets", json: #"{"widgets":[{"extension":"sample","id":"today","title":"今天","families":["small"],"refresh_minutes":30}]}"#)
        hub.on("POST", "/api/v1/extensions/sample/-/widget/today", json: #"{"view":{"sections":[{"items":[{"type":"metric","title":"用量","value":"12"}]}]},"expires_at":"2040-01-01T00:00:00Z"}"#)
        let place = try #require(try await hub.client.extensionWidgets().first)
        let answer = try await hub.client.extensionWidget(place, family: "small")
        #expect(answer.expiration != nil && answer.view.sections.count == 1)
        let data = try #require(hub.sent("POST", "/api/v1/extensions/sample/-/widget/today").first?.body)
        let body = try JSON.decoder.decode(JSONValue.self, from: data)
        #expect(body["context"]?["size"] == .string("small"))
        #expect(body["context"]?["ui_version"] == .number(3))
        #expect(body["context"]?["platform"] == .string("ios"))
        do {
            _ = try await hub.client.extensionWidget(place, family: "large")
            Issue.record("未支持的尺寸不应发请求")
        } catch { #expect((error as? APIError)?.status == 400) }
        #expect(hub.sent("POST", "/api/v1/extensions/sample/-/widget/today").count == 1)
    }

    @Test func shortcutsAndReportsReuseGeneratedWireTypes() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions/-/shortcuts", json: #"{"shortcuts":[{"extension":"sample","id":"budget","title":"设预算"}]}"#)
        hub.on("POST", "/api/v1/extensions/sample/-/shortcut/budget", json: #"{"text":"已设预算"}"#)
        hub.on("PUT", "/api/v1/devices/device/widgets", json: #"{"widgets":[]}"#)
        let shortcut = try #require(try await hub.client.extensionShortcuts().first)
        let answer = try await hub.client.runExtensionShortcut(shortcut, values: ["amount": "100", "enabled": "true"])
        #expect(answer.text == "已设预算")
        let request = try #require(hub.sent("POST", "/api/v1/extensions/sample/-/shortcut/budget").first)
        let body = try JSON.decoder.decode(JSONValue.self, from: try #require(request.body))
        #expect(body["values"] == .object(["amount": .string("100"), "enabled": .string("true")]))
        try await hub.client.reportExtensionWidgets(device: "device", widgets: [])
        let report = try #require(hub.sent("PUT", "/api/v1/devices/device/widgets").first?.body)
        #expect(try JSON.decoder.decode(ExtensionDeviceWidgets.self, from: report).widgets.isEmpty)
        #expect(!hub.requests.contains { $0.path.contains("/plugins/") })
    }

    private func withStore(_ body: (ExtensionSurfaceStore) throws -> Void) throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        try body(ExtensionSurfaceStore(root: root))
    }
    private var credential: ExtensionSurfaceCredential {
        ExtensionSurfaceCredential(url: URL(string: "https://hub.example.com")!, token: "fixture-device-token", deviceID: "device", name: "Example")
    }
    private var widget: ExtensionWidgetPlace {
        ExtensionWidgetPlace(extension: "sample", id: "today", title: "Today", families: ["small", "medium"], refreshMinutes: 30, opens: "usage")
    }
    private func seed(_ store: ExtensionSurfaceStore, group: String = "") throws -> ExtensionSurfaceGroup {
        try store.replaceCredentials([group: credential])
        let state = try #require(store.read().groups[group])
        try store.catalog(group: group, expected: state, widgets: [widget], shortcuts: [])
        return try #require(store.read().groups[group])
    }
    private func cache(group: String = "", family: String = "small") -> ExtensionSurfaceCache {
        ExtensionSurfaceCache(key: ExtensionSurfaceKey(group: group, owner: "sample", contribution: "today"), family: family,
                              locale: "en-US", timeZone: "UTC", answer: ExtensionWidgetAnswer(expiresAt: "2040-01-01T00:00:00Z"))
    }

    @Test func expiryOfflineAndContextIsolation() throws {
        try withStore { store in
            let state = try seed(store)
            let cached = cache()
            let expiry = try #require(cached.answer.expiration)
            #expect(cached.fresh(at: expiry.addingTimeInterval(-1)))
            #expect(!cached.fresh(at: expiry))
            #expect(ExtensionWidgetAnswer(expiresAt: "invalid").expiration == nil)
            try store.save(cached, expected: state)
            // Offline does not erase the content; stale is a display state, not another network write.
            let snapshot = store.read()
            #expect(snapshot.cache(cached.key, family: "small", locale: "en-US", timeZone: "UTC")?.answer == cached.answer)
            #expect(snapshot.cache(cached.key, family: "medium", locale: "en-US", timeZone: "UTC") == nil)
            #expect(snapshot.cache(cached.key, family: "small", locale: "ja-JP", timeZone: "UTC") == nil)
            #expect(snapshot.cache(cached.key, family: "small", locale: "en-US", timeZone: "Asia/Tokyo") == nil)
            let file = try #require(store.root).appendingPathComponent("snapshot.json")
            let attributes = try FileManager.default.attributesOfItem(atPath: file.path)
            #expect((attributes[.posixPermissions] as? NSNumber)?.intValue == 0o600)
        }
    }

    @Test func revocationAndRotationFenceLateReplies() throws {
        try withStore { store in
            let old = try seed(store)
            try store.save(cache(), expected: old)
            try store.revoke("")
            #expect(store.read().groups.isEmpty)
            try store.save(cache(), expected: old)
            try store.catalog(group: "", expected: old, widgets: [widget], shortcuts: [])
            #expect(store.read().groups.isEmpty)
            let repaired = try seed(store)
            #expect(repaired.generation != old.generation)
            try store.save(cache(), expected: old)
            try store.revoke("", generation: old.generation)
            #expect(store.read().groups[""]?.generation == repaired.generation)
            #expect(store.read().groups[""]?.cache.isEmpty == true)
            var rotated = credential; rotated.token = "fixture-rotated-token"
            try store.replaceCredentials(["": rotated])
            #expect(store.read().groups[""]?.credential == rotated)
            #expect(store.read().groups[""]?.widgets.isEmpty == true)
            try store.replaceCredentials([:])
            #expect(store.read().groups.isEmpty)
        }
    }

    @Test func invalidationFencesRenderingAndEmptyCatalogRemovesContent() throws {
        try withStore { store in
            let state = try seed(store)
            try store.save(cache(), expected: state)
            try store.invalidate(group: "", owner: "sample", contribution: "today")
            #expect(store.read().groups[""]?.cache.first?.invalidated == true)
            #expect(store.read().groups[""]?.cache.first?.fresh(at: .distantPast) == false)
            try store.save(cache(), expected: state)
            #expect(store.read().groups[""]?.cache.first?.invalidated == true)
            try store.catalog(group: "", expected: state, widgets: [], shortcuts: [])
            #expect(store.read().groups[""]?.cache.isEmpty == true)
        }
    }

    @Test func malformedProtectedSnapshotIsNotReplacedAsEmpty() throws {
        try withStore { store in
            _ = try seed(store)
            let file = try #require(store.root).appendingPathComponent("snapshot.json")
            try Data("not-json".utf8).write(to: file)
            #expect(throws: (any Error).self) { try store.replaceCredentials([:]) }
            #expect(try Data(contentsOf: file) == Data("not-json".utf8))
        }
    }

    private func withAsyncStore(_ body: (ExtensionSurfaceStore) async throws -> Void) async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        try await body(ExtensionSurfaceStore(root: root))
    }

    @Test func offlineClientReturnsOnlyItsExpiredContextCache() async throws {
        try await withAsyncStore { store in
            let state = try seed(store)
            var expired = cache()
            expired.locale = ExtensionUIContext.appLocale
            expired.timeZone = TimeZone.current.identifier
            expired.answer.expiresAt = "2000-01-01T00:00:00Z"
            try store.save(expired, expected: state)
            let client = ExtensionSurfaceClient(credential: credential, transport: { _ in throw URLError(.notConnectedToInternet) })
            let result = await client.widget(expired.key, family: "small", store: store)
            #expect(result?.answer == expired.answer)
            #expect(result?.fresh(at: .now) == false)
            #expect(await client.widget(expired.key, family: "medium", store: store) == nil)
            let other = ExtensionSurfaceKey(group: "other", owner: "sample", contribution: "today")
            #expect(await client.widget(other, family: "small", store: store) == nil)
        }
    }

    @Test func authenticatedClientFailureClearsCredentialsAndContent() async throws {
        try await withAsyncStore { store in
            let state = try seed(store)
            var expired = cache()
            expired.locale = ExtensionUIContext.appLocale; expired.timeZone = TimeZone.current.identifier
            expired.answer.expiresAt = "2000-01-01T00:00:00Z"
            try store.save(expired, expected: state)
            let client = ExtensionSurfaceClient(credential: credential, transport: { request in
                (Data(), HTTPURLResponse(url: request.url!, statusCode: 401, httpVersion: nil, headerFields: nil)!)
            })
            #expect(await client.widget(expired.key, family: "small", store: store) == nil)
            #expect(store.read().groups.isEmpty)
        }
    }

    @Test func renderingAfterRevocationCannotRestoreTheCache() async throws {
        try await withAsyncStore { store in
            _ = try seed(store)
            let client = ExtensionSurfaceClient(credential: credential, transport: { request in
                try store.revoke("")
                return (Data(#"{"view":{"sections":[{"items":[{"type":"metric","label":"Usage","value":"12"}]}]},"expires_at":"2040-01-01T00:00:00Z"}"#.utf8),
                        HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!)
            })
            let key = ExtensionSurfaceKey(group: "", owner: "sample", contribution: "today")
            #expect(await client.widget(key, family: "small", store: store) == nil)
            #expect(store.read().groups.isEmpty)
        }
    }

    @Test func systemEntityIDsAreCanonicalAcrossProcesses() {
        let key = ExtensionSurfaceKey(group: "", owner: "sample", contribution: "today")
        #expect(key.id == #"{"contribution":"today","group":"","owner":"sample"}"#)
        #expect(ExtensionSurfaceKey(id: key.id)?.id == key.id)
    }

    @Test func anOlderHubOnlyReceivesTheCapabilitiesProbe() async throws {
        try await withAsyncStore { store in
            let state = try seed(store)
            try store.save(cache(), expected: state)
            let client = ExtensionSurfaceClient(credential: credential, transport: { request in
                #expect(request.url?.path == "/api/v1/health", "unadvertised system surfaces must not be queried")
                return (Data(#"{"features":[]}"#.utf8), HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!)
            })
            let features = try await client.refreshCatalog(group: "", expected: state, store: store)
            #expect(features.isEmpty)
            #expect(store.read().groups[""]?.widgets.isEmpty == true)
            #expect(store.read().groups[""]?.cache.isEmpty == true)
            #expect(store.read().groups[""]?.credential == credential)
        }
    }

    @Test func redactedPublicHealthUsesTheAuthenticatedHello() async throws {
        try await withAsyncStore { store in
            let state = try seed(store)
            let client = ExtensionSurfaceClient(credential: credential, transport: { request in
                let data: Data
                switch request.url?.path {
                case "/api/v1/health": data = Data(#"{"ok":true}"#.utf8)
                case "/api/v1/extensions/-/widgets": data = Data(#"{"widgets":[{"extension":"sample","id":"today","title":"Today","families":["small"],"refresh_minutes":30}]}"#.utf8)
                default:
                    Issue.record("An unadvertised catalog was queried")
                    throw URLError(.unsupportedURL)
                }
                return (data, HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!)
            }, helloTransport: { request in
                #expect(request.url?.scheme == "wss" && request.url?.path == "/api/v1/ws")
                #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer fixture-device-token")
                return ["extension_widgets"]
            })
            let features = try await client.refreshCatalog(group: "", expected: state, store: store)
            #expect(features == ["extension_widgets"])
            #expect(store.read().groups[""]?.widgets.map(\.id) == ["today"])
        }
    }

    @Test func systemProcessReportsTheLastRemovalAsAnEmptyReplacement() async throws {
        try await withAsyncStore { store in
            let state = try seed(store)
            let client = ExtensionSurfaceClient(credential: credential, transport: { request in
                #expect(request.httpMethod == "PUT" && request.url?.path == "/api/v1/devices/device/widgets")
                let data = try #require(request.httpBody)
                #expect(try JSONDecoder().decode(ExtensionDeviceWidgets.self, from: data).widgets.isEmpty)
                return (Data(#"{"ok":true}"#.utf8), HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!)
            })
            try await client.reportWidgets(group: "", expected: state, configurations: [], store: store)
        }
    }

    @Test func eachSystemCatalogRequiresItsOwnAdvertisedFeature() async throws {
        try await withAsyncStore { store in
            let state = try seed(store)
            let client = ExtensionSurfaceClient(credential: credential, transport: { request in
                let data: Data
                switch request.url?.path {
                case "/api/v1/health": data = Data(#"{"features":["extension_shortcuts"]}"#.utf8)
                case "/api/v1/extensions/-/shortcuts": data = Data(#"{"shortcuts":[{"extension":"sample","id":"echo","title":"Echo"}]}"#.utf8)
                default:
                    Issue.record("The unadvertised widget catalog was queried")
                    throw URLError(.unsupportedURL)
                }
                return (data, HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!)
            })
            let features = try await client.refreshCatalog(group: "", expected: state, store: store)
            #expect(features == ["extension_shortcuts"])
            #expect(store.read().groups[""]?.widgets.isEmpty == true)
            #expect(store.read().groups[""]?.shortcuts.map(\.id) == ["echo"])
        }
    }

    @Test func groupIdentityAndWholeConfigurationReports() throws {
        let key = ExtensionSurfaceKey(group: "g|other/quoted", owner: "sample", contribution: "today")
        #expect(ExtensionSurfaceKey(id: key.id) == key)
        #expect(ExtensionSurfaceKey(id: "sample/today") == nil)
        let other = ExtensionSurfaceKey(group: "g2", owner: key.owner, contribution: key.contribution)
        #expect(other.id != key.id)
        let link = ExtensionSystemLink(owner: key.owner, ui: "usage", group: key.group, state: "period=day")
        #expect(ExtensionSystemLink(url: try #require(link.url)) == link)
        try withStore { store in
            try store.replaceCredentials(["": credential, "g2": credential])
            for group in ["", "g2"] {
                let state = try #require(store.read().groups[group])
                try store.catalog(group: group, expected: state, widgets: [widget], shortcuts: [])
            }
            let snapshot = store.read()
            #expect(snapshot.reports([]) == ["": [], "g2": []])
            let report = snapshot.reports([(other, "small"), (other, "small"), (other, "large"), (key, "small")])
            #expect(report[""] == [])
            #expect(report["g2"] == [ExtensionDeviceWidget(extension: "sample", id: "today", family: "small")])
        }
    }

    @Test func staticParametersAndJSONEscapeHatchMapDeclaredNamesOnly() throws {
        let place = ExtensionShortcutPlace(params: [
            ExtensionShortcutParam(name: "note", title: "Note", kind: "text"),
            ExtensionShortcutParam(name: "amount", title: "Amount", kind: "number", required: true),
            ExtensionShortcutParam(name: "extra", title: "Extra", kind: "number"),
            ExtensionShortcutParam(name: "enabled", title: "Enabled", kind: "toggle"),
            ExtensionShortcutParam(name: "period", title: "Period", kind: "choice", choices: [ExtensionChoice(value: "day", title: "Daily")])
        ])
        let mapped = try ExtensionActionValues.map(place, text: "note", number: 12, toggle: false, choice: "day", json: #"{"extra":"3","amount":"20"}"#)
        #expect(mapped == ["note": "note", "amount": "20", "extra": "3", "enabled": "false", "period": "day"])
        for json in [#"{"unknown":"1"}"#, #"{"amount":"nan"}"#, #"{"amount":"1","enabled":"yes"}"#, #"{"amount":"1","period":"year"}"#, #"{"amount":1}"#, "{}"] {
            #expect(throws: ExtensionActionInputError.self) {
                _ = try ExtensionActionValues.map(place, text: nil, number: nil, toggle: nil, choice: nil, json: json)
            }
        }
    }

    @Test func publicWatchCapabilitiesNeverOpenAForbiddenSocket() async throws {
        try await withAsyncStore { store in
            var watch = credential
            watch.answerOnly = true; watch.capabilities = ["extension_widgets", "extension_shortcuts"]
            try store.replaceCredentials(["": watch])
            let state = try #require(store.read().groups[""])
            let client = ExtensionSurfaceClient(credential: watch, transport: { request in
                #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer fixture-device-token")
                let data: Data
                switch (request.httpMethod, request.url?.path) {
                case ("GET", "/api/v1/health"): data = Data(#"{"ok":true}"#.utf8)
                case ("GET", "/api/v1/extensions/-/widgets"):
                    data = Data(#"{"widgets":[{"extension":"sample","id":"today","title":"Today","families":["small"],"refresh_minutes":30,"opens":"usage"}]}"#.utf8)
                case ("GET", "/api/v1/extensions/-/shortcuts"):
                    data = Data(#"{"shortcuts":[{"extension":"sample","id":"echo","title":"Echo"}]}"#.utf8)
                case ("POST", "/api/v1/extensions/sample/-/widget/today"):
                    data = Data(#"{"view":{"sections":[]},"expires_at":"2040-01-01T00:00:00Z"}"#.utf8)
                case ("POST", "/api/v1/extensions/sample/-/shortcut/echo"):
                    data = Data(#"{"text":"Done"}"#.utf8)
                default:
                    Issue.record("The watch requested an endpoint outside its answer-only permissions")
                    return (Data(), HTTPURLResponse(url: request.url!, statusCode: 403, httpVersion: nil, headerFields: nil)!)
                }
                return (data, HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!)
            }, helloTransport: { _ in
                Issue.record("The watch must never request /api/v1/ws")
                throw ExtensionSurfaceHTTPError(status: 403)
            })
            #expect(try await client.refreshCatalog(group: "", expected: state, store: store) == Set(watch.capabilities!))
            let link = ExtensionSystemLink(owner: "sample", ui: "usage", group: "")
            let projection = try #require(store.read().projection(link))
            #expect(projection.key.contribution == "today" && projection.family == "small")
            #expect(await client.widget(projection.key, family: projection.family, store: store) != nil)
            let action: ExtensionShortcutResult = try await client.request("/api/v1/extensions/sample/-/shortcut/echo",
                body: ExtensionShortcutRequest(values: [:], context: .here()))
            #expect(action.text == "Done")
            #expect(store.read().groups[""]?.credential == watch)
        }
    }

    @Test func unknownWatchCapabilitiesKeepTheOldProjectionAndCredential() async throws {
        try await withAsyncStore { store in
            let old = try seed(store)
            var cached = cache()
            cached.locale = ExtensionUIContext.appLocale; cached.timeZone = TimeZone.current.identifier
            cached.answer.expiresAt = "2000-01-01T00:00:00Z"
            try store.save(cached, expected: old)
            var watch = credential; watch.answerOnly = true
            try store.replaceCredentials(["": watch])
            let state = try #require(store.read().groups[""])
            #expect(state.generation == old.generation)
            let client = ExtensionSurfaceClient(credential: watch, transport: { request in
                #expect(request.url?.path == "/api/v1/health")
                return (Data(#"{"ok":true}"#.utf8), HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!)
            }, helloTransport: { _ in
                Issue.record("Unknown Watch metadata is not permission to open a WebSocket")
                throw ExtensionSurfaceHTTPError(status: 403)
            })
            do {
                _ = try await client.refreshCatalog(group: "", expected: state, store: store)
                Issue.record("Unknown capabilities must ask for a phone sync")
            } catch is ExtensionSurfaceCapabilitiesUnknown { }
            #expect(await client.widget(cached.key, family: "small", store: store)?.answer == cached.answer)
            #expect(store.read().groups[""]?.credential == watch)
            #expect(store.read().groups[""]?.widgets == [widget])
            // A metadata-only update preserves offline state but fences replies based on the old capabilities.
            watch.capabilities = []
            try store.replaceCredentials(["": watch])
            try store.catalog(group: "", expected: state, widgets: [], shortcuts: [])
            #expect(store.read().groups[""]?.widgets == [widget])
            let known = try #require(store.read().groups[""])
            let unsupported = ExtensionSurfaceClient(credential: watch, transport: client.transport, helloTransport: client.helloTransport)
            #expect(try await unsupported.refreshCatalog(group: "", expected: known, store: store).isEmpty)
            #expect(store.read().groups[""]?.cache.isEmpty == true)
        }
    }

    @Test func widgetProjectionUsesOpensBestFamilyAndExactGroup() throws {
        try withStore { store in
            let state = try seed(store)
            var place = widget; place.families = ["accessory_inline", "accessory_circular", "accessory_rectangular"]
            try store.catalog(group: "", expected: state, widgets: [place], shortcuts: [])
            let link = ExtensionSystemLink(owner: "sample", ui: "usage", group: "")
            #expect(store.read().projection(link)?.family == "accessory_rectangular")
            #expect(store.read().projection(ExtensionSystemLink(owner: "sample", ui: "today", group: "")) == nil)
            #expect(store.read().projection(ExtensionSystemLink(owner: "sample", ui: "usage", group: "other")) == nil)
            #expect(store.read().projection(ExtensionSystemLink(owner: "other", ui: "usage", group: "")) == nil)
        }
    }

    @Test func readOnlyProjectionDropsControlsAndFitsAccessoryFamilies() {
        let view = ExtensionView(sections: [ExtensionSection(items: [
            .field(ExtensionFieldComponent()), .button(ExtensionButtonComponent()),
            .progress(ExtensionProgressComponent(label: "Budget", ratio: 0.5)),
            .metric(ExtensionMetricComponent(label: "Usage", value: "12")),
            .row(ExtensionRowComponent(title: "Read only", action: ExtensionAction(id: "must-not-run")))
        ])])
        #expect(ExtensionSurfaceContent.items(view, family: "small").map(\.type) == ["progress", "metric", "row"])
        #expect(ExtensionSurfaceContent.items(view, family: "accessory_inline").map(\.type) == ["metric"])
        #expect(ExtensionSurfaceContent.items(view, family: "accessory_circular").map(\.type) == ["progress"])
    }
}
