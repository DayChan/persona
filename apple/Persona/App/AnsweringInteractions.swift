import Foundation

/// The cards being answered right now (`AppModel.resolve`), and which of them the hub ended while the answer was on
/// its way (#3922). A screen card whose dialog changed (409 `prompt_changed`) or whose keys did not reach the computer
/// is over on the hub: it says `interaction_resolved` before its error comes back, and a card for what is on the screen
/// now follows. Put back after that event, the old card stayed for good. A Codex card the node could not answer stays
/// pending on the hub, without that event, so it goes back.
nonisolated struct AnsweringInteractions {
    private var inFlight: Set<String> = []
    private var endedByHub: Set<String> = []

    mutating func begin(_ uid: String) {
        inFlight.insert(uid)
        endedByHub.remove(uid)
    }

    /// The hub said this card is over.
    mutating func hubEnded(_ uid: String) {
        if inFlight.contains(uid) { endedByHub.insert(uid) }
    }

    /// This card's answer is on its way.
    func contains(_ uid: String) -> Bool { inFlight.contains(uid) }

    /// Whether a failed answer puts the card back: not when the hub ended it meanwhile.
    func putsBack(_ uid: String) -> Bool { !endedByHub.contains(uid) }

    mutating func finish(_ uid: String) {
        inFlight.remove(uid)
        endedByHub.remove(uid)
    }
}
