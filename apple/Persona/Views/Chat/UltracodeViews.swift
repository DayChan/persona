import SwiftUI

// Claude Code's ultracode switch in the chat and in New Agent (#1307, docs/apple/ios.md "Claude Code ultracode"): the
// switch under the thinking effort in the model menu and the effort chip's menu, the workflow mark after the effort
// label and beside an agent row's title, the line above the composer after a resume turned it off, and New Agent's row.
// The rules are `Ultracode` (Shared/Ultracode.swift), the requests `AppModel.setUltracode` / `dismissUltracodeLost`.

/// The workflow mark (the same icon as a workflow's progress line) in the AI color. Callers put it in an `if` with
/// `.transition(.opacity)` and animate their own container with `UltracodeMark.motion` on the value, so it comes and
/// goes with a state change, at once with Reduce Motion, and has no motion of its own.
struct UltracodeMark: View {
    var font: Font = Theme.Font.label
    /// What VoiceOver reads; nil leaves it out (the label beside it already says it, a Codex "Ultra").
    var spoken: String? = Ultracode.onWords

    var body: some View {
        ThemedIcon(Theme.Icon.workflow)
            .font(font)
            .foregroundStyle(Theme.ai)
            .accessibilityHidden(spoken == nil)
            .accessibilityLabel(spoken ?? "")
            .accessibilityIdentifier("ultracode.mark")
    }

    /// The state change the mark comes and goes with; none with Reduce Motion.
    @MainActor static var motion: Animation? { Theme.Motion.reduced ? nil : Theme.Motion.state }

    /// `title` with the mark after its last word while `on`, read out as 「…，ultracode 开」: part of the text, so a title
    /// that wraps carries it at the end of its last line. Beside the text's frame it sat after the first line, between
    /// the title's two halves. The label is the whole text's: an image's own inside a `Text` is not read.
    static func after(_ title: String, on: Bool, font: Font = Theme.Font.label) -> Text {
        guard on else { return Text(title) }
        let mark = Text(Theme.Icon.image(Theme.Icon.workflow)).font(font).foregroundStyle(Theme.ai)
        return (Text(title) + Text(verbatim: "\u{00A0}") + mark)
            .accessibilityLabel(title + String(localized: ", ", comment: "List separator") + Ultracode.onWords)
    }
}

/// The switch in a menu (the composer's model menu or platform effort chip): a section of its own under the thinking
/// effort, the switch with its line as the subtitle (what it does, "Applies next turn", or the node's reason), and the
/// Claude quota with its warning as the section's header (`Ultracode.menuWords`). A menu draws a subtitle only from a
/// second `Text` directly in the label, not from a `Label`'s title; an iOS menu cuts it after three lines, so there the
/// node's reason goes in the header too, which it draws whole. The header is the system's own secondary style: a
/// disabled item for the quota was drawn faint, as if it could be used some other time. It applies at once with its own
/// request; a failure, a refusal too, puts it back and says why once (a toast: the menu has closed by then).
///
/// An iOS menu that opens upward from the composer or a chip lists its items the other way round (the ones declared
/// first next to the button): there the section is declared before the effort (`UltracodeMenuSection.leadsEffort`), so
/// it reads under it; a Mac menu keeps the declared order. A section's header stays on top of it either way.
struct UltracodeMenuSection: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var session: Session?

    /// Declared before the effort it goes under: the menus that list their items the other way round (iOS, visionOS).
    static var leadsEffort: Bool {
        #if os(macOS)
        false
        #else
        true
        #endif
    }

    /// The menu draws a header line by line and cuts a subtitle (iOS, visionOS), or wraps a subtitle and draws a header
    /// on one line (the Mac): `Ultracode.menuWords`.
    static var headerWraps: Bool { leadsEffort }

    var body: some View {
        if let s = session, Ultracode.offered(s, features: model.features(key.group)) {
            let ask = model.ultracodeAsk(key)
            let quota = Ultracode.quota(UsageWindowsStore.shared.accounts(group: key.group, multi: model.isMultiHub))
            section(Ultracode.switchState(s, ask: ask), Ultracode.menuWords(s, ask: ask, quota: quota, headerWraps: Self.headerWraps))
        }
    }

    private func section(_ sw: Ultracode.Switch, _ words: Ultracode.MenuWords) -> some View {
        Section {
            Toggle(isOn: binding(sw)) {
                Text(verbatim: Ultracode.name)
                Text(words.subtitle)
                Theme.Icon.image(Theme.Icon.workflow)
            }
            .disabled(sw.disabled)
            .accessibilityIdentifier("ultracode.switch")
        } header: {
            if let header = words.header { Text(header) }
        }
    }

    private func binding(_ sw: Ultracode.Switch) -> Binding<Bool> {
        Binding(get: { sw.on }, set: { on in Task { await model.setUltracode(key, on: on) } })
    }
}

