import SwiftUI

/// How to fix a launcher New Agent grays out (#1643): why it can't start there, what to do about it, the hub's own words
/// under Details, and the ways on: add it to Persona as a model provider (for one the terminal can't run), or Try Anyway
/// once it is fixed (a start with it that works clears the record).
struct LauncherBlockedHelp: View {
    @Environment(\.dismiss) private var dismiss
    var launcher: Launcher
    var block: LauncherBlock
    /// Add Provider is offered: the computer is in the hub group the provider wizard saves to (the hub-level one). On a
    /// computer of another group of hubs a provider added here would land on the wrong hub, so only the words say it.
    var addsProvider: Bool
    /// Picks it after all: the help closes, then the list.
    var tryAnyway: () -> Void
    @State private var addingProvider = false
    @State private var showDetail = false

    init(launcher: Launcher, block: LauncherBlock, addsProvider: Bool = true, tryAnyway: @escaping () -> Void) {
        self.launcher = launcher
        self.block = block
        self.addsProvider = addsProvider
        self.tryAnyway = tryAnyway
        #if DEBUG
        // `-openLauncherDetails YES`: Details open, for screenshots.
        _showDetail = State(initialValue: UserDefaults.standard.bool(forKey: "openLauncherDetails"))
        #endif
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                        ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.cautionText)
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(launcher.name).font(Theme.Font.heading.monospaced()).lineLimit(1).truncationMode(.middle)
                            Text(block.reason).font(Theme.Font.secondary).foregroundStyle(Theme.cautionText)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    if block.notInTerminal {
                        para(String(localized: "Persona found “\(launcher.name)” in this computer's login shell, but the shell new terminal tabs run doesn't have it: another shell, a container, or a startup file that stops early. New agents start in those tabs."))
                    } else {
                        para(String(localized: "Its latest check on this computer failed, and automatic repair didn't fix it."))
                    }
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        Text("How to Fix").font(Theme.Font.heading)
                        if block.notInTerminal {
                            bullet(String(localized: "Define it in a startup file the terminal's shell reads (for bash, ~/.bashrc), then start again."))
                            bullet(String(localized: "Or add it to Persona as a model provider: those work in any terminal."))
                        } else {
                            bullet(String(localized: "Fix its key, gateway or CLI on that computer, or pick another launcher."))
                        }
                    }
                    if let detail = block.detail {
                        VStack(alignment: .leading, spacing: Theme.Space.s) {
                            // A toggle like ReadinessCard's, not a DisclosureGroup: its chevron ignores the tint and
                            // reads as a heavy navigation link next to a gray label.
                            Button {
                                withAnimation(Theme.Motion.state) { showDetail.toggle() }
                            } label: {
                                Label("Details [Readiness Card 979a33]", systemImage: showDetail ? Theme.Icon.collapse : Theme.Icon.expand)
                                    .font(Theme.Font.secondaryStrong).lineLimit(1)
                            }
                            .buttonStyle(.borderless)
                            .foregroundStyle(.secondary)
                            .accessibilityValue(showDetail ? Text("Expanded") : Text("Collapsed [Readiness Card ee6046]"))
                            .accessibilityIdentifier("launcherHelp.details")
                            if showDetail {
                                Text(detail).font(Theme.Font.code).lineHeight(.code).foregroundStyle(.secondary).textSelection(.enabled)
                                    .fixedSize(horizontal: false, vertical: true)
                                    .padding(Theme.Space.s)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                                    .transition(.opacity)
                            }
                        }
                    }
                    para(block.notInTerminal ? String(localized: "A start with it that works clears this; otherwise it clears by itself after 6 hours.")
                                             : String(localized: "A check or start with it that works clears this."), secondary: true)
                    VStack(spacing: Theme.Space.s) {
                        if block.notInTerminal, addsProvider {
                            Button { addingProvider = true } label: {
                                Text("Add Provider").font(Theme.Font.heading).lineLimit(1).minimumScaleFactor(0.8)
                                    .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.wellS)
                            }
                            .themedButton(prominent: true)
                            .accessibilityIdentifier("launcherHelp.addProvider")
                        }
                        Button(action: tryAnyway) {
                            Text("Try Anyway").font(Theme.Font.heading).lineLimit(1).minimumScaleFactor(0.8)
                                .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.wellS)
                        }
                        .themedButton()
                        .accessibilityIdentifier("launcherHelp.tryAnyway")
                    }
                    .padding(.top, Theme.Space.s)
                }
                .padding(Theme.Space.page)
                .frame(maxWidth: ChatView.readableWidth, alignment: .leading)
                .frame(maxWidth: .infinity)
            }
            .themedGroupedPage()
            .navigationTitle(block.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { dismiss() } label: { Text("Done [Extension System Presentation c0b3fb]").lineLimit(1) }
                }
            }
        }
        .sheet(isPresented: $addingProvider) { ProviderWizard(start: .pick) }
        #if os(macOS)
        .macSheet()
        #endif
        .accessibilityIdentifier("launcherHelp")
    }

    private func para(_ s: String, secondary: Bool = false) -> some View {
        Text(s).font(Theme.Font.body).lineHeight(.body).foregroundStyle(secondary ? .secondary : .primary)
            .fixedSize(horizontal: false, vertical: true)
    }

    private func bullet(_ s: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.done).font(Theme.Font.label).foregroundStyle(Theme.accent)
            para(s)
        }
    }
}
