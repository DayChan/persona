package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AgentSkill
import persona.core.contracts.api.AlwaysGrant
import persona.core.contracts.api.AssistantRefused
import persona.core.contracts.api.AssistantSkills
import persona.core.contracts.api.NewMainResult
import persona.core.contracts.api.ScheduledTask
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class AssistantManagementTest {
    /** "Push when done" travels both ways; a task without it never sends it, so a hub without `task_push` is not sent it. */
    @Test fun taskPushIsReadAndSentOnlyWhenSet() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient()) { true }
            server.enqueue(MockResponse().setBody("""{"tasks":[{"id":"t1","repeat":"interval","every_minutes":30,"push":"asked"},{"id":"t2","repeat":"daily"}]}"""))
            val (asked, plain) = api.tasks()
            server.takeRequest()
            assertTrue(asked.pushesOnlyAsked)
            assertNull(plain.push)
            assertFalse(plain.pushesOnlyAsked)

            server.enqueue(MockResponse().setBody("""{"task":{"id":"t1","repeat":"interval","push":"asked"}}"""))
            api.saveTask(asked)
            assertEquals("asked", JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject.getValue("push").jsonPrimitive.content)
            server.enqueue(MockResponse().setBody("""{"task":{"id":"t1","repeat":"interval","push":""}}"""))
            api.saveTask(asked.copy(push = ""))
            assertEquals("", JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject.getValue("push").jsonPrimitive.content)
            server.enqueue(MockResponse().setBody("""{"task":{"id":"t2","repeat":"daily"}}"""))
            api.saveTask(plain)
            assertFalse(JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject.containsKey("push"))
        }
    }

    @Test fun migratedActionsPreserveDefaultsPathsAndPairingScope() = runBlocking {
        MockWebServer().use { server ->
            var current = true
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient()) { current }
            server.enqueue(MockResponse().setBody("""{"task":{"id":"task_1","repeat":"daily","time":"09:00","enabled":true,"next_run":"2026-11-02T09:00:00+08:00"}}"""))
            val created = api.saveTask(ScheduledTask(title = "早报", prompt = "整理安排", repeats = ScheduledTask.DAILY, time = "09:00",
                enabled = true, nextRun = "server-owned", runSessionId = "server-owned"))
            val post = server.takeRequest()
            assertEquals("POST", (post.method).zhAny())
            assertEquals("/api/v1/assistant/tasks", (post.path).zhAny())
            assertEquals("Bearer test-token", (post.getHeader("Authorization")).zhAny())
            val body = JsonCodec.parseToJsonElement(post.body.readUtf8()).jsonObject
            assertEquals("09:00", (body.getValue("time").jsonPrimitive.content).zhAny())
            assertEquals("daily", (body.getValue("repeat").jsonPrimitive.content).zhAny())
            assertTrue(body.getValue("enabled").jsonPrimitive.boolean)
            assertEquals(JsonArray(emptyList()), body["weekdays"])
            assertEquals("", (body.getValue("permission_mode").jsonPrimitive.content).zhAny())
            assertFalse(body.containsKey("id"))
            assertFalse(body.containsKey("next_run"))
            assertFalse(body.containsKey("run_session_id"))
            assertFalse(body.containsKey("push"))
            assertEquals("task_1", (created.id).zhAny())
            assertNotNull(created.nextRun)

            server.enqueue(MockResponse().setBody("""{"task":{"id":"task_1","repeat":"interval","every_minutes":30,"enabled":false}}"""))
            api.saveTask(created.copy(repeats = ScheduledTask.INTERVAL, everyMinutes = 30, enabled = false, permissionMode = "auto"))
            val put = server.takeRequest()
            assertEquals("PUT", (put.method).zhAny())
            assertEquals("/api/v1/assistant/tasks/task_1", (put.path).zhAny())
            val edited = JsonCodec.parseToJsonElement(put.body.readUtf8()).jsonObject
            assertEquals(30, edited.getValue("every_minutes").jsonPrimitive.int)
            assertEquals("auto", (edited.getValue("permission_mode").jsonPrimitive.content).zhAny())

            server.enqueue(MockResponse().setBody("""{"task":{"id":"task_1","repeat":"daily"}}"""))
            assertNull(api.freshTask("task_1").runSessionId)
            assertEquals("/api/v1/assistant/tasks/task_1/fresh", (server.takeRequest().path).zhAny())
            server.enqueue(MockResponse().setBody("{}"))
            api.endConversation(SessionKey("demo", "branch/one"))
            val close = server.takeRequest()
            assertEquals("POST", (close.method).zhAny())
            assertEquals("/api/v1/sessions/demo/branch%2Fone/close", (close.path).zhAny())

            server.enqueue(MockResponse().setBody("""{"machine_id":"demo","session_id":"started"}"""))
            assertEquals(SessionKey("demo", "started"), api.runTask("task_1"))
            assertEquals("/api/v1/assistant/tasks/task_1/run", (server.takeRequest().path).zhAny())
            server.enqueue(MockResponse().setBody("{}"))
            api.setNotes("")
            assertEquals("{\"notes\":\"\"}", (server.takeRequest().body.readUtf8()).zhAny())
            server.enqueue(MockResponse().setBody("{}"))
            api.saveMemory("example.md", "")
            val memory = server.takeRequest()
            assertEquals("/api/v1/assistant/memory/example.md", (memory.path).zhAny())
            assertEquals("{\"content\":\"\"}", (memory.body.readUtf8()).zhAny())

            current = false
            try { api.warm(); fail("Stale pairings must not send requests") }
            catch (_: CancellationException) { }
            assertEquals(7, server.requestCount)
        }
    }

    @Test fun archivingPostsToTheConversationsArchiveRoute() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"ok":true,"archived":true}"""))
            api.archiveConversation(SessionKey("demo", "side-1"))
            val archive = server.takeRequest()
            assertEquals("POST", (archive.method).zhAny())
            assertEquals("/api/v1/assistant/conversations/side-1/archive", (archive.path).zhAny())
        }
    }

    @Test fun alwaysGrantsAreListedAndRevokedPerConversationAndAMissingRouteShowsNothing() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            val session = SessionKey("demo", "side-1")
            val hash = "a".repeat(64)
            server.enqueue(MockResponse().setBody("""{"grants":[{"key_hash":"$hash","tool_name":"Bash","created_at":"2026-11-08T09:30:00Z"}]}"""))
            assertEquals(listOf(AlwaysGrant(hash, "Bash", "2026-11-08T09:30:00Z")), api.alwaysGrants(session))
            val list = server.takeRequest()
            assertEquals("GET", (list.method).zhAny())
            assertEquals("/api/v1/assistant/conversations/side-1/always", (list.path).zhAny())
            server.enqueue(MockResponse().setBody("""{"ok":true,"restart_required":true}"""))
            assertTrue(api.revokeAlwaysGrant(session, hash))
            val revoke = server.takeRequest()
            assertEquals("DELETE", (revoke.method).zhAny())
            assertEquals("/api/v1/assistant/conversations/side-1/always/$hash", (revoke.path).zhAny())
            server.enqueue(MockResponse().setBody("""{"ok":true,"restart_required":false}"""))
            assertFalse(api.revokeAlwaysGrant(session, hash))
            server.takeRequest()
            // A hub before the route, or a device without full access: nothing to show rather than an error.
            for (status in listOf(404, 403)) {
                server.enqueue(MockResponse().setResponseCode(status).setBody("""{"error":"no"}"""))
                assertNull(api.alwaysGrants(session))
                server.takeRequest()
            }
            server.enqueue(MockResponse().setResponseCode(500).setBody("""{"error":"permission store unavailable"}"""))
            try { api.alwaysGrants(session); fail("A server failure must throw") } catch (_: HubFailure) { }
        }
    }

    @Test fun compactAndNewMainSendTheirBodiesAndTurnRefusalsIntoTheHubsWords() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            api.compactConversation(SessionKey("demo", "main-1"))
            val compact = server.takeRequest()
            assertEquals("POST", (compact.method).zhAny())
            assertEquals("/api/v1/assistant/conversations/main-1/compact", (compact.path).zhAny())
            assertEquals("""{"machine_id":"demo"}""", (compact.body.readUtf8()).zhAny())

            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"code":"busy","error":"正在回答，等这一轮结束再压缩"}"""))
            try { api.compactConversation(SessionKey("demo", "main-1")); fail("A refusal must throw") }
            catch (refused: AssistantRefused) {
                assertEquals("busy", (refused.code).zhAny())
                assertEquals("正在回答，等这一轮结束再压缩", (refused.message).zhAny())
            }
            server.takeRequest()

            server.enqueue(MockResponse().setBody("""{"ok":true,"archived":"main-1","carried":true}"""))
            assertEquals(NewMainResult("main-1", true), api.newMainConversation(true, "main-1"))
            val created = server.takeRequest()
            assertEquals("/api/v1/assistant/main/new", (created.path).zhAny())
            assertEquals("""{"carry":"summary","expect":"main-1"}""", (created.body.readUtf8()).zhAny())

            server.enqueue(MockResponse().setBody("""{"ok":true,"archived":"","carried":false}"""))
            assertEquals(NewMainResult(), api.newMainConversation(false, null))
            assertEquals("""{"carry":"none"}""", (server.takeRequest().body.readUtf8()).zhAny())

            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"code":"main_changed","error":"主对话已经换过了，刷新后再试"}"""))
            try { api.newMainConversation(true, "main-1"); fail("A refusal must throw") }
            catch (refused: AssistantRefused) { assertEquals("main_changed", (refused.code).zhAny()) }
            server.takeRequest()

            // Anything else stays the hub failure it is.
            server.enqueue(MockResponse().setResponseCode(500).setBody("""{"error":"boom"}"""))
            try { api.newMainConversation(true, "main-1"); fail("A failure must throw") }
            catch (failure: HubFailure) { assertEquals(500, failure.status) }
        }
    }

    @Test fun skillsReadTheAssistantsMachineAndTurnItsReasonIntoTheHubsWords() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"machine_id":"macbook","machine":"MacBook","skills":[
                {"name":"lark-gateway","description":"Feishu / Lark","source":"builtin"},
                {"name":"weekly-report","source":"user","extra":1},{"name":""}]}"""))
            assertEquals(AssistantSkills("macbook", "MacBook", listOf(AgentSkill("lark-gateway", "Feishu / Lark", "builtin"), AgentSkill("weekly-report", null, "user"))),
                api.skills())
            val get = server.takeRequest()
            assertEquals("GET", get.method)
            assertEquals("/api/v1/skills", get.path)

            server.enqueue(MockResponse().setBody("""{"machine_id":"macbook","skills":[]}"""))
            assertEquals(AssistantSkills("macbook", null, emptyList()), api.skills())
            server.takeRequest()
            server.enqueue(MockResponse().setBody("{}"))
            assertEquals(AssistantSkills(), api.skills())
            server.takeRequest()

            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"code":"conflict","error":"no machine runs the assistant"}"""))
            try { api.skills(); fail("A refusal must throw") }
            catch (refused: AssistantRefused) { assertEquals("no machine runs the assistant", refused.message) }
            server.takeRequest()
            server.enqueue(MockResponse().setResponseCode(502).setBody("""{"code":"offline","error":"machine is offline"}"""))
            try { api.skills(); fail("A refusal must throw") }
            catch (refused: AssistantRefused) { assertEquals("offline", refused.code) }
            server.takeRequest()

            server.enqueue(MockResponse().setResponseCode(401).setBody("""{"code":"invalid_token","error":"invalid token"}"""))
            try { api.skills(); fail("A failure must throw") }
            catch (failure: HubFailure) { assertEquals(HubError.PairingExpired, failure.kind) }
        }
    }
}
