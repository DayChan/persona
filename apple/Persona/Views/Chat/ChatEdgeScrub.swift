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
    static let previewLimit = 160
    var id: String
    var text: String
    /// Optional for synthetic marks without a transcript position.
    var seq: Int? = nil
    var answer: String? = nil
}

/// Every fetched mark remains addressable. Drawing can sample dense ticks, but hit testing never drops old inputs.
nonisolated struct EdgeScrubColumn: Equatable, Sendable {
    var first: Int
    var count: Int
    var gap: CGFloat
    var top: CGFloat
    /// A page arriving under the finger keeps that identity at the same y. The two sides scale independently.
    var pivot: Int? = nil
    var pivotY: CGFloat? = nil

    static func fit(marks: Int, height: CGFloat, top: CGFloat, bottom: CGFloat) -> EdgeScrubColumn? {
        let room = height - top - bottom
        guard marks > 0, room > 0 else { return nil }
        let gap = marks > 1 ? min(Theme.Size.edgeTickGapMax, room / CGFloat(marks - 1)) : 0
        let span = gap * CGFloat(marks - 1)
        return EdgeScrubColumn(first: 0, count: marks, gap: gap, top: top + (room - span) / 2)
    }

    func index(at y: CGFloat) -> Int {
        guard count > 1 else { return first }
        if let pivot, let pivotY, y == pivotY { return pivot }
        // Monotonic coordinates, including compressed sides after prepend. Binary search avoids walking history
        // on each finger movement, and preserves the exact selected identity at a page boundary.
        var low = first, high = first + count - 1
        while low < high {
            let mid = (low + high) / 2
            if self.y(of: mid) < y { low = mid + 1 } else { high = mid }
        }
        if low > first, abs(self.y(of: low - 1) - y) < abs(self.y(of: low) - y) { return low - 1 }
        return low
    }

    func y(of index: Int) -> CGFloat {
        guard let pivot, let pivotY else { return top + gap * CGFloat(index - first) }
        let end = first + count - 1
        if index <= pivot {
            return pivot == first ? pivotY : top + (pivotY - top) * CGFloat(index - first) / CGFloat(pivot - first)
        }
        let bottom = top + gap * CGFloat(count - 1)
        return pivot == end ? pivotY : pivotY + (bottom - pivotY) * CGFloat(index - pivot) / CGFloat(end - pivot)
    }

    /// Keep the visual minimum gap without restricting the index's selectable range.
    func drawnIndices(picked: Int?) -> [Int] {
        var out: [Int] = [], lastY: CGFloat?
        for i in first..<(first + count) {
            let y = y(of: i)
            if lastY == nil || y - lastY! >= Theme.Size.edgeTickGapMin || i == first + count - 1 {
                out.append(i)
                lastY = y
            }
        }
        if let picked, !out.contains(picked) { out.append(picked) }
        return out
    }
}

/// The navigator while a finger is on it: what it shows and which tick is picked. Its own object, so the slide redraws
/// only the overlay, never the chat page.
@MainActor @Observable final class EdgeScrubState {
    private(set) var marks: [EdgeMark] = []
    private(set) var column: EdgeScrubColumn?
    private(set) var picked: Int?
    private(set) var isGestureActive = false
    @ObservationIgnored private var passiveRevision: Int?
    @ObservationIgnored private var fadeTask: Task<Void, Never>?
    @ObservationIgnored private var fadeRevision = UUID()
    #if DEBUG
    @ObservationIgnored private(set) var lastGestureMark: EdgeMark?
    #endif
    private(set) var tickIndices: [Int] = []
    var drawnIndices: [Int] {
        guard let picked, !tickIndices.contains(picked) else { return tickIndices }
        return tickIndices + [picked]
    }
    @ObservationIgnored private var height: CGFloat = 0
    @ObservationIgnored private var header: CGFloat = 0
    @ObservationIgnored private var fingerY: CGFloat?
    @ObservationIgnored private(set) var movingOlder = false
    @ObservationIgnored private(set) var enteredOlderHalf = false

    var isOpen: Bool { column != nil }
    var pickedMark: EdgeMark? { picked.flatMap { marks.indices.contains($0) ? marks[$0] : nil } }
    var previewTop: CGFloat { header + Theme.Space.l }

