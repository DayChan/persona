#if os(iOS) || os(visionOS)
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// Whether the clipboard holds pictures the composer has not offered yet (#4573): asked with
/// `hasImages`, which reads no content and so shows no Allow Paste prompt. A copy made while the app is in the background
/// is seen when it comes back.
@MainActor @Observable final class PasteboardImages {
    static let shared = PasteboardImages()

    private(set) var offered = false
    /// The clipboard's `changeCount` taken or dismissed: the same copy is offered once.
    @ObservationIgnored private var handled = -1
    @ObservationIgnored private var observers: [NSObjectProtocol] = []

    private init() {
        let center = NotificationCenter.default
        for name in [UIPasteboard.changedNotification, UIApplication.didBecomeActiveNotification] {
            observers.append(center.addObserver(forName: name, object: nil, queue: .main) { _ in
                MainActor.assumeIsolated { PasteboardImages.shared.refresh() }
            })
        }
        refresh()
    }

    func refresh() {
        let board = UIPasteboard.general
        offered = board.hasImages && board.changeCount != handled
    }

    /// Taken or dismissed: the same copy is not offered again.
    nonisolated static func image(_ provider: NSItemProvider) async -> UIImage? {
        await withCheckedContinuation { c in
            _ = provider.loadDataRepresentation(for: .image) { @Sendable data, _ in c.resume(returning: data.flatMap(UIImage.init(data:))) }
        }
    }

    func dismiss() {
        handled = UIPasteboard.general.changeCount
        refresh()
    }
}

/// Paste over a focused composer while the clipboard holds a picture (copied in Safari, Photos, Lark, or a screenshot's
/// Copy): one tap attaches it, as Signal and Telegram offer. The text field itself only pastes text.
struct PasteImageChip: View {
    var focused: Bool
    var attach: (UIImage) -> Void
    @State private var board = PasteboardImages.shared

    var body: some View {
        if focused && board.offered {
            HStack(spacing: Theme.Space.s) {
                // The system's paste button: a tap is the consent, so iOS shows no Allow Paste prompt.
                PasteButton(supportedContentTypes: [.image]) { providers in
                    Task { @MainActor in
                        board.dismiss()
                        for p in providers.prefix(4) {
                            if let img = await PasteboardImages.image(p) { attach(img) }
                        }
                    }
                }
                .labelStyle(.titleAndIcon)
                .buttonBorderShape(.capsule)
                .tint(Theme.accent)
                .accessibilityIdentifier("composer-paste-image")
                Spacer(minLength: 4)
                Button {
                    board.dismiss()
                } label: {
                    ThemedIcon(Theme.Icon.close).font(Theme.Font.label).foregroundStyle(.secondary)
                        .scaledFrame(minWidth: Theme.Size.wellXS, minHeight: Theme.Size.wellXS).contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Don't paste")
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .surface(tint: Theme.accent.opacity(0.06), interactive: true, in: .capsule)
            .transition(.move(edge: .bottom).combined(with: .opacity))
        }
    }
}
#endif
