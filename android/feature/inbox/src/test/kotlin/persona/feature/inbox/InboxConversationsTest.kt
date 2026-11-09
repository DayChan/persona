package persona.feature.inbox

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.domain.inbox.InboxItem

@OptIn(ExperimentalCoroutinesApi::class)
class InboxConversationsTest {
    @Test fun readFyiLeavesTheUnreadGroupsWithoutBecomingPendingOrCompleted() {
        val fyi = item("f", "room", "fyi")
        val read = setOf(InboxReadVersion("f", "v1"))
        val local = buildInboxConversations("paired", InboxSnapshot(records = listOf(fyi)), read).rows.single()
        assertEquals(InboxConversationSection.Read, local.section)
        assertFalse(local.unread)
        assertFalse(local.records.single().item.isDone)
        val remote = buildInboxConversations("paired", InboxSnapshot(records = listOf(fyi.copy(read = true)))).rows.single()
        assertEquals(InboxConversationSection.Read, remote.section)
        val mixed = buildInboxConversations("paired", InboxSnapshot(records = listOf(fyi.copy(read = true), item("a", "room", "action").copy(read = true)))).rows.single()
        assertEquals(InboxConversationSection.ReadPending, mixed.section)
    }

    @Test fun explicitlyOpeningBeyondPrefetchLoadsAndKeepsTheCurrentCompletedConversation() = runTest {
        val records = (1..26).map { item("item-${it.toString().padStart(2, '0')}", "room-${it.toString().padStart(2, '0')}", "fyi") }
        val repository = ConversationRepository(records)
        val controller = InboxConversations(repository, "paired", backgroundScope, ReplyComposers(repository, backgroundScope))
        runCurrent()
        val key = controller.state.value.rows.last().key
        repository.window(key.thread).value = window(message(26))
        runCurrent()
        assertNull(controller.capture(key))
        var opened = LarkConversation()
        backgroundScope.launch { controller.observe(key).collect { opened = it } }
        runCurrent()
        assertTrue(opened.verified)
        assertNotNull(controller.capture(key))
        repository.snapshot.value = InboxSnapshot(records = records.map { record ->
            if (record.feedId == key.thread) record.copy(read = true, item = record.item.copy(doneAt = "2026-11-06T10:00:00Z")) else record
        })
        runCurrent()
        assertEquals(listOf("m26"), opened.messages.map { it.id })
        assertFalse(opened.loading)
    }

    @Test fun managerIdentityAndScopedReadsNeverAcquireAnotherAssistantsRows() = runTest {
        val mine = item("a", "room", "action")
        val other = item("b", "room", "action").copy(manager = "other-assistant")
        val grouped = buildInboxConversations("paired", InboxSnapshot(records = listOf(mine, other)))
        assertEquals(2, grouped.rows.size)
        assertEquals(2, grouped.rows.map { it.key }.distinct().size)
        val repository = ConversationRepository(listOf(mine, other, item("f", "fyi", "fyi")))
        repository.window("room").value = window(message(1))
        val controller = InboxConversations(repository, "paired", backgroundScope, ReplyComposers(repository, backgroundScope))
        runCurrent()
        val key = controller.state.value.rows.single { it.records.single().item.id == "a" }.key
        val read = controller.readAll(setOf(InboxConversationSection.Action), setOf(key))
        runCurrent()
        assertEquals(listOf(key), read.succeeded)
        assertEquals(1, repository.reads.size)
        assertTrue(repository.done.isEmpty())
        assertTrue(repository.snapshot.value.records.none { it.item.isDone })
        val empty = controller.readAll(setOf(InboxConversationSection.Action), emptySet())
        assertTrue(empty.succeeded.isEmpty())
        assertEquals(1, repository.reads.size)
    }

    @Test fun groupsBySourceAccountAndThreadKeepingEveryOriginalItem() {
        val first = item("a", "room", "action")
        val second = item("b", "room", "fyi")
        val otherAccount = item("c", "room", "fyi", account = "other")
        val unknown = item("document", null, "action")
        val groups = buildInboxConversations("paired", InboxSnapshot(records = listOf(first, second, otherAccount, unknown)))
        assertEquals(2, groups.rows.size)
        val action = groups.rows.first { it.section == InboxConversationSection.Action }
        assertEquals(setOf("a", "b"), action.records.map { it.item.id }.toSet())
        assertEquals(listOf(unknown), groups.originalRecords)
        assertNull(action.unreadCount)
        assertNotEquals(action.key, inboxConversationKey("another-pairing", first))
    }

