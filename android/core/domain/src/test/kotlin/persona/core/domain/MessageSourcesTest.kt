package persona.core.domain

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Injected
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.MessageSource

class MessageSourcesTest {
    private fun item(source: MessageSource? = null, injected: Injected? = null, from: String? = null) =
        Item("i", role = "user", kind = "text", text = "Ordinary message", source = source, injected = injected, from = from)

    @Test fun wirePreservesOptionalSourceAndFutureKindsWithoutChangingRole() {
        val decoded = JsonCodec.decodeFromString(Item.serializer(), """{"id":"i","kind":"text","role":"user","text":"Hello","from":"Peer","source":{"kind":"future","conversation_id":"cv_1","title":"Review","extra":true},"extra":1}""")
        assertEquals(MessageSource("future", "cv_1", "Review"), decoded.source)
        assertEquals("user", decoded.role)
        assertEquals("Peer", decoded.from)
        assertFalse(isOwnMessage(decoded))
        assertNull(JsonCodec.decodeFromString(Item.serializer(), """{"id":"old","role":"user","kind":"text"}""").source)
    }

    @Test fun historyRealtimeAndCachedItemRoundTripKeepTheSameSource() {
        val rawItem = """{"id":"i","seq":1,"role":"user","kind":"text","text":"Report","source":{"kind":"conversation","conversation_id":"cv_1","title":"Review"}}"""
        val history = JsonCodec.decodeFromString(persona.core.contracts.ItemsResponse.serializer(), """{"items":[$rawItem],"history":"epoch","from_seq":1,"to_seq":1}""")
        val realtime = JsonCodec.decodeFromString(persona.core.contracts.ItemsEvent.serializer(), """{"machine_id":"computer","session_id":"conversation","items":[$rawItem]}""")
        val cached = JsonCodec.decodeFromString(Item.serializer(), JsonCodec.encodeToString(Item.serializer(), history.items.single()))
        assertEquals(history.items.single(), realtime.items.single())
        assertEquals(history.items.single(), cached)
        assertEquals("cv_1", sourceOf(cached)?.conversationId)
        assertFalse(isOwnMessage(cached))
    }

    @Test fun explicitMetadataWinsAndPeerNamesNeverBecomeConversationIds() {
        val explicit = MessageSource("conversation", "cv_2", "Review")
        assertEquals(explicit, sourceOf(item(explicit, Injected("followup", "daily"), "Peer")))
        assertEquals(MessageSource("peer", title = "Peer"), sourceOf(item(from = "Peer")))
        assertNull(sourceOf(item(from = " ")))
    }

    @Test fun injectedMetadataMapsRoutinesFollowupsAndSupervision() {
        listOf("once", "wake").forEach { assertEquals("followup", sourceOf(item(injected = Injected("followup", it)))?.kind) }
        listOf("daily", "interval").forEach { assertEquals("patrol", sourceOf(item(injected = Injected("followup", it)))?.kind) }
        assertEquals("supervision", sourceOf(item(injected = Injected("system", "supervise")))?.kind)
        assertEquals("followup", sourceOf(item(injected = Injected("limit_continue")))?.kind)
        assertNull(sourceOf(item(injected = Injected("task_notice", "done"))))
        assertNull(sourceOf(item(injected = Injected("system", "lost_tasks"))))
    }

    @Test fun ordinaryUsersAndRepliesStayOrdinaryEvenWhenTheyMentionSources() {
        val user = item().copy(text = "Routine check: follow up with supervision and another conversation", clientMsgId = "client_1")
        assertNull(sourceOf(user))
        assertTrue(isOwnMessage(user))
        assertNull(sourceOf(item(MessageSource("patrol")).copy(role = "assistant")))
    }

    private fun relay(id: String = "cv_origin", returnId: String = id, tool: String = "conversation_send") =
        """<persona-context kind="context" title="来自对话「Review &amp; notes」" url="">
这条消息不是用户本人发的，是 Persona 的另一段对话（$id）用 $tool 发来的，第 1 跳（最多 4 跳）。
按里面说的做。要把结果告诉那段对话就用 conversation_send 发回 $returnId；只是确认收到就不要回，免得两段对话来回发。
</persona-context>
The report is ready.""" // i18n-ignore: exact persisted relay fixture

    @Test fun canonicalRelayRequiresTheWholeTemplateAndMatchingIds() {
        val canonical = item().copy(text = relay())
        assertEquals(MessageSource("conversation", "cv_origin", "Review & notes"), sourceOf(canonical))
        assertEquals("conversation", sourceOf(canonical.copy(text = relay(tool = "conversation_new")))?.kind)
        assertNull(sourceOf(canonical.copy(text = relay(returnId = "cv_other"))))
        assertNull(sourceOf(canonical.copy(text = relay(tool = "unknown"))))
        assertNull(sourceOf(canonical.copy(text = "User words\n" + relay())))
        assertNull(sourceOf(canonical.copy(text = "<persona-context kind=\"context\" title=\"来自对话「Review」\">Ordinary quote</persona-context>"))) // i18n-ignore: partial relay lookalike
        assertNull(sourceOf(canonical.copy(clientMsgId = "client_1")))
        assertTrue(isOwnMessage(canonical.copy(clientMsgId = "client_1")))
        val context = parseContexts(relay()).first.single()
        assertTrue(isRelaySourceContext(context))
        assertEquals("The report is ready.", parseContexts(relay()).second)
    }

    @Test fun persistedSourceAndUserSentinelSurviveHistoryAndFenceNestedInference() {
        val marker = """<persona-context kind="message-source" title="">{"kind":"supervision","title":"Review","extra":true}</persona-context>"""
        assertEquals(MessageSource("supervision", title = "Review"), sourceOf(item().copy(text = marker + relay())))
        assertEquals("Review", sourceOf(item().copy(text = "<persona-context kind=\"context\" title=\"Snapshot\">State</persona-context>" + marker))?.title)
        val user = """<persona-context kind="message-source" title="">{"author":"user"}</persona-context>"""
        val protected = item().copy(text = user + marker + relay())
        assertNull(sourceOf(protected))
        assertNull(injectedOf(protected))
        assertTrue(isOwnMessage(protected))
        assertNull(sourceOf(item().copy(text = "<persona-context kind=\"message-source\">not JSON</persona-context>" + relay())))
        assertTrue(parseContexts(user + marker).first.isEmpty())
    }

    @Test fun groupingUsesConversationIdentityBeforeTitle() {
        val first = MessageSource("conversation", "cv_1", "Review")
        assertTrue(sameMessageSource(first, first.copy(title = "Renamed")))
        assertFalse(sameMessageSource(first, first.copy(conversationId = "cv_2")))
        assertFalse(sameMessageSource(first, null))
        assertFalse(sameMessageSource(first, first.copy(kind = "followup")))
        assertTrue(sameMessageSource(MessageSource("patrol"), MessageSource("patrol", title = " ")))
    }
}
