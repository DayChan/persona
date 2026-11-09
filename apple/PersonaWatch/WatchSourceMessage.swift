import SwiftUI

/// The watch keeps externally delivered model input on the source side, never in the user's bubble.
struct WatchSourceMessage: View {
    var message: WatchMessage
    var source: MessageSource
    @State private var open = false

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text(verbatim: source.mention)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(Theme.accent)
                .lineLimit(1)
            Text(message.text)
                .font(Theme.Font.secondary)
                .lineLimit(open ? nil : Theme.MessageSource.previewLines)
            if message.text.count > Theme.MessageSource.foldCharacters || message.text.split(separator: "\n").count > Theme.MessageSource.previewLines {
                Button(open ? String(localized: "Collapse") : String(localized: "Expand")) { open.toggle() }
                    .font(Theme.Font.captionStrong)
                    .lineLimit(1)
            }
        }
        .padding(Theme.Watch.bubblePadding)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control))
        .accessibilityIdentifier("watch-message-source")
    }
}
