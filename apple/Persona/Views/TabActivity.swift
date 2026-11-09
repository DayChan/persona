import SwiftUI

// Which page is in front, for the two kinds of background work that should only run there: polling and read
// reports (docs/apple/ios.md「只在前台做的事」).
//
// The system TabView takes a tab out of the window when another is picked: its onDisappear runs and its tasks are
// cancelled. The shell's two pages (Assistant, Agents) are both kept in the window and only made transparent, so nothing
// stops a hidden page's polling or its read reports. They key off `tabIsActive` as well: they run while the view is in
// the window *and* its page is in front. Under the system TabView this is what happens already (the two change
// together), and a hidden tab that the system briefly puts back in the window no longer restarts its work.
//
// A visible chat's transcript subscription also uses this scope: switching pages must not render a hidden reply or
// mark it read. The Agent still runs on the hub; returning resumes from the retained sequence. Dictation keeps its
// own lifetime; display clocks use `VisibleWork` rather than changing the tab callbacks' voice-call semantics.

extension EnvironmentValues {
    /// Whether this view's tab is the one picked (`TabActivityScope`). True outside any tab.
    @Entry var tabIsActive = true
}

/// Set on each tab's content in the platform TabView: whether it is in front (`tabIsActive`).
@MainActor
struct TabActivityScope: ViewModifier {
    let tab: AppTab
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        let active = model.selectedTab == tab
        #if PERF_SIGNPOST
        let _ = PerfTrace.event("Tab activity scope", "tab=\(tab.rawValue) active=\(active)")
        #endif
        content.environment(\.tabIsActive, active)
    }
}

/// Whether a view is on screen in front: in the window and its tab picked. `appear` and `disappear` run in pairs, once
/// each time that changes (the system can put a hidden tab back in the window for a moment; that is not an appear).
@MainActor
final class TabVisibilityState {
    private var mounted = false
    private var active = false
    private(set) var visible = false

    func update(mounted: Bool? = nil, active: Bool? = nil, appear: () -> Void, disappear: () -> Void) {
        if let mounted { self.mounted = mounted }
        if let active { self.active = active }
        let next = self.mounted && self.active
        guard next != visible else { return }
        #if PERF_SIGNPOST
        PerfTrace.event("Tab visibility transition", "state=\(ObjectIdentifier(self).hashValue) mounted=\(self.mounted) active=\(self.active) visible=\(next)")
        #endif
        visible = next
        if next { appear() } else { disappear() }
    }
}

@MainActor
private struct TabVisibilityScope: ViewModifier {
    let appear: () -> Void
    let disappear: () -> Void
    @Environment(\.tabIsActive) private var active
    @State private var state = TabVisibilityState()

    func body(content: Content) -> some View {
        content
            .onAppear { state.update(mounted: true, active: active, appear: appear, disappear: disappear) }
            .onDisappear { state.update(mounted: false, active: active, appear: appear, disappear: disappear) }
            .onChange(of: active) { _, active in state.update(active: active, appear: appear, disappear: disappear) }
    }
}

nonisolated private struct TabActiveTaskID<ID: Hashable & Sendable>: Hashable, Sendable {
    var active: Bool
    var value: ID
}

@MainActor
private struct TabActiveTask<ID: Hashable & Sendable>: ViewModifier {
    let id: ID
    let action: @MainActor () async -> Void
    @Environment(\.tabIsActive) private var active
    @Environment(\.scenePhase) private var phase

    private var eligible: Bool { active && VisibleScenePolicy.permits(phase) }

    func body(content: Content) -> some View {
        content.task(id: TabActiveTaskID(active: eligible, value: id)) {
            guard eligible, !Task.isCancelled else { return }
            #if PERF_SIGNPOST
            PerfTrace.event("Tab active task", "key=\(id.hashValue)")
            defer { PerfTrace.event("Tab active task finish", "key=\(id.hashValue) cancelled=\(Task.isCancelled)") }
            #endif
            await action()
        }
    }
}

extension View {
    /// `onAppear` / `onDisappear` for work that belongs to the page in front: `appear` when the view is in the window and
    /// its tab is picked, `disappear` when either stops.
    func onTabVisibility(appear: @escaping () -> Void, disappear: @escaping () -> Void = {}) -> some View {
        modifier(TabVisibilityScope(appear: appear, disappear: disappear))
    }

    /// `.task` for the page in front; on iOS and visionOS it also requires an active scene. Cancelled when ineligible,
    /// started again (from the top) when eligible.
    func tabActiveTask(_ action: @escaping @MainActor () async -> Void) -> some View {
        modifier(TabActiveTask(id: true, action: action))
    }

    /// `.task(id:)` that runs only while its tab is in front.
    func tabActiveTask<ID: Hashable & Sendable>(id: ID, _ action: @escaping @MainActor () async -> Void) -> some View {
        modifier(TabActiveTask(id: id, action: action))
    }
}

nonisolated enum VisibleScenePolicy {
    static func permits(_ phase: ScenePhase) -> Bool {
        #if os(iOS) || os(visionOS)
        phase == .active
        #else
        // A visible Mac window stays useful when another application takes focus.
        true
        #endif
    }
}

