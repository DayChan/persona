import SwiftUI

/// What a composer needs to put the keyboard up on the Type to Persona control's ask (`AppModel.openTypingConversation`): the page
/// in front and the app active (a cold start from the Lock Screen has the app launching, and the keyboard does not come up then),
/// once for each ask, and only for the composer the ask names. Not `AppModel.composerFocus` (⌘I on the Mac): that one has no
/// active-app gate, no expiry, and cannot name the new conversation page, which has no session yet.
private struct TypingRequestTaker: ViewModifier {
    let target: TypingRequest.Target
    let focused: FocusState<Bool>.Binding
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase

    /// What the task is keyed on: it starts again when an ask comes, and when the app turns active or the page comes to the front.
    nonisolated private struct Trigger: Hashable, Sendable {
        var asks: Int
        var active: Bool
    }

    /// A field just put on screen (the conversation sliding in, the app coming up from the Lock Screen) does not take focus at
    /// once: it is set again until it holds.
    private static let tries = 5
    private static let pause = Duration.milliseconds(250)

    func body(content: Content) -> some View {
        content.tabActiveTask(id: Trigger(asks: model.typingAsks, active: phase == .active)) {
            let serial = model.typingAsks
            guard model.takeTypingRequest(for: target, appActive: phase == .active) else { return }
            for _ in 0..<Self.tries {
                try? await Task.sleep(for: Self.pause)
                if Task.isCancelled { break }
                focused.wrappedValue = true
                try? await Task.sleep(for: Self.pause)
                if focused.wrappedValue { return }
            }
            // Stopped (or never held) before the keyboard came up: the next time this page is in front tries again.
            if Task.isCancelled { model.giveBackTypingRequest(serial) }
        }
    }
}

extension View {
    /// Focuses `focused` when the Type to Persona control asked for `target`'s composer; nothing when `target` is nil (the
    /// composer then does not even watch the ask).
    @ViewBuilder func takesTypingRequest(_ target: TypingRequest.Target?, focused: FocusState<Bool>.Binding) -> some View {
        if let target { modifier(TypingRequestTaker(target: target, focused: focused)) } else { self }
    }
}