    /// The messages the user typed, oldest first: not Persona's notes to the model, not other conversations', not a
    /// /loop tick.
    static func marks(_ lines: [ChatLine]) -> [EdgeMark] {
        var seen = Set<Int>()
        let textSeqs = Set(lines.compactMap { line -> Int? in
            if case .row(.user(let item)) = line.content { return item.seq }
            return nil
        })
        var marks = lines.compactMap { line -> EdgeMark? in
            let item: Item
            switch line.content {
            case .row(.user(let it)): item = it
            case .userImages(_, let items):
                guard let first = items.first, !textSeqs.contains(first.seq) else { return nil }
                item = first
            case .row(.image(let it)) where it.role == "user" && it.toolUseID == nil:
                guard !textSeqs.contains(it.seq) else { return nil }
                item = it
            default: return nil
            }
            guard Injected.of(item) == nil, item.from == nil, !seen.contains(item.seq) else { return nil }
            let raw = item.text ?? ""
            guard !raw.hasPrefix("/loop") else { return nil }
            seen.insert(item.seq)
            let shown = UserRowText(raw).shown.trimmingCharacters(in: .whitespacesAndNewlines)
            return EdgeMark(id: line.id, text: shown.isEmpty ? String(localized: "Photo [Chat Layout]") : String(shown.prefix(EdgeMark.previewLimit)), seq: item.seq)
        }
        let bySeq = Dictionary(marks.enumerated().compactMap { pair in pair.element.seq.map { ($0, pair.offset) } },
                               uniquingKeysWith: { first, _ in first })
        var active: Int?
        for line in lines {
            switch line.content {
            case .row(.user(let item)): active = bySeq[item.seq] // excluded inputs delimit, rather than donate, answers
            case .userImages(_, let images): active = images.first.flatMap { bySeq[$0.seq] }
            case .row(.image(let item)) where item.role == "user": active = bySeq[item.seq]
            case .row(.assistant(let item)):
                if let active, let text = item.text?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty {
                    marks[active].answer = String(text.prefix(EdgeMark.previewLimit))
                }
            default: break
            }
        }
        return marks
    }

    /// The input interval containing the viewport. No linear walk of all fetched marks during scrolling.
    static func index(at seq: Int?, marks: [EdgeMark]) -> Int? {
        guard let seq, !marks.isEmpty else { return nil }
        var low = 0, high = marks.count
        while low < high {
            let mid = (low + high) / 2
            if (marks[mid].seq ?? .max) <= seq { low = mid + 1 } else { high = mid }
        }
        return low > 0 ? low - 1 : nil
    }

    /// Passive discovery from the list's existing observer: never changes the body position or loads history.
    func passive(_ marks: [EdgeMark], revision: Int, seq: Int?, height: CGFloat, top: CGFloat, scrolling: Bool) {
        guard !isGestureActive, !marks.isEmpty else { return }
        if scrolling { cancelFade() }
        if column == nil || passiveRevision != revision || self.height != height || header != top {
            guard let column = EdgeScrubColumn.fit(marks: marks.count, height: height, top: top + Theme.Space.l,
                                                   bottom: Theme.Space.xxl) else { return }
            self.marks = marks
            self.column = column
            tickIndices = column.drawnIndices(picked: nil)
            self.height = height
            header = top
            passiveRevision = revision
        }
        let current = Self.index(at: seq, marks: self.marks)
        if picked != current { picked = current }
    }

    func refreshPassive(_ marks: [EdgeMark], revision: Int, seq: Int?) {
        guard isOpen, !isGestureActive else { return }
        passive(marks, revision: revision, seq: seq, height: height, top: header, scrolling: false)
    }

    private func cancelFade() {
        guard fadeTask != nil else { return }
        fadeRevision = UUID()
        fadeTask?.cancel()
        fadeTask = nil
    }

    func stopPassive(after delay: Duration = .seconds(Theme.Motion.edgeScrubLinger)) {
        guard isOpen, !isGestureActive else { return }
        cancelFade()
        let revision = UUID()
        fadeRevision = revision
        fadeTask = Task { @MainActor [weak self] in
            do { try await Task.sleep(for: delay) } catch { return }
            guard let self, self.fadeRevision == revision, !self.isGestureActive else { return }
            withAnimation(Theme.Motion.state) { self.close() }
        }
    }

    /// Opens on these marks for a list `height` tall whose top `top` is under the top bar, picking the tick nearest
    /// `y`. False when there is nothing to show.
    @discardableResult
    func open(_ marks: [EdgeMark], height: CGFloat, top: CGFloat, at y: CGFloat) -> Bool {
        guard let column = EdgeScrubColumn.fit(marks: marks.count, height: height, top: top + Theme.Space.l,
                                               bottom: Theme.Space.xxl) else { return false }
        cancelFade()
        isGestureActive = true
        passiveRevision = nil
        self.marks = marks
        self.height = height
        header = top
        self.column = column
        tickIndices = column.drawnIndices(picked: nil)
        fingerY = y
        movingOlder = false
        picked = column.index(at: y)
        enteredOlderHalf = (picked ?? marks.count) < marks.count / 2
        return true
    }

