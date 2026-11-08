package persona.platform.notify

import persona.core.contracts.DialogOption
import persona.core.contracts.Interaction
import persona.core.contracts.NotifyEvent
import persona.core.contracts.Question
import persona.core.contracts.QuestionOption
import persona.core.contracts.ScreenDialog
import persona.core.contracts.SessionKey
import persona.core.domain.answer.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import java.io.File

class NotificationPolicyTest {
    @Test fun notificationActionsMatchUnifiedFixtures() {
        val fixture = File(System.getProperty("persona.repo.root"), "spec/fixtures/approval/unified.json")
        val cases = Json.parseToJsonElement(fixture.readText()).jsonObject.getValue("cases").jsonArray
        var checked = 0
        cases.forEach { element ->
            val case = element.jsonObject
            val input = case.getValue("input").jsonObject
            if (input.getValue("entry").jsonPrimitive.content != "notification") return@forEach
            val kind = input.getValue("kind").jsonPrimitive.content
            val interaction = interaction(kind)
            val expected = case.getValue("expected").jsonObject.getValue("actions").jsonArray
                .map { it.jsonObject }.filter { it.getValue("id").jsonPrimitive.content != "open_app" }
            val actions = NotificationPolicy.actions(interaction)
            actions.forEach { action ->
                assertEquals(AnswerRetry.Never, action.retry)
                assertEquals(AnswerBuilder.build(interaction, action.answer, action.draft,
                    AnswerContext(entry = AnswerEntry.Conversation, fullPlanRead = true)).resolution,
                    NotificationPolicy.resolution(interaction, action.key, null))
            }
            expected.forEach { action ->
                val id = action.getValue("id").jsonPrimitive.content
                val actual = if (id == "choose_option") actions else actions.filter { it.key == id }
                assertTrue(case.getValue("name").jsonPrimitive.content, actual.isNotEmpty())
                actual.forEach {
                    assertEquals(action.getValue("authentication").jsonPrimitive.content == "required", it.authenticationRequired)
                    assertEquals(action["input"] != null, it.replyLabel != null)
                }
            }
            if (kind == "question" || kind == "screen") assertEquals(3, actions.size)
            checked++
        }
        assertEquals(8, checked)
        assertTrue(NotificationPolicy.actions(interaction("question").copy(questions = listOf(
            Question(question = "选择", multiSelect = true, options = listOf(QuestionOption("选项"))),
        ))).isEmpty())
        assertEquals("acceptEdits", NotificationPolicy.resolution(interaction("plan"), "approve_edits", null)?.mode)
        assertEquals("需要修改", NotificationPolicy.resolution(interaction("plan"), "reject", "需要修改")?.feedback)
        assertEquals("不允许", NotificationPolicy.resolution(interaction("permission"), "deny", "不允许")?.message)
        assertNull(NotificationPolicy.resolution(interaction("permission"), "approve_edits", null))
        for (kind in listOf("plan", "permission", "question", "screen", "elicitation", "unknown")) {
            assertNull(NotificationPolicy.resolution(interaction(kind), "approve_all", null))
            assertNull(NotificationPolicy.resolution(interaction(kind), "always", null))
        }
        assertTrue(NotificationPolicy.actions(interaction("elicitation")).isEmpty())
        assertTrue(NotificationPolicy.actions(interaction("unknown")).isEmpty())
        assertTrue(NotificationPolicy.actions(interaction("question").copy(questions =
            interaction("question").questions.orEmpty() + interaction("question").questions.orEmpty())).isEmpty())
        val screen = interaction("screen").copy(dialog = ScreenDialog(options = listOf(
            DialogOption("unknown", "未知操作", kind = "unknown"),
            DialogOption("chat", "输入意见", kind = "chat"),
            DialogOption("text", "自填", kind = "type_something"),
        )))
        assertTrue(NotificationPolicy.actions(screen).isEmpty())
        assertNull(NotificationPolicy.resolution(screen, "option.unknown", null))
    }

    @Test fun sayingNoComesBeforeSayingYes() {
        // The same order as the ongoing notification and Android's dialogs: the negative button on the left.
        assertEquals(listOf(R.string.notify_action_deny, R.string.notify_action_allow),
            NotificationPolicy.actions(interaction("permission")).map { it.labelRes })
        assertEquals(listOf(R.string.notify_action_reject, R.string.notify_action_approve),
            NotificationPolicy.actions(interaction("plan")).map { it.labelRes })
        // Options are choices, not yes and no: they stay in the order the question gives them.
        assertEquals(listOf("选项 0", "选项 1", "选项 2"), NotificationPolicy.actions(interaction("question")).map { it.label })
    }

