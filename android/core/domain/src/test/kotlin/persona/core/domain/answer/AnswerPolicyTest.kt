package persona.core.domain.answer

import persona.core.domain.zhAny

import persona.core.contracts.DialogOption
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.QuestionOption
import persona.core.contracts.ScreenDialog
import org.junit.Assert.*
import org.junit.Test

class AnswerPolicyTest {
    @Test fun entryAndFormGatesApplyTogether() {
        val plan = Interaction("request", kind = "plan")
        for (entry in AnswerEntry.entries) {
            val unlocked = AnswerContext(entry = entry, fullPlanRead = true)
            val all = AnswerBuilder.build(plan, AnswerAction.ApproveAll, context = unlocked)
            assertEquals(entry.name, entry == AnswerEntry.Conversation, all.allowed)
            val edits = AnswerBuilder.build(plan, AnswerAction.ApproveEdits, context = unlocked)
            assertEquals("acceptEdits", (edits.resolution?.mode).zhAny())
            assertFalse(AnswerBuilder.build(plan, AnswerAction.ApproveEdits, context = unlocked.copy(locked = true)).allowed)
            assertFalse(AnswerBuilder.build(plan, AnswerAction.ApproveEdits,
                AnswerDraft(feedback = "先补测试"), unlocked).allowed)
        }
        val lockedNotification = AnswerContext(entry = AnswerEntry.Notification, locked = true)
        assertTrue(AnswerBuilder.build(plan, AnswerAction.Reject,
            AnswerDraft(feedback = "先补测试"), lockedNotification).allowed)
        val permission = Interaction("request", kind = "permission")
        val denial = AnswerBuilder.build(permission, AnswerAction.Deny,
            AnswerDraft(message = "请检查"), lockedNotification)
        assertEquals("请检查", (denial.resolution?.message).zhAny())
        val question = Interaction("request", kind = "question", questions = listOf(
            Question("选择？", options = (1..4).map { QuestionOption("选项 $it") })))
        assertFalse(AnswerBuilder.build(question, AnswerAction.PickOption, context = lockedNotification).allowed)
        val notification = lockedNotification.copy(locked = false)
        assertTrue(AnswerBuilder.build(question, AnswerAction.PickOption, context = notification).allowed)
        assertFalse(AnswerBuilder.build(question, AnswerAction.PickOption, AnswerDraft(optionIndex = 3), notification).allowed)
        val questions = requireNotNull(question.questions)
        assertFalse(AnswerBuilder.build(question.copy(questions = questions + questions),
            AnswerAction.PickOption, context = notification).allowed)
        assertFalse(AnswerBuilder.build(question.copy(questions = listOf(questions.single().copy(multiSelect = true))),
            AnswerAction.PickOption, context = notification).allowed)
        val screen = Interaction("request", kind = "screen", dialog = ScreenDialog(options = listOf(
            DialogOption("1", "确认"), DialogOption("2", "Type something.", kind = "type_something"))))
        assertFalse(AnswerBuilder.build(screen, AnswerAction.ScreenOption, AnswerDraft(option = "1"), lockedNotification).allowed)
        assertTrue(AnswerBuilder.build(screen, AnswerAction.ScreenOption, AnswerDraft(option = "1"), notification).allowed)
        assertFalse(AnswerBuilder.build(screen, AnswerAction.ScreenOption,
            AnswerDraft(option = "2", text = "继续"), notification).allowed)
        assertFalse(AnswerBuilder.build(question, AnswerAction.PickOption,
            context = AnswerContext(entry = AnswerEntry.Watch, locked = true)).allowed)
    }

    /** A multi-select dialog is one path: a tap ticks, submit sends the whole set (and its free text) at once. */
    @Test fun multiSelectDialogTicksOptionsAndSubmitsThemWhole() {
        val dialog = Interaction("request", kind = "screen", dialog = ScreenDialog(multi = true, submit = "Next", options = listOf(
            DialogOption("1", "预览"), DialogOption("3", "线上", checked = true),
            DialogOption("4", "Type something.", kind = "type_something"), DialogOption("5", "Chat about this", kind = "chat"))))
        // A tap neither answers nor resolves: it only moves the ticked set.
        val ticked = AnswerBuilder.build(dialog, AnswerAction.ScreenOption, AnswerDraft(option = "1"))
        assertTrue(ticked.allowed)
        assertNull(ticked.resolution)
        assertEquals(setOf("1"), ticked.draft.selected)
        val unticked = AnswerBuilder.build(dialog, AnswerAction.ScreenOption, ticked.draft.copy(option = "1"))
        assertEquals(emptySet<String>(), unticked.draft.selected)
        // Nothing ticked and nothing typed: submit stays blocked.
        assertFalse(AnswerBuilder.build(dialog, AnswerAction.ScreenSubmit).allowed)
        val sent = AnswerBuilder.build(dialog, AnswerAction.ScreenSubmit, AnswerDraft(selected = setOf("3", "1"), text = " 补充 "))
        assertEquals(listOf("1", "3"), sent.resolution?.options) // the dialog's order, not the tap order
        assertEquals("补充", (sent.resolution?.text).zhAny())
        // "Chat about this" hands the question back even in a multi-select dialog.
        val chat = AnswerBuilder.build(dialog, AnswerAction.ScreenOption, AnswerDraft(option = "5"))
        assertEquals("5", (chat.resolution?.option).zhAny())
        // A single-choice dialog keeps the one-tap answer, and submit does not exist for it.
        val single = dialog.copy(dialog = dialog.dialog!!.copy(multi = false))
        assertEquals("1", (AnswerBuilder.build(single, AnswerAction.ScreenOption, AnswerDraft(option = "1")).resolution?.option).zhAny())
        assertFalse(AnswerBuilder.build(single, AnswerAction.ScreenSubmit).allowed)
    }

    @Test fun multipleQuestionsWaitForSubmitAndKeepCustomMultiselectAnswer() {
        val first = Question("第一题？", options = listOf(QuestionOption("选项")))
        val last = first.copy(question = "第二题？")
        val interaction = Interaction("request", kind = "question", questions = listOf(first, last))
        val pickedFirst = AnswerBuilder.build(interaction, AnswerAction.PickOption)
        assertEquals(1, pickedFirst.nextPage)
        assertNull(pickedFirst.resolution)
        val pickedLast = AnswerBuilder.build(interaction, AnswerAction.PickOption, pickedFirst.draft)
        assertNull(pickedLast.resolution)
        val submitted = AnswerBuilder.build(interaction, AnswerAction.Submit, pickedLast.draft)
        assertEquals(mapOf(first.question to "选项", last.question to "选项"), submitted.resolution?.answers)
        val multi = interaction.copy(questions = listOf(first.copy(multiSelect = true)))
        val picked = AnswerBuilder.build(multi, AnswerAction.PickOption,
            AnswerDraft(other = mapOf(first.question to "  自填  ")))
        assertEquals("自填", (AnswerBuilder.build(multi, AnswerAction.Submit, picked.draft).resolution?.answers?.get(first.question)).zhAny())
        assertFalse(AnswerTransport.begin(submitted.resolution!!, true, true, "unknown").allowed)
    }
}