    /// An index page arrives during the gesture: the selected id, preview and finger mapping stay put.
    func update(_ marks: [EdgeMark]) {
        guard isOpen, !marks.isEmpty, let old = pickedMark,
              let selected = marks.firstIndex(where: { $0.id == old.id || (old.seq != nil && $0.seq == old.seq) }),
              var column = EdgeScrubColumn.fit(marks: marks.count, height: height, top: header + Theme.Space.l,
                                              bottom: Theme.Space.xxl) else { return }
        column.pivot = selected
        column.pivotY = min(column.y(of: marks.count - 1), max(column.top, fingerY ?? self.column?.y(of: picked ?? 0) ?? column.top))
        var updated = marks
        updated[selected].id = old.id
        updated[selected].text = old.text
        updated[selected].answer = old.answer
        self.marks = updated
        self.column = column
        tickIndices = column.drawnIndices(picked: nil)
        picked = selected
        enteredOlderHalf = false // arrival is not another user movement; never chain-fetch the entire index
    }

    /// Picks a mark by its index (`-showEdgeScrub`, screenshots).
    func pick(_ index: Int) {
        guard let column, (column.first..<(column.first + column.count)).contains(index) else { return }
        picked = index
        fingerY = column.y(of: index)
    }

    /// The finger moved: true when another tick is picked.
    func move(to y: CGFloat) -> Bool {
        guard isGestureActive, let column else { return false }
        movingOlder = fingerY.map { y < $0 } ?? false
        let wasOlder = (picked ?? marks.count) < marks.count / 2
        fingerY = y
        let i = column.index(at: y)
        enteredOlderHalf = i < marks.count / 2 && !wasOlder
        guard i != picked else { return false }
        picked = i
        return true
    }

    /// Closes; the mark that was picked.
    @discardableResult
    func close() -> EdgeMark? {
        let mark = isGestureActive ? pickedMark : nil
        #if DEBUG
        if let mark { lastGestureMark = mark }
        #endif
        cancelFade()
        isGestureActive = false
        passiveRevision = nil
        marks = []
        column = nil
        tickIndices = []
        picked = nil
        fingerY = nil
        movingOlder = false
        enteredOlderHalf = false
        return mark
    }
}

/// Unobserved scroll bookkeeping, rebuilt with ChatView's cached lines. Per-frame lookups visit visible ids only.
@MainActor final class EdgeScrubViewport {
    private var lines: [String: (order: Int, seq: Int?)] = [:]
    private var visible: [String] = []
    private var loaded: [EdgeMark] = []
    private var indexed: [EdgeMark] = []
    private var loadedHistory: String?
    private var indexedHistory: String?
    private(set) var marks: [EdgeMark] = []
    private(set) var revision = 0

    var currentSequence: Int? {
        var first: (order: Int, seq: Int?)?
        for id in visible {
            if let line = lines[id], first == nil || line.order < first!.order { first = line }
        }
        return first?.seq
    }

    func setVisible(_ ids: [String]) { visible = ids }

    func rebuild(_ source: [ChatLine], history: String?) {
        loaded = EdgeScrubState.marks(source)
        loadedHistory = history
        let inputs = Set(loaded.compactMap(\.seq))
        var current: Int?
        var positions: [String: (order: Int, seq: Int?)] = [:]
        for (order, line) in source.enumerated() {
            let seq: Int?
            switch line.content {
            case .row(.user(let item)), .row(.image(let item)): seq = inputs.contains(item.seq) ? item.seq : nil
            case .userImages(_, let images): seq = images.first.flatMap { inputs.contains($0.seq) ? $0.seq : nil }
            default: seq = nil
            }
            if let seq { current = seq }
            positions[line.id] = (order, current)
        }
        lines = positions
        mergeMarks()
    }

    func setIndex(_ marks: [EdgeMark], history: String?) {
        indexed = marks
        indexedHistory = history
        mergeMarks()
    }

    private func mergeMarks() {
        let compatible = loadedHistory == nil || indexedHistory == nil || loadedHistory == indexedHistory
        var bySeq: [Int: EdgeMark] = [:]
        if compatible {
            for mark in loaded { if let seq = mark.seq { bySeq[seq] = mark } }
        }
        for var mark in indexed {
            guard let seq = mark.seq else { continue }
            mark.answer = mark.answer ?? bySeq[seq]?.answer
            bySeq[seq] = mark
        }
        marks = bySeq.values.sorted { ($0.seq ?? 0) < ($1.seq ?? 0) }
        revision += 1
    }
}

