import SwiftUI

/// One agent task and its progress.
public struct TaskItem: Identifiable, Sendable {
    public enum Status: Sendable, Equatable {
        /// Queued: a still ring around the step number.
        case pending(step: Int)
        /// In progress: a spinning ring around the step number.
        case running(step: Int)
        /// Failed: red badge and a pill with a spinning retry glyph.
        case failed
        /// Done: green check badge and a pill.
        case completed
    }

    /// A line in the task's expanded detail.
    public struct Detail: Hashable, Sendable {
        public var label: String
        public var meta: String

        public init(_ label: String, meta: String) {
            self.label = label
            self.meta = meta
        }
    }

    public var id: String
    public var title: String
    /// A line under the title, e.g. the last thing an agent said.
    public var subtitle: String?
    /// A quieter line under that, e.g. where the task runs.
    public var footnote: String?
    /// Right-aligned quantity, e.g. "12 suppliers".
    public var amount: String?
    public var status: Status
    public var details: [Detail]

    public init(id: String, title: String, subtitle: String? = nil, footnote: String? = nil, amount: String? = nil, status: Status,
                details: [Detail] = []) {
        self.id = id
        self.title = title
        self.subtitle = subtitle
        self.footnote = footnote
        self.amount = amount
        self.status = status
        self.details = details
    }
}

/// Live agent task status: capsule rows (or a grouped list) with a progress ring or
/// result badge, a status pill, and a tap-to-expand detail list.
///
/// `TaskRows` lays the rows out itself. A host with its own lazy list draws each row with `TaskRow`.
public struct TaskRows: View {
    public enum Style: String, CaseIterable, Sendable {
        /// Separate rounded capsules that square up when opened.
        case capsules
        /// One card with divided rows.
        case list
    }

    var tasks: [TaskItem]
    var style: Style
    var completedLabel: String
    var failedLabel: String
    var showsPills: Bool
    var titleLines: Int
    var maxWidth: CGFloat?
    var externalExpanded: Binding<Set<String>>?
    var onRetry: ((TaskItem) -> Void)?

    @State private var internalExpanded: Set<String> = []
    @Environment(\.buiTheme) private var theme

    /// - Parameters:
    ///   - showsPills: the Completed / Failed pills; a checklist reads quieter without them.
    ///   - titleLines: how many lines a title may wrap to (the original cuts it at one).
    ///   - maxWidth: the original's 440 pt; `nil` fills the host's width (and drops the capsules' minimum height).
    public init(
        _ tasks: [TaskItem],
        style: Style = .capsules,
        expanded: Binding<Set<String>>? = nil,
        completedLabel: String = "Completed",
        failedLabel: String = "Failed",
        showsPills: Bool = true,
        titleLines: Int = 1,
        maxWidth: CGFloat? = 440,
        onRetry: ((TaskItem) -> Void)? = nil
    ) {
        self.tasks = tasks
        self.style = style
        self.externalExpanded = expanded
        self.completedLabel = completedLabel
        self.failedLabel = failedLabel
        self.showsPills = showsPills
        self.titleLines = titleLines
        self.maxWidth = maxWidth
        self.onRetry = onRetry
    }

    private var expanded: Set<String> {
        externalExpanded?.wrappedValue ?? internalExpanded
    }

    private func toggle(_ id: String) {
        var next = expanded
        if next.contains(id) { next.remove(id) } else { next.insert(id) }
        withAnimation(BUICurve.outStrong.animation(duration: 0.3)) {
            if let externalExpanded { externalExpanded.wrappedValue = next } else { internalExpanded = next }
        }
    }

