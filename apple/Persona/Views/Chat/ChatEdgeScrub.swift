import SwiftUI

// The right-edge navigator of Persona's conversation in the new layout (#1666, docs/apple/ios.md "New conversation
// layout"), like the ChatGPT desktop app's: a slide up or down along the list's right edge shows a column of short
// ticks, one per message the user typed, with a light tap as it opens; moving over the ticks picks one (a tick of the
// haptic each) and shows its first words beside it; letting go brings that message into view.
//
// It never takes another gesture's touch: it starts only in the band at the right edge (`Theme.Size.edgeBand`, inside
// the list's side margin) and only for a slide that is more up-and-down than sideways, so a sideways drag there still
// pages to Agents or opens the drawer (`DrawerPan`), and the system's back swipe is at the other edge. The list's own
// scrolling waits for it to give up, which a touch outside the band does at once.

/// One tick: a message the user typed, by its line's id, and its first words.
nonisolated struct EdgeMark: Equatable, Sendable {
    var id: String
    var text: String
}

/// Where the ticks go in a list `height` tall, between `top` and `bottom` margins (the top bar floats over the list):
/// the newest that fit, evenly spaced, centered in the room left.
nonisolated struct EdgeScrubColumn: Equatable, Sendable {
    /// The first mark shown (older ones do not fit) and how many are.
    var first: Int
    var count: Int
    var gap: CGFloat
    /// The first tick's y in the list.
    var top: CGFloat

    static func fit(marks: Int, height: CGFloat, top: CGFloat, bottom: CGFloat) -> EdgeScrubColumn? {
        let room = height - top - bottom
        guard marks > 0, room > 0 else { return nil }
        let fits = Int(room / Theme.Size.edgeTickGapMin) + 1
        let count = min(marks, fits)
        let gap = count > 1 ? min(Theme.Size.edgeTickGapMax, room / CGFloat(count - 1)) : 0
        let span = gap * CGFloat(count - 1)
        return EdgeScrubColumn(first: marks - count, count: count, gap: gap, top: top + (room - span) / 2)
    }

    /// The mark (an index into all of them) whose tick is nearest `y`.
    func index(at y: CGFloat) -> Int {
        guard count > 1, gap > 0 else { return first }
        let i = Int(((y - top) / gap).rounded())
        return first + min(count - 1, max(0, i))
    }

    func y(of index: Int) -> CGFloat { top + gap * CGFloat(index - first) }
}

/// The navigator while a finger is on it: what it shows and which tick is picked. Its own object, so the slide redraws
/// only the overlay, never the chat page.
@MainActor @Observable final class EdgeScrubState {
    private(set) var marks: [EdgeMark] = []
    private(set) var column: EdgeScrubColumn?
    private(set) var picked: Int?

    var isOpen: Bool { column != nil }
    var pickedMark: EdgeMark? { picked.flatMap { marks.indices.contains($0) ? marks[$0] : nil } }

    /// The messages the user typed, oldest first: not Persona's notes to the model, not other conversations', not a
    /// /loop tick.
    static func marks(_ lines: [ChatLine]) -> [EdgeMark] {
        lines.compactMap { line in
            guard case .row(.user(let item)) = line.content, Injected.of(item) == nil, item.from == nil,
                  MessageSource.of(item) == nil else { return nil }
            let raw = item.text ?? ""
            guard !raw.hasPrefix("/loop") else { return nil }
            let shown = UserRowText(raw).shown.trimmingCharacters(in: .whitespacesAndNewlines)
            return EdgeMark(id: line.id, text: shown.isEmpty ? String(localized: "Photo [Chat Layout]") : shown)
        }
    }

    /// Opens on these marks for a list `height` tall whose top `top` is under the top bar, picking the tick nearest
    /// `y`. False when there is nothing to show.
    @discardableResult
    func open(_ marks: [EdgeMark], height: CGFloat, top: CGFloat, at y: CGFloat) -> Bool {
        guard let column = EdgeScrubColumn.fit(marks: marks.count, height: height, top: top + Theme.Space.l,
                                               bottom: Theme.Space.xxl) else { return false }
        self.marks = marks
        self.column = column
        picked = column.index(at: y)
        return true
    }

    /// Picks a mark by its index (`-showEdgeScrub`, screenshots).
    func pick(_ index: Int) {
        guard let column, (column.first..<(column.first + column.count)).contains(index) else { return }
        picked = index
    }

    /// The finger moved: true when another tick is picked.
    func move(to y: CGFloat) -> Bool {
        guard let column else { return false }
        let i = column.index(at: y)
        guard i != picked else { return false }
        picked = i
        return true
    }

    /// Closes; the mark that was picked.
    @discardableResult
    func close() -> EdgeMark? {
        let mark = pickedMark
        marks = []
        column = nil
        picked = nil
        return mark
    }
}

/// The ticks and the picked message's first words, over the list (it takes no touch).
struct EdgeScrubOverlay: View {
    let state: EdgeScrubState

