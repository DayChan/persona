import MarkdownUI
import PersonaKit
import Photos
import SwiftUI

/// A message to share as a picture (Share in the long-press bar).
struct ShareMessage: Identifiable {
    var text: String
    var isUser: Bool
    var source: String
    var date: Date?
    var agent = "Claude"
    var id: String { text }
}

/// The message laid out as a card: who said it, where, the words, and a quiet Persona mark — drawn into an image.
struct MessageShareCard: View {
    var message: ShareMessage
    var dark: Bool

    static let width: CGFloat = 390
    /// Very long answers are cut: a picture of ten screens helps nobody.
    static let maxChars = 3000

    private var text: String {
        let t = message.text.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.count > Self.maxChars ? String(t.prefix(Self.maxChars)) + "\n\n…" : t
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            HStack(spacing: 10) {
                avatar
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(message.isUser ? String(localized: "Me") : message.agent).font(.system(size: 15, weight: .semibold))
                    if !message.source.isEmpty {
                        Text(message.source).font(.system(size: 12)).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer(minLength: 8)
                if let d = message.date, d.isMeaningful {
                    Text(d.formatted(.dateTime.month().day().hour().minute())).font(.system(size: 12)).foregroundStyle(.secondary)
                }
            }
            Markdown(Links.markdown(text))
                .markdownTheme(.card)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
            HStack(spacing: 6) {
                ThemedIcon(Theme.Icon.ai).font(.system(size: 11, weight: .semibold))
                Text("Persona").font(.system(size: 12, weight: .semibold))
                Spacer()
            }
            .foregroundStyle(.tertiary)
            .padding(.top, 2)
        }
        .padding(22)
        .background(dark ? Color(white: 0.11) : .white, in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
        .shadow(color: .black.opacity(dark ? 0.4 : 0.08), radius: 16, y: 6)
        .padding(24)
        .frame(width: Self.width)
        .background(LinearGradient(colors: dark ? [Color(hex: 0x1C2440), Color(hex: 0x2B1D3F)] : [Color(hex: 0xDCE8FF), Color(hex: 0xF1E4FF)],
                                   startPoint: .topLeading, endPoint: .bottomTrailing))
        .environment(\.colorScheme, dark ? .dark : .light)
    }

    /// Who wrote it: the user, or the agent's own mark (Claude's, OpenAI's for Codex). TRAEX has no public mark and
    /// keeps a terminal on green.
    @ViewBuilder private var avatar: some View {
        if message.isUser {
            ThemedIcon(Theme.Icon.user).font(.system(size: 15, weight: .semibold)).foregroundStyle(.white)
                .frame(width: 32, height: 32).background(Color.gray, in: .circle)
        } else if message.agent == "TRAEX" {
            ThemedIcon(Theme.Icon.terminalFilled).font(.system(size: 15, weight: .semibold)).foregroundStyle(.white)
                .frame(width: 32, height: 32)
                .background(LinearGradient(colors: [Color(hex: 0x2FD38F), Color(hex: 0x12935E)], startPoint: .top, endPoint: .bottom), in: .circle)
        } else {
            Image(message.agent == "Codex" ? Theme.Brand.openAI : Theme.Brand.claude)
                .resizable().scaledToFit().foregroundStyle(.primary)
                .padding(7)
                .frame(width: 32, height: 32)
                .background(Color(.secondarySystemBackground), in: .circle)
        }
    }

    @MainActor static func render(_ message: ShareMessage, dark: Bool) -> UIImage? {
        let r = ImageRenderer(content: MessageShareCard(message: message, dark: dark))
        r.proposedSize = ProposedViewSize(width: width, height: nil) // measured at the width it is drawn at
        r.scale = 3
        return r.uiImage
    }
}

extension MarkdownUI.Theme {
    /// The chat's theme with code blocks that wrap instead of scrolling (an image can't scroll; the renderer
    /// draws no scroll views at all).
    @MainActor static let card = MarkdownUI.Theme.persona
        .text { FontSize(15) }
        .codeBlock { config in
            config.label
                .relativeLineSpacing(.em(0.2))
                .markdownTextStyle { FontFamilyVariant(.monospaced); FontSize(.em(0.8)) }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .markdownMargin(top: 4, bottom: 12)
        }
        .table { config in
            config.label
                .markdownTableBorderStyle(.init(color: Color.primary.opacity(0.12)))
                .markdownTableBackgroundStyle(.alternatingRows(Color.clear, Color.primary.opacity(0.03)))
                .markdownMargin(top: 4, bottom: 12)
        }
}

/// Preview of the card, light or dark, with the image viewer's bars (#4714, `ViewerTopBar` / `ViewerBottomBar`): a round
/// close button and Light / Dark in a glass capsule on top; Share (the image) · Save and Copy in a capsule · Share Text (the
/// plain text) at the bottom. The card scrolls under the bars.
struct MessageShareSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(\.colorScheme) private var scheme
    var message: ShareMessage
    @State private var dark: Bool?
    @State private var image: UIImage?
    @State private var saving = false

