package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.navigation.*
import persona.feature.inbox.LarkMomentRoute
import persona.feature.inbox.LarkRoute
import persona.feature.inbox.MailRoute
import persona.platform.phonetools.PhoneToolsRoute
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** The list beside a detail pane marks the row of the page the detail shows, whichever way the page was opened. */
class ShellPanesTest {
    private val stacks = mapOf<AppTab, MutableList<NavKey>>(
        AppTab.Persona to mutableListOf(AssistantRoute), AppTab.Agents to mutableListOf(AgentsRoute),
    )
    private var selected = AppTab.Persona
    private val navigator = AppNavigator(stacks, { selected }, { selected = it })
    private fun marked(tab: AppTab) = detailOf(stacks.getValue(tab))

    @Test fun deepLinkedSettingsPagesAreMarkedInTheList() {
        for (page in SettingsPage.entries) {
            stacks.getValue(AppTab.Persona).retainAll { it == AssistantRoute }
            val link = "persona://open?target=settings&page=" + page.name.lowercase()
            selected = Router().parse(RouteInput.DeepLink(link))!!.open(stacks)
            assertEquals(AppTab.Persona, selected)
            // The settings list compares its rows with exactly this key.
            assertEquals(link, SettingsPageRoute(page), marked(AppTab.Persona))
        }
    }

    @Test fun theMarkFollowsTapsBackAndOtherTabs() {
        assertNull("nothing open beside the list", marked(AppTab.Persona))
        navigator.navigate(SettingsPageRoute(SettingsPage.About))
        assertEquals(SettingsPageRoute(SettingsPage.About), marked(AppTab.Persona))
        navigator.navigate(SettingsPageRoute(SettingsPage.Devices))
        assertEquals(SettingsPageRoute(SettingsPage.Devices), marked(AppTab.Persona))
        navigator.back()
        assertEquals(SettingsPageRoute(SettingsPage.About), marked(AppTab.Persona))
        navigator.back()
        assertNull(marked(AppTab.Persona))

        // A notification opens a conversation in Agents; the originating settings page keeps its own mark.
        navigator.navigate(SettingsPageRoute(SettingsPage.Notifications))
        val chat = ChatRoute("machine", "session", ConversationKind.Agent)
        navigator.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        assertEquals(chat, marked(AppTab.Agents))
        assertEquals(SettingsPageRoute(SettingsPage.Notifications), marked(AppTab.Persona))
    }

    @Test fun pagesOpenedUnderAnotherKeyMarkTheirSettingsRow() {
        // debug.openPairing, pairing links and debug.openPage phone push these keys, not SettingsPageRoute.
        assertEquals(SettingsPageRoute(SettingsPage.Pairing), detailOf(listOf(SettingsRoute, PairingRoute)))
        assertEquals(SettingsPageRoute(SettingsPage.PhoneData), detailOf(listOf(SettingsRoute, PhoneToolsRoute)))
        selected = Router().parse(RouteInput.Internal(ConnectionSettingsRoute))!!.open(stacks)
        assertEquals(SettingsPageRoute(SettingsPage.Diagnostics), marked(AppTab.Persona))
    }

    @Test fun onlyDetailsAreMarked() {
        val chat = ChatRoute("machine", "session")
        listOf(
            chat, InteractionRoute(chat, "request"), TerminalRoute("machine", "session"), MachinesRoute, MachineDetailRoute("machine"),
            LarkRoute("chat", "item", 1), LarkMomentRoute("post-1", "item"), MailRoute("item"), InboxItemRoute("item"), LarkChatRoute("chat"),
            InboxItemUiRoute("item", "larkext", "event"),
        ).forEach { key -> assertEquals(key.toString(), key, detailOf(listOf(AgentsRoute, key))) }
        // Lists and pages that never sit beside a list mark nothing.
        listOf(AssistantRoute, AgentsRoute, InboxRoute, SettingsRoute).forEach { assertNull(detailOf(listOf(it))) }
        assertNull(detailOf(emptyList()))
        assertNull(detailOf(listOf(AssistantRoute, ConnectionSettingsRoute)))
    }

    @Test fun settingsPagesTheListMarksOpenBesideIt() {
        // Settings › Assistant and a provider from Settings › Providers: the list's rows compare with these keys.
        listOf(AssistantSettingsRoute, ModelProviderRoute("example")).forEach { key ->
            assertEquals(key.toString(), key, detailOf(listOf(SettingsRoute, key)))
        }
    }

    @Test fun aSpacePageOpensBesideTheListAndChatsGoOnTopOfIt() {
        val space = SpaceRoute("machine/space")
        selected = AppTab.Agents
        navigator.navigate(space)
        assertEquals(listOf(AgentsRoute, space), stacks.getValue(AppTab.Agents))
        assertEquals(space, marked(AppTab.Agents))
        navigator.navigate(ChatRoute("machine", "session", ConversationKind.Agent))
        assertEquals(ChatRoute("machine", "session", ConversationKind.Agent), marked(AppTab.Agents))
        navigator.back()
        assertEquals("back returns to the space page", space, marked(AppTab.Agents))
    }
}