    var body: some View {
        if let column = state.column {
            GeometryReader { g in
                // The ticks' trailing ends line up just inside the list's right edge.
                let right = g.size.width - Theme.Space.xs
                ForEach(Array(column.first..<(column.first + column.count)), id: \.self) { i in
                    let picked = i == state.picked
                    let width = picked ? Theme.Size.edgeTickPicked : Theme.Size.edgeTick
                    Capsule()
                        .fill(picked ? AnyShapeStyle(Theme.accent) : AnyShapeStyle(.tertiary))
                        .frame(width: width, height: Theme.Size.rule)
                        .position(x: right - width / 2, y: column.y(of: i))
                }
                if let i = state.picked, let mark = state.pickedMark {
                    HStack(spacing: Theme.Space.flush) {
                        Spacer(minLength: 0)
                        Text(mark.text)
                            .font(Theme.Font.secondary)
                            .foregroundStyle(.primary)
                            .lineLimit(2)
                            .truncationMode(.tail)
                            .fixedSize(horizontal: false, vertical: true)
                            .padding(.horizontal, Theme.Space.m)
                            .padding(.vertical, Theme.Space.s)
                            // Opaque: it sits over the messages, whose words must not show through it.
                            .background(Theme.Shell.composer, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
                            .overlay {
                                RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                                    .strokeBorder(Theme.Shell.composerStroke, lineWidth: Theme.Size.hairline)
                            }
                            .accessibilityIdentifier("chat.edgeScrub.preview")
                    }
                    .frame(width: Theme.Size.box240)
                    .position(x: right - Theme.Size.edgeTickPicked - Theme.Space.m - Theme.Size.box240 / 2, y: column.y(of: i))
                }
            }
            .animation(Theme.Motion.state, value: state.picked)
            .transition(.opacity)
            .allowsHitTesting(false)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("chat.edgeScrub")
        }
    }
}

#if os(iOS)
/// The navigator's taps: a light one as it opens, a tick for each message passed.
@MainActor enum EdgeScrubHaptics {
    private static let impact = UIImpactFeedbackGenerator(style: .light)
    private static let selection = UISelectionFeedbackGenerator()

    static func open() {
        impact.impactOccurred()
        selection.prepare()
    }

    static func tick() {
        selection.selectionChanged()
        selection.prepare()
    }
}

/// The touch side of the navigator: begins in the band along the right edge (`band`, window coordinates) on a slide
/// more up-and-down than sideways; the list's scrolling waits for it to fail.
struct EdgeScrubGesture: UIGestureRecognizerRepresentable {
    var band: () -> CGRect
    /// Where the finger is (window coordinates) as the gesture begins, moves and ends.
    var changed: (CGPoint, UIGestureRecognizer.State) -> Void

    final class Recognizer: UIGestureRecognizer {
        var band: () -> CGRect = { .zero }
        private var start: CGPoint?
        /// How far the finger goes before its direction counts.
        private let slop = Theme.Space.s

        override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent) {
            guard start == nil, touches.count == 1, let p = touches.first?.location(in: nil), band().contains(p) else {
                if state == .possible { state = .failed }
                return
            }
            start = p
        }

        override func touchesMoved(_ touches: Set<UITouch>, with event: UIEvent) {
            guard let start, let p = touches.first?.location(in: nil) else { return }
            guard state == .possible else {
                state = .changed
                return
            }
            let dx = p.x - start.x, dy = p.y - start.y
            if abs(dx) > slop, abs(dx) >= abs(dy) {
                state = .failed // sideways: the strip's pager or the drawer
            } else if abs(dy) > slop {
                state = .began
            }
        }

        override func touchesEnded(_ touches: Set<UITouch>, with event: UIEvent) {
            state = state == .possible ? .failed : .ended
        }

        override func touchesCancelled(_ touches: Set<UITouch>, with event: UIEvent) {
            state = state == .possible ? .failed : .cancelled
        }

        override func reset() {
            super.reset()
            start = nil
        }
    }

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        /// A scroll view's pan (the list's) waits until the navigator gives up: a slide that starts in the band and
        /// goes up or down is the navigator's, not the list's.
        func gestureRecognizer(_ g: UIGestureRecognizer, shouldBeRequiredToFailBy other: UIGestureRecognizer) -> Bool {
            other.view is UIScrollView && other is UIPanGestureRecognizer
        }
    }

    func makeCoordinator(converter: CoordinateSpaceConverter) -> Coordinator { Coordinator() }

    func makeUIGestureRecognizer(context: Context) -> Recognizer {
        let r = Recognizer()
        r.delegate = context.coordinator
        r.band = band
        return r
    }

    func updateUIGestureRecognizer(_ recognizer: Recognizer, context: Context) {
        recognizer.band = band
    }

    func handleUIGestureRecognizerAction(_ recognizer: Recognizer, context: Context) {
        changed(recognizer.location(in: nil), recognizer.state)
    }
}
#endif

/// The navigator's gesture on the chat list, only where the new layout is on (iPhone and iPad).
#if os(iOS)
struct EdgeScrubbing: ViewModifier {
    var on: Bool
    var band: () -> CGRect
    var changed: (CGPoint, UIGestureRecognizer.State) -> Void

    func body(content: Content) -> some View {
        if on { content.gesture(EdgeScrubGesture(band: band, changed: changed)) } else { content }
    }
}
#else
struct EdgeScrubbing: ViewModifier {
    var on: Bool
    var band: () -> CGRect
    var changed: (CGPoint, Int) -> Void

    func body(content: Content) -> some View { content }
}
#endif
