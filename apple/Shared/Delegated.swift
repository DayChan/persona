import Foundation

// The assistant answering agents' questions first (#544; docs/assistant/assistant.md "Assistant answers questions first";
// docs/protocol/protocol.md `interaction`, `interaction_resolved`, `interaction_reanswered`, POST …/reanswer): a question
// card it is still looking at (`delegating`), the context it filled in when it left a question to the user (`delegate`),
// and the questions it answered, which the user may answer again. The pure parts, as the web's lib/delegate.ts; the
// views are Persona/Views/Interactions/DelegateViews.swift.

/// What the assistant answered for the user, and why: `interaction_resolved` with `answered_by: "assistant"`, or the
/// hub's `already` reply to an answer of this device's that came second.
nonisolated struct AssistantAnswer: Hashable, Sendable {
    /// `answered_by` on a question the assistant answered.
    static let assistant = "assistant"
    var answers: [String: String]
    var reason: String
}

/// A question the assistant answered for the user: what it answered and why, and when (nil when the hub did not say).
nonisolated struct DelegatedAnswer: Hashable, Sendable, Identifiable {
    var interaction: Interaction
    var answers: [String: String]
    var reason: String
    var at: Date?
    var id: String { interaction.uid }
}

/// A row of GET /api/v1/interactions/history.
nonisolated struct InteractionRecord: Decodable, Sendable {
    var interaction: Interaction
    /// pending / resolved / expired / cancelled / dismissed.
    var status: String
    var resolution: Resolution?
    var resolvedAt: Date?

    enum CodingKeys: String, CodingKey {
        case interaction, status, resolution
        case resolvedAt = "resolved_at"
    }

    init(interaction: Interaction, status: String, resolution: Resolution? = nil, resolvedAt: Date? = nil) {
        self.interaction = interaction
        self.status = status
        self.resolution = resolution
        self.resolvedAt = resolvedAt
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        interaction = try c.decode(Interaction.self, forKey: .interaction)
        status = (try? c.decodeIfPresent(String.self, forKey: .status)).flatMap { $0 } ?? ""
        resolution = (try? c.decodeIfPresent(Resolution.self, forKey: .resolution)).flatMap { $0 }
        resolvedAt = (try? c.decodeIfPresent(Date.self, forKey: .resolvedAt)).flatMap { $0 }
    }
}

/// One line of the context the assistant filled in (`Delegate`).
nonisolated struct DelegateLine: Hashable, Sendable {
    enum Field: CaseIterable, Sendable { case what, whyNow, recommend, cost, reason, deadline }
    var field: Field
    var label: String
    var text: String
}

nonisolated extension Interaction {
    /// The assistant is still deciding whether it may answer this question for the user: nothing was pushed yet, and an
    /// answer given here wins. Only questions are ever delegated.
    var isDelegating: Bool { kind == .question && delegating == true }
}

