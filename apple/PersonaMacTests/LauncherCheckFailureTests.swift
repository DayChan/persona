import Foundation
import Testing
@testable import Persona

/// Test Availability on a provider's page said "The request didn't finish" for every failed request (#1901): the hub's
/// reason (computer offline, a node call that failed or timed out, no machine to test on) was dropped, while web and
/// Android show it.
struct LauncherCheckFailureTests {
    private let fallback = String(localized: "The request didn't finish")

    @Test func theHubsWordsAreTheRowsReason() {
        let offline = LauncherCheck.failed(APIError(status: 503, message: "work-mac is offline"))
        #expect(offline.ok == false)
        #expect(offline.millis == 0)
        #expect(offline.error == "work-mac is offline")
        #expect(LauncherCheck.failed(APIError(status: 400, message: "no machine to test on")).error == "no machine to test on")
        #expect(LauncherCheck.failed(APIError(status: 409, message: "launcher not found on this machine")).error == "launcher not found on this machine")
        // a node call that ran out of time is still the hub's answer, not a lost request
        #expect(LauncherCheck.failed(APIError(status: 502, message: "work-mac did not answer in time (timeout)")).error == "work-mac did not answer in time (timeout)")
        // a server error keeps the request id the hub logged it under
        #expect(LauncherCheck.failed(APIError(status: 500, message: "boom", requestID: "r1")).error == APIError(status: 500, message: "boom", requestID: "r1").localizedDescription)
    }

    @Test func nothingAnsweredIsTheFallback() {
        #expect(LauncherCheck.failed(URLError(.timedOut)).error == fallback)
        #expect(LauncherCheck.failed(URLError(.notConnectedToInternet)).error == fallback)
        #expect(LauncherCheck.failed(CancellationError()).error == fallback)
        #expect(LauncherCheck.failed(APIError(status: 502, message: "")).error == fallback)
    }
}
