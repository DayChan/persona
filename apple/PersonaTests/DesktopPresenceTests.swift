import Foundation
import Testing
@testable import Persona

/// 「Mac 上的 Persona 已登录」(docs/protocol/protocol.md「桌面端」): the hub says which desktops are connected; a phone may
/// choose Phone Muted while one is.
struct DesktopPresenceTests {
    private static func link(_ name: String, _ machine: String) -> DesktopDevice {
        DesktopDevice(device: "", name: name, since: nil, kind: .link, machineID: machine)
    }

    @Test func decodesTheDesktopsEvent() throws {
        let json = #"{"type":"desktops","desktops":[{"device":"dev_1","name":"MacBook","since":"2026-11-01T08:00:00.123456789+08:00"}]}"#
        guard case .desktops(let list)? = ServerEvent.decode(Data(json.utf8)) else { Issue.record("not decoded"); return }
        #expect(list.map(\.name) == ["MacBook"])
        #expect(list.first?.device == "dev_1")
        #expect(list.first?.since != nil)
    }

    /// Persona Link entries (sent only to clients declaring `desktops_link`) carry `kind` and `machine_id`; an entry without
    /// `kind` is the Mac app, as every hub before them sends. A kind this build doesn't know keeps the rest of the list.
    @Test func decodesPersonaLinkEntries() throws {
        let json = #"{"type":"desktops","desktops":[{"device":"dev_1","name":"MacBook"},"# +
            #"{"kind":"link","machine_id":"mbp","device":"","name":"MacBook","since":"2026-11-01T08:00:00Z"},"# +
            #"{"kind":"later","device":"","name":"PC"}]}"#
        guard case .desktops(let list)? = ServerEvent.decode(Data(json.utf8)) else { Issue.record("not decoded"); return }
        try #require(list.count == 3)
        #expect(list.map(\.kind) == [nil, .link, .unknown])
        #expect(list.map(\.machineID) == [nil, "mbp", nil])
        #expect(list[1].since != nil)
    }

    /// The sheet lists each entry: Persona Link on a Mac whose app connected with the pairing token (no device id) is still
    /// its own row, and the same machine's Persona Link is one row however it is named.
    @Test func idsStayApartAcrossKinds() {
        let app = DesktopDevice(device: "", name: "MacBook", since: nil)
        #expect(app.id != Self.link("MacBook", "mbp").id)
        #expect(Self.link("MacBook", "mbp").id == Self.link("Work Mac", "mbp").id)
    }

    /// Sign Out revokes a device token: only a Mac app that has one offers it; Persona Link has nothing to revoke.
    @Test func onlyAMacAppWithItsOwnTokenSignsOut() {
        #expect(DesktopDevice(device: "d1", name: "MacBook", since: nil).canSignOut)
        #expect(!DesktopDevice(device: "", name: "MacBook", since: nil).canSignOut)
        #expect(!DesktopDevice(device: "d1", name: "MacBook", since: nil, kind: .link, machineID: "mbp").canSignOut)
        #expect(!DesktopDevice(device: "d1", name: "PC", since: nil, kind: .unknown).canSignOut)
    }

