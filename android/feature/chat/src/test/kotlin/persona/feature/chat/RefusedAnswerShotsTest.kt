package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import persona.core.designsystem.*
import java.io.File

/**
 * A card the hub refused to take (#1881), as the chat draws it: before, it stayed "Result unconfirmed" with its buttons off
 * until the chat was reopened; now the hub's reason shows under it and it can be answered again. Every style, light and
 * dark; PNGs in `build/refused-answer-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi", application = QuestionTestApplication::class)
class RefusedAnswerShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/refused-answer-shots").also { it.mkdirs() }

    private val card = Interaction("ix_codex", machineId = "box", sessionId = "s1", kind = "permission", toolName = "Bash",
        toolInput = buildJsonObject { put("command", JsonPrimitive("npm test")) })
    private val reason = "The terminal has a draft in its input box: clear it on the computer, then answer again."

    @Test fun beforeAndAfter() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fixed by mutableStateOf(false)
        val sent = mutableListOf<Resolution>()
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val res = androidx.compose.ui.platform.LocalResources.current
                // what ChatScreen keeps after the answer: before #1881 every hub error put the card in `uncertain`
                val reply = if (fixed) InteractionReply(InteractionSubmission.Refused, reason = reason) else InteractionReply(InteractionSubmission.Unknown)
                val uncertain = if (reply.submission == InteractionSubmission.Unknown) setOf(card.id) else emptySet()
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(vertical = Tokens.Space.m)) {
                    InteractionPanel(listOf(card), null, 600.dp, null, uncertain, { _, r -> sent += r }, null, {})
                    // ChatScreen's line under the panel
                    Text(reply.notice(res, card), Modifier.padding(horizontal = Tokens.Space.m), style = PersonaTheme.type.caption,
                        color = PersonaTheme.palette.ink2)
                }
            }
        }
        for (state in listOf(false, true)) {
            fixed = state
            rule.waitForIdle()
            val allow = rule.onNode(hasText("Allow") and hasClickAction())
            if (state) {
                rule.onNodeWithText("The hub didn't take this answer: $reason").assertExists()
                rule.onNodeWithText("Result unconfirmed").assertDoesNotExist()
                allow.assertIsEnabled()
            } else {
                rule.onNodeWithText("Result unconfirmed").assertExists()
                allow.assertIsNotEnabled()
            }
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                style = s; dark = d
                rule.waitForIdle()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                File(out(), "${if (state) "after" else "before"}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                    .use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
            }
        }
        // the card answers again once the hub refused it
        rule.onNode(hasText("Allow") and hasClickAction()).performClick()
        assertEquals(1, sent.size)
    }
}
