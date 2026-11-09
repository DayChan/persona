package persona.core.navigation

import persona.core.contracts.ProtocolCompatibility

import androidx.navigation3.runtime.NavKey
import java.net.URI
import java.net.URLDecoder

sealed interface RouteInput {
    data class DeepLink(val url: String) : RouteInput
    data class Notification(
        val sessionPath: String,
        val kind: ConversationKind? = null,
        val requestId: String? = null,
    ) : RouteInput
    data class Share(val chat: ChatRoute) : RouteInput
    data class Internal(val key: NavKey) : RouteInput
}

/**
 * Pairing credentials are transient input, never a serialized back-stack key. [focusComposer]: the Persona tab opens
 * with its composer focused and the keyboard up ([AskPersonaLink]). [card]: Agents opens at its list with this request
 * card open (a card outside any session, #4808).
 */
class ResolvedRoute(
    val tab: AppTab,
    val key: NavKey,
    val pairingLink: String? = null,
    val focusComposer: Boolean = false,
    val card: RequestRef? = null,
)

/** The launcher's "Ask Persona": the Persona tab with the cursor in its composer, cold start or not. */
const val AskPersonaLink = "persona://open?target=tab&tab=persona&focus=composer"

/** Agents › To-dos with a new to-do being written ([NewTodoRoute]). */
const val NewTodoLink = "persona://open?target=new-todo"

class Router(
    private val conversationKind: (machineId: String, sessionId: String) -> ConversationKind? = { _, _ -> null },
) {
    /** Null means invalid input or missing session metadata; retain pending legacy input until metadata arrives. */
    fun parse(input: RouteInput): ResolvedRoute? = when (input) {
        is RouteInput.DeepLink -> deepLink(input.url)
        is RouteInput.Notification -> sessionPath(input.sessionPath, input.kind)?.let { chat ->
            resolve(input.requestId?.let { InteractionRoute(chat, it) } ?: chat)
        }
        is RouteInput.Share -> resolve(input.chat)
        is RouteInput.Internal -> resolve(input.key)
    }

    private fun resolve(key: NavKey): ResolvedRoute? = when (key) {
        AssistantRoute -> ResolvedRoute(AppTab.Persona, key)
        AgentsRoute -> ResolvedRoute(AppTab.Agents, key)
        InboxRoute -> ResolvedRoute(AppTab.Inbox, key)
        SettingsRoute -> ResolvedRoute(AppTab.Settings, key)
        NewTodoRoute -> ResolvedRoute(AppTab.Agents, key)
        ConnectionSettingsRoute -> resolve(SettingsPageRoute(SettingsPage.Diagnostics))
        PairingRoute, is SettingsPageRoute -> ResolvedRoute(AppTab.Settings, key)
        is ExtensionUiRoute -> key.takeIf { it.extensionId.isNotBlank() && it.uiId.isNotBlank() }?.let { ResolvedRoute(AppTab.Settings, it) }
        is InboxItemRoute -> key.takeIf { it.itemId.isNotBlank() }?.let { ResolvedRoute(AppTab.Inbox, it) }
        is LarkChatRoute -> key.takeIf { it.chatId.isNotBlank() }?.let { ResolvedRoute(AppTab.Inbox, it) }
        is ChatRoute -> normalizedChat(key)?.let { ResolvedRoute(it.kind.tab, it) }
        is InteractionRoute -> normalizedChat(key.chat)?.takeIf { key.requestId.isNotBlank() }?.let {
            ResolvedRoute(it.kind.tab, key.copy(chat = it))
        }
        else -> null
    }

    private fun normalizedChat(chat: ChatRoute): ChatRoute? {
        if (chat.machineId.isBlank() || chat.sessionId.isBlank()) return null
        val kind = chat.kind ?: conversationKind(chat.machineId, chat.sessionId) ?: return null
        val validSource = when (val source = chat.source) {
            null -> true
            is RouteSource.InboxItem -> source.itemId.isNotBlank()
            is RouteSource.LarkConversation -> source.chatId.isNotBlank()
            is RouteSource.Interaction -> source.machineId.isNotBlank() && source.requestId.isNotBlank()
        }
        return chat.copy(kind = kind).takeIf { validSource }
    }

    private fun deepLink(url: String): ResolvedRoute? {
        val uri = try { URI(url) } catch (_: IllegalArgumentException) { return null }
            catch (_: java.net.URISyntaxException) { return null }
        if (uri.scheme !in ProtocolCompatibility.appSchemes || uri.rawAuthority !in setOf("open", "pair") ||
            !uri.rawPath.isNullOrEmpty() || uri.rawFragment != null) return null
        if (uri.host == "pair") {
            // PairingInput remains responsible for validating credentials and transport policy.
            return ResolvedRoute(AppTab.Settings, PairingRoute, url)
        }
        val parameters = linkedMapOf<String, String>()
        try {
            for (part in uri.rawQuery.orEmpty().split('&').filter { it.isNotEmpty() }) {
                val name = decode(part.substringBefore('='))
                val value = decode(part.substringAfter('=', ""))
                if (parameters.put(name, value) != null) return null
            }
        } catch (_: IllegalArgumentException) { return null }
        val kind = when (parameters["kind"]) {
            null -> null
            "assistant" -> ConversationKind.Assistant
            "agent" -> ConversationKind.Agent
            else -> return null
        }
        val key: NavKey = when (parameters["target"] ?: "chat") {
            "chat", "request" -> {
                val chat = ChatRoute(parameters["machine"] ?: return null, parameters["session"] ?: return null, kind)
                // A notification's link without a tab (session metadata not loaded yet) still names its request.
                val request = parameters["request"]
                if (parameters["target"] == "request" || parameters["target"] == null && !request.isNullOrBlank())
                    InteractionRoute(chat, request ?: return null)
                else chat
            }
            "extension" -> ExtensionUiRoute(parameters["extension"] ?: return null, parameters["ui"] ?: return null,
                parameters["title"].orEmpty(), parameters["state"])
            "inbox" -> InboxItemRoute(parameters["item"] ?: return null)
            "new-todo" -> NewTodoRoute
            "lark" -> LarkChatRoute(parameters["chat"] ?: return null)
            "settings" -> SettingsPageRoute(if (parameters["page"] == "connectors/sent") SettingsPage.Sent
                else SettingsPage.entries.firstOrNull {
                    it.name.equals(parameters["page"], ignoreCase = true)
                } ?: return null)
            "tab" -> tabRoot(AppTab.entries.firstOrNull { it.name.equals(ProtocolCompatibility.canonicalTab(parameters["tab"]), ignoreCase = true) } ?: return null)
            else -> return null
        }
        val resolved = resolve(key) ?: return null
        // Only the Persona tab has a composer to focus; elsewhere the parameter means nothing.
        return if (parameters["focus"] == "composer" && resolved.key == AssistantRoute) {
            ResolvedRoute(resolved.tab, resolved.key, resolved.pairingLink, focusComposer = true)
        } else resolved
    }
}