    @Test func noDesktopsIsAnEmptyList() throws {
        guard case .desktops(let list)? = ServerEvent.decode(Data(#"{"type":"desktops","desktops":[]}"#.utf8)) else {
            Issue.record("not decoded"); return
        }
        #expect(list.isEmpty)
    }

    @Test func muteWhileDesktopRoundTrips() throws {
        #expect(try JSON.decoder.decode(NotifyPrefs.self, from: Data("{}".utf8)).muteWhileDesktop == false)
        var p = try JSON.decoder.decode(NotifyPrefs.self, from: Data(#"{"mute_while_desktop":true}"#.utf8))
        #expect(p.muteWhileDesktop)
        // Settings › Notifications saves the whole NotifyPrefs it loaded: it must not carry Phone Muted back (an older value would undo a
        // switch made on the banner meanwhile). The banner writes it alone.
        let obj = try JSONSerialization.jsonObject(with: try JSON.encoder.encode(p)) as? [String: Any]
        #expect(obj?["mute_while_desktop"] == nil)
        let alone = try JSONSerialization.jsonObject(with: try JSON.encoder.encode(MuteWhileDesktopPut(on: true))) as? [String: Any]
        #expect(alone?.count == 1 && alone?["mute_while_desktop"] as? Bool == true)
    }

    /// The banner's line: one desktop by kind, several by count.
    @Test func bannerTitle() {
        let one = [DesktopDevice(device: "d1", name: "MacBook", since: nil)]
        #expect(DesktopDevice.bannerTitle(one) == "Mac 上的 Persona 已登录")
        #expect(DesktopDevice.bannerTitle(one + [DesktopDevice(device: "d2", name: "iMac", since: nil)]) == "2 台 Mac 上的 Persona 已登录")
    }

    /// Persona Link alone names the computer you're at, several by count; next to Mac apps, a Mac with both counts once
    /// (by name: a Mac app has no machine id).
    @Test func bannerTitleWithPersonaLink() {
        let app = DesktopDevice(device: "d1", name: "MacBook", since: nil)
        let studio = Self.link("Mac Studio", "studio"), mbp = Self.link("MacBook", "mbp")
        #expect(DesktopDevice.bannerTitle([studio]) == "正在使用「Mac Studio」")
        #expect(DesktopDevice.bannerTitle([studio, mbp]) == "正在使用 2 台电脑")
        #expect(DesktopDevice.bannerTitle([studio, Self.link("Mac Studio", "studio2")]) == "正在使用 2 台电脑") // two machines, one name
        #expect(DesktopDevice.bannerTitle([app, mbp]) == "Mac 上的 Persona 已登录")
        #expect(DesktopDevice.bannerTitle([mbp, app]) == "Mac 上的 Persona 已登录")
        #expect(DesktopDevice.bannerTitle([app, mbp, studio]) == "正在使用 2 台电脑")
    }

    /// Phone Muted's footer: the Mac app's rule while any Mac app is online; Persona Link alone mutes only "Needs you", and with
    /// several computers it comes back once every one of them is locked or has quit Persona Link, not after the first.
    @Test func muteNoteFollowsWhatIsListed() {
        let app = DesktopDevice(device: "d1", name: "MacBook", since: nil)
        let studio = Self.link("Mac Studio", "studio"), mbp = Self.link("MacBook", "mbp")
        #expect(DesktopDevice.muteNote([app]).contains("由 Mac 提醒你"))
        #expect(DesktopDevice.muteNote([app, mbp, studio]).contains("由 Mac 提醒你"))
        #expect(DesktopDevice.muteNote([studio]).contains("锁定那台电脑"))
        #expect(DesktopDevice.muteNote([studio, mbp]).contains("这些电脑都锁定"))
    }

    /// A kind this build doesn't know is left out of what the banner and the sheet show: it says nothing about a Mac signing in
    /// or what is muted, and can't collide with a Mac app's id.
    @Test func anUnknownKindIsLeftOut() {
        let pc = DesktopDevice(device: "", name: "MacBook", since: nil, kind: .unknown)
        #expect(DesktopDevice.shown([pc]).isEmpty) // no banner

        let studio = Self.link("Mac Studio", "studio")
        let withLink = DesktopDevice.shown([studio, pc])
        #expect(withLink == [studio])
        #expect(DesktopDevice.bannerTitle(withLink) == "正在使用「Mac Studio」")
        #expect(DesktopDevice.muteNote(withLink).contains("锁定那台电脑"))

        let app = DesktopDevice(device: "", name: "MacBook", since: nil) // same name, and id `name:MacBook` like the unknown one
        let withApp = DesktopDevice.shown([app, pc])
        #expect(withApp.map(\.id) == ["name:MacBook"])
        #expect(DesktopDevice.bannerTitle(withApp) == "Mac 上的 Persona 已登录")
        #expect(DesktopDevice.muteNote(withApp).contains("由 Mac 提醒你"))
    }

    #if os(iOS)
    /// The iPhone is the phone: it registers its tools (the Mac registers as a desktop, PersonaMacTests).
    @Test func thePhoneRegistersAsThePhone() {
        let m = AppModel.registration(deviceName: "iPhone", tools: [], simulator: false)
        #expect(m["type"] as? String == "register_phone")
        #expect(m["tools"] != nil)
    }
    #endif
}
