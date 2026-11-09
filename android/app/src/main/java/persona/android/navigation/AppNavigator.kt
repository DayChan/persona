package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.navigation.*
import persona.core.contracts.SessionKey
import persona.core.hub.errors.ErrorBreadcrumbs

internal class AppNavigator(
    private val stacks: Map<AppTab, MutableList<NavKey>>,
    private val selectedTab: () -> AppTab,
    private val onSelectTab: (AppTab) -> Unit,
    private val chatTab: (ChatRoute) -> AppTab? = { null },
    /** Returns true when the Persona home shows this conversation itself; the flag says whether to switch to it. */
    private val openAssistant: (ChatRoute, Boolean) -> Boolean = { _, _ -> false },
) : Navigator {
    /**
     * Pages [navigate] pushed onto another tab's stack, and the tab each was opened from: back from such a page returns
     * to that tab (a task card in the Persona home opens its agent's chat under Agents; back goes to the home, not to
     * the Agents list). The user picking a tab themselves drops these.
     */
    private val openedFrom = mutableMapOf<NavKey, AppTab>()

    init { stacks.values.forEach(::restoreConnectorParents) }

    override fun selectTab(tab: AppTab) {
        if (tab != selectedTab()) openedFrom.clear()
        onSelectTab(tab)
        ErrorBreadcrumbs.shared.navigation("tab:${tab.name}")
    }

    override fun navigate(key: NavKey) {
        val from = selectedTab()
        // The route's type only, never its arguments (machine, session, item ids).
        ErrorBreadcrumbs.shared.navigation(key::class.simpleName ?: "route")
        val shown = stacks.mapValues { it.value.lastOrNull() }
        open(key)
        val to = selectedTab()
        val page = stacks.getValue(to).lastOrNull() ?: return
        // Not recorded: the tab's root, and a page that already topped the target tab's stack but is not the one asked
        // for (this call did not open it).
        if (to != from && page != tabRoot(to) && (page != shown[to] || page == key)) openedFrom[page] = from
    }

    private fun open(key: NavKey) {
        val chat = when (key) { is ChatRoute -> key; is InteractionRoute -> key.chat; else -> null }
        if (chat != null && openAssistant(chat, true)) return
        if (chat != null && chat.kind != null) {
            Router().parse(RouteInput.Internal(key))?.let { onSelectTab(it.restore(stacks)); return }
        }
        if (key is SettingsPageRoute || key == PairingRoute) {
            val route = Router().parse(RouteInput.Internal(key))!!
            onSelectTab(route.restore(stacks)); return
        }
        val rootTab = when (key) {
            AssistantRoute -> AppTab.Persona
            AgentsRoute -> AppTab.Agents
            InboxRoute -> AppTab.Inbox
            SettingsRoute -> AppTab.Settings
            else -> null
        }
        if (rootTab != null) {
            onSelectTab(rootTab)
            return
        }
        if (key == ConnectionSettingsRoute) {
            onSelectTab(AppTab.Settings)
            return
        }
        val target = if (key is ChatRoute) chatTab(key)
            ?: stacks.entries.firstOrNull { key in it.value }?.key
            ?: selectedTab().takeIf { it == AppTab.Persona }
            ?: AppTab.Agents else selectedTab()
        onSelectTab(target)
        if (key is ChatRoute) stacks.filterKeys { it != target }.values.forEach { stack -> stack.removeAll { it == key } }
        val stack = stacks.getValue(target)
        if (stack.last() != key) stack.add(key)
    }

    /** Repair restored entries once session metadata becomes available. */
    fun restoreChatTabs() {
        val visible = stacks.getValue(selectedTab()).lastOrNull()
        stacks.forEach { (tab, stack) ->
            stack.filterIsInstance<ChatRoute>().forEach chatEntry@ { chat ->
                val owner = chatTab(chat) ?: return@chatEntry
                if (owner == AppTab.Persona && openAssistant(chat, visible == chat)) {
                    stack.removeAll { it == chat }
                    return@chatEntry
                }
                if (owner != tab) {
                    stack.removeAll { it == chat }
                    val destination = stacks.getValue(owner)
                    if (chat !in destination) {
                        if (owner == selectedTab() && visible != chat) destination.add(1, chat)
                        else destination.add(chat)
                    }
                    if (visible == chat) onSelectTab(owner)
                }
            }
        }
    }

    override fun back() {
        val stack = stacks.getValue(selectedTab())
        if (stack.size <= 1) return
        val page = stack.removeAt(stack.lastIndex)
        openedFrom.remove(page)?.let(onSelectTab)
    }

    /** Remove every destination owned by the confirmed closed session, including hidden tabs. */
    fun sessionClosed(session: SessionKey) {
        stacks.values.forEach { stack ->
            stack.removeAll { key ->
                when (key) {
                    is ChatRoute -> key.machineId == session.machineId && key.sessionId == session.sessionId
                    is InteractionRoute -> key.chat.machineId == session.machineId && key.chat.sessionId == session.sessionId
                    is TerminalRoute -> key.machineId == session.machineId && key.sessionId == session.sessionId
                    else -> false
                }
            }
        }
        openedFrom.keys.retainAll { page -> stacks.values.any { page in it } }
    }

    /**
     * The chat's hub group went away: its pages become [moved] (the same session under its new id) where they stand,
     * else they go. Visited tabs stay composed, so this can come from a hidden tab: the tab on screen stays as it is.
     */
    fun groupGone(chat: ChatRoute, moved: ChatRoute?) {
        fun shows(key: NavKey) = key == chat || key is InteractionRoute && key.chat == chat
        stacks.values.forEach { stack ->
            val at = stack.indexOfFirst { shows(it) }.takeIf { it >= 0 } ?: return@forEach
            val from = stack.firstNotNullOfOrNull { page -> openedFrom[page]?.takeIf { shows(page) } }
            stack.removeAll { shows(it) }
            if (moved != null && moved !in stack) {
                stack.add(at, moved)
                from?.let { openedFrom[moved] = it }
            }
        }
        openedFrom.keys.retainAll { page -> stacks.values.any { page in it } }
    }
}
