import ActivityKit
import SwiftUI
import WidgetKit

/// Draws the Fleet activity: lock screen (and the Watch Smart Stack's small family), Dynamic Island compact / minimal /
/// expanded. The compact island's left side is the persona chosen in Settings › Appearance › Avatar in its state for the fleet
/// (FleetFace) with the most urgent count; the right side is the next count or a number the device chose (IslandCell,
/// Settings › Notifications › Dynamic Island). Beside another app's activity it is one glyph, the most urgent count (`IslandGlyph`): no room
/// for a face beside it; with no count, the persona alone (`IslandMinimal`). The expanded island and the lock screen
/// lead with the persona and the counts, then the agents.
/// Status colors are Persona' agent colors
/// (docs/protocol/agent-states.md), fixed rather than following the app's style: the extension cannot read it.
struct FleetLiveActivity: Widget {
    /// Agent rows that fit: the expanded island and the lock screen are both capped at about 160 pt.
    static let rows = 2
    /// The persona's side: the expanded island's corner, the lock screen's header.
    static let expandedFace = IslandMetrics.expandedFace
    static let lockFace = IslandMetrics.lockFace

    var body: some WidgetConfiguration { Self.configuration(for: PersonaActivityAttributes.self) }

    static func configuration<Attributes: ActivityAttributes>(for attributes: Attributes.Type) -> some WidgetConfiguration
    where Attributes.ContentState == FleetState {
        ActivityConfiguration(for: attributes) { context in
            FleetLockScreen(state: context.state, stale: context.isStale)
                // The lock screen is cut at about 160 pt: larger text would push the rows off it.
                .dynamicTypeSize(...DynamicTypeSize.large)
                .activitySystemActionForegroundColor(FleetPalette.accent)
                // The app's interface language now, not the one this long-lived process started in.
                .environment(\.locale, LanguageStore.drawingLocale)
        } dynamicIsland: { context in
            let state = context.state
            return DynamicIsland {
                // The persona in the corner, like the compact island's left side; the counts move to the right (beside
                // the face they lost their numbers: the corner is about 125 pt).
                DynamicIslandExpandedRegion(.leading) {
                    FleetFace(size: FleetLiveActivity.expandedFace, state: FleetPersona.state(state, stale: context.isStale), look: state.drawnLook)
                        .padding(.leading, 6)
                }
                DynamicIslandExpandedRegion(.trailing, priority: 1) {
                    VStack(alignment: .trailing, spacing: 2) {
                        HStack(spacing: 10) {
                            if state.needsYou > 0 { IslandCellView(cell: .needsYou(state.needsYou)) }
                            if state.working > 0 { IslandCellView(cell: .working(state.working)) }
                            if state.unread > 0 { IslandCellView(cell: .unread(state.unread)) }
                        }
                        FleetStatsLine(state: state, onDark: true)
                    }
                    .padding(.trailing, 6)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    // Two rows of two lines and the "+2" under them only fit packed (`FleetState.islandPacked`).
                    let packed = (context.isStale || state.firstActionable == nil) && state.islandPacked(showing: Self.rows)
                    VStack(alignment: .leading, spacing: packed ? IslandMetrics.packedRowGap : IslandMetrics.rowGap) {
                        if !context.isStale, let act = state.firstActionable {
                            // A request the buttons can settle takes the room: it, its buttons, and how many more.
                            Link(destination: FleetLink.url(act)!) { FleetRow(item: act, showTitle: true, onDark: true) }
                            FleetActionRow(item: act)
                            if state.hidden(showing: 1) > 0 {
                                Text(FleetText.more(state.hidden(showing: 1))).font(.caption).foregroundStyle(.white.opacity(0.6))
                                    .padding(.leading, 15)
                            }
                        } else {
                            ForEach(state.items.prefix(Self.rows)) { item in
                                Link(destination: FleetLink.url(item)!) { FleetRow(item: item, showTitle: true, onDark: true) }
                            }
                        }
                        if context.isStale || state.firstActionable == nil, state.hidden(showing: Self.rows) > 0 {
                            Text(FleetText.more(state.hidden(showing: Self.rows))).font(.caption).foregroundStyle(.white.opacity(0.6))
                                .padding(.leading, 15) // under the titles, clear of the rounded corner
                        }
                    }
                    .padding(.horizontal, 4)
                    .padding(.top, packed ? 0 : IslandMetrics.rowsTop)
                    .environment(\.locale, LanguageStore.drawingLocale)
                }
            } compactLeading: {
                IslandSide(state: state, leading: true, stale: context.isStale)
            } compactTrailing: {
                IslandSide(state: state, leading: false)
            } minimal: {
                IslandWidth { limited in IslandMinimal(state: state, stale: context.isStale, limited: limited, look: state.drawnLook) }
            }
            .widgetURL(FleetLink.whole(state))
            .keylineTint(FleetPalette.accent)
        }
        .supplementalActivityFamilies([.small])
    }
}

