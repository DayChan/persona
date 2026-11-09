import SwiftUI

/// A machine's heading where the Agents list is split by machine (By Repository, By Project with agents on more than one machine,
/// #2108): its mark in a tile and its name a level above the headers under it, its agents' state counts on the right.
struct HostHeading: View {
    var symbol: String
    var title: String
    var group: SpaceGroup
    @Environment(\.compactList) private var compact

    var body: some View {
        CountsHeading {
            ThemedIcon(symbol)
                .font(Theme.Font.secondaryStrong)
                .foregroundStyle(Color.primary)
                .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                .background(Theme.Fill.chip, in: .rect(cornerRadius: Theme.Radius.badge))
            // Compact List's spaces and repositories are titled at the heading's size: the machine goes a level up.
            Text(title).font(compact ? Theme.Font.title : Theme.Font.heading).foregroundStyle(Color.primary).textCase(nil).lineLimit(1)
        } counts: {
            StateCounts(group: group)
        }
    }
}

/// A working tree's row in its repository's card (By Repository): ⌂ for the main checkout, the branch mark for the others, how
/// many agents (Idle when nothing goes on), and the chevron that turns as it opens.
struct WorktreeRow: View {
    var name: String
    var main: Bool
    var count: Int
    var quiet: Bool
    var open: Bool
    @Environment(\.compactList) private var compact

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(main ? Theme.Icon.home : Theme.Icon.gitBranch)
                .font(compact ? CompactList.meta : Theme.Font.caption)
                .foregroundStyle(.secondary)
                .scaledFrame(width: compact ? CompactList.mark : Theme.Size.iconXS)
            Text(name)
                .font(compact ? CompactList.metaStrong : Theme.Font.secondaryMedium)
                .foregroundStyle(quiet ? Color.secondary : Color.primary)
                .lineLimit(1)
            Spacer(minLength: Theme.Space.xs)
            Text(quiet ? String(localized: "Idle") : String(localized: "\(count) agents"))
                .font(compact ? CompactList.meta : Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .fixedSize()
            FoldChevron(folded: !open)
        }
        .padding(.vertical, compact ? 0 : Theme.Space.xxs)
        .contentShape(.rect)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([main ? String(localized: "Main worktree") : String(localized: "Worktree"), name,
                             quiet ? String(localized: "Idle") : String(localized: "\(count) agents"),
                             open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]")]
            .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
        .accessibilityAddTraits(.isButton)
    }
}

/// The "N items" row under a working tree (By Repository): the spaces its host nests under it (Orca's child worktrees, #3075),
/// their agents' state counts, and the chevron that turns as it opens.
struct ChildSpacesRow: View {
    var count: Int
    var open: Bool
    /// Everything nested under it, for the counts.
    var group: SpaceGroup
    @Environment(\.compactList) private var compact

    var body: some View {
        CountsHeading {
            ThemedIcon(Theme.Icon.childSpaces)
                .font(compact ? CompactList.meta : Theme.Font.caption)
                .foregroundStyle(.secondary)
                .scaledFrame(width: compact ? CompactList.mark : Theme.Size.iconXS)
            Text("\(count) items [Repo Tree Rows 8af700]")
                .font(compact ? CompactList.metaStrong : Theme.Font.secondaryMedium)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .fixedSize()
        } counts: {
            StateCounts(group: group)
        } trailing: {
            FoldChevron(folded: !open)
        }
        .padding(.vertical, compact ? 0 : Theme.Space.xxs)
        .contentShape(.rect)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([String(localized: "\(count) items [Repo Tree Rows 8af700]"), StateCounts.spoken(group), open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]")].filter { !$0.isEmpty }
            .joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label")))
        .accessibilityAddTraits(.isButton)
    }
}

/// The ⋯ of a repository whose agents are in more than one space (By Repository): what applies to all of them. Equal while
/// its unread agents are: the menu held open is not rebuilt by every push.
struct RepoMenuItems: View, Equatable {
    @Environment(AppModel.self) private var model
    nonisolated let unread: [SessionKey]

    nonisolated static func == (a: Self, b: Self) -> Bool { a.unread == b.unread }

    var body: some View {
        if !unread.isEmpty {
            Button { model.markSeen(unread) } label: { Label(StateName.markAllSeen(unread.count), systemImage: Theme.Icon.success) }
        }
    }
}
