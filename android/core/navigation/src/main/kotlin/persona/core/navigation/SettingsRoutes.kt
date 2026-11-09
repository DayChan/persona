package persona.core.navigation

import androidx.navigation3.runtime.NavKey
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * One model use of Settings › Model configuration (docs/assistant/model-config.md), by the hub's purpose id: `agent.new`, `task.<id>`,
 * `plugin.<id>` (a voice extension's model, which the voice page also opens).
 */
@Serializable @SerialName("persona.core.navigation.ModelPurposeRoute") data class ModelPurposeRoute(val purposeId: String, val manager: String? = null) : NavKey

/**
 * One extension of Settings › Extensions (docs/extensions/extensions.md "App: Settings › Extensions"), by the hub's extension id; [signIn] opens it with
 * its sign-in started (just installed and not signed in, #4507).
 */
@Serializable @SerialName("persona.core.navigation.PluginRoute") data class ExtensionRoute(
    val extensionId: String, val signIn: Boolean = false,
    /** Whose extension it is (several managers: the same id may be under each, docs/assistant/managers.md); null: the default's. */
    val manager: String? = null,
) : NavKey

/**
 * A page an extension draws (its `extension_page` UI, docs/extensions/extensions.md "Declarative UI"), opened from the extension's page.
 * A page the extension pushed on another (UI version 3) is first drawn with its [state], [depth] pages up.
 */
@Serializable @SerialName("persona.core.navigation.PluginUiRoute")
data class ExtensionUiRoute(
    val extensionId: String, val uiId: String, val title: String, val state: String? = null, val depth: Int = 0,
    /** Whose extension it is, as on [ExtensionRoute]. */
    val manager: String? = null,
) : NavKey

/** Settings › Extensions › Extension market: what the hub's extension markets offer (docs/extensions/extensions.md "Extension market"). */
@Serializable @SerialName("persona.core.navigation.PluginMarketRoute") data object ExtensionMarketRoute : NavKey

/** One extension of a market's catalog: what it may do, and installing, upgrading or removing it. */
@Serializable @SerialName("persona.core.navigation.PluginCatalogRoute")
data class ExtensionCatalogRoute(val marketId: String, val extensionId: String, val manager: String? = null) : NavKey

/** Extension market › Manage markets: the markets the hub reads, adding, removing, refreshing them and their tokens. */
@Serializable @SerialName("persona.core.navigation.PluginMarketsRoute") data object ExtensionMarketsRoute : NavKey

/** One launcher of a machine: what it runs, and the sections extensions draw for its gateway (`launcher_section`). */
@Serializable @SerialName("persona.core.navigation.LauncherRoute")
data class LauncherRoute(val machineId: String, val name: String) : NavKey

/** One channel of Settings › Channels (docs/extensions/channels.md "UI"), by the hub's channel id. */
@Serializable @SerialName("persona.core.navigation.ChannelRoute") data class ChannelRoute(val channelId: String) : NavKey

/** One manager of Settings › Managers (docs/assistant/managers.md), by the hub's manager id. */
@Serializable @SerialName("persona.core.navigation.ManagerRoute") data class ManagerRoute(val managerId: String) : NavKey

/** Settings › AI › Assistant (iOS `SettingsPage.assistant`): the assistant's settings, which the Persona tab also opens in place. */
@Serializable @SerialName("persona.core.navigation.AssistantSettingsRoute") data object AssistantSettingsRoute : NavKey

/** One provider of Settings › AI › Model providers (iOS `ModelLauncherDetailView`), by its launcher name. */
@Serializable @SerialName("persona.core.navigation.ModelProviderRoute") data class ModelProviderRoute(val name: String) : NavKey

/** One connector of Settings › Connectors (its sign-in, switches and inbox settings), by the hub's connector id; an extension's page opens it. */
@Serializable @SerialName("persona.core.navigation.ConnectorRoute") data class ConnectorRoute(val connectorId: String, val manager: String? = null) : NavKey
