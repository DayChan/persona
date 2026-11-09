import Foundation
import Testing
@testable import Persona

/// This device's notifications per manager and its quiet hours (#1863, the device prefs' `muted_managers` and `quiet`):
/// each goes alone in its PUT so the hub merges nothing else, a hub without the field shows no switch, a refused change
/// is put back, and Settings › Notifications' own save never sends either. Made-up values.
@MainActor
struct ManagerPushTests {
    private static let path = "/api/v1/devices/dev1/prefs"

    private func body(_ req: LocalRequest?) throws -> [String: Any] {
        let data = try #require(req?.body)
        return try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    @Test func theSwitchSendsOnlyTheMutedManagers() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: #"{"input":true,"done":"long","muted_managers":["m-2"]}"#)
        hub.on("PUT", Self.path) { req in
            let sent = try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any]
            let muted = try JSONSerialization.data(withJSONObject: sent?["muted_managers"] ?? [])
            return Data(#"{"input":true,"done":"long","muted_managers":"#.utf8) + muted + Data("}".utf8)
        }
        let api = hub.client
        var shared: NotifyPrefs?
        let push = ManagerPushModel(id: "m-1", deps: .init(api: { api }, device: { "dev1" }, shared: { shared = $0 }))
        await push.load()
        #expect(push.available && push.on)
        await push.set(false)
        let sent = try body(hub.sent("PUT", Self.path).first)
        #expect(Array(sent.keys) == ["muted_managers"], "nothing else goes with it")
        #expect(sent["muted_managers"] as? [String] == ["m-2", "m-1"])
        #expect(!push.on && shared?.mutedManagers == ["m-2", "m-1"], "the Mac's banners read the saved list")
        await push.set(true)
        #expect(push.on && shared?.mutes(manager: "m-1") == false && shared?.mutes(manager: "m-2") == true)
    }

    @Test func aRefusedChangeIsPutBack() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: #"{"input":true,"done":"long","muted_managers":[]}"#)
        hub.on("PUT", Self.path) { _ in throw APIError(status: 500, message: "down") }
        let api = hub.client
        let push = ManagerPushModel(id: "m-1", deps: .init(api: { api }, device: { "dev1" }, shared: { _ in }))
        await push.load()
        await push.set(false)
        #expect(push.on && push.error?.contains("down") == true)
    }

    @Test func noSwitchOnAHubWithoutIt() async {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: #"{"input":true,"done":"long"}"#)
        let api = hub.client
        let push = ManagerPushModel(id: "m-1", deps: .init(api: { api }, device: { "dev1" }, shared: { _ in }))
        await push.load()
        #expect(!push.available)
    }

    @Test func quietHoursGoAloneWithTheirTimeZone() async throws {
        let hub = FakeHub()
        hub.on("PUT", Self.path, json: #"{"input":true,"done":"long","quiet":{"enabled":true,"start":"22:30","end":"06:00","tz":"Asia/Tokyo"}}"#)
        let saved = try await hub.client.setQuietHours(device: "dev1", QuietHours(enabled: true, start: "22:30", end: "06:00", tz: "Asia/Tokyo"))
        let sent = try body(hub.sent("PUT", Self.path).first)
        #expect(Array(sent.keys) == ["quiet"])
        let quiet = try #require(sent["quiet"] as? [String: Any])
        #expect(quiet["enabled"] as? Bool == true && quiet["start"] as? String == "22:30" && quiet["end"] as? String == "06:00")
        #expect(quiet["tz"] as? String == "Asia/Tokyo")
        #expect(saved.quiet == QuietHours(enabled: true, start: "22:30", end: "06:00", tz: "Asia/Tokyo"))
    }

    @Test func theirFieldsDecodeAndTheOtherSaveLeavesThemOut() throws {
        let p = try JSON.decoder.decode(NotifyPrefs.self, from: Data(#"""
        {"input":true,"done":"always","muted_managers":["m-2"],"quiet":{"enabled":false,"start":"","end":""}}
        """#.utf8))
        #expect(p.mutedManagers == ["m-2"] && p.mutes(manager: "m-2") && !p.mutes(manager: nil))
        #expect(p.quiet == QuietHours(), "empty times read as the defaults, 23:00 to 07:00")
        let sent = try #require(JSONSerialization.jsonObject(with: JSONEncoder().encode(p)) as? [String: Any])
        #expect(sent["muted_managers"] == nil && sent["quiet"] == nil, "Settings › Notifications' switches never overwrite them")
        let older = try JSON.decoder.decode(NotifyPrefs.self, from: Data(#"{"input":true,"done":"long","quiet":"?"}"#.utf8))
        #expect(older.mutedManagers == nil && older.quiet == nil)
        #expect(QuietHours.minutes("07:05") == 425 && QuietHours.minutes("7") == nil && QuietHours.clock(425) == "07:05")
    }

    @Test func quietHoursSetInAnotherZoneSayWhich() throws {
        let here = try #require(TimeZone(identifier: "Europe/Paris"))
        for tz in [nil, "", "Europe/Paris", "Nowhere/Else"] { #expect(QuietHours(tz: tz).elsewhere(here: here) == nil) }
        let tokyo = QuietHours(tz: "Asia/Tokyo").elsewhere(here: here, locale: Locale(identifier: "en_US"))
        #expect(tokyo?.contains("Japan") == true)
    }
}
