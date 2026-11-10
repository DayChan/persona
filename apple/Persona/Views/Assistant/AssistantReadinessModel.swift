import Foundation
import Observation

/// The main conversation's readiness on this device (docs/assistant/onboarding.md 4.4, 5.1): asked when the Persona page shows,
/// after pairing, after a computer is added or confirmed, after a provider is saved, when the machines change; kept
/// from `warm` too. On a hub without `assistant_readiness` (or one answering 404) it stays nil and every page behaves
/// as before. Also holds the card of a start or a send the hub refused with a code (5.2).
@MainActor @Observable
final class AssistantReadinessModel {
    private(set) var readiness: AssistantReadiness?
    /// The hub answered 404: an older hub, the pages as before.
    private(set) var unsupported = false
    private(set) var probing = false
    /// The last Try a Message's result (`probe`), until the next ask without one.
    private(set) var probe: AssistantReadiness.Probe?
    /// A try that could not get a result (timeout, offline, hub failure), shown instead of an older result.
    private(set) var probeError: String?
    /// The checklist step that started this try; its feedback stays beside that row's button.
    private(set) var probeStep: String?
    /// A coded refusal of a start or a send: a card where the send was, not a toast.
    var failure: CodedFailure?

    @ObservationIgnored private var at = Date.distantPast
    @ObservationIgnored private var asked = 0
    @ObservationIgnored private var answered = 0
    @ObservationIgnored private var latestProbe = 0
    @ObservationIgnored private var inflight: Task<Void, Never>?
    /// Moves on each `forget`: an answer, a probe's too, about the manager shown before doesn't land.
    @ObservationIgnored private var generation = 0

    /// How long an answer is good for before a visit asks again.
    static let fresh: TimeInterval = 5

    /// The readiness the pages draw: nil on an older hub and before the first answer.
    var current: AssistantReadiness? { unsupported ? nil : readiness }

    /// What holds the send button (nil: send as before).
    var blocking: AssistantReadiness.Step? { current?.blockingStep }

    /// An answer from elsewhere (`warm`): kept unless a later ask has answered. A new answer also retires a failed try.
    func land(_ r: AssistantReadiness) {
        readiness = r
        unsupported = false
        probeError = nil
        at = .now
    }

    /// Asks the hub. `force`: even with a fresh answer; `probe`: try one word on the main conversation (costs a little).
    /// A later ordinary answer is never overwritten by an earlier one; only the latest probe may land or end `probing`.
    /// `probeStep` is the checklist row that owns this try's spinner and result.
    func refresh(api: APIClient?, force: Bool = false, probe: Bool = false, probeStep: String? = nil) async {
        guard let api else { return }
        if !probe, !force, Date.now.timeIntervalSince(at) < Self.fresh { return }
        if !probe, !force, let inflight { await inflight.value; return }
        asked += 1
        let mine = asked
        let gen = generation
        if probe {
            probing = true
            latestProbe = mine
            self.probeStep = probeStep
            self.probe = nil // while this try runs, an earlier result or failure cannot stand in for it
            self.probeError = nil
        }
        let task = Task { [weak self] in
            do {
                let r = try await api.assistantReadiness(probe: probe)
                guard let self, gen == self.generation, (probe ? mine == self.latestProbe : mine > self.answered) else { return }
                self.answered = max(self.answered, mine)
                self.land(r)
                if probe { self.probe = r.probe } else if r.probe == nil { self.probe = nil }
            } catch {
                guard let self, gen == self.generation, (probe ? mine == self.latestProbe : mine > self.answered) else { return }
                if error.isCancellation { return } // a page exit or a replaced request is not the user's failure
                if probe, (error as? APIError)?.status != 404 {
                    // A try has no answer to keep quiet about: show a timeout / offline / hub failure, never an older result.
                    self.answered = max(self.answered, mine)
                    self.probe = nil
                    self.probeError = error.localizedDescription
                    return
                }
                // 404: an older hub. 503 assistant_offline and the rest of an ordinary read: keep what was there.
                guard (error as? APIError)?.status == 404 else { return }
                self.answered = max(self.answered, mine)
                self.unsupported = true
                self.readiness = nil
                self.probe = nil
                self.probeError = nil
                self.probeStep = nil
                self.at = .now
            }
        }
        if !probe { inflight = task }
        await task.value
        if probe, gen == generation, mine == latestProbe { probing = false }
        if inflight == task { inflight = nil }
    }

    /// Later: the checklist stops coming up by itself; the card still shows when something is missing.
    func dismiss(api: APIClient?) async {
        guard let api else { return }
        try? await api.dismissAssistantReadiness()
        readiness?.dismissed = true
    }

    /// The Persona page shows another manager: what was read of the one before, and an answer about it still on its way,
    /// are not this one's.
    func forget() {
        generation += 1
        answered = asked
        inflight = nil
        probing = false
        reset()
    }

    /// For tests and screenshots.
    func reset(_ r: AssistantReadiness? = nil) {
        readiness = r
        unsupported = false
        probe = nil
        probeError = nil
        probeStep = nil
        failure = nil
        at = r == nil ? .distantPast : .now
    }
}
