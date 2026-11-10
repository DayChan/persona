import SwiftUI

/// The buttons of the `brief` cards (#2320): Accept / Got It / Take Recommendation on a to-do to verify, Handled on a
/// question that can wait. What each one came to is kept here by row, not in the card, so a card drawn again (scrolled away
/// and back, or another briefing listing the same round) still says what happened.
@MainActor @Observable final class BriefActions {
    /// What became of a button.
    enum Outcome: Equatable, Sendable {
        /// Its request is out: another tap does nothing.
        case busy
        /// The hub took the to-do: Accepted, Noted or Recommendation Taken.
        case taken(BriefBody.Row.Take)
        /// The task ended without delivering a recommendation or another agent turn.
        case completed
        /// The question left the queue.
        case marked
        /// 409: that round was over, or the question was not in the queue any more.
        case alreadyHandled
        /// 404: the hub has no such route (rolled back past the item's routes), so the button goes.
        case hubTooOld
    }

    static let shared = BriefActions()

    private(set) var outcomes: [String: Outcome] = [:]
    /// The hub of the conversation the card is in.
    @ObservationIgnored private let api: (String) -> APIClient?
    /// Says why a request failed; the button works again.
    @ObservationIgnored private let failed: (String) -> Void
    /// After a to-do was taken: its state changed on the hub.
    @ObservationIgnored private let taken: () -> Void
    /// Writes completion through the shared index so the pending tray updates as soon as the PATCH succeeds.
    @ObservationIgnored private let complete: (String, APIClient) async throws -> Void

    init(api: @escaping (String) -> APIClient? = { AppModel.shared.api($0) },
         failed: @escaping (String) -> Void = { AppModel.shared.toast = Toast($0, style: .error) },
         taken: @escaping () -> Void = { TasksStore.shared.reloadTodos() },
         complete: @escaping (String, APIClient) async throws -> Void = { id, api in
             _ = try await TodoStore.shared.update(id: id, done: true, api: api)
         }) {
        self.api = api
        self.failed = failed
        self.taken = taken
        self.complete = complete
    }

    /// A round of a to-do: the same round listed by two briefings is one button.
    private static func key(accepting row: BriefBody.Row, group: String) -> String { "\(group)|accept|\(row.id)|\(row.askedAt ?? "")" }
    private static func key(marking row: BriefBody.Row, group: String) -> String { "\(group)|later|\(row.id)" }

    func outcome(accepting row: BriefBody.Row, group: String = "") -> Outcome? { outcomes[Self.key(accepting: row, group: group)] }
    func outcome(marking row: BriefBody.Row, group: String = "") -> Outcome? { outcomes[Self.key(marking: row, group: group)] }

    /// Accept, Got It or Take Recommendation (`POST /api/v1/todos/{id}/accept` with the row's round).
    func accept(_ row: BriefBody.Row, group: String) async {
        guard let take = row.take, let askedAt = row.askedAt, !askedAt.isEmpty else { return }
        await run(Self.key(accepting: row, group: group), group: group, done: .taken(take), took: taken, failure: { String(localized: "Couldn't accept: \($0)") }) {
            try await $0.acceptTodo(row.id, askedAt: askedAt)
        }
    }

    /// End a decision task through PATCH done=true, never through /accept or interaction resolution.
    func completeDirectly(_ row: BriefBody.Row, group: String) async {
        guard row.isDecision, !row.id.isEmpty, row.askedAt?.isEmpty == false else { return }
        await run(Self.key(accepting: row, group: group), group: group, done: .completed, took: taken,
                  failure: { String(localized: "Couldn't complete the task: \($0)") }, terminalErrors: false) {
            try await self.complete(row.id, $0)
        }
    }

    /// Handled (`POST /api/v1/later/{id}/handled`).
    func markHandled(_ row: BriefBody.Row, group: String) async {
        await run(Self.key(marking: row, group: group), group: group, done: .marked, took: {}, failure: { String(localized: "Couldn't mark it as handled: \($0)") }) {
            try await $0.laterHandled(row.id)
        }
    }

    private func run(_ key: String, group: String, done: Outcome, took: () -> Void, failure: (String) -> String,
                     terminalErrors: Bool = true, _ call: (APIClient) async throws -> Void) async {
        guard outcomes[key] == nil else { return }
        guard let api = api(group) else {
            failed(failure(String(localized: "Not connected to a hub [Todo Store 04f822]")))
            return
        }
        outcomes[key] = .busy
        do {
            try await call(api)
            outcomes[key] = done
            took()
        } catch let e as APIError where terminalErrors && e.status == 409 {
            outcomes[key] = .alreadyHandled
        } catch let e as APIError where terminalErrors && e.status == 404 {
            outcomes[key] = .hubTooOld
        } catch {
            // A 502 (a decision's recommendation that didn't reach its Agent), a 503 or no answer: nothing was taken.
            outcomes[key] = nil
            failed(failure(error.localizedDescription))
        }
    }
}