    public var body: some View {
        let list = style == .list
        VStack(spacing: list ? 0 : 8) {
            ForEach(Array(tasks.enumerated()), id: \.element.id) { index, task in
                TaskRow(
                    task,
                    shape: list ? .list(first: false, last: index == tasks.count - 1, card: false) : .capsule,
                    isOpen: expanded.contains(task.id),
                    accessory: task.details.isEmpty ? .none : .expand,
                    titleLines: titleLines,
                    action: task.details.isEmpty ? nil : { toggle(task.id) }
                ) {
                    TaskBadge(task.status)
                } trailing: {
                    if showsPills { TaskStatusPill(task, completedLabel: completedLabel, failedLabel: failedLabel, onRetry: onRetry) }
                } detail: {
                    TaskDetails(task.details)
                }
                .buiEntrance(.fadeUp, duration: 0.45, delay: Double(index) * 0.08)
            }
        }
        .frame(maxWidth: maxWidth ?? .infinity)
        .modifier(ListCard(active: list))
        .frame(minHeight: list || maxWidth == nil ? nil : 196, alignment: .top)
    }
}

/// How far a `TaskRow`'s content sits in from its sides: a host lines up what it draws over the row's end with it.
public enum TaskRowInset {
    public static let horizontal: CGFloat = 10
}

/// One task row, for a host that lays out its own (lazy) list: the capsule, a slice of the list card, or bare.
/// The leading badge, the views before the chevron and the opened detail are the host's; `TaskBadge`,
/// `TaskStatusPill` and `TaskDetails` draw `TaskRows`' own.
public struct TaskRow<Leading: View, Trailing: View, Detail: View>: View {
    public enum Shape: Sendable, Equatable {
        /// A rounded capsule of its own that squares up when open.
        case capsule
        /// One row of a list card: the card's top corners on the first, its bottom ones and no divider on the
        /// last. `card: false` leaves the card to an enclosing view.
        case list(first: Bool, last: Bool, card: Bool = true)
        /// No surface: the host draws it.
        case plain
    }

    /// What ends the row.
    public enum Accessory: Sendable, Equatable {
        /// A chevron that turns when the row opens.
        case expand
        /// A right chevron: the row goes somewhere.
        case disclosure
        case none
    }

    var task: TaskItem
    var shape: Shape
    var isOpen: Bool
    var accessory: Accessory
    var titleLines: Int
    var subtitleLines: Int
    var action: (() -> Void)?
    var leading: Leading
    var trailing: Trailing
    var detail: Detail
    var titleLeading: AnyView?
    @Environment(\.buiTheme) private var theme

    /// - Parameter action: what a tap does (toggle the detail, open a page); `nil` leaves the row inert, for a
    ///   host that wraps it in its own link or button.
    public init(
        _ task: TaskItem,
        shape: Shape = .capsule,
        isOpen: Bool = false,
        accessory: Accessory = .expand,
        titleLines: Int = 1,
        subtitleLines: Int = 1,
        action: (() -> Void)? = nil,
        @ViewBuilder leading: () -> Leading,
        @ViewBuilder trailing: () -> Trailing,
        @ViewBuilder detail: () -> Detail
    ) {
        self.task = task
        self.shape = shape
        self.isOpen = isOpen
        self.accessory = accessory
        self.titleLines = titleLines
        self.subtitleLines = subtitleLines
        self.action = action
        self.leading = leading()
        self.trailing = trailing()
        self.detail = detail()
    }

    /// Views before the title, on its first line (a tag saying where the task came from). The badge's column is the
    /// leading view's alone: anything wider there spills out of the row and over the title.
    public func titleLeading<V: View>(@ViewBuilder _ content: () -> V) -> Self {
        var row = self
        row.titleLeading = AnyView(content())
        return row
    }

    /// `titleLeading` when there is something to put there; `nil` leaves the title alone (no gap before it).
    public func titleLeading<V: View>(_ content: (() -> V)?) -> Self {
        guard let content else { return self }
        return titleLeading(content)
    }

    private var oneLine: Bool { (task.subtitle ?? "").isEmpty && (task.footnote ?? "").isEmpty }

    /// The badge's column: details and the list's dividers start after it.
    static var badgeColumn: CGFloat { 10 + 24 + 10 }

