import Foundation
import Testing
@testable import Persona

/// What a failed read of a session's Git status says (#1889): a refresh the user asked for over a status already shown
/// says it failed; the page's error only when nothing is shown yet; the live refresh and quiet errors say nothing.
struct GitReadFailureTests {
    @Test func aManualRefreshOverAShownStatusSaysItFailed() {
        #expect(GitReadFailure("The computer is offline", shown: true, live: false) == .refresh("The computer is offline"))
    }

    @Test func nothingShownYetIsThePagesError() {
        #expect(GitReadFailure("Not readable", shown: false, live: false) == .page("Not readable"))
    }

    @Test func theLiveRefreshAndQuietErrorsSayNothing() {
        #expect(GitReadFailure("Not readable", shown: true, live: true) == .quiet)
        #expect(GitReadFailure("Not readable", shown: false, live: true) == .quiet)
        #expect(GitReadFailure(nil, shown: true, live: false) == .quiet, "a cancellation, a timeout, no network")
    }
}
