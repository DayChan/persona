package persona.feature.settings

import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.ServerEvent
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.ConnectorRoute
import java.io.File

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ConnectorOwnershipShotsTest {
    @get:Rule val rule = createComposeRule()
    private val roster = ManagerRoster(listOf(
        HubManager("assistant", "Personal", online = true, isDefault = true),
        HubManager("work", "Work", online = true),
    ))
    private val signedOut = HubConnector("github", "GitHub", status = "not_signed_in", manager = "assistant",
        capabilities = listOf("login"), enabled = true)
    private class Online : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = false
    }
    private class Api(var rows: List<HubConnector>) : ConnectorsApiStub() {
        val calls = mutableListOf<Pair<String, String?>>()
        private suspend fun note(action: String) { calls += action to currentCoroutineContext()[RequestManager]?.id }
        override suspend fun mailAccounts() = emptyList<MailAccount>()
        override suspend fun connectors(all: Boolean): List<HubConnector> { note("list"); return rows }
        override suspend fun connectorLogin(id: String, step: String, values: Map<String, String>): BindPrompt {
            note("login:$id")
            return BindPrompt(step = "token", fields = listOf(BindField("token", "Token")))
        }
        override suspend fun setConnectorEnabled(id: String, enabled: Boolean): HubConnector {
            note("enable:$id")
            val owner = currentCoroutineContext()[RequestManager]?.id
            return rows.first { it.id == id && it.manager == owner }.copy(enabled = enabled)
        }
        override suspend fun connectorLogout(id: String) { note("logout:$id") }
    }

    @Test fun zeroSingleAndMultipleAccountsKeepTheirIdentities() {
        assertEquals(listOf(ConnectorRow.Phone, ConnectorRow.Mail), connectorRows(emptyList(), false))
        assertEquals("GitHub", connectorTitle(signedOut, listOf(signedOut), roster))
        val other = signedOut.copy(manager = "work")
        val rows = listOf(signedOut, other)
        assertEquals(2, connectorRows(rows, false).filterIsInstance<ConnectorRow.Hub>().size)
        assertEquals(listOf("GitHub · Personal", "GitHub · Work"), rows.map { connectorTitle(it, rows, roster) })
        val accounts = listOf(signedOut.copy(status = "ok", account = "personal-example"),
            other.copy(status = "ok", account = "work-example"),
            signedOut.copy(id = "enterprise", account = "enterprise-example"))
        assertEquals(accounts, connectorRows(accounts, false).filterIsInstance<ConnectorRow.Hub>().map { it.connector })
        assertEquals("GitHub · work", connectorTitle(other, rows, null))
        assertEquals("GitHub · Personal", connectorTitle(signedOut.copy(manager = null), rows, roster))
        val sameNames = roster.copy(managers = roster.managers.map { it.copy(name = "Shared") })
        assertEquals(listOf("GitHub · Shared (assistant)", "GitHub · Shared (work)"),
            rows.map { connectorTitle(it, rows, sameNames) })
    }

    @Test fun settingsDistinguishesOwnersAndClicksKeepTheOwner() {
        val api = Api(listOf(signedOut, signedOut.copy(manager = "work")))
        val context = ApplicationProvider.getApplicationContext<Context>()
        val services = demoSettingsServices(context.resources).copy(demo = false, connection = Online(), connectors = api)
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        val opened = mutableListOf<ConnectorRoute>()
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides roster) {
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                        SettingsScreen(services, initialPage = "connectors", onNavigate = { opened += it as ConnectorRoute })
                    }
                }
            }
        }
        rule.waitUntil { api.calls.isNotEmpty() }
        rule.onNode(hasScrollAction()).performScrollToNode(hasText("GitHub · Work"))
        rule.onNodeWithText("GitHub · Personal").assertExists().performClick()
        rule.onNodeWithText("GitHub · Work").assertExists().performClick()
        assertEquals(listOf(ConnectorRoute("github", "assistant"), ConnectorRoute("github", "work")), opened)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("connectors-owners-${s.key}-${if (d) "dark" else "light"}")
        }
    }

    @Test fun detailLoginAndSettingsStayWithTheClickedOwnerWhenOwnersChange() {
        val api = Api(listOf(signedOut.copy(status = "ok", account = "personal-example"), signedOut.copy(manager = "work")))
        var owner by mutableStateOf("work")
        rule.setContent { PersonaTheme(reduceMotion = true) {
            CompositionLocalProvider(LocalManagerRoster provides roster) {
                HubConnectorPage(api, "github", {}, manager = owner)
            }
        } }
        rule.waitForIdle()
        rule.onNodeWithText("personal-example").assertDoesNotExist()
        val res = ApplicationProvider.getApplicationContext<Context>().resources
        rule.onNodeWithText(res.getString(R.string.settings_src_connector_sign_in, "GitHub")).performClick()
        rule.waitForIdle()
        assertTrue(api.calls.contains("login:github" to "work"))
        rule.onNode(hasSetTextAction()).performTextInput("example-token")
        rule.onNodeWithText(res.getString(R.string.settings_src_next)).performClick()
        rule.waitForIdle()
        assertEquals(2, api.calls.count { it == "login:github" to "work" })
        rule.onNode(isToggleable()).performClick()
        rule.waitForIdle()
        assertTrue(api.calls.contains("enable:github" to "work"))
        owner = "assistant"
        rule.waitForIdle()
        rule.onAllNodesWithText("personal-example").assertCountEquals(2)
        rule.onNodeWithText(res.getString(R.string.settings_src_connector_sign_out)).performClick()
        rule.onAllNodesWithText(res.getString(R.string.settings_src_connector_sign_out)).onLast().performClick()
        rule.waitForIdle()
        assertTrue(api.calls.contains("logout:github" to "assistant"))
        assertEquals(listOf("work", "assistant"), api.calls.filter { it.first == "list" }.map { it.second }.distinct())
    }

    private fun capture(name: String) {
        rule.waitForIdle()
        val dir = File("build/connector-ownership-shots").also { it.mkdirs() }
        File(dir, "$name.png").outputStream().use {
            assertTrue(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }
}