    private var isDark: Bool { dark ?? (scheme == .dark) }

    var body: some View {
        ScrollView {
            Group {
                if let image {
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFit()
                        .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
                        .shadow(color: .black.opacity(0.12), radius: 12, y: 4)
                        .accessibilityLabel("Message card")
                } else {
                    ProgressView().frame(maxWidth: .infinity, minHeight: 240)
                }
            }
            .padding(.horizontal, Theme.Space.xl)
            .padding(.vertical, Theme.Space.l)
        }
        .scrollIndicators(.hidden)
        .background(Color(.systemGroupedBackground))
        .safeAreaInset(edge: .top, spacing: 0) { topBar.padding(.vertical, Theme.Space.s) }
        .safeAreaInset(edge: .bottom, spacing: 0) { bottomBar.padding(.vertical, Theme.Space.s) }
        .task(id: isDark) { image = MessageShareCard.render(message, dark: isDark) }
        .presentationDetents([.large])
    }

    private var topBar: some View {
        ViewerTopBar {
            Button { dismiss() } label: { ViewerIcon(symbol: Theme.Icon.close) }
                .viewerRound()
                .accessibilityLabel("Done [Extension System Presentation c0b3fb]")
                .accessibilityIdentifier("share-card-close")
                .keyboardShortcut(.cancelAction)
        } center: {
            Picker("Style", selection: Binding(get: { isDark }, set: { dark = $0 })) {
                Text("Light").tag(false)
                Text("Dark").tag(true)
            }
            .pickerStyle(.segmented)
            .fixedSize()
            .accessibilityIdentifier("share-card-style")
            .padding(Theme.Space.xs)
            .surface(in: Capsule()) // not interactive glass: that would take the segments' taps
        } trailing: {
            ViewerBarSpacer()
        }
    }

    private var bottomBar: some View {
        ViewerBottomBar {
            if let image {
                ShareLink(item: Image(uiImage: image), preview: SharePreview(String(localized: "Message card"), image: Image(uiImage: image))) {
                    ViewerIcon(symbol: Theme.Icon.share)
                }
                .accessibilityLabel("Share")
            } else {
                ViewerIcon(symbol: Theme.Icon.share, enabled: false)
                    .accessibilityLabel("Share")
                    .accessibilityAddTraits(.isButton)
            }
        } actions: {
            Button { Task { await save() } } label: { ViewerIcon(symbol: Theme.Icon.save) }
                .disabled(image == nil || saving)
                .accessibilityLabel(saving ? String(localized: "Saving [Image Viewer a1a172]") : String(localized: "Save"))
            Button { copyImage() } label: { ViewerIcon(symbol: Theme.Icon.copy) }
                .disabled(image == nil)
                .accessibilityLabel("Copy [Readiness Views d37380]")
        } trailing: {
            ShareLink(item: message.text) { ViewerIcon(symbol: Theme.Icon.text) }
                .accessibilityLabel("Share Text")
        }
    }

    private func copyImage() {
        guard let image else { return }
        UIPasteboard.general.image = image
        Haptics.success()
        AppModel.shared.toast = Toast(String(localized: "Image copied"), style: .success)
    }

    private func save() async {
        guard let image else { return }
        saving = true
        defer { saving = false }
        let status = await PHPhotoLibrary.requestAuthorization(for: .addOnly)
        guard status == .authorized || status == .limited else {
            Haptics.error()
            AppModel.shared.toast = Toast(String(localized: "No access to Photos. In Settings › Persona › Photos, allow “Add Photos Only”."), style: .error)
            return
        }
        do {
            // `@Sendable`: Photos runs the block on its own queue, where main-actor code is a crash (BackgroundCallbackTests).
            try await PHPhotoLibrary.shared().performChanges { @Sendable in PHAssetChangeRequest.creationRequestForAsset(from: image) }
            Haptics.success()
            AppModel.shared.toast = Toast(String(localized: "Saved to Photos"), style: .success)
        } catch {
            Haptics.error()
            if !error.isCancellation { AppModel.shared.toast = Toast(String(localized: "Couldn't save: \(error.localizedDescription) [Image Viewer fcc6eb]"), style: .error) }
        }
    }
}
