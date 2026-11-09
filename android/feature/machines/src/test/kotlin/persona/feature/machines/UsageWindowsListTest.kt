package persona.feature.machines

import androidx.activity.ComponentActivity
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.UsageAccount
import persona.core.contracts.api.UsageWindow
import persona.core.designsystem.PersonaTheme
import java.time.Instant

/** Subscription quota when the hub gives two accounts the same id (#5304): both are drawn, the page does not crash. */
@RunWith(AndroidJUnit4::class)
class UsageWindowsListTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    @Config(sdk = [35])
    @Test fun accountsSharingAnIdAreAllDrawn() {
        val now = Instant.ofEpochSecond(1_793_600_000L)
        // Two Codex accounts without resets_at: the hub's id is sha256(provider|minutes|0) for both.
        val accounts = listOf("Codex A", "Codex B").map {
            UsageAccount("same", "codex", it, sampledAt = now.minusSeconds(30).toString(), windows = listOf(UsageWindow("7d", 10080, 20.0)))
        }
        rule.setContent { PersonaTheme(reduceMotion = true) { LazyColumn { usageWindows(accounts, now) } } }
        rule.onNodeWithText("Codex A").assertExists()
        rule.onNodeWithText("Codex B").assertExists()
    }

    /** A gateway that turned the key away says where to fix it, not that it can't be reached (#1902). */
    @Config(sdk = [35])
    @Test fun aRejectedKeyIsNotCalledUnreachable() {
        val now = Instant.ofEpochSecond(1_793_600_000L)
        val accounts = listOf("rejected" to "Seat A", "failed" to "Seat B").map { (problem, name) ->
            UsageAccount(name, "codex", name, sampledAt = now.minusSeconds(30).toString(), stale = true, sources = listOf("Relay"), problem = problem,
                windows = listOf(UsageWindow("7d", 10080, 20.0)))
        }
        rule.setContent { PersonaTheme(reduceMotion = true) { LazyColumn { usageWindows(accounts, now) } } }
        val words = { id: Int -> rule.activity.getString(id) }
        rule.onNodeWithText(words(R.string.machines_usage_problem_rejected)).assertExists()
        rule.onNodeWithText(words(R.string.machines_usage_problem_failed)).assertExists()
        rule.onNodeWithText(words(R.string.machines_usage_problem_unreachable)).assertDoesNotExist()
        assertNull(problemText("something-newer")) // a reason this build doesn't know: no words, the card still says "out of date"
    }
}