/// Display-only work: mounted, on the selected tab, in an active phone scene and at least partly on screen.
/// Outside a scroll container no visibility callback arrives, so mounted content is visible by default.
/// This does not send scene-driven disappear callbacks to business lifecycles such as voice calls.
struct VisibleWork<Content: View>: View {
    @Environment(\.tabIsActive) private var tabActive
    @Environment(\.scenePhase) private var phase
    @State private var visibility = VisibleWorkState()
    @ViewBuilder var content: (Bool) -> Content

    var body: some View {
        content(visibility.eligible(tabActive: tabActive, sceneActive: VisibleScenePolicy.permits(phase)))
            .onAppear { visibility.mounted = true }
            .onDisappear { visibility.unmount() }
            .onScrollVisibilityChange(threshold: 0.01) { visibility.onScreen = $0 }
    }
}

nonisolated struct VisibleWorkState {
    var mounted = false
    var onScreen = true

    func eligible(tabActive: Bool, sceneActive: Bool) -> Bool {
        mounted && onScreen && tabActive && sceneActive
    }

    mutating func unmount() {
        mounted = false
        onScreen = true
    }
}

/// A paused display clock emits its current entry once, then finishes: no hidden periodic timer is retained.
/// Resuming rebuilds the schedule from the current wall time, rather than replaying missed display ticks.
nonisolated struct VisiblePeriodicSchedule: TimelineSchedule {
    var interval: TimeInterval
    var paused: Bool

    func entries(from startDate: Date, mode: TimelineScheduleMode) -> Entries {
        Entries(nextDate: startDate, interval: paused ? nil : interval)
    }

    nonisolated struct Entries: Sequence, IteratorProtocol {
        var nextDate: Date?
        var interval: TimeInterval?

        mutating func next() -> Date? {
            guard let date = nextDate else { return nil }
            nextDate = nil
            if let interval, interval.isFinite, interval > 0 {
                let next = date.addingTimeInterval(interval)
                if next.timeIntervalSinceReferenceDate.isFinite, next > date { nextDate = next }
            }
            return date
        }
    }
}

/// Relative and elapsed display text, with its timer owned by the text's actual visibility.
struct VisibleTimelineView<Content: View>: View {
    var interval: TimeInterval
    @ViewBuilder var content: (Date) -> Content

    var body: some View {
        VisibleWork { eligible in
            TimelineView(VisiblePeriodicSchedule(interval: interval, paused: !eligible)) { context in
                content(context.date)
            }
        }
    }
}

extension View {
    /// `.searchable` for a page that may be kept in the window while another one is in front (the Mac's sections,
    /// `MacPageStack`): only the page in front puts its field in the window's toolbar. Two pages' fields there at once
    /// are a crash (`NSToolbar … already contains an item with the identifier com.apple.SwiftUI.search`). On the Mac the
    /// field hangs off a view in the page's background, so the page's own views do not see `isSearching` /
    /// `dismissSearch`; use `isPresented` instead.
    func pageSearchable(text: Binding<String>, isPresented: Binding<Bool>? = nil, placement: SearchFieldPlacement = .automatic,
                        prompt: String) -> some View {
        modifier(PageSearchable(text: text, isPresented: isPresented, placement: placement, prompt: prompt))
    }
}

private struct PageSearchable: ViewModifier {
    let text: Binding<String>
    let isPresented: Binding<Bool>?
    let placement: SearchFieldPlacement
    let prompt: String
    @Environment(\.tabIsActive) private var active

    func body(content: Content) -> some View {
        #if os(macOS)
        // On a view of its own beside the content, so the content keeps its identity when the field comes and goes.
        content.background { if active { field(Color.clear) } }
        #else
        field(content)
        #endif
    }

    @ViewBuilder private func field<V: View>(_ v: V) -> some View {
        if let isPresented {
            v.searchable(text: text, isPresented: isPresented, placement: placement, prompt: Text(prompt))
        } else {
            v.searchable(text: text, placement: placement, prompt: Text(prompt))
        }
    }
}

extension View {
    /// `.toolbar` for a page that may be kept in the window while another one is in front (the Mac's sections,
    /// `MacPageStack`): only the page in front puts its items in the window's toolbar; a hidden page's buttons showed
    /// there beside the shown one's. On the iPhone and iPad each page has its own bar: the same as `.toolbar`.
    func pageToolbar<Items: ToolbarContent>(@ToolbarContentBuilder _ items: () -> Items) -> some View {
        modifier(PageToolbar(items: items()))
    }
}

private struct PageToolbar<Items: ToolbarContent>: ViewModifier {
    let items: Items
    @Environment(\.tabIsActive) private var active

    func body(content: Content) -> some View {
        #if os(macOS)
        // Taken out behind, not `.hidden(!active)`: AppKit lays the toolbar out again either way, and with every page's
        // items kept hidden in it the switch's long frame grew from about 85 ms to 150 (#390).
        content.toolbar { if active { items } }
        #else
        content.toolbar { items }
        #endif
    }
}

/// Tells `change` whether its page is in front (`tabIsActive`), at once and on every change, from a view of its own:
/// a page that read `tabIsActive` in its body was drawn again, rows and all, each time it came to the front or went
/// behind (the Mac's sections, #390). Put it in the page's background.
struct PageFrontWatcher: View {
    /// Also told once as it appears.
    var initial = true
    let change: (Bool) -> Void
    @Environment(\.tabIsActive) private var active

    var body: some View {
        Color.clear
            .allowsHitTesting(false)
            .accessibilityHidden(true)
            .onChange(of: active, initial: initial) { _, now in change(now) }
    }
}