/// Retains the previous OS activity identity with exactly the same drawing recipe.
struct CompatibleFleetLiveActivity: Widget {
    var body: some WidgetConfiguration { FleetLiveActivity.configuration(for: LegacyFleetActivityAttributes.self) }
}

// MARK: - Pieces

/// One side of the compact island. When the system limits its width (another app's activity beside ours on iPhone 18
/// Pro, landscape, iPhone Duo: 28 pt a side, both sides together by the camera), the leading side is the minimal
/// island's glyph and the trailing one is empty, which the system then hides: both sides squeezed into 28 pt each cut
/// 「⚙3 📊42M」 to 「⚙3 📊4」 (2026-11-02). Apple's advice for limited width, and the leading side alone says what matters.
struct IslandSide: View {
    var state: FleetState
    var leading: Bool
    var stale = false

    var body: some View {
        IslandWidth { limited in
            if limited {
                if leading { IslandMinimal(state: state, stale: stale, limited: true, look: state.drawnLook) }
            } else {
                if leading {
                    // The persona, then the most urgent count (docs/apple/live-activity.md「形象」).
                    IslandLeading(state: state, stale: stale, look: state.drawnLook).padding(.leading, IslandMetrics.edge)
                } else {
                    IslandCellView(cell: IslandCell.sides(state).right).padding(.trailing, IslandMetrics.edge)
                }
            }
        }
    }
}

/// Whether the system limits the island's width (`isDynamicIslandLimitedInWidth`, iOS 27). Before iOS 27 it is never
/// said, and the compact island is the two sides as always.
struct IslandWidth<Content: View>: View {
    @ViewBuilder var content: (Bool) -> Content

    var body: some View {
        // `isDynamicIslandLimitedInWidth` is in the iOS 27 SDK; Xcode 26 (Swift 6.2, and 6.3 from 26.4) has no such key path, and
        // `#available` only guards the run time, so it is left out at compile time there.
        #if compiler(>=6.4)
        if #available(iOS 27.0, *) {
            Reader(content: content)
        } else {
            content(false)
        }
        #else
        content(false)
        #endif
    }

    #if compiler(>=6.4)
    @available(iOS 27.0, *)
    private struct Reader: View {
        @Environment(\.isDynamicIslandLimitedInWidth) private var limited
        var content: (Bool) -> Content
        var body: some View { content(limited) }
    }
    #endif
}

struct FleetTimer: View {
    var since: Date
    var body: some View {
        Text(timerInterval: since...Date.distantFuture, countsDown: false, showsHours: true)
            .monospacedDigit()
            .multilineTextAlignment(.trailing)
            .lineLimit(1)
    }
}

/// One agent: a colored dot, its name (or what kind of request, when names are hidden), what it wants.
struct FleetRow: View {
    var item: FleetState.Item
    var showTitle: Bool
    var onDark = false

    var body: some View {
        Group {
            if showTitle, item.runsInBackground {
                backgroundRow
            } else {
                row
            }
        }
        .foregroundStyle(onDark ? Color.white : Color.primary)
    }

    private var row: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            dot
            VStack(alignment: .leading, spacing: 1) {
                if showTitle {
                    HStack(spacing: 4) {
                        Text(item.title).font(.subheadline.weight(.semibold)).lineLimit(1)
                        if item.kind == .unread, !item.detail.isEmpty {
                            // Finished: what it said, on the same line (rows must fit three in the island).
                            Text(item.detail).font(.caption).foregroundStyle(secondary).lineLimit(1)
                        } else if !item.place.isEmpty {
                            Text(item.place).font(.caption2).foregroundStyle(secondary).lineLimit(1)
                        }
                    }
                    if item.needsYou {
                        Text(FleetText.line(item)).font(.caption).foregroundStyle(secondary).lineLimit(1)
                    }
                } else {
                    Text(FleetText.anonymous(item)).font(.subheadline.weight(.medium)).lineLimit(1)
                }
            }
            Spacer(minLength: 4)
            timer
        }
    }

    /// An agent whose turn is over while its workflow runs on (#3437): 「后台」 where its place goes, the workflow's clock;
    /// under them what runs, and the workflow's bar under the clock. Two lines, as a request's row: the island and the lock
    /// screen keep their height. The bar is static (`FleetBar`): repeating animations never run here.
    private var backgroundRow: some View {
        VStack(alignment: .leading, spacing: 1) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                dot
                HStack(spacing: 4) {
                    Text(item.title).font(.subheadline.weight(.semibold)).lineLimit(1)
                    Text(FleetText.background).font(.caption2).foregroundStyle(secondary).lineLimit(1)
                }
                Spacer(minLength: 4)
                timer
            }
            HStack(spacing: 8) {
                Text(FleetText.line(item)).font(.caption).foregroundStyle(secondary).lineLimit(1)
                Spacer(minLength: 4)
                if let fraction = item.progress?.fraction {
                    FleetBar(fraction: fraction, fill: working, onDark: onDark)
                }
            }
            .padding(.leading, IslandMetrics.rowInset)
        }
    }

    private var dot: some View {
        Circle().fill(FleetPalette.color(item.kind)).frame(width: 7, height: 7).alignmentGuide(.firstTextBaseline) { $0[.bottom] - 1 }
    }

    @ViewBuilder private var timer: some View {
        if item.kind == .working, let since = item.sinceDate {
            FleetTimer(since: since).font(.caption.weight(.medium)).foregroundStyle(working)
                .frame(maxWidth: 60, alignment: .trailing)
        }
    }

    private var secondary: Color { onDark ? .white.opacity(0.65) : .secondary }
    private var working: Color { onDark ? FleetPalette.working : FleetPalette.workingText }
}

