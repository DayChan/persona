import SwiftUI

/// Saved Places' state and what the page does with the hub (#1598): the places the user names (home, the office) that
/// reminders by place go off at. The view draws what is here and calls its methods, so tests run it against a `FakeHub`.
@Observable final class SavedPlacesModel {
    typealias Deps = AssistantDeps
    typealias Item = APIClient.SavedPlace

    private(set) var places: [Item] = []
    private(set) var loaded = false
    /// Why the list could not be read; the page shows it only when it has none to show.
    private(set) var error: String?
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    func load() async {
        guard let api = deps.api() else { return }
        do {
            places = try await api.savedPlaces()
            error = nil
        } catch {
            self.error = error.shown ?? self.error
        }
        loaded = true
    }

    /// Adds `p` (no id yet) or saves the changes to it; returns it as saved, nil when the hub refused (a name another
    /// place has, a device that may only look) after reporting why.
    func save(_ p: Item) async -> Item? {
        guard let api = deps.api() else { return nil }
        var p = p
        p.name = p.name.trimmingCharacters(in: .whitespacesAndNewlines)
        p.address = p.address.trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            let saved = try await api.savePlace(p)
            withAnimation(Theme.Motion.standard) {
                if let i = places.firstIndex(where: { $0.id == saved.id }) { places[i] = saved } else { places.append(saved) }
            }
            return saved
        } catch {
            deps.report(error)
            return nil
        }
    }

    /// Optimistic: the row goes at once; a failure reports and reloads what the hub has.
    func delete(_ p: Item) async {
        guard let api = deps.api(), !p.id.isEmpty else { return }
        withAnimation(Theme.Motion.standard) { places.removeAll { $0.id == p.id } }
        do {
            try await api.deletePlace(p.id)
            deps.toast(Toast(String(localized: "Deleted “\(p.name)”"), style: .success))
        } catch {
            deps.report(error)
            if let fresh = try? await api.savedPlaces() { places = fresh }
        }
    }
}

/// What the place editor decides without the hub or MapKit: when it may save, the radius choices and how a place is
/// described under its name.
enum SavedPlaceText {
    /// The radius choices in the editor, in meters: the reminders' default first among the small ones.
    static let radii = [100, 150, 300, 500, 1000, 2000]

    /// A name and a location: what the hub needs.
    static func canSave(_ p: APIClient.SavedPlace, located: Bool) -> Bool {
        located && !p.name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// The choices with `current` among them (a radius the assistant saved that is not one of the presets).
    static func radii(with current: Int) -> [Int] {
        radii.contains(current) ? radii : (radii + [current]).sorted()
    }

    /// "150 m", "1 km": the radius as the user's locale writes a short distance.
    static func radius(_ meters: Int) -> String {
        Measurement(value: Double(meters), unit: UnitLength.meters)
            .formatted(.measurement(width: .abbreviated, usage: .road))
    }

    /// The line under a place's name: its address, else its coordinates.
    static func detail(_ p: APIClient.SavedPlace) -> String {
        p.address.isEmpty ? coordinates(p) : p.address
    }

    /// "31.23040, 121.47370".
    static func coordinates(_ p: APIClient.SavedPlace) -> String {
        "\(p.latitude.formatted(.number.precision(.fractionLength(5)))), \(p.longitude.formatted(.number.precision(.fractionLength(5))))"
    }
}
