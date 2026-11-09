package persona.core.navigation

import androidx.navigation3.runtime.NavKey
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable @SerialName("persona.core.navigation.AssistantRoute") data object AssistantRoute : NavKey
@Serializable @SerialName("persona.core.navigation.AgentsRoute") data object AgentsRoute : NavKey
@Serializable @SerialName("persona.core.navigation.InboxRoute") data object InboxRoute : NavKey
@Serializable @SerialName("persona.core.navigation.SettingsRoute") data object SettingsRoute : NavKey
@Serializable @SerialName("persona.core.navigation.ConnectionSettingsRoute") data object ConnectionSettingsRoute : NavKey
@Serializable enum class ConversationKind { Assistant, Agent }

@Serializable
@SerialName("persona.core.navigation.RouteSource")
sealed interface RouteSource {
    @Serializable @SerialName("persona.core.navigation.RouteSource.InboxItem") data class InboxItem(val itemId: String) : RouteSource
    @Serializable @SerialName("persona.core.navigation.RouteSource.LarkConversation") data class LarkConversation(val chatId: String) : RouteSource
    @Serializable @SerialName("persona.core.navigation.RouteSource.Interaction") data class Interaction(val machineId: String, val requestId: String) : RouteSource
}

@Serializable @SerialName("persona.core.navigation.ChatRoute") data class ChatRoute(
    val machineId: String,
    val sessionId: String,
    val kind: ConversationKind? = null,
    val source: RouteSource? = null,
) : NavKey

@Serializable @SerialName("persona.core.navigation.InboxItemRoute") data class InboxItemRoute(val itemId: String) : NavKey

/**
 * An inbox item drawn by the extension of the connector it comes from (its `inbox_item` UI, docs/extensions/extensions.md "UI slots"):
 * [extensionId]'s [uiId] for item [itemId]; [title] is the item's until the extension's view has one. When the extension cannot
 * draw it, the item's own page opens instead.
 */
@Serializable @SerialName("persona.core.navigation.InboxItemUiRoute")
data class InboxItemUiRoute(val itemId: String, val extensionId: String, val uiId: String, val title: String = "") : NavKey
@Serializable @SerialName("persona.core.navigation.LarkChatRoute") data class LarkChatRoute(val chatId: String) : NavKey
@Serializable @SerialName("persona.core.navigation.InteractionRoute") data class InteractionRoute(val chat: ChatRoute, val requestId: String) : NavKey
@Serializable @SerialName("persona.core.navigation.PairingRoute") data object PairingRoute : NavKey
@Serializable @SerialName("persona.core.navigation.TodosRoute") data object TodosRoute : NavKey
/** The to-do list with "New to-do" open: the launcher's "New to-do" shortcut ([NewTodoLink], iOS AddTodoIntent). */
@Serializable @SerialName("persona.core.navigation.NewTodoRoute") data object NewTodoRoute : NavKey
@Serializable @SerialName("persona.core.navigation.TodoDetailRoute") data class TodoDetailRoute(val todoId: String) : NavKey
@Serializable enum class SettingsPage { Diagnostics, Readiness, Notifications, PhoneData, Appearance, Devices, About, Pairing, ClaudeCode, ModelConfig, Hubs, Persona, Extensions, Mail, Feishu, Channels, HubUpdate, Translation, Triage, Capsule, Language, Codex, Providers, Storage, Desktops, Managers, FeishuBot, Sent, Connectors }
@Serializable @SerialName("persona.core.navigation.SettingsPageRoute") data class SettingsPageRoute(val page: SettingsPage) : NavKey

enum class AppTab { Persona, Agents, Inbox, Settings }

interface Navigator {
    fun navigate(key: NavKey)
    fun back()
    fun selectTab(tab: AppTab)
}
