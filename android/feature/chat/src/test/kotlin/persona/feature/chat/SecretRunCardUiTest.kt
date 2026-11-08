package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.InteractionField
import persona.core.contracts.Resolution
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * The assistant's password card (`secret_run`, #1137) in the chat: the computer, the purpose and every command exactly as
 * it will run, "Fill this in on your iPhone", and only Decline: no Allow, no Always allow, no hand back, nothing to type. Robolectric
 * native captures of every style in light and dark go to `build/secret-run-shots/` (not emulator screenshots).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h800dp-xhdpi", application = QuestionTestApplication::class)
class SecretRunCardUiTest {
    @get:Rule val rule = createComposeRule()

    private val commands = listOf(
        "sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/com.example.backup.plist",
        "sudo -S -k -p '' sh -c \"echo  two  spaces > /tmp/x\"",
    )
    private val card = Interaction("ix_secret", machineId = "hub", sessionId = "chat", sessionTitle = "Persona", kind = "permission",
        toolName = "secret_run", summary = "Mac mini\nInstall the launch daemon for the backup job\n$ ${commands.first()}",
        toolInput = buildJsonObject {
            put("machine", "macmini"); put("machine_name", "Mac mini"); put("purpose", "Install the launch daemon for the backup job")
            put("commands", JsonArray(commands.map { JsonPrimitive(it) })); put("delivery", "stdin")
        },
        fields = listOf(InteractionField(key = "password", label = "Password", secret = true)))

    @Test fun showsWhatWouldRunAndOnlyDeclines() {
        val sent = mutableListOf<Resolution>()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                InteractionPanel(listOf(card), null, 800.dp, null, emptySet(), { _, resolution -> sent += resolution }, null, {})
            }
        }
        rule.onAllNodesWithText("用你的密码在 Mac mini 上运行").onFirst().assertExists()
        rule.onAllNodesWithText("Install the launch daemon for the backup job").onFirst().assertExists()
        commands.forEach { rule.onNodeWithText(it, useUnmergedTree = true).assertExists() }
        rule.onAllNodesWithText("请在 iPhone 上填写").onFirst().assertExists()
        // Neither "允许" nor "总是允许…", no deny reason and no password field.
        rule.onAllNodes(hasText("允许", substring = true), useUnmergedTree = true).assertCountEquals(0)
        rule.onAllNodes(hasSetTextAction()).assertCountEquals(0)
        // Nothing to hand back to a computer, no terminal to explain: the card is the assistant's own.
        rule.onAllNodesWithText("交给电脑处理").assertCountEquals(0)
        rule.onAllNodesWithText("终端处理说明").assertCountEquals(0)
        rule.onNodeWithText("拒绝").assertIsEnabled().performClick()
        assertEquals(listOf(Resolution(allow = false)), sent)
    }

    @Test fun drawsInEveryStyleLightAndDark() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Column(Modifier.width(400.dp).background(PersonaTheme.palette.background).testTag("secret-run-shot")) {
                    InteractionPanel(listOf(card), null, 800.dp, null, emptySet(), { _, _ -> }, null, {})
                }
            }
        }
        val output = File("build/secret-run-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) {
            val pixels = mutableListOf<IntArray>()
            for (d in listOf(false, true)) {
                style = s; dark = d
                Snapshot.sendApplyNotifications(); rule.waitForIdle()
                rule.onNodeWithText("拒绝").assertExists()
                val bitmap = rule.onNodeWithTag("secret-run-shot").captureToImage().asAndroidBitmap()
                File(output, "${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                    assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
                }
                pixels += IntArray(bitmap.width * bitmap.height).also { bitmap.getPixels(it, 0, bitmap.width, 0, 0, bitmap.width, bitmap.height) }
            }
            assertFalse("${s.name}: light and dark must differ", pixels[0].contentEquals(pixels[1]))
        }
    }
}
