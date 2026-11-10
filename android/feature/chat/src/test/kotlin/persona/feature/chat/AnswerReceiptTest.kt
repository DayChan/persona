package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.DialogOption
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.Resolution
import persona.core.contracts.ScreenDialog

/** Right after an answer reaches the hub, the notice says what was answered, not only that something was (#4740). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class AnswerReceiptTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    private val bash = Interaction("b", kind = "permission", toolName = "Bash",
        toolInput = buildJsonObject { put("command", JsonPrimitive("./gradlew test")) })
    private val rm = Interaction("r", kind = "permission", summary = "rm -rf build")
    private val plan = Interaction("p", kind = "plan", plan = "## Use Kotlin\n1. Port it")
    private val question = Interaction("q", kind = "question", questions = listOf(Question("Which language?")))
    private val dialog = Interaction("s", kind = "screen", dialog = ScreenDialog(options = listOf(DialogOption("1", "Yes"), DialogOption("2", "No"))))
    private val asked = Interaction("t", kind = "screen", dialog = ScreenDialog(question = "Do you want to make this edit to **Main.kt**?",
        options = listOf(DialogOption("1", "Yes"), DialogOption("2", "No"))))

    private fun sent(interaction: Interaction, resolution: Resolution) =
        InteractionReply(InteractionSubmission.Answered).notice(res, interaction, resolution)

    @Config(qualifiers = "zh")
    @Test fun eachKindSaysWhatWasAnswered() {
        assertEquals("已允许：./gradlew test", sent(bash, Resolution(allow = true)))
        assertEquals("已总是允许：./gradlew test", sent(bash, Resolution(allow = true, always = true)))
        assertEquals("已拒绝：rm -rf build", sent(rm, Resolution(allow = false)))
        assertEquals("已批准计划（自动接受编辑）：Use Kotlin", sent(plan, Resolution(approve = true, mode = "acceptEdits")))
        assertEquals("已打回计划：shorter", sent(plan, Resolution(approve = false, feedback = "shorter")))
        assertEquals("已回答：用 Kotlin", sent(question, Resolution(answers = mapOf("Which language?" to "用 Kotlin"))))
        assertEquals("已回答：No", sent(dialog, Resolution(option = "2")))
    }

    // #5121: a plan says which plan (its first line, cut short) and how it was approved; a dialog says what it asked.
    @Config(qualifiers = "zh")
    @Test fun planAndDialogSayWhatTheyWereAbout() {
        assertEquals("已批准计划：Use Kotlin", sent(plan, Resolution(approve = true)))
        assertEquals("已批准计划（全部放行）", sent(Interaction("p0", kind = "plan"), Resolution(approve = true, mode = "bypassPermissions")))
        assertEquals("已批准计划", sent(Interaction("p0", kind = "plan", plan = "\n  \n"), Resolution(approve = true, mode = "plan")))
        val long = "x".repeat(80)
        assertEquals("已批准计划：" + "x".repeat(60) + "…", sent(Interaction("p1", kind = "plan", plan = "# $long\nmore"), Resolution(approve = true)))
        assertEquals("已批准计划：Port it", sent(Interaction("p2", kind = "plan", summary = "Port it"), Resolution(approve = true)))
        assertEquals("已回答「Do you want to make this edit to Main.kt?」：Yes", sent(asked, Resolution(option = "1")))
        assertEquals("已回传你的选择", sent(asked, Resolution(keys = listOf("escape"))))
    }

    @Config(qualifiers = "zh")
    @Test fun nothingOfItsOwnKeepsTheOldNotice() {
        // Leave to computer says so, not that a choice was made (#2821).
        assertEquals("交回电脑处理", sent(bash, Resolution(dismiss = true)))
        assertEquals("已回传你的选择", sent(dialog, Resolution(keys = listOf("escape"))))
        assertEquals("已回传你的选择", sent(question, Resolution(answers = emptyMap())))
        assertEquals("已回传你的选择", sent(Interaction("e", kind = "elicitation"), Resolution(action = "accept")))
        assertEquals("已回传你的选择", InteractionReply(InteractionSubmission.Answered).notice(res, bash))
        assertNull(AnswerReceipt(bash, Resolution()).text(res))
    }

    @Config(qualifiers = "en")
    @Test fun englishAndJapaneseReadInTheirOwnWords() {
        assertEquals("Allowed: ./gradlew test", sent(bash, Resolution(allow = true)))
        assertEquals("Plan approved: Use Kotlin", sent(plan, Resolution(approve = true)))
        assertEquals("Plan approved, auto-accept edits: Use Kotlin", sent(plan, Resolution(approve = true, mode = "acceptEdits")))
        assertEquals("Answered “Do you want to make this edit to Main.kt?”: No", sent(asked, Resolution(option = "2")))
        assertEquals("Handed back to the computer", sent(bash, Resolution(dismiss = true)))
    }

    @Config(qualifiers = "ja")
    @Test fun japanese() {
        assertEquals("許可しました：./gradlew test", sent(bash, Resolution(allow = true)))
        assertEquals("回答しました：Yes", sent(dialog, Resolution(option = "1")))
        assertEquals("計画を承認しました（編集を自動承認）：Use Kotlin", sent(plan, Resolution(approve = true, mode = "acceptEdits")))
        assertEquals("「Do you want to make this edit to Main.kt?」に回答しました：Yes", sent(asked, Resolution(option = "1")))
    }
}
