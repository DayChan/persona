import Foundation
import Testing
@testable import Persona

/// What the app had on its way when it was ended comes back at the next launch (`AppModel.saveOutbox` /
/// `restoreOutbox`): a message the hub took and whose transcript copy has not come yet too (#1455), and not one whose
/// copy the chat already has.
@MainActor
@Suite(.serialized)
struct OutboxRestoreTests {
    private let key = SessionKey(machineID: "box", sessionID: "outbox-restore")
    /// The app's disk: a folder of the test's own, not the simulator's app's (#1637).
    private let folder = FileManager.default.temporaryDirectory.appending(path: "outbox-restore-\(UUID().uuidString)")

    private func message(_ state: PendingMessage.State, age: TimeInterval = 0) -> PendingMessage {
        var p = PendingMessage(text: "Is the museum open on Sunday?", images: [])
        p.state = state
        p.createdAt = Date().addingTimeInterval(-age).rounded()
        return p
    }

    /// The transcript's copy of `p`, tied to it by the hub.
    private func copy(of p: PendingMessage) -> Item {
        var it = Item(id: "l4#0", seq: 4, ts: Date(), role: "user", kind: .text, text: p.text, toolUseID: nil, toolName: nil,
                      toolInput: nil, isError: nil, image: nil)
        it.clientMsgID = p.id.uuidString
        return it
    }

    private func clean() {
        try? FileManager.default.removeItem(at: folder)
    }

    /// The app with `pending` in the chat is ended once its outbox is written (300 ms after a change) and starts again.
    private func relaunched(with pending: [PendingMessage], cached: [Item] = []) async throws -> [PendingMessage] {
        clean()
        let before = AppModel(stateFolder: folder)
        before.chat(key).pending = pending
        before.saveOutbox()
        try await Task.sleep(for: .milliseconds(600))
        if !cached.isEmpty { ChatCache.save(key, items: cached, hasMore: false, in: folder) }
        let after = AppModel(stateFolder: folder)
        after.restoreOutbox()
        let restored = after.chat(key).pending
        try await Task.sleep(for: .milliseconds(600)) // what the restore writes back, before the next test starts
        clean()
        return restored
    }

    @Test func aSentMessageWaitingForItsCopyComesBackAsSent() async throws {
        let p = message(.sent)
        let back = try await relaunched(with: [p])
        #expect(back.map(\.id) == [p.id])
        #expect(back.first?.state == .sent) // the hub has it: it is not sent again
    }

    @Test func aSentMessageWhoseCopyIsCachedDoesNotComeBack() async throws {
        let p = message(.sent)
        #expect(try await relaunched(with: [p], cached: [copy(of: p)]).isEmpty)
    }

    /// The request was out when the app ended, and the copy reached the chat's cache: it arrived.
    @Test func aMessageInFlightWhoseCopyIsCachedDoesNotComeBack() async throws {
        let p = message(.sending)
        #expect(try await relaunched(with: [p], cached: [copy(of: p)]).isEmpty)
    }

    @Test func aMessageInFlightComesBackWaiting() async throws {
        let p = message(.sending)
        let back = try await relaunched(with: [p])
        #expect(back.map(\.id) == [p.id])
        #expect(back.first?.state == .waiting)
    }

    /// Offline for a day or two is still worth sending; a week on, its chat may be long gone.
    @Test func aWaitingMessageComesBackUntilItIsAWeekOld() async throws {
        let recent = message(.waiting, age: 86400 * 2)
        #expect(try await relaunched(with: [recent]).map(\.id) == [recent.id])
        #expect(try await relaunched(with: [message(.waiting, age: OutboxWait.restoreLimit + 60)]).isEmpty)
    }

    /// Longer ago than a sent message ever waits for its copy: it had its copy or gave up then.
    @Test func aSentMessageFromLongAgoDoesNotComeBack() async throws {
        #expect(try await relaunched(with: [message(.sent, age: AppModel.sentWait + 60)]).isEmpty)
    }
}

private extension Date {
    /// Whole seconds, as the outbox keeps it.
    func rounded() -> Date { Date(timeIntervalSince1970: timeIntervalSince1970.rounded(.down)) }
}
