import BeautifulUI
import SwiftUI

#if os(iOS) || os(visionOS)
/// The hub's link in the shell (docs/apple/shell.md「连接状态和 toast」): Waiting for Network / Connecting… / Updating…, floating just under
/// the top bar like a toast, in the toast window (`ToastWindow`): it takes no touches and moves nothing (#413). It used
/// to take a row of room under the status bar through the window's safe area, and folding that row made SwiftUI lay
/// Agents' navigation stack out again: the list jumped some 210 pt for a few frames and, 2 times in 12, the navigation
/// bar folded the search field pulled out of it.
///
/// The app's toasts float the same way, under the link's pill while it shows (#1053).
@MainActor @Observable final class ShellNotices {
    enum Item: Equatable {
        case link(ConnectionStatus.Phase)
    }

    /// What shows; nil while the hub is reached.
    private(set) var shown: Item?
    /// How many times the link's state came and went (`probe.shell`: a toast must not bring it).
    private(set) var opened = 0
    private(set) var folded = 0
    /// How many views in front say the link in their own title (an agent's chat, Inbox, Machines: `titleSaysLink()`). While
    /// any does the pill stays away: it repeated Connecting… just under the chat's title.
    private(set) var titles = 0
    /// The link's state as the pill shows it: nil while the hub is reached or a title in front already says it.
    var pill: ConnectionStatus.Phase? {
        guard titles == 0, case .link(let phase)? = shown else { return nil }
        return phase
    }

    func titleShowsLink(_ on: Bool) {
        titles = max(0, titles + (on ? 1 : -1))
    }

    func linkChanged(_ phase: ConnectionStatus.Phase) {
        let want: Item? = phase == .online ? nil : .link(phase)
        guard want != shown else { return }
        if want == nil { folded += 1 } else if shown == nil { opened += 1 }
        #if DEBUG
        ShellTrace.shared.note("link \(want == nil ? "fine" : "shows")")
        #endif
        withAnimation(Theme.Motion.standard) { shown = want }
    }
}

/// The link's state in a capsule, floating in the toast window.
struct ShellLinkPill: View {
    let phase: ConnectionStatus.Phase
    var body: some View {
        ConnectionLabel(phase: phase, compact: true)
            .padding(.horizontal, Theme.Space.m)
            .scaledFrame(height: Theme.Size.icon)
            .modifier(ShellStatusSurface())
            .allowsHitTesting(false)
    }
}

/// The link pill's capsule: a material, or in the BeautifulUI style its raised capsule.
struct ShellStatusSurface: ViewModifier {
    func body(content: Content) -> some View {
        if AppStyle.current.usesBeautifulUI {
            content.buiSurface(Capsule(), elevation: .button)
        } else {
            content.background(.regularMaterial, in: .capsule)
        }
    }
}

/// The shell's hooks into its window: the toast window above it (`ToastWindow`), and whether a finger is on the screen
/// for the pager (`ShellPager.fingers`): a recognizer on the window that only watches (it never begins, delays or
/// cancels a touch).
struct ShellWindowHooks: UIViewRepresentable {
    let notices: ShellNotices
    let pager: ShellPager

    func makeUIView(context: Context) -> Host {
        let v = Host()
        v.isUserInteractionEnabled = false
        v.isAccessibilityElement = false
        v.notices = notices
        v.pager = pager
        return v
    }

    func updateUIView(_ v: Host, context: Context) {
        v.notices = notices
        v.pager = pager
    }

    static func dismantleUIView(_ v: Host, coordinator: ()) {
        v.toasts?.close()
    }

    final class Host: UIView {
        weak var notices: ShellNotices?
        weak var pager: ShellPager?
        private var watcher: Watcher?
        private(set) var toasts: ToastWindow?

        override func didMoveToWindow() {
            super.didMoveToWindow()
            if let watcher { watcher.view?.removeGestureRecognizer(watcher) }
            guard let window else { return }
            let w = Watcher()
            w.touching = { [weak self] down in self?.pager?.fingers(down: down) }
            window.addGestureRecognizer(w)
            watcher = w
            if toasts?.over !== window, let notices {
                toasts?.close()
                toasts = ToastWindow(over: window, notices: notices)
            }
        }
    }

    final class Watcher: UIGestureRecognizer, UIGestureRecognizerDelegate {
        /// A finger is down, as the touches come and go.
        var touching: (Bool) -> Void = { _ in }
        private var down = 0

        override init(target: Any? = nil, action: Selector? = nil) {
            super.init(target: target, action: action)
            cancelsTouchesInView = false
            delaysTouchesBegan = false
            delaysTouchesEnded = false
            delegate = self
        }

        func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool { true }

        override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent) {
            if down == 0 { touching(true) }
            down += touches.count
        }

        override func touchesEnded(_ touches: Set<UITouch>, with event: UIEvent) { lift(touches) }
        override func touchesCancelled(_ touches: Set<UITouch>, with event: UIEvent) { lift(touches) }

