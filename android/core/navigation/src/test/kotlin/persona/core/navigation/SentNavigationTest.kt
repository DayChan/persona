package persona.core.navigation

import androidx.navigation3.runtime.NavKey
import kotlinx.serialization.json.Json
import org.junit.Assert.*
import org.junit.Test

class SentNavigationTest {
    private val parent = SettingsPageRoute(SettingsPage.Connectors)
    private val sent = SettingsPageRoute(SettingsPage.Sent)
    private fun stacks() = AppTab.entries.associateWith { mutableListOf<NavKey>(tabRoot(it)) }

    @Test fun oldAndNewAppLinksRestoreTheConnectorParentWithoutDuplicates() {
        val stacks = stacks()
        for (page in listOf("sent", "Sent", "connectors/sent")) {
            val route = Router().parse(RouteInput.DeepLink("persona://open?target=settings&page=$page"))!!
            assertEquals(sent, route.key)
            assertEquals(AppTab.Settings, route.restore(stacks))
            assertEquals(listOf(SettingsRoute, parent, sent), stacks.getValue(AppTab.Settings))
        }
        stacks.getValue(AppTab.Settings).removeAt(2)
        assertEquals(parent, stacks.getValue(AppTab.Settings).last())
        Router().parse(RouteInput.Internal(sent))!!.restore(stacks)
        assertEquals(listOf(SettingsRoute, parent, sent), stacks.getValue(AppTab.Settings))
    }

    @Test fun legacySerializedSentKeyKeepsItsIdentityAndGetsAParent() {
        val restored = Json.decodeFromString(SettingsPageRoute.serializer(), """{"page":"Sent"}""")
        val stack = mutableListOf<NavKey>(SettingsRoute, restored)
        restoreConnectorParents(stack)
        restoreConnectorParents(stack)
        assertEquals(listOf(SettingsRoute, parent, sent), stack)
        assertSame(restored, stack.last())
    }

    @Test fun unrelatedDestinationsKeepTheirExistingBackStacks() {
        for (key in listOf(SettingsPageRoute(SettingsPage.Mail), SettingsPageRoute(SettingsPage.Feishu),
            SettingsPageRoute(SettingsPage.PhoneData), SettingsPageRoute(SettingsPage.FeishuBot), ConnectorRoute("github", "work"),
            SettingsPageRoute(SettingsPage.Notifications))) {
            val stack = mutableListOf<NavKey>(SettingsRoute, key)
            val before = stack.toList()
            restoreConnectorParents(stack)
            assertEquals(before, stack)
        }
    }
}
