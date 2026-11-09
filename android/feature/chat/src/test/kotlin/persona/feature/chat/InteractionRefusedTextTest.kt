package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction

/** What the chat says under a card the hub refused to take (#1881), next to the unconfirmed one it used to say. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InteractionRefusedTextTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    private val card = Interaction("request", kind = "permission")

    @Test fun chinese() {
        assertEquals("hub 没有接受这个回答：终端输入框里有草稿",
            InteractionReply(InteractionSubmission.Refused, reason = "终端输入框里有草稿").notice(res, card))
        assertEquals("hub 没有接受这个回答，可以再答一次", InteractionReply(InteractionSubmission.Refused).notice(res, card))
        assertEquals("还不知道你的回答送到没有，先看一下这个请求", InteractionReply(InteractionSubmission.Unknown).notice(res, card))
    }

    @Config(qualifiers = "en")
    @Test fun english() {
        assertEquals("The hub didn't take this answer: The terminal has a draft in its input box",
            InteractionReply(InteractionSubmission.Refused, reason = "The terminal has a draft in its input box").notice(res, card))
        assertEquals("The hub didn't take this answer. You can answer again.", InteractionReply(InteractionSubmission.Refused).notice(res, card))
    }

    @Config(qualifiers = "ja")
    @Test fun japanese() {
        assertEquals("ハブはこの回答を受け付けませんでした。もう一度回答できます", InteractionReply(InteractionSubmission.Refused).notice(res, card))
    }
}
