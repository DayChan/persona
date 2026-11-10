import SwiftUI

/// The foot of the sections column (docs/server/usage-windows.md「UI」, as the web's sidebar has it): the two fullest
/// subscription accounts as thin bars, name and fullest window's percentage; a click opens every account's windows with
/// the reset countdowns, the sample ages and the pace. Not drawn while the hubs have no accounts.
struct MacUsageFooter: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    var store = UsageWindowsStore.shared
    @State private var open = false
    @State private var hovering = false

    var body: some View {
        // A minute's clock (periodic, not an animation's): kept numbers turn stale on their own time while refreshes
        // fail, without a new answer to redraw them (#1905). Always a view, so the task below always runs.
        TimelineView(.periodic(from: .now, by: 60)) { ctx in
            bars(UsageWindows.top(UsageWindows.live(store.accounts, now: ctx.date)))
        }
        // While the window's app is in front: the column is always on screen, so that is when the bars are seen.
        .task(id: phase == .active) {
            guard phase == .active else { return }
            await store.poll(model)
        }
    }

    @ViewBuilder private func bars(_ top: [UsageAccount]) -> some View {
        VStack(spacing: 0) {
            if !top.isEmpty {
                Button { open.toggle() } label: {
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        ForEach(top) { a in bar(a) }
                    }
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.s)
                    .background {
                        if hovering || open {
                            RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous).fill(Theme.Fill.macHover)
                        }
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .onHover { hovering = $0 }
                .help(Self.tooltip(top))
                .accessibilityLabel(Self.summary(top))
                .accessibilityHint("Shows each account's usage windows")
                .accessibilityIdentifier("mac.usage")
                .popover(isPresented: $open, arrowEdge: .trailing) {
                    ScrollView {
                        VStack(alignment: .leading, spacing: Theme.Space.m) {
                            Text("Subscription Usage").font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                                .accessibilityAddTraits(.isHeader)
                            UsageAccountsList(accounts: store.accounts)
                        }
                        .padding(Theme.Space.l)
                    }
                    .scrollBounceBehavior(.basedOnSize)
                    .scaledFrame(width: Theme.Size.box300)
                    .frame(maxHeight: Theme.Size.box380)
                }
            }
        }
    }

    private func bar(_ a: UsageAccount) -> some View {
        let used = UsageWindows.fullest(a)
        return VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                Text(a.name).font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                Text(UsageWindows.percentText(used)).font(Theme.Font.label.monospacedDigit())
                    .foregroundStyle(UsageWindows.isWarn(used) ? Theme.cautionText : Color.primary).lineLimit(1)
            }
            UsageMeter(used: used)
        }
        .opacity(a.stale ? 0.6 : 1)
    }

    /// Where the bars' numbers came from, and which are old (the web's title on each bar).
    /// The footer for VoiceOver: each account and how full its fullest window is.
    static func summary(_ accounts: [UsageAccount]) -> String {
        let parts = accounts.map { a in
            let used = UsageWindows.percentText(UsageWindows.fullest(a))
            return String(localized: "\(a.name) \(used) used")
        }.joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label"))
        return String(localized: "Subscription usage: \(parts)")
    }

    static func tooltip(_ accounts: [UsageAccount]) -> String {
        accounts.map { a in
            let from = UsageWindows.sourceNames(a)
            var parts = [a.name]
            if !from.isEmpty {
                let names = from.joined(separator: " · ")
                parts.append(String(localized: "Source: \(names)"))
            }
            if a.stale { parts.append(String(localized: "Data outdated")) }
            return parts.joined(separator: " · ")
        }.joined(separator: "\n")
    }
}
