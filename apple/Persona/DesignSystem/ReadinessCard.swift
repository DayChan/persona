import BeautifulUI
import SwiftUI

/// What the main conversation still needs, or why a start or a send was refused (docs/assistant/onboarding.md 4.4, 5.2): one card
/// over the composer — a title, one sentence, one button for what to do, and under Details the original text (the hub's
/// `detail`, the request id) for a failure. Later (`dismiss`) when the checklist may be put off. In the BeautifulUI
/// style it is that library's list row in a section, the button its pill.
struct ReadinessCard: View {
    enum Tone {
        /// A step still to do (a warning, not an error).
        case caution
        /// A refusal or a step that broke.
        case danger
    }

    var title: String
    var detail: String?
    var tone: Tone = .caution
    /// The button's words and what it does; nil: text only (an action this app does not know).
    var actionTitle: String?
    var action: (() -> Void)?
    /// A second, quieter way (Use an API Key Instead next to How to Sign In).
    var altTitle: String?
    var alt: (() -> Void)?
    /// The original text, folded under Details.
    var details: [String] = []
    var busy = false
    var dismiss: (() -> Void)?
    @State private var open = false

    var body: some View {
        if AppStyle.current.usesBeautifulUI { beautiful } else { plain }
    }

    private var color: Color { tone == .danger ? Theme.danger : Theme.caution }

    private var plain: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                ThemedIcon(tone == .danger ? Theme.Icon.errorFilled : Theme.Icon.error)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(color)
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(title).font(Theme.Font.labelStrong).foregroundStyle(Color.primary)
                        .fixedSize(horizontal: false, vertical: true)
                    if let detail, !detail.isEmpty {
                        Text(detail).font(Theme.Font.secondary).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                if let dismiss {
                    Button(action: dismiss) {
                        ThemedIcon(Theme.Icon.close).font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                    .buttonStyle(.borderless)
                    .accessibilityLabel(Text("Later [Readiness Card 9d2055]"))
                }
            }
            buttons.padding(.leading, Theme.Space.l)
            if open { detailsBlock.padding(.leading, Theme.Space.l) }
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("readiness-card")
    }

    @ViewBuilder private var buttons: some View {
        let hasAny = action != nil || alt != nil || !details.isEmpty || busy
        if hasAny {
            HStack(spacing: Theme.Space.m) {
                if busy { ProgressView().controlSize(.small) }
                if let actionTitle, let action {
                    Button {
                        Haptics.tap()
                        action()
                    } label: {
                        Text(actionTitle).font(Theme.Font.secondaryStrong).lineLimit(1).minimumScaleFactor(0.8)
                    }
                    .themedButton(prominent: true)
                    .disabled(busy)
                }
                if let altTitle, let alt {
                    Button(action: alt) {
                        Text(altTitle).font(Theme.Font.secondaryStrong).lineLimit(1).minimumScaleFactor(0.8)
                    }
                    .buttonStyle(.borderless)
                    .foregroundStyle(Theme.accent)
                }
                Spacer(minLength: 0)
                if !details.isEmpty { detailsToggle }
            }
        }
    }

    private var detailsToggle: some View {
        Button {
            withAnimation(Theme.Motion.state) { open.toggle() }
        } label: {
            Label("Details [Readiness Card 979a33]", systemImage: open ? Theme.Icon.collapse : Theme.Icon.expand)
                .font(Theme.Font.label).lineLimit(1)
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.secondary)
        .accessibilityValue(open ? Text("Expanded") : Text("Collapsed [Readiness Card ee6046]"))
    }

    private var detailsBlock: some View {
        Text(details.joined(separator: "\n"))
            .font(Theme.Font.code)
            .lineHeight(.code)
            .textSelection(.enabled)
            .fixedSize(horizontal: false, vertical: true)
            .padding(Theme.Space.s)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
            .transition(.opacity)
    }

    /// BeautifulUI: a list section of one row (its tone's mark, the words), the button as the row's pill.
    private var beautiful: some View {
        BUIListSection {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                BUIListRow(title, detail: detail, detailLines: 4) {
                    IconTile(tone == .danger ? Theme.Icon.errorFilled : Theme.Icon.error, tint: color)
                } trailing: {
                    if busy { BUISpinner() }
                    if let dismiss {
                        Button(action: dismiss) { BUIPill(String(localized: "Later [Readiness Card 9d2055]"), tone: .neutral) }
                            .buttonStyle(.plain)
                    }
                }
                if action != nil || alt != nil || !details.isEmpty {
                    HStack(spacing: Theme.Space.s) {
                        if let actionTitle, let action {
                            Button {
                                Haptics.tap()
                                action()
                            } label: {
                                BUIPill(actionTitle, tone: .accent)
                            }
                            .buttonStyle(.plain)
                            .disabled(busy)
                        }
                        if let altTitle, let alt {
                            Button(action: alt) { BUIPill(altTitle, tone: .outline) }.buttonStyle(.plain)
                        }
                        Spacer(minLength: 0)
                        if !details.isEmpty { detailsToggle }
                    }
                }
                if open { detailsBlock }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("readiness-card")
    }
}

/// The send button while the main conversation cannot answer yet (docs/assistant/onboarding.md 4.4): it says what to do first
/// (Connect a Model First / Add a Computer First) and goes there; the draft stays in the field and nothing is queued.
struct SendGate {
    var title: String
    var action: () -> Void
}

/// A composer's send button as a `SendGate`: the words in a prominent capsule, one line.
struct SendGateButton: View {
    var gate: SendGate
    var body: some View {
        Button {
            Haptics.tap()
            gate.action()
        } label: {
            Text(gate.title).font(Theme.Font.secondaryStrong).lineLimit(1).minimumScaleFactor(0.8)
                .padding(.horizontal, Theme.Space.xs)
        }
        .themedButton(prominent: true)
        .accessibilityIdentifier("composer-send-gate")
    }
}
