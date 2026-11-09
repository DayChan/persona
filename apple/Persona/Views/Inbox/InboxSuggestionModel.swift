import Foundation
import Observation

/// The suggestion card's logic (docs/assistant/inbox.md): confirm / edit then confirm / reject / say a word, each one request with the
/// version the card shows, so a suggestion the manager replaced meanwhile is never run: the hub answers 409
/// `suggestion_stale` with the item as it is, the card says 「建议已更新」 and draws the new one.
@MainActor @Observable
final class InboxSuggestionModel {
    /// The request in flight (the card disables its buttons, the pressed one spins).
    private(set) var busy: String?
    /// 「建议已更新」 after a stale answer, until the next action.
    private(set) var notice: String?
    /// Why the last action failed.
    private(set) var error: String?

    private let itemID: String
    private let send: (String, InboxSuggestionBody) async throws -> InboxItem
    private let apply: (InboxItem) -> Void

    /// `send` posts to the hub (`APIClient.inboxSuggestion`); `apply` puts the item it answers into the list.
    init(itemID: String, send: @escaping (String, InboxSuggestionBody) async throws -> InboxItem, apply: @escaping (InboxItem) -> Void) {
        self.itemID = itemID
        self.send = send
        self.apply = apply
    }

    /// The app's own: the hub of the moment and the inbox store.
    /// `manager`: the item's (its list's stamp), whose suggestion it is.
    static func live(_ itemID: String, manager: String? = nil) -> InboxSuggestionModel {
        InboxSuggestionModel(itemID: itemID, send: { id, body in
            guard let api = AppModel.shared.api?.manager(manager) else { throw APIError(status: 0, message: String(localized: "Not connected to the hub [Inbox Suggestion Model e21521]")) }
            return try await api.inboxSuggestion(id, body)
        }, apply: { InboxStore.shared.put($0, manager: manager) })
    }

    /// send / start / got it / confirm, with `text` when the user changed the words (edit).
    @discardableResult
    func confirm(_ s: InboxItem.Suggestion, text: String? = nil) async -> InboxSuggestionOutcome? {
        let t = text?.trimmingCharacters(in: .whitespacesAndNewlines)
        return await run("confirm", s, text: t == nil || t == s.text ? nil : t)
    }

    @discardableResult
    func reject(_ s: InboxItem.Suggestion) async -> InboxSuggestionOutcome? { await run("reject", s, text: nil) }

    /// say a word: the words go back to the processing conversation, which suggests again. Empty words do nothing.
    @discardableResult
    func instruct(_ s: InboxItem.Suggestion, _ words: String) async -> InboxSuggestionOutcome? {
        let w = words.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !w.isEmpty else { return nil }
        return await run("instruct", s, text: w)
    }

    private func run(_ action: String, _ s: InboxItem.Suggestion, text: String?) async -> InboxSuggestionOutcome? {
        guard busy == nil else { return nil }
        busy = action
        notice = nil
        error = nil
        defer { busy = nil }
        let body = InboxSuggestionBody(version: s.version, action: action, text: text, clientID: UUID().uuidString)
        let outcome: InboxSuggestionOutcome
        do {
            outcome = .done(try await send(itemID, body))
        } catch {
            outcome = InboxSuggestionOutcome.of(error)
        }
        switch outcome {
        case .done(let item):
            apply(item)
        case .stale(let item):
            if let item { apply(item) }
            notice = String(localized: "Suggestion updated")
        case .failed(let words, let item):
            if let item { apply(item) }
            error = words
        }
        return outcome
    }
}
