import BeautifulUI
import SwiftUI

/// One toast (`AppModel.toast`): the style's icon and the words on a floating capsule; in the BeautifulUI style its
/// raised capsule. A toast tells the result of something just done (Copied, Archived, Send failed…) and goes by itself after
/// a moment; what lasts (the hub's link: Waiting for Network, Connecting…) floats in the shell's toast window too (docs/apple/shell.md「连接状态和 toast」).
struct ToastCapsule: View {
    let toast: Toast
    /// Closes the toast after its button (Undo) ran.
    var dismiss: () -> Void = {}
    /// Told where its button is, in global coordinates (the shell's toast window lets touches through but there).
    var onActionFrame: ((CGRect) -> Void)?

    var body: some View {
        let row = HStack(spacing: Theme.Space.m) {
            ThemedIcon(toast.style == .error ? Theme.Icon.errorFilled : toast.style == .success ? Theme.Icon.successFilled : Theme.Icon.infoFilled)
                .foregroundStyle(toast.style == .error ? Theme.danger : toast.style == .success ? Theme.positive : Theme.accent)
            Text(toast.message)
                .font(Theme.Font.secondaryMedium)
                .lineLimit(3)
            if let action = toast.action {
                Button {
                    action.run()
                    dismiss()
                } label: {
                    // As large as the words beside it (the owner asked, #138: Undo was too small to read and hit).
                    Text(action.title)
                        .font(Theme.Font.secondaryStrong)
                        .foregroundStyle(Theme.accent)
                        .lineLimit(1)
                        .fixedSize()
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("toast-action")
                .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { onActionFrame?($0) }
                .onDisappear { onActionFrame?(.null) }
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        if AppStyle.current.usesBeautifulUI {
            row.buiSurface(Capsule(), elevation: .button)
        } else {
            row.surface(in: .capsule)
        }
    }

    /// How long it stays: an error longer, one with a button (Undo) long enough to reach it.
    static func shown(_ toast: Toast) -> Duration { .seconds(toast.action != nil ? 5 : toast.style == .error ? 4 : 2.5) }
}

/// The app's toast at the top of what it covers, over the content (it moves nothing), gone after `ToastCapsule.shown`.
/// `tapToDismiss`: a tap on it closes it (the Mac, the welcome page); on the iPhone and iPad it is shown in a window of
/// its own that takes no touches (`ToastWindow`), so nothing under it ever misses a tap.
struct ToastOverlay: View {
    @Environment(AppModel.self) private var model
    var tapToDismiss = true
    /// Where the toast's button is (`ToastCapsule.onActionFrame`).
    var onActionFrame: ((CGRect) -> Void)?

    var body: some View {
        ZStack {
            if let toast = model.toast {
                ToastCapsule(toast: toast, dismiss: { withAnimation(Theme.Motion.standard) { model.toast = nil } }, onActionFrame: onActionFrame)
                    .padding(.horizontal, Theme.Space.xl)
                    .transition(.move(edge: .top).combined(with: .opacity))
                    .onTapGesture { if tapToDismiss { model.toast = nil } }
                    .accessibilityElement(children: toast.action == nil ? .combine : .contain)
                    .accessibilityIdentifier("toast")
                    .task(id: toast.id) {
                        #if os(iOS) || os(visionOS)
                        // In a window that takes no touches VoiceOver does not come to it: it is read out as it comes.
                        if !tapToDismiss { AccessibilityNotification.Announcement(toast.message).post() }
                        #endif
                        // Cancelled (this overlay went away: the engine step giving way to the shell's toast window), the
                        // toast stays for the overlay that shows next, which starts its own timer (#4273).
                        do { try await Task.sleep(for: ToastCapsule.shown(toast)) } catch { return }
                        guard model.toast?.id == toast.id else { return }
                        withAnimation(Theme.Motion.standard) { model.toast = nil }
                    }
            }
        }
        .animation(Theme.Motion.standard, value: model.toast)
    }
}
