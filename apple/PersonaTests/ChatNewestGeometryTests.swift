import PersonaKit
import Testing
import UIKit
@testable import Persona

@MainActor struct ChatNewestGeometryTests {
    private func fixture(offset: CGFloat) -> (ChatPin, UIScrollView) {
        let list = UIScrollView(frame: CGRect(x: 0, y: 0, width: 320, height: 640))
        list.contentInsetAdjustmentBehavior = .never
        list.contentSize = CGSize(width: 320, height: 2400)
        list.contentInset = UIEdgeInsets(top: 10, left: 0, bottom: 100, right: 0)
        list.contentOffset = CGPoint(x: 0, y: offset)
        let pin = ChatPin()
        pin.list = list
        return (pin, list)
    }

    @Test func actualInsetsCorrectStaleNewestStateWithoutMovingTheList() throws {
        let (pin, list) = fixture(offset: -10)
        var follow = ChatFollow()
        follow.scrolled(gap: 1480)
        follow.follow()
        #expect(!follow.near)
        let before = list.contentOffset
        let gap = try #require(pin.measuredNewestGap)
        #expect(gap == 0)
        follow.scrolled(gap: gap)
        #expect(follow.near && follow.follows && follow.mode == .following)
        #expect(list.contentOffset == before)
    }

    @Test func anInterruptedJumpDoesNotInventNearOrFollowing() throws {
        let (pin, list) = fixture(offset: 400)
        var follow = ChatFollow()
        follow.read()
        let before = list.contentOffset
        follow.scrolled(gap: try #require(pin.measuredNewestGap))
        #expect(!follow.near && !follow.follows && follow.mode == .reading)
        #expect(list.contentOffset == before)
    }

    @Test func measuringNearDoesNotOverwriteReadingIntent() throws {
        let (pin, list) = fixture(offset: -10)
        var follow = ChatFollow()
        follow.read()
        follow.scrolled(gap: try #require(pin.measuredNewestGap))
        #expect(follow.near && !follow.follows && follow.mode == .reading)
        #expect(list.contentOffset.y == -10)
    }

    @Test func anotherVisibleOwnerDoesNotKeepAHiddenViewsCompletionEligible() {
        let model = AppModel()
        let key = SessionKey(group: "geometry-fixture", machineID: "box", sessionID: UUID().uuidString)
        model.chat(key).apply([], span: ItemSpan(), backlog: true, hasMore: false)
        let hidden = UUID(), visible = UUID()
        model.subscribe(key, by: hidden)
        model.subscribe(key, by: visible)
        #expect(model.isSubscribed(key, by: hidden))
        model.unsubscribe(key, by: hidden)
        #expect(model.isSubscribed(key))
        #expect(!model.isSubscribed(key, by: hidden))
        #expect(model.isSubscribed(key, by: visible))
        model.unsubscribe(key, by: visible)
        #expect(!model.isSubscribed(key, by: visible))
    }

    @Test func aMissingScrollContainerIsNotAZeroMeasurement() {
        #expect(ChatPin().measuredNewestGap == nil)
    }
}
