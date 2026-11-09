package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.feature.inbox.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import java.nio.file.Files

/** The data side of "AI draft", "Hand to AI" and the row labels. */
@OptIn(ExperimentalCoroutinesApi::class)
class InboxHandOffRepositoryTest {
    @Test fun recordsCarryLabelsMailAndWhoWroteTheDraft() = runTest {
        val directory = Files.createTempDirectory("inbox-labels-test").toFile()
        try {
            val transport = ItemsTransport(buildJsonArray {
                add(buildJsonObject {
                    put("id", "lark:f2:5"); put("source", "lark"); put("kind", "mention"); put("title", "项目群")
                    put("lark", buildJsonObject { put("feed_id", "f2"); put("at_all", true) })
                })
                add(buildJsonObject {
                    put("id", "mail:a1:t:7"); put("source", "mail"); put("kind", "mail"); put("title", "周报"); put("account_name", "me@example.com")
                    // The hub leaves direct out when the user was only copied.
                    put("mail", buildJsonObject { put("account_id", "a1"); put("mailbox", "INBOX"); put("uids", buildJsonArray { add(5); add(7) }) })
                })
                add(buildJsonObject {
                    put("id", "mail:a1:t:9"); put("source", "mail"); put("kind", "mail"); put("title", "促销")
                    put("mail", buildJsonObject { put("account_id", "a1"); put("uids", buildJsonArray { add(9) }); put("direct", true) })
                })
            })
            val machines = MutableStateFlow(listOf(Machine("laptop", name = "我的笔记本")))
            val repository = repository(transport, directory, backgroundScope, machines = machines)
            // The pairing's own start (which clears the list for it) comes first.
            repository.snapshot.first { it.contentSource != null }
            repository.refresh()
            val records = repository.snapshot.value.records.associateBy { it.item.id }
            assertTrue(records.getValue("lark:f2:5").atAll)
            assertEquals("lark", records.getValue("lark:f2:5").source)
            assertTrue(records.getValue("lark:f2:5").isLark)
            val copied = records.getValue("mail:a1:t:7").mail!!
            assertEquals(InboxMailRef("a1", "INBOX", listOf(5L, 7L), direct = false), copied)
            assertFalse(records.getValue("mail:a1:t:7").isLark)
            assertTrue(records.getValue("mail:a1:t:9").mail!!.direct)
            assertEquals("INBOX", records.getValue("mail:a1:t:9").mail!!.mailbox)
            repository.snapshot.first { it.machines["laptop"] == "我的笔记本" }

            // A draft AI wrote shows as its card; the user's own reads "Draft · …".
            repository.saveDraft(ReplyTarget.Inbox("mail:a1:t:7"), ReplyDraft("AI 写的", ai = true))
            repository.saveDraft(ReplyTarget.Inbox("mail:a1:t:9"), ReplyDraft("自己写的"))
            val drafted = repository.snapshot.value.records.associateBy { it.item.id }
            assertTrue(drafted.getValue("mail:a1:t:7").aiDraft)
            assertNull(drafted.getValue("mail:a1:t:7").localStatus)
            assertFalse(drafted.getValue("mail:a1:t:9").aiDraft)
            assertEquals("草稿 · 自己写的", drafted.getValue("mail:a1:t:9").localStatus)
            assertTrue(repository.readDraft(ReplyTarget.Inbox("mail:a1:t:7"))!!.ai)
            assertFalse(repository.readDraft(ReplyTarget.Inbox("mail:a1:t:9"))!!.ai)
        } finally {
            directory.deleteRecursively()
        }
    }

    // #1773: the list carries how triage stands, refreshed with it; an older hub says nothing, and the banner stays away.
    @Test fun refreshCarriesTheHubsTriage() = runTest {
        val directory = Files.createTempDirectory("inbox-triage-test").toFile()
        try {
            val transport = ItemsTransport(buildJsonArray {})
            val repository = repository(transport, directory, backgroundScope)
            repository.snapshot.first { it.contentSource != null }
            repository.refresh()
            assertNull(repository.snapshot.value.triage)

            transport.triage = buildJsonObject {
                put("state", "unconfigured"); put("waiting", 3)
                put("sources", buildJsonArray { add(buildJsonObject {
                    put("source", "lark"); put("name", "飞书"); put("state", "unconfigured"); put("waiting", 3)
                }) })
            }
            repository.refresh()
            assertEquals(InboxTriage("unconfigured", null, 3, listOf(InboxTriageSource("lark", "飞书", "unconfigured", waiting = 3))),
                repository.snapshot.value.triage)

            // Sorted again: back to ok, and the banner goes.
            transport.triage = buildJsonObject { put("state", "ok"); put("waiting", 0) }
            repository.refresh()
            assertEquals("ok", repository.snapshot.value.triage?.state)
        } finally {
            directory.deleteRecursively()
        }
    }

