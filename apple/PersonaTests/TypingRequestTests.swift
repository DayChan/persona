import Foundation
import Testing
@testable import Persona

/// The Type to Persona control's ask (#1996): the composer it names focuses once, only while the app is active, and an ask nobody
/// took in time does not raise the keyboard later.
@MainActor
struct TypingRequestTests {
    private let main = SessionKey(group: "", machineID: "hub", sessionID: "main")
    private let other = SessionKey(group: "", machineID: "hub", sessionID: "agent")

    @Test func theNamedComposerTakesTheAskOnce() {
        var request = TypingRequest()
        request.ask(.conversation(main))
        let took1 = request.take(for: .conversation(main), appActive: true)
        #expect(took1)
        let took2 = request.take(for: .conversation(main), appActive: true)
        #expect(!took2)
    }

    @Test func anotherComposerLeavesTheAskForTheNamedOne() {
        var request = TypingRequest()
        request.ask(.conversation(main))
        let took3 = request.take(for: .conversation(other), appActive: true)
        #expect(!took3)
        let took4 = request.take(for: .newConversation, appActive: true)
        #expect(!took4)
        let took5 = request.take(for: .conversation(main), appActive: true)
        #expect(took5)
    }

    @Test func theAskWaitsWhileTheAppIsNotActive() {
        var request = TypingRequest()
        request.ask(.newConversation)
        let took6 = request.take(for: .newConversation, appActive: false)
        #expect(!took6)
        let took7 = request.take(for: .newConversation, appActive: true)
        #expect(took7)
    }

    @Test func aStaleAskIsSpentWithoutFocusing() {
        var request = TypingRequest()
        let asked = Date()
        request.ask(.conversation(main), now: asked)
        let took8 = request.take(for: .conversation(main), appActive: true, now: asked.addingTimeInterval(TypingRequest.lifetime + 1))
        #expect(!took8)
        // Spent: the conversation opened later does not focus either.
        let took9 = request.take(for: .conversation(main), appActive: true)
        #expect(!took9)
    }

    @Test func eachPressAsksAgain() {
        var request = TypingRequest()
        request.ask(.conversation(main))
        let took10 = request.take(for: .conversation(main), appActive: true)
        #expect(took10)
        request.ask(.conversation(main))
        #expect(request.serial == 2)
        let took11 = request.take(for: .conversation(main), appActive: true)
        #expect(took11)
    }

    @Test func anAskGivenBackIsTakenAgainUnlessANewerPressReplacedIt() {
        var request = TypingRequest()
        request.ask(.conversation(main))
        let first = request.take(for: .conversation(main), appActive: true)
        #expect(first)
        request.giveBack(1)
        let again = request.take(for: .conversation(main), appActive: true)
        #expect(again)
        request.ask(.conversation(main))
        request.giveBack(1) // an older press's: ignored
        let newer = request.take(for: .conversation(main), appActive: true)
        #expect(newer)
        let spent = request.take(for: .conversation(main), appActive: true)
        #expect(!spent)
    }

    @Test func withoutAMainConversationTheNewConversationPageIsAsked() {
        let model = AppModel()
        model.selectedTab = .chats
        let before = model.assistantReveal
        model.openTypingConversation()
        #expect(model.selectedTab == .assistant)
        #expect(model.assistantCurrent == nil)
        #expect(model.assistantReveal == before + 1)
        #expect(model.typingAsks == 1)
        #expect(!model.takeTypingRequest(for: .conversation(main), appActive: true))
        #expect(model.takeTypingRequest(for: .newConversation, appActive: true))
    }
}
