import Foundation
import Testing
@testable import Persona

/// #55: the watch face shows what the watch app last saw, and stops standing for the agents once that is old.
struct WatchPetSnapshotTests {
    private let at = Date(timeIntervalSince1970: 10_000)
    private var snapshot: WatchPetSnapshot {
        WatchPetSnapshot(state: "idle", title: "Nothing going on", staleTitle: "Not updated lately", at: at)
    }

    @Test func aFreshSnapshotShowsItsState() {
        let shown = snapshot.shown(at: at.addingTimeInterval(WatchPetSnapshot.staleAfter - 1))
        #expect(shown.state == "idle" && shown.title == "Nothing going on" && !shown.stale)
    }

    @Test func anOldSnapshotIsNeverCalm() {
        let shown = snapshot.shown(at: snapshot.staleDate)
        #expect(shown.state == WatchPetSnapshot.offlineState && shown.title == "Not updated lately" && shown.stale)
    }

    @Test func itRoundTripsAndNamesItsFaces() throws {
        let data = try JSONEncoder().encode(snapshot)
        #expect(try JSONDecoder().decode(WatchPetSnapshot.self, from: data) == snapshot)
        #expect(WatchPetSnapshot.faceName("needs_you") == "face-needs_you.png")
    }

    /// The face's offline picture is the app's offline state: the same name the watch app copies.
    @Test func theOfflineStateIsTheFrogs() {
        #expect(WatchPetState(rawValue: WatchPetSnapshot.offlineState) == .offline)
    }
}
