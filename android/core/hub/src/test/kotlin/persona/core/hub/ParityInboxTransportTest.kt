package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.MicrosoftFinish

/** The hub calls behind #3045: inbox mutes, a chat's pins and read receipts, Lark message display (anti-recall, edit history), Messages and Outlook sign-in. */
class ParityInboxTransportTest {
    @Test fun inboxAndLarkCallsUseTheHubsRoutes() = runBlocking {
        MockWebServer().use { server ->
            val t = HttpInboxAssistantTransport("scope", server.url("/").toString(), "token", OkHttpClient())
            repeat(6) { server.enqueue(MockResponse().setBody("""{"ok":true}""")) }
            t.inboxMutes()
            t.muteInbox("github:1", "repo")
            t.unmuteInbox("m/1")
            t.larkChatPins("f1", "acct", "c2")
            t.larkChangePin("f1", "m1", "p1", "acct")
            t.larkReadStates("f1", listOf("a", "a", "b"))
            fun next() = server.takeRequest()
            next().let { assertEquals("GET /api/v1/inbox/mutes", "${it.method} ${it.path}") }
            next().let { assertEquals("POST /api/v1/inbox/mutes", "${it.method} ${it.path}"); assertEquals("""{"item_id":"github:1","tag":"repo"}""", it.body.readUtf8()) }
            next().let { assertEquals("DELETE /api/v1/inbox/mutes/m%2F1", "${it.method} ${it.path}") }
            next().let { assertEquals("GET /api/v1/lark/conversations/f1/pins?limit=50&account_id=acct&cursor=c2", "${it.method} ${it.path}") }
            next().let {
                assertEquals("POST /api/v1/lark/conversations/f1/pins", "${it.method} ${it.path}")
                assertEquals("""{"account_id":"acct","message_id":"m1","pin_id":"p1","remove":true}""", it.body.readUtf8())
            }
            next().let {
                assertEquals("POST /api/v1/lark/conversations/f1/read-states", "${it.method} ${it.path}")
                assertEquals("""{"message_ids":["a","b"]}""", it.body.readUtf8())
            }
        }
    }

