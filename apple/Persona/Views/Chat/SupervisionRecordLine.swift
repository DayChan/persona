import SwiftUI

/// A quiet round keeps its full transcript, but occupies only one muted line until requested.
struct SupervisionRecordLine: View {
    var live: Bool
    var open: Bool
    var toggle: () -> Void

    var body: some View {
        Button(action: toggle) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.disclosure)
                    .rotationEffect(.degrees(open ? 90 : 0))
                    .accessibilityHidden(true)
                Text(live ? String(localized: "Supervision record · running") : String(localized: "Supervision record"))
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
            .font(Theme.Font.caption)
            .foregroundStyle(.secondary)
            .padding(.vertical, Theme.Space.sm)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("supervision-record")
        .accessibilityValue(open ? String(localized: "Expanded") : String(localized: "Collapsed"))
    }
}

/// Unlike an injected hint, the opened audit trail exposes the original model input verbatim.
struct SupervisionInputLine: View {
    var item: Item
    @Environment(\.loadFullText) private var loadFullText
    @State private var full: String?
    @State private var showFull = false

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Text("Supervision input")
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
            Text(full ?? item.cutText)
                .font(Theme.Font.secondary)
                .foregroundStyle(.primary)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
            if item.fullSize != nil, full == nil {
                Button("Show Full Text") { showFull = true }
                    .font(Theme.Font.caption)
                    .lineLimit(1)
                    .sheet(isPresented: $showFull) { FullOutputSheet(itemID: item.id, load: loadFullText) }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .chatBoxed(Theme.ChatBox.persona)
        .accessibilityIdentifier("supervision-input")
        .task(id: item.id) {
            if item.fullSize != nil, let loadFullText { full = try? await loadFullText(item.id) }
        }
    }
}
