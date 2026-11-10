package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.navigation.*
import persona.feature.inbox.InboxEventRoute
import persona.feature.inbox.InboxItemDetailRoute
import persona.feature.inbox.LarkMomentRoute
import persona.feature.inbox.LarkRoute
import persona.feature.inbox.MailRoute
import persona.platform.phonetools.PhoneToolsRoute

/**
 * Destinations that can open beside a list when the window has room for two panes: the Agents list and the secondary
 * Inbox and Settings lists. The shell gives these the list-detail scene's detail role and nothing else does.
 */
internal fun isDetailDestination(key: NavKey): Boolean = when (key) {
    is ChatRoute, is InteractionRoute, is TerminalRoute, is LarkRoute, is LarkMomentRoute, is MailRoute, is InboxItemRoute, is LarkChatRoute,
    is InboxItemUiRoute, is InboxItemDetailRoute, is InboxEventRoute,
    is SettingsPageRoute, PairingRoute, PhoneToolsRoute, MachinesRoute, is MachineDetailRoute, is AddComputerRoute, TerminalPanesRoute,
    is EngineRoute, is ModelPurposeRoute, is ModelProviderRoute, AssistantSettingsRoute, is ExtensionRoute, is ExtensionUiRoute,
    ExtensionMarketRoute, is ExtensionCatalogRoute, ExtensionMarketsRoute, is LauncherRoute, is ChannelRoute, is ManagerRoute, is ConnectorRoute -> true
    // A space's page opens beside the Agents list too; a conversation opened from it goes on top of it.
    is SpaceRoute -> true
    else -> false
}

/** Secondary Settings/Inbox lists never inherit the primary Agents page's collapse policy. */
internal fun canCollapseList(stack: List<NavKey>): Boolean =
    stack.lastOrNull { it == AgentsRoute || it == InboxRoute || it == SettingsRoute } == AgentsRoute

/**
 * The detail a tab's list marks as open beside it (`LocalDetailKey`): the top of the tab's stack when that is a
 * detail, however it got there (a tap in the list, a deep link, a notification, a debug launch, back). Pages that also
 * open under another key come back as the settings page whose row the list shows.
 *
 * Decided from the keys, not from the entries' scene metadata: `ListDetailSceneStrategy.detailPane()` builds a new
 * `DetailMetadata` on every call and it has no equality, so comparing metadata never matched and no list row was
 * ever marked (2026-11-02).
 */
internal fun detailOf(stack: List<NavKey>): NavKey? = when (val top = stack.lastOrNull()) {
    null -> null
    PairingRoute -> SettingsPageRoute(SettingsPage.Pairing)
    PhoneToolsRoute -> SettingsPageRoute(SettingsPage.PhoneData)
    else -> top.takeIf(::isDetailDestination)
}
