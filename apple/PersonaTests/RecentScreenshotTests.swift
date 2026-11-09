import Foundation
import Testing
@testable import Persona

/// The + menu's recent screenshot (#1994): which screenshot is offered, for how long, and which access row shows
/// without full photo access.
@MainActor struct RecentScreenshotTests {
    private typealias Rule = RecentScreenshotRule
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func shot(_ id: String, secondsAgo: TimeInterval) -> Rule.Candidate {
        .init(id: id, taken: now.addingTimeInterval(-secondsAgo))
    }

    @Test func offersTheNewestScreenshotInsideTheWindow() {
        let list = [shot("old", secondsAgo: 90), shot("new", secondsAgo: 10), shot("mid", secondsAgo: 40)]
        #expect(Rule.pick(list, now: now, handled: []) == shot("new", secondsAgo: 10))
    }

    @Test func twoMinutesIsTheEdge() {
        #expect(Rule.pick([shot("a", secondsAgo: 120)], now: now, handled: []) != nil)
        #expect(Rule.pick([shot("a", secondsAgo: 121)], now: now, handled: []) == nil)
        #expect(Rule.window == 120)
    }

    @Test func aStampALittleAheadOfTheClockCounts() {
        #expect(Rule.pick([shot("a", secondsAgo: -3)], now: now, handled: []) != nil)
        #expect(Rule.pick([shot("a", secondsAgo: -30)], now: now, handled: []) == nil)
    }

    @Test func anAttachedScreenshotGivesWayToTheNextOne() {
        let list = [shot("new", secondsAgo: 10), shot("earlier", secondsAgo: 60)]
        #expect(Rule.pick(list, now: now, handled: ["new"])?.id == "earlier")
        #expect(Rule.pick(list, now: now, handled: ["new", "earlier"]) == nil)
        #expect(Rule.pick([], now: now, handled: []) == nil)
    }

    @Test func theOfferLeavesWhenItGetsOld() {
        #expect(Rule.expiry(shot("a", secondsAgo: 30)) == now.addingTimeInterval(90))
    }

    /// Without full access the menu stays as it was plus one row that asks, until it is used once.
    @Test func theAccessRow() {
        #expect(Rule.guide(.notDetermined, used: false) == .ask)
        #expect(Rule.guide(.denied, used: false) == .settings)
        #expect(Rule.guide(.limited, used: false) == .settings) // limited access never sees a new screenshot
        #expect(Rule.guide(.granted, used: false) == .none)
        for status in [PhoneAuthStatus.notDetermined, .denied, .limited, .granted] {
            #expect(Rule.guide(status, used: true) == .none)
        }
    }

    @Test func readsOnlyWithFullAccess() {
        #expect(Rule.reads(.granted))
        for status in [PhoneAuthStatus.notDetermined, .denied, .limited, .notNeeded] { #expect(!Rule.reads(status)) }
    }

    /// Access asked for from the + menu is not access for Claude: its Photos switch, left at its default (on), is turned
    /// off before the prompt or Settings; one the user set stays as set.
    @Test func claudesPhotosSwitchStaysAsChosen() {
        let key = "phoneSource.photos"
        let saved = UserDefaults.standard.object(forKey: key)
        defer { UserDefaults.standard.set(saved, forKey: key) }

        UserDefaults.standard.removeObject(forKey: key)
        #expect(!PhoneDataSource.photos.hasChoice && PhoneDataSource.photos.isEnabled) // the default: on
        RecentScreenshots.keepClaudePhotosAsChosen()
        #expect(PhoneDataSource.photos.hasChoice && !PhoneDataSource.photos.isEnabled)

        PhoneDataSource.photos.isEnabled = true // turned on by the user
        RecentScreenshots.keepClaudePhotosAsChosen()
        #expect(PhoneDataSource.photos.isEnabled)
    }

    /// The BeautifulUI style's attach list: the screenshot first, then Photos as before; nothing new without one.
    @Test func thePromptBarListPutsItFirst() {
        let with = PromptItems.attachments(camera: true, recentScreenshot: now)
        #expect(with.map(\.id) == [PromptItems.recentScreenshotID, "photo", "camera", "file"])
        #expect(PromptItems.attachments(camera: true).map(\.id) == ["photo", "camera", "file"])
    }
}
