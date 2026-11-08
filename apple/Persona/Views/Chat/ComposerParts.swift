import SwiftUI

/// The composer's input line, shared by an agent's chat (`Composer`) and a new assistant conversation
/// (`StarterComposer`) so the two look alike: [+] with the caller's menu, the field (a glass capsule, at least 44 pt),
/// then send (faded while there is nothing to send) or, while the agent works and nothing is typed, a red stop.
/// The callers keep their own state: the chat's drafts, SendSafety and uploads stay in `Composer`.
struct ComposerRow<MenuItems: View>: View {
    var placeholder: String
    @Binding var text: String
    @Binding var selection: TextSelection?
    var focused: FocusState<Bool>.Binding
    var canSend: Bool
    /// Set while the agent works: shown instead of send when there is nothing to send.
    var stop: (() -> Void)?
    var send: () -> Void
    /// What goes with the message, quoted over the field inside its box (#986).
    var quotes: Binding<[ContextRef]>? = nil
    /// The conversation whose sent messages ↑ / ↓ and ⌃R bring back (the Mac, `MacSendHistory`); nil: none.
    var history: String? = nil
    /// The main conversation cannot answer yet: send says what to do first and goes there, the draft stays.
    var gate: SendGate? = nil
    @ViewBuilder var menu: () -> MenuItems

    /// The symbols' frame: the button styles pad it to about 44 pt, the field's height.
    private static var glyph: CGFloat { 31 }

    var body: some View {
        HStack(alignment: .bottom, spacing: Theme.Space.s) {
            Menu { menu() } label: {
                ThemedIcon(Theme.Icon.add).font(Theme.Font.heading).frame(width: Self.glyph, height: Self.glyph)
            }
            #if os(macOS)
            // A Mac menu draws as a pull-down button with its arrow beside the label: clipped to the circle, the +
            // was cut in half (2026-11-02). As a plain button with no arrow, in a round glass of its own.
            .menuStyle(.button)
            .menuIndicator(.hidden)
            .macRoundButton()
            #else
            .themedButton()
            .clipShape(.circle)
            #endif

            VStack(alignment: .leading, spacing: 0) {
                if let quotes, !quotes.wrappedValue.isEmpty {
                    QuoteBar(refs: quotes)
                        .padding(.leading, Theme.Space.l)
                        .padding(.trailing, Theme.Space.xs)
                        .padding(.top, Theme.Space.s)
                }
                TextField(placeholder, text: $text, selection: $selection, axis: .vertical)
                    .lineLimit(1...6)
                    #if os(macOS)
                    .textFieldStyle(.plain) // the surface below is the field's box; the Mac's own bezel drew a second one inside
                    .macComposerKeys(focused: focused.wrappedValue, canSend: canSend, // ⏎ sends, ⇧⏎ a new line
                                     history: history.map { MacComposerHistory(key: $0, text: $text, focus: { focused.wrappedValue = true }) }, send: send)
                    #endif
                    .focused(focused)
                    .padding(.horizontal, Theme.Space.l)
                    .padding(.vertical, Theme.Space.m)
                    .scaledFrame(minHeight: Theme.Size.tapTarget)
            }
            .surface(interactive: true, in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))

            if let gate {
                SendGateButton(gate: gate)
            } else if let stop, !canSend {
                Button(action: stop) {
                    ThemedIcon(Theme.Icon.interrupt).font(Theme.Font.heading).frame(width: Self.glyph, height: Self.glyph)
                }
                #if os(macOS)
                .macRoundButton(prominent: true)
                #else
                .themedButton(prominent: true)
                .clipShape(.circle)
                #endif
                .tint(Theme.danger)
                .accessibilityLabel("Interrupt [Composer Buttons f9d193]")
            } else {
                Button(action: sendTyped) {
                    ThemedIcon(Theme.Icon.send).font(Theme.Font.heading).frame(width: Self.glyph, height: Self.glyph)
                }
                #if os(macOS)
                .macRoundButton(prominent: true)
                #else
                .themedButton(prominent: true)
                .clipShape(.circle)
                #endif
                .disabled(!canSend)
                .keyboardShortcut(.return, modifiers: .command)
                .accessibilityLabel("Send")
            }
        }
    }
}

extension ComposerRow {
    /// Sends what the field shows: the keyboard's composition confirmed into the draft first (`TypedText`, #2203).
    fileprivate func sendTyped() {
        #if os(iOS) || os(visionOS)
        if focused.wrappedValue { TypedText.commitFocused(into: $text) }
        #endif
        #if os(macOS)
        if let history { MacSendHistory.shared.record(history, text) }
        #endif
        send()
    }
}

/// Photos picked for the next message, above the composer: × takes one out; with `edit`, a tap marks it up first.
struct ComposerImages: View {
    @Binding var images: [(UIImage, ImagePayload)]
    var edit: ((Int) -> Void)?

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack {
                ForEach(Array(images.enumerated()), id: \.offset) { idx, pair in
                    #if os(macOS)
                    MacComposerThumb(image: pair.0, index: idx, editable: edit != nil,
                                     edit: { edit?(idx) }, remove: { images.remove(at: idx) })
                    #else
                    Image(uiImage: pair.0).resizable().scaledToFill().scaledFrame(width: Theme.Size.box56, height: Theme.Size.box56)
                        .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
                        .overlay(alignment: .bottomLeading) {
                            if edit != nil {
                                ThemedIcon("pencil.tip.crop.circle.fill").font(Theme.Font.label).foregroundStyle(.white)
                                    .shadow(radius: 2).padding(Theme.Space.xs)
                            }
                        }
                        .onTapGesture { edit?(idx) }
                        .accessibilityLabel(edit == nil ? String(localized: "Image \(idx + 1) [Composer Parts 0d2e3a]") : String(localized: "Image \(idx + 1), tap to edit"))
                        .overlay(alignment: .topTrailing) {
                            Button { images.remove(at: idx) } label: {
                                ThemedIcon(Theme.Icon.clear).symbolRenderingMode(.palette)
                                    .foregroundStyle(.white, .black.opacity(0.6))
                            }
                            .offset(x: 5, y: -5)
                            .accessibilityLabel("Remove Image \(idx + 1)") // same key as the Mac thumbnail
                        }
                    #endif
                }
            }
            .padding(.top, Theme.Space.sm)
        }
    }
}
