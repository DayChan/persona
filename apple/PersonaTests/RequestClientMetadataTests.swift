import Foundation
import Testing
@testable import Persona

struct RequestClientMetadataTests {
    @Test func platformsAndSurfacesUseBoundedPublicTokens() {
        let platforms: [RequestClientMetadata.Platform] = [.iOS, .macOS, .watchOS, .visionOS, .other]
        let surfaces: [RequestClientMetadata.Surface] = [.app, .widget, .intent, .systemExtension]
        for platform in platforms {
            for surface in surfaces {
                let metadata = RequestClientMetadata(platform: platform, surface: surface, version: "1.2.3+4")
                #expect(metadata.value == "\(platform.rawValue)-\(surface.rawValue)/1.2.3+4")
                #expect(metadata.headers[RequestClientMetadata.headerName] == metadata.value)
                #expect(metadata.headers[LegacyNames.clientHeader] == metadata.value)
            }
        }
    }

    @Test func unsafeAndOversizedVersionsCannotInjectHeaders() {
        let source = String(repeating: "bad/\r\n😀", count: 100)
        let metadata = RequestClientMetadata(platform: .visionOS, surface: .systemExtension, version: source)
        #expect(metadata.value.utf8.count <= 128)
        let parts = metadata.value.split(separator: "/", omittingEmptySubsequences: false)
        #expect(parts.count == 2 && !parts[1].isEmpty)
        #expect(parts.allSatisfy { part in
            part.utf8.allSatisfy { (65...90).contains($0) || (97...122).contains($0) || (48...57).contains($0) || [46, 95, 43, 45].contains($0) }
        })
        #expect(RequestClientMetadata(platform: .iOS, surface: .app, version: "").value == "ios-app/unknown")
    }

    @Test func widgetDetectionAndExplicitIntentDoNotDependOnAppIdentity() {
        #expect(RequestClientMetadata.Surface.detect(isExtension: true, extensionPoint: "com.apple.widgetkit-extension") == .widget)
        #expect(RequestClientMetadata.Surface.detect(isExtension: true, extensionPoint: "other") == .systemExtension)
        #expect(RequestClientMetadata.Surface.detect(isExtension: false, extensionPoint: nil) == .app)
        #expect(RequestClientMetadata(platform: .iOS, surface: .intent, version: "1").value == "ios-intent/1")
    }

    @Test func applyingAttributionPreservesAuthenticationAndTransferSemantics() {
        var request = URLRequest(url: URL(string: "https://fake.invalid/data")!)
        let preserved = ["Authorization": "Bearer fixture", "Accept-Language": "en", "Range": "bytes=1-2",
                         "Accept-Encoding": "identity", "If-Match": "fixture-revision", "X-Persona-Op-Id": "fixture-operation"]
        for (key, value) in preserved { request.setValue(value, forHTTPHeaderField: key) }
        let metadata = RequestClientMetadata(platform: .iOS, surface: .intent, version: "1")
        metadata.apply(to: &request)
        for (key, value) in preserved { #expect(request.value(forHTTPHeaderField: key) == value) }
        #expect(request.value(forHTTPHeaderField: RequestClientMetadata.headerName) == metadata.value)
        #expect(request.value(forHTTPHeaderField: LegacyNames.clientHeader) == metadata.value)
    }

    @MainActor @Test func localTodoTransportReceivesTheExplicitExecutionSurface() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        var client = hub.client
        client.clientMetadata = RequestClientMetadata(platform: .iOS, surface: .intent, version: "1")
        _ = try await client.todos()
        let request = try #require(hub.sent("GET", "/api/v1/todos").first)
        #expect(request.headers[RequestClientMetadata.headerName] == "ios-intent/1")
        #expect(request.headers[LegacyNames.clientHeader] == "ios-intent/1")
    }
}
