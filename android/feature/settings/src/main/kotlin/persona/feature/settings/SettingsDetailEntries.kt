package persona.feature.settings

import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.ChannelRoute
import persona.core.navigation.ConnectorRoute
import persona.core.navigation.EngineRoute
import persona.core.navigation.ModelPurposeRoute
import persona.core.navigation.Navigator
import persona.core.navigation.LauncherRoute
import persona.core.navigation.ExtensionCatalogRoute
import persona.core.navigation.ExtensionMarketRoute
import persona.core.navigation.ExtensionMarketsRoute
import persona.core.navigation.ExtensionRoute
import persona.core.navigation.ExtensionUiRoute

/**
 * Settings pages opened by key rather than from the settings list: a machine's engine (from its page in Agents) and
 * one AI use of "Model configuration" (from that list, or from the voice page for its extension's model), one extension of "Extensions" and the
 * pages it draws, "Extension market" with its extensions and markets, a launcher of a machine (from the machine's page), and one channel
 * of "Channels".
 */
fun EntryProviderScope<NavKey>.settingsDetailEntries(navigator: Navigator, services: SettingsServices) {
    val demo = services.demo
    entry<EngineRoute> { route ->
        EngineSelectionScreen(route.machineId, services.sessions, services.machines.takeUnless { demo }, pairing = false,
            onDone = navigator::back, onBack = navigator::back)
    }
    entry<ModelPurposeRoute> { route ->
        ModelPurposePage(services.models.takeUnless { demo }, route.purposeId, onBack = navigator::back,
            providers = services.providers.takeUnless { demo }, manager = route.manager)
    }
    entry<ExtensionRoute> { route ->
        val manager = remember(route) { ExtensionOwners.of(route.manager) }
        ExtensionPage(services.extensions.takeUnless { demo }, route.extensionId, onBack = navigator::back, startSignIn = route.signIn,
            manager = manager,
            onPage = { page -> navigator.navigate(ExtensionUiRoute(page.extension.ifBlank { route.extensionId }, page.id, page.title.ifBlank { page.id }, manager = manager)) },
            onChannel = { navigator.navigate(ChannelRoute(it)) }, machines = services.sessions.machines,
            onUpgrade = { market, id -> navigator.navigate(ExtensionCatalogRoute(market, id, manager = manager)) },
            connectors = services.connectors.takeUnless { demo }, onConnector = { navigator.navigate(ConnectorRoute(it, manager)) })
    }
    entry<ConnectorRoute> { route ->
        HubConnectorPage(services.connectors.takeUnless { demo }, route.connectorId, onBack = navigator::back, manager = route.manager)
    }
    entry<ExtensionUiRoute> { route ->
        ExtensionUiPage(services.extensions.takeUnless { demo }, route.extensionId, route.uiId, route.title, onBack = navigator::back,
            initialState = route.state, depth = route.depth,
            shown = LocalExtensionPageVisible.current && (persona.core.navigation.LocalDetailKey.current == null || persona.core.navigation.LocalDetailKey.current == route),
            onPush = { navigator.navigate(it.copy(manager = it.manager ?: route.manager)) }, manager = route.manager)
    }
    entry<ExtensionMarketRoute> {
        ExtensionMarketPage(services.extensions.takeUnless { demo }, onBack = navigator::back,
            onExtension = { navigator.navigate(ExtensionCatalogRoute(it.market, it.id)) }, onMarkets = { navigator.navigate(ExtensionMarketsRoute) })
    }
    entry<ExtensionCatalogRoute> { route ->
        val manager = remember(route) { ExtensionOwners.of(route.manager) }
        ExtensionCatalogPage(services.extensions.takeUnless { demo }, route.marketId, route.extensionId, onBack = navigator::back,
            manager = manager,
            onInstalled = { navigator.navigate(ExtensionRoute(it, manager = manager)) },
            onSignIn = { navigator.navigate(ExtensionRoute(it, signIn = true, manager = manager)) })
    }
    entry<ExtensionMarketsRoute> { ExtensionMarketsPage(services.extensions.takeUnless { demo }, onBack = navigator::back) }
    entry<persona.core.navigation.ManagerRoute> { route ->
        val machines by services.sessions.machines.collectAsState()
        ManagerPage(services.managers.takeUnless { demo }, route.managerId, onBack = navigator::back,
            devices = services.bound().deviceSettings.takeUnless { demo }, machines = machines)
    }
    entry<ChannelRoute> { route ->
        ChannelPage(services.channels.takeUnless { demo }, route.channelId, onBack = navigator::back, onOpen = navigator::navigate)
    }
    entry<persona.core.navigation.ModelProviderRoute> { route ->
        val machines by services.sessions.machines.collectAsState()
        ProviderDetailPage(services.providers.takeUnless { demo }, route.name, machines, onBack = navigator::back)
    }
    entry<LauncherRoute> { route ->
        // Only the machines' names (for "Quota"'s footer): their live metrics change all the time and would redraw the page.
        val names by remember(services.sessions) {
            services.sessions.machines.map { list -> list.associate { it.id to it.name } }.distinctUntilChanged()
        }.collectAsState(emptyMap())
        val machineName = remember(names) { { id: String -> names[id]?.takeIf { it.isNotBlank() } ?: id } }
        LauncherPage(services.machines.takeUnless { demo }, services.extensions.takeUnless { demo }, route.machineId, route.name, onBack = navigator::back,
            machineName = machineName, onOpen = navigator::navigate)
    }
}