        private func lift(_ touches: Set<UITouch>) {
            down = max(0, down - touches.count)
            guard down == 0 else { return }
            touching(false)
            state = .failed // back to .possible for the next touch; it never began
        }
    }
}
/// The shell's toasts (`AppModel.toast`) over everything in its scene, the drawer, the pages and a sheet over them, in a
/// window of their own above the shell's: it takes no touches, so what is under a toast takes the tap (a toast over the
/// top bar's switch took the tap meant for it, 2026-11-02), and it moves nothing (opening the status row for a toast
/// pushed the pages and the drawer down and back, #1053). Under the link's pill while that shows; the keyboard is at
/// the bottom, out of its way. The tabs (Old Layout, iPad) have one too, without the pill (`ToastWindowHook`).
@MainActor final class ToastWindow {
    private(set) weak var over: UIWindow?
    private var window: UIWindow?

    /// `notices`: the hub's link to float above the toast (the shell's); nil for none.
    init(over shell: UIWindow, notices: ShellNotices?) {
        over = shell
        guard let scene = shell.windowScene else { return }
        let w = PassThrough(windowScene: scene)
        let hit = w.touchable
        w.windowLevel = .normal + 1
        w.backgroundColor = .clear
        // Settings › Appearance › Light & Dark reaches every window of the scene as it changes (`ColorSchemeChoice`); this one starts as the shell's.
        w.overrideUserInterfaceStyle = shell.overrideUserInterfaceStyle
        let host = UIHostingController(rootView: ToastWindowRoot(model: AppModel.shared, notices: notices) { hit.rect = $0 })
        host.view.backgroundColor = .clear
        w.rootViewController = host
        w.isHidden = false
        window = w
    }

    func close() {
        window?.isHidden = true
        window = nil
    }

    /// Where a touch is taken: the toast's button (Undo), a little around it; nowhere when it has none.
    final class Touchable {
        var rect = CGRect.null
    }

    /// Takes no touch but on the toast's button: every other one goes to the windows below.
    private final class PassThrough: UIWindow {
        let touchable = Touchable()

        override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
            guard !touchable.rect.isNull, touchable.rect.insetBy(dx: -Theme.Space.s, dy: -Theme.Space.s).contains(point) else { return nil }
            return super.hitTest(point, with: event)
        }
    }
}

/// A toast window (`ToastWindow`, no link pill) over the window this view is in, for the tabs (Old Layout, iPad, iPhone
/// Duo open; `RootView`). In `RootView`'s overlay their toasts were drawn under every cover and sheet: Saved to Photos
/// from the image viewer and the share card never showed (#1492).
struct ToastWindowHook: UIViewRepresentable {
    func makeUIView(context: Context) -> Host {
        let v = Host()
        v.isUserInteractionEnabled = false
        v.isAccessibilityElement = false
        return v
    }

    func updateUIView(_ v: Host, context: Context) {}

    static func dismantleUIView(_ v: Host, coordinator: ()) {
        v.toasts?.close()
    }

    final class Host: UIView {
        private(set) var toasts: ToastWindow?

        override func didMoveToWindow() {
            super.didMoveToWindow()
            // Out of its window while a full-screen cover is up: the toast window stays, it is there for that.
            guard let window, toasts?.over !== window else { return }
            toasts?.close()
            toasts = ToastWindow(over: window, notices: nil)
        }
    }
}

/// The toast window's content: the hub's link (when it is not reached) and the app's toast just below the top bars, in
/// the app's text size and look. Right under the status bar it lay on the switch (Assistant / Agents unreadable while it
/// showed) and on the drawer's title and search.
private struct ToastWindowRoot: View {
    @Environment(\.dynamicTypeSize) private var systemTypeSize
    let model: AppModel
    let notices: ShellNotices?
    let onActionFrame: (CGRect) -> Void

    var body: some View {
        let size = model.textSize.applied(to: systemTypeSize)
        VStack(spacing: Theme.Space.s) {
            if let phase = notices?.pill {
                ShellLinkPill(phase: phase)
                    .accessibilityIdentifier("shell-connection")
                    .transition(.opacity.combined(with: .scale(scale: 0.92)))
            }
            ToastOverlay(tapToDismiss: false, onActionFrame: onActionFrame)
        }
            .animation(Theme.Motion.standard, value: notices?.pill)
            .padding(.top, ShellNavigationClearance.needed(at: size))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .environment(model)
            .dynamicTypeSize(size)
            .buiTheme(.persona())
            .tint(Theme.accent)
    }
}
#endif

/// A view in a title that says the hub's link itself (`ConnectionLabel`): while it is on screen in front, the shell's
/// floating pill does not say it too. Outside the shell (old layout, iPad, Mac) nothing.
private struct TitleSaysLink: ViewModifier {
    @Environment(\.shell) private var shell

    func body(content: Content) -> some View {
        #if os(iOS) || os(visionOS)
        content.onTabVisibility(appear: { shell?.notices.titleShowsLink(true) },
                                disappear: { shell?.notices.titleShowsLink(false) })
        #else
        content
        #endif
    }
}

extension View {
    /// This title says Waiting for Network / Connecting… / Updating… itself: the shell's pill keeps out of its way (`ShellNotices.pill`).
    func titleSaysLink() -> some View { modifier(TitleSaysLink()) }
}
