import SwiftUI
import UIKit
import WidgetKit

/// The Milk Frog on the watch face (docs/apple/persona.md "The Milk Frog on the watch"): the face of the state the
/// watch app last saw, a widget of `PersonaWatchWidgetsBundle`. Still by design; the frog moves only in the app.
struct PetComplication: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: WatchPetSnapshot.widgetKind, provider: PetProvider()) { entry in
            PetComplicationFamilyView(entry: entry).containerBackground(WatchFaceTokens.background, for: .widget)
        }
        .configurationDisplayName(Text(verbatim: "Persona"))
        .supportedFamilies([.accessoryCircular, .accessoryCorner, .accessoryRectangular, .accessoryInline])
    }
}

/// The snapshot as it stands now, then once more when it goes stale; the app reloads the timeline when it changes.
struct PetProvider: TimelineProvider {
    func placeholder(in context: Context) -> PetEntry { PetEntry(date: .now, snapshot: nil) }

    func getSnapshot(in context: Context, completion: @escaping (PetEntry) -> Void) {
        completion(PetEntry(date: .now, snapshot: WatchPetSnapshot.read()))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<PetEntry>) -> Void) {
        let snapshot = WatchPetSnapshot.read()
        var entries = [PetEntry(date: .now, snapshot: snapshot)]
        if let snapshot, snapshot.staleDate > .now { entries.append(PetEntry(date: snapshot.staleDate, snapshot: snapshot)) }
        completion(Timeline(entries: entries, policy: .never))
    }
}

/// The face as WidgetKit sizes it, in the family it asks for.
struct PetComplicationFamilyView: View {
    @Environment(\.widgetFamily) private var family
    let entry: PetEntry

    var body: some View { PetComplicationView(entry: entry, family: family) }
}
