package persona.feature.agents

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.InteractionField
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.answer.AnswerAction
import java.time.Instant

/** The assistant's password card (`secret_run`, #1137) in the Agents tab: the computer and the commands, "Fill this in on
 *  your iPhone" instead of "allow it on the web or on iPhone", and only Decline. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class SecretRunRequestCardTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.parse("2026-11-06T08:00:00Z")
    private val commands = listOf("sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/com.example.backup.plist", "sudo -S -k -p '' true")
    private val card = Interaction("ix_secret", machineId = "hub", sessionId = "chat", sessionTitle = "Persona", kind = "permission",
        toolName = "secret_run", expiresAt = now.plusSeconds(600).toString(),
        toolInput = buildJsonObject {
            put("machine", "macmini"); put("machine_name", "Mac mini"); put("purpose", "Install the launch daemon for the backup job")
            put("commands", JsonArray(commands.map { JsonPrimitive(it) })); put("delivery", "stdin")
        },
        fields = listOf(InteractionField(key = "password", label = "Password", secret = true)))

    @Test fun showsTheCommandsAndOnlyDeclines() {
        val answers = mutableListOf<AnswerAction>()
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                RequestCard(card, "hub", AgentsMetadata(), now, online = true, delivery = null, onOpen = {},
                    onAnswer = { _, action, _, _ -> answers += action })
            }
        }
        rule.onAllNodesWithText("用你的密码在 Mac mini 上运行").onFirst().assertExists()
        rule.onAllNodesWithText("Install the launch daemon for the backup job").onFirst().assertExists()
        commands.forEach { rule.onNodeWithText(it, useUnmergedTree = true).assertExists() }
        rule.onAllNodesWithText("请在 iPhone 上填写").onFirst().assertExists()
        // No "允许" or "总是允许…", and not the generic fields note ("…才能允许，请到网页或 iPhone 上处理…").
        rule.onAllNodes(hasText("允许", substring = true), useUnmergedTree = true).assertCountEquals(0)
        rule.onNodeWithText("拒绝").assertIsEnabled().performClick()
        assertEquals(listOf(AnswerAction.Deny), answers)
    }
}