nonisolated enum Delegated {
    /// The hub writes a recommendation for several questions as their answers joined by a full-width semicolon
    /// (assistant/delegate.go answerLine), and a multi-select answer as its labels joined by ", ", as the apps send it.
    static let questionsSeparator = "；" // i18n-ignore: the hub's wire format
    static let picksSeparator = ", "
    /// The hub's most: its limit counts every card of the session, permissions too, before only the assistant's answers
    /// are kept.
    static let historyLimit = 500
    /// Answered questions kept for their chats at most, the oldest put away first.
    static let kept = 50

    /// Whether a card arriving buzzes: not while the assistant is still looking at it; the same card handed to the user
    /// does, once.
    static func announces(_ ix: Interaction, replacing old: Interaction?) -> Bool {
        !ix.isDelegating && (old == nil || old?.isDelegating == true)
    }

    /// The context the assistant filled in, one line per field it gave, always in the same order; the deadline as a date.
    static func lines(_ d: Delegate?) -> [DelegateLine] {
        guard let d else { return [] }
        return DelegateLine.Field.allCases.compactMap { field in
            let text: String
            switch field {
            case .what: text = d.what ?? ""
            case .whyNow: text = d.whyNow ?? ""
            case .recommend: text = d.recommend ?? ""
            case .cost: text = d.cost ?? ""
            case .reason: text = d.reason ?? ""
            case .deadline: text = d.deadline.flatMap { $0.isMeaningful ? $0.formatted(date: .abbreviated, time: .shortened) : nil } ?? ""
            }
            let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
            return trimmed.isEmpty ? nil : DelegateLine(field: field, label: label(field), text: trimmed)
        }
    }

    static func label(_ field: DelegateLine.Field) -> String {
        switch field {
        case .what: String(localized: "What", comment: "The context the assistant filled in on a question: what it is about")
        case .whyNow: String(localized: "Why now", comment: "The context the assistant filled in on a question: why the agent asks now")
        case .recommend: String(localized: "Recommended")
        case .cost: String(localized: "If not", comment: "The context the assistant filled in on a question: what not taking its recommendation costs")
        case .reason: String(localized: "Why you decide", comment: "The context the assistant filled in on a question: why it is the user's to answer")
        case .deadline: String(localized: "Deadline", comment: "The context the assistant filled in on a question: answer by when")
        }
    }

    /// The card's context lines and, per question, the options the assistant recommends: a recommendation that is the
    /// options' own words shows on them, not again in the context.
    static func context(_ ix: Interaction) -> (lines: [DelegateLine], picks: [[String]]) {
        let questions = ix.questions ?? []
        let picks = recommendedLabels(questions, ix.delegate?.recommend)
        let onOptions = !picks.isEmpty && picks.allSatisfy { !$0.isEmpty }
        return (lines(ix.delegate).filter { !($0.field == .recommend && onOptions) }, picks)
    }

    /// The option labels `answer` picks on `q`: the label itself, or for a multi-select every one of its ", "-joined
    /// labels; [] for no answer, nil when it is words of its own (or names something that is no option).
    static func labels(of answer: String, on q: Question) -> [String]? {
        let a = answer.trimmingCharacters(in: .whitespacesAndNewlines)
        if a.isEmpty { return [] }
        let labels = q.options.map(\.label)
        if labels.contains(a) { return [a] }
        guard q.multiSelect else { return nil }
        let parts = a.components(separatedBy: picksSeparator).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        return !parts.isEmpty && parts.allSatisfy(labels.contains) ? parts : nil
    }

    /// For each question, the options the assistant recommends, when its recommendation is the options' own words; none
    /// when it said something else (it may recommend not answering at all).
    static func recommendedLabels(_ questions: [Question], _ recommend: String?) -> [[String]] {
        let r = (recommend ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        let parts = questions.count > 1 ? r.components(separatedBy: questionsSeparator) : [r]
        let fits = parts.count == questions.count
        return questions.enumerated().map { i, q in fits ? (labels(of: parts[i], on: q) ?? []) : [] }
    }

    /// Change Answer's starting point: the answers given so far picked where they are options, typed out where not.
    static func initialPicks(_ questions: [Question], _ answers: [String: String]) -> (picked: [Int: [String]], custom: [Int: String]) {
        var picked: [Int: [String]] = [:]
        var custom: [Int: String] = [:]
        for (i, q) in questions.enumerated() {
            let a = (answers[q.question] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            let labels = labels(of: a, on: q)
            picked[i] = labels ?? []
            if labels == nil, !a.isEmpty { custom[i] = a }
        }
        return (picked, custom)
    }

    /// The form's answers, by question text, as a card sends them: the typed answer, else the picks joined by ", ".
    static func formAnswers(_ questions: [Question], picked: [Int: [String]], custom: [Int: String]) -> [String: String] {
        var out: [String: String] = [:]
        for (i, q) in questions.enumerated() {
            let typed = (custom[i] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            out[q.question] = typed.isEmpty ? (picked[i] ?? []).joined(separator: picksSeparator) : typed
        }
        return out
    }

    /// Whether two answers to `q` say the same: the same words, or a multi-select's same options picked in any order (an
    /// answer in words of its own is compared as written).
    static func sameAnswer(_ q: Question?, _ a: String, _ b: String) -> Bool {
        let x = a.trimmingCharacters(in: .whitespacesAndNewlines), y = b.trimmingCharacters(in: .whitespacesAndNewlines)
        if x == y { return true }
        guard let q, q.multiSelect, let l = labels(of: x, on: q), let r = labels(of: y, on: q), !l.isEmpty, !r.isEmpty else { return false }
        return l.sorted() == r.sorted()
    }

    /// The answers in `after` that say something other than `before`: only those go to the agent (an unchanged one
    /// would only repeat what it already went on with).
    static func changedAnswers(before: [String: String], after: [String: String], questions: [Question] = []) -> [String: String] {
        var out: [String: String] = [:]
        for (question, answer) in after {
            let now = answer.trimmingCharacters(in: .whitespacesAndNewlines)
            if !now.isEmpty, !sameAnswer(questions.first { $0.question == question }, now, before[question] ?? "") { out[question] = now }
        }
        return out
    }

    /// What the answers are now: the assistant's, with what the user changed since.
    static func latest(_ d: DelegatedAnswer, _ changed: [String: String]?) -> [String: String] {
        d.answers.merging(changed ?? [:]) { _, new in new }
    }

    /// A question's answers as sent on the wire: only the text ones.
    static func answers(in v: JSONValue?) -> [String: String] {
        v?.object?.compactMapValues { if case .string(let s) = $0 { s } else { nil } } ?? [:]
    }

    /// The questions the history says the assistant answered, newest first.
    static func history(_ records: [InteractionRecord]) -> [DelegatedAnswer] {
        records.filter { $0.status == "resolved" && $0.interaction.kind == .question && $0.resolution?.answeredBy == AssistantAnswer.assistant }
            .map { r in
                DelegatedAnswer(interaction: r.interaction, answers: r.resolution?.answers ?? [:], reason: r.resolution?.reason ?? "",
                                at: r.resolvedAt.flatMap { $0.isMeaningful ? $0 : nil })
            }
            .sorted { ($0.at ?? .distantPast) > ($1.at ?? .distantPast) }
    }

    /// Why a change of answer did not go, by the hub's status (docs/protocol/protocol.md POST …/reanswer); a hub without
    /// the route answers a bare 404.
    static func reanswerError(_ error: Error) -> String {
        if let e = error as? APIError {
            switch e.status {
            case 403: return String(localized: "This device can only answer with a card's own choices, so it can't change answers")
            case 409: return String(localized: "The assistant didn't answer this question, so its answer can't be changed here")
            case 400: return String(localized: "Choose an answer first")
            case 502: return String(localized: "Couldn't send the new answer to the agent: \(e.message)")
            case 503 where e.code == "assistant_offline": return String(localized: "The assistant is offline. Try again shortly")
            case 404 where e.code == nil: return String(localized: "This hub can't change answers yet. Update the hub first")
            default: break
            }
        }
        return String(localized: "Couldn't change the answer: \(error.localizedDescription)")
    }
}