    @Test fun onlyHubNotifyPostsCompletionAndOnlyVisibleConversationIsSuppressed() {
        assertEquals("alert", NotificationPolicy.channel(NotifyEvent("alert", "machine", "session")))
        assertEquals(Notifier.CH_DONE, NotificationPolicy.channel(NotifyEvent("done", "machine", "session")))
        assertEquals(Notifier.CH_DONE, NotificationPolicy.channel(NotifyEvent("reply", "machine", "session")))
        assertEquals(Notifier.CH_ASSISTANT, NotificationPolicy.channel(NotifyEvent("assistant", "machine", "session")))
        assertNull(NotificationPolicy.channel(NotifyEvent("interaction", "machine", "session")))
        assertNull(NotificationPolicy.channel(NotifyEvent("unknown", "machine", "session")))
        val session = SessionKey("machine", "session")
        assertTrue(NotificationPolicy.suppressed(session, true, session))
        assertFalse(NotificationPolicy.suppressed(session, false, session))
        assertFalse(NotificationPolicy.suppressed(session, true, SessionKey("machine", "other")))
        assertFalse(NotificationPolicy.suppressed(session, true, null))
    }

    @Test fun separateExtensionAlertsCoexistAndOnlyCompleteSessionKeysCanOpenAConversation() {
        val alert = NotifyEvent("alert", "machine", "session")
        assertEquals(SessionKey("machine", "session"), NotificationPolicy.session(alert))
        for (event in listOf(alert.copy(machineId = ""), alert.copy(sessionId = ""),
            alert.copy(machineId = " ", sessionId = " "))) {
            assertNull(NotificationPolicy.session(event))
        }
        val first = NotificationPolicy.tag(alert)
        val second = NotificationPolicy.tag(alert.copy(body = "另一个提醒"))
        assertTrue(first.startsWith("alert:"))
        assertNotEquals(first, second)
        assertNotEquals(NotificationPolicy.tag(alert.copy(machineId = "", sessionId = "")), first)
        assertEquals("done:machine/session", NotificationPolicy.tag(alert.copy(kind = "done")))
        assertEquals("assistant:machine/session", NotificationPolicy.tag(alert.copy(kind = "assistant")))
    }

    /** An alert sent again with the same id replaces the earlier one, as apns-collapse-id does on an iPhone (#5614). */
    @Test fun alertsWithTheSameIdReplaceEachOther() {
        val alert = NotifyEvent("alert", "", "", title = "进度 50%", id = "progress-1")
        assertEquals(NotificationPolicy.tag(alert), NotificationPolicy.tag(alert.copy(title = "进度 80%")))
        assertNotEquals(NotificationPolicy.tag(alert), NotificationPolicy.tag(alert.copy(id = "progress-2")))
        val plain = alert.copy(id = null)
        assertNotEquals(NotificationPolicy.tag(plain), NotificationPolicy.tag(plain))
        assertNotEquals(NotificationPolicy.tag(plain), NotificationPolicy.tag(alert))
    }

    /** A card with fields to fill in has no Allow button (#4987): the hub refuses allow without their values. */
    @Test fun permissionWithRequiredFieldsHasNoAllow() {
        val card = persona.core.contracts.JsonCodec.decodeFromString<Interaction>(
            """{"id":"request","kind":"permission","fields":[{"key":"password","label":"Password","secret":true}]}""")
        assertEquals(listOf("deny"), NotificationPolicy.actions(card).map { it.key })
        assertNull(NotificationPolicy.resolution(card, "allow", null))
    }

    /** The assistant's password card (#1137): only Deny, and Deny takes no typed reason; nothing typed is ever sent. */
    @Test fun passwordCardOnlyDeniesWithoutAReply() {
        val card = persona.core.contracts.JsonCodec.decodeFromString<Interaction>(
            """{"id":"request","kind":"permission","tool_name":"secret_run","tool_input":{"machine_name":"Mac mini"},
                "fields":[{"key":"password","label":"Password","secret":true}]}""")
        val actions = NotificationPolicy.actions(card)
        assertEquals(listOf("deny"), actions.map { it.key })
        assertEquals(listOf("deny"), NotificationPolicy.actions(card.copy(fields = null)).map { it.key })
        assertNull(actions.single().replyLabel)
        assertEquals(persona.core.contracts.Resolution(allow = false), NotificationPolicy.resolution(card, "deny", null))
        assertNull(NotificationPolicy.resolution(card, "deny", "typed by mistake"))
        assertNull(NotificationPolicy.resolution(card, "allow", null))
    }

    private fun interaction(kind: String) = Interaction("request", kind = kind,
        questions = listOf(Question(question = "选择", options = List(4) { QuestionOption("选项 $it") })),
        dialog = ScreenDialog(options = listOf(DialogOption("input", "输入", kind = "type_something")) +
            List(4) { DialogOption("$it", "选项 $it") }),
    )
}
