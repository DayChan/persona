import SwiftUI
import UIKit
import WidgetKit

/// What a complication draws; the watch app compiles it too, for its debug preview of the faces.
struct PetEntry: TimelineEntry {
    let date: Date
    let snapshot: WatchPetSnapshot?
}

struct PetComplicationView: View {
    let entry: PetEntry
    let family: WidgetFamily

    var body: some View {
        let shown = entry.snapshot?.shown(at: entry.date)
        switch family {
        case .accessoryRectangular:
            HStack {
                face(shown?.state).frame(maxHeight: .infinity)
                VStack(alignment: .leading) {
                    Text(verbatim: shown?.title ?? "Persona").font(WatchFaceTokens.title).lineLimit(2)
                        .minimumScaleFactor(WatchFaceTokens.titleMinScale)
                    if shown?.stale == true, let at = entry.snapshot?.at {
                        Text(at, style: .relative).font(WatchFaceTokens.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: 0)
            }
        case .accessoryInline:
            Text(verbatim: shown?.title ?? "Persona")
        default:
            face(shown?.state)
        }
    }

    /// The round face for the state; a plain disc before the app has written one (no frog chosen, or not fetched yet).
    @ViewBuilder private func face(_ state: String?) -> some View {
        if let state, let url = WatchPetSnapshot.directory?.appending(path: WatchPetSnapshot.faceName(state)),
           let image = UIImage(contentsOfFile: url.path) {
            Image(uiImage: image).resizable().scaledToFit().clipShape(Circle())
        } else {
            Circle().fill(WatchFaceTokens.placeholder)
        }
    }
}

#if DEBUG
/// `-openWatchItem complication` in the watch app: every family at a 46 mm face's sizes, fresh and stale, on a mock
/// face's grey, since a simulator cannot put a complication on a face from the command line (screenshots).
struct PetComplicationPreview: View {
    var body: some View {
        let fresh = PetEntry(date: .now, snapshot: WatchPetSnapshot.read())
        let stale = PetEntry(date: fresh.snapshot?.staleDate ?? .now, snapshot: fresh.snapshot)
        let size = WatchFaceTokens.previewRectangular
        ScrollView {
            VStack(alignment: .leading, spacing: WatchFaceTokens.previewSpacing) {
                ForEach([fresh, stale], id: \.date) { entry in
                    HStack(spacing: WatchFaceTokens.previewSpacing) {
                        PetComplicationView(entry: entry, family: .accessoryCircular)
                            .frame(width: WatchFaceTokens.previewCircular, height: WatchFaceTokens.previewCircular)
                        PetComplicationView(entry: entry, family: .accessoryCorner)
                            .frame(width: WatchFaceTokens.previewCorner, height: WatchFaceTokens.previewCorner)
                        PetComplicationView(entry: entry, family: .accessoryInline).font(WatchFaceTokens.previewInline).lineLimit(1)
                    }
                    PetComplicationView(entry: entry, family: .accessoryRectangular).frame(width: size.width, height: size.height)
                        .background(WatchFaceTokens.previewWash, in: RoundedRectangle(cornerRadius: WatchFaceTokens.previewRadius))
                }
            }
        }
    }
}
#endif
