package persona.core.navigation

import androidx.navigation3.runtime.NavKey
import java.net.URI
import java.net.URLDecoder

/**
 * A page of the hub's web app (web/src/lib/router.ts) that the app shows itself: the links the assistant writes into
 * its answers (`/s/<machine>/<session>`, `/persona/<conversation>`, `/todos?id=<todo>`, server/internal/assistant
 * feishu_cards.go sessionLink / todoLink) and the other pages a user may paste. Machine ids are the hub's own; the
 * shell adds the group suffix ([persona.core.contracts.GroupKeys]) of the hub the link was on.
 */
sealed interface HubPage {
    /**
     * `/s/<machine>/<session>`, or `/t/<machine>/<space>/<slot>/<session>`: that agent's conversation; [itemId] the
     * message `?at=<item id>` names (#2194), to land on.
     */
    data class Agent(val machineId: String, val sessionId: String, val itemId: String? = null) : HubPage
    /**
     * `/persona/<conversation>`: one of the assistant's conversations, at the message `?at=` names ([itemId]); `/`, `/persona` and
     * `/persona/new` are Persona's page (null, no message).
     */
    data class Conversation(val sessionId: String?, val itemId: String? = null) : HubPage
    /** A page with no ids to resolve: a todo, the todo list, an inbox item, a tab, the machines. */
    data class Screen(val key: NavKey) : HubPage
}

object HubLinks {
    /**
     * The page [url] opens when it is on one of [hubs] (the hub's addresses: its pairing address, alternatives and
     * the ones it advertises, `public_url` first); null for other sites and the hub's pages the app has no screen for,
     * which stay web pages. A hub behind a reverse proxy's sub-path serves its pages under that path. A conversation's
     * `?at=<item id>` is the message to land on; the rest of the query (`?view=`) and the fragment never stop a page
     * from opening.
     */
    fun page(url: String, hubs: Collection<String>): HubPage? {
        val link = uri(url.trim()) ?: return null
        val scheme = link.scheme?.lowercase()
        if ((scheme != "http" && scheme != "https") || link.host.isNullOrEmpty() || link.rawUserInfo != null) return null
        val path = link.rawPath.orEmpty()
        val prefix = hubs.asSequence().mapNotNull { base(it) }
            .filter { it.host == link.host.lowercase().trim('[', ']') && it.port == port(link) }
            .map { it.path }.filter { path == it || path.startsWith("$it/") }
            .maxByOrNull { it.length } ?: return null
        val parts = try {
            path.removePrefix(prefix).split('/').filter { it.isNotEmpty() }.map { URLDecoder.decode(it.replace("+", "%2B"), "UTF-8") }
        } catch (_: IllegalArgumentException) { return null }
        if (parts.any { it.isBlank() }) return null
        return route(parts, query(link.rawQuery))
    }

    private fun route(p: List<String>, query: Map<String, String>): HubPage? = when (persona.core.contracts.ProtocolCompatibility.canonicalAssistantPath(p.firstOrNull())) {
        null -> HubPage.Conversation(null)
        "s" -> if (p.size == 3) HubPage.Agent(p[1], p[2], at(query)) else null
        "t" -> when (p.size) {
            5 -> HubPage.Agent(p[1], p[4], at(query))
            4 -> HubPage.Screen(AgentsRoute)
            else -> null
        }
        "persona" -> when {
            p.size == 1 || p.size == 2 && p[1] == "new" -> HubPage.Conversation(null)
            p.size == 2 -> HubPage.Conversation(p[1], at(query))
            else -> null
        }
        "todos" -> when {
            p.size == 2 -> HubPage.Screen(TodoDetailRoute(p[1]))
            p.size == 1 -> query["id"]?.takeIf { it.isNotBlank() }?.let { HubPage.Screen(TodoDetailRoute(it)) } ?: HubPage.Screen(TodosRoute)
            else -> null
        }
        "inbox" -> when (p.size) {
            1 -> HubPage.Screen(InboxRoute)
            2 -> HubPage.Screen(InboxItemRoute(p[1]))
            else -> null
        }
        "agents" -> if (p.size == 1) HubPage.Screen(AgentsRoute) else null
        "settings" -> when {
            p == listOf("settings", "sent") || p.size == 1 && (query["tab"] == "sent" ||
                query["tab"] == "connectors" && query["connector"] == "-sent") -> HubPage.Screen(SettingsPageRoute(SettingsPage.Sent))
            p.size == 1 && query["tab"] == "connectors" -> HubPage.Screen(SettingsPageRoute(SettingsPage.Connectors))
            p.size == 1 -> HubPage.Screen(SettingsRoute)
            else -> null
        }
        "machines" -> when (p.size) {
            1 -> HubPage.Screen(MachinesRoute)
            2 -> HubPage.Screen(MachineDetailRoute(p[1]))
            else -> null
        }
        else -> null
    }

    /** The message a conversation's link names (`?at=<item id>`; Claude's ids hold `#`, written `%23`). */
    private fun at(query: Map<String, String>): String? = query["at"]?.takeIf { it.isNotBlank() }

    private data class Base(val host: String, val port: Int, val path: String)

    /** A hub address as host, port and path prefix; an address without a scheme (`host:port`) is http. */
    private fun base(address: String): Base? {
        val value = address.trim().let { if ("://" in it) it else "http://$it" }
        val u = uri(value) ?: return null
        val host = u.host?.lowercase()?.trim('[', ']')?.takeIf { it.isNotEmpty() } ?: return null
        if (u.scheme?.lowercase() !in setOf("http", "https")) return null
        return Base(host, port(u), u.rawPath.orEmpty().trimEnd('/'))
    }

    private fun port(u: URI): Int = if (u.port >= 0) u.port else if (u.scheme.equals("https", ignoreCase = true)) 443 else 80

    private fun uri(value: String): URI? = try { URI(value) } catch (_: java.net.URISyntaxException) { null }

    private fun query(raw: String?): Map<String, String> = try {
        raw.orEmpty().split('&').filter { it.isNotEmpty() }.associate { part ->
            decode(part.substringBefore('=')) to decode(part.substringAfter('=', ""))
        }
    } catch (_: IllegalArgumentException) { emptyMap() }

    private fun decode(value: String): String = URLDecoder.decode(value, "UTF-8")
}
