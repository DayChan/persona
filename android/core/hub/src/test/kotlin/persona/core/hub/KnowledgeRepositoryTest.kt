package persona.core.hub

import persona.core.contracts.HubScope
import persona.core.contracts.api.KnowledgeKind
import persona.core.contracts.api.KnowledgeUpdate
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.ResponseBody.Companion.toResponseBody
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class KnowledgeRepositoryTest {
    @Test fun oldDisabledReplyIsScopedAndConfigurationRefusalsDoNotRetry() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setResponseCode(503).setHeader("X-Request-Id", "request-1")
                .setBody("""{"code":"internal","detail":"the knowledge base is off","error":"Request failed"}"""))
            val legacy = runCatching { repository.list(KnowledgeKind.Projects) }.exceptionOrNull() as HubFailure
            assertEquals("kb_disabled", legacy.code)
            assertFalse(legacy.retryable)
            assertEquals("request-1", legacy.requestId)
            assertTrue(legacy.body!!.contains("the knowledge base is off"))
            server.enqueue(MockResponse().setResponseCode(403).setBody("""{"code":"kb_read_only","error":"Request failed","retryable":true}"""))
            val readOnly = runCatching { repository.update(KnowledgeKind.Projects, "example", KnowledgeUpdate(tags = emptyList())) }
                .exceptionOrNull() as HubFailure
            assertEquals("kb_read_only", readOnly.code)
            assertFalse(readOnly.retryable)
            assertEquals(HubError.Server, readOnly.kind)
            assertEquals(2, server.requestCount)
        }
    }

    @Test fun ambiguousLegacyErrorsKeepTheirOriginalCode() {
        for ((path, status, code, detail) in listOf(
            listOf("/api/v1/models", "503", "internal", "the knowledge base is off"),
            listOf("/api/v1/kb-other/projects", "503", "internal", "the knowledge base is off"),
            listOf("/api/v1/kb/projects", "502", "internal", "the knowledge base is off"),
            listOf("/api/v1/kb/projects", "503", "internal", "wrapped: the knowledge base is off"),
            listOf("/api/v1/kb/projects", "503", "future_code", "the knowledge base is off"),
        )) {
            val response = okhttp3.Response.Builder().request(okhttp3.Request.Builder().url("https://hub.invalid$path").build())
                .protocol(okhttp3.Protocol.HTTP_1_1).code(status.toInt()).message("Unavailable")
                .body("""{"code":"$code","detail":"$detail","error":"Request failed"}""".toResponseBody()).build()
            response.use { assertEquals(code, HubFailure.from(it).code) }
        }
    }

    @Test fun preservesAvailabilityCodesAndSuccessfulEmptyLists() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            for (code in listOf("kb_disabled", "kb_unavailable")) {
                server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"unavailable","code":"$code"}"""))
                val error = runCatching { repository.list(KnowledgeKind.Projects) }.exceptionOrNull() as HubFailure
                assertEquals(code, error.code)
                assertEquals(503, error.status)
            }
            server.enqueue(MockResponse().setBody("""{"items":[],"tags":[]}"""))
            assertTrue(repository.list(KnowledgeKind.Projects).items.isEmpty())
        }
    }

    @Test fun readsExistingWireFormatEncodesParametersAndRejectsRetiredPairingsAndRedirects() = runBlocking {
        MockWebServer().use { server ->
            var current = true
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { current })
            server.enqueue(MockResponse().setBody("""{"items":[{"kind":"projects","id":"sample","name":"示例项目","relevance":80,"tags":null}],"tags":null,"future":true}"""))
            assertEquals("示例项目", (repository.list(KnowledgeKind.Projects, "A & B").items.single().name).zhAny())
            val list = server.takeRequest()
            assertEquals("A & B", (list.requestUrl!!.queryParameter("tag")).zhAny())
            assertEquals("Bearer fixture-token", (list.getHeader("Authorization")).zhAny())
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"people","id":"sample/#id","path":"data/people/sample","summary":"# 档案","log":null,"links":null},"agents":null}"""))
            assertEquals("# 档案", (repository.detail(KnowledgeKind.People, "sample/#id").summary).zhAny())
            assertEquals("/api/v1/kb/people/sample%2F%23id", (server.takeRequest().path).zhAny())
            server.enqueue(MockResponse().setBody("""{"hits":[{"path":"projects/sample/summary.md","line":"进展"}]}"""))
            assertEquals(1, repository.search("进展 & 状态").size)
            val search = server.takeRequest()
            assertEquals("进展 & 状态", (search.requestUrl!!.queryParameter("q")).zhAny())
            assertEquals("/api/v1/kb-search", (search.requestUrl!!.encodedPath).zhAny())
            server.enqueue(MockResponse().setResponseCode(302).setHeader("Location", "https://example.com/redirect"))
            try { repository.list(KnowledgeKind.Chats); fail("Redirect must not receive pairing credentials") }
            catch (_: HubFailure) { }
            server.takeRequest()
            current = false
            try { repository.search("query"); fail("Retired pairing must not issue requests") }
            catch (_: CancellationException) { }
            assertEquals(4, server.requestCount)
        }
    }

    @Test fun entryKeepsLiveAgentsAndTaggingSendsOnlyWhatIsSet() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"projects","id":"weather","path":"data/projects/weather",
                "meta":{"dirs":["laptop:/code/weather"]}},"agents":[
                {"machine_id":"laptop","session_id":"s1","title":"小组件","cwd":"/code/weather","status":"working"},
                {"machine_id":"laptop","session_id":"s2","cwd":"/code/weather","status":"offline"},
                {"machine_id":"server","session_id":"persona","assistant":true,"status":"idle"}]}"""))
            val entry = repository.entry(KnowledgeKind.Projects, "weather")
            assertEquals(listOf("s1"), (entry.agents.orEmpty().map { it.sessionId }).zhAny())
            assertEquals("/api/v1/kb/projects/weather", (server.takeRequest().path).zhAny())
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"projects","id":"other","path":"x"}}"""))
            assertTrue(runCatching { repository.entry(KnowledgeKind.Projects, "weather") }.exceptionOrNull() is HubFailure)
            server.takeRequest()

            server.enqueue(MockResponse().setBody("""{"id":"weather"}"""))
            repository.update(KnowledgeKind.Projects, "weather", KnowledgeUpdate("P1", 55, emptyList(), "paused", "long"))
            val put = server.takeRequest()
            assertEquals("PUT", (put.method).zhAny())
            assertEquals("/api/v1/kb/projects/weather", (put.path).zhAny())
            assertEquals("Bearer fixture-token", (put.getHeader("Authorization")).zhAny())
            assertEquals("""{"importance":"P1","relevance":55,"tags":[],"status":"paused","project_kind":"long"}""", (put.body.readUtf8()).zhAny())
            server.enqueue(MockResponse().setBody("{}"))
            repository.update(KnowledgeKind.People, "p", KnowledgeUpdate("P0", 0, listOf("a")))
            assertEquals("""{"importance":"P0","relevance":0,"tags":["a"]}""", (server.takeRequest().body.readUtf8()).zhAny())
            server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"not found"}"""))
            assertTrue(runCatching { repository.update(KnowledgeKind.People, "gone", KnowledgeUpdate(tags = emptyList())) }.exceptionOrNull() is HubFailure)
        }
    }

    @Test fun recordingDetailKeepsItsSegments() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setBody("""{"items":[{"kind":"recordings","id":"r1","name":"r1","updated":"2026-11-05T14:00:00Z","extra":"phone"}],"tags":[]}"""))
            assertEquals("phone", repository.list(KnowledgeKind.Recordings).items.single().extra)
            assertEquals("/api/v1/kb/recordings", server.takeRequest().requestUrl!!.encodedPath)
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"recordings","id":"r1","path":"data/recordings/r1",
                "meta":{"started_at":"2026-11-05T14:00:00Z","duration":12,"complete":true,"bookmarks":[]},"summary":"",
                "recent":"x","segments":[{"start":0.5,"end":2,"text":"你好","speaker":"a"}],"unknown":[1]}}"""))
            val detail = repository.detail(KnowledgeKind.Recordings, "r1")
            assertEquals(listOf(0.5 to "你好"), detail.segments.orEmpty().map { it.start to it.text })
            assertEquals("/api/v1/kb/recordings/r1", server.takeRequest().path)
        }
    }
}
