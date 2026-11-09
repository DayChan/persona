package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.InboxBulkAction
import persona.core.contracts.api.InboxDoneTarget
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

/** Synthetic HTTP only: filing and read receipts are distinct writes and preserve their failure semantics. */
class InboxCompletionReadContractTest {
    @Test fun filingPostsVersionsWhileReadOnlyPostsTheSourceReceiptAndReportsFailure() = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient()
            val inbox = HttpInboxAssistantTransport("scope", server.url("/").toString(), "fixture-token", http)
            val lark = HttpLarkConversationTransport(inbox, server.url("/").toString(), "fixture-token", http) { true }
            server.enqueue(MockResponse().setBody("""{"changed":[{"id":"event","done_at":"2026-11-09T01:02:00Z"}]}"""))
            inbox.bulk(listOf(InboxDoneTarget("event", "v1")), InboxBulkAction.Done)
            val done = server.takeRequest()
            assertEquals("/api/v1/inbox/bulk", done.path)
            assertEquals("""{"action":"done","items":[{"id":"event","version":"v1"}]}""", done.body.readUtf8())
            server.enqueue(MockResponse().setBody("{}"))
            lark.markRead("room", 42, 1, listOf("message", "message"))
            val read = server.takeRequest()
            assertEquals("/api/v1/lark/conversations/room/read", read.path)
            assertEquals("""{"position":"42","badge_count":1,"message_ids":["message"]}""", read.body.readUtf8())
            server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"receipt unavailable"}"""))
            assertTrue(runCatching { lark.markRead("room", 43, null, listOf("new-message")) }.exceptionOrNull() is HubFailure)
            assertEquals("/api/v1/lark/conversations/room/read", server.takeRequest().path)
            assertEquals(3, server.requestCount)
            println("Synthetic HTTP: POST /inbox/bulk action=done version=v1; POST /lark/conversations/room/read position=42; 503 propagated; no completion/delete on read")
        }
    }
}
