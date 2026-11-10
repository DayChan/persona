import Foundation

/// A to-do's owner-facing review round, shared with briefing cards. The timestamp stays byte-for-byte
/// as the hub sent it so accepting a recommendation cannot answer a newer round.
nonisolated struct TodoAsk: Codable, Hashable, Sendable {
    var outcome: String?
    var verdict: String?
    var recommend: String?
    var askedAt: String

    enum CodingKeys: String, CodingKey {
        case outcome, verdict, recommend
        case askedAt = "asked_at"
    }

    func row(for todo: TodoItem) -> BriefBody.Row {
        let waited = JSON.parseDate(askedAt).map { Int(max(0, Date.now.timeIntervalSince($0))) } ?? 0
        return BriefBody.Row(id: todo.id, title: todo.title, waitedS: waited, askedAt: askedAt,
                             verdict: verdict, outcome: outcome, recommend: recommend)
    }
}
