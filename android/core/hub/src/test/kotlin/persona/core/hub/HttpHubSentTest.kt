package persona.core.hub

import persona.core.contracts.api.SentLetThrough
import persona.core.contracts.api.SentOrigin
import persona.core.contracts.api.SentPage
import persona.core.contracts.parseTime
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class HttpHubSentTest {
    // Like the pairing's connection: a status that is not 2xx is thrown as the hub's error before the client sees it.
    private fun <T> withHub(block: suspend (MockWebServer, HttpHubSent) -> T): T = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            block(server, HttpHubSent { path, method, body ->
                val response = http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
                if (!response.isSuccessful) throw response.use { HubFailure.from(it) }
                response
            })
        }
    }

    @Test fun asksForAPageByTheCursorAndTheConnector() { withHub { server, api ->
        repeat(3) { server.enqueue(MockResponse().setBody("""{"items":[],"next":null}""")) }
        api.page()
        api.page(before = 41, connector = "mail")
        // Zero, empty and absent are not asked for.
        api.page(before = 0, limit = 200, connector = "")
        val asked = List(3) { server.takeRequest() }
        assertEquals(listOf("GET", "GET", "GET"), asked.map { it.method })
        assertEquals(listOf("/api/v1/connectors/sent", "/api/v1/connectors/sent?before=41&connector=mail", "/api/v1/connectors/sent?limit=200"), asked.map { it.path })
        assertTrue(asked.all { it.bodySize == 0L })
    } }

    @Test fun aConnectorIsOneEscapedQueryValueWhateverIsInIt() { withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"items":[]}"""))
        api.page(connector = "a b&c=d")
        assertEquals("/api/v1/connectors/sent?connector=a%20b%26c%3Dd", server.takeRequest().path)
    } }

    @Test fun takesAPageAsItComes() { withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"items":[
          {"id":7,"at":"2026-11-05T12:00:00Z","connector":"github","target":null,"let_through":[],"origin":"no"},
          {"connector":"mail"}, null, 5, {"id":"9"}, {"id":7.5},
          {"id":8,"at":"2026-11-05T12:01:00.123456789Z","connector":"mail","action":"mail_send",
           "target":{"to":["a@example.com"],"subject":"Hi"},"text":"x","summary":"s",
           "origin":{"machine_id":"mac","session_id":"s1","extension":"examplechat"},
           "let_through":{"kind":"card","interaction_id":"ix-1","answered_by":"assistant"},
           "message_ref":"m-1","recalled_at":"2026-11-05T13:00:00Z"}
        ],"next":7}"""))
        val page = api.page()
        // A row with no number for an id is none, and the three objects are objects whatever the hub sent.
        assertEquals(listOf(7L, 8L), page.items.map { it.id })
        assertEquals(7L, page.next)
        val bare = page.items[0]
        assertTrue(bare.connector == "github" && bare.target.isEmpty() && bare.letThrough == SentLetThrough() && bare.origin == SentOrigin())
        assertEquals(1_793_880_000L, parseTime(bare.at)?.epochSecond)
        val full = page.items[1]
        assertEquals(SentOrigin("mac", "s1", "examplechat"), full.origin)
        assertEquals(SentLetThrough("card", interactionId = "ix-1", answeredBy = "assistant"), full.letThrough)
        assertTrue(full.messageRef == "m-1" && full.recalledAt.isNotEmpty() && parseTime(full.at) != null)
        assertEquals(JsonArray(listOf(JsonPrimitive("a@example.com"))), full.target["to"])
    } }

    @Test fun aWholeNumberIsAnIdWhateverItIsWritten() { withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"items":[{"id":12.0},{"id":1e2},{"id":-3}],"next":12.0}"""))
        val page = api.page()
        assertEquals(listOf(12L, 100L, -3L), page.items.map { it.id })
        assertEquals(12L, page.next)
    } }

    @Test fun theLastPageHasNoNextAndAnEmptyOneIsEmpty() { withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"items":[{"id":3}],"next":null}"""))
        val last = api.page()
        assertNull(last.next)
        assertEquals(listOf(3L), last.items.map { it.id })
        server.enqueue(MockResponse().setBody("{}"))
        assertEquals(SentPage(), api.page())
        // `items` that is not a list, and a `next` that is not a number, are no rows and no next.
        server.enqueue(MockResponse().setBody("""{"items":{"id":1},"next":"4"}"""))
        assertEquals(SentPage(), api.page())
    } }

    @Test fun jsonThatIsNotAnObjectIsAnEmptyPageAndAnythingThatIsNotJsonIsAnError() { withHub { server, api ->
        server.enqueue(MockResponse().setBody("null"))
        assertEquals(SentPage(), api.page())
        server.enqueue(MockResponse().setBody("[1,2]"))
        assertEquals(SentPage(), api.page())
        server.enqueue(MockResponse().setBody("not json at all"))
        val bad = runCatching { api.page() }.exceptionOrNull()
        assertTrue(bad is HubFailure && bad.kind == HubError.Protocol)
        server.enqueue(MockResponse().setBody(""))
        assertTrue(runCatching { api.page() }.exceptionOrNull() is HubFailure)
    } }

    @Test fun theHubsRefusalsKeepTheirStatusAndCode() { withHub { server, api ->
        server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"no database: there is no ledger","code":"no_database"}"""))
        val noDatabase = runCatching { api.page() }.exceptionOrNull() as HubFailure
        assertEquals(503, noDatabase.status)
        assertEquals("no_database", noDatabase.code)
        server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"only a device with full rights","code":"forbidden"}"""))
        val forbidden = runCatching { api.page() }.exceptionOrNull() as HubFailure
        assertEquals(403, forbidden.status)
        // A hub from before the ledger has no such route: its mux answers 404 without a code.
        server.enqueue(MockResponse().setResponseCode(404).setBody("404 page not found"))
        val old = runCatching { api.page() }.exceptionOrNull() as HubFailure
        assertEquals(404, old.status)
        assertNull(old.code)
        server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"no such thing","code":"not_found"}"""))
        assertEquals("not_found", (runCatching { api.page() }.exceptionOrNull() as HubFailure).code)
    } }

    @Test fun aRecallPostsTheConfirmationAndReadsTheRowBack() { withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"ok":true,"item":{"id":9,"connector":"github","recalled_at":"2026-11-05T12:01:00Z","can_recall":false}}"""))
        val row = api.recall(9)
        val asked = server.takeRequest()
        assertEquals("POST", asked.method)
        assertEquals("/api/v1/connectors/sent/9/recall", asked.path)
        assertEquals("""{"confirm":true}""", asked.body.readUtf8())
        assertEquals(9L, row?.id)
        assertEquals("2026-11-05T12:01:00Z", row?.recalledAt)
        assertFalse(row!!.canRecall)
        // can_recall is true only as a JSON true
        server.enqueue(MockResponse().setBody("""{"items":[{"id":2,"can_recall":true},{"id":1,"can_recall":"true"}]}"""))
        assertEquals(listOf(true, false), api.page().items.map { it.canRecall })
        server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"too old","code":"too_old"}"""))
        val refused = runCatching { api.recall(9) }.exceptionOrNull() as HubFailure
        assertEquals(409, refused.status)
        assertEquals("too_old", refused.code)
    } }
}
