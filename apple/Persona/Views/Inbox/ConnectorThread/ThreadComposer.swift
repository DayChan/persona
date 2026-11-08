import SwiftUI

/// The box at the bottom of a connector's conversation page (Telegram's chat, GitHub's thread): what is answered over
/// the field, send once there are words, a spinner while it goes. What the user sends here is theirs: the hub sends it
/// without a card (#1190; connector_gate is for the AI's writes).
struct ThreadComposer: View {
    var placeholder: String
    @Binding var text: String
    @Binding var selection: TextSelection?
    var focused: FocusState<Bool>.Binding
    /// What the reply answers, over the field ("Replying to <who>", its words); nil: nothing.
    var quote: (heading: String, summary: String)?
    var sending: Bool
    var cancelQuote: () -> Void
    var send: () -> Void

    var body: some View {
        HStack(alignment: .bottom, spacing: Theme.Space.s) {
            VStack(alignment: .leading, spacing: 0) {
                if let quote {
                    QuoteBarRow(heading: quote.heading, summary: quote.summary, lines: 1, tint: LarkColor.blue,
                                removeLabel: String(localized: "Cancel Reply"), remove: cancelQuote)
                        .padding(.leading, Theme.Space.l)
                        .padding(.trailing, Theme.Space.xs)
                        .padding(.top, Theme.Space.s)
                }
                TextField(placeholder, text: $text, selection: $selection, axis: .vertical)
                    .lineLimit(1...5)
                    .focused(focused)
                    .disabled(sending)
                    .accessibilityIdentifier("thread.box")
                    .padding(.horizontal, Theme.Space.l)
                    .padding(.vertical, Theme.Space.s)
            }
            .surface(interactive: true, in: .rect(cornerRadius: Theme.Radius.bubble, style: .continuous))
            if sending {
                ProgressView().scaledFrame(width: Theme.Size.wellL, height: Theme.Size.wellL)
            } else if LarkComposer.canSend(text) {
                Button(action: send) {
                    ThemedIcon(Theme.Icon.send).font(Theme.Font.heading)
                        .scaledFrame(width: Theme.Size.wellL, height: Theme.Size.wellL)
                }
                .themedButton(prominent: true)
                .clipShape(.circle)
                .accessibilityLabel("Send")
                .accessibilityIdentifier("thread.send")
                .transition(.scale.combined(with: .opacity))
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.bottom, Theme.Space.s)
        .animation(Theme.Motion.standard, value: quote.map { $0.heading + "\n" + $0.summary })
        .animation(Theme.Motion.standard, value: sending)
    }
}