    @Test fun readingMovesActionToPendingAndCompletionKeepsUnreadIndependent() {
        val action = item("a", "room", "action")
        val read = setOf(InboxReadVersion("a", "v1"))
        assertEquals(InboxConversationSection.ReadPending, buildInboxConversations("paired", InboxSnapshot(records = listOf(action)), read).rows.single().section)
        val completed = action.copy(item = action.item.copy(doneAt = "2026-11-06T10:00:00Z"))
        assertEquals(InboxConversationSection.Done, buildInboxConversations("paired", InboxSnapshot(records = listOf(completed))).rows.single().section)
        assertEquals(InboxConversationSection.Done, buildInboxConversations("paired", InboxSnapshot(records = listOf(completed)), read).rows.single().section)
        val newer = action.copy(item = action.item.copy(version = "v2"))
        assertEquals(InboxConversationSection.Action, buildInboxConversations("paired", InboxSnapshot(records = listOf(newer)), read).rows.single().section)
    }

    @Test fun capturesOnlyVerifiedMessagesAndNeverUsesEventCount() = runTest {
        val repository = ConversationRepository(listOf(item("a", "room", "fyi")))
        val controller = InboxConversations(repository, "paired", backgroundScope, ReplyComposers(repository, backgroundScope))
        runCurrent()
        val key = controller.state.value.rows.single().key
        assertNull(controller.capture(key))
        assertNull(controller.state.value.rows.single().unreadCount)
        repository.window("room").value = window(message(1), message(2), mine(3))
        runCurrent()
        assertEquals(2, controller.state.value.rows.single().unreadCount)
        assertEquals(listOf("m1", "m2"), controller.capture(key)!!.messages.map { it.id })
        assertEquals(1, controller.captureFyi().size)
        repository.window("room").value = window(message(1), message(2)).copy(verified = false)
        runCurrent()
        assertNull(controller.capture(key))
    }

    @Test fun successUsesTheRealReceiptAndMovesActionWithoutCompletingIt() = runTest {
        val repository = ConversationRepository(listOf(item("a", "room", "action")))
        repository.window("room").value = window(message(1))
        val composers = ReplyComposers(repository, backgroundScope)
        val controller = InboxConversations(repository, "paired", backgroundScope, composers)
        runCurrent()
        val key = controller.state.value.rows.single().key
        val snapshot = controller.capture(key)!!
        assertSame(composers.get(ReplyTarget.Lark("room")), controller.composer(key))
        assertTrue(controller.read(snapshot))
        runCurrent()
        assertEquals(listOf("room" to listOf("m1")), repository.reads)
        assertEquals(InboxConversationSection.ReadPending, controller.state.value.rows.single().section)
        assertFalse(repository.snapshot.value.records.single().item.isDone)
        assertTrue(repository.done.isEmpty())
    }

    @Test fun failurePreservesUnreadAndRetryUsesCapturedMessagesOnly() = runTest {
        val repository = ConversationRepository(listOf(item("a", "room", "fyi")))
        repository.window("room").value = window(message(1))
        val controller = InboxConversations(repository, "paired", backgroundScope, ReplyComposers(repository, backgroundScope))
        runCurrent()
        val snapshot = controller.captureFyi()
        repository.accepted = false
        val failed = controller.readFyi(snapshot)
        assertEquals(snapshot, failed.failed)
        runCurrent()
        assertTrue(controller.state.value.rows.single().unread)
        repository.accepted = true
        val retried = controller.readFyi(failed.failed)
        assertEquals(1, retried.succeeded.size)
        runCurrent()
        assertEquals(InboxConversationSection.Read, controller.state.value.rows.single().section)
        assertFalse(controller.state.value.rows.single().records.single().item.isDone)
        assertEquals(listOf("m1"), repository.reads.last().second)
        assertTrue(repository.done.isEmpty())
    }

