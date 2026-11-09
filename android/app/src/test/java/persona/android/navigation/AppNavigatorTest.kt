package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.navigation.*
import org.junit.Assert.assertEquals
import org.junit.Test

class AppNavigatorTest {
    @Test fun legacyLedgerNavigationAndRestoredStacksReturnToConnectors() {
        selected = AppTab.Settings
        navigator.navigate(SettingsPageRoute(SettingsPage.Sent))
        assertEquals(listOf(SettingsRoute, SettingsPageRoute(SettingsPage.Connectors), SettingsPageRoute(SettingsPage.Sent)),
            stacks.getValue(AppTab.Settings))
        navigator.back()
        assertEquals(SettingsPageRoute(SettingsPage.Connectors), stacks.getValue(AppTab.Settings).last())
        navigator.back()
        assertEquals(listOf(SettingsRoute), stacks.getValue(AppTab.Settings))

        stacks.getValue(AppTab.Settings).add(SettingsPageRoute(SettingsPage.Sent))
        val restored = AppNavigator(stacks, { selected }, { selected = it })
        restored.back()
        assertEquals(SettingsPageRoute(SettingsPage.Connectors), stacks.getValue(AppTab.Settings).last())
    }

    @Test fun confirmedCloseRemovesAllMatchingDestinationsAcrossTabs() {
        val target = persona.core.contracts.SessionKey("machine", "closed")
        val chat = ChatRoute(target.machineId, target.sessionId)
        val other = ChatRoute("other-machine", target.sessionId)
        stacks.getValue(AppTab.Agents).addAll(listOf(chat, TerminalRoute(target.machineId, target.sessionId)))
        stacks.getValue(AppTab.Persona).add(chat.copy(kind = ConversationKind.Assistant))
        stacks.getValue(AppTab.Inbox).addAll(listOf(InteractionRoute(chat, "request"), other))
        navigator.sessionClosed(target)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(InboxRoute, other), stacks.getValue(AppTab.Inbox))
        navigator.sessionClosed(target)
        navigator.back()
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
    }

    private val stacks = mapOf<AppTab, MutableList<NavKey>>(
        AppTab.Persona to mutableListOf(AssistantRoute),
        AppTab.Agents to mutableListOf(AgentsRoute),
        AppTab.Inbox to mutableListOf(InboxRoute),
        AppTab.Settings to mutableListOf(SettingsRoute),
    )
    private var selected = AppTab.Agents
    private val navigator = AppNavigator(stacks, { selected }, { selected = it })

    @Test fun aChatWhoseGroupWentAwayLeavesItsOwnTabNotTheOneOnScreen() {
        // Disconnecting the chat's group from Settings › Hubs (#5714): the chat under Agents still renders there.
        val chat = ChatRoute("mac~g0a1b2c3d", "s")
        val hubs = SettingsPageRoute(SettingsPage.Hubs)
        stacks.getValue(AppTab.Agents).add(chat)
        stacks.getValue(AppTab.Settings).add(hubs)
        selected = AppTab.Settings
        navigator.groupGone(chat, null)
        assertEquals(AppTab.Settings, selected)
        assertEquals(listOf(SettingsRoute, hubs), stacks.getValue(AppTab.Settings))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        // Merged into another group: the page becomes the session's new id where it stands.
        val moved = ChatRoute("mac", "s")
        stacks.getValue(AppTab.Agents).add(InteractionRoute(chat, "request"))
        navigator.groupGone(chat, moved)
        assertEquals(AppTab.Settings, selected)
        assertEquals(listOf(SettingsRoute, hubs), stacks.getValue(AppTab.Settings))
        assertEquals(listOf(AgentsRoute, moved), stacks.getValue(AppTab.Agents))

        // On screen, opened from the Persona home: back from its new id still returns there.
        val router = agentsOwnAgentChats()
        stacks.getValue(AppTab.Agents).remove(moved)
        selected = AppTab.Persona
        router.navigate(chat)
        router.groupGone(chat, moved)
        assertEquals(listOf(AgentsRoute, moved), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
    }

    @Test fun switchingTabsKeepsEachStackAndBackNeverRemovesTheRoot() {
        val chat = ChatRoute("machine", "session")
        navigator.navigate(chat)
        navigator.selectTab(AppTab.Inbox)
        navigator.back()
        assertEquals(listOf(InboxRoute), stacks.getValue(AppTab.Inbox))
        navigator.selectTab(AppTab.Agents)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        navigator.back()
        navigator.back()
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
    }

    @Test fun notificationOpensAgentsFromAnotherTabAndDoesNotDuplicateTheChat() {
        navigator.selectTab(AppTab.Settings)
        val chat = ChatRoute("machine", "session")
        navigator.navigate(chat)
        navigator.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        navigator.navigate(InboxRoute)
        assertEquals(AppTab.Inbox, selected)
        assertEquals(listOf(SettingsRoute), stacks.getValue(AppTab.Settings))
    }

    @Test fun assistantChatsStayInPersonaAndLateMetadataRepairsRestoredChats() {
        var assistantKnown = false
        val assistantChat = ChatRoute("machine", "assistant")
        val router = AppNavigator(stacks, { selected }, { selected = it }, chatTab = {
            if (assistantKnown && it == assistantChat) AppTab.Persona else null
        })
        router.selectTab(AppTab.Persona)
        router.navigate(assistantChat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        router.back()

        // Simulate an old restored stack before repositories have loaded metadata.
        stacks.getValue(AppTab.Agents).add(assistantChat)
        router.selectTab(AppTab.Agents)
        assistantKnown = true
        router.restoreChatTabs()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, assistantChat), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        router.selectTab(AppTab.Settings)
        router.navigate(assistantChat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, assistantChat), stacks.getValue(AppTab.Persona))
    }

    @Test fun restoredStackKeepsOwnershipAndSettingsGearOpensTheSettingsTab() {
        val chat = ChatRoute("machine", "assistant")
        stacks.getValue(AppTab.Persona).add(chat)
        navigator.navigate(chat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, chat), stacks.getValue(AppTab.Persona))
        navigator.selectTab(AppTab.Agents)
        navigator.navigate(ConnectionSettingsRoute)
        assertEquals(AppTab.Settings, selected)
        assertEquals(listOf(SettingsRoute), stacks.getValue(AppTab.Settings))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
    }

    @Test fun embeddedAssistantConversationsStayInThePersonaHome() {
        val assistantChat = ChatRoute("machine", "assistant")
        val opened = mutableListOf<Pair<ChatRoute, Boolean>>()
        val router = AppNavigator(stacks, { selected }, { selected = it }, chatTab = {
            if (it.sessionId == "assistant") AppTab.Persona else AppTab.Agents
        }, openAssistant = { chat, select ->
            (chat.sessionId == "assistant").also { if (it) { opened += chat to select; if (select) selected = AppTab.Persona } }
        })
        router.navigate(assistantChat)
        assertEquals(listOf(assistantChat to true), opened)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))

        // A page restored before metadata arrived becomes a home request, switching only if it was on screen.
        stacks.getValue(AppTab.Agents).add(assistantChat)
        router.selectTab(AppTab.Inbox)
        router.restoreChatTabs()
        assertEquals(assistantChat to false, opened.last())
        assertEquals(AppTab.Inbox, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        val agentChat = ChatRoute("machine", "agent")
        router.navigate(agentChat)
        assertEquals(listOf(AgentsRoute, agentChat), stacks.getValue(AppTab.Agents))
    }

    private fun agentsOwnAgentChats() = AppNavigator(stacks, { selected }, { selected = it }, chatTab = { AppTab.Agents })

    @Test fun backFromAnAgentChatOpenedInThePersonaHomeReturnsToTheHome() {
        // A task card in the main conversation opens its agent's chat (#1946): it lands under Agents.
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        val chat = ChatRoute("machine", "agent")
        router.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))

        // Already on top under Agents: back still returns to the page it was opened from.
        stacks.getValue(AppTab.Agents).add(chat)
        router.navigate(chat)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)

        // A chat with its kind known goes through the router, and returns the same way.
        val known = ChatRoute("machine", "known", ConversationKind.Agent)
        router.navigate(known)
        assertEquals(AppTab.Agents, selected)
        router.back()
        assertEquals(AppTab.Persona, selected)
    }

    @Test fun pagesPushedAboveACrossTabChatGoBackToItFirst() {
        val router = agentsOwnAgentChats()
        selected = AppTab.Inbox
        val chat = ChatRoute("machine", "agent")
        val terminal = TerminalRoute("machine", "agent")
        router.navigate(chat)
        router.navigate(terminal)
        assertEquals(listOf(AgentsRoute, chat, terminal), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Inbox, selected)
    }

    @Test fun pickingATabForgetsWhereACrossTabPageCameFrom() {
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        val chat = ChatRoute("machine", "agent")
        router.navigate(chat)
        router.selectTab(AppTab.Inbox)
        router.selectTab(AppTab.Agents)
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        // Back within the tab a page was opened in stays in that tab.
        selected = AppTab.Agents
        router.navigate(chat)
        router.back()
        assertEquals(AppTab.Agents, selected)
    }
}
