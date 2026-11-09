package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.feature.inbox.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import java.io.File
import java.io.IOException
import java.nio.file.Files

@OptIn(ExperimentalCoroutinesApi::class)
class InboxRepositoryTest {
    @Test fun updatingWaitsForEveryLarkPageAndPreservesVerifiedWindowOnOnline() = runBlocking {
        val directory = Files.createTempDirectory("window-reconcile-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            val transport = FakeTransport().apply { onMessages = { _, _, _ -> page(row("first", 10, "消息")) } }
            val hub = FakeHub()
            val repository = repository(transport, directory, owner, hub)
            val observed = MutableStateFlow(LarkConversation())
            owner.launch { repository.conversation("feed", null).collect { observed.value = it } }
            awaitState(observed) { it.verified }
            hub.state.value = ConnectionState.Updating
            awaitState(observed) { !it.verified }
            val requested = CompletableDeferred<Unit>()
            val lastPage = CompletableDeferred<JsonObject>()
            transport.onMessages = { _, _, after ->
                if (after == null) page(row("first", 10, "消息"), newer = "10") else {
                    requested.complete(Unit)
                    lastPage.await()
                }
            }
            val ready = async { hub.openWindowReconciliation.reconcile(5_000) }
            withTimeout(5_000) { requested.await() }
            assertFalse(ready.isCompleted)
            assertFalse(observed.value.verified)
            lastPage.complete(page(row("last", 11, "后续消息")))
            assertTrue(ready.await())
            val verified = awaitState(observed) { it.verified }
            hub.state.value = ConnectionState.Online
            repository.refresh()
            assertTrue(observed.value.verified)
            assertEquals(listOf("first", "last"), verified.messages.map { it.id })
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun inboxAiUsesSharedOfflineOutboxAndRetainsRequestOnWriteFailure() = runTest {
        val directory = Files.createTempDirectory("inbox-persona-test").toFile()
        var failWrite = false
        val store = object : OutboxStore {
            override val entries = MutableStateFlow<List<OutboxEntry>>(emptyList())
            override suspend fun enqueue(entry: OutboxEntry) {
                if (failWrite) throw IOException("Write failed")
                val previous = entries.value.firstOrNull { it.clientMessageId == entry.clientMessageId }
                require(previous == null || previous.text == entry.text)
                if (previous == null) entries.value += entry
            }
            override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?): Boolean {
                val old = entries.value.firstOrNull { it.clientMessageId == id && it.state in from } ?: return false
                entries.value = entries.value.map { if (it == old) it.copy(state = to) else it }
                return true
            }
            override suspend fun recoverInterrupted() = Unit
        }
        val sender = OutboxMessageSender(HubScope("hub", "generation"), store, object : MessageTransport {
            override val readyToSend = MutableStateFlow(false)
            override val supportsClientOps = true
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Offline must not use transport")
        }, backgroundScope)
        try {
            val transport = FakeTransport()
            val hub = FakeHub().apply { state.value = ConnectionState.WaitingForNetwork }
            val snapshots = ListSnapshotStore(directory.resolve("snapshots"))
            snapshots.save("${transport.scope}:inbox", buildJsonObject {
                put("items", buildJsonArray { add(buildJsonObject { put("id", "item"); put("title", "事项"); put("preview", "已加载内容") }) })
            })
            val repository = repository(transport, directory, backgroundScope, hub, snapshots, sender)
            repository.snapshot.first { it.records.isNotEmpty() }
            sender.sendToMain("分享上下文", "shared-id")
            val request = InboxPersonaRequest("item", InboxAiAction.Handoff, "handoff-id", "<external>参考</external>")
            failWrite = true
            try { repository.askPersona(request); fail("Expected storage failure") } catch (_: IOException) { }
            assertEquals(listOf("shared-id"), store.entries.value.map { it.clientMessageId })
            failWrite = false
            repository.askPersona(request)
            repository.askPersona(request)
            repository.askPersona(InboxPersonaRequest("item", InboxAiAction.Summarize, "summary-id"))
            repository.askPersona(InboxPersonaRequest("item", InboxAiAction.DraftReply, "draft-id"))
            runCurrent()
            assertEquals(listOf("shared-id", "handoff-id", "summary-id", "draft-id"), store.entries.value.map { it.clientMessageId })
            assertTrue(store.entries.value.all { it.mainTarget && it.session == PendingMainConversation })
            assertTrue(store.entries.value.single { it.clientMessageId == "handoff-id" }.text.contains("&lt;external&gt;参考&lt;/external&gt;"))
            assertTrue(store.entries.value.single { it.clientMessageId == "summary-id" }.text.contains("已加载内容"))
            assertTrue(store.entries.value.all { it.state == OutboxState.WaitingForNetwork })
        } finally {
            sender.close()
            backgroundScope.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun mailUnknownUsesReadOnlyReceiptAndKeepsOriginalClientId() = runTest {
        val directory = Files.createTempDirectory("inbox-receipt-test").toFile()
        val transport = FakeTransport()
        val journal = InboxReplyJournal(directory, transport.scope)
        val target = ReplyTarget.Inbox("mail-item")
        val request = ReplyRequest("回复", "original-id")
        var confirmed = false
        val lookups = mutableListOf<String>()
        val connection = object : InboxAssistantConnection {
            override val current = MutableStateFlow<InboxAssistantTransport?>(transport)
        }
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        try {
            val repository = RealInboxRepository(connection, journal, sessions, interactions, backgroundScope,
                lookupReply = { actual, id ->
                    assertEquals(target, actual)
                    lookups += id
                    InboxReplyReceipt(if (confirmed) OutboxState.Sent else OutboxState.Unknown)
                }) { _, _ -> error("Unused") }
            runCurrent()
            repository.saveDraft(target, ReplyDraft(request.text, request.clientId))
            var sends = 0
            transport.onReply = { sends++; throw IOException("Receipt lost") }
            try { repository.reply(target, request); fail("Expected unknown outcome") } catch (_: IOException) { }
            repository.reconcilePendingReplies()
            assertEquals(ReplyDelivery.Unknown, repository.verifyReply(target, request.clientId))
            try { repository.reply(target, request); fail("Must not resend unknown") } catch (_: IOException) { }
            assertEquals(1, sends)
            assertEquals(request.clientId, repository.readDraft(target)?.clientId)
            confirmed = true
            assertEquals(ReplyDelivery.Sent, repository.verifyReply(target, request.clientId))
            assertEquals(1, sends)
            assertTrue(lookups.all { it == request.clientId })
            assertNull(repository.readDraft(target))
        } finally {
            backgroundScope.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun persistsBeforeSendingRetainsRetryIdAndDoesNotClearNewerDraft() = runTest {
        val directory = Files.createTempDirectory("inbox-test").toFile()
        try {
            val journal = InboxReplyJournal(directory, "hub/generation")
            val transport = FakeTransport()
            val connection = object : InboxAssistantConnection {
                override val current = MutableStateFlow<InboxAssistantTransport?>(transport)
            }
            val sessions = object : SessionsRepository {
                override val machines = MutableStateFlow<List<Machine>>(emptyList())
                override val sessions = MutableStateFlow<List<Session>>(emptyList())
                override suspend fun refresh() = Unit
            }
            val interactions = object : InteractionCenter {
                override val pending = MutableStateFlow<List<Interaction>>(emptyList())
                override suspend fun refresh() = Unit
                override suspend fun resolve(id: String, resolution: Resolution) = Unit
            }
            val repository = RealInboxRepository(connection, journal, sessions, interactions, backgroundScope,
                classifyReplyFailure = { OutboxState.WaitingForNetwork }) { _, _ -> error("unused") }
            runCurrent()
            val target = ReplyTarget.Lark("feed")
            val request = ReplyRequest("回复", "reply-id")
            repository.saveDraft(target, ReplyDraft(request.text, request.clientId))
            var attempts = 0
            transport.onReply = { id ->
                assertEquals(request.clientId, id)
                assertEquals(request, InboxReplyJournal(directory, "hub/generation").read(transport.scope).outbox.single().request)
                if (attempts++ == 0) throw IOException("offline")
                repository.saveDraft(target, ReplyDraft("后续草稿", "new-id"))
                buildJsonObject { put("id", "server-id") }
            }
            try { repository.reply(target, request); fail("Expected network failure") } catch (_: IOException) { }
            assertEquals("reply-id", repository.readDraft(target)?.clientId)
            repository.reply(target, request)
            assertEquals("new-id", repository.readDraft(target)?.clientId)
            repository.reply(target, request)
            assertEquals(2, attempts)
            assertEquals("sent", journal.read(transport.scope).outbox.single().state)
        } finally {
            backgroundScope.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    // #4687: a sent reply stays on screen until Feishu's copy is seen, then never comes back at the tail.
    @Test fun sentLarkReplyLeavesTheOutboxOnceItsCopyIsSeen() = runTest {
        val directory = Files.createTempDirectory("inbox-outbox-test").toFile()
        try {
            val transport = FakeTransport()
            val connection = object : InboxAssistantConnection {
                override val current = MutableStateFlow<InboxAssistantTransport?>(transport)
            }
            val sessions = object : SessionsRepository {
                override val machines = MutableStateFlow<List<Machine>>(emptyList())
                override val sessions = MutableStateFlow<List<Session>>(emptyList())
                override suspend fun refresh() = Unit
            }
            val interactions = object : InteractionCenter {
                override val pending = MutableStateFlow<List<Interaction>>(emptyList())
                override suspend fun refresh() = Unit
                override suspend fun resolve(id: String, resolution: Resolution) = Unit
            }
            val repository = RealInboxRepository(connection, InboxReplyJournal(directory, "hub/generation"), sessions,
                interactions, backgroundScope) { _, _ -> error("unused") }
            runCurrent()
            transport.onReply = { buildJsonObject { put("ok", true); put("message_id", "m1") } }
            repository.reply(ReplyTarget.Lark("feed"), ReplyRequest("ok", "c1"))
            assertEquals(listOf("m1"), repository.larkOutbox("feed").first().map { it.id })
            repository.acceptLarkWindow("feed", listOf(row("m1", 10, "ok")))
            assertEquals(emptyList<String>(), repository.larkOutbox("feed").first().map { it.id })
            // A later window without the copy (reopened at the first new message) must not bring it back.
            repository.acceptLarkWindow("feed", (200L..249L).map { row("n$it", it, "later") })
            assertEquals(emptyList<String>(), repository.larkOutbox("feed").first().map { it.id })
        } finally {
            backgroundScope.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    // #4687: opened past the reply (start_position after m1), the tail never holds the copy, so the hub confirms it.
    @Test fun sentLarkReplyIsConfirmedOnTheHubWhenTheWindowLacksItsCopy() = runTest {
        val directory = Files.createTempDirectory("inbox-outbox-test").toFile()
        try {
            val transport = FakeTransport()
            val repository = larkRepository(transport, InboxReplyJournal(directory, "hub/generation"))
            runCurrent()
            transport.onReply = { buildJsonObject { put("ok", true); put("message_id", "m1") } }
            repository.reply(ReplyTarget.Lark("feed"), ReplyRequest("ok", "c1"))
            val later = (200L..249L).map { row("n$it", it, "later") }
            // A copy the hub still holds as pending is not delivery.
            transport.onLarkMessage = { id -> buildJsonObject { put("message", buildJsonObject {
                put("id", id); put("position", 10); put("local_state", "pending") }) } }
            repository.acceptLarkWindow("feed", later)
            assertEquals(listOf("m1"), repository.larkOutbox("feed").first().map { it.id })
            transport.onLarkMessage = { id -> buildJsonObject { put("message", row(id, 10, "ok")) } }
            repository.acceptLarkWindow("feed", later)
            assertEquals(emptyList<String>(), repository.larkOutbox("feed").first().map { it.id })
            assertEquals(listOf("m1", "m1"), transport.lookedUp)
        } finally {
            backgroundScope.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    // #4687: replies confirmed before this build were saved without `seen`; they leave the tail once the hub confirms them.
    @Test fun sentLarkReplyFromAnOlderJournalLeavesTheOutbox() = runTest {
        val directory = Files.createTempDirectory("inbox-outbox-test").toFile()
        try {
            val old = JsonCodec.encodeToJsonElement(InboxReplyDocument(outbox = listOf(InboxPendingReply(
                ReplyTarget.Lark("feed"), ReplyRequest("ok", "c1"), "feed", state = "sent", serverId = "m1")))).jsonObject
            val legacy = JsonObject(old + ("outbox" to JsonArray(old.getValue("outbox").jsonArray.map {
                JsonObject(it.jsonObject - "seen") })))
            File(directory, "replies.json").writeText(legacy.toString())
            val transport = FakeTransport()
            transport.onLarkMessage = { id -> buildJsonObject { put("message", row(id, 10, "ok")) } }
            val repository = larkRepository(transport, InboxReplyJournal(directory, "hub/generation"))
            assertEquals(listOf("m1"), repository.larkOutbox("feed").first { it.isNotEmpty() }.map { it.id })
            repository.acceptLarkWindow("feed", (200L..249L).map { row("n$it", it, "later") })
            assertEquals(emptyList<String>(), repository.larkOutbox("feed").first().map { it.id })
        } finally {
            backgroundScope.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    private fun TestScope.larkRepository(transport: FakeTransport, journal: InboxReplyJournal): RealInboxRepository {
        val connection = object : InboxAssistantConnection {
            override val current = MutableStateFlow<InboxAssistantTransport?>(transport)
        }
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        return RealInboxRepository(connection, journal, sessions, interactions, backgroundScope) { _, _ -> error("unused") }
    }

    @Test fun reconnectRevalidatesSparseRangeAndStalledCursorReplacesLatest() = runBlocking {
        val directory = Files.createTempDirectory("inbox-window-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val hub = FakeHub()
        val transport = FakeTransport()
        val snapshots = ListSnapshotStore(directory.resolve("snapshots"))
        try {
            val repository = repository(transport, directory, owner, hub, snapshots)
            transport.onMessages = { around, _, after ->
                assertEquals(10L, around)
                assertNull(after)
                page(row("first", 10, "原消息"), row("deleted", 20, "已删除"))
            }
            val observed = MutableStateFlow(LarkConversation())
            owner.launch { repository.conversation("feed", 10).collect { observed.value = it } }
            awaitState(observed) { it.verified && it.messages.size == 2 }
            hub.state.value = ConnectionState.WaitingForNetwork
            awaitState(observed) { !it.verified }
            transport.onMessages = { around, _, after -> when (after) {
                null -> { assertEquals(10L, around); page(row("first", 10, "编辑后"), newer = "10") }
                "10" -> page(row("middle", 30, "中间消息"), newer = "30")
                "30" -> page(row("last", 90, "末尾消息"))
                else -> error("Unexpected cursor")
            } }
            hub.state.value = ConnectionState.Online
            val reconciled = awaitState(observed) { it.verified && it.messages.lastOrNull()?.id == "last" }
            assertEquals(listOf("first", "middle", "last"), reconciled.messages.map { it.id })
            assertEquals("编辑后", (reconciled.messages.first().parts.single() as LarkPart.Text).text)
            assertFalse(reconciled.hasNewer)

            withTimeout(5_000) {
                while (snapshots.restore<JsonObject>("${transport.scope}:lark:feed:10")?.array("messages")?.size != 3) delay(1)
            }
            val restoredOwner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
            try {
                val offlineHub = FakeHub().apply { state.value = ConnectionState.WaitingForNetwork }
                val restored = repository(transport, directory, restoredOwner, offlineHub, snapshots)
                val cached = withTimeout(5_000) { restored.conversation("feed", 10).first { it.messages.size == 3 } }
                assertFalse(cached.verified)
                assertEquals(listOf("first", "middle", "last"), cached.messages.map { it.id })
            } finally { restoredOwner.coroutineContext[Job]!!.cancelAndJoin() }

            transport.onMessages = { around, _, after ->
                if (around == null && after == null) page(row("latest", 500, "最新消息"))
                else page(row("first", 10, "编辑后"), newer = "10")
            }
            hub.events.emit(LarkEvent(listOf("feed")))
            val latest = awaitState(observed) { it.verified && it.windowRevision == 1L }
            assertEquals(listOf("latest"), latest.messages.map { it.id })
            assertTrue(latest.error!!.contains("最新"))
            transport.onMessages = { _, _, _ -> page(row("latest", 500, "不完整的编辑"), sync = "local") }
            hub.events.emit(InboxEvent())
            val incomplete = awaitState(observed) { it.error == "消息记录尚未同步完整" }
            assertFalse(incomplete.verified)
            assertEquals("最新消息", (incomplete.messages.single().parts.single() as LarkPart.Text).text)
            assertEquals(LarkWindowSyncState.Incomplete, repository.syncStates.value["feed"])
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun restartRecoversReplyWithSameIdAndUnknownReceiptNeedsEcho() = runBlocking {
        val directory = Files.createTempDirectory("inbox-recovery-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val transport = FakeTransport()
        val hub = FakeHub().apply { state.value = ConnectionState.WaitingForNetwork }
        val target = ReplyTarget.Lark("feed")
        val request = ReplyRequest("回复内容", "stable-client-id")
        val journal = InboxReplyJournal(directory, "hub/generation")
        try {
            journal.update(transport.scope) { InboxReplyDocument(
                drafts = mapOf(target.storageKey() to InboxStoredDraft(request.text, request.clientId)),
                outbox = listOf(InboxPendingReply(target, request, "feed")),
            ) }
            val attempts = java.util.concurrent.atomic.AtomicInteger()
            transport.onReply = { id ->
                assertEquals(request.clientId, id)
                attempts.incrementAndGet()
                buildJsonObject { put("state", "unknown") }
            }
            val repository = repository(transport, directory, owner, hub)
            val observed = MutableStateFlow(LarkConversation())
            owner.launch { repository.conversation("feed", null).collect { observed.value = it } }
            withTimeout(5_000) { while (repository.replyStatus(target, request.clientId) != ReplyDelivery.Unknown) delay(1) }
            hub.state.value = ConnectionState.Online
            withTimeout(5_000) {
                while (repository.replyStatus(target, request.clientId) != ReplyDelivery.Unknown) delay(1)
            }
            awaitState(observed) { it.messages.any { message -> message.clientId == request.clientId && message.delivery == LarkDelivery.Failed && message.pendingDelivery == DeliveryState.Unknown } }
            assertEquals(0, attempts.get())
            assertEquals(ReplyDelivery.Unknown, repository.verifyReply(target, request.clientId))
            assertEquals(request.clientId, repository.readDraft(target)?.clientId)
            repository.saveDraft(target, ReplyDraft("后续草稿", "new-client-id"))
            transport.onMessages = { _, _, _ -> page(row("echo", 50, request.text, request.clientId)) }
            hub.events.emit(LarkEvent(listOf("feed")))
            awaitState(observed) { it.verified && it.messages.any { message -> message.id == "echo" } }
            assertEquals(ReplyDelivery.Sent, repository.replyStatus(target, request.clientId))
            assertEquals("new-client-id", repository.readDraft(target)?.clientId)
            assertEquals(0, attempts.get())
            assertEquals(1, observed.value.messages.count { it.clientId == request.clientId })
            journal.clear(transport.scope)
            assertEquals(InboxReplyDocument(), journal.read(transport.scope))
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun offlineAiAndLarkRepliesShareQueueAndRestartReconcilesLostReceipt() = runBlocking {
        val directory = Files.createTempDirectory("inbox-offline-test").toFile()
        var owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val transport = FakeTransport()
        val hub = FakeHub().apply { state.value = ConnectionState.WaitingForNetwork }
        val snapshots = ListSnapshotStore(directory.resolve("snapshots"))
        val ai = ReplyTarget.Inbox("item")
        val lark = ReplyTarget.Lark("feed")
        val aiRequest = ReplyRequest("同样的回复", "ai-reply")
        val larkRequest = ReplyRequest("同样的回复", "lark-reply")
        val calls = java.util.concurrent.CopyOnWriteArrayList<String>()
        val echoed = java.util.concurrent.atomic.AtomicBoolean(false)
        try {
            snapshots.save("${transport.scope}:inbox", buildJsonObject {
                put("items", buildJsonArray { add(buildJsonObject {
                    put("id", "item"); put("lark", buildJsonObject { put("feed_id", "feed") })
                }) })
            })
            var repository = repository(transport, directory, owner, hub, snapshots)
            withTimeout(5_000) { repository.snapshot.first { it.records.isNotEmpty() } }
            for ((target, request) in listOf(ai to aiRequest, lark to larkRequest)) {
                repository.saveDraft(target, ReplyDraft(request.text, request.clientId))
                try { repository.reply(target, request); fail("Expected queued delivery") } catch (_: IOException) { }
            }
            repository.saveDraft(ai, ReplyDraft("后续草稿", "new-draft"))
            val journal = InboxReplyJournal(directory, transport.scope)
            assertEquals(listOf("ai-reply", "lark-reply"), journal.read(transport.scope).outbox.map { it.request.clientId })
            assertTrue(journal.read(transport.scope).outbox.all { it.outboxState == OutboxState.WaitingForNetwork && it.feedId == "feed" })
            owner.coroutineContext[Job]!!.cancelAndJoin()
            transport.onMessages = { _, _, _ ->
                if (echoed.get()) page(row("ai-server", 20, aiRequest.text, aiRequest.clientId))
                else page(row("unrelated", 10, aiRequest.text, "different-id"))
            }
            transport.onReply = { id ->
                assertTrue(journal.read(transport.scope).outbox.any { it.request.clientId == id })
                calls += id
                if (id == aiRequest.clientId) {
                    echoed.set(true)
                    throw IOException("Receipt lost")
                }
                buildJsonObject { put("id", "lark-server") }
            }
            owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
            hub.state.value = ConnectionState.Online
            repository = repository(transport, directory, owner, hub, snapshots)
            withTimeout(5_000) {
                while (repository.replyStatus(lark, larkRequest.clientId) != ReplyDelivery.Sent) delay(1)
            }
            // The second queued reply also fetches echoes, including the first reply's lost receipt.
            assertEquals(ReplyDelivery.Sent, repository.replyStatus(ai, aiRequest.clientId))
            coroutineScope {
                launch { repository.verifyReply(ai, aiRequest.clientId) }
                launch { repository.reply(lark, larkRequest) }
            }
            assertEquals(listOf("ai-reply", "lark-reply"), calls.toList())
            assertEquals("new-draft", repository.readDraft(ai)?.clientId)
            assertNull(repository.readDraft(lark))
            assertTrue(journal.read(transport.scope).outbox.all { it.state == "sent" })
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun aBotsRowReadsItsFullUrlPictureAsAnAvatarOnceAndKeyedByPairing() = runBlocking {
        val directory = Files.createTempDirectory("inbox-bot-avatar-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(
                    buildJsonObject { put("id", "lark:bot:1"); put("source", "lark"); put("kind", "dm"); put("title", "Bot")
                        put("avatar", "https://cdn.example.com/avatar/bot.jpg"); put("lark", buildJsonObject { put("feed_id", "bot") }) },
                ))
            }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            val avatar = repository.snapshot.value.records.single().avatar!!
            assertEquals("hub/generation:https://cdn.example.com/avatar/bot.jpg", avatar.cacheKey)
            avatar.read()
            avatar.read()
            // Read once as an avatar (the hub transport reads a full https URL directly); the second is cached.
            assertEquals(listOf("https://cdn.example.com/avatar/bot.jpg"), transport.avatarRefs)
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun feishuRowsCarryTheChatsPictureAtThumbnailSizeAndMailRowsDoNot() = runBlocking {
        val directory = Files.createTempDirectory("inbox-avatar-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(
                    buildJsonObject { put("id", "lark:chat:1"); put("source", "lark"); put("kind", "group"); put("title", "群")
                        put("avatar", "/api/v1/lark/media/key"); put("lark", buildJsonObject { put("feed_id", "chat") }) },
                    buildJsonObject { put("id", "lark:dm:1"); put("source", "lark"); put("kind", "dm"); put("title", "人")
                        put("lark", buildJsonObject { put("feed_id", "dm") }) },
                    buildJsonObject { put("id", "mail:1"); put("source", "mail"); put("kind", "mail"); put("title", "信")
                        put("avatar", "/api/v1/lark/media/other") },
                ))
            }
            val repository = repository(transport, directory, owner, FakeHub())
            // The pairing's first emission clears the list; refresh after it, not before.
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            val avatars = repository.snapshot.value.records.associate { it.item.id to it.avatar }
            assertEquals("hub/generation:/api/v1/lark/media/key?size=144", avatars["lark:chat:1"]?.cacheKey)
            assertNull(avatars["lark:dm:1"])
            assertNull(avatars["mail:1"])
            avatars["lark:chat:1"]!!.read()
            avatars["lark:chat:1"]!!.read()
            // Read with the pairing at the thumbnail size, and once: the second comes from the bytes cache.
            assertEquals(listOf("/api/v1/lark/media/key?size=144"), transport.mediaRefs)
            assertEquals(transport.mediaRefs, transport.avatarRefs)
            // A refresh rebuilds the records; the same picture is the same value, so unchanged rows stay equal.
            val before = repository.snapshot.value.records
            repository.refresh()
            assertEquals(before, repository.snapshot.value.records)
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun bulkDoneKeepsWhatWasAlreadyDoneElsewhereAndDropsWhatIsGone() = runBlocking {
        val directory = Files.createTempDirectory("inbox-bulk-unchanged-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(id: String, doneAt: String? = null) = buildJsonObject {
                put("id", id); put("source", "mail"); put("kind", "mail"); put("title", id); put("version", "v$id")
                if (doneAt != null) put("done_at", doneAt)
            }
            val transport = FakeTransport().apply { items = JsonArray(listOf(item("a"), item("b"), item("c"))) }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            // Before the answer: b was filed on another device, c is gone from the hub. The refresh after the
            // answer fails (offline), so what the list shows is what bulk() itself made of the answer.
            transport.onBulk = { _, _ ->
                transport.onInbox = { throw IOException("offline") }
                buildJsonObject {
                    put("changed", JsonArray(listOf(item("a", "2026-11-06T08:00:00Z"))))
                    put("unchanged", JsonArray(listOf(JsonPrimitive("b"))))
                    put("skipped", JsonArray(listOf(buildJsonObject { put("id", "c"); put("reason", "missing") })))
                }
            }
            val calls = transport.inboxCalls.get()
            val result = repository.bulk(listOf(InboxDoneTarget("a", "va"), InboxDoneTarget("b", "vb"), InboxDoneTarget("c", "vc")), InboxBulkAction.Done)
            assertEquals(listOf("a"), result.changed)
            // Nothing took new messages, yet the hub's own state for b and c is asked for once.
            assertEquals(calls + 1, transport.inboxCalls.get())
            // Already done elsewhere stays done; one the hub no longer has leaves the list; neither comes back open.
            assertEquals(mapOf("a" to true, "b" to true), repository.snapshot.value.records.associate { it.item.id to it.item.isDone })
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    /** A merged list (several managers): each item keeps its manager, and its own requests will name it. */
    @Test fun anotherManagersItemsKeepTheirManager() = runBlocking {
        val directory = Files.createTempDirectory("inbox-managers-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(id: String, manager: String?) = buildJsonObject {
                put("id", id); put("source", "mail"); put("kind", "mail"); put("title", id)
                if (manager != null) put("manager", manager)
            }
            AssistantManagerChoice.choose("hub", null, "assistant")
            val transport = FakeTransport().apply { items = JsonArray(listOf(item("a", null), item("b", "m-3"), item("c", "assistant"))) }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            // another manager's item has an app id of its own (InboxItemKey); the hub's id is kept for its requests
            assertEquals(mapOf("a" to null, InboxItemKey.of("b", "m-3") to "m-3", "c" to "assistant"),
                repository.snapshot.value.records.associate { it.item.id to it.manager })
            assertEquals(listOf("a", "b", "c"), repository.snapshot.value.records.map { it.hubItemId })
            assertEquals("m-3", AssistantManagerChoice.itemHeader(listOf("inbox", "b", "done")))
            assertNull(AssistantManagerChoice.itemHeader(listOf("inbox", "a", "done")))
            assertNull(AssistantManagerChoice.itemHeader(listOf("inbox", "c")))
        } finally {
            AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems()
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    /** Bulk actions over several managers' items: one request per manager, each naming its own, the answers put together. */
    @Test fun bulkGoesToEachItemsManager() = runBlocking {
        val directory = Files.createTempDirectory("inbox-managers-bulk-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(id: String, manager: String?, done: Boolean = false) = buildJsonObject {
                put("id", id); put("source", "mail"); put("kind", "mail"); put("title", id); put("version", "v$id")
                if (manager != null) put("manager", manager)
                if (done) { put("done_at", "2026-11-04T08:00:00Z"); put("done_by", "user") }
            }
            AssistantManagerChoice.choose("hub", null, "assistant")
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(item("a", null), item("b", "m-3"), item("c", "assistant"), item("d", "m-3")))
                // each manager answers about its own items alone, unstamped (only the merged list stamps)
                onBulk = { asked, _ -> buildJsonObject { put("changed", JsonArray(asked.map { item(it.id, null, done = true) })) } }
            }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            transport.items = JsonArray(listOf("a", "b", "c", "d").map { item(it, null, done = true) })

            val targets = repository.snapshot.value.records.map { InboxDoneTarget(it.item.id, "v${it.hubItemId}") }
            val result = repository.bulk(targets, InboxBulkAction.Done)
            // named on the request itself, by the hub's ids ("" the default's: no manager, whatever a list noted)
            assertEquals(setOf(listOf("a", "c") to "", listOf("b", "d") to "m-3"), transport.bulkManagers.toSet())
            assertEquals(2, transport.bulkManagers.size)
            assertEquals(targets.map { it.id }.toSet(), result.changed.toSet())
            assertTrue(repository.snapshot.value.records.all { it.item.isDone })
            // they keep their stamps and ids: their groups, and where Undo goes
            assertEquals(mapOf("a" to null, "b" to "m-3", "c" to "assistant", "d" to "m-3"),
                repository.snapshot.value.records.associate { it.hubItemId to it.manager })
            assertEquals(targets.map { it.id }, repository.snapshot.value.records.map { it.item.id })
        } finally {
            AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems()
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    /** One manager failing leaves the others' done: its own items go back and are the result's `failed`. */
    @Test fun bulkKeepsWhatTheOtherManagersDid() = runBlocking {
        val directory = Files.createTempDirectory("inbox-managers-bulk-partial-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(id: String, manager: String?, done: Boolean = false) = buildJsonObject {
                put("id", id); put("source", "mail"); put("kind", "mail"); put("title", id); put("version", "v$id")
                if (manager != null) put("manager", manager)
                if (done) { put("done_at", "2026-11-04T08:00:00Z"); put("done_by", "user") }
            }
            AssistantManagerChoice.choose("hub", null, "assistant")
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(item("a", "assistant"), item("b", "m-3")))
                onBulk = { asked, _ ->
                    if (asked.any { it.id == "b" }) throw IOException("503 assistant offline")
                    buildJsonObject { put("changed", JsonArray(asked.map { item(it.id, null, done = true) })) }
                }
            }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            transport.onInbox = { throw IOException("offline") } // what bulk() itself made of it stays shown

            val b = InboxItemKey.of("b", "m-3")
            val result = repository.bulk(listOf(InboxDoneTarget("a", "va"), InboxDoneTarget(b, "vb")), InboxBulkAction.Done)
            assertEquals(listOf("a"), result.changed)
            assertEquals(listOf(b), result.failed)
            val shown = repository.snapshot.value.records.associate { it.item.id to (it.item.isDone to it.manager) }
            assertEquals(mapOf("a" to (true to "assistant"), b to (false to "m-3")), shown)
            // every manager failing fails the whole action, and everything goes back
            transport.onBulk = { _, _ -> throw IOException("offline") }
            assertThrows(IOException::class.java) { runBlocking { repository.bulk(listOf(InboxDoneTarget(b, "vb")), InboxBulkAction.Done) } }
            assertFalse(repository.snapshot.value.records.first { it.item.id == b }.item.isDone)
        } finally {
            AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems()
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    /**
     * Two managers with an item of the same id (the same source read by both): two records with ids of their own, each
     * one's requests going to its own manager by the hub's id, and an unstamped answer keeping the record's id and stamp.
     */
    @Test fun theSameIdUnderTwoManagersIsTwoItems() = runBlocking {
        val directory = Files.createTempDirectory("inbox-managers-same-id-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(id: String, manager: String?, title: String, done: Boolean = false) = buildJsonObject {
                put("id", id); put("source", "mail"); put("kind", "mail"); put("title", title)
                if (manager != null) put("manager", manager)
                if (done) { put("done_at", "2026-11-04T08:00:00Z"); put("done_by", "user") }
            }
            AssistantManagerChoice.choose("hub", null, "assistant")
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(item("x", "assistant", "mine"), item("x", "m-3", "theirs")))
                onSetDone = { id, done -> buildJsonObject { put("item", item(id, null, "theirs", done)) } }
            }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            val records = repository.snapshot.value.records
            assertEquals(2, records.map { it.item.id }.toSet().size)
            assertEquals(listOf("x", "x"), records.map { it.hubItemId })
            val theirs = records.first { it.manager == "m-3" }
            assertEquals(InboxItemKey.of("x", "m-3"), theirs.item.id)
            // the ids don't depend on the list's order: the other manager's copy listed first keeps its own
            transport.items = JsonArray(listOf(item("x", "m-3", "theirs"), item("x", "assistant", "mine")))
            repository.refresh()
            assertEquals(listOf(theirs.item.id, "x"), repository.snapshot.value.records.map { it.item.id })
            transport.items = JsonArray(listOf(item("x", "assistant", "mine"), item("x", "m-3", "theirs")))
            repository.refresh()

            transport.onInbox = { throw IOException("offline") }
            repository.setDone(theirs.item.id, true)
            assertEquals(listOf("x" to "m-3"), transport.doneCalls)
            val after = repository.snapshot.value.records
            assertEquals(listOf(false, true), after.map { it.item.isDone })
            assertEquals(listOf("assistant", "m-3"), after.map { it.manager })
            assertEquals(theirs.item.id, after[1].item.id)
            // the default's one names no manager on its request
            repository.setDone("x", true)
            assertEquals("x" to "", transport.doneCalls.last())
        } finally {
            AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems()
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    /**
     * A chat's send (ReplyTarget.Lark) names the manager of the one item of that chat; with none known it names nothing,
     * so the chat page's pin still decides (an empty manager would send it to the default).
     */
    @Test fun aChatSendNamesItsItemsManagerOrNothing() = runBlocking {
        val directory = Files.createTempDirectory("inbox-managers-lark-send-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            AssistantManagerChoice.choose("hub", null, "assistant")
            val named = mutableListOf<String?>()
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(buildJsonObject {
                    put("id", "lark:f1"); put("source", "lark"); put("kind", "dm"); put("title", "f1"); put("manager", "m-3")
                    putJsonObject("lark") { put("feed_id", "f1") }
                }))
                onReply = { named += currentCoroutineContext()[RequestManager]?.id; buildJsonObject { put("message_id", "om_1") } }
            }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            repository.reply(ReplyTarget.Lark("f1"), ReplyRequest("你好", "c1"))
            repository.reply(ReplyTarget.Lark("f2"), ReplyRequest("你好", "c2"))
            assertEquals(listOf("m-3", null), named)
        } finally {
            AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems()
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun bulkPutsBackWhatTheHubFailedToWrite() = runBlocking {
        val directory = Files.createTempDirectory("inbox-bulk-failed-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(id: String, doneAt: String? = null) = buildJsonObject {
                put("id", id); put("source", "mail"); put("kind", "mail"); put("title", id); put("version", "v$id")
                if (doneAt != null) put("done_at", doneAt)
            }
            val transport = FakeTransport().apply { items = JsonArray(listOf(item("a"), item("b", "2026-11-06T07:00:00Z"))) }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            // The hub's store write failed for both: they were never filed (or reopened), and the refresh after it fails
            // (offline), so what the list shows is what bulk() itself made of the answer.
            transport.onBulk = { _, _ ->
                transport.onInbox = { throw IOException("offline") }
                buildJsonObject {
                    put("changed", JsonArray(emptyList())); put("unchanged", JsonArray(emptyList()))
                    put("skipped", JsonArray(emptyList())); put("failed", JsonArray(listOf(JsonPrimitive("a"), JsonPrimitive("b"))))
                }
            }
            val calls = transport.inboxCalls.get()
            repository.bulk(listOf(InboxDoneTarget("a", "va")), InboxBulkAction.Done)
            assertNull(repository.snapshot.value.records.first { it.item.id == "a" }.item.doneAt)
            // The hub's own state is asked for, as for the other ids it did not move.
            assertEquals(calls + 1, transport.inboxCalls.get())
            repository.bulk(listOf(InboxDoneTarget("b")), InboxBulkAction.Reopen)
            assertEquals("2026-11-06T07:00:00Z", repository.snapshot.value.records.first { it.item.id == "b" }.item.doneAt)
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun bulkDoneIsOneRequestWithVersionsAndPutsBackWhatTheHubDidNotFile() = runBlocking {
        val directory = Files.createTempDirectory("inbox-bulk-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(id: String, version: String) = buildJsonObject {
                put("id", id); put("source", "mail"); put("kind", "mail"); put("title", id); put("version", version)
            }
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(item("a", "va"), item("b", "vb"), item("c", "vc")))
                onBulk = { _, _ ->
                    // As the hub: a is filed; b took a new mail (its version moved), so it is not.
                    val filedA = buildJsonObject {
                        put("id", "a"); put("source", "mail"); put("kind", "mail"); put("title", "a"); put("version", "va")
                        put("done_at", "2026-11-04T08:00:00Z"); put("done_by", "user")
                    }
                    items = JsonArray(listOf(filedA, item("b", "vb2"), item("c", "vc")))
                    buildJsonObject {
                    put("changed", JsonArray(listOf(filedA)))
                    put("unchanged", JsonArray(emptyList()))
                    put("skipped", JsonArray(listOf(buildJsonObject { put("id", "b"); put("reason", "changed") })))
                } }
            }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            val listed = repository.snapshot.value.records.associate { it.item.id to it.item.version }
            assertEquals(mapOf("a" to "va", "b" to "vb", "c" to "vc"), listed)

            val result = repository.setDone(listOf(InboxDoneTarget("a", "va"), InboxDoneTarget("b", "vb")), true)
            // One request carrying each version; b took new messages since and stays open; c was not asked for.
            assertEquals(listOf(listOf(InboxDoneTarget("a", "va"), InboxDoneTarget("b", "vb")) to true), transport.bulk)
            assertEquals(listOf("a"), result.changed)
            assertEquals(listOf("b"), result.changedSince)
            // Refreshed for b's new mail: its new version is what the next bulk sends.
            val after = repository.snapshot.value.records.associate { it.item.id to (it.item.isDone to it.item.version) }
            assertEquals(mapOf("a" to (true to "va"), "b" to (false to "vb2"), "c" to (false to "vc")), after)
            // A fresh server read must retain the accepted filing, not resurrect the optimistic row.
            repository.refresh()
            assertEquals(after, repository.snapshot.value.records.associate { it.item.id to (it.item.isDone to it.item.version) })

            assertEquals(listOf(InboxBulkAction.Done), transport.actions)
            // Clean up all is the same request with action dismiss; c is filed.
            transport.onBulk = { _, _ -> buildJsonObject {
                put("changed", JsonArray(listOf(buildJsonObject {
                    put("id", "c"); put("source", "mail"); put("kind", "mail"); put("title", "c"); put("version", "vc")
                    put("done_at", "2026-11-04T08:00:00Z"); put("done_by", "dismissed")
                })))
            } }
            assertEquals(listOf("c"), repository.bulk(listOf(InboxDoneTarget("c", "vc")), InboxBulkAction.Dismiss).changed)
            assertEquals(InboxBulkAction.Dismiss, transport.actions.last())
            assertTrue(repository.snapshot.value.records.first { it.item.id == "c" }.item.isDone)
            // "Undo" is reopen.
            transport.onBulk = { _, _ -> buildJsonObject { put("changed", JsonArray(listOf(item("c", "vc")))) } }
            repository.bulk(listOf(InboxDoneTarget("c")), InboxBulkAction.Reopen)
            assertEquals(InboxBulkAction.Reopen, transport.actions.last())

            // A failure puts every item back.
            transport.onBulk = { _, _ -> throw IOException("offline") }
            assertThrows(IOException::class.java) { runBlocking { repository.setDone(listOf(InboxDoneTarget("c", "vc")), true) } }
            assertFalse(repository.snapshot.value.records.first { it.item.id == "c" }.item.isDone)
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    // Inbox phase 1: an item's event fields are read with it, and answering its suggestion puts the item the hub answers
    // with in the list, also when the suggestion was stale (409 suggestion_stale carries the current item).
    @Test fun aSuggestionAnswerRedrawsTheItemTheHubAnswersWith() = runBlocking {
        val directory = Files.createTempDirectory("inbox-suggestion-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(version: Int, status: String, state: String) = buildJsonObject {
                put("id", "lark:c:1"); put("source", "lark"); put("kind", "dm"); put("title", "示例"); put("tier", "action")
                put("status", status); put("urgent", true); put("recap", "示例总结")
                putJsonObject("lark") { put("feed_id", "c"); put("start_position", "1") }
                putJsonObject("handling") { put("owner", "manager"); put("state", state) }
                if (status == "action") putJsonObject("suggestion") { put("version", version); put("kind", "reply"); put("text", "好的 $version") }
                putJsonArray("chain") {
                    add(buildJsonObject { put("id", "c1"); put("kind", "received"); put("text", "收到 1 条新消息")
                        putJsonObject("link") { put("type", "source"); put("position", "1") } })
                    add(buildJsonObject { put("id", "c2"); put("kind", "something_new"); put("text", "以后的动作") })
                }
            }
            val transport = object : InboxAssistantTransport by FakeTransport() {
                val seen = mutableListOf<List<Any?>>()
                var answer: suspend () -> JsonObject = { buildJsonObject { put("item", item(2, "working", "waiting")) } }
                override suspend fun inbox() = buildJsonObject { put("items", JsonArray(listOf(item(2, "action", "done")))) }
                override suspend fun suggestion(itemId: String, version: Long, action: String, text: String?, clientId: String): JsonObject {
                    seen += listOf(itemId, version, action, text, clientId)
                    return answer()
                }
            }
            val repository = RealInboxRepository(
                object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) },
                InboxReplyJournal(directory, "hub/generation"),
                object : SessionsRepository {
                    override val machines = MutableStateFlow<List<Machine>>(emptyList())
                    override val sessions = MutableStateFlow<List<Session>>(emptyList())
                    override suspend fun refresh() = Unit
                },
                object : InteractionCenter {
                    override val pending = MutableStateFlow<List<Interaction>>(emptyList())
                    override suspend fun refresh() = Unit
                    override suspend fun resolve(id: String, resolution: Resolution) = Unit
                }, owner, hubConnection = FakeHub(),
            ) { _, _ -> error("unused") }
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            val listed = repository.snapshot.value.records.single()
            assertEquals("action", listed.item.status)
            assertEquals(true, listed.item.urgent)
            assertEquals("示例总结", listed.event.recap)
            assertEquals(2L, listed.event.suggestion?.version)
            assertEquals(listOf("received", "something_new"), listed.event.chain.map { it.kind })
            assertEquals(1L, listed.event.chain.first().link?.positionValue)

            val request = persona.feature.inbox.SuggestionRequest("lark:c:1", 2, persona.feature.inbox.SuggestionAction.Confirm, "好的，改过")
            assertEquals(persona.feature.inbox.SuggestionOutcome.Done, repository.actOnSuggestion(request))
            assertEquals(listOf("lark:c:1", 2L, "confirm", "好的，改过", request.clientId), transport.seen.single())
            repository.snapshot.value.records.single().let {
                assertEquals("working", it.item.status)
                assertEquals("waiting", it.event.handling?.state)
                assertEquals(null, it.event.suggestion)
            }

            transport.answer = { throw InboxSuggestionStale(item(3, "action", "done")) }
            val stale = persona.feature.inbox.SuggestionRequest("lark:c:1", 2, persona.feature.inbox.SuggestionAction.Reject)
            assertEquals(persona.feature.inbox.SuggestionOutcome.Stale, repository.actOnSuggestion(stale))
            assertEquals(3L, repository.snapshot.value.records.single().event.suggestion?.version)
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun aMuteFilesWhatItCoversAtOnceAndAFailurePutsThemBack() = runBlocking {
        val directory = Files.createTempDirectory("inbox-mute-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(id: String, repo: String) = buildJsonObject {
                put("id", id); put("source", "github"); put("kind", "mention"); put("title", id)
                putJsonArray("mutes") { add(buildJsonObject { put("tag", "repo"); put("value", repo) }) }
            }
            val chat = buildJsonObject {
                put("id", "l"); put("source", "lark"); put("kind", "group"); put("title", "l")
                putJsonObject("lark") { put("feed_id", "f"); put("is_group", true); put("is_topic", true) }
            }
            val transport = FakeTransport().apply { items = JsonArray(listOf(item("a", "o/r"), item("b", "o/r"), item("c", "o/other"), chat)) }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            fun done() = repository.snapshot.value.records.filter { it.item.isDone }.map { it.item.id }.toSet()
            val records = repository.snapshot.value.records.associateBy { it.item.id }
            // A Feishu row carries its chat's kind (read receipts, pinned messages in a topic group).
            assertEquals(true to true, records.getValue("l").isGroup to records.getValue("l").isTopic)
            assertEquals(null, records.getValue("a").isGroup)

            val seen = mutableListOf<Set<String>>()
            transport.onMute = { id, tag -> seen += done(); assertEquals("a" to "repo", id to tag); throw IOException("offline") }
            assertThrows(IOException::class.java) { runBlocking { repository.mute(records.getValue("a"), records.getValue("a").mutes.single()) } }
            // Filed while the request was out, the other repository's item untouched; back after the failure.
            assertEquals(listOf(setOf("a", "b")), seen)
            assertEquals(emptySet<String>(), done())

            transport.onMute = { _, _ -> seen += done(); JsonObject(emptyMap()) }
            // The hub files them too, so the refresh after the mute keeps them done.
            transport.items = JsonArray(transport.items.map { row ->
                val id = (row as JsonObject)["id"]!!.jsonPrimitive.content
                if (id in setOf("a", "b")) JsonObject(row + ("done_at" to JsonPrimitive("2026-11-04T08:00:00Z"))) else row
            })
            repository.mute(records.getValue("b"), records.getValue("b").mutes.single())
            assertEquals(setOf("a", "b"), seen.last())
            assertEquals(setOf("a", "b"), done())
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    @Test fun aRulesTierMovesWhatItCoversAtOnceAndAFailurePutsThemBack() = runBlocking {
        val directory = Files.createTempDirectory("inbox-policy-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            fun item(id: String, repo: String, status: String = "action", manager: String? = null) = buildJsonObject {
                put("id", id); put("source", "github"); put("kind", "mention"); put("title", id)
                put("tier", "action"); put("status", status); put("urgent", true)
                if (manager != null) put("manager", manager)
                putJsonArray("mutes") { add(buildJsonObject { put("tag", "repo"); put("value", repo) }) }
            }
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(item("a", "o/r"), item("b", "o/r", status = "working"), item("c", "o/other"), item("d", "o/r", manager = "m-3")))
            }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            fun sections() = repository.snapshot.value.records.associate { it.hubItemId to "${it.item.tier}/${it.item.status}/${it.item.urgent}" }
            val before = sections()
            val a = repository.snapshot.value.records.single { it.hubItemId == "a" }
            val target = InboxPolicyTarget.of(a, a.mutes.single())

            val seen = mutableListOf<Map<String, String>>()
            transport.onSetPolicy = { _, _, _, _ -> seen += sections(); throw IOException("offline") }
            assertThrows(IOException::class.java) { runBlocking { repository.savePolicy(target, InboxPolicyTier.Noise, InboxPolicyTwin.First) } }
            // Moved while the request was out: not the one being handled, the other repository's, or another manager's.
            assertEquals(before + ("a" to "noise/noise/false"), seen.single())
            assertEquals(before, sections())
            assertEquals(listOf("POST a repo noise first "), transport.policyCalls)

            transport.onSetPolicy = { _, tag, tier, twin -> buildJsonObject {
                putJsonObject("policy") { put("id", "p1"); put("source", "github"); put("tag", tag); put("value", "o/r"); put("tier", tier); put("twin", twin) }
                put("applied", 1)
            } }
            transport.items = JsonArray(transport.items.map { row ->
                if ((row as JsonObject)["id"]!!.jsonPrimitive.content == "a") JsonObject(row + mapOf("tier" to JsonPrimitive("noise"), "status" to JsonPrimitive("noise"),
                    "urgent" to JsonPrimitive(false))) else row
            })
            val saved = repository.savePolicy(target, InboxPolicyTier.Noise, InboxPolicyTwin.First)
            assertEquals(InboxPolicy("p1", "github", tag = "repo", value = "o/r", name = "o/r", tier = InboxPolicyTier.Noise, twin = InboxPolicyTwin.First), saved)
            assertEquals("noise/noise/false", sections()["a"])

            // From "Rules": the rule's id, the default manager's items; clearing both parts removes it.
            transport.onUpdatePolicy = { _, _, _ -> buildJsonObject { put("applied", 0) } }
            assertNull(repository.savePolicy(InboxPolicyTarget.of(checkNotNull(saved)), InboxPolicyTier.None, InboxPolicyTwin.None))
            assertEquals("PATCH p1   null", transport.policyCalls.last())
            assertEquals("noise/noise/false", sections()["a"])

            transport.policies = JsonArray(listOf(buildJsonObject { put("id", "p1"); put("source", "github"); put("tag", "repo"); put("value", "o/r") }))
            assertEquals(listOf("p1"), repository.inboxPolicies().map { it.id })
            val d = repository.snapshot.value.records.single { it.hubItemId == "d" }
            repository.inboxPolicies(d)
            repository.removePolicy("p1")
            assertEquals(listOf("GET null", "GET m-3", "DELETE p1"), transport.policyCalls.takeLast(3))
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    // A Feishu chat that keeps announcing itself unchanged, and a burst of inbox changes, over a shown list on a slow
    // hub: the list never goes back to loading (which inserts a row and pushes it down), Feishu events do not refresh
    // the inbox (the hub sends {"type":"inbox"} when it changes), the burst is one refresh instead of a queue, and an
    // event that comes in during a refresh still gets a refresh after it.
    @Test fun hubEventBurstsOverAShownListRefreshOnceWithoutLoading() = runBlocking<Unit> {
        val directory = Files.createTempDirectory("inbox-burst-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val window = 200L
        try {
            val gate = java.util.concurrent.atomic.AtomicReference<CompletableDeferred<Unit>?>(null)
            val entered = java.util.concurrent.atomic.AtomicReference(CompletableDeferred<Unit>())
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(buildJsonObject { put("id", "lark:chat:1"); put("source", "lark"); put("kind", "group")
                    put("title", "群"); put("lark", buildJsonObject { put("feed_id", "chat") }) }))
                onInbox = { entered.get().complete(Unit); gate.get()?.await() ?: delay(50) }
            }
            val hub = FakeHub()
            val repository = repository(transport, directory, owner, hub, refreshWindowMillis = window)
            // The pairing's first emission clears the list; refresh after it, not before.
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            assertEquals(1, repository.snapshot.value.records.size)
            val loading = java.util.Collections.synchronizedList(mutableListOf<Boolean>())
            owner.launch { repository.snapshot.collect { loading += it.loading } }
            // The page being shown starts the polling fallback with one refresh of its own; let it finish.
            withTimeout(5_000) { repository.snapshot.first { it.refreshes >= 2 } }
            loading.clear()
            suspend fun settled(): Int { delay(window * 4); return transport.inboxCalls.get() }
            val before = settled()

            repeat(6) { hub.events.emit(LarkEvent(listOf("chat"))) }
            assertEquals("Feishu events alone do not refresh the inbox", before, settled())

            repeat(6) { hub.events.emit(InboxEvent()) }
            withTimeout(5_000) { while (transport.inboxCalls.get() == before) delay(10) }
            val refreshes = settled() - before
            assertTrue("a burst of inbox events is merged into one refresh, was $refreshes", refreshes in 1..2)
            assertFalse("a shown list never goes back to loading", loading.any { it })
            assertEquals(listOf("lark:chat:1"), repository.snapshot.value.records.map { it.item.id })

            val held = CompletableDeferred<Unit>()
            gate.set(held)
            entered.set(CompletableDeferred())
            val during = transport.inboxCalls.get()
            hub.events.emit(InboxEvent())
            withTimeout(5_000) { entered.get().await() }
            entered.set(CompletableDeferred())
            hub.events.emit(InboxEvent())
            gate.set(null)
            held.complete(Unit)
            withTimeout(5_000) { entered.get().await() }
            assertEquals("an event during a refresh gets one more refresh", during + 2, settled())
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    // A shown list over a reconnect (the hub goes Connecting and back Online, which refreshes) and over refreshes that
    // fail: the list stays, it never goes back to loading, and a failure is an error over the list rather than a reset.
    @Test fun reconnectsAndFailedRefreshesOverAShownListNeverShowLoading() = runBlocking<Unit> {
        val directory = Files.createTempDirectory("inbox-reconnect-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            val failing = java.util.concurrent.atomic.AtomicBoolean(false)
            val transport = FakeTransport().apply {
                items = JsonArray(listOf(buildJsonObject { put("id", "lark:chat:1"); put("source", "lark"); put("kind", "group")
                    put("title", "群"); put("lark", buildJsonObject { put("feed_id", "chat") }) }))
                onInbox = { delay(50); if (failing.get()) throw IOException("hub went away") }
            }
            val hub = FakeHub()
            val repository = repository(transport, directory, owner, hub, refreshWindowMillis = 100)
            withTimeout(5_000) { repository.snapshot.first { it.contentSource != null } }
            repository.refresh()
            val seen = java.util.Collections.synchronizedList(mutableListOf<InboxSnapshot>())
            owner.launch { repository.snapshot.collect { seen += it } }
            withTimeout(5_000) { repository.snapshot.first { it.refreshes >= 2 } }
            seen.clear()

            repeat(2) {
                hub.state.value = ConnectionState.Connecting
                delay(100)
                hub.state.value = ConnectionState.Online
                val done = repository.snapshot.value.refreshes
                withTimeout(5_000) { repository.snapshot.first { it.refreshes > done } }
            }
            failing.set(true)
            val calls = transport.inboxCalls.get()
            hub.state.value = ConnectionState.Connecting
            delay(100)
            hub.state.value = ConnectionState.Online
            hub.events.emit(InboxEvent())
            withTimeout(5_000) { while (transport.inboxCalls.get() < calls + 2) delay(10) }
            withTimeout(5_000) { repository.snapshot.first { it.error != null } }

            assertFalse("a shown list never goes back to loading", seen.any { it.loading })
            assertTrue("the list stays through reconnects and failures", seen.all { it.records.map { r -> r.item.id } == listOf("lark:chat:1") })
            failing.set(false)
            repository.refresh()
            assertNull(repository.snapshot.value.error)
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    // An empty list still shows that it is loading while the first refresh runs.
    @Test fun emptyListShowsLoadingWhileRefreshing() = runBlocking<Unit> {
        val directory = Files.createTempDirectory("inbox-empty-loading-test").toFile()
        val owner = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        try {
            val started = CompletableDeferred<Unit>()
            val release = CompletableDeferred<Unit>()
            val transport = FakeTransport().apply { onInbox = { started.complete(Unit); release.await() } }
            val repository = repository(transport, directory, owner, FakeHub())
            withTimeout(5_000) { started.await() }
            withTimeout(5_000) { repository.snapshot.first { it.loading } }
            release.complete(Unit)
            withTimeout(5_000) { repository.snapshot.first { !it.loading } }
        } finally {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    private suspend fun awaitState(state: StateFlow<LarkConversation>, predicate: (LarkConversation) -> Boolean) =
        withTimeout(5_000) { state.first(predicate) }

    private fun repository(transport: FakeTransport, directory: java.io.File, owner: CoroutineScope,
        hub: FakeHub, snapshots: ListSnapshotStore? = null, sender: MainConversationSender? = null,
        refreshWindowMillis: Long = 1_000): RealInboxRepository = RealInboxRepository(
        object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) },
        InboxReplyJournal(directory, "hub/generation"),
        object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        },
        object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }, owner, snapshots = snapshots, hubConnection = hub, mainConversationSender = sender,
        refreshWindowMillis = refreshWindowMillis,
    ) { _, _ -> error("unused") }

    private class FakeHub : HubConnection, OpenWindowReconciliationOwner {
        override val openWindowReconciliation = OpenWindowReconciliation()
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private fun row(id: String, position: Long, text: String, clientId: String? = null) = buildJsonObject {
        put("id", id); put("position", position); put("text", text)
        clientId?.let { put("client_id", it) }
    }
    private fun page(vararg rows: JsonObject, newer: String? = null, sync: String = "complete") = buildJsonObject {
        put("messages", JsonArray(rows.toList())); put("sync_state", sync)
        put("has_newer", newer != null); put("has_older", false)
        put("newer_cursor", newer ?: rows.lastOrNull()?.number("position")?.toString())
        put("older_cursor", rows.firstOrNull()?.number("position")?.toString())
    }

    private class FakeTransport(override val scope: String = "hub/generation") : InboxAssistantTransport {
        var onReply: suspend (String) -> JsonObject = { JsonObject(emptyMap()) }
        var items = JsonArray(emptyList())
        val mediaRefs = mutableListOf<String>()
        var onMessages: suspend (Long?, String?, String?) -> JsonObject = { _, _, _ -> buildJsonObject {
            put("messages", JsonArray(emptyList())); put("sync_state", "complete")
        } }
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = onReply(clientId)
        val inboxCalls = java.util.concurrent.atomic.AtomicInteger()
        var onInbox: suspend () -> Unit = {}
        override suspend fun inbox(): JsonObject { inboxCalls.incrementAndGet(); onInbox(); return buildJsonObject { put("items", items) } }
        /** Each single-item done request: the item id it named and the manager its request named (RequestManager). */
        val doneCalls = mutableListOf<Pair<String, String?>>()
        var onSetDone: suspend (String, Boolean) -> JsonObject = { _, _ -> JsonObject(emptyMap()) }
        override suspend fun setDone(itemId: String, done: Boolean): JsonObject {
            doneCalls += itemId to currentCoroutineContext()[RequestManager]?.id
            return onSetDone(itemId, done)
        }
        val bulk = mutableListOf<Pair<List<InboxDoneTarget>, Boolean>>()
        var onBulk: suspend (List<InboxDoneTarget>, Boolean) -> JsonObject = { _, _ -> JsonObject(emptyMap()) }
        override suspend fun setDone(items: List<InboxDoneTarget>, done: Boolean): JsonObject { bulk += items to done; return onBulk(items, done) }
        val actions = mutableListOf<InboxBulkAction>()
        /** Each bulk request: the ids it named and the manager its request named (RequestManager). */
        val bulkManagers = mutableListOf<Pair<List<String>, String?>>()
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): JsonObject {
            bulkManagers += items.map { it.id } to currentCoroutineContext()[RequestManager]?.id
            actions += action
            return setDone(items, action != InboxBulkAction.Reopen)
        }
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = onMessages(around, before, after)
        val lookedUp = mutableListOf<String>()
        var onLarkMessage: suspend (String) -> JsonObject = { throw UnsupportedOperationException() }
        override suspend fun larkMessage(messageId: String): JsonObject { lookedUp += messageId; return onLarkMessage(messageId) }
        override suspend fun assistant() = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean) = JsonObject(emptyMap())
        override suspend fun pins() = JsonObject(emptyMap())
        override suspend fun mutes() = JsonObject(emptyMap())
        override suspend fun media(ref: String) = byteArrayOf(1).also { mediaRefs += ref }
        val avatarRefs = mutableListOf<String>()
        override suspend fun avatar(ref: String) = media(ref).also { avatarRefs += ref }
        var onMute: suspend (String, String) -> JsonObject = { _, _ -> JsonObject(emptyMap()) }
        override suspend fun muteInbox(itemId: String, tag: String) = onMute(itemId, tag)
        /** Each policy request: its method, what it named and the manager its request named (RequestManager). */
        val policyCalls = mutableListOf<String>()
        private suspend fun manager() = currentCoroutineContext()[RequestManager]?.id
        var policies = JsonArray(emptyList())
        override suspend fun inboxPolicies(): JsonObject { policyCalls += "GET ${manager()}"; return buildJsonObject { put("policies", policies) } }
        var onSetPolicy: suspend (String, String, String, String) -> JsonObject = { _, _, _, _ -> JsonObject(emptyMap()) }
        override suspend fun setInboxPolicy(itemId: String, tag: String, tier: String, twin: String): JsonObject {
            policyCalls += "POST $itemId $tag $tier $twin ${manager()}"
            return onSetPolicy(itemId, tag, tier, twin)
        }
        var onUpdatePolicy: suspend (String, String, String) -> JsonObject = { _, _, _ -> JsonObject(emptyMap()) }
        override suspend fun updateInboxPolicy(id: String, tier: String, twin: String): JsonObject {
            policyCalls += "PATCH $id $tier $twin ${manager()}"
            return onUpdatePolicy(id, tier, twin)
        }
        override suspend fun removeInboxPolicy(id: String) { policyCalls += "DELETE $id" }
    }
}