private fun decode(value: String): String = URLDecoder.decode(value.replace("+", "%2B"), "UTF-8")

internal fun sessionPath(path: String, kind: ConversationKind?): ChatRoute? {
    val separator = path.indexOf('/')
    if (separator <= 0 || separator == path.lastIndex) return null
    return ChatRoute(path.substring(0, separator), path.substring(separator + 1), kind)
}

private val ConversationKind?.tab: AppTab
    get() = if (this == ConversationKind.Assistant) AppTab.Persona else AppTab.Agents

fun tabRoot(tab: AppTab): NavKey = when (tab) {
    AppTab.Persona -> AssistantRoute
    AppTab.Agents -> AgentsRoute
    AppTab.Inbox -> InboxRoute
    AppTab.Settings -> SettingsRoute
}

/** Select the returned tab after applying this to the app-owned stacks; "New to-do" also asks [newTodo] for its sheet. */
fun ResolvedRoute.restore(stacks: Map<AppTab, MutableList<NavKey>>, newTodo: NewTodoRequests = NewTodoOpen): AppTab {
    val stack = stacks.getValue(tab)
    if (stack.isEmpty()) stack.add(tabRoot(tab))
    // The card is listed on the tab's root and the composer to focus sits there: pages left open on top are closed first.
    if (card != null || focusComposer) while (stack.size > 1) stack.removeAt(stack.lastIndex)
    if (key == tabRoot(tab)) return tab
    // Move legacy entries out of the wrong tab, then pop to an existing target.
    stacks.filterKeys { it != tab }.values.forEach { other -> other.removeAll { sameTarget(it, key) } }
    val existing = stack.indexOfFirst { sameTarget(it, key) }
    if (existing < 0) stack.add(key)
    else {
        while (stack.lastIndex > existing) stack.removeAt(stack.lastIndex)
        // Preserve the existing NavKey and its UI state when only provenance changed.
        val previous = stack[existing]
        if (previous == ConnectionSettingsRoute ||
            previous is ChatRoute && key is ChatRoute && previous.kind != key.kind ||
            // A notification about one request of a conversation already open: that page shows this request first.
            key is InteractionRoute && previous != key) {
            stack[existing] = key
        }
    }
    // Keep the serialized Sent key compatible, but make its parent explicit for links and history restores.
    restoreConnectorParents(stack)
    // The list kept from an earlier use keeps its state too: its sheet opens on this request, each time.
    if (key == NewTodoRoute) newTodo.request()
    return tab
}

/** Insert the ledger's connector parent without replacing its saved key or affecting other pages. */
fun restoreConnectorParents(stack: MutableList<NavKey>) {
    var index = 0
    val parent = SettingsPageRoute(SettingsPage.Connectors)
    while (index < stack.size) {
        val key = stack[index]
        val child = key == SettingsPageRoute(SettingsPage.Sent)
        if (child && (index == 0 || stack[index - 1] != parent)) {
            stack.add(index, parent)
            index++
        }
        index++
    }
}

private fun sameTarget(left: NavKey, right: NavKey): Boolean = when {
    left is ChatRoute && right is ChatRoute -> left.machineId == right.machineId && left.sessionId == right.sessionId
    // One conversation is one page, whichever of its requests it was opened at.
    left is InteractionRoute -> sameTarget(left.chat, right)
    right is InteractionRoute -> sameTarget(left, right.chat)
    left == ConnectionSettingsRoute -> right == SettingsPageRoute(SettingsPage.Diagnostics)
    else -> left == right
}