    @Test fun recordsCarryReadStateWithoutChangingMentionOrTier() = runTest {
        val directory = Files.createTempDirectory("inbox-read-test").toFile()
        try {
            val transport = ItemsTransport(buildJsonArray {
                for (read in listOf(false, true)) for (atAll in listOf(false, true)) {
                    add(buildJsonObject {
                        put("id", "mention-$read-$atAll"); put("source", "lark"); put("kind", "mention")
                        put("tier", "fyi"); put("read", read)
                        put("lark", buildJsonObject { put("feed_id", "feed"); put("at_all", atAll) })
                    })
                }
                add(buildJsonObject { put("id", "without-read"); put("kind", "mention") })
            })
            val repository = repository(transport, directory, backgroundScope)
            repository.snapshot.first { it.contentSource != null }
            repository.refresh()
            val records = repository.snapshot.value.records.associateBy { it.item.id }
            for (read in listOf(false, true)) for (atAll in listOf(false, true)) {
                val record = records.getValue("mention-$read-$atAll")
                assertEquals(read, record.read)
                assertEquals(atAll, record.atAll)
                assertEquals("mention", record.item.kind)
                assertEquals("fyi", record.item.tier)
            }
            assertFalse(records.getValue("without-read").read)
        } finally {
            directory.deleteRecursively()
        }
    }

