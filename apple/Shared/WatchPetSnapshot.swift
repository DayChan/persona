import Foundation

/// What the watch app last knew about the Milk Frog, for the watch face (docs/apple/persona.md "The Milk Frog on the
/// watch"). The app writes it into the App Group container with the faces beside it; the complication only reads it, so
/// it needs no network, credentials or strings of its own.
nonisolated struct WatchPetSnapshot: Codable, Equatable, Sendable {
    /// A `WatchPetState` raw value.
    var state: String
    /// The state as the app words it, in the phone's language.
    var title: String
    /// What the face says once the snapshot is too old to stand for the agents.
    var staleTitle: String
    /// When the data behind it is from (the watch's last refresh), not when it was written.
    var at: Date

    /// Past this, nobody knows how the agents are: the face shows the frog in the rain instead of an old calm.
    static let staleAfter: TimeInterval = 60 * 60
    static let offlineState = "offline"
    static let fileName = "pet.json"
    /// The complication's WidgetKit kind: the watch app reloads only its timelines, not the extension widgets'.
    static let widgetKind = "pet"
    /// Info.plist key of the App Group both bundles share: the watch-only group (`PERSONA_WATCH_APP_GROUP`).
    static let groupKey = "PersonaAppGroup"

    var staleDate: Date { at.addingTimeInterval(Self.staleAfter) }

    /// The face's state and words at `date`.
    func shown(at date: Date) -> (state: String, title: String, stale: Bool) {
        date >= staleDate ? (Self.offlineState, staleTitle, true) : (state, title, false)
    }

    /// The face picture's file name for a state, beside the snapshot.
    static func faceName(_ state: String) -> String { "face-\(state).png" }

    /// `Pet/` in the App Group container; nil without the group (an unsigned build).
    static var directory: URL? {
        guard let group = Bundle.main.object(forInfoDictionaryKey: groupKey) as? String, !group.isEmpty,
              let root = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else { return nil }
        return root.appending(path: "Pet", directoryHint: .isDirectory)
    }

    static func read() -> WatchPetSnapshot? {
        guard let url = directory?.appending(path: fileName), let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(WatchPetSnapshot.self, from: data)
    }
}
