import Foundation
import Testing
@testable import Persona

@MainActor
struct AnsweredTests {
    @Test func dismissYieldsHandOverText() {
        let ix = Interaction(id: "ix1", machineID: "m", sessionID: "s", kind: .screen)
        let lines = Answered.answerLines(ix, .dismissed)
        #expect(lines == [AnswerLine(label: "", answer: "交回电脑处理")])
    }

    @Test func questionMapsQuestionsToAnswers() {
        let q1 = Question(question: "发吗？", header: nil, multiSelect: false, options: [])
        let q2 = Question(question: "改哪些？", header: nil, multiSelect: true, options: [])
        let ix = Interaction(id: "ix2", machineID: "m", sessionID: "s", kind: .question, questions: [q1, q2])
        let r = Resolution(answers: ["发吗？": "发", "改哪些？": "A, B"])
        let lines = Answered.answerLines(ix, r)
        #expect(lines == [
            AnswerLine(label: "发吗？", answer: "发"),
            AnswerLine(label: "改哪些？", answer: "A, B")
        ])

        let partial = Resolution(answers: ["发吗？": "发"])
        let partialLines = Answered.answerLines(ix, partial)
        #expect(partialLines[1].answer == "（未回答）")
    }

    @Test func planApprovalsAndFeedbacks() {
        let ix = Interaction(id: "ix3", machineID: "m", sessionID: "s", kind: .plan)
        let r1 = Resolution(approve: true, mode: "acceptEdits")
        #expect(Answered.answerLines(ix, r1) == [AnswerLine(label: "", answer: "批准，自动接受编辑")])

        let r2 = Resolution(approve: false, feedback: "先写测试")
        #expect(Answered.answerLines(ix, r2) == [AnswerLine(label: "", answer: "打回：先写测试")])

        let r3 = Resolution(approve: false)
        #expect(Answered.answerLines(ix, r3) == [AnswerLine(label: "", answer: "打回")])
    }

    @Test func permissionAllowedAlwaysAndRefused() {
        let ix = Interaction(id: "ix4", machineID: "m", sessionID: "s", kind: .permission)
        let r1 = Resolution(allow: true, always: true)
        #expect(Answered.answerLines(ix, r1) == [AnswerLine(label: "", answer: "总是允许")])

        let r2 = Resolution(allow: true, always: false)
        #expect(Answered.answerLines(ix, r2) == [AnswerLine(label: "", answer: "允许")])

        let r3 = Resolution(allow: false)
        #expect(Answered.answerLines(ix, r3) == [AnswerLine(label: "", answer: "拒绝")])
    }

    @Test func dialogOptionsAndRedactedInput() {
        let d = ScreenDialog(header: "选择", question: "选哪项？", options: [
            ScreenDialog.Option(key: "1", label: "Yes"),
            ScreenDialog.Option(key: "2", label: "No")
        ], rev: "r1")
        let ix = Interaction(id: "ix5", machineID: "m", sessionID: "s", kind: .screen, dialog: d, secret: false)

        let r1 = Resolution(option: "2")
        #expect(Answered.answerLines(ix, r1) == [AnswerLine(label: "", answer: "No")])

        let r2 = Resolution(text: "还有", options: ["1", "2"])
        #expect(Answered.answerLines(ix, r2) == [AnswerLine(label: "", answer: "Yes，No，还有")])

        let r3 = Resolution(text: "<redacted>")
        #expect(Answered.answerLines(ix, r3) == [AnswerLine(label: "", answer: "（已输入）")])
    }

    @Test func endedStatusTextMapping() {
        #expect(Answered.endedText("expired") == "提问已过期，回答没有送到")
        #expect(Answered.endedText("cancelled") == "电脑上已撤回该提问，回答没有送到")
        #expect(Answered.endedText("dismissed") == "提问已交回电脑处理，回答没有送到")
        #expect(Answered.endedText("unknown") == "提问已结束，回答没有送到")
        #expect(Answered.endedText(nil) == "提问已结束，回答没有送到")
    }

    @Test func resolveResultDecodesAlreadyResponse() throws {
        let json = """
        {
            "ok": true,
            "already": true,
            "status": "resolved",
            "resolution": {
                "allow": true,
                "always": true
            }
        }
        """
        let data = Data(json.utf8)
        let res = try JSONDecoder().decode(ResolveResult.self, from: data)
        #expect(res.ok == true)
        #expect(res.already == true)
        #expect(res.status == "resolved")
        #expect(res.resolution?.allow == true)
        #expect(res.resolution?.always == true)
    }

    @Test func replaceInteractionsDoesNotResurrectLocallyEndedCards() {
        let model = AppModel.shared
        let ix = Interaction(id: "ended1", machineID: "m", sessionID: "s", kind: .screen)
        model.endedInteractionIDs.insert("ended1")
        defer { model.endedInteractionIDs.remove("ended1") }

        model.replaceInteractions("", [ix])
        #expect(model.interactions[ix.uid] == nil)
    }

    /// The pending-card snapshot after a hello that resumed is card frames, not a list: it brought back a card this
    /// device had answered (#1521). Another group's card with the same id is another card.
    @Test func aCardFrameDoesNotResurrectALocallyEndedCard() {
        let model = AppModel()
        let ix = Interaction(id: "ended2", machineID: "m", sessionID: "s", kind: .screen)
        model.endedInteractionIDs.insert(ix.uid)
        model.handle(.interaction(ix), group: "")
        #expect(model.interactions[ix.uid] == nil)

        var other = ix
        other.group = "work"
        model.handle(.interaction(other), group: "work")
        #expect(model.interactions[other.uid] != nil)
    }
}
