import SwiftUI

/// Compact List's one scale (docs/apple/ios.md「Agents 列表」): every row in it — an agent, "N more idle", Collapse, a quiet space —
/// is one line of the same height with its mark (state dot, fold chevron, space icon) in one column, so the titles
/// start at one edge; three levels of type: section titles, row titles, meta. All Dynamic Type levels, so the three
/// text sizes (Small / Standard / Large) keep the ratios; read each time, since the Mac's move with `Theme.Font.scale`. The cards (Cards) do not use it.
enum CompactList {
    /// A space's name, and the list's other section titles (Needs You, Idle Spaces, a state, a machine).
    static var sectionTitle: Font { Theme.Font.heading }
    /// What a row is: an agent's title, a quiet space's name, "N more idle".
    static var title: Font { Theme.Font.secondary }
    /// A finished-unseen agent's title.
    static var titleUnread: Font { Theme.Font.secondaryStrong }
    /// Where and when, the tab names behind "N more idle", a header's counts.
    static var meta: Font { Theme.Font.caption }
    static var metaStrong: Font { Theme.Font.captionMedium }
    /// The column the marks sit in (`scaledFrame`: it grows with the text).
    static let mark = Theme.Size.markM
    /// Between the mark and the title, and between the things on a line.
    static let gap = Theme.Space.s
    /// Every row: the same height whatever it holds.
    static let rowInsets = EdgeInsets(top: Theme.Space.s, leading: Theme.Space.l, bottom: Theme.Space.s, trailing: Theme.Space.m)
    /// A row a level in, under a working tree's row (By Repository).
    static let nestedRowInsets = rowInsets(level: 1)
    /// A row `level` levels in: under a working tree's row, under a tab's starter (#2320), or both.
    static func rowInsets(level: Int) -> EdgeInsets {
        EdgeInsets(top: Theme.Space.s, leading: Theme.Space.l + Theme.Space.l * CGFloat(level), bottom: Theme.Space.s, trailing: Theme.Space.m)
    }
}

extension EnvironmentValues {
    /// The Agents list shows Compact List: headers and fold rows take `CompactList`'s scale.
    @Entry var compactList = false
}

/// Compact List: the idle agents of a busy space behind one line, the size of an agent's — their tab names as words, not chips.
struct CompactFoldRow: View {
    var sessions: [Session]

    var body: some View {
        HStack(spacing: CompactList.gap) {
            ThemedIcon(Theme.Icon.expand)
                .font(CompactList.metaStrong)
                .foregroundStyle(.secondary)
                .scaledFrame(width: CompactList.mark)
            Text("\(sessions.count) more idle")
                .font(CompactList.title)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .fixedSize()
            Text(sessions.prefix(6).map { $0.slotLabel ?? $0.displayTitle }.joined(separator: " · "))
                .font(CompactList.meta)
                .foregroundStyle(.tertiary)
                .lineLimit(1)
            Spacer(minLength: 0)
        }
        .contentShape(.rect)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Show \(sessions.count) idle agents")
    }
}

/// Compact List: Collapse under the idle agents it opened, laid out like the line that opened them.
struct CompactCollapseRow: View {
    var title: String

    var body: some View {
        HStack(spacing: CompactList.gap) {
            ThemedIcon(Theme.Icon.collapse)
                .font(CompactList.metaStrong)
                .scaledFrame(width: CompactList.mark)
            Text(title).font(CompactList.title).lineLimit(1)
            Spacer(minLength: 0)
        }
        .foregroundStyle(Theme.accent)
        .contentShape(.rect)
    }
}

/// Compact List: a space where every agent is idle, one line like an agent's: its name, how many tabs, when; tap to list them.
struct CompactQuietSpaceRow: View {
    var group: SpaceGroup
    var machine: Machine?
    var showMachine: Bool
    var expanded: Bool
    /// A repository (By Repository's Idle Repositories), not a space: its folder.
    var repo = false
    @Environment(AgentsFeed.self) private var feed: AgentsFeed?

    var body: some View {
        let at = Fmt.relative(group.updatedAt(feed))
        HStack(spacing: CompactList.gap) {
            ThemedIcon(repo ? Theme.Icon.folder : Theme.Icon.space)
                .font(CompactList.meta)
                .foregroundStyle(.secondary)
                .scaledFrame(width: CompactList.mark)
            HStack(alignment: .firstTextBaseline, spacing: CompactList.gap) {
                Text(group.name).font(CompactList.title).lineLimit(1).layoutPriority(1)
                Text([showMachine ? machine?.name : nil, count].compactMap { $0 }.joined(separator: " · "))
                    .font(CompactList.meta).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: Theme.Space.xs)
            Text(at).font(CompactList.meta.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                .fixedSize()
            FoldChevron(folded: !expanded)
        }
        .contentShape(.rect)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([group.name, expanded ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]"), showMachine ? machine?.name ?? "" : "",
                             count, at].filter { !$0.isEmpty }.joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
        .accessibilityAddTraits(.isButton)
    }

    /// Agents counted, not tabs: a tab may hold several (#2320).
    private var count: String { String(localized: "\(group.sessions.count) agents") }
}

/// Compact List: something waiting for you (a plan, a question, a permission) as one row like an agent's: its kind's mark in
/// Needs You red, what it is, which agent and machine, when. Tapping opens the chat, which answers it.
struct CompactInteractionRow: View {
    var interaction: Interaction
    var machine: String?

    var body: some View {
        HStack(spacing: CompactList.gap) {
            ThemedIcon(interaction.kind.symbol)
                .font(CompactList.metaStrong)
                .foregroundStyle(StatusPalette.needsYou.dot)
                .scaledFrame(width: CompactList.mark)
            HStack(alignment: .firstTextBaseline, spacing: CompactList.gap) {
                Text(interaction.title).font(CompactList.titleUnread).lineLimit(1).fixedSize()
                Text(interaction.placeLine(machine: machine))
                    .font(CompactList.meta).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: Theme.Space.xs)
            VisibleTimelineView(interval: 30) { now in
                Text(Fmt.relative(interaction.createdAt, now: now)).font(CompactList.meta.monospacedDigit())
                    .foregroundStyle(.secondary).lineLimit(1)
            }
            .fixedSize()
        }
        .accessibilityElement(children: .combine)
    }
}