    public var body: some View {
        VStack(spacing: 0) {
            head
            if isOpen {
                detail
                    .padding(.leading, Self.badgeColumn)
                    .padding(.trailing, 10)
                    .padding(.bottom, 10)
                    .transition(.opacity)
            }
        }
        .clipShape(clip)
        .background { surface }
        .overlay(alignment: .bottom) {
            if case .list(_, let last, _) = shape, !last {
                Rectangle().fill(theme.colors.line).frame(height: 1).padding(.leading, Self.badgeColumn)
            }
        }
    }

    /// The badge, the words and the chevron are the row's button; the host's trailing views sit between them on their
    /// own, so a switch or a retry pill there takes its own taps.
    private var head: some View {
        HStack(alignment: oneLine ? .center : .top, spacing: 10) {
            tappable(highlight: true) { main }
            trailing
                .padding(.top, oneLine ? 0 : 1)
            tappable(highlight: false) { chevron }
        }
        .padding(.horizontal, TaskRowInset.horizontal)
        .padding(.vertical, oneLine ? 0 : 10)
        .frame(minHeight: 44 * max(1, theme.typeScale * 0.9))
    }

    @ViewBuilder private func tappable<Label: View>(highlight: Bool, @ViewBuilder _ label: () -> Label) -> some View {
        if let action {
            Button(action: action) { label().contentShape(.rect) }
                .buttonStyle(RowPressStyle(fill: highlight ? theme.colors.inset : .clear))
                .accessibilityValue(accessory == .expand ? theme.strings.expanded(isOpen) : "")
        } else {
            label()
        }
    }

