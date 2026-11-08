import SwiftUI

/// The row of a herdr tab holding several agents (#2320), over their rows in the Agents list: its most urgent agent's
/// state, the tab's name and what that agent is doing, 「N 个 Agent」 and the fold's chevron. Tapping it folds the tab's
/// agents away (kept per tab, `TabPanes.foldID`).
struct TabGroupRow: View {
    var group: TabPanes.Group
    var folded: Bool
    @Environment(\.compactList) private var compact

    var body: some View {
        let lead = group.lead
        HStack(spacing: compact ? CompactList.gap : Theme.Space.s) {
            StatusDot(state: lead.state, size: Theme.Size.dot, waiting: lead.waitsInBackground)
                .scaledFrame(width: compact ? CompactList.mark : Theme.Size.iconXS)
            HStack(alignment: .firstTextBaseline, spacing: compact ? CompactList.gap : Theme.Space.sm) {
                Text(name)
                    .font(compact ? CompactList.metaStrong : Theme.Font.secondaryMedium)
                    .foregroundStyle(Color.primary)
                    .lineLimit(1)
                    .layoutPriority(1)
                Text(lead.displayTitle)
                    .font(compact ? CompactList.meta : Theme.Font.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: Theme.Space.xs)
            Text(count)
                .font(compact ? CompactList.meta : Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .fixedSize()
            FoldChevron(folded: folded)
        }
        .padding(.vertical, compact ? 0 : Theme.Space.xxs)
        .contentShape(.rect)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([String(localized: "Tab \(name)"), count, lead.stateLabel(muted: AppModel.shared.isMuted(lead)), folded ? String(localized: "Collapsed [Readiness Card ee6046]") : String(localized: "Expanded")].joined(separator: String(localized: ", ")))
        .accessibilityHint(folded ? String(localized: "Show This Tab's Agents") : String(localized: "Collapse"))
        .accessibilityAddTraits(.isButton)
    }

    private var name: String { group.label ?? String(localized: "Tab") }
    private var count: String { String(localized: "\(group.sessions.count) agents") }
}
