package persona.core.navigation

import org.junit.Assert.*
import org.junit.Test

class HubLinksTest {
    private val hub = "http://hub.example.com:18660"
    private val hubs = listOf(hub, "https://[2001:db8::1]:18661", "192.0.2.10:18660")

    private fun page(url: String, on: List<String> = hubs) = HubLinks.page(url, on)

    @Test fun opensTheLinksTheAssistantWrites() {
        assertEquals(HubPage.Agent("m1", "6f1c3a2e-0000-4000-8000-000000000001"),
            page("$hub/s/m1/6f1c3a2e-0000-4000-8000-000000000001"))
        assertEquals(HubPage.Screen(TodoDetailRoute("todo_1")), page("$hub/todos?id=todo_1"))
        assertEquals(HubPage.Conversation("abc-1"), page("$hub/persona/abc-1"))
    }

    @Test fun ignoresTheQueryAndFragment() {
        assertEquals(HubPage.Agent("m1", "abc-1"), page("$hub/s/m1/abc-1?view=chat#item-42"))
        assertEquals(HubPage.Conversation("abc-1"), page("$hub/persona/abc-1#at"))
        assertEquals(HubPage.Screen(TodoDetailRoute("todo 1")), page("$hub/todos?view=x&id=todo%201"))
    }

    @Test fun aMessageOfAConversation() {
        // Claude's item ids hold `#`: the hub query-escapes them (#2531)
        assertEquals(HubPage.Agent("m1", "abc-1", "a1b2#3"), page("$hub/s/m1/abc-1?at=a1b2%233"))
        assertEquals(HubPage.Agent("m1", "abc-1", "item-42"), page("$hub/s/m1/abc-1?view=chat&at=item-42#x"))
        assertEquals(HubPage.Agent("m1", "abc-1", "item-42"), page("$hub/t/m1/space/slot/abc-1?at=item-42"))
        assertEquals(HubPage.Conversation("abc-1", "item 7"), page("$hub/persona/abc-1?at=item+7"))
        assertEquals(HubPage.Conversation("abc-1", "item-7"), page("$hub/persona/abc-1?at=item-7"))
        // an empty anchor is no anchor; Persona's own page has no message to land on
        assertEquals(HubPage.Agent("m1", "abc-1"), page("$hub/s/m1/abc-1?at="))
        assertEquals(HubPage.Conversation(null), page("$hub/persona?at=item-7"))
        assertEquals(HubPage.Screen(TodoDetailRoute("todo_1")), page("$hub/todos?id=todo_1&at=item-7"))
    }

    @Test fun decodesPathSegments() {
        assertEquals(HubPage.Agent("mac book", "a+b"), page("$hub/s/mac%20book/a+b"))
        assertNull(page("$hub/s/m1/%E0%A4%A"))
    }

    @Test fun knowsTheOtherPages() {
        assertEquals(HubPage.Agent("m1", "abc-1"), page("$hub/t/m1/space/slot/abc-1"))
        assertEquals(HubPage.Screen(AgentsRoute), page("$hub/t/m1/space/slot"))
        assertEquals(HubPage.Conversation(null), page(hub))
        assertEquals(HubPage.Conversation(null), page("$hub/"))
        assertEquals(HubPage.Conversation(null), page("$hub/persona"))
        assertEquals(HubPage.Conversation(null), page("$hub/persona/new"))
        assertEquals(HubPage.Screen(TodoDetailRoute("todo_1")), page("$hub/todos/todo_1"))
        assertEquals(HubPage.Screen(TodosRoute), page("$hub/todos"))
        assertEquals(HubPage.Screen(InboxRoute), page("$hub/inbox"))
        assertEquals(HubPage.Screen(InboxItemRoute("mail:1")), page("$hub/inbox/mail:1"))
        assertEquals(HubPage.Screen(AgentsRoute), page("$hub/agents"))
        assertEquals(HubPage.Screen(SettingsRoute), page("$hub/settings"))
        assertEquals(HubPage.Screen(MachinesRoute), page("$hub/machines"))
        assertEquals(HubPage.Screen(MachineDetailRoute("m1")), page("$hub/machines/m1"))
    }

    @Test fun legacyAndCanonicalSentLinksOpenTheNestedLedger() {
        val sent = HubPage.Screen(SettingsPageRoute(SettingsPage.Sent))
        listOf("/settings?tab=sent", "/settings/sent", "/settings/sent/", "/settings?tab=connectors&connector=-sent").forEach {
            assertEquals(it, sent, page("$hub$it"))
        }
        assertEquals(HubPage.Screen(SettingsPageRoute(SettingsPage.Connectors)), page("$hub/settings?tab=connectors"))
        assertNull(page("$hub/settings/sent/more"))
        assertNull(page("https://other.example.com/settings/sent"))
    }

    @Test fun leavesPagesWithoutAScreenToTheWeb() {
        listOf("/s/m1", "/s/m1/abc-1/more", "/kb/people/x", "/terminal", "/admin", "/api/v1/sessions", "/assets/app.js",
            "/persona/abc/more", "/todos/a/b", "/extension/x/y").forEach { assertNull(it, page("$hub$it")) }
    }

    @Test fun onlyOnTheHubsOwnAddresses() {
        assertNull(page("http://other.example.com:18660/s/m1/abc-1"))
        assertNull(page("http://hub.example.com/s/m1/abc-1"))
        assertNull(page("http://hub.example.com:18661/s/m1/abc-1"))
        assertNull(page("$hub/s/m1/abc-1", emptyList()))
        assertNull(page("http://user@hub.example.com:18660/s/m1/abc-1"))
        assertNull(page("persona://session/m1/abc-1"))
        assertNull(page("not a url"))
        // the host's case, IPv6 literals, an address given without a scheme, and the default ports
        assertEquals(HubPage.Agent("m1", "a"), page("http://HUB.example.com:18660/s/m1/a"))
        assertEquals(HubPage.Agent("m1", "a"), page("https://[2001:db8::1]:18661/s/m1/a"))
        assertEquals(HubPage.Agent("m1", "a"), page("http://192.0.2.10:18660/s/m1/a"))
        assertEquals(HubPage.Agent("m1", "a"), page("https://hub.example.com/s/m1/a", listOf("https://hub.example.com")))
        assertEquals(HubPage.Agent("m1", "a"), page("https://hub.example.com:443/s/m1/a", listOf("https://hub.example.com/")))
    }

    @Test fun aHubUnderASubPathServesItsPagesThere() {
        val proxied = listOf("https://proxy.example.com/persona/")
        assertEquals(HubPage.Agent("m1", "a"), page("https://proxy.example.com/persona/s/m1/a", proxied))
        assertEquals(HubPage.Conversation(null), page("https://proxy.example.com/persona", proxied))
        assertNull(page("https://proxy.example.com/s/m1/a", proxied))
        assertNull(page("https://proxy.example.com/personas/s/m1/a", proxied))
    }
}
