package persona.core.contracts

import kotlinx.serialization.decodeFromString
import kotlinx.serialization.encodeToString
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class MessageEchoTest {
    private val sentAt = java.time.Instant.parse("2026-11-03T09:00:00Z")
    private fun user(text: String, secondsAfter: Long = 0, fullSize: Int? = null) =
        Item("uuid", 1, sentAt.plusSeconds(secondsAfter).toString(), role = "user", kind = "text", text = text, fullSize = fullSize)

    @Test fun sameWordsIgnoringWhitespaceAndPlaceholders() {
        assertTrue(MessageEcho.arrived("看看现在有多少\n已经合入的", sentAt.toEpochMilli(), user("[Image #3] 看看现在有多少 已经合入的")))
        assertTrue(MessageEcho.same("<pasted_content>一段比较长的粘贴内容</pasted_content>", "一段比较长的粘贴内容"))
    }

    @Test fun shortTextsMustBeEqual() {
        assertFalse(MessageEcho.same("好", "好的，删掉吧"))
        assertTrue(MessageEcho.same("好", " 好 "))
    }

    @Test fun clockSlackAndOlderMessages() {
        val created = sentAt.toEpochMilli()
        assertTrue(MessageEcho.arrived("继续", created, user("继续", secondsAfter = -100)))
        assertFalse(MessageEcho.arrived("继续", created, user("继续", secondsAfter = -200)))
        assertTrue(MessageEcho.arrived("继续", created, user("继续").copy(ts = null)))
    }

    @Test fun cutTextMatchesByItsStart() {
        val whole = "这是一条很长很长的消息".repeat(50)
        val cut = whole.take(40) + "\n…（已截断，共 9000 字节）"
        assertTrue(MessageEcho.arrived(whole, sentAt.toEpochMilli(), user(cut, fullSize = 9000)))
        assertFalse(MessageEcho.arrived("别的内容" + whole, sentAt.toEpochMilli(), user(cut, fullSize = 9000)))
    }

    @Test fun onlyUserText() {
        assertFalse(MessageEcho.arrived("继续", sentAt.toEpochMilli(), user("继续").copy(role = "assistant")))
    }

    @Test fun taggedItemsMatchByTheirTagOnly() {
        // The hub tags every item a sent message became (#968): other words, a notice, an image, all carry it.
        val compact = Item("notice-1", 2, role = "system", kind = "notice", text = "压缩上下文", clientMsgId = "a")
        assertTrue(MessageEcho.candidate(compact))
        assertTrue(MessageEcho.carries(compact, "a"))
        assertFalse(MessageEcho.carries(compact, "notice-1"))
        assertTrue(MessageEcho.carries(user("完全不同的话").copy(clientMsgId = "a"), "a"))
        // An item tagged for another message is never taken for this one by its words or its own id.
        val other = user("继续").copy(id = "b", clientMsgId = "a")
        assertFalse(MessageEcho.carries(other, "b"))
        assertFalse(MessageEcho.arrived("继续", sentAt.toEpochMilli(), other))
        // Older hubs: untagged items keep their own id and words.
        assertTrue(MessageEcho.carries(user("继续").copy(id = "b"), "b"))
        assertFalse(MessageEcho.carries(user("继续").copy(id = "b", role = "assistant"), "b"))
        assertTrue(MessageEcho.arrived("继续", sentAt.toEpochMilli(), user("继续").copy(clientMsgId = "")))
        assertFalse(MessageEcho.candidate(Item("notice-2", 3, role = "system", kind = "notice", text = "压缩上下文")))
    }

    @Test fun automatedSourcesDoNotSettleAnIdenticallyWordedOutboxMessage() {
        listOf(
            user("Report ready").copy(source = MessageSource("patrol")),
            user("Report ready").copy(injected = Injected("followup", "once")),
            user("Report ready").copy(from = "Peer"),
        ).forEach { item ->
            assertFalse(MessageEcho.candidate(item))
            assertFalse(MessageEcho.carries(item, item.id))
            assertFalse(MessageEcho.arrived("Report ready", sentAt.toEpochMilli(), item))
            assertTrue(MessageEcho.carries(item.copy(clientMsgId = "client_1"), "client_1"))
        }
    }

    @Test fun decodesTheTag() {
        val item = JsonCodec.decodeFromString<Item>("""{"id":"i1","role":"user","kind":"image","client_msg_id":"m1"}""")
        assertEquals("m1", (item.clientMsgId).zhAny())
        assertEquals(null, JsonCodec.decodeFromString<Item>("""{"id":"i2"}""").clientMsgId)
        assertEquals("m1", (JsonCodec.decodeFromString<Item>(JsonCodec.encodeToString(item)).clientMsgId).zhAny())
    }
}