    @Test fun handOffGoesToTheMainConversationOrTheAgentsOutboxAndDraftsAskTheHub() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-test").toFile()
        try {
            val main = RecordingMain()
            val agents = RecordingSender()
            val instructions = mutableListOf<Pair<String, String>>()
            val actions = object : InboxActionsTransport {
                override suspend fun collectNow() = Unit
                override suspend fun context(itemId: String) = "上下文"
                override suspend fun draft(itemId: String, instruction: String): String {
                    instructions += itemId to instruction
                    return "能赶上，周五前给你。"
                }
            }
            val repository = repository(ItemsTransport(JsonArray(emptyList())), directory, backgroundScope, main = main, agents = agents, actions = actions)
            repository.handOff(InboxHandOffRequest("item", "<persona-context …>\n\n总结一下", operationId = "op-main"))
            repository.handOff(InboxHandOffRequest("item", "交给 Agent", "laptop", "s1", "op-agent"))
            assertEquals(listOf("op-main" to "<persona-context …>\n\n总结一下"), main.sent)
            assertEquals(listOf(Triple(SessionKey("laptop", "s1"), "交给 Agent", "op-agent")), agents.sent)
            assertEquals("能赶上，周五前给你。", repository.draftReply("item", "在这版上改：能"))
            assertEquals(listOf("item" to "在这版上改：能"), instructions)

            // Without the services a hand-off says so instead of pretending.
            val bare = repository(ItemsTransport(JsonArray(emptyList())), directory.resolve("bare"), backgroundScope)
            assertTrue(runCatching { bare.handOff(InboxHandOffRequest("item", "x", "laptop", "s1")) }.exceptionOrNull() is IllegalStateException)
            assertTrue(runCatching { bare.draftReply("item", "") }.exceptionOrNull() is IllegalStateException)
        } finally {
            directory.deleteRecursively()
        }
    }

    /** Another manager's item handed to Persona with no Agent: its own manager's main conversation, not this phone's main queue. */
    @Test fun anotherManagersItemGoesToItsOwnMainConversation() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-manager").toFile()
        try {
            persona.core.contracts.api.AssistantManagerChoice.choose("hub", null, "assistant")
            val main = RecordingMain()
            val agents = RecordingSender()
            val asked = mutableListOf<String>()
            val items = buildJsonArray {
                add(buildJsonObject { put("id", "mine"); put("kind", "mail"); put("title", "t"); put("manager", "assistant") })
                add(buildJsonObject { put("id", "theirs"); put("kind", "mail"); put("title", "t"); put("manager", "m-2") })
            }
            val repository = repository(ItemsTransport(items), directory, backgroundScope, main = main, agents = agents,
                mainOf = { manager -> asked += manager; SessionKey("lab", "main-$manager") })
            repository.refresh()
            repository.handOff(InboxHandOffRequest("mine", "a", operationId = "op-1"))
            repository.handOff(InboxHandOffRequest("theirs", "b", operationId = "op-2"))
            assertEquals(listOf("op-1" to "a"), main.sent)
            assertEquals(listOf("m-2"), asked)
            assertEquals(listOf(Triple(SessionKey("lab", "main-m-2"), "b", "op-2")), agents.sent)
        } finally {
            persona.core.contracts.api.AssistantManagerChoice.reset()
            directory.deleteRecursively()
        }
    }

    /** Summarize / Draft reply on another manager's item asks that manager's Persona too, by the item's id on the hub. */
    @Test fun askingPersonaGoesToTheItemsManager() = runTest {
        val directory = Files.createTempDirectory("inbox-ask-manager").toFile()
        try {
            persona.core.contracts.api.AssistantManagerChoice.choose("hub", null, "assistant")
            val main = RecordingMain()
            val agents = RecordingSender()
            val items = buildJsonArray {
                add(buildJsonObject { put("id", "mine"); put("kind", "mail"); put("title", "t"); put("preview", "p"); put("manager", "assistant") })
                add(buildJsonObject { put("id", "theirs"); put("kind", "mail"); put("title", "t"); put("preview", "p"); put("manager", "m-2") })
            }
            val repository = repository(ItemsTransport(items), directory, backgroundScope, main = main, agents = agents,
                mainOf = { manager -> SessionKey("lab", "main-$manager") })
            repository.refresh()
            val theirs = repository.snapshot.value.records.first { it.manager == "m-2" }.item.id
            repository.askPersona(InboxPersonaRequest("mine", InboxAiAction.Summarize, operationId = "op-1"))
            repository.askPersona(InboxPersonaRequest(theirs, InboxAiAction.Summarize, operationId = "op-2"))
            assertEquals(listOf("op-1"), main.sent.map { it.first })
            assertEquals(listOf(SessionKey("lab", "main-m-2")), agents.sent.map { it.first })
            val text = agents.sent.single().second
            assertTrue(text, text.contains("item_id=\"theirs\"") && !text.contains("\u001F"))
        } finally {
            persona.core.contracts.api.AssistantManagerChoice.reset()
            directory.deleteRecursively()
        }
    }

    @Test fun mainHandOffUsesTheItemsManagerAndReturnsItsSession() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-route").toFile()
        try {
            AssistantManagerChoice.choose("hub", null, "assistant")
            val transport = ItemsTransport(buildJsonArray {
                add(buildJsonObject { put("id", "item"); put("kind", "mail"); put("title", "Example"); put("manager", "m-2") })
            })
            val calls = mutableListOf<List<String?>>()
            transport.onHandOff = { item, text, client ->
                calls += listOf(item, text, client, currentCoroutineContext()[RequestManager]?.id)
                buildJsonObject {
                    put("session", buildJsonObject { put("machine_id", "hub-machine"); put("session_id", "main-m-2") })
                    put("item", buildJsonObject { put("id", "item"); put("kind", "mail"); put("title", "Handed off") })
                }
            }
            val main = RecordingMain()
            val repository = repository(transport, directory, backgroundScope, main = main)
            repository.snapshot.first { it.contentSource != null }
            repository.refresh()
            val id = repository.snapshot.value.records.single().item.id
            val result = repository.handOff(InboxHandOffRequest(id, "Summarize", operationId = "attempt-1"))
            assertEquals(SessionKey("hub-machine", "main-m-2"), result)
            assertEquals(listOf(listOf("item", "Summarize", "attempt-1", "m-2")), calls)
            assertTrue(main.sent.isEmpty())
            assertEquals("Handed off", repository.snapshot.value.records.single().item.title)
            assertEquals("m-2", repository.snapshot.value.records.single().manager)
        } finally {
            AssistantManagerChoice.reset()
            directory.deleteRecursively()
        }
    }

    @Test fun oldHandOffRoutesFallBackButUnclearFailuresRetainTheAttempt() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-fallback").toFile()
        try {
            val transport = ItemsTransport(JsonArray(emptyList()))
            val main = RecordingMain()
            val repository = repository(transport, directory, backgroundScope, main = main)
            for (status in listOf(404, 405)) {
                transport.onHandOff = { _, _, _ -> throw HttpFailure(status) }
                assertNull(repository.handOff(InboxHandOffRequest("item", "Summarize", operationId = "attempt-$status")))
            }
            assertEquals(listOf("attempt-404", "attempt-405"), main.sent.map { it.first })
            val calls = mutableListOf<String>()
            var failure: Exception? = HttpFailure(502)
            transport.onHandOff = { _, _, client ->
                calls += client
                failure?.let { throw it }
                buildJsonObject { put("session", buildJsonObject { put("machine_id", "hub-machine"); put("session_id", "main") }) }
            }
            val request = InboxHandOffRequest("item", "Summarize", operationId = "same-attempt")
            for (error in listOf(HttpFailure(502), java.io.IOException("connection lost"))) {
                failure = error
                assertSame(error, runCatching { repository.handOff(request) }.exceptionOrNull())
            }
            failure = null
            assertEquals(SessionKey("hub-machine", "main"), repository.handOff(request))
            assertEquals(listOf("same-attempt", "same-attempt", "same-attempt"), calls)
            assertEquals(2, main.sent.size)
        } finally { directory.deleteRecursively() }
    }

    @Test fun offlineMainHandOffDoesNotQueueOrCallTheHub() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-offline").toFile()
        try {
            val transport = ItemsTransport(JsonArray(emptyList()))
            var called = false
            transport.onHandOff = { _, _, _ -> called = true; JsonObject(emptyMap()) }
            val main = RecordingMain()
            val link = object : HubConnection {
                override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
                override val events = MutableSharedFlow<ServerEvent>()
                override fun reconnect() = Unit
                override fun send(message: JsonObject) = false
            }
            val repository = repository(transport, directory, backgroundScope, main = main, link = link)
            val error = runCatching { repository.handOff(InboxHandOffRequest("item", "Summarize")) }.exceptionOrNull()
            assertTrue(error is java.io.IOException)
            assertEquals("network offline", error?.message)
            assertFalse(called)
            assertTrue(main.sent.isEmpty())
        } finally { directory.deleteRecursively() }
    }

    @Test fun malformedMainHandOffReceiptDoesNotFallBackToAnotherSend() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-receipt").toFile()
        try {
            val transport = ItemsTransport(JsonArray(emptyList()))
            val main = RecordingMain()
            val repository = repository(transport, directory, backgroundScope, main = main)
            for (session in listOf(JsonObject(emptyMap()), buildJsonObject { put("machine_id", "hub"); put("session_id", "") })) {
                transport.onHandOff = { _, _, _ -> buildJsonObject { put("session", session) } }
                assertTrue(runCatching { repository.handOff(InboxHandOffRequest("item", "Summarize")) }.exceptionOrNull() is java.io.IOException)
            }
            assertTrue(main.sent.isEmpty())
        } finally { directory.deleteRecursively() }
    }

    private class HttpFailure(override val status: Int) : java.io.IOException("HTTP $status"), HubCodedFailure {
        override val code: String? = null
    }

    private fun repository(
        transport: ItemsTransport, directory: java.io.File, owner: CoroutineScope,
        machines: MutableStateFlow<List<Machine>> = MutableStateFlow(emptyList()),
        main: MainConversationSender? = null, agents: MessageSender? = null, actions: InboxActionsTransport? = null,
        mainOf: (suspend (String) -> SessionKey)? = null,
        link: HubConnection? = null,
    ) = RealInboxRepository(
        object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) },
        InboxReplyJournal(directory, "hub/generation"),
        object : SessionsRepository {
            override val machines = machines
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        },
        object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }, owner, hubConnection = link, mainConversationSender = main, actions = actions, messageSender = agents, mainConversationOf = mainOf,
    ) { _, _ -> error("unused") }

    private class RecordingMain : MainConversationSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val sent = mutableListOf<Pair<String, String>>()
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) { sent += clientMessageId to text }
    }

    private class RecordingSender : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val sent = mutableListOf<Triple<SessionKey, String, String>>()
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            sent += Triple(session, text, clientMessageId)
        }
    }

    private class ItemsTransport(private val items: JsonArray, override val scope: String = "hub/generation") : InboxAssistantTransport {
        /** What the hub says of triage with the list (#1773); none: an older hub. */
        var triage: JsonObject? = null
        var onHandOff: suspend (String, String, String) -> JsonObject = { _, _, _ ->
            throw UnsupportedOperationException("Inbox hand-off route unavailable")
        }
        override suspend fun handOff(itemId: String, text: String, clientId: String) = onHandOff(itemId, text, clientId)
        override suspend fun inbox() = buildJsonObject { put("items", items); triage?.let { put("triage", it) } }
        override suspend fun setDone(itemId: String, done: Boolean) = JsonObject(emptyMap())
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = JsonObject(emptyMap())
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = buildJsonObject {
            put("messages", JsonArray(emptyList())); put("sync_state", "complete")
        }
        override suspend fun assistant() = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean) = JsonObject(emptyMap())
        override suspend fun pins() = JsonObject(emptyMap())
        override suspend fun mutes() = buildJsonObject { put("spaces", JsonArray(emptyList())); put("sessions", JsonArray(emptyList())) }
        override suspend fun media(ref: String) = byteArrayOf()
    }
}
