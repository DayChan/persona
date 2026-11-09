import Foundation

/// The Type to Persona control's one-shot ask (`AppModel.openTypingConversation`): the composer it names puts the keyboard up once,
/// and nothing focuses after it. Each press counts (`serial`), so a second press while the app is open asks again.
/// An ask no composer took within `lifetime` is stale (the app opened somewhere else, the conversation never loaded): it goes, so
/// opening that conversation minutes later does not raise the keyboard by itself.
struct TypingRequest: Equatable {
    /// Which composer the ask is for: the main conversation's, or the new conversation page's when there is no main conversation yet.
    enum Target: Hashable, Sendable {
        case conversation(SessionKey)
        case newConversation
    }

    /// The app opening from the Lock Screen, Face ID done, and the page drawn, with room to spare on a busy device.
    static let lifetime: TimeInterval = 30

    private(set) var serial = 0
    private(set) var target: Target?
    private var taken = 0
    private var asked = Date.distantPast

    mutating func ask(_ target: Target, now: Date = .now) {
        serial += 1
        self.target = target
        asked = now
    }

    /// What the composer for `target` does with the ask when its page is in front: whether to focus its field now. While the app
    /// is not active (a cold start from the Lock Screen has it launching) the ask waits, unspent; another composer leaves it alone.
    /// Once taken it is spent, fresh or not.
    mutating func take(for target: Target, appActive: Bool, now: Date = .now) -> Bool {
        guard appActive, serial > taken, self.target == target else { return false }
        taken = serial
        return now.timeIntervalSince(asked) <= Self.lifetime
    }

    /// The composer that took ask `serial` stopped before its field held focus (the app went inactive, the page went away): the
    /// ask is open again for the next time that composer is in front, unless a newer press replaced it or it went stale.
    mutating func giveBack(_ serial: Int) {
        guard serial == self.serial, taken == serial else { return }
        taken = serial - 1
    }
}
