package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.*
import java.io.File

/** Real pointer gestures through the grouped production renderer; all records and writes are synthetic. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class InboxConversationSwipeTest {
    @get:Rule val rule = createComposeRule()
    private val owner = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val opened = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    @After fun stop() { owner.cancel() }

    private fun record(id: String) = InboxRecord(
        InboxItem(id, account = "demo", title = "Synthetic conversation", preview = "Synthetic preview",
            kind = "dm", tier = "action", version = "version-$id"),
        source = "lark", feedId = "synthetic-room", read = true,
    )

    private class FakeInbox(records: List<InboxRecord>, query: String = "") : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        override val connectionState = MutableStateFlow(ConnectionState.Online)
        override val preferences = MutableStateFlow(InboxPreferences(query = query, expanded = listOf("action", "fyi", "read-pending", "done")))
        override suspend fun savePreferences(value: InboxPreferences) { preferences.value = value }
        val reads = mutableListOf<String>()
        var failRead = false
        override suspend fun markLatestRead(feedId: String): Boolean {
            reads += feedId
            if (failRead) throw IllegalStateException("Synthetic read failure")
            snapshot.value = snapshot.value.copy(records = snapshot.value.records.map { if (it.feedId == feedId) it.copy(read = true) else it })
            return true
        }
        override val conversationInbox = MutableStateFlow<InboxConversations?>(null)
        val calls = mutableListOf<Pair<List<InboxDoneTarget>, InboxBulkAction>>()
        val handoffs = mutableListOf<InboxHandOffRequest>()
        override suspend fun handOff(request: InboxHandOffRequest): SessionKey? {
            handoffs += request
            throw IllegalStateException("Synthetic handoff failure")
        }
        var response: suspend (List<InboxDoneTarget>, InboxBulkAction) -> InboxBulkResult = { items, action ->
            val ids = items.map { it.id }
            snapshot.value = snapshot.value.copy(records = snapshot.value.records.map { record ->
                if (record.item.id in ids) record.copy(item = record.item.copy(
                    doneAt = if (action == InboxBulkAction.Reopen) null else "2026-11-08T00:00:00Z")) else record
            })
            InboxBulkResult(changed = ids)
        }
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
            calls += items to action
            return response(items, action)
        }
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = error("Grouped rows must use versioned bulk")
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = error("A swipe must never send a reply")
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private fun screen(inbox: FakeInbox) {
        inbox.conversationInbox.value = InboxConversations(inbox, "synthetic-pairing", owner, ReplyComposers(inbox, owner))
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxScreen(inbox, navigator) } }
        rule.waitForIdle()
    }
    private fun row() = rule.onNode(hasText("Synthetic conversation") and hasClickAction())
    private fun swipe(right: Boolean) {
        row().performTouchInput { if (right) swipeRight() else swipeLeft() }
        rule.waitForIdle()
    }

    @Test fun completingUnreadConversationLeavesOpenSectionsAndSurvivesRefresh() {
        val inbox = FakeInbox(listOf(record("a").copy(read = false), record("b").copy(read = false)))
        screen(inbox)
        swipe(true)
        rule.runOnIdle {
            assertTrue(inbox.snapshot.value.records.all { it.item.isDone && !it.read })
            assertEquals(InboxConversationSection.Done, inbox.conversationInbox.value!!.state.value.rows.single().section)
        }
        rule.onNodeWithText("Needs you", substring = true).performClick()
        rule.onNodeWithText("Synthetic conversation").assertDoesNotExist()
        rule.onNodeWithText("Done", substring = true).performClick()
        row().assertExists()
        rule.runOnIdle {
            // Rebuild from the persisted server snapshot rather than retaining the old controller's projection.
            inbox.conversationInbox.value = InboxConversations(inbox, "synthetic-pairing", owner, ReplyComposers(inbox, owner))
        }
        rule.waitForIdle()
        row().assertExists()
    }

    @Test fun failedUnreadCompletionRestoresTheOpenRowAndReportsFailure() {
        val inbox = FakeInbox(listOf(record("a").copy(read = false)))
        inbox.response = { _, _ -> throw IllegalStateException("Synthetic write failure") }
        screen(inbox)
        swipe(true)
        row().assertExists()
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        rule.runOnIdle {
            assertFalse(inbox.snapshot.value.records.single().item.isDone)
            assertEquals(InboxConversationSection.Action, inbox.conversationInbox.value!!.state.value.rows.single().section)
            assertEquals(1, inbox.calls.size)
        }
    }

    @Test fun needsYouReadAllWorksFoldedAndExpandedWithoutFilingAndRetriesFailure() {
        val inbox = FakeInbox(listOf(record("a").copy(read = false),
            record("fyi").let { it.copy(feedId = "other-room", read = false, item = it.item.copy(tier = "fyi", title = "Other section")) }))
        inbox.preferences.value = inbox.preferences.value.copy(expanded = emptyList())
        screen(inbox)
        row().assertDoesNotExist()
        rule.onAllNodesWithText("Mark all as read").assertCountEquals(2)
        inbox.failRead = true
        rule.onAllNodesWithText("Mark all as read")[0].performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Some messages could not be marked as read. Try again.").assertExists()
        rule.runOnIdle { assertTrue(inbox.calls.isEmpty()); assertTrue(inbox.snapshot.value.records.none { it.item.isDone || it.read }) }
        inbox.failRead = false
        rule.onNodeWithText("Retry").performClick()
        rule.waitForIdle()
        rule.runOnIdle {
            assertEquals(listOf("synthetic-room", "synthetic-room"), inbox.reads)
            assertTrue(inbox.snapshot.value.records.first().read)
            assertFalse(inbox.snapshot.value.records.last().read)
            assertTrue(inbox.calls.isEmpty())
        }
    }

    @Test fun expandedNeedsYouReadsOnlyUnreadConversationsAndLeavesOtherAssistantUntouched() {
        val inbox = FakeInbox(listOf(record("a").copy(read = false),
            record("already-read").copy(feedId = "read-room"),
            record("other").copy(feedId = "other-room", read = false, manager = "other-assistant")))
        inbox.conversationInbox.value = InboxConversations(inbox, "synthetic-pairing", owner, ReplyComposers(inbox, owner))
        val roster = ManagerRoster(listOf(HubManager("assistant", "Assistant", online = true, isDefault = true),
            HubManager("other-assistant", "Other assistant", online = true)), "assistant")
        rule.setContent { PersonaTheme(reduceMotion = true) {
            CompositionLocalProvider(LocalManagerRoster provides roster) { InboxScreen(inbox, navigator) }
        } }
        rule.waitForIdle()
        rule.onAllNodesWithText("Mark all as read").assertCountEquals(2)
        rule.onAllNodesWithText("Mark all as read")[0].performClick()
        rule.waitForIdle()
        rule.runOnIdle {
            assertEquals(listOf("synthetic-room"), inbox.reads)
            assertTrue(inbox.snapshot.value.records[0].read)
            assertTrue(inbox.snapshot.value.records[1].read)
            assertFalse(inbox.snapshot.value.records[2].read)
            assertTrue(inbox.snapshot.value.records.none { it.item.isDone })
            assertTrue(inbox.calls.isEmpty())
        }
        rule.onAllNodesWithText("Mark all as read").assertCountEquals(1)
    }

    @Test fun needsYouReadControlIsVisibleInBothFoldStatesAcrossAllStyles() {
        val inbox = FakeInbox(listOf(record("a").copy(read = false),
            record("fyi").let { it.copy(feedId = "other-room", read = false, item = it.item.copy(tier = "fyi", title = "Other section")) }))
        inbox.conversationInbox.value = InboxConversations(inbox, "synthetic-pairing", owner, ReplyComposers(inbox, owner))
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent { PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            Box(Modifier.testTag("inbox-state-page")) { InboxScreen(inbox, navigator) }
        } }
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (folded in listOf(false, true)) {
            rule.runOnIdle {
                style = s; dark = d
                inbox.preferences.value = inbox.preferences.value.copy(expanded = if (folded) emptyList() else listOf("action", "fyi"))
            }
            rule.waitForIdle()
            rule.onAllNodesWithText("Mark all as read").assertCountEquals(2)
            val dir = File("build/reports/tests/inbox-state-shots").apply { mkdirs() }
            File(dir, "${s.name}-${if (d) "dark" else "light"}-${if (folded) "folded" else "expanded"}.png").outputStream().use {
                assertTrue(rule.onNodeWithTag("inbox-state-page").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
    }

    @Test fun rightSwipeFilesEveryDisplayedVersionThenReopensWithTheSameSemantics() {
        val inbox = FakeInbox(listOf(record("a"), record("b")))
        screen(inbox)
        swipe(true)
        rule.runOnIdle {
            assertEquals(1, inbox.calls.size)
            assertEquals(setOf(InboxDoneTarget("a", "version-a"), InboxDoneTarget("b", "version-b")), inbox.calls[0].first.toSet())
            assertEquals(InboxBulkAction.Done, inbox.calls[0].second)
            assertTrue(inbox.snapshot.value.records.all { it.item.isDone })
            assertEquals(InboxConversationSection.Done, inbox.conversationInbox.value!!.state.value.rows.single().section)
        }
        swipe(true)
        rule.runOnIdle {
            assertEquals(InboxBulkAction.Reopen, inbox.calls.last().second)
            assertTrue(inbox.snapshot.value.records.none { it.item.isDone })
            assertEquals(InboxConversationSection.ReadPending, inbox.conversationInbox.value!!.state.value.rows.single().section)
        }
    }

    @Test fun filteredSwipeCannotFileHiddenItemsInTheSameConversation() {
        val inbox = FakeInbox(listOf(record("a").let { it.copy(item = it.item.copy(preview = "Visible match")) },
            record("b").let { it.copy(item = it.item.copy(preview = "Hidden event")) }), query = "Visible")
        screen(inbox)
        swipe(true)
        rule.runOnIdle {
            assertEquals(listOf(InboxDoneTarget("a", "version-a")), inbox.calls.single().first)
            assertFalse(inbox.snapshot.value.records.single { it.item.id == "b" }.item.isDone)
        }
    }

    @Test fun filingAMixedConversationDoesNotFileAlreadyCompletedEventsAgain() {
        val inbox = FakeInbox(listOf(record("a"), record("b").let { it.copy(item = it.item.copy(doneAt = "2026-11-08T00:00:00Z")) }))
        screen(inbox)
        swipe(true)
        rule.runOnIdle { assertEquals(listOf(InboxDoneTarget("a", "version-a")), inbox.calls.single().first) }
    }

    @Test fun anUpdatedVersionCancelsAnInFlightGestureRatherThanActingOnUnseenContent() {
        var record by mutableStateOf(record("a"))
        var count = 0
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                InboxConversationItemRow(buildInboxConversations("scope", InboxSnapshot(records = listOf(record))).rows.single(),
                    navigator, GroupPosition.of(0, 1), null, false, false, { count++ }, { count++ })
            }
        }
        row().performTouchInput {
            down(center)
            moveTo(center + androidx.compose.ui.geometry.Offset(width * .35f, 0f), 500)
        }
        rule.runOnIdle { record = record.copy(item = record.item.copy(version = "new-version")) }
        row().performTouchInput {
            moveTo(center + androidx.compose.ui.geometry.Offset(width * .9f, 0f), 500)
            up()
        }
        rule.waitForIdle()
        rule.runOnIdle { assertEquals("A changed event needs a new deliberate gesture", 0, count) }
    }

    @Test fun partialFailureKeepsTheFailedEventOpenAndARetryOnlyAsksForThatEvent() {
        val inbox = FakeInbox(listOf(record("a"), record("b")))
        inbox.response = { _, _ ->
            inbox.snapshot.value = inbox.snapshot.value.copy(records = inbox.snapshot.value.records.map {
                if (it.item.id == "a") it.copy(item = it.item.copy(doneAt = "2026-11-08T00:00:00Z")) else it
            })
            InboxBulkResult(changed = listOf("a"), failed = listOf("b"))
        }
        screen(inbox)
        swipe(true)
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        rule.runOnIdle {
            assertEquals(InboxConversationSection.ReadPending, inbox.conversationInbox.value!!.state.value.rows.single().section)
        }
        swipe(true)
        rule.runOnIdle {
            assertEquals(2, inbox.calls.size)
            assertEquals(listOf(InboxDoneTarget("b", "version-b")), inbox.calls.last().first)
        }
    }

    @Test fun leftSwipeOpensTheExistingConsentSheetWithoutExecutingOrFiling() {
        val inbox = FakeInbox(listOf(record("a")))
        screen(inbox)
        swipe(false)
        rule.onNodeWithText("What should it do").assertExists()
        rule.runOnIdle { assertTrue(inbox.calls.isEmpty()); assertTrue(opened.isEmpty()) }
    }

    @Test fun filteredLeftSwipeUsesTheVisibleItemOnlyAfterConsentAndKeepsTheRetryOperationId() {
        val inbox = FakeInbox(listOf(record("a").let { it.copy(item = it.item.copy(preview = "Hidden event")) },
            record("b").let { it.copy(item = it.item.copy(preview = "Visible match")) }), query = "Visible")
        screen(inbox)
        swipe(false)
        rule.onNodeWithText("What should it do").assertExists()
        rule.runOnIdle { assertTrue(inbox.handoffs.isEmpty()); assertTrue(inbox.calls.isEmpty()) }
        rule.onNodeWithText("Send").performClick()
        rule.onNodeWithText("Couldn't hand off:", substring = true).assertExists()
        rule.runOnIdle {
            assertEquals("b", inbox.handoffs.single().itemId)
            assertTrue(inbox.calls.isEmpty())
        }
        rule.onNodeWithText("Send").performClick()
        rule.runOnIdle {
            assertEquals(2, inbox.handoffs.size)
            assertEquals(inbox.handoffs.first().operationId, inbox.handoffs.last().operationId)
            assertTrue(inbox.snapshot.value.records.none { it.item.isDone })
        }
    }

    @Test fun pendingBlocksBothDirectionsAndAFailureRestoresTheRowAndReportsIt() {
        val inbox = FakeInbox(listOf(record("a")))
        val result = CompletableDeferred<InboxBulkResult>()
        inbox.response = { _, _ -> result.await() }
        screen(inbox)
        swipe(true)
        swipe(true)
        swipe(false)
        rule.runOnIdle { assertEquals(1, inbox.calls.size); assertTrue("Pending swipes must not navigate", opened.isEmpty()) }
        rule.runOnIdle { result.completeExceptionally(IllegalStateException("Synthetic failure")) }
        rule.waitForIdle()
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        row().assertExists()
        rule.runOnIdle { assertFalse(inbox.snapshot.value.records.single().item.isDone) }
        inbox.response = { items, _ -> InboxBulkResult(failed = items.map { it.id }) }
        swipe(true)
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        rule.runOnIdle { assertEquals(2, inbox.calls.size) }
        inbox.response = { _, _ -> throw UnsupportedOperationException("Synthetic unsupported operation") }
        swipe(true)
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        rule.runOnIdle { assertEquals(3, inbox.calls.size); assertFalse(inbox.snapshot.value.records.single().item.isDone) }

    }

    @Test fun tapsShortHorizontalDragsAndVerticalScrollDoNotExecuteActions() {
        val inbox = FakeInbox(listOf(record("a")))
        screen(inbox)
        row().performTouchInput { swipeUp() }
        rule.runOnIdle { assertTrue(inbox.calls.isEmpty()); assertTrue(opened.isEmpty()) }
        row().performTouchInput { swipe(center, center + androidx.compose.ui.geometry.Offset(8f, 0f), 500) }
        rule.runOnIdle { assertTrue(inbox.calls.isEmpty()) }
        row().performClick()
        rule.runOnIdle { assertEquals(1, opened.size); assertTrue(inbox.calls.isEmpty()) }
    }

    private fun drag(fraction: Float, release: Boolean = true) {
        row().performTouchInput {
            val origin = center
            down(origin)
            repeat(60) { step -> moveTo(origin + androidx.compose.ui.geometry.Offset(width * fraction * (step + 1) / 60, 0f), 16) }
            if (release) up() else cancel()
        }
        rule.waitForIdle()
    }

    @Test fun slowDragsHonorTheHistoricalThresholdAndCancelSpringsBackInBothDirections() {
        val inbox = FakeInbox(listOf(record("a")))
        inbox.response = { items, _ -> InboxBulkResult(failed = items.map { it.id }) }
        screen(inbox)
        val origin = row().fetchSemanticsNode().boundsInRoot.left
        for (direction in listOf(1f, -1f)) {
            drag(direction * .10f) // 40 dp: below the shared component's 56 dp threshold.
            rule.runOnIdle { assertTrue(inbox.calls.isEmpty()); assertTrue(opened.isEmpty()) }
            rule.onNodeWithText("What should it do").assertDoesNotExist()
            assertEquals(origin, row().fetchSemanticsNode().boundsInRoot.left, 1f)
            drag(direction * .40f, release = false)
            rule.runOnIdle { assertTrue(inbox.calls.isEmpty()); assertTrue(opened.isEmpty()) }
            rule.onNodeWithText("What should it do").assertDoesNotExist()
            assertEquals(origin, row().fetchSemanticsNode().boundsInRoot.left, 1f)
        }
        drag(.30f) // 120 dp: over the threshold, including touch slop.
        rule.runOnIdle { assertEquals(1, inbox.calls.size) }
        assertEquals(origin, row().fetchSemanticsNode().boundsInRoot.left, 1f)
        drag(-.30f)
        rule.onNodeWithText("What should it do").assertExists()
    }

    @Test fun emptyRowsHaveNoGestureOrActionAndEveryStyleDrawsBothDirections() {
        var records by mutableStateOf(emptyList<InboxRecord>())
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var count = 0
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.fillMaxWidth().testTag("shot").background(PersonaTheme.palette.background)) {
                    InboxConversationItemRow(InboxConversationRow(InboxConversationKey("scope", "lark", "demo", "room"),
                        "Synthetic conversation", records, InboxConversationSection.Action, "v1"), navigator,
                        GroupPosition.of(0, 1), null, false, false, { count++ }, { count++ })
                }
            }
        }
        rule.onNode(hasText("Synthetic conversation") and hasClickAction()).assertDoesNotExist()
        records = listOf(record("a"))
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            for (right in listOf(true, false)) {
                row().performTouchInput {
                    down(center)
                    moveTo(center + androidx.compose.ui.geometry.Offset(if (right) width * .08f else -width * .08f, 0f), 100)
                    moveTo(center + androidx.compose.ui.geometry.Offset(if (right) width * .35f else -width * .35f, 0f), 500)
                }
                rule.waitForIdle()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val dir = File("build/reports/tests/inbox-swipe-shots").also { it.mkdirs() }
                File(dir, "${s.name}-${if (d) "dark" else "light"}-${if (right) "right" else "left"}.png").outputStream().use {
                    assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
                }
                row().performTouchInput { cancel() }
                rule.waitForIdle()
            }
        }
        assertEquals(0, count)
    }
}