/// Over the composer after a resume turned it off (`ultracode_lost_at`): one line, Turn Back On (one request; the node
/// takes the line away once it is on, "Applies next turn" while it waits) and × to dismiss it.
struct UltracodeLostNote: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var session: Session

    var body: some View {
        let ask = model.ultracodeAsk(key)
        let waiting = Ultracode.shownOn(session, ask: ask) && session.ultracodePending
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.workflow).font(Theme.Font.labelStrong).foregroundStyle(Theme.ai)
                .accessibilityHidden(true)
            Text("ultracode off after resume")
                .font(Theme.Font.secondary).foregroundStyle(.secondary)
                .lineLimit(1).truncationMode(.tail)
            Spacer(minLength: Theme.Space.xs)
            if waiting {
                Text(Ultracode.pendingWords).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1).fixedSize()
            } else {
                Button { Task { await model.setUltracode(key, on: true) } } label: {
                    HStack(spacing: Theme.Space.xs) {
                        if ask?.sending == true { ProgressView().controlSize(.mini) } else { ThemedIcon(Theme.Icon.retry) }
                        Text("Turn Back On")
                    }
                    .font(Theme.Font.secondaryStrong)
                    .foregroundStyle(Theme.accent)
                    .lineLimit(1)
                    .fixedSize()
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(ask?.sending == true)
                .accessibilityIdentifier("ultracode.reopen")
            }
            Button { Task { await model.dismissUltracodeLost(key) } } label: {
                ThemedIcon(Theme.Icon.close)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.secondary)
                    .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                    .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(Text("Dismiss [Ultracode 5a0c31]"))
            .accessibilityIdentifier("ultracode.dismissLost")
        }
        .padding(.leading, Theme.Space.m)
        .padding(.trailing, Theme.Space.s)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("ultracode.lost")
    }
}

/// The same switch in New Agent, under the thinking effort: off each time the sheet opens, never remembered (only `true`
/// is ever sent). `blocked`: why this computer can't start with it (its Claude Code is too old), grayed and said
/// instead of the line.
struct UltracodeStartRow: View {
    @Environment(AppModel.self) private var model
    @Binding var on: Bool
    var blocked: String?
    var group: String

    var body: some View {
        Toggle(isOn: Binding(get: { on && blocked == nil }, set: { on = $0 })) {
            Label {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(verbatim: Ultracode.name).lineLimit(1)
                    Text(blocked ?? Ultracode.subtitle)
                        .font(Theme.Font.label)
                        .foregroundStyle(blocked == nil ? Color.secondary : Theme.cautionText)
                        .fixedSize(horizontal: false, vertical: true)
                    if let q = quota {
                        // out of date: faded as on the usage bars, with its age on a line of its own (#1906)
                        Text(Ultracode.quotaLine(q)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                            .opacity(q.stale ? 0.6 : 1)
                        if let age = Ultracode.quotaAge(q) {
                            Text(age).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).opacity(0.6)
                        }
                        if q.warn, blocked == nil {
                            Text(Ultracode.quotaWarning).font(Theme.Font.label).foregroundStyle(Theme.cautionText)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }
                }
            } icon: {
                ThemedIcon(Theme.Icon.workflow).foregroundStyle(Theme.ai)
            }
        }
        .disabled(blocked != nil)
        .accessibilityIdentifier("newChat.ultracode")
    }

    private var quota: Ultracode.Quota? {
        Ultracode.quota(UsageWindowsStore.shared.accounts(group: group, multi: model.isMultiHub))
    }
}
