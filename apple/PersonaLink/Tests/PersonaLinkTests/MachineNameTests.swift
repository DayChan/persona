import Foundation
import XCTest
@testable import PersonaLink

/// What the hub calls this computer (docs/operations/link.md "The computer's name"), as the helper's status says it.
final class MachineNameTests: XCTestCase {
    private func status(_ json: String) throws -> LinkStatus {
        try JSONDecoder().decode(LinkStatus.self, from: Data(json.utf8))
    }

    func testTheHubsNameIsShownAndRenamable() throws {
        let s = try status(#"{"state":"connected","machine":"studio-mac","machine_name":"Studio","mode":"active"}"#)
        XCTAssertEqual(s.shownMachine, "Studio")
        XCTAssertTrue(s.renamable)
    }

    func testAnOlderHubShowsTheIdAndCannotRename() throws {
        let s = try status(#"{"state":"connected","machine":"studio-mac"}"#)
        XCTAssertEqual(s.shownMachine, "studio-mac")
        XCTAssertFalse(s.renamable)
    }

    func testNoRenameWhilePendingOfflineOrPassive() throws {
        XCTAssertFalse(try status(#"{"state":"connected","machine":"m","machine_name":"M","pending":true}"#).renamable)
        XCTAssertFalse(try status(#"{"state":"waiting","machine":"m","machine_name":"M"}"#).renamable)
        XCTAssertFalse(try status(#"{"state":"connected","machine":"m","machine_name":"M","mode":"passive"}"#).renamable)
    }
}
