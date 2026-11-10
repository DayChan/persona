package persona.android.di

import android.app.Application
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import org.robolectric.Shadows
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Machine
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.ChatRoute
import persona.feature.settings.SettingsScreen
import persona.feature.settings.SettingsServices

/**
 * Settings › Sent as you on a hub group (docs/android/android.md "Multiple hubs"): the hub speaks its own machine ids, the chat a row
 * opens is found by the merged lists', so the rows come back in the selected group's form and the page opens the chat by it.
 * The last step of choosing the group (`MachineManagementModule.sent`: which group is selected now) is the runtime's.
 */
@RunWith(AndroidJUnit4::class)
// The plain Application keeps Hilt's PersonaApp out of the test.
@Config(application = Application::class, sdk = [35], qualifiers = "zh-w400dp-h2000dp-xhdpi")
class SelectedGroupSentTest {
    @get:Rule val rule = createEmptyComposeRule()

    private val rows = SentPage(listOf(SentItem(1, connector = "github", action = "github_comment", text = "Looks good.", origin = SentOrigin("mac", "s-1"))))

    private fun hub(page: SentPage = rows) = object : HubSent {
        var calls = 0
        override suspend fun page(before: Long?, limit: Int?, connector: String?): SentPage { calls++; return page }
        override suspend fun recall(id: Long) = page.items.firstOrNull { it.id == id }
    }

    @Test fun anotherGroupsRowsCarryItsSuffixAndThePrimaryGroupsAreUnchanged() = runBlocking {
        val inner = hub()
        val other = SelectedGroupSent { SelectedGroupSent.Group(inner, "g0a1b2c3d") }.page()
        assertEquals("mac~g0a1b2c3d", other.items.single().origin.machineId)
        assertEquals("s-1", other.items.single().origin.sessionId)
        val primary = SelectedGroupSent { SelectedGroupSent.Group(inner, null) }.page()
        assertSame(rows, primary)
        // The row a recall answers with is in the group's form too.
        assertEquals("mac~g0a1b2c3d", SelectedGroupSent { SelectedGroupSent.Group(inner, "g0a1b2c3d") }.recall(1)?.origin?.machineId)
    }

    @Test fun eachReadAsksTheSelectedGroupOnceAtItsStart() = runBlocking {
        var asked = 0
        val inner = hub()
        val sent = SelectedGroupSent { asked++; SelectedGroupSent.Group(inner, if (asked == 1) "g0a1b2c3d" else null) }
        // A group chosen while a read is on its way does not change the suffix that read gets.
        assertEquals("mac~g0a1b2c3d", sent.page().items.single().origin.machineId)
        assertEquals("mac", sent.page().items.single().origin.machineId)
        assertEquals(2, asked)
        assertEquals(2, inner.calls)
    }

    /** From the hub to the page and out again: the row's Open chat hands the navigation the group's form of the machine id. */
    private fun openedChat(namespace: String?): NavKey? {
        var navigated: NavKey? = null
        val services = SettingsServices(
            pairing = object : PairingService {
                override val isPaired = MutableStateFlow(true)
                override suspend fun pair(request: PairingRequest) = error("not used")
                override suspend fun unpair() = error("not used")
            },
            connection = object : HubConnection {
                override val state = MutableStateFlow(ConnectionState.Online)
                override val events = MutableSharedFlow<ServerEvent>()
                override fun reconnect() = error("not used")
                override fun send(message: JsonObject) = false
            },
            sessions = object : SessionsRepository {
                override val machines = MutableStateFlow(emptyList<Machine>())
                override val sessions = MutableStateFlow(emptyList<Session>())
                override suspend fun refresh() {}
            },
            updates = object : AppUpdates { override suspend fun check(): AppUpdate? = null },
            sent = SelectedGroupSent { SelectedGroupSent.Group(hub(), namespace) },
        )
        // The app's manifest has no plain activity to host the content: register one for this test.
        val app = ApplicationProvider.getApplicationContext<Application>()
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        val scenario = ActivityScenario.launch(ComponentActivity::class.java)
        scenario.onActivity { activity -> activity.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(2000.dp)) {
                    SettingsScreen(services, initialPage = "sent", onNavigate = { navigated = it })
                }
            }
        } }
        rule.waitForIdle()
        rule.onNode(hasText("打开对话") and hasClickAction()).performClick()
        scenario.close()
        return navigated
    }

    @Test fun aChatOpensByTheSelectedGroupsMachineId() {
        assertEquals(ChatRoute("mac~g0a1b2c3d", "s-1"), openedChat("g0a1b2c3d"))
    }

    @Test fun aChatOfThePrimaryGroupOpensByTheHubsOwnMachineId() {
        assertEquals(ChatRoute("mac", "s-1"), openedChat(null))
    }
}
