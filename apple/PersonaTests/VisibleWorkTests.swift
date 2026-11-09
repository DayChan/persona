import SwiftUI
import Testing
@testable import Persona

struct VisiblePeriodicScheduleTests {
    @Test func phoneSceneInactivityDoesNotChangeMacFocusSemantics() {
        #expect(VisibleScenePolicy.permits(.active))
        #if os(iOS) || os(visionOS)
        #expect(!VisibleScenePolicy.permits(.inactive))
        #expect(!VisibleScenePolicy.permits(.background))
        #else
        #expect(VisibleScenePolicy.permits(.inactive))
        #expect(VisibleScenePolicy.permits(.background))
        #endif
    }

    @Test func pausedScheduleHasOneCurrentEntryAndThenEnds() {
        let now = Date(timeIntervalSinceReferenceDate: 1234)
        var entries = VisiblePeriodicSchedule(interval: 30, paused: true).entries(from: now, mode: .normal)
        #expect(entries.next() == now)
        #expect(entries.next() == nil)
        #expect(entries.next() == nil)
    }

    @Test func activeScheduleAdvancesAndResumeStartsAtCurrentTime() {
        let start = Date(timeIntervalSinceReferenceDate: 1234)
        var entries = VisiblePeriodicSchedule(interval: 1, paused: false).entries(from: start, mode: .normal)
        #expect(entries.next() == start)
        #expect(entries.next() == start.addingTimeInterval(1))
        #expect(entries.next() == start.addingTimeInterval(2))
        let resume = start.addingTimeInterval(120)
        var resumed = VisiblePeriodicSchedule(interval: 1, paused: false).entries(from: resume, mode: .normal)
        #expect(resumed.next() == resume)
        #expect(resumed.next() == resume.addingTimeInterval(1))
    }

    @Test(arguments: [0.0, -1.0, Double.infinity, -Double.infinity, Double.nan])
    func invalidIntervalsEndInsteadOfSpinning(interval: Double) {
        let now = Date(timeIntervalSinceReferenceDate: 1234)
        var entries = VisiblePeriodicSchedule(interval: interval, paused: false).entries(from: now, mode: .normal)
        #expect(entries.next() == now)
        #expect(entries.next() == nil)
    }

    @Test func subPrecisionIntervalCannotRepeatTheSameDateForever() {
        let now = Date(timeIntervalSinceReferenceDate: 1234)
        var entries = VisiblePeriodicSchedule(interval: .leastNonzeroMagnitude, paused: false).entries(from: now, mode: .normal)
        #expect(entries.next() == now)
        #expect(entries.next() == nil)
    }

    @Test func reduceMotionRetainsTheStateButRemovesItsBreathingDecoration() {
        #expect(StatusDot.showsBreathingRing(state: .needsYou, reduceMotion: false))
        #expect(!StatusDot.showsBreathingRing(state: .needsYou, reduceMotion: true))
        #expect(!StatusDot.showsBreathingRing(state: .working, reduceMotion: false))
    }

    @Test func visibilityRequiresAllOwnersAndUnmountResetsScrollState() {
        var state = VisibleWorkState()
        #expect(!state.eligible(tabActive: true, sceneActive: true))
        state.mounted = true
        #expect(state.eligible(tabActive: true, sceneActive: true), "non-scroll content defaults to visible")
        #expect(!state.eligible(tabActive: false, sceneActive: true))
        #expect(!state.eligible(tabActive: true, sceneActive: false))
        state.onScreen = false
        #expect(!state.eligible(tabActive: true, sceneActive: true))
        state.unmount()
        #expect(!state.eligible(tabActive: true, sceneActive: true))
        state.mounted = true
        #expect(state.eligible(tabActive: true, sceneActive: true))
    }
}

#if os(iOS) || os(visionOS)
import UIKit

/// Actual hosting scopes, including a partly visible row and a non-scrolling decoration.
@MainActor @Suite(.serialized) struct VisibleWorkTests {
    @Observable @MainActor final class Front {
        var active = true
        var phase: ScenePhase = .active
        var target = "top"
    }

