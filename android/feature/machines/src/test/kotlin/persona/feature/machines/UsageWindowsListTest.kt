package persona.feature.machines

import androidx.activity.ComponentActivity
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
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

    /** The source line names the computers that read the account, not "this computer" (#1907); an older hub's `local` reads "Computer". */
    @Config(sdk = [35])
    @Test fun theComputersAreNamed() {
        val now = Instant.ofEpochSecond(1_793_600_000L)
        val accounts = listOf(
            UsageAccount("a", "codex", "Codex A", sampledAt = now.minusSeconds(30).toString(), local = true, machines = listOf("MacBook", "Studio"),
                sources = listOf("Relay"), windows = listOf(UsageWindow("7d", 10080, 20.0))),
            UsageAccount("b", "codex", "Codex B", sampledAt = now.minusSeconds(30).toString(), local = true, windows = listOf(UsageWindow("7d", 10080, 10.0))),
        )
        rule.setContent { PersonaTheme(reduceMotion = true) { LazyColumn { usageWindows(accounts, now) } } }
        rule.onNodeWithText("MacBook · Studio · Relay").assertExists()
        rule.onNodeWithText(rule.activity.getString(R.string.machines_usage_source_local)).assertExists()
    }
}