    @Test fun snapshotNeverConsumesNewMessageArrivingWhileTheReceiptWaits() = runTest {
        val repository = ConversationRepository(listOf(item("a", "room", "fyi")))
        repository.window("room").value = window(message(1))
        repository.gate = CompletableDeferred()
        val controller = InboxConversations(repository, "paired", backgroundScope, ReplyComposers(repository, backgroundScope))
        runCurrent()
        val snapshot = controller.captureFyi().single()
        val first = async { controller.read(snapshot) }
        val duplicate = async { controller.read(snapshot) }
        runCurrent()
        assertEquals(1, repository.reads.size)
        repository.window("room").value = window(message(1), message(2))
        runCurrent()
        repository.gate!!.complete(Unit)
        assertTrue(first.await())
        assertTrue(duplicate.await())
        runCurrent()
        assertTrue(controller.state.value.rows.single().unread)
        assertNotEquals(snapshot.version, controller.state.value.rows.single().version)
        assertEquals(listOf("m1"), repository.reads.single().second)
    }

    @Test fun changedItemVersionRejectsStaleCacheAndBulkSkipsIt() = runTest {
        val repository = ConversationRepository(listOf(item("a", "room", "fyi")))
        repository.window("room").value = window(message(1))
        val controller = InboxConversations(repository, "paired", backgroundScope, ReplyComposers(repository, backgroundScope))
        runCurrent()
        val snapshot = controller.captureFyi().single()
        repository.snapshot.value = InboxSnapshot(records = listOf(item("a", "room", "fyi", version = "v2")))
        runCurrent()
        assertNull(controller.capture(snapshot.key))
        assertEquals(listOf(snapshot.key), controller.readFyi(listOf(snapshot)).skipped)
        assertTrue(repository.reads.isEmpty())
    }

    @Test fun bulkDoesNotTouchActionOtherScopesOrUnsupportedItems() = runTest {
        val repository = ConversationRepository(listOf(item("f", "fyi", "fyi"), item("a", "action", "action"), item("doc", null, "fyi")))
        repository.window("fyi").value = window(message(1))
        repository.window("action").value = window(message(2))
        val controller = InboxConversations(repository, "paired", backgroundScope, ReplyComposers(repository, backgroundScope))
        runCurrent()
        val captured = controller.captureFyi()
        assertEquals(listOf("fyi"), captured.map { it.key.thread })
        val wrongScope = captured.single().copy(key = captured.single().key.copy(scope = "other"))
        assertFalse(controller.read(wrongScope))
        assertEquals(1, controller.readFyi(captured).succeeded.size)
        assertEquals(listOf("fyi"), repository.reads.map { it.first })
        assertTrue(repository.done.isEmpty())
    }

    @Test fun partialViewportReadsOnlyItsMessagesAndLeavesTheConversationUnread() = runTest {
        val repository = ConversationRepository(listOf(item("a", "room", "action")))
        repository.window("room").value = window(message(1), message(2))
        val controller = InboxConversations(repository, "paired", backgroundScope, ReplyComposers(repository, backgroundScope))
        runCurrent()
        val snapshot = controller.capture(controller.state.value.rows.single().key, listOf(message(1)))!!
        assertFalse(snapshot.complete)
        assertTrue(controller.read(snapshot))
        runCurrent()
        assertTrue(controller.state.value.rows.single().unread)
    }
}

private fun item(id: String, feed: String?, tier: String, account: String = "account", version: String = "v1") = InboxRecord(
    InboxItem(id, account, "group", title = "Conversation", preview = "Message", tier = tier, version = version),
    source = "lark", feedId = feed, count = 999,
)
private fun message(position: Long) = LarkMessage("m$position", "sender", "Sender", position, parts = listOf(LarkPart.Text("Message $position")))
private fun mine(position: Long) = message(position).copy(mine = true)
private fun window(vararg messages: LarkMessage) = LarkConversation(messages = messages.toList(), verified = true)

private class ConversationRepository(records: List<InboxRecord>) : InboxRepository by FakeInboxRepository() {
    override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
    private val windows = mutableMapOf<String, MutableStateFlow<LarkConversation>>()
    fun window(feed: String) = windows.getOrPut(feed) { MutableStateFlow(LarkConversation(loading = true)) }
    override fun conversation(feedId: String, startPosition: Long?) = window(feedId)
    val reads = mutableListOf<Pair<String, List<String>>>()
    val done = mutableListOf<String>()
    var accepted = true
    var gate: CompletableDeferred<Unit>? = null
    override suspend fun markVisible(feedId: String, messages: List<LarkMessage>): Boolean {
        reads += feedId to messages.map { it.id }
        gate?.await()
        return accepted
    }
    override suspend fun setDone(itemId: String, done: Boolean) { this.done += itemId }
}