    private var main: some View {
        HStack(alignment: oneLine ? .center : .top, spacing: 10) {
            leading
                .frame(width: 24, height: 24)
                .padding(.top, oneLine ? 0 : 1)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 10) {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        if let titleLeading { titleLeading.fixedSize() }
                        Text(task.title)
                            .buiFont(.label)
                            .foregroundStyle(theme.colors.ink)
                            .lineLimit(titleLines)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    if let amount = task.amount {
                        Text(amount)
                            .buiFont(BUITextStyle(size: 12.5))
                            .monospacedDigit()
                            .foregroundStyle(theme.colors.ink2)
                            .lineLimit(1)
                            .fixedSize()
                    }
                }
                if let subtitle = task.subtitle, !subtitle.isEmpty {
                    Text(subtitle)
                        .buiFont(.detail)
                        .foregroundStyle(theme.colors.ink2)
                        .lineLimit(subtitleLines)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                if let footnote = task.footnote, !footnote.isEmpty {
                    Text(footnote)
                        .buiFont(.caption)
                        .foregroundStyle(theme.colors.ink3)
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private var chevron: some View {
        switch accessory {
        case .expand:
            BUIIcon(.chevronDown, size: 15, lineWidth: 2.2)
                .foregroundStyle(theme.colors.ink3)
                .rotationEffect(.degrees(isOpen ? 180 : 0))
                .frame(width: 28, height: 20)
                .padding(.leading, -8)
        case .disclosure:
            BUIIcon(.chevronRight, size: 14, lineWidth: 2.2)
                .foregroundStyle(theme.colors.ink3)
                .frame(width: 20, height: 20)
                .padding(.leading, -6)
        case .none:
            EmptyView()
        }
    }

    private var radius: CGFloat {
        switch shape {
        case .capsule: isOpen ? 14 : 22
        case .list: theme.radii.card
        case .plain: 0
        }
    }

    private var clip: UnevenRoundedRectangle {
        let r = radius
        switch shape {
        case .list(let first, let last, let card):
            let top = first && card ? r : 0, bottom = last && card ? r : 0
            return UnevenRoundedRectangle(topLeadingRadius: top, bottomLeadingRadius: bottom, bottomTrailingRadius: bottom,
                                          topTrailingRadius: top, style: .continuous)
        default:
            return UnevenRoundedRectangle(topLeadingRadius: r, bottomLeadingRadius: r, bottomTrailingRadius: r,
                                          topTrailingRadius: r, style: .continuous)
        }
    }

    @ViewBuilder private var surface: some View {
        switch shape {
        case .capsule:
            let shape = RoundedRectangle(cornerRadius: radius, style: .continuous)
            shape.fill(theme.colors.surface)
                .shadow(color: Color(light: 0, dark: 0, lightAlpha: 0.035, darkAlpha: 0.2), radius: 1, y: 1)
                .shadow(color: Color(light: 0, dark: 0, lightAlpha: 0.035, darkAlpha: 0.2), radius: 10, y: 5)
                .overlay { shape.strokeBorder(BUIElevation.card.ring(theme.colors) ?? .clear, lineWidth: 1) }
        case .list(let first, let last, let card) where card:
            clip.fill(theme.colors.surface)
                .overlay { SliceEdges(top: first, bottom: last, radius: radius).stroke(BUIElevation.card.ring(theme.colors) ?? .clear, lineWidth: 1) }
        default:
            EmptyView()
        }
    }
}

public extension TaskRow where Leading == TaskBadge, Trailing == TaskStatusPill, Detail == TaskDetails {
    /// `TaskRows`' own row: the status badge, the Completed / Failed pill and the detail lines.
    init(
        _ task: TaskItem,
        shape: Shape = .capsule,
        isOpen: Bool = false,
        completedLabel: String = "Completed",
        failedLabel: String = "Failed",
        onRetry: ((TaskItem) -> Void)? = nil,
        action: (() -> Void)? = nil
    ) {
        self.init(task, shape: shape, isOpen: isOpen, accessory: task.details.isEmpty ? .none : .expand, action: action) {
            TaskBadge(task.status)
        } trailing: {
            TaskStatusPill(task, completedLabel: completedLabel, failedLabel: failedLabel, onRetry: onRetry)
        } detail: {
            TaskDetails(task.details)
        }
    }
}

/// A list card's outline for one row: the sides, plus the top edge on the first row and the bottom edge on the last,
/// rounded there. Rows in between draw only their sides, so stacked rows read as one card.
private struct SliceEdges: Shape {
    var top: Bool
    var bottom: Bool
    var radius: CGFloat

    func path(in rect: CGRect) -> Path {
        let rect = rect.insetBy(dx: 0.5, dy: 0)
        var p = Path()
        let r = min(radius, rect.height / 2, rect.width / 2)
        p.move(to: CGPoint(x: rect.minX, y: bottom ? rect.maxY - r - 0.5 : rect.maxY))
        if top {
            p.addLine(to: CGPoint(x: rect.minX, y: rect.minY + r + 0.5))
            p.addArc(tangent1End: CGPoint(x: rect.minX, y: rect.minY + 0.5), tangent2End: CGPoint(x: rect.minX + r, y: rect.minY + 0.5), radius: r)
            p.addLine(to: CGPoint(x: rect.maxX - r, y: rect.minY + 0.5))
            p.addArc(tangent1End: CGPoint(x: rect.maxX, y: rect.minY + 0.5), tangent2End: CGPoint(x: rect.maxX, y: rect.minY + r + 0.5), radius: r)
        } else {
            p.addLine(to: CGPoint(x: rect.minX, y: rect.minY))
            p.move(to: CGPoint(x: rect.maxX, y: rect.minY))
        }
        p.addLine(to: CGPoint(x: rect.maxX, y: bottom ? rect.maxY - r - 0.5 : rect.maxY))
        if bottom {
            p.addArc(tangent1End: CGPoint(x: rect.maxX, y: rect.maxY - 0.5), tangent2End: CGPoint(x: rect.maxX - r, y: rect.maxY - 0.5), radius: r)
            p.addLine(to: CGPoint(x: rect.minX + r, y: rect.maxY - 0.5))
            p.addArc(tangent1End: CGPoint(x: rect.minX, y: rect.maxY - 0.5), tangent2End: CGPoint(x: rect.minX, y: rect.maxY - r - 0.5), radius: r)
        }
        return p
    }
}

/// A task's 24 pt badge: the step ring (spinning while running), the red cross or the green check.
public struct TaskBadge: View {
    var status: TaskItem.Status
    @Environment(\.buiTheme) private var theme

    public init(_ status: TaskItem.Status) {
        self.status = status
    }

    public var body: some View {
        switch status {
        case .pending(let step):
            ProgressRing(step: step, active: false)
        case .running(let step):
            ProgressRing(step: step, active: true)
        case .failed:
            StatusBadge(fill: theme.colors.red, glyph: .close, size: 12)
                .id("failed")
        case .completed:
            StatusBadge(fill: theme.colors.green, glyph: .check, size: 13)
                .id("completed")
        }
    }
}

/// The Completed pill, or the Failed one that retries when tapped; nothing while the task is queued or running.
public struct TaskStatusPill: View {
    var task: TaskItem
    var completedLabel: String
    var failedLabel: String
    var onRetry: ((TaskItem) -> Void)?

    public init(_ task: TaskItem, completedLabel: String = "Completed", failedLabel: String = "Failed", onRetry: ((TaskItem) -> Void)? = nil) {
        self.task = task
        self.completedLabel = completedLabel
        self.failedLabel = failedLabel
        self.onRetry = onRetry
    }

    public var body: some View {
        switch task.status {
        case .completed:
            BUIPill(completedLabel, tone: .green)
        case .failed:
            if let onRetry {
                Button {
                    onRetry(task)
                } label: {
                    BUIPill(failedLabel, tone: .red, icon: .retry)
                }
                .buttonStyle(.plain)
                .accessibilityHint("Retry")
            } else {
                BUIPill(failedLabel, tone: .red)
            }
        case .pending, .running:
            EmptyView()
        }
    }
}

/// An open task's detail lines under a guide rule: a label, and its meta in the code face on the right.
public struct TaskDetails: View {
    var details: [TaskItem.Detail]
    @Environment(\.buiTheme) private var theme

    public init(_ details: [TaskItem.Detail]) {
        self.details = details
    }

    public var body: some View {
        VStack(spacing: 6) {
            ForEach(Array(details.enumerated()), id: \.element) { index, detail in
                HStack {
                    Text(detail.label)
                        .buiFont(.caption)
                        .foregroundStyle(theme.colors.ink2)
                    Spacer(minLength: 8)
                    Text(detail.meta)
                        .buiFont(BUITextStyle(size: 11.5, design: .monospaced))
                        .monospacedDigit()
                        .foregroundStyle(theme.colors.ink3)
                }
                .buiEntrance(.fadeUp, duration: 0.3, delay: 0.12 + Double(index) * 0.1)
            }
        }
        .fixedSize(horizontal: false, vertical: true)
        // The rule runs down the badge's middle (the row puts the detail after the badge's column).
        .overlay(alignment: .leading) {
            Rectangle().fill(theme.colors.line).frame(width: 1).offset(x: -22)
        }
    }
}

/// The 24 pt step ring: a `line` track with an `ink-3` arc (28 %) turning every 1.1 s (a Core Animation layer on iOS,
/// `SpinningArc`).
struct ProgressRing: View {
    var step: Int
    var active: Bool
    @Environment(\.buiTheme) private var theme

    var body: some View {
        ZStack {
            Circle().stroke(theme.colors.line, lineWidth: 2).padding(1)
            if active {
                SpinningArc(track: nil, head: theme.colors.ink3, lineWidth: 2, period: 1.1, from: 0, to: 0.28, roundCap: true)
            }
            Text("\(step)")
                .buiFont(BUITextStyle(size: 10.5, weight: .semibold))
                .monospacedDigit()
                .foregroundStyle(theme.colors.ink)
        }
        .frame(width: 24, height: 24)
    }
}

/// 22 pt result badge that pops in.
struct StatusBadge: View {
    var fill: Color
    var glyph: BUIGlyph
    var size: CGFloat

    var body: some View {
        Circle()
            .fill(fill)
            .overlay { BUIIcon(glyph, size: size, lineWidth: 3.5).foregroundStyle(.white) }
            .frame(width: 22, height: 22)
            .buiEntrance(.popIn, duration: 0.3)
    }
}

/// The `h-5.5 rounded-full px-2 text-[11.5px] font-medium` status pill, in a tone: Completed in green, Failed in red
/// with a spinning retry glyph, or any state a host has (Needs You, Running, Online…).
public struct BUIPill: View {
    public enum Tone: Sendable, Hashable {
        case neutral, accent, green, red, orange
        /// A host's own pair, e.g. a state's words on its tint.
        case custom(ink: Color, fill: Color)
        /// Secondary words in a hairline ring, no fill (which of several kinds: an agent's name).
        case outline
    }

    public enum Icon: Sendable, Hashable {
        /// A small dot before the words, in the tone.
        case dot
        /// The retry arrow, turning (the failed pill's).
        case retry
        /// The 12 pt ring spinner.
        case spinner
        /// An SF Symbol.
        case symbol(String)
    }

    var text: String
    var tone: Tone
    var icon: Icon?
    var truncates: Bool
    @Environment(\.buiTheme) private var theme
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var turning = false

    /// - Parameter truncates: offered less than its full width (a pill longer than its line in a wrapping row), the
    ///   words end in "…" instead of the pill keeping its width.
    public init(_ text: String, tone: Tone = .neutral, icon: Icon? = nil, truncates: Bool = false) {
        self.text = text
        self.tone = tone
        self.icon = icon
        self.truncates = truncates
    }

    private var colors: (ink: Color, fill: Color) {
        switch tone {
        case .neutral: (theme.colors.ink2, theme.colors.field)
        case .accent: (theme.colors.accent, theme.colors.accentTint)
        case .green: (theme.colors.green, theme.colors.greenTint)
        case .red: (theme.colors.red, theme.colors.redTint)
        case .orange: (theme.colors.orange, theme.colors.orangeTint)
        case .custom(let ink, let fill): (ink, fill)
        case .outline: (theme.colors.ink2, .clear)
        }
    }

    public var body: some View {
        let c = colors
        HStack(spacing: 5) {
            switch icon {
            case .dot:
                Circle().fill(c.ink).frame(width: 6 * theme.typeScale, height: 6 * theme.typeScale)
            case .spinner:
                BUISpinner(size: 10 * theme.typeScale)
            case .symbol(let name):
                Image(systemName: name).font(.system(size: 10 * theme.typeScale, weight: .semibold))
            case .retry, nil:
                EmptyView()
            }
            Text(text)
                .buiFont(BUITextStyle(size: 11.5, weight: .medium))
                .lineLimit(1)
            if icon == .retry {
                BUIIcon(.retry, size: 12, lineWidth: 3)
                    .rotationEffect(.degrees(turning ? 360 : 0))
                    .onAppear {
                        guard !reduceMotion else { return }
                        withAnimation(.linear(duration: 1.2).repeatForever(autoreverses: false)) { turning = true }
                    }
            }
        }
        .foregroundStyle(c.ink)
        .padding(.horizontal, 8)
        .frame(minHeight: 22 * max(1, theme.typeScale * 0.9))
        .background(c.fill, in: Capsule())
        .overlay { if tone == .outline { Capsule().strokeBorder(theme.colors.lineStrong, lineWidth: 1) } }
        .fixedSize(horizontal: !truncates, vertical: true)
        .buiEntrance(.fadeIn, duration: 0.2, curve: .easeOut)
    }
}

/// Fills the whole row while pressed — the touch stand-in for the original's `hover:bg-inset`.
struct RowPressStyle: ButtonStyle {
    var fill: Color

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(configuration.isPressed ? fill : .clear)
            .animation(.easeOut(duration: 0.15), value: configuration.isPressed)
    }
}

/// The list style's single card around all rows.
private struct ListCard: ViewModifier {
    var active: Bool
    @Environment(\.buiTheme) private var theme

    func body(content: Content) -> some View {
        if active {
            content.buiSurface(radius: theme.radii.card, elevation: .card)
        } else {
            content
        }
    }
}