/// A background workflow's bar (#3437): agents done of those started, static. Drawn rather than a linear
/// `ProgressView`, whose track (the system fill) all but vanished on the island's and the lock screen's black (1.2:1):
/// the track is `FleetPalette.track`, so the empty part shows and 3/8 reads as three eighths. The line beside it prints
/// the same counts, so VoiceOver reads those and skips the bar.
struct FleetBar: View {
    var fraction: Double
    var fill: Color
    var onDark: Bool

    var body: some View {
        Capsule()
            .fill(FleetPalette.track(onDark: onDark))
            .overlay(alignment: .leading) {
                Capsule().fill(fill).frame(width: IslandMetrics.bar * min(max(fraction, 0), 1))
            }
            .frame(width: IslandMetrics.bar, height: IslandMetrics.barHeight)
            .accessibilityHidden(true)
    }
}

/// Counts: 「💬 2 需要你  ⚙ 3 在工作  ● 1 未看」, the first line of the lock screen (the island's symbols).
struct FleetCounts: View {
    var state: FleetState
    /// Numbers only (the symbols say which): when the island's numbers share the line.
    var short = false
    var body: some View {
        // The words go first when the line is too narrow for them (all three kinds in Chinese, or long counts): numbers
        // only, never a line wider than the card.
        Group {
            if short {
                row(short: true)
            } else {
                ViewThatFits(in: .horizontal) {
                    row(short: false)
                    row(short: true)
                }
            }
        }
        .layoutPriority(1)
    }

    private func row(short: Bool) -> some View {
        HStack(spacing: short ? 8 : 10) {
            if state.needsYou > 0 { chip(Image(systemName: FleetSymbol.needsYou), "\(state.needsYou)", StateName.count(.needsYou, state.needsYou), FleetPalette.needsYou, short) }
            if state.working > 0 { chip(Image(systemName: FleetSymbol.working), "\(state.working)", StateName.count(.working, state.working), FleetPalette.working, short) }
            if state.unread > 0 { chip(Image(systemName: FleetSymbol.unread).font(.system(size: 7)), "\(state.unread)", StateName.count(.unread, state.unread), FleetPalette.unread, short) }
        }
    }

    /// `full` is the count with its words, one localized string (「2 需要你」), so a language can reorder them.
    private func chip(_ symbol: some View, _ count: String, _ full: String, _ color: Color, _ short: Bool) -> some View {
        HStack(spacing: 4) {
            symbol.foregroundStyle(color).font(.footnote)
            Text(short ? count : full).font(.subheadline.weight(.semibold)).monospacedDigit().lineLimit(1)
        }
        .fixedSize()
    }
}

/// The numbers the device chose for its island (TPM, today's tokens, load, the assistant), right of the counts.
struct FleetStatsLine: View {
    var state: FleetState
    var onDark = false

    static func count(_ s: FleetState) -> Int { [s.left, s.right].filter { $0 != .status && $0 != .none }.count }

    var body: some View {
        let cells = [state.left, state.right].filter { $0 != .status && $0 != .none }.map { IslandCell.cell($0, state, status: nil) }
        HStack(spacing: 8) {
            ForEach(Array(cells.enumerated()), id: \.offset) { _, cell in
                if case let .timer(since) = cell {
                    FleetTimer(since: since).font(.caption.weight(.semibold)).foregroundStyle(onDark ? FleetPalette.working : FleetPalette.workingText)
                        .frame(maxWidth: 52, alignment: .trailing)
                } else if let text = FleetText.stat(cell) {
                    Text(text).font(.caption.weight(.semibold)).monospacedDigit().lineLimit(1).minimumScaleFactor(0.8)
                        .foregroundStyle(onDark ? Color.white.opacity(0.75) : Color.secondary)
                }
            }
        }
    }
}

