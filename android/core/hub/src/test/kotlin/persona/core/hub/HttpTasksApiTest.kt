package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/** "Retry" and "Remove" on a task (#1396): the hub's routes, the todo it answers, and its refusal kept for the user. */
class HttpTasksApiTest {
    private fun api(server: MockWebServer, lookup: Boolean = false): HttpTasksApi {
        val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
        return HttpTasksApi({ path, method, body ->
            val rb = body?.toString()?.toRequestBody("application/json".toMediaType())
            http.newCall(Request.Builder().url(server.url(path)).method(method, rb).build()).execute()
        }, kotlinx.coroutines.flow.MutableStateFlow(if (lookup) setOf("sessions_lookup") else emptySet()))
    }

    @Test fun boundAgentLookupEscapesExactIdentityAndRejectsAnUnrelatedRow() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"sessions":[{"machine_id":"box","session_id":"original/1","state":"working"}]}"""))
            val api = api(server, lookup = true)
            val key = persona.core.contracts.SessionKey("box", "original/1")
            assertEquals(key, api.taskAgent(key)?.key)
            val request = server.takeRequest()
            assertEquals("original/1", request.requestUrl?.queryParameter("session_id"))
            assertEquals("box", request.requestUrl?.queryParameter("machine_id"))
            assertEquals("0", request.requestUrl?.queryParameter("summary"))
            server.enqueue(MockResponse().setBody("""{"sessions":[{"machine_id":"box","session_id":"other"}]}"""))
            assertNull(api.taskAgent(key))
        }
    }

    @Test fun anOlderHubUsesTheDurableBindingWithoutAnUnboundedListRequest() = runBlocking {
        MockWebServer().use { server ->
            assertNull(api(server).taskAgent(persona.core.contracts.SessionKey("box", "original")))
            assertEquals(0, server.requestCount)
        }
    }

    @Test fun acceptanceCarriesTheCapturedRoundAndRetainsAStaleRefusal() = runBlocking {
        MockWebServer().use { server ->
            val askedAt = "2026-11-01T09:00:00.123Z"
            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            api(server).acceptTask("todo/one", askedAt)
            val request = server.takeRequest()
            assertEquals("POST", request.method)
            assertEquals("/api/v1/todos/todo%2Fone/accept", request.path)
            assertEquals("""{"asked_at":"$askedAt"}""", request.body.readUtf8())
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"Already handled"}"""))
            try { api(server).acceptTask("todo/one", askedAt); fail("stale acceptance was not refused") }
            catch (error: HubFailure) { assertEquals(409, error.status) }
        }
    }

    @Test fun retryPostsAndAnswersTheReopenedTodo() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"todo":{"id":"todo_1","title":"设置页按钮对齐","origin":"c","plan_id":"p","agents":[]}}"""))
            val todo = api(server).retryTask("todo_1")
            val req = server.takeRequest()
            assertEquals("POST", (req.method).zhAny())
            assertEquals("/api/v1/todos/todo_1/retry", (req.path).zhAny())
            assertEquals("todo_1", (todo.id).zhAny())
            assertNull(todo.doneAt)
            assertNull(todo.startError)
        }
    }

    @Test fun anOfflineMachineIsSaidInTheHubsWords() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"「公司 Mac」现在离线，等它连上再重试"}"""))
            try {
                api(server).retryTask("todo_1")
                fail("a refused retry answered")
            } catch (e: HubFailure) {
                assertEquals(409, e.status)
                assertTrue(e.message.orEmpty(), e.message.orEmpty().contains("现在离线"))
            }
        }
    }

    @Test fun aCardsReplyNamesTheCard() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"id":"example:1","done_by":"replied"}"""))
            api(server).inboxReply("example:1", "修好了", "feed_1")
            val req = server.takeRequest()
            assertEquals("POST", (req.method).zhAny())
            assertTrue(req.path.orEmpty(), req.path.orEmpty().endsWith("/reply"))
            assertEquals("""{"text":"修好了","card":"feed_1"}""", (req.body.readUtf8()).zhAny())
        }
    }

    @Test fun aProjectGoesOnAndOffATodoByItsEscapedName() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"ok":true,"linked":2}"""))
            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            api(server).linkTodoSpace("todo_1", "mac", "web/app")
            val put = server.takeRequest()
            assertEquals("PUT", (put.method).zhAny())
            assertEquals("/api/v1/todos/todo_1/spaces/mac/web%2Fapp", (put.path).zhAny())
            api(server).unlinkTodoSpace("todo_1", "mac", "web/app")
            val delete = server.takeRequest()
            assertEquals("DELETE", (delete.method).zhAny())
            assertEquals("/api/v1/todos/todo_1/spaces/mac/web%2Fapp", (delete.path).zhAny())
        }
    }

    @Test fun startingAnEditedPlanSendsItsTasks() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"plan":{"id":"p","state":"started","tasks":[]}}"""))
            server.enqueue(MockResponse().setBody("""{"plan":{"id":"p","state":"started","tasks":[]}}"""))
            api(server).startPlan("p", false)
            assertEquals("""{"remember":false}""", (server.takeRequest().body.readUtf8()).zhAny())
            api(server).startPlan("p", true, listOf(persona.core.contracts.PlanTask(title = "改标题", prompt = "先看看", machine = "mac", cwd = "/w/app")))
            val body = server.takeRequest().body.readUtf8()
            assertTrue(body, body.contains("\"tasks\":[{\"title\":\"改标题\",\"prompt\":\"先看看\",\"machine\":\"mac\",\"cwd\":\"/w/app\"}]"))
            assertTrue(body, body.contains("\"remember\":true"))
        }
    }

    @Test fun removeDeletesTheTodo() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            api(server).removeTask("todo_1")
            val req = server.takeRequest()
            assertEquals("DELETE", (req.method).zhAny())
            assertEquals("/api/v1/todos/todo_1", (req.path).zhAny())
        }
    }

    @Test fun directCompletionOnlyPatchesDoneAndDecodesTheUserCompletionReceipt() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"todo":{"id":"todo/one","title":"Review the sample release","done_at":"2026-11-04T10:00:00Z"}}"""))
            val completed = api(server).patchTodo("todo/one", done = true)
            val request = server.takeRequest()
            assertEquals("PATCH", request.method)
            assertEquals("/api/v1/todos/todo%2Fone", request.path)
            assertEquals("""{"done":true}""", request.body.readUtf8())
            assertEquals("todo/one", completed.id)
            assertTrue(completed.isDone)
            assertNull(completed.doneBy)
            assertNull(completed.doneReason)
            assertNull(completed.ask)
            assertEquals(1, server.requestCount)
        }
    }

    @Test fun createPatchDeleteAndLinkTodo() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"todo":{"id":"todo_new","title":"测试待办","notes":"说明","agents":[]}}"""))
            val created = api(server).createTodo("测试待办", "说明")
            val createReq = server.takeRequest()
            assertEquals("POST", (createReq.method).zhAny())
            assertEquals("/api/v1/todos", (createReq.path).zhAny())
            assertEquals("todo_new", (created.id).zhAny())
            assertEquals("测试待办", (created.title).zhAny())
            assertEquals("说明", (created.notes).zhAny())

            server.enqueue(MockResponse().setBody("""{"todo":{"id":"todo_new","title":"新标题","done_at":"2026-11-04T10:00:00Z","agents":[]}}"""))
            val patched = api(server).patchTodo("todo_new", title = "新标题", done = true)
            val patchReq = server.takeRequest()
            assertEquals("PATCH", (patchReq.method).zhAny())
            assertEquals("/api/v1/todos/todo_new", (patchReq.path).zhAny())
            assertEquals("新标题", (patched.title).zhAny())
            assertTrue(patched.isDone)

            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            api(server).linkTodoAgent("todo_new", "m1", "s1")
            val linkReq = server.takeRequest()
            assertEquals("PUT", (linkReq.method).zhAny())
            assertEquals("/api/v1/todos/todo_new/agents/m1/s1", (linkReq.path).zhAny())

            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            api(server).unlinkTodoAgent("todo_new", "m1", "s1")
            val unlinkReq = server.takeRequest()
            assertEquals("DELETE", (unlinkReq.method).zhAny())
            assertEquals("/api/v1/todos/todo_new/agents/m1/s1", (unlinkReq.path).zhAny())

            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            api(server).deleteTodo("todo_new")
            val deleteReq = server.takeRequest()
            assertEquals("DELETE", (deleteReq.method).zhAny())
            assertEquals("/api/v1/todos/todo_new", (deleteReq.path).zhAny())
        }
    }

    // A to-do's decisions (#1618, #1853): every one with all=1 (the revised ones too); recording one sends what was typed, trimmed,
    // without a blank reason, and with the decision it revises when there is one.
    @Test fun decisionsAreReadWholeAndRecordedTrimmed() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"decisions":[{"id":"dec_1","todo_id":"todo_ask_1","question":"Merge now?","choice":"Wait",""" +
                """"source":"manual","superseded_by":"dec_2","created_at":"2026-11-09T05:00:00Z","extra":1},""" +
                """{"id":"dec_2","todo_id":"todo_ask_1","question":"Merge now?","choice":"Merge","supersedes_id":"dec_1","source":"manual"}]}"""))
            val list = api(server).todoDecisions("todo_ask_1")
            val read = server.takeRequest()
            assertEquals("GET", read.method)
            assertEquals("/api/v1/todos/todo_ask_1/decisions?all=1", read.path)
            assertEquals(listOf("dec_1", "dec_2"), list.map { it.id })
            assertEquals(listOf(false, true), list.map { it.isCurrent })
            assertEquals("dec_1", list[1].supersedesId)

            server.enqueue(MockResponse().setBody("""{"decision":{"id":"dec_3","todo_id":"todo_ask_1","question":"Merge now?","choice":"Merge"}}"""))
            val added = api(server).addTodoDecision("todo_ask_1", "  Merge now? ", " Merge ", "  ")
            val first = server.takeRequest()
            assertEquals("POST", first.method)
            assertEquals("/api/v1/todos/todo_ask_1/decisions", first.path)
            assertEquals("""{"question":"Merge now?","choice":"Merge"}""", first.body.readUtf8())
            assertEquals("dec_3", added.id)

            server.enqueue(MockResponse().setBody("""{"decision":{"id":"dec_4","question":"Merge now?","choice":"Wait"}}"""))
            api(server).addTodoDecision("todo_ask_1", "Merge now?", "Wait", "The review is not in", supersedesId = "dec_3")
            assertEquals("""{"question":"Merge now?","choice":"Wait","reason":"The review is not in","supersedes_id":"dec_3"}""",
                server.takeRequest().body.readUtf8())
        }
    }

    // A card's action (docs/assistant/assistant-views.md "Persistent submission"): the key is one escaped path segment, the version goes with it.
    @Test fun aCardsActionEscapesItsKeyAndSendsTheVersion() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"action":{"key":"uuid#0/2","action":"choose-b","client_msg_id":"m1","status":"pending","revision":1,"at":"2026-11-05T10:00:00Z"}}"""))
            val answer = api(server).viewAction("c 1", "uuid#0/2", persona.core.contracts.generated.ExtensionViewActionInput(
                "choose-b", null, null, "m1", confirmed = true, expectedVersion = 1))
            val req = server.takeRequest()
            assertEquals("/api/v1/assistant/conversations/c%201/views/uuid%230%2F2/actions", req.path)
            assertEquals("""{"action":"choose-b","client_msg_id":"m1","confirmed":true,"expected_version":1}""", req.body.readUtf8())
            assertEquals("pending", (answer as persona.core.contracts.api.AssistantViewAnswer.Recorded).action.status)
        }
    }

    @Test fun aCardsConflictsAreAnswersNotFailures() = runBlocking {
        MockWebServer().use { server ->
            // The shared example of schema/extension/examples/ViewChangedError.example.json, in short.
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"card changed; review it and confirm again","code":"view_changed","version":4,
                "view":{"id":"view_example","conversation":"c","version":4,"view":{"fallback":"已更新","sections":[]}}}"""))
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"card already answered","code":"already_answered",
                "action":{"key":"view_1","action":"b","client_msg_id":"other","status":"sent","revision":2,"at":"2026-11-05T10:00:00Z"}}"""))
            server.enqueue(MockResponse().setResponseCode(400).setBody("""{"error":"confirm","code":"bad_request"}"""))
            val input = persona.core.contracts.generated.ExtensionViewActionInput("a", null, null, "m", expectedVersion = 2)
            val changed = api(server).viewAction("c", "view_1", input) as persona.core.contracts.api.AssistantViewAnswer.ViewChanged
            assertEquals(4L, changed.version)
            assertEquals(4L, changed.view?.version)
            val answered = api(server).viewAction("c", "view_1", input) as persona.core.contracts.api.AssistantViewAnswer.AlreadyAnswered
            assertEquals("other", answered.action?.clientMsgId)
            try {
                api(server).viewAction("c", "view_1", input)
                fail("a refusal answered")
            } catch (e: HubFailure) {
                assertEquals(400, e.status)
            }
        }
    }

    // A card refused for this device (read-only role, cards switched off) is that request's refusal, not the pairing's
    // end, and so is a credential not usable from this source (#4692); only a 401 ends it.
    @Test fun aRefusedCardKeepsThePairing() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"read-only role cannot reply from a card","code":"forbidden","retryable":false,"delivered":"none"}"""))
            server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"not granted","code":"permission_denied"}"""))
            server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"the master token is restricted to local networks","code":"master_token_local_only"}"""))
            server.enqueue(MockResponse().setResponseCode(401).setBody("""{"error":"invalid token","code":"invalid_token"}"""))
            val input = persona.core.contracts.generated.ExtensionViewActionInput("a", null, null, "m", expectedVersion = 1)
            val kinds = (1..4).map { runCatching { api(server).viewAction("c", "view_1", input) }.exceptionOrNull() as HubFailure }
            assertEquals(listOf(HubError.Server, HubError.Server, HubError.Server, HubError.PairingExpired), kinds.map { it.kind })
            assertTrue(kinds.take(3).none { it.retryable })
            assertEquals("read-only role cannot reply from a card", kinds[0].message)
        }
    }

    @Test fun anHtmlPagesOpenComesBackChecked() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""{"open":"https://example.com/a","version":4}"""))
            val answer = api(server).viewHtmlAction("c", "view_1", persona.core.contracts.generated.ExtensionAssistantHTMLActionInput(
                "open", "guide", null, 4, url = "https://example.com/a", clientMsgId = "m2", confirmed = true))
            val req = server.takeRequest()
            assertEquals("/api/v1/assistant/conversations/c/views/view_1/html-actions", req.path)
            assertEquals("""{"kind":"open","component_key":"guide","expected_version":4,"url":"https://example.com/a","client_msg_id":"m2","confirmed":true}""",
                req.body.readUtf8())
            assertEquals(persona.core.contracts.api.AssistantViewAnswer.Open("https://example.com/a", 4), answer)
        }
    }
}