/// Observes index pages inside the overlay, not in the chat page's per-frame rendering graph.
struct SessionNavigationOverlay: View {
    let navigation: SessionNavigationModel
    let scrub: EdgeScrubState
    let viewport: EdgeScrubViewport

    var body: some View {
        // Keep observers and the DEBUG probe mounted when the tick view is empty, without intercepting touches.
        ZStack(alignment: .topLeading) {
            Color.clear
            EdgeScrubOverlay(state: scrub)
            #if DEBUG
            EdgeNavigationProbe(navigation: navigation, scrub: scrub)
            #endif
        }
            .allowsHitTesting(false)
            .overlay(alignment: .topTrailing) {
                if navigation.loadingTarget {
                    ProgressView().controlSize(.small).padding(Theme.Space.m).allowsHitTesting(false)
                }
            }
            .onAppear { viewport.setIndex(navigation.marks, history: navigation.history) }
            .onChange(of: navigation.history) { old, new in
                viewport.setIndex(navigation.marks, history: new)
                if old != nil, old != new { scrub.close() }
            }
            .onChange(of: navigation.marks) { _, marks in
                viewport.setIndex(marks, history: navigation.history)
                if scrub.isGestureActive {
                    if marks.isEmpty { scrub.close() } else { scrub.update(viewport.marks) }
                } else {
                    scrub.refreshPassive(viewport.marks, revision: viewport.revision, seq: viewport.currentSequence)
                }
            }
    }
}

#if DEBUG
/// A small self-observing fixture probe: it never makes finger movement redraw ChatView.
private struct EdgeNavigationProbe: View {
    let navigation: SessionNavigationModel
    let scrub: EdgeScrubState
    private struct Snapshot: Encodable, Equatable {
        var mode: String
        var pickedSeq: Int?
        var marks: Int
        var fetched: Int
        var lastPickedSeq: Int?
        var lastText: String?
        var lastAnswer: String?
        var window: Bool
    }
    var body: some View {
        Color.clear.frame(width: Theme.Size.line, height: Theme.Size.line)
            .screenProbe("session-navigation", page: "assistant",
                         Snapshot(mode: scrub.isGestureActive ? "gesture" : (scrub.isOpen ? "passive" : "hidden"),
                                  pickedSeq: scrub.pickedMark?.seq, marks: scrub.marks.count, fetched: navigation.marks.count,
                                  lastPickedSeq: scrub.lastGestureMark?.seq, lastText: scrub.lastGestureMark?.text,
                                  lastAnswer: scrub.lastGestureMark?.answer, window: navigation.window != nil))
    }
}
#endif

/// The ticks and the picked message's first words, over the list (it takes no touch).
struct EdgeScrubOverlay: View {
    let state: EdgeScrubState
    @State private var previewHeight: CGFloat = 0

    var body: some View {
        if let column = state.column {
            GeometryReader { g in
                // The ticks' trailing ends line up just inside the list's right edge.
                let right = g.size.width - Theme.Space.xs
                ForEach(state.drawnIndices, id: \.self) { i in
                    let picked = i == state.picked
                    let width = picked ? Theme.Size.edgeTickPicked : Theme.Size.edgeTick
                    Capsule()
                        .fill(picked ? AnyShapeStyle(Theme.accent) : AnyShapeStyle(.tertiary))
                        .frame(width: width, height: Theme.Size.rule)
                        .position(x: right - width / 2, y: column.y(of: i))
                }
                if state.isGestureActive, let i = state.picked, let mark = state.pickedMark {
                    HStack(spacing: Theme.Space.flush) {
                        Spacer(minLength: 0)
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Text(mark.text)
                                .font(Theme.Font.secondary)
                                .foregroundStyle(.primary)
                                .lineLimit(2)
                                .truncationMode(.tail)
                            if let answer = mark.answer, !answer.isEmpty {
                                Text(answer)
                                    .font(Theme.Font.caption)
                                    .foregroundStyle(.secondary)
                                    .lineLimit(2)
                                    .truncationMode(.tail)
                            }
                        }
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
                            .onGeometryChange(for: CGFloat.self, of: { $0.size.height }) { previewHeight = $0 }
                    }
                    .frame(width: Theme.Size.box240)
                    .position(x: right - Theme.Size.edgeTickPicked - Theme.Space.m - Theme.Size.box240 / 2,
                              y: min(g.size.height - Theme.Space.xxl - previewHeight / 2,
                                     max(state.previewTop + previewHeight / 2, column.y(of: i))))
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
