package persona.core.hub

import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import persona.core.contracts.JsonCodec
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class InboxAssistantTransportTest {
    @Test fun handOffPostsTheItemAndStableAttemptIdAndReturnsItsSession() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            val response = """{"item":{"id":"item/with#separator"},"session":{"machine_id":"hub","session_id":"main"}}"""
            server.enqueue(MockResponse().setBody(response))
            assertEquals(JsonCodec.parseToJsonElement(response).jsonObject, transport.handOff("item/with#separator", "Please handle this", "attempt-id"))
            val request = server.takeRequest()
            assertEquals("POST", request.method)
            assertEquals("/api/v1/inbox/item%2Fwith%23separator/handoff", request.path)
            assertEquals("Bearer test-token", request.getHeader("Authorization"))
            assertEquals("""{"text":"Please handle this","client_id":"attempt-id"}""", request.body.readUtf8())
        }
    }

    @Test fun handOffPreservesUnsupportedAndUncertainFailuresForTheCaller() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            for (status in listOf(404, 405, 502)) {
                server.enqueue(MockResponse().setResponseCode(status).setBody("""{"error":"unavailable"}"""))
                try { transport.handOff("item", "Please handle this", "attempt-id"); fail("The caller must handle HTTP $status") }
                catch (e: HubFailure) { assertEquals(status, e.status) }
                assertEquals("/api/v1/inbox/item/handoff", server.takeRequest().path)
            }
            assertEquals(3, server.requestCount)
        }
    }

    @Test fun conversationNamesUseTheCorrectAccountAndCursor() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("{}"))
            transport.larkConversationsForAccount(2, "next page")
            val request = server.takeRequest()
            assertEquals("GET", request.method)
            assertEquals("/api/v1/lark/conversations", request.requestUrl?.encodedPath)
            assertEquals("chats", request.requestUrl?.queryParameter("scope"))
            assertEquals("2", request.requestUrl?.queryParameter("ol_account"))
            assertEquals("next page", request.requestUrl?.queryParameter("cursor"))
            server.enqueue(MockResponse().setBody("{}"))
            transport.larkConversations()
            assertNull(server.takeRequest().requestUrl?.queryParameter("ol_account"))
        }
    }

    @Test fun bulkPostsTheActionAndAnOldHubGetsDoneTheOldWay() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            val targets = listOf(persona.core.contracts.api.InboxDoneTarget("a", "va"), persona.core.contracts.api.InboxDoneTarget("b"))
            server.enqueue(MockResponse().setBody("{\"action\":\"dismiss\",\"changed\":[],\"ids\":[]}"))
            transport.bulk(targets, persona.core.contracts.api.InboxBulkAction.Dismiss)
            val bulk = server.takeRequest()
            assertEquals("/api/v1/inbox/bulk", bulk.path)
            assertEquals("""{"action":"dismiss","items":[{"id":"a","version":"va"},{"id":"b"}]}""", bulk.body.readUtf8())

            // A hub from before /inbox/bulk: "Done" and "Dismiss" go to /inbox/done, like the web and Apple.
            server.enqueue(MockResponse().setResponseCode(405).setBody("{\"error\":\"method not allowed\"}"))
            server.enqueue(MockResponse().setBody("{\"changed\":[]}"))
            transport.bulk(targets, persona.core.contracts.api.InboxBulkAction.Done)
            server.takeRequest()
            val old = server.takeRequest()
            assertEquals("/api/v1/inbox/done", old.path)
            assertEquals("""{"done":true,"items":[{"id":"a","version":"va"},{"id":"b"}]}""", old.body.readUtf8())
            server.enqueue(MockResponse().setResponseCode(405).setBody("{\"error\":\"method not allowed\"}"))
            server.enqueue(MockResponse().setBody("{\"changed\":[]}"))
            transport.bulk(targets, persona.core.contracts.api.InboxBulkAction.Dismiss)
            server.takeRequest()
            val dismissed = server.takeRequest()
            assertEquals("/api/v1/inbox/done", dismissed.path)
            assertEquals("""{"done":true,"items":[{"id":"a","version":"va"},{"id":"b"}]}""", dismissed.body.readUtf8())
            assertEquals(5, server.requestCount)
        }
    }

    @Test fun documentReadsUseTheReadonlyProxyAndMediaRemainsPairingBound() = runBlocking {
        MockWebServer().use { server ->
            var current = true
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient()) { current }
            server.enqueue(MockResponse().setBody("{\"title\":\"文档\"}"))
            transport.readDocument("https://example.com/docx/token?from=message")
            val read = server.takeRequest()
            assertEquals("GET", (read.method).zhAny())
            assertEquals("/api/v1/lark/raw/documents/read", (read.requestUrl?.encodedPath).zhAny())
            assertEquals("https://example.com/docx/token?from=message", (read.requestUrl?.queryParameter("url")).zhAny())
            assertEquals("json", (read.requestUrl?.queryParameter("format")).zhAny())
            assertEquals("Bearer test-token", (read.getHeader("Authorization")).zhAny())
            server.enqueue(MockResponse().setBody("image"))
            assertArrayEquals("image".toByteArray(), transport.documentMedia("/api/v1/lark/raw/documents/images/cover"))
            server.takeRequest()
            for (ref in listOf("https://example.com/api/v1/lark/raw/documents/images/cover", "/api/v1/lark/raw/contacts")) {
                try { transport.documentMedia(ref); fail("Invalid document media must be rejected") }
                catch (_: IllegalArgumentException) { }
            }
            current = false
            try { transport.readDocument("https://example.com/docx/token"); fail("Stale pairing must be rejected") }
            catch (_: kotlinx.coroutines.CancellationException) { }
            assertEquals(2, server.requestCount)
        }
    }

    @Test fun documentDetailsReadOnlyTheReadersThreeExtras() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            for ((path, wire) in listOf("reader/info" to "/api/v1/lark/raw/documents/reader/info", "header" to "/api/v1/lark/raw/documents/header",
                "ai-summary" to "/api/v1/lark/raw/documents/ai-summary")) {
                server.enqueue(MockResponse().setBody("{\"views\":3}"))
                assertEquals(3, transport.documentExtra(path, "https://example.com/docx/token")["views"]?.jsonPrimitive?.content?.toInt())
                val read = server.takeRequest()
                assertEquals(wire, read.requestUrl?.encodedPath)
                assertEquals("https://example.com/docx/token", read.requestUrl?.queryParameter("url"))
                assertEquals("Bearer test-token", read.getHeader("Authorization"))
            }
            for ((path, link) in listOf("comments" to "https://example.com/docx/token", "../contacts" to "https://example.com/docx/token",
                "header" to "https://user:pass@example.com/docx/token")) {
                try { transport.documentExtra(path, link); fail("$path for $link must be rejected") }
                catch (_: IllegalArgumentException) { }
            }
            assertEquals(3, server.requestCount)
        }
    }

    @Test fun documentCommentsReadAndWriteThePluginsRoutes() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            val doc = "https://example.com/docx/token"
            server.enqueue(MockResponse().setBody("{\"threads\":[]}"))
            transport.documentComments(doc)
            server.takeRequest().let {
                assertEquals("GET", it.method)
                assertEquals("/api/v1/lark/raw/documents/comments", it.requestUrl?.encodedPath)
                assertEquals(doc, it.requestUrl?.queryParameter("url"))
            }
            val body = kotlinx.serialization.json.buildJsonObject { put("url", kotlinx.serialization.json.JsonPrimitive(doc)); put("content", kotlinx.serialization.json.JsonPrimitive("好")) }
            server.enqueue(MockResponse().setBody("{\"ok\":true}"))
            transport.writeDocumentComment(null, body)
            server.takeRequest().let {
                assertEquals("POST", it.method)
                assertEquals("/api/v1/lark/documents/comments", it.path)
                assertEquals(body.toString(), it.body.readUtf8())
            }
            server.enqueue(MockResponse().setBody("{\"ok\":true}"))
            transport.writeDocumentComment("th/1", body)
            assertEquals("/api/v1/lark/documents/comments/th%2F1/replies", server.takeRequest().path)
            server.enqueue(MockResponse().setBody("{\"ok\":true}"))
            transport.resolveDocumentComment("th1", doc, resolved = false)
            server.takeRequest().let {
                assertEquals("/api/v1/lark/documents/comments/th1/resolve", it.path)
                assertEquals("""{"url":"$doc","resolved":false}""", it.body.readUtf8())
            }
            server.enqueue(MockResponse().setBody("{\"members\":[]}"))
            transport.documentPeople(doc, "李")
            server.takeRequest().let {
                assertEquals("/api/v1/lark/documents/people", it.requestUrl?.encodedPath)
                assertEquals("李", it.requestUrl?.queryParameter("q"))
            }
            try { transport.documentComments("https://user:pass@example.com/docx/token"); fail("credentials in the link must be rejected") }
            catch (_: IllegalArgumentException) { }
            assertEquals(5, server.requestCount)
        }
    }

    @Test fun repliesPreserveClientIdAndMediaNeverForwardsCredentialsOffOrigin() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("{}"))
            transport.reply("item/with#separator", null, "回复", "reply-id")
            val reply = server.takeRequest()
            assertEquals("/api/v1/inbox/item%2Fwith%23separator/reply", (reply.path).zhAny())
            assertEquals("reply-id", (JsonCodec.parseToJsonElement(reply.body.readUtf8()).jsonObject["client_id"]?.jsonPrimitive?.content).zhAny())
            assertEquals("Bearer test-token", (reply.getHeader("Authorization")).zhAny())

            server.enqueue(MockResponse().setBody("image"))
            assertArrayEquals("image".toByteArray(), transport.media("/api/v1/lark/media/image?size=120"))
            val media = server.takeRequest()
            assertEquals("/api/v1/lark/media/image?size=120", (media.path).zhAny())
            assertEquals("Bearer test-token", (media.getHeader("Authorization")).zhAny())
            try {
                transport.media("https://example.com/api/v1/lark/media/image")
                fail("Foreign media origin must be rejected")
            } catch (_: IllegalArgumentException) { }
            server.enqueue(MockResponse().setResponseCode(302).setHeader("Location", "https://example.com/image"))
            try {
                transport.media("/api/v1/lark/media/redirect")
                fail("Media redirects must be rejected")
            } catch (_: HubFailure) { }
            server.takeRequest()
            assertEquals(3, server.requestCount)
        }
    }

    @Test fun aConversationOnALauncherNamesItInThePost() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("{\"session\":{\"machine_id\":\"mac\",\"session_id\":\"c\"}}"))
            transport.createConversation(false, "cc_x")
            val launched = server.takeRequest()
            assertEquals("POST", launched.method)
            assertEquals("/api/v1/assistant/conversations", launched.path)
            assertEquals("""{"prompt":"","main":false,"launcher":"cc_x"}""", launched.body.readUtf8())
            server.enqueue(MockResponse().setBody("{\"session\":{\"machine_id\":\"mac\",\"session_id\":\"d\"}}"))
            transport.createConversation(true)
            assertEquals("""{"prompt":"","main":true}""", server.takeRequest().body.readUtf8())
        }
    }
}