    @Test fun inboxPoliciesUseTheHubsRoutes() = runBlocking {
        MockWebServer().use { server ->
            val t = HttpInboxAssistantTransport("scope", server.url("/").toString(), "token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"policies":[{"id":"p1"}]}"""))
            server.enqueue(MockResponse().setBody("""{"policy":{"id":"p2"},"applied":2}"""))
            server.enqueue(MockResponse().setBody("""{"policy":{"id":"p/3"},"applied":0}"""))
            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            assertEquals("""{"policies":[{"id":"p1"}]}""", t.inboxPolicies().toString())
            assertEquals("""{"id":"p2"}""", t.setInboxPolicy("github:1", "repo", "noise", "")["policy"].toString())
            t.updateInboxPolicy("p/3", "", "first")
            t.removeInboxPolicy("p/3")
            fun next() = server.takeRequest()
            next().let { assertEquals("GET /api/v1/inbox/policies", "${it.method} ${it.path}") }
            next().let {
                assertEquals("POST /api/v1/inbox/policies", "${it.method} ${it.path}")
                assertEquals("""{"item_id":"github:1","tag":"repo","tier":"noise","twin":""}""", it.body.readUtf8())
            }
            next().let {
                assertEquals("PATCH /api/v1/inbox/policies/p%2F3", "${it.method} ${it.path}")
                assertEquals("""{"tier":"","twin":"first"}""", it.body.readUtf8())
            }
            next().let { assertEquals("DELETE /api/v1/inbox/policies/p%2F3", "${it.method} ${it.path}") }
        }
    }

    // Inbox phase 1: POST /api/v1/inbox/{id}/suggestion with the version shown; 409 suggestion_stale carries the item.
    @Test fun aSuggestionAnswerPostsItsVersionAndAStaleOneCarriesTheItem() = runBlocking {
        MockWebServer().use { server ->
            val t = HttpInboxAssistantTransport("scope", server.url("/").toString(), "token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"id":"lark:c:1","status":"working"}"""))
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"code":"suggestion_stale","error":"建议已更新","item":{"id":"lark:c:1","suggestion":{"version":4}}}"""))
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"code":"inbox_item_done","error":"done"}"""))
            assertEquals("working", t.suggestion("lark:c:1", 3, "confirm", "改过的", "c-1")["status"]?.toString()?.trim('"'))
            val stale = runCatching { t.suggestion("lark:c:1", 3, "reject", null, "c-2") }.exceptionOrNull()
            assertTrue(stale is persona.core.contracts.api.InboxSuggestionStale)
            assertEquals("lark:c:1", (stale as persona.core.contracts.api.InboxSuggestionStale).item?.get("id")?.toString()?.trim('"'))
            val done = runCatching { t.suggestion("lark:c:1", 4, "instruct", "先问时间", "c-3") }.exceptionOrNull()
            assertTrue(done is HubFailure && done.code == "inbox_item_done")
            server.takeRequest().let {
                assertEquals("POST /api/v1/inbox/lark:c:1/suggestion", "${it.method} ${it.path}")
                assertEquals("""{"version":3,"action":"confirm","text":"改过的","client_id":"c-1"}""", it.body.readUtf8())
            }
            server.takeRequest().let { assertEquals("""{"version":3,"action":"reject","client_id":"c-2"}""", it.body.readUtf8()) }
            server.takeRequest().let { assertEquals("""{"version":4,"action":"instruct","text":"先问时间","client_id":"c-3"}""", it.body.readUtf8()) }
        }
    }

    @Test fun connectorCallsUseTheHubsRoutes() = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient()
            val api = HttpConnectorsManagement({ path, method, body ->
                http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            })
            server.enqueue(MockResponse().setBody("""{"machine":"Studio","status":"no_permission","guide":["a","b"],"ai":false}"""))
            server.enqueue(MockResponse().setBody("""{"machine":"Studio","status":"ok","ai":true}"""))
            server.enqueue(MockResponse().setBody("""{"chats":3,"people":2,"inbox":1}"""))
            server.enqueue(MockResponse().setBody("""{"chats":[{"id":"c1","name":"Alex","last":"2026-01-01T00:00:00Z","unread":2}],"more":true,"before":"x"}"""))
            server.enqueue(MockResponse().setBody("""{"chat":{"id":"c 1","name":"Alex"},"messages":[{"id":"m","at":"2026-01-01T00:00:00Z","from_me":true,"text":"hi"}]}"""))
            server.enqueue(MockResponse().setBody("""{"account_id":"acct","anti_recall":true,"show_edit_history":false}"""))
            server.enqueue(MockResponse().setBody("""{"account_id":"acct","anti_recall":true,"show_edit_history":true}"""))
            server.enqueue(MockResponse().setBody("""{"configured":true,"client_id":"id","redirect_uri":"persona-mail-oauth://outlook"}"""))
            server.enqueue(MockResponse().setBody("""{"account":{"id":"o1","email":"me@example.com","provider":"outlook","auth":"oauth2"}}"""))

            val status = api.messagesStatus()
            assertEquals(listOf("a", "b"), status.guide)
            assertEquals(false, status.ai)
            assertEquals(true, api.setMessagesAi(true).ai)
            assertEquals(3, api.deleteMessagesData().chats)
            val chats = api.messagesChats("y")
            assertEquals(2, chats.chats.single().unread)
            assertEquals("x", chats.before)
            assertTrue(api.messagesThread("c 1").messages.single().isMine)
            val display = api.larkMessageDisplay()
            assertEquals(true, api.setLarkMessageDisplay(display, "show_edit_history", true).showEditHistory)
            assertEquals("persona-mail-oauth://outlook", api.setMicrosoftClientId("id").redirectUri)
            assertEquals("outlook", api.microsoftSignInFinish(MicrosoftFinish("s", error = "e", errorDescription = "d")).provider)

            fun next() = server.takeRequest()
            assertEquals("GET /api/v1/imessage", next().let { "${it.method} ${it.path}" })
            next().let { assertEquals("PUT /api/v1/imessage/settings", "${it.method} ${it.path}"); assertEquals("""{"ai":true}""", it.body.readUtf8()) }
            assertEquals("DELETE /api/v1/imessage/data", next().let { "${it.method} ${it.path}" })
            assertEquals("GET /api/v1/imessage/chats?limit=50&before=y", next().let { "${it.method} ${it.path}" })
            assertEquals("GET /api/v1/imessage/chats/c%201/messages?limit=50", next().let { "${it.method} ${it.path}" })
            assertEquals("GET /api/v1/lark/message-display", next().let { "${it.method} ${it.path}" })
            next().let {
                assertEquals("POST /api/v1/lark/message-display", "${it.method} ${it.path}")
                assertEquals("""{"account_id":"acct","key":"show_edit_history","value":true,"expected":false}""", it.body.readUtf8())
            }
            next().let { assertEquals("PUT /api/v1/mail/oauth/microsoft/config", "${it.method} ${it.path}"); assertEquals("""{"client_id":"id"}""", it.body.readUtf8()) }
            next().let {
                assertEquals("POST /api/v1/mail/oauth/microsoft/finish", "${it.method} ${it.path}")
                assertEquals("""{"state":"s","error":"e","error_description":"d"}""", it.body.readUtf8())
            }
        }
    }
}
