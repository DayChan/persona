import SwiftUI
import Testing
@testable import Persona

@MainActor
struct TabActivityTests {
    private final class Calls {
        var log: [String] = []
    }

    private func update(_ state: TabVisibilityState, _ calls: Calls, mounted: Bool? = nil, active: Bool? = nil) {
        state.update(mounted: mounted, active: active, appear: { calls.log.append("appear") }, disappear: { calls.log.append("disappear") })
    }

    @Test func appearsOnceWhenInTheWindowAndInFront() {
        let state = TabVisibilityState(), calls = Calls()
        update(state, calls, mounted: true, active: true)
        update(state, calls, mounted: true, active: true) // a second onAppear (back from a pushed page) without a disappear
        #expect(calls.log == ["appear"])
        #expect(state.visible)
    }

    @Test func goingToTheBackDisappearsOnceThenLeavingTheWindowDoesNothing() {
        let state = TabVisibilityState(), calls = Calls()
        update(state, calls, mounted: true, active: true)
        update(state, calls, active: false) // a container that keeps the tab in the window
        update(state, calls, mounted: false, active: false) // the system TabView takes it out as well
        #expect(calls.log == ["appear", "disappear"])
        #expect(!state.visible)
    }

    @Test func aHiddenTabPutBackInTheWindowDoesNotAppear() {
        let state = TabVisibilityState(), calls = Calls()
        update(state, calls, mounted: true, active: true)
        update(state, calls, mounted: false, active: false)
        update(state, calls, mounted: true, active: false) // the system briefly puts the old tab back on the way out
        update(state, calls, mounted: false, active: false)
        #expect(calls.log == ["appear", "disappear"])
    }

    @Test func retainedChatReleasesItsStreamAndResumesOncePerSwitch() {
        let state = TabVisibilityState(), calls = Calls()
        update(state, calls, mounted: true, active: false)
        #expect(calls.log.isEmpty, "building a hidden chat must not subscribe")
        for _ in 0..<20 {
            update(state, calls, active: true)
            update(state, calls, active: true)
            update(state, calls, active: false)
            update(state, calls, active: false)
        }
        #expect(calls.log == Array(repeating: ["appear", "disappear"], count: 20).flatMap { $0 })
        update(state, calls, mounted: false)
        #expect(calls.log.count == 40, "unmounting an already hidden chat must not release another owner")
    }

    @Test func chatVisibilityPreservesStoreAndOtherSubscriptionOwners() {
        let model = AppModel()
        // This group has no API or socket: never read a test runner's paired hub.
        let key = SessionKey(group: "visibility-fixture", machineID: "box", sessionID: "visibility-fixture")
        let owner = UUID(), other = UUID(), state = TabVisibilityState()
        let store = model.chat(key)
        let appear = {
            model.subscribe(key, by: owner)
            model.show(key, by: owner)
        }
        let disappear = {
            model.unsubscribe(key, by: owner)
            model.hide(by: owner)
        }
        state.update(mounted: true, active: false, appear: appear, disappear: disappear)
        #expect(!model.isSubscribed(key) && model.visibleSession != key)
        state.update(active: true, appear: appear, disappear: disappear)
        #expect(model.isSubscribed(key) && model.visibleSession == key)
        state.update(active: false, appear: appear, disappear: disappear)
        #expect(!model.isSubscribed(key) && model.visibleSession != key)
        state.update(active: true, appear: appear, disappear: disappear)
        #expect(model.chat(key) === store, "returning must not reload or rebuild the chat store")
        model.subscribe(key, by: other)
        model.show(key, by: other)
        state.update(active: false, appear: appear, disappear: disappear)
        #expect(model.isSubscribed(key) && model.visibleSession == key, "another visible view still owns the stream")
        model.unsubscribe(key, by: other)
        model.hide(by: other)
        #expect(!model.isSubscribed(key) && model.visibleSession != key)
    }

    @Test func comingBackToTheFrontAppearsAgainWhicheverArrivesFirst() {
        let state = TabVisibilityState(), calls = Calls()
        update(state, calls, mounted: true, active: true)
        update(state, calls, mounted: false, active: false)
        update(state, calls, active: true) // the tab is picked before its view is back in the window
        #expect(calls.log == ["appear", "disappear"])
        update(state, calls, mounted: true, active: true)
        #expect(calls.log == ["appear", "disappear", "appear"])
    }
}

#if os(iOS)
/// The helpers on a real view: a page that stays in the window (the shell's hidden page) still has its work cancelled.
@MainActor
struct TabActiveTaskTests {
    private final class Calls {
        var log: [String] = []
    }

    @Observable @MainActor final class Front {
        var active = true
        var phase: ScenePhase = .active
        var key = 0
    }

    private struct Page: View {
        let front: Front
        let calls: Calls

        var body: some View {
            Color.clear
                .tabActiveTask(id: front.key) {
                    calls.log.append("start")
                    await withTaskCancellationHandler {
                        try? await Task.sleep(for: .seconds(60))
                    } onCancel: {
                        Task { @MainActor in calls.log.append("cancel") }
                    }
                }
                .environment(\.tabIsActive, front.active)
                .environment(\.scenePhase, front.phase)
        }
    }

    private func settle() async { try? await Task.sleep(for: .milliseconds(300)) }

    @Test func aPageStillInTheWindowStopsItsWorkWhenItGoesToTheBack() async {
        let front = Front(), calls = Calls()
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 200, height: 200))
        window.rootViewController = UIHostingController(rootView: Page(front: front, calls: calls))
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        await settle()
        #expect(calls.log == ["start"])

        front.active = false
        await settle()
        #expect(calls.log == ["start", "cancel"]) // cancelled, and not started again while behind

        front.active = true
        await settle()
        #expect(calls.log == ["start", "cancel", "start"]) // back in front: from the top
    }

    @Test func backgroundCancelsAndManualKeysCannotStartUntilOneResume() async {
        let front = Front(), calls = Calls()
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 200, height: 200))
        window.rootViewController = UIHostingController(rootView: Page(front: front, calls: calls))
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        await settle()
        #expect(calls.log == ["start"])

        front.phase = .background
        await settle()
        #expect(calls.log == ["start", "cancel"])
        front.key += 1
        await settle()
        front.active = false
        front.key += 1
        await settle()
        #expect(calls.log == ["start", "cancel"], "manual refresh keys cannot start background work")

        front.phase = .active
        await settle()
        #expect(calls.log == ["start", "cancel"], "an active scene does not resume a hidden tab")
        front.active = true
        await settle()
        #expect(calls.log == ["start", "cancel", "start"])
        front.phase = .inactive
        await settle()
        #expect(calls.log == ["start", "cancel", "start", "cancel"])
        front.phase = .active
        await settle()
        #expect(calls.log == ["start", "cancel", "start", "cancel", "start"])
    }

    @Test func initiallyBackgroundManualKeyWaitsForForeground() async {
        let front = Front(), calls = Calls()
        front.phase = .background
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 200, height: 200))
        window.rootViewController = UIHostingController(rootView: Page(front: front, calls: calls))
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        await settle()
        front.key += 1
        await settle()
        #expect(calls.log.isEmpty)
        front.phase = .active
        await settle()
        #expect(calls.log == ["start"])
    }
}
#endif
