import SwiftUI

/// A made-up picture of what a setting does (Settings › Session Triage, #1413). Smaller type, a soft wash and an
/// "Example" tag in its corner keep it from reading as real content, and it ignores taps.
struct ExampleFrame<Content: View>: View {
    @Environment(\.dynamicTypeSize) private var typeSize
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Chip(text: String(localized: "Example"), compact: true)
            content
        }
        .dynamicTypeSize(Self.smaller(typeSize))
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .allowsHitTesting(false)
        .accessibilityElement(children: .combine)
    }

    /// Two steps under the reader's own size, so the example stays smaller than the page at every text size.
    nonisolated static func smaller(_ size: DynamicTypeSize) -> DynamicTypeSize {
        let all = DynamicTypeSize.allCases
        guard let i = all.firstIndex(of: size) else { return size }
        return all[max(i - 2, 0)]
    }
}

/// What a setting changes: how a thing looks with it off, an arrow, how it looks with it on.
struct ExampleChange<Off: View, On: View>: View {
    @ViewBuilder var off: Off
    @ViewBuilder var on: On

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            row(String(localized: "Off [Example]")) { off }
            row(nil) { ThemedIcon(Theme.Icon.down).font(Theme.Font.label).foregroundStyle(.tertiary) }
            row(String(localized: "On [Example]")) { on }
        }
    }

    /// Its tag, then what it shows over the rest of the width. Both tags take the room of the wider one (hidden
    /// copies under each), so the two states and the arrow start on one line.
    private func row(_ tag: String?, @ViewBuilder content: () -> some View) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ZStack(alignment: .leading) {
                label(String(localized: "Off [Example]")).hidden()
                label(String(localized: "On [Example]")).hidden()
                if let tag { label(tag) }
            }
            content().frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func label(_ text: String) -> some View {
        Text(text).font(Theme.Font.labelStrong).foregroundStyle(.secondary).lineLimit(1)
    }
}

/// One agent in a line: its state's dot, its name, and the words its row would show for it (what it waits for, else
/// its state), from the same `Session` logic as the Agents list.
struct ExampleAgentLine: View {
    var session: Session

    var body: some View {
        let words = session.workLine() ?? session.stateLabel(muted: false)
        let tone = session.workLine() != nil ? StatusPalette.idle : session.stateTone(muted: false)
        HStack(spacing: Theme.Space.s) {
            StatusDot(state: session.state)
            Text(session.displayTitle).font(Theme.Font.bodyStrong).lineLimit(1)
            Text(words).font(Theme.Font.secondary).foregroundStyle(tone.text).lineLimit(1)
        }
    }
}

/// Steps that follow one another in time, as chips joined by arrows: in a line when they fit, else one under another.
struct ExampleTimeline: View {
    struct Step {
        var symbol: String
        var text: String
        var look: Chip.Look = .neutral
    }

    var steps: [Step]

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: Theme.Space.xs) { chain(arrow: Theme.Icon.becomes) }
            VStack(alignment: .leading, spacing: Theme.Space.xs) { chain(arrow: Theme.Icon.down) }
        }
    }

    @ViewBuilder private func chain(arrow: String) -> some View {
        ForEach(steps.indices, id: \.self) { i in
            if i > 0 { ThemedIcon(arrow).font(Theme.Font.label).foregroundStyle(.tertiary) }
            Chip(text: steps[i].text, symbol: steps[i].symbol, look: steps[i].look)
        }
    }
}

/// A push notification as the lock screen shows it: the persona's face, the title, when, and the message.
struct PushBannerSample: View {
    var title: String
    var message: String

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.sm) {
            PersonaAvatar(size: Theme.Size.icon)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(alignment: .firstTextBaseline) {
                    Text(title).font(Theme.Font.bodyStrong).lineLimit(2)
                    Spacer(minLength: Theme.Space.s)
                    Text("now [Push Sample]").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
                Text(message).font(Theme.Font.secondary).lineLimit(2)
            }
        }
        .padding(Theme.Space.m)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
    }
}
