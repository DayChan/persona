package persona.core.domain.answer

import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Resolution
import persona.core.contracts.SecretRunRequest
import persona.core.domain.zh
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** The assistant's password card (`secret_run`, #1137): this app shows what would run and can only decline it. */
class SecretRunCardTest {
    private val commands = listOf(
        "sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/com.example.backup.plist",
        "sudo -S -k -p '' sh -c \"echo  two  spaces > /tmp/x\"",
    )
    private val password = """[{"key":"password","label":"Password","secret":true}]"""
    private fun card(input: String, fields: String = password, kind: String = "permission", tool: String = "secret_run") =
        JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix","machine_id":"hub","session_id":"chat","kind":"$kind","tool_name":"$tool","tool_input":$input,"fields":$fields}""",
        )
    private val input = """{"machine":"macmini","machine_name":"Mac mini","purpose":"Install the launch daemon for the backup job",
        "commands":["sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/com.example.backup.plist",
                    "sudo -S -k -p '' sh -c \"echo  two  spaces > /tmp/x\""],"delivery":"stdin"}"""

    /** Every entry (chat, notifications, lock screen, shortcuts…) builds its answer here: only a bare decline gets out. */
    @Test fun onlyABareDeclineGetsOutFromAnyEntryEvenWithoutItsFields() {
        for (request in listOf(card(input), card(input, fields = "[]"))) {
            for (entry in AnswerEntry.entries) {
                val context = AnswerContext(entry = entry)
                for (action in AnswerAction.entries - AnswerAction.Deny) {
                    assertFalse("${entry.wire} ${action.wire}", AnswerBuilder.build(request, action, context = context).allowed)
                }
                assertFalse(entry.wire, AnswerBuilder.build(request, AnswerAction.Deny, AnswerDraft(message = "typed by mistake"), context).allowed)
            }
            assertEquals(Resolution(allow = false), AnswerBuilder.build(request, AnswerAction.Deny).resolution)
        }
        assertTrue(card(input).needsFieldValues)
    }

    @Test fun readsTheComputerPurposeAndCommandsExactlyAsSent() {
        assertEquals(SecretRunRequest("Mac mini", "Install the launch daemon for the backup job", commands), card(input).secretRun)
        val sparse = card("""{"machine":"macmini","commands":["true", 1, null, "  ", "false"]}""").secretRun
        assertEquals(SecretRunRequest("macmini", "", listOf("true", "false")), sparse)
        // Another tool, or a card of another kind, is no password card.
        assertNull(card(input, tool = "Bash").secretRun)
        assertNull(card(input, kind = "question").secretRun)
        assertFalse(card(input, kind = "question").isSecretRun)
    }

    @Test fun headlineNamesTheComputerInsteadOfTheRawTool() {
        assertEquals("用你的密码在 Mac mini 上运行", card(input).headline.zh())
        assertEquals("用你的密码在 macmini 上运行", card("""{"machine":"macmini"}""").headline.zh())
    }
}