    private struct ScrollPage: View {
        var front: Front
        var body: some View {
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(spacing: 0) {
                        Color.clear.frame(height: 180).id("top")
                        VisibleWork { eligible in
                            BreathingRing(color: .primary, eligible: eligible).frame(width: 100, height: 100)
                        }.id("probe")
                        Color.clear.frame(height: 300).id("bottom")
                    }
                }
                .onChange(of: front.target) { _, target in proxy.scrollTo(target, anchor: .top) }
            }
            .environment(\.tabIsActive, front.active)
            .environment(\.scenePhase, front.phase)
            .ignoresSafeArea()
        }
    }

    private struct Page: View {
        var front: Front
        var body: some View {
            VisibleWork { eligible in
                BreathingRing(color: .primary, eligible: eligible).frame(width: 40, height: 40)
            }
                .environment(\.tabIsActive, front.active)
                .environment(\.scenePhase, front.phase)
            }
    }

    private final class ClockCalls {
        var dates: [Date] = []
    }

    private struct ClockPage: View {
        var front: Front
        var calls: ClockCalls
        var body: some View {
            VisibleTimelineView(interval: 0.05) { now in
                Color.clear.onChange(of: now, initial: true) { _, date in calls.dates.append(date) }
            }
            .environment(\.tabIsActive, front.active)
            .environment(\.scenePhase, front.phase)
        }
    }

    private func ring(in view: UIView) -> BreathingRingView? {
        if let ring = view as? BreathingRingView { return ring }
        return view.subviews.lazy.compactMap { ring(in: $0) }.first
    }

    private func makeWindow() throws -> UIWindow {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 200, height: 200)
        return window
    }

    private func settle() async { try? await Task.sleep(for: .milliseconds(300)) }

    @Test func displayClockActuallyStopsTicksInHiddenTabsAndBackgroundScenes() async throws {
        let front = Front(), calls = ClockCalls()
        let window = try makeWindow()
        window.rootViewController = UIHostingController(rootView: ClockPage(front: front, calls: calls))
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        await settle()
        #expect(calls.dates.count > 1)
        front.active = false
        await settle() // the paused schedule is allowed its single current entry
        let hidden = calls.dates.count
        await settle()
        #expect(calls.dates.count == hidden, "hidden content must not retain a periodic timer")
        front.active = true
        await settle()
        #expect(calls.dates.count > hidden)
        front.phase = .background
        await settle()
        let background = calls.dates.count
        await settle()
        #expect(calls.dates.count == background)
        front.phase = .active
        await settle()
        #expect(calls.dates.count > background)
    }

    @Test func nonScrollRingStopsForTabSceneAndUnmount() async throws {
        let front = Front()
        let window = try makeWindow()
        window.rootViewController = UIHostingController(rootView: Page(front: front))
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        await settle()
        let view = try #require(ring(in: window))
        #expect(view.ring.animation(forKey: BreathingRingView.key) != nil)
        front.active = false
        await settle()
        #expect(view.ring.animation(forKey: BreathingRingView.key) == nil)
        front.active = true
        await settle()
        #expect(view.ring.animation(forKey: BreathingRingView.key) != nil)
        front.phase = .background
        await settle()
        #expect(view.ring.animation(forKey: BreathingRingView.key) == nil)
        front.phase = .active
        await settle()
        #expect(view.ring.animation(forKey: BreathingRingView.key) != nil)
        window.rootViewController = nil
        await settle()
        #expect(view.ring.animation(forKey: BreathingRingView.key) == nil)
    }

    @Test func partiallyVisibleScrollRowMovesButOffscreenRowStops() async throws {
        let front = Front()
        let window = try makeWindow()
        window.rootViewController = UIHostingController(rootView: ScrollPage(front: front))
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        await settle()
        let view = try #require(ring(in: window))
        let frame = view.convert(view.bounds, to: window)
        #expect(frame.minY < window.bounds.maxY && frame.maxY > window.bounds.maxY, "fixture must really be partly visible")
        #expect(view.ring.animation(forKey: BreathingRingView.key) != nil, "a partially visible row still moves")
        front.target = "bottom"
        await settle()
        #expect(view.ring.animation(forKey: BreathingRingView.key) == nil)
        front.target = "probe"
        await settle()
        #expect(view.ring.animation(forKey: BreathingRingView.key) != nil)
    }
}
#endif