/// Deny / Allow for a permission, View / Approve for a plan (rejecting a plan needs words: that is the app's job).
/// Hidden while the content is stale, so nobody approves something the agent has moved past.
struct FleetActionRow: View {
    var item: FleetState.Item

    var body: some View {
        if let id = item.interaction {
            HStack(spacing: 10) {
                switch item.kind {
                // Deny / View: the system's neutral fill with primary words (white on gray was 1.6:1 on a light lock
                // screen); Allow / Approve: a blue dark enough for white words.
                case .permission:
                    Button(intent: DenyPermissionIntent(interactionID: id)) { label("Deny", "xmark") }
                        .buttonStyle(.bordered)
                        .tint(.primary)
                    Button(intent: AllowPermissionIntent(interactionID: id)) { label("Allow [Notification Actions ce7ef2]", "checkmark") }
                        .buttonStyle(.borderedProminent)
                        .tint(FleetPalette.button)
                        .foregroundStyle(.white)
                case .plan:
                    Link(destination: FleetLink.url(item)!) {
                        label("View [Fleet Live Activity db8db0]", "doc.text")
                            .padding(.vertical, 7)
                            .background(Color.primary.opacity(0.12), in: .capsule)
                    }
                    .foregroundStyle(.primary)
                    Button(intent: ApprovePlanIntent(interactionID: id)) { label("Approve [Notification Actions 8cbe69]", "checkmark") }
                        .buttonStyle(.borderedProminent)
                        .tint(FleetPalette.button)
                        .foregroundStyle(.white)
                default:
                    EmptyView()
                }
            }
            .buttonBorderShape(.capsule)
            .padding(.leading, 15)
        }
    }

    private func label(_ text: LocalizedStringKey, _ symbol: String) -> some View {
        Label(text, systemImage: symbol)
            .font(.subheadline.weight(.semibold))
            .lineLimit(1)
            .frame(maxWidth: .infinity)
    }
}

// MARK: - Lock screen

struct FleetLockScreen: View {
    var state: FleetState
    var stale: Bool
    @Environment(\.activityFamily) private var family

    var body: some View {
        // The small family (the Watch's counts) taps as the whole activity; the lock screen opens the one request it
        // draws, if it draws just one.
        if family == .small {
            small.widgetURL(FleetLink.whole(state))
        } else {
            full.widgetURL(FleetLink.lockScreen(state, stale: stale))
        }
    }

    private var full: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack(spacing: 10) {
                FleetFace(size: FleetLiveActivity.lockFace, state: FleetPersona.state(state, stale: stale), look: state.drawnLook)
                FleetCounts(state: state, short: FleetStatsLine.count(state) > 1)
                Spacer(minLength: 0)
                FleetStatsLine(state: state)
            }
            if !stale, let act = state.firstActionable {
                // Allow / Approve need Face ID first (authenticationRequired); Deny works locked.
                FleetRow(item: act, showTitle: state.details)
                FleetActionRow(item: act)
            } else if state.details {
                ForEach(state.items.prefix(FleetLiveActivity.rows)) { FleetRow(item: $0, showTitle: true) }
            } else if let first = state.items.first, first.needsYou {
                FleetRow(item: first, showTitle: false)
            }
            if stale {
                Label("Waiting for hub…", systemImage: "wifi.exclamationmark").font(.caption2).foregroundStyle(.secondary)
            } else if state.details, state.firstActionable == nil, state.hidden(showing: FleetLiveActivity.rows) > 0 {
                Text(FleetText.more(state.hidden(showing: FleetLiveActivity.rows))).font(.caption).foregroundStyle(.secondary)
                    .padding(.leading, 15)
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 14)
    }

    /// Apple Watch Smart Stack: one glance.
    private var small: some View {
        HStack(spacing: 8) {
            IslandCellView(cell: IslandCell.minimal(state)).font(.title3)
            VStack(alignment: .leading, spacing: 0) {
                Text(FleetText.headline(state)).font(.headline).lineLimit(1).minimumScaleFactor(0.8)
                if state.phase == .needsYou, state.working > 0 {
                    Text(StateName.count(.working, state.working)).font(.caption).foregroundStyle(.secondary)
                } else if state.phase != .done, state.unread > 0 {
                    Text(StateName.count(.unread, state.unread)).font(.caption).foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(10)
    }
}
