package persona.feature.chat

import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.Item
import persona.core.contracts.MessageSource
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.ChatRow

class SourceLabelsTest {
    private val source = MessageSource("conversation", "cv_1", "Review")
    private fun row(id: String, origin: MessageSource? = source, role: String = "user") =
        ChatLine.Row(ChatRow(if (role == "user") "user" else "reply", Item(id, role = role, kind = "text", text = "Report", source = origin).asLineItem()))

    @Test fun adjacentSourcesHaveOneLabelButAReplyOrUserRestartsTheRun() {
        assertEquals(setOf("first", "after-reply", "after-user", "other-id"), sourceLabelIds(listOf(
            row("first"), row("second"), row("reply", role = "assistant"), row("after-reply"),
            row("user", origin = null), row("after-user"), row("other-id", source.copy(conversationId = "cv_2")),
        )))
    }

    @Test fun pagedWindowLabelsItsFirstSourceAndTitleChangesDoNotSplitIdentity() {
        assertEquals(setOf("second"), sourceLabelIds(listOf(row("second"), row("third", source.copy(title = "Renamed")))))
    }
}
